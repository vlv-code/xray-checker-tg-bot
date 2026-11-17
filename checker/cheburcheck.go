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
	"sort"
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

// Verdict-kind taxonomy. Source: the scanner methodology article
// («Методика работы сканеров», «Динамическое сканирование»,
// https://cheburcheck.ru/kb/probing) — scanners report Доступен (ok),
// CDN Блок (cdn_block), SNI Блок (sni_block), Подмена DNS (dns_spoofing),
// ТСПУ Блок (tspu_block), Исключение для CDN (whitelist), Неясно
// (uncertain), and «Один сканер может сообщить сразу о нескольких
// признаках» — so aggregation counts scanners PER VERDICT KIND, not one
// bucket per scanner. Per the site's overall-verdict behavior
// (user-confirmed against the live site 2026-11-17), the CDN family
// (cdn_block, whitelist) is informational exclusion data, NOT a hard
// restriction; only sni_block, tspu_block and dns_spoofing restrict the
// overall verdict. Note: passive registry data from /check (blocked, ips,
// geo) is never read — only active probe verdicts surface anywhere.
var cheburRealRestrictionKinds = []string{"sni_block", "tspu_block", "dns_spoofing"}

// cheburCountsLineKinds fixes the display order of the per-kind counters in
// FormatCheburcheckReport: informational family first, then unclear, then
// real restrictions (wave G frozen format).
var cheburCountsLineKinds = []string{"ok", "whitelist", "cdn_block", "uncertain", "dns_spoofing", "sni_block", "tspu_block"}

// isCheburKnownKind reports whether the kind belongs to the site taxonomy.
func isCheburKnownKind(k string) bool {
	for _, known := range cheburCountsLineKinds {
		if k == known {
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
// call, then the probe phase in one of two modes. API-key mode (an account
// token set via SetAPIKey) opens ONE Bearer-authenticated SSE stream that the
// server fans out to ALL online public scanners; scanner mode opens one SSE
// stream per scanner token. When both are configured the API key wins.
type CheburcheckClient struct {
	BaseURL    string
	HTTPClient *http.Client
	apiKey     string        // account-level Bearer token; "" = scanner mode
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

// SetAPIKey sets the account-level Cheburcheck API token (Bearer) used for
// /probe streams. Call it BEFORE the first CheckTarget — the field is
// deliberately unsynchronized, which is safe only for set-once-before-use
// wiring (main.go sets it during startup). When set, it wins over any
// per-scanner tokens.
func (c *CheburcheckClient) SetAPIKey(key string) {
	c.apiKey = key
}

// HasCredentials reports whether the client can probe at all: an API key is
// set OR at least one scanner credential is usable. Call sites gating
// "is the Cheburcheck feature configured" must use this (not
// HasActiveScanners, which stays scanner-specific).
func (c *CheburcheckClient) HasCredentials() bool {
	return c != nil && (c.apiKey != "" || len(c.scanners) > 0)
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
// Wave G frozen contract: per-verdict-kind counts (a multi-verdict scanner is
// counted in EACH kind it reported); the old one-bucket OKCount/BlockedCount/
// UncertainCount fields are REMOVED (user-authorized rework 2026-11-17 — the
// old aggregate mislabeled cdn_block-heavy hosts as fully blocked).
type CheburcheckSummary struct {
	Target       string              `json:"target"`
	ID           string              `json:"id,omitempty"`
	OnlineProbes int                 `json:"onlineProbes,omitempty"` // started event
	Answered     int                 `json:"answered,omitempty"`     // scanners that delivered results (done.response_count)
	KindCounts   map[string]int      `json:"kindCounts,omitempty"`   // per verdict kind; multi-verdict scanners counted in EACH kind
	Results      []CheburProbeResult `json:"results,omitempty"`      // per-scanner rows (kept for detail)
	Verdict      string              `json:"verdict"`                // site-style RU overall
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
// then the probe phase. API-key mode (key set) uses ONE Bearer stream that
// carries every online public scanner's result; scanner mode opens one SSE
// stream per configured scanner, sequentially (key wins when both are set).
// Single scanner failures in scanner mode are tolerated (warn + skip); 429
// aborts immediately as a typed error in both modes.
func (c *CheburcheckClient) CheckTarget(ctx context.Context, host string) (*CheburcheckSummary, error) {
	if !c.HasCredentials() {
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

	// 2. Probe phase: API-key mode wins over per-scanner tokens.
	summary := &CheburcheckSummary{Target: host, ID: checkResp.ID}
	if c.apiKey != "" {
		// API-key mode: ONE stream, results from ALL online public scanners
		// (each result event carries its own probe_id; Name stays empty and
		// report lines fall back to region/provider, then probe id).
		results, stats, err := c.probeWithAPIKey(ctx, checkResp.ID)
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			return nil, fmt.Errorf("cheburcheck: нет ответов от сканеров")
		}
		summary.Results = append(summary.Results, results...)
		summary.OnlineProbes = stats.onlineProbes
		summary.Answered = stats.answered
	} else {
		var lastErr error
		succeeded := 0
		for _, sc := range c.scanners {
			results, stats, err := c.probeScanner(ctx, checkResp.ID, sc)
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
			succeeded++
			// started reports the job-wide online-scanner count: identical
			// across scanner streams, so keep the max (no double-count);
			// each stream's done.response_count sums into Answered.
			if stats.onlineProbes > summary.OnlineProbes {
				summary.OnlineProbes = stats.onlineProbes
			}
			summary.Answered += stats.answered
			summary.Results = append(summary.Results, results...)
		}

		if succeeded == 0 {
			if lastErr != nil {
				return nil, fmt.Errorf("cheburcheck: все сканеры вернули ошибку: %w", lastErr)
			}
			return nil, fmt.Errorf("cheburcheck: нет ответов от сканеров")
		}
	}

	c.aggregate(summary)
	return summary, nil
}

// probeWithAPIKey consumes the single API-key SSE stream until done, ctx
// cancellation or server-side close. The server fans the probe out to ALL
// online public scanners and reports each as its own result event; results
// keep Name empty (there is no per-scanner credential) — the report formatter
// labels them by region/provider, falling back to the probe id.
func (c *CheburcheckClient) probeWithAPIKey(ctx context.Context, id string) ([]CheburProbeResult, cheburStreamStats, error) {
	probeURL := fmt.Sprintf("%s/probe/%s", c.BaseURL, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return nil, cheburStreamStats{}, fmt.Errorf("cheburcheck probe: %w", err)
	}
	// Account credential: Bearer header, deliberately NO token query param.
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, cheburStreamStats{}, err
		}
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, cheburStreamStats{}, fmt.Errorf("cheburcheck probe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, cheburStreamStats{}, parseCheburError("probe", resp)
	}

	return c.consumeProbeStream(ctx, resp.Body, "api-key")
}

// probeScanner consumes one scanner's SSE stream until done, ctx cancellation
// or server-side close, mapping result events into CheburProbeResult.
func (c *CheburcheckClient) probeScanner(ctx context.Context, id string, sc ScannerCred) ([]CheburProbeResult, cheburStreamStats, error) {
	probeURL := fmt.Sprintf("%s/probe/%s?token=%s", c.BaseURL, url.PathEscape(id), url.QueryEscape(sc.Token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return nil, cheburStreamStats{}, fmt.Errorf("сканер %s: %w", sc.Name, err)
	}
	req.Header.Set("Accept", "text/event-stream")

	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, cheburStreamStats{}, err
		}
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, cheburStreamStats{}, fmt.Errorf("сканер %s: %w", sc.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, cheburStreamStats{}, parseCheburError("probe", resp)
	}

	return c.consumeProbeStream(ctx, resp.Body, sc.Name)
}

// cheburStreamStats carries the per-stream bookkeeping from the started/done
// SSE events: online_probes (the job-wide online-scanner count reported at
// start) and response_count (results delivered when done fires).
type cheburStreamStats struct {
	onlineProbes int
	answered     int
}

// consumeProbeStream parses the SSE event stream from body until done, ctx
// cancellation or server-side close, mapping result events into
// CheburProbeResult entries named scannerName (per-scanner credential name,
// or "" in API-key mode where the stream aggregates all public scanners) and
// reporting the started/done bookkeeping via cheburStreamStats.
func (c *CheburcheckClient) consumeProbeStream(ctx context.Context, body io.Reader, scannerName string) ([]CheburProbeResult, cheburStreamStats, error) {
	var results []CheburProbeResult
	var stats cheburStreamStats
	sawDone := false
	scanner := bufio.NewScanner(body)
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
			logger.Warn("cheburcheck: scanner %s sent malformed %s event: %v", scannerName, evName, err)
		} else if evName == "result" {
			results = append(results, CheburProbeResult{
				ProbeID:  payload.ProbeID,
				Name:     scannerName,
				Region:   payload.Region,
				Provider: payload.Provider,
				ASN:      string(payload.ASN),
				Verdicts: append([]string(nil), payload.Verdicts...),
				Cached:   payload.Cached,
			})
		} else if evName == "started" {
			// started reports the job-wide online-scanner count; keep the max
			// so repeated job-wide values are not double-counted across
			// scanner-mode streams.
			if payload.OnlineProbes > stats.onlineProbes {
				stats.onlineProbes = payload.OnlineProbes
			}
		} else if evName == "done" {
			sawDone = true
			stats.answered = payload.ResponseCount
		}
		evName, dataLines = "", nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			dispatch()
			if sawDone {
				return results, stats, nil
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
			return nil, stats, fmt.Errorf("сканер %s: %w", scannerName, ctx.Err())
		}
		// Stream cut mid-flight: collected results are still valid, an
		// empty collection counts as a scanner failure.
		if len(results) > 0 {
			logger.Warn("cheburcheck: scanner %s stream ended without done: %v", scannerName, err)
			return results, stats, nil
		}
		return nil, stats, fmt.Errorf("сканер %s: поток завершён без результатов: %w", scannerName, err)
	}

	if !sawDone && len(results) == 0 {
		return nil, stats, fmt.Errorf("сканер %s: поток завершён без результатов", scannerName)
	}
	if !sawDone {
		logger.Warn("cheburcheck: scanner %s stream ended without done event", scannerName)
	}
	return results, stats, nil
}

// aggregate builds the per-verdict-kind scanner counts and the site-style RU
// overall verdict. Rule per the scanner methodology article
// (https://cheburcheck.ru/kb/probing): «Один сканер может сообщить сразу о
// нескольких признаках» — every scanner counts in EACH verdict kind it
// reported; scanners with no verdicts count as uncertain («Неясно»).
func (c *CheburcheckClient) aggregate(s *CheburcheckSummary) {
	s.KindCounts = make(map[string]int, len(s.Results))
	for _, r := range s.Results {
		if len(r.Verdicts) == 0 {
			s.KindCounts["uncertain"]++
			continue
		}
		for _, v := range r.Verdicts {
			s.KindCounts[v]++
		}
	}
	s.Verdict = cheburOverallVerdict(s.KindCounts)
}

// cheburOverallVerdict renders the site-style overall verdict: only REAL
// restrictions (sni_block/tspu_block/dns_spoofing) produce «Обнаружены
// ограничения» with per-kind scanner counts; the informational CDN family
// (cdn_block/whitelist) and uncertain never restrict the overall status
// (user-confirmed live-site behavior 2026-11-17).
func cheburOverallVerdict(kinds map[string]int) string {
	var pairs []string
	for _, k := range cheburRealRestrictionKinds {
		if n := kinds[k]; n > 0 {
			pairs = append(pairs, fmt.Sprintf("%s — %d сканеров", k, n))
		}
	}
	if len(pairs) == 0 {
		return "Не ограничен — ограничений не обнаружено"
	}
	return "Обнаружены ограничения: " + strings.Join(pairs, ", ")
}

// CheburVerdictLabel maps a raw verdict kind to the site's RU label.
// Source (verdict taxonomy and wording): «Методика работы сканеров» /
// «Динамическое сканирование», https://cheburcheck.ru/kb/probing —
// Доступен, CDN Блок, SNI Блок, Подмена DNS, ТСПУ Блок, Исключение для
// CDN, Неясно; «Один сканер может сообщить сразу о нескольких признаках».
// The wave G plan freezes cdn_block as «CDN-блокировка» (the article
// renders it «CDN Блок»); the remaining labels follow the article.
// Unknown kinds are returned as-is (callers must escape them before HTML
// rendering).
func CheburVerdictLabel(v string) string {
	switch v {
	case "ok":
		return "Доступен"
	case "uncertain":
		return "Неясно"
	case "cdn_block":
		return "CDN-блокировка"
	case "sni_block":
		return "SNI Блок"
	case "dns_spoofing":
		return "Подмена DNS"
	case "tspu_block":
		return "ТСПУ Блок"
	case "whitelist":
		return "Исключение для CDN"
	default:
		return v
	}
}

// cheburDominantVerdict picks the kind a scanner's group line follows:
// real restrictions dominate, then the informational CDN family, then ok;
// an unknown kind keeps its own raw group; no verdicts -> uncertain.
func cheburDominantVerdict(verdicts []string) string {
	for _, want := range cheburRealRestrictionKinds {
		for _, v := range verdicts {
			if v == want {
				return want
			}
		}
	}
	for _, want := range []string{"cdn_block", "whitelist", "ok"} {
		for _, v := range verdicts {
			if v == want {
				return want
			}
		}
	}
	if len(verdicts) > 0 {
		return verdicts[0]
	}
	return "uncertain"
}

// cheburGroupRank orders detail groups: real restrictions first, then the
// informational CDN family, then ok/uncertain; unknown kinds rank last and
// keep first-appearance order (stable sort).
func cheburGroupRank(kind string) int {
	switch kind {
	case "sni_block":
		return 0
	case "tspu_block":
		return 1
	case "dns_spoofing":
		return 2
	case "cdn_block":
		return 3
	case "whitelist":
		return 4
	case "ok":
		return 5
	case "uncertain":
		return 6
	default:
		return 7
	}
}

// cheburKindIcon: restrictions are ⛔, the informational CDN family ℹ️
// (never ⛔ — the old one-bucket ⛔ for cdn_block was the user-rejected
// mislabel), ok ✅, everything else ❓.
func cheburKindIcon(kind string) string {
	switch kind {
	case "sni_block", "tspu_block", "dns_spoofing":
		return "⛔"
	case "cdn_block", "whitelist":
		return "ℹ️"
	case "ok":
		return "✅"
	default:
		return "❓"
	}
}

// cheburScannerIdent is the compact per-scanner identifier used in the
// grouped detail: region first («Москва, СПб …»), then provider, then the
// scanner name, then the probe id (API-key mode has no credential name).
func cheburScannerIdent(r CheburProbeResult) string {
	switch {
	case r.Region != "":
		return r.Region
	case r.Provider != "":
		return r.Provider
	case r.Name != "":
		return r.Name
	case r.ProbeID != "":
		return "probe #" + r.ProbeID
	default:
		return "сканер"
	}
}

// cheburCountsLine renders the per-kind counters in the fixed site order
// (cheburCountsLineKinds); only non-zero kinds are shown. Unknown kinds
// (outside the site taxonomy) are appended sorted by kind for deterministic
// output.
func cheburCountsLine(kinds map[string]int) string {
	if len(kinds) == 0 {
		return ""
	}
	parts := make([]string, 0, len(kinds))
	for _, k := range cheburCountsLineKinds {
		if n := kinds[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", html.EscapeString(CheburVerdictLabel(k)), n))
		}
	}
	unknown := make([]string, 0, len(kinds))
	for k, n := range kinds {
		if n > 0 && !isCheburKnownKind(k) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		parts = append(parts, fmt.Sprintf("%s: %d", html.EscapeString(CheburVerdictLabel(k)), kinds[k]))
	}
	return strings.Join(parts, " · ")
}

// cheburDetailSection renders the per-scanner compact section: scanners are
// grouped by their dominant verdict kind, scanner identifiers (region, else
// provider, else name, else probe id) are joined «Москва, СПб +12»-style
// (first 3 distinct, then the remaining scanner count); group lines are
// capped at 12 with «+ ещё N сканеров» for the remainder; «(кеш)» markers
// are kept on groups containing cached scanners.
func cheburDetailSection(results []CheburProbeResult) string {
	if len(results) == 0 {
		return ""
	}
	groups := make(map[string][]CheburProbeResult)
	order := make([]string, 0, len(cheburCountsLineKinds))
	for _, r := range results {
		k := cheburDominantVerdict(r.Verdicts)
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return cheburGroupRank(order[i]) < cheburGroupRank(order[j])
	})

	type identCount struct {
		ident string
		n     int
	}
	lines := make([]string, 0, len(order))
	for _, k := range order {
		g := groups[k]
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("  • %s %s: ", cheburKindIcon(k), html.EscapeString(CheburVerdictLabel(k))))

		idents := make([]identCount, 0, len(g))
		idx := make(map[string]int, len(g))
		cached := false
		for _, r := range g {
			if r.Cached {
				cached = true
			}
			id := cheburScannerIdent(r)
			if i, seen := idx[id]; seen {
				idents[i].n++
			} else {
				idx[id] = len(idents)
				idents = append(idents, identCount{ident: id, n: 1})
			}
		}
		shown := 0
		for i, ic := range idents {
			if i == 3 {
				break
			}
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(html.EscapeString(ic.ident))
			shown += ic.n
		}
		if extra := len(g) - shown; extra > 0 {
			sb.WriteString(fmt.Sprintf(" +%d", extra))
		}
		if cached {
			sb.WriteString(" (кеш)")
		}
		lines = append(lines, sb.String())
	}

	const maxDetailLines = 12
	var sb strings.Builder
	for i, line := range lines {
		if i == maxDetailLines {
			rest := 0
			for _, k := range order[maxDetailLines:] {
				rest += len(groups[k])
			}
			sb.WriteString(fmt.Sprintf("  + ещё %d сканеров\n", rest))
			break
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// FormatCheburcheckReport renders the summary as Telegram HTML, summary-first
// (wave G frozen format): 🧀 header, overall status, answered ratio, per-kind
// counts with site labels, then the compact grouped per-scanner detail. Every
// external string is escaped exactly once.
func FormatCheburcheckReport(s *CheburcheckSummary) string {
	if s == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🧀 <b>Cheburcheck</b>: <code>%s</code>\n", html.EscapeString(s.Target)))
	if s.Verdict != "" {
		sb.WriteString(fmt.Sprintf("Статус: <i>%s</i>\n", html.EscapeString(s.Verdict)))
	}
	switch {
	case s.OnlineProbes > 0:
		sb.WriteString(fmt.Sprintf("Ответы: %d из %d сканеров\n", s.Answered, s.OnlineProbes))
	case s.Answered > 0:
		sb.WriteString(fmt.Sprintf("Ответы: %d сканеров\n", s.Answered))
	}
	if line := cheburCountsLine(s.KindCounts); line != "" {
		sb.WriteString("• " + line + "\n")
	}
	sb.WriteString(cheburDetailSection(s.Results))
	return sb.String()
}

// EnrichVerdictWithChebur appends the Cheburcheck finding to a diagnostic
// verdict (deep-diag path, called only for offline proxies). Wave G: only
// REAL restrictions (sni_block/tspu_block/dns_spoofing) surface as
// restrictions — the informational CDN family (cdn_block/whitelist) must
// never read as «blocked». A summary without real restrictions but with ok
// scanners is kept deliberately: it CONTRADICTS the offline status and is
// exactly the interesting signal (local-vs-RF asymmetry).
func EnrichVerdictWithChebur(verdict string, s *CheburcheckSummary) string {
	if s == nil {
		return verdict
	}
	kinds := s.KindCounts
	if len(kinds) == 0 && len(s.Results) > 0 {
		// Hand-built summary without aggregation: derive the counts
		// defensively (same rule as aggregate).
		kinds = make(map[string]int, len(s.Results))
		for _, r := range s.Results {
			if len(r.Verdicts) == 0 {
				kinds["uncertain"]++
				continue
			}
			for _, v := range r.Verdicts {
				kinds[v]++
			}
		}
	}
	var pairs []string
	for _, k := range cheburRealRestrictionKinds {
		if n := kinds[k]; n > 0 {
			pairs = append(pairs, fmt.Sprintf("%s — %d сканеров", k, n))
		}
	}
	switch {
	case len(pairs) > 0:
		return fmt.Sprintf("%s | Cheburcheck: обнаружены ограничения: %s", verdict, strings.Join(pairs, ", "))
	case kinds["ok"] > 0:
		return fmt.Sprintf("%s | Cheburcheck: сканеры РФ видят доступность (%d/%d)", verdict, kinds["ok"], len(s.Results))
	default:
		return fmt.Sprintf("%s | Cheburcheck: неопределённый результат (%d ответов)", verdict, len(s.Results))
	}
}
