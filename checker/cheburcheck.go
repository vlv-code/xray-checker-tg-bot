package checker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xray-checker/logger"
)

// ErrCheburInactive is returned when no scanners are configured (the public
// repository default): the whole feature stays invisible instead of probing.
var ErrCheburInactive = errors.New("сканеры cheburcheck не настроены")

// ScannerCred names one Cheburcheck scanner addressed by its own token.
type ScannerCred struct{ Name, Token string }

// DefaultCheburScanners is intentionally EMPTY: repo is public, tokens are
// pasted at deploy time. Examples (do not enable):
// {"msk-1", "<scanner-token>"},
var DefaultCheburScanners []ScannerCred

const defaultCheburBaseURL = "https://cheburcheck.ru/api/v1"

// cheburBlockKinds lists the blocking verdicts in their canonical order for
// stable verdict strings. Note: passive registry data from /check (blocked,
// ips, geo) is never read — only active probe verdicts surface anywhere.
var cheburBlockKinds = []string{"cdn_block", "sni_block", "dns_spoofing", "tspu_block"}

func isCheburBlockKind(v string) bool {
	for _, k := range cheburBlockKinds {
		if v == k {
			return true
		}
	}
	return false
}

// CheburHTTPError is a typed pre-stream error from the cheburcheck API
// (JSON {code, info} bodies: 403 no key, 404 bad token, 429 rate limit...).
type CheburHTTPError struct {
	Op   string // "check" or "probe"
	Code int
	Info string
}

func (e *CheburHTTPError) Error() string {
	if e.Info != "" {
		return fmt.Sprintf("cheburcheck %s: HTTP %d: %s", e.Op, e.Code, e.Info)
	}
	return fmt.Sprintf("cheburcheck %s: HTTP %d", e.Op, e.Code)
}

// CheburcheckClient probes RF blocking via cheburcheck.ru: one signed-id /check
// call, then one SSE stream per scanner token. No API key is used — each
// scanner is addressed by its own token only.
type CheburcheckClient struct {
	BaseURL    string
	HTTPClient *http.Client
	scanners   []ScannerCred // active entries only (non-empty Name AND Token)
	limiter    *rateLimiter
}

// NewCheburcheckClient creates a client; baseURL "" → production default,
// timeout ≤0 → 15s. Scanner entries with empty Name or Token are dropped so a
// half-filled credential never produces real requests.
func NewCheburcheckClient(baseURL string, timeout time.Duration, scanners []ScannerCred) *CheburcheckClient {
	if baseURL == "" {
		baseURL = defaultCheburBaseURL
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	active := make([]ScannerCred, 0, len(scanners))
	for _, s := range scanners {
		if s.Name != "" && s.Token != "" {
			active = append(active, ScannerCred{Name: s.Name, Token: s.Token})
		}
	}
	return &CheburcheckClient{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: timeout},
		scanners:   active,
		// Cheburcheck rate limits are undocumented — stay conservative.
		limiter: newRateLimiter(1.0, 2),
	}
}

// HasActiveScanners reports whether at least one scanner credential is usable.
func (c *CheburcheckClient) HasActiveScanners() bool {
	return c != nil && len(c.scanners) > 0
}

// CheburProbeResult is one scanner's active-probe outcome.
type CheburProbeResult struct {
	ProbeID  string   `json:"probe_id,omitempty"`
	Name     string   `json:"name"`
	Region   string   `json:"region,omitempty"`
	Provider string   `json:"provider,omitempty"`
	ASN      string   `json:"asn,omitempty"`
	Verdicts []string `json:"verdicts"`
	Cached   bool     `json:"cached,omitempty"`
}

// CheburcheckSummary aggregates the active probe verdicts for one target.
type CheburcheckSummary struct {
	Target         string              `json:"target"`
	ID             string              `json:"id,omitempty"`
	Results        []CheburProbeResult `json:"results,omitempty"`
	Verdict        string              `json:"verdict,omitempty"`
	OKCount        int                 `json:"ok_count,omitempty"`
	BlockedCount   int                 `json:"blocked_count,omitempty"`
	UncertainCount int                 `json:"uncertain_count,omitempty"`
}

// cheburFlexStr accepts both JSON strings and bare number tokens (e.g. asn),
// normalizing everything to a string.
type cheburFlexStr string

func (f *cheburFlexStr) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if len(s) >= 2 && s[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = cheburFlexStr(v)
		return nil
	}
	*f = cheburFlexStr(s)
	return nil
}

// cheburSSEPayload covers started/result/done event bodies; unknown extra
// fields are ignored by encoding/json.
type cheburSSEPayload struct {
	ID            string        `json:"id"`
	Target        string        `json:"target"`
	OnlineProbes  int           `json:"online_probes"`
	JobID         string        `json:"job_id"`
	ProbeID       string        `json:"probe_id"`
	Cached        bool          `json:"cached"`
	Verdicts      []string      `json:"verdicts"`
	Region        string        `json:"region"`
	Provider      string        `json:"provider"`
	ASN           cheburFlexStr `json:"asn"`
	Status        string        `json:"status"`
	ResponseCount int           `json:"response_count"`
}

// parseCheburError converts a non-200 JSON {code,info} body into a typed error.
func parseCheburError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	var payload struct {
		Code int    `json:"code"`
		Info string `json:"info"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && (payload.Code != 0 || payload.Info != "") {
		return &CheburHTTPError{Op: op, Code: payload.Code, Info: payload.Info}
	}
	return &CheburHTTPError{Op: op, Code: resp.StatusCode}
}

// CheckTarget runs the active-probe flow: one unauthenticated /check call to
// obtain the signed id (passive registry fields are deliberately ignored),
// then one SSE probe stream per configured scanner, sequentially. Single
// scanner failures are tolerated (warn + skip); 429 aborts immediately as a
// typed error; if every scanner fails, the last cause is returned.
func (c *CheburcheckClient) CheckTarget(ctx context.Context, host string) (*CheburcheckSummary, error) {
	if !c.HasActiveScanners() {
		return nil, ErrCheburInactive
	}

	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		host = h
	}
	if host == "" {
		return nil, fmt.Errorf("cheburcheck: пустой хост")
	}

	// 1. Signed id only — the passive registry payload (blocked/ips/geo) is
	// never read beyond the id field.
	q := url.Values{}
	q.Set("target", host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/check?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("cheburcheck: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cheburcheck /check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, parseCheburError("check", resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("cheburcheck /check: чтение ответа: %w", err)
	}
	var checkResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &checkResp); err != nil {
		return nil, fmt.Errorf("cheburcheck /check: разбор ответа: %w", err)
	}
	if checkResp.ID == "" {
		return nil, fmt.Errorf("cheburcheck: пустой id")
	}

	// 2. Per-scanner SSE streams, sequentially.
	summary := &CheburcheckSummary{Target: host, ID: checkResp.ID}
	var lastErr error
	answered := 0
	for _, sc := range c.scanners {
		results, err := c.probeScanner(ctx, checkResp.ID, sc)
		if err != nil {
			var httpErr *CheburHTTPError
			if errors.As(err, &httpErr) && httpErr.Code == http.StatusTooManyRequests {
				logger.Warn("cheburcheck: rate limited on scanner %s: %v", sc.Name, err)
				return nil, err
			}
			lastErr = err
			logger.Warn("cheburcheck: scanner %s failed, skipping: %v", sc.Name, err)
			continue
		}
		answered++
		summary.Results = append(summary.Results, results...)
	}

	if answered == 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("cheburcheck: все сканеры вернули ошибку: %w", lastErr)
		}
		return nil, fmt.Errorf("cheburcheck: нет ответов от сканеров")
	}

	c.aggregate(summary)
	return summary, nil
}

// probeScanner consumes one scanner's SSE stream until done, ctx cancellation
// or server-side close, mapping result events into CheburProbeResult.
func (c *CheburcheckClient) probeScanner(ctx context.Context, id string, sc ScannerCred) ([]CheburProbeResult, error) {
	probeURL := fmt.Sprintf("%s/probe/%s?token=%s", c.BaseURL, url.PathEscape(id), url.QueryEscape(sc.Token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return nil, fmt.Errorf("сканер %s: %w", sc.Name, err)
	}
	req.Header.Set("Accept", "text/event-stream")

	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сканер %s: %w", sc.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, parseCheburError("probe", resp)
	}

	var results []CheburProbeResult
	sawDone := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var evName string
	var dataLines []string
	dispatch := func() {
		if evName == "" || len(dataLines) == 0 {
			evName, dataLines = "", nil
			return
		}
		var payload cheburSSEPayload
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &payload); err != nil {
			logger.Warn("cheburcheck: scanner %s sent malformed %s event: %v", sc.Name, evName, err)
		} else if evName == "result" {
			results = append(results, CheburProbeResult{
				ProbeID:  payload.ProbeID,
				Name:     sc.Name,
				Region:   payload.Region,
				Provider: payload.Provider,
				ASN:      string(payload.ASN),
				Verdicts: append([]string(nil), payload.Verdicts...),
				Cached:   payload.Cached,
			})
		} else if evName == "done" {
			sawDone = true
		}
		evName, dataLines = "", nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			dispatch()
			if sawDone {
				return results, nil
			}
		case strings.HasPrefix(line, "event:"):
			evName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	dispatch() // flush a trailing event cut off by the stream end

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("сканер %s: %w", sc.Name, ctx.Err())
		}
		// Stream cut mid-flight: collected results are still valid, an
		// empty collection counts as a scanner failure.
		if len(results) > 0 {
			logger.Warn("cheburcheck: scanner %s stream ended without done: %v", sc.Name, err)
			return results, nil
		}
		return nil, fmt.Errorf("сканер %s: поток завершён без результатов: %w", sc.Name, err)
	}

	if !sawDone && len(results) == 0 {
		return nil, fmt.Errorf("сканер %s: поток завершён без результатов", sc.Name)
	}
	if !sawDone {
		logger.Warn("cheburcheck: scanner %s stream ended without done event", sc.Name)
	}
	return results, nil
}

// aggregate classifies each answered scanner and builds the RU verdict line.
func (c *CheburcheckClient) aggregate(s *CheburcheckSummary) {
	distinct := make(map[string]bool)
	for _, r := range s.Results {
		hasBlock, hasOK := false, false
		for _, v := range r.Verdicts {
			if isCheburBlockKind(v) {
				hasBlock = true
				distinct[v] = true
			}
			if v == "ok" {
				hasOK = true
			}
		}
		switch {
		case hasBlock:
			s.BlockedCount++
		case hasOK:
			s.OKCount++
		default:
			s.UncertainCount++
		}
	}

	n := len(s.Results)
	switch {
	case s.BlockedCount > 0:
		kinds := make([]string, 0, len(distinct))
		for _, k := range cheburBlockKinds {
			if distinct[k] {
				kinds = append(kinds, k)
			}
		}
		s.Verdict = fmt.Sprintf("⛔ блокировка: %s — %d из %d сканеров", strings.Join(kinds, ", "), s.BlockedCount, n)
	case s.OKCount == n && n > 0:
		s.Verdict = fmt.Sprintf("✅ все сканеры видят доступность (%d/%d)", n, n)
	default:
		s.Verdict = fmt.Sprintf("❓ неопределённый результат (%d ответов)", n)
	}
}

// CheburVerdictLabel maps a raw verdict kind to a RU label; unknown kinds are
// returned as-is (callers must escape them before HTML rendering).
func CheburVerdictLabel(v string) string {
	switch v {
	case "ok":
		return "доступен"
	case "uncertain":
		return "неопределённо"
	case "cdn_block":
		return "блокировка CDN"
	case "sni_block":
		return "SNI-блокировка"
	case "dns_spoofing":
		return "подмена DNS"
	case "tspu_block":
		return "ТСПУ-блокировка"
	case "whitelist":
		return "белый список"
	default:
		return v
	}
}

// FormatCheburcheckReport renders the summary as Telegram HTML; every external
// string is escaped exactly once.
func FormatCheburcheckReport(s *CheburcheckSummary) string {
	if s == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🇷🇺 <b>Активный пробинг Cheburcheck:</b> <code>%s</code>\n", html.EscapeString(s.Target)))

	for _, r := range s.Results {
		icon := "❓"
		hasBlock, hasOK := false, false
		for _, v := range r.Verdicts {
			if isCheburBlockKind(v) {
				hasBlock = true
			}
			if v == "ok" {
				hasOK = true
			}
		}
		switch {
		case hasBlock:
			icon = "⛔"
		case hasOK:
			icon = "✅"
		}

		meta := ""
		switch {
		case r.Region != "" && r.Provider != "":
			meta = fmt.Sprintf(" (%s/%s)", html.EscapeString(r.Region), html.EscapeString(r.Provider))
		case r.Region != "":
			meta = fmt.Sprintf(" (%s)", html.EscapeString(r.Region))
		case r.Provider != "":
			meta = fmt.Sprintf(" (%s)", html.EscapeString(r.Provider))
		}

		labels := make([]string, 0, len(r.Verdicts))
		for _, v := range r.Verdicts {
			labels = append(labels, html.EscapeString(CheburVerdictLabel(v)))
		}

		line := fmt.Sprintf("  • %s%s %s %s", html.EscapeString(r.Name), meta, icon, strings.Join(labels, ", "))
		if r.Cached {
			line += " (кеш)"
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	if s.Verdict != "" {
		sb.WriteString(fmt.Sprintf("\n💡 <b>Вывод:</b> <i>%s</i>\n", html.EscapeString(s.Verdict)))
	}
	return sb.String()
}

// EnrichVerdictWithChebur appends the Cheburcheck finding to a diagnostic
// verdict. An all-ok summary is kept deliberately: it CONTRADICTS the offline
// status and is exactly the interesting signal (local-vs-RF asymmetry).
func EnrichVerdictWithChebur(verdict string, s *CheburcheckSummary) string {
	if s == nil {
		return verdict
	}
	blocked := 0
	total := len(s.Results)
	distinct := make(map[string]bool)
	allOK := total > 0
	for _, r := range s.Results {
		hasBlock, hasOK := false, false
		for _, v := range r.Verdicts {
			if isCheburBlockKind(v) {
				hasBlock = true
				distinct[v] = true
			}
			if v == "ok" {
				hasOK = true
			}
		}
		if hasBlock {
			blocked++
			allOK = false
		} else if !hasOK {
			allOK = false
		}
	}

	switch {
	case blocked > 0:
		kinds := make([]string, 0, len(distinct))
		for _, k := range cheburBlockKinds {
			if distinct[k] {
				kinds = append(kinds, k)
			}
		}
		return fmt.Sprintf("%s | Cheburcheck: %s на %d/%d сканерах РФ", verdict, strings.Join(kinds, ", "), blocked, total)
	case allOK:
		return fmt.Sprintf("%s | Cheburcheck: все сканеры видят доступность", verdict)
	default:
		return fmt.Sprintf("%s | Cheburcheck: неопределённый результат (%d ответов)", verdict, total)
	}
}
