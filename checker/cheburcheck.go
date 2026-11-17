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

// CheburCDNControl aggregates one scanner's CDN control-server probe outcome
// (wave I F2, real model). Source: the REAL SSE schema live-captured
// 2026-11-17 — each result carries host_results entries
// {"host":"Blacklist"|"Whitelist","host_id":"…","probe_evidence":{"bytes":N,
// "type":"Good"|"ClientHello"|"DataTimeout"|"ConnectionError"}}; counts are
// bucketed per family (Blacklist = «заблок. диапазоны», Whitelist =
// «доступные») and per evidence type (site statuses: Good → «Успешно» → ✅,
// ClientHello → «Блокировка после ClientHello», DataTimeout → «Таймаут
// данных», ConnectionError → «Ошибка подключения»; the KB
// https://cheburcheck.ru/kb/probing documents the STATUSES but not the SSE
// payload schema). Unknown evidence types and unknown host families are
// bucketed under their own raw names so a schema extension can never break
// the stream or the report. A result without host_results carries nil.
type CheburCDNControl struct {
	Blacklist map[string]int            `json:"blacklist,omitempty"`
	Whitelist map[string]int            `json:"whitelist,omitempty"`
	Other     map[string]map[string]int `json:"other,omitempty"` // unknown families → type → count
}

// cheburEvidenceBucket normalizes one probe_evidence.type spelling: the site
// status «Таймаут данных» arrived as DataTimeout in the 2026-11-17 capture;
// a hypothetical "Timeout" spelling folds into the same bucket so both
// render as one таймаут count. Everything else keeps its raw name.
func cheburEvidenceBucket(t string) string {
	if t == "Timeout" {
		return "DataTimeout"
	}
	return t
}

// cheburHostResult is one SSE host_results entry (real schema, live capture
// 2026-11-17).
type cheburHostResult struct {
	Host          string `json:"host"`
	HostID        string `json:"host_id"`
	ProbeEvidence struct {
		Type  string `json:"type"`
		Bytes int    `json:"bytes"`
	} `json:"probe_evidence"`
}

// cheburDNSObservation is one dns.observations entry; only the spoofing flag
// is consumed (metadata/outcome shapes are the resolver API's own business).
type cheburDNSObservation struct {
	SuspectedSpoofing bool `json:"suspected_spoofing"`
}

// cheburDNSInfo is the REAL SSE `dns` object (live capture 2026-11-17):
// {"observations":[…],"spoofing_detected":bool,…}. A result is DNS-spoofed
// when ANY observation carries suspected_spoofing=true.
type cheburDNSInfo struct {
	Observations []cheburDNSObservation `json:"observations"`
}

// cheburCDNControlFromHostResults buckets one result's host_results entries
// into the per-family/per-type counts. Entries with an empty evidence type
// are skipped (no status reported); empty families yield empty maps so the
// report can still distinguish "family present but zero-countable".
func cheburCDNControlFromHostResults(entries []cheburHostResult) *CheburCDNControl {
	ctrl := &CheburCDNControl{
		Blacklist: make(map[string]int, 4),
		Whitelist: make(map[string]int, 4),
		Other:     make(map[string]map[string]int),
	}
	for _, h := range entries {
		t := cheburEvidenceBucket(h.ProbeEvidence.Type)
		if t == "" {
			continue
		}
		switch h.Host {
		case "Blacklist":
			ctrl.Blacklist[t]++
		case "Whitelist":
			ctrl.Whitelist[t]++
		default:
			if ctrl.Other[h.Host] == nil {
				ctrl.Other[h.Host] = make(map[string]int, 4)
			}
			ctrl.Other[h.Host][t]++
		}
	}
	return ctrl
}

// add sums another scanner's CDN control counts into this one (report-level
// aggregation across results).
func (x *CheburCDNControl) add(o *CheburCDNControl) {
	for t, n := range o.Blacklist {
		x.Blacklist[t] += n
	}
	for t, n := range o.Whitelist {
		x.Whitelist[t] += n
	}
	for fam, types := range o.Other {
		if x.Other[fam] == nil {
			x.Other[fam] = make(map[string]int, len(types))
		}
		for t, n := range types {
			x.Other[fam][t] += n
		}
	}
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
	// DNSSpoofed is the parse of the optional SSE `dns` object (nil = field
	// absent; otherwise ANY observations[].suspected_spoofing=true — wave I
	// F3, real schema live capture 2026-11-17). Verdicts stay authoritative:
	// the flag only speaks when a scanner's verdict list is silent (see
	// aggregate).
	DNSSpoofed *bool `json:"dns_spoofed,omitempty"`
	// CDNControl aggregates the per-result host_results CDN control evidence
	// (nil = no host_results entries; wave I F2 real model).
	CDNControl *CheburCDNControl `json:"cdn_control,omitempty"`
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
	Answered     int                 `json:"answered,omitempty"`     // scanners that delivered results: len(Results) — done.response_count is NOT used (wave I F1: done may never arrive within client timeouts)
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
// fields are ignored by encoding/json (the real stream also carries a
// boolean cdn_unblocked and dns metadata/outcome shapes this client never
// reads). The REAL result-only fields (live capture 2026-11-17) are the
// `dns` object (observations with suspected_spoofing) and `host_results`
// (CDN control evidence) — both parse typed; absent fields stay nil so a
// partial payload never fails the stream.
type cheburSSEPayload struct {
	ID           string             `json:"id"`
	Target       string             `json:"target"`
	OnlineProbes int                `json:"online_probes"`
	JobID        string             `json:"job_id"`
	ProbeID      string             `json:"probe_id"`
	Cached       bool               `json:"cached"`
	Verdicts     []string           `json:"verdicts"`
	Region       string             `json:"region"`
	Provider     string             `json:"provider"`
	ASN          cheburFlexStr      `json:"asn"`
	DNS          *cheburDNSInfo     `json:"dns"`
	HostResults  []cheburHostResult `json:"host_results"`
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
			// across scanner streams, so keep the max (no double-count).
			// Answered is derived once below from the delivered results —
			// done.response_count is never trusted (wave I F1).
			if stats.onlineProbes > summary.OnlineProbes {
				summary.OnlineProbes = stats.onlineProbes
			}
			summary.Results = append(summary.Results, results...)
		}

		if succeeded == 0 {
			if lastErr != nil {
				return nil, fmt.Errorf("cheburcheck: все сканеры вернули ошибку: %w", lastErr)
			}
			return nil, fmt.Errorf("cheburcheck: нет ответов от сканеров")
		}
	}

	// Wave I F1: Answered counts the scanners that DELIVERED results in
	// whichever mode ran — the done event may never arrive within client
	// timeouts (49-scanner jobs), and its response_count may diverge from
	// what the client actually received.
	summary.Answered = len(summary.Results)
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

// cheburStreamStats carries the per-stream bookkeeping from the started SSE
// event: the job-wide online-scanner count. The done event only marks the
// stream end — its response_count is deliberately ignored (wave I F1:
// Answered is derived from the delivered results).
type cheburStreamStats struct {
	onlineProbes int
}

// consumeProbeStream parses the SSE event stream from body until done, ctx
// cancellation or server-side close, mapping result events into
// CheburProbeResult entries named scannerName (per-scanner credential name,
// or "" in API-key mode where the stream aggregates all public scanners) and
// reporting the started bookkeeping via cheburStreamStats.
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
			result := CheburProbeResult{
				ProbeID:  payload.ProbeID,
				Name:     scannerName,
				Region:   payload.Region,
				Provider: payload.Provider,
				ASN:      string(payload.ASN),
				Verdicts: append([]string(nil), payload.Verdicts...),
				Cached:   payload.Cached,
			}
			// Wave I F3: the real `dns` object — DNSSpoofed speaks only when
			// the field is present (any observation suspected); absent field
			// stays nil and never fails the stream.
			if payload.DNS != nil {
				v := false
				for _, o := range payload.DNS.Observations {
					if o.SuspectedSpoofing {
						v = true
						break
					}
				}
				result.DNSSpoofed = &v
			}
			// Wave I F2: the real `host_results` evidence — bucketed counts,
			// absent field stays nil.
			if len(payload.HostResults) > 0 {
				result.CDNControl = cheburCDNControlFromHostResults(payload.HostResults)
			}
			results = append(results, result)
		} else if evName == "started" {
			// started reports the job-wide online-scanner count; keep the max
			// so repeated job-wide values are not double-counted across
			// scanner-mode streams.
			if payload.OnlineProbes > stats.onlineProbes {
				stats.onlineProbes = payload.OnlineProbes
			}
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
// reported; scanners with no verdicts count as uncertain («Неясно»), except
// that a silent verdict list with a parsed dns=true flag (optional SSE field,
// wave H5) counts as dns_spoofing — verdicts stay authoritative whenever
// they are non-empty.
func (c *CheburcheckClient) aggregate(s *CheburcheckSummary) {
	s.KindCounts = make(map[string]int, len(s.Results))
	for _, r := range s.Results {
		if len(r.Verdicts) == 0 {
			if r.DNSSpoofed != nil && *r.DNSSpoofed {
				s.KindCounts["dns_spoofing"]++
			} else {
				s.KindCounts["uncertain"]++
			}
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

// cheburCDNTypeOrder fixes the display order of the evidence-type tokens in
// a CDN family line (site style: successes first, then ClientHello blocks,
// timeouts, connection errors; unknown types trail, sorted).
var cheburCDNTypeOrder = []string{"Good", "ClientHello", "DataTimeout", "ConnectionError"}

// cheburEvidenceLabel renders one evidence-type count (site status wording,
// wave I F2): Good → «N✅», ClientHello → «N ClientHello», DataTimeout →
// «N таймаута(-ов)», ConnectionError → «N ошибка(-и,-ок)»; unknown types
// render their raw (escaped) name.
func cheburEvidenceLabel(t string, n int) string {
	switch t {
	case "Good":
		return fmt.Sprintf("%d✅", n)
	case "ClientHello":
		return fmt.Sprintf("%d ClientHello", n)
	case "DataTimeout":
		return fmt.Sprintf("%d %s", n, cheburPluralTimeouts(n))
	case "ConnectionError":
		return fmt.Sprintf("%d %s", n, cheburPluralEvidenceErrors(n))
	default:
		return fmt.Sprintf("%d %s", n, html.EscapeString(t))
	}
}

// cheburCDNFamilyLine renders one family line: «CDN заблок. диапазоны: 8✅ ·
// 2 ClientHello · 1 таймаут · 1 ошибка» — labels for the known families are
// static (site wording); unknown families pass a pre-escaped label. Empty
// counts yield "" so zero families never render.
func cheburCDNFamilyLine(label string, counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(counts))
	seen := make(map[string]bool, len(counts))
	for _, t := range cheburCDNTypeOrder {
		if n := counts[t]; n > 0 {
			parts = append(parts, cheburEvidenceLabel(t, n))
			seen[t] = true
		}
	}
	unknown := make([]string, 0, len(counts))
	for t := range counts {
		if !seen[t] {
			unknown = append(unknown, t)
		}
	}
	sort.Strings(unknown)
	for _, t := range unknown {
		parts = append(parts, cheburEvidenceLabel(t, counts[t]))
	}
	if len(parts) == 0 {
		return ""
	}
	return label + ": " + strings.Join(parts, " · ")
}

// cheburCDNControlLines sums the CDN control evidence over the results that
// carry host_results (wave I F2 real model) and renders the TWO dense
// site-style lines: blocked-range servers first, then accessible ones
// (unknown families trail, sorted). The lines stay hidden entirely when no
// result carries host_results data.
func cheburCDNControlLines(results []CheburProbeResult) []string {
	total := &CheburCDNControl{
		Blacklist: make(map[string]int, 4),
		Whitelist: make(map[string]int, 4),
		Other:     make(map[string]map[string]int),
	}
	carried := false
	for i := range results {
		if results[i].CDNControl == nil {
			continue
		}
		carried = true
		total.add(results[i].CDNControl)
	}
	if !carried {
		return nil
	}
	var lines []string
	if line := cheburCDNFamilyLine("CDN заблок. диапазоны", total.Blacklist); line != "" {
		lines = append(lines, line)
	}
	if line := cheburCDNFamilyLine("CDN доступные", total.Whitelist); line != "" {
		lines = append(lines, line)
	}
	fams := make([]string, 0, len(total.Other))
	for fam := range total.Other {
		fams = append(fams, fam)
	}
	sort.Strings(fams)
	for _, fam := range fams {
		if line := cheburCDNFamilyLine("CDN "+html.EscapeString(fam), total.Other[fam]); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// cheburPluralTimeouts is the RU plural: 1 таймаут, 2–4 таймаута, otherwise
// таймаутов (11–14 included).
func cheburPluralTimeouts(n int) string {
	if n%10 == 1 && n%100 != 11 {
		return "таймаут"
	}
	if n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14) {
		return "таймаута"
	}
	return "таймаутов"
}

// cheburPluralEvidenceErrors is the RU plural: 1 ошибка, 2–4 ошибки,
// otherwise ошибок (11–14 included).
func cheburPluralEvidenceErrors(n int) string {
	if n%10 == 1 && n%100 != 11 {
		return "ошибка"
	}
	if n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14) {
		return "ошибки"
	}
	return "ошибок"
}

// cheburRegionIconOrder fixes the icon order inside one region segment of
// the «По регионам» line: ✅ ok first (both pinned site examples — «4✅1⛔»
// and wave I F4 «2✅1ℹ️» — lead with ✅), then ⛔ real restrictions, then the
// informational ℹ️ CDN family, then ❓ unclear.
var cheburRegionIconOrder = []string{"✅", "⛔", "ℹ️", "❓"}

// cheburRegionsLine renders the dense «По регионам: Москва 2✅1ℹ️ · …» line:
// scanners aggregated per region, each scanner classified by its dominant
// verdict kind's icon (cheburKindIcon(cheburDominantVerdict(…))), every
// region showing its verdict-mix icon counts (wave I F4 — NO iconless
// segments: the old bare count could not tell a healthy region from an
// undetermined one); regions sorted by scanner count (desc, then name asc),
// top-5 shown, the remainder folded into «+N регионов».
func cheburRegionsLine(results []CheburProbeResult) string {
	type regStat struct {
		total int
		icons map[string]int
	}
	stats := make(map[string]*regStat)
	for _, r := range results {
		if r.Region == "" {
			continue
		}
		st, seen := stats[r.Region]
		if !seen {
			st = &regStat{icons: make(map[string]int, len(cheburRegionIconOrder))}
			stats[r.Region] = st
		}
		st.total++
		st.icons[cheburKindIcon(cheburDominantVerdict(r.Verdicts))]++
	}
	if len(stats) == 0 {
		return ""
	}
	regions := make([]string, 0, len(stats))
	for name := range stats {
		regions = append(regions, name)
	}
	sort.Slice(regions, func(i, j int) bool {
		if stats[regions[i]].total != stats[regions[j]].total {
			return stats[regions[i]].total > stats[regions[j]].total
		}
		return regions[i] < regions[j]
	})
	const topN = 5
	parts := make([]string, 0, topN)
	for i, name := range regions {
		if i == topN {
			break
		}
		seg := html.EscapeString(name) + " "
		for _, icon := range cheburRegionIconOrder {
			if n := stats[name].icons[icon]; n > 0 {
				seg += fmt.Sprintf("%d%s", n, icon)
			}
		}
		parts = append(parts, seg)
	}
	line := "По регионам: " + strings.Join(parts, " · ")
	if extra := len(regions) - topN; extra > 0 {
		line += fmt.Sprintf(" · +%d %s", extra, cheburPluralRegions(extra))
	}
	return line
}

// cheburPluralRegions is the RU plural for the extra-region counter:
// 1 регион, 2–4 региона, otherwise регионов (11–14 included).
func cheburPluralRegions(n int) string {
	if n%10 == 1 && n%100 != 11 {
		return "регион"
	}
	if n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14) {
		return "региона"
	}
	return "регионов"
}

// FormatCheburcheckReport renders the summary as Telegram HTML, summary-first
// (wave H5 compact-full format; replaces the wave G 🧀 header — authorized
// H3 rework 2026-11-17): plain header, overall status, answered ratio
// (Answered = delivered results, wave I F1), per-kind counts with site
// labels, the always-on DNS substitution ratio («у 0 из N» is site-parity),
// the two CDN control lines (wave I F2 real host_results model), the region
// breakdown line with per-region verdict-mix icons (wave I F4), then the
// compact grouped per-scanner detail. Every external string is escaped
// exactly once.
func FormatCheburcheckReport(s *CheburcheckSummary) string {
	if s == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>Cheburcheck</b>: <code>%s</code>\n", html.EscapeString(s.Target)))
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
	// DNS substitution ratio: always shown (site parity), «у 0 из N» is fine.
	probed := len(s.Results)
	if probed == 0 {
		probed = s.Answered
	}
	if probed > 0 {
		sb.WriteString(fmt.Sprintf("Подмена DNS: %d из %d\n", s.KindCounts["dns_spoofing"], probed))
	}
	for _, line := range cheburCDNControlLines(s.Results) {
		sb.WriteString(line + "\n")
	}
	if line := cheburRegionsLine(s.Results); line != "" {
		sb.WriteString(line + "\n")
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
		return fmt.Sprintf("%s | Cheburcheck: сканеры видят доступность (%d/%d)", verdict, kinds["ok"], len(s.Results))
	default:
		return fmt.Sprintf("%s | Cheburcheck: неопределённый результат (%d ответов)", verdict, len(s.Results))
	}
}
