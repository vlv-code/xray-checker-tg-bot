package checker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// cheburSSE renders a single named SSE event (blank-line terminated), as
// produced by cheburcheck.ru /probe/{id} streams.
func cheburSSE(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

// TestCheburcheck_InactiveWithoutScanners pins the PUBLIC-REPO invariant:
// DefaultCheburScanners stays empty and a client without valid scanner
// credentials is fully inactive — CheckTarget must fail fast with the
// exported sentinel and must NOT touch the network at all.
func TestCheburcheck_InactiveWithoutScanners(t *testing.T) {
	if len(DefaultCheburScanners) != 0 {
		t.Fatalf("DefaultCheburScanners must stay EMPTY in the public repo, got %d entries", len(DefaultCheburScanners))
	}

	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, time.Second, nil)
	if cl.HasActiveScanners() {
		t.Fatalf("client without scanners must be inactive")
	}
	if _, err := cl.CheckTarget(context.Background(), "example.com"); !errors.Is(err, ErrCheburInactive) {
		t.Fatalf("expected ErrCheburInactive, got %v", err)
	}

	// An entry with a name but an EMPTY token is not an active scanner either.
	cl2 := NewCheburcheckClient(srv.URL, time.Second, []ScannerCred{{Name: "msk-1", Token: ""}})
	if cl2.HasActiveScanners() {
		t.Fatalf("scanner with empty token must not count as active")
	}
	if _, err := cl2.CheckTarget(context.Background(), "example.com"); !errors.Is(err, ErrCheburInactive) {
		t.Fatalf("expected ErrCheburInactive for empty-token scanner, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("inactive client must make zero HTTP calls, made %d", calls)
	}
}

// TestCheburcheck_CheckTargetHappyPath pins the core flow: /check consumes
// ONLY the signed id (passive registry fields are present in the mock and must
// stay unsurfaced), per-scanner SSE streams are queried with their own tokens,
// and aggregation is per VERDICT KIND (wave G frozen contract).
// Replaced-test register (user-authorized rework 2026-11-17): the old
// one-bucket-per-scanner assertions (OKCount/BlockedCount/UncertainCount +
// "⛔ блокировка: … — K из N сканеров" verdict) are REPLACED by
// KindCounts/OnlineProbes/Answered pins and the site-style verdict.
func TestCheburcheck_CheckTargetHappyPath(t *testing.T) {
	var mu sync.Mutex
	var checkTargets []string
	var probeTokens []string
	var probeAccepts []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/check":
			checkTargets = append(checkTargets, r.URL.Query().Get("target"))
			// Passive registry fields are intentionally present: the client
			// must consume ONLY the signed id and ignore the rest.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":      "job123",
				"blocked": true,
				"ips":     []string{"203.0.113.9"},
				"geo":     map[string]string{"country": "RU"},
			})
		case r.URL.Path == "/probe/job123":
			probeTokens = append(probeTokens, r.URL.Query().Get("token"))
			probeAccepts = append(probeAccepts, r.Header.Get("Accept"))
			w.Header().Set("Content-Type", "text/event-stream")
			switch r.URL.Query().Get("token") {
			case "tok-a":
				_, _ = w.Write([]byte(
					cheburSSE("started", `{"id":"job123","target":"example.com","online_probes":2}`) +
						cheburSSE("result", `{"job_id":"j1","probe_id":"p-a","cached":false,"verdicts":["ok"],"host_results":[],"target_hop":null,"dpi_hop":null,"dns":null}`) +
						cheburSSE("done", `{"id":"job123","status":"finished","response_count":1,"online_probes":2}`)))
			case "tok-b":
				_, _ = w.Write([]byte(
					cheburSSE("started", `{"id":"job123","target":"example.com","online_probes":2}`) +
						cheburSSE("result", `{"job_id":"j2","probe_id":"p-b","cached":true,"verdicts":["sni_block","tspu_block"],"region":"moscow","provider":"provider-b","asn":"AS9000"}`) +
						cheburSSE("done", `{"id":"job123","status":"finished","response_count":1,"online_probes":2}`)))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 5*time.Second, []ScannerCred{
		{Name: "msk-a", Token: "tok-a"},
		{Name: "msk-b", Token: "tok-b"},
	})
	cl.limiter = nil // rate limiting is not under test; keep the run fast

	s, err := cl.CheckTarget(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.ID != "job123" || s.Target != "example.com" {
		t.Fatalf("summary identity wrong: id=%q target=%q", s.ID, s.Target)
	}
	if len(s.Results) != 2 {
		t.Fatalf("expected 2 probe results, got %d", len(s.Results))
	}
	r0, r1 := s.Results[0], s.Results[1]
	if r0.Name != "msk-a" || r0.ProbeID != "p-a" || len(r0.Verdicts) != 1 || r0.Verdicts[0] != "ok" || r0.Cached {
		t.Errorf("msk-a result wrong: %+v", r0)
	}
	if r1.Name != "msk-b" || r1.ProbeID != "p-b" || r1.Region != "moscow" || r1.Provider != "provider-b" || r1.ASN != "AS9000" || !r1.Cached {
		t.Errorf("msk-b result wrong: %+v", r1)
	}
	if len(r1.Verdicts) != 2 || r1.Verdicts[0] != "sni_block" || r1.Verdicts[1] != "tspu_block" {
		t.Errorf("msk-b verdicts wrong: %v", r1.Verdicts)
	}
	// Per-verdict-kind counting: msk-a feeds "ok", msk-b feeds BOTH
	// sni_block and tspu_block.
	if s.KindCounts["ok"] != 1 || s.KindCounts["sni_block"] != 1 || s.KindCounts["tspu_block"] != 1 || len(s.KindCounts) != 3 {
		t.Fatalf("kind counts wrong: %v", s.KindCounts)
	}
	// started/done bookkeeping: online_probes is job-wide (max across
	// scanner streams), answered is the sum of done.response_count.
	if s.OnlineProbes != 2 || s.Answered != 2 {
		t.Fatalf("started/done stats wrong: online=%d answered=%d", s.OnlineProbes, s.Answered)
	}
	for _, want := range []string{"Обнаружены ограничения:", "sni_block — 1", "tspu_block — 1"} {
		if !strings.Contains(s.Verdict, want) {
			t.Errorf("verdict %q missing %q", s.Verdict, want)
		}
	}
	if idxSNI, idxTSPU := strings.Index(s.Verdict, "sni_block"), strings.Index(s.Verdict, "tspu_block"); idxSNI > idxTSPU {
		t.Errorf("verdict kinds must render in canonical order (sni first): %q", s.Verdict)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(checkTargets) != 1 || checkTargets[0] != "example.com" {
		t.Errorf("expected single /check with bare host, got %v", checkTargets)
	}
	if len(probeTokens) != 2 || probeTokens[0] != "tok-a" || probeTokens[1] != "tok-b" {
		t.Errorf("probe tokens wrong: %v", probeTokens)
	}
	for _, a := range probeAccepts {
		if a != "text/event-stream" {
			t.Errorf("probe Accept header wrong: %q", a)
		}
	}

	// Invariant: NO passive registry data may leak into the surfaced summary.
	// Match exact JSON keys (with colon) so the legitimate aggregate field
	// "kindCounts" is not a false positive.
	raw, _ := json.Marshal(s)
	for _, leaked := range []string{`"blocked":`, `"ips":`, `"geo":`} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("summary leaks passive registry field %s: %s", leaked, raw)
		}
	}
}

// TestCheburcheck_PortStrippedAndTargetValidated: a host:port argument is
// probed on the bare host only (cheburcheck has no port concept) and an empty
// host is rejected before any HTTP call.
func TestCheburcheck_PortStrippedAndTargetValidated(t *testing.T) {
	var mu sync.Mutex
	var checkTargets []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/check":
			checkTargets = append(checkTargets, r.URL.Query().Get("target"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "id-port"})
		case r.URL.Path == "/probe/id-port":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				cheburSSE("started", `{"id":"id-port","online_probes":1}`) +
					cheburSSE("result", `{"probe_id":"p","verdicts":["ok"]}`) +
					cheburSSE("done", `{"id":"id-port","response_count":1}`)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 2*time.Second, []ScannerCred{{Name: "s1", Token: "t1"}})
	cl.limiter = nil

	if _, err := cl.CheckTarget(context.Background(), "host.example:8443"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), checkTargets...)
	checkTargets = nil
	mu.Unlock()
	if len(got) != 1 || got[0] != "host.example" {
		t.Fatalf("expected /check with bare host host.example, got %v", got)
	}

	if _, err := cl.CheckTarget(context.Background(), ""); err == nil {
		t.Fatalf("empty host must be rejected before any HTTP call")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(checkTargets) != 0 {
		t.Fatalf("empty host must make zero HTTP calls, made %d (%v)", len(checkTargets), checkTargets)
	}
}

// TestCheburcheck_SSEParserEdgeCases covers the adversarial stream shapes from
// the plan: result-before-started, done with partial responses, a stream cut
// without done, unknown extra fields, and an empty verdicts array.
func TestCheburcheck_SSEParserEdgeCases(t *testing.T) {
	newMock := func(t *testing.T, stream string) *CheburcheckClient {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/check":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "edge-id"})
			case r.URL.Path == "/probe/edge-id":
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(stream))
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)
		cl := NewCheburcheckClient(srv.URL, 2*time.Second, []ScannerCred{{Name: "edge", Token: "tok"}})
		cl.limiter = nil
		return cl
	}

	t.Run("result before started", func(t *testing.T) {
		cl := newMock(t,
			cheburSSE("result", `{"job_id":"j","probe_id":"p1","verdicts":["ok"]}`)+
				cheburSSE("done", `{"id":"edge-id","response_count":1}`))
		s, err := cl.CheckTarget(context.Background(), "example.com")
		if err != nil {
			t.Fatalf("results before started must still be collected, got error: %v", err)
		}
		// No started event -> OnlineProbes stays 0 (omitempty drops it from
		// JSON); done.response_count still feeds Answered.
		if len(s.Results) != 1 || s.KindCounts["ok"] != 1 || s.Answered != 1 || s.OnlineProbes != 0 {
			t.Fatalf("expected 1 ok result with answered=1 online=0, got %+v", s)
		}
	})

	t.Run("done with partial responses", func(t *testing.T) {
		cl := newMock(t,
			cheburSSE("started", `{"id":"edge-id","online_probes":5}`)+
				cheburSSE("result", `{"probe_id":"p1","verdicts":["ok"]}`)+
				cheburSSE("done", `{"id":"edge-id","status":"timeout","response_count":1,"online_probes":5}`))
		s, err := cl.CheckTarget(context.Background(), "example.com")
		if err != nil {
			t.Fatalf("done with response_count < online_probes must end cleanly, got: %v", err)
		}
		if len(s.Results) != 1 {
			t.Fatalf("expected 1 collected result, got %d", len(s.Results))
		}
		if s.OnlineProbes != 5 || s.Answered != 1 {
			t.Fatalf("started/done stats wrong: online=%d answered=%d", s.OnlineProbes, s.Answered)
		}
	})

	t.Run("stream cut without done", func(t *testing.T) {
		cl := newMock(t,
			cheburSSE("started", `{"id":"edge-id","online_probes":2}`)+
				cheburSSE("result", `{"probe_id":"p1","verdicts":["ok"]}`))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s, err := cl.CheckTarget(ctx, "example.com")
		if err != nil {
			t.Fatalf("stream cut after results must not error and must not hang, got: %v", err)
		}
		if len(s.Results) != 1 {
			t.Fatalf("expected collected result after cut stream, got %d", len(s.Results))
		}
		if s.OnlineProbes != 2 || s.Answered != 0 {
			t.Fatalf("cut stream without done must not fake an answer: online=%d answered=%d", s.OnlineProbes, s.Answered)
		}
	})

	t.Run("unknown fields ignored", func(t *testing.T) {
		cl := newMock(t,
			cheburSSE("result", `{"future_field":{"nested":[1,2]},"dpi_hop":null,"dns":null,"probe_id":"p9","verdicts":["ok"],"cdn_unblocked":true}`)+
				cheburSSE("done", `{"id":"edge-id","response_count":1}`))
		s, err := cl.CheckTarget(context.Background(), "example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(s.Results) != 1 || s.Results[0].ProbeID != "p9" || s.KindCounts["ok"] != 1 {
			t.Fatalf("unknown fields must be ignored, got %+v", s)
		}
	})

	t.Run("empty verdicts uncertain", func(t *testing.T) {
		cl := newMock(t,
			cheburSSE("result", `{"probe_id":"p0","verdicts":[]}`)+
				cheburSSE("done", `{"id":"edge-id","response_count":1}`))
		s, err := cl.CheckTarget(context.Background(), "example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Replaced-test register (wave G): the old one-bucket assertion
		// (UncertainCount==1 + verdict contains "неопределённый") is REPLACED
		// by the per-kind uncertain bucket and the site-style overall verdict
		// — uncertain is NOT a real restriction, so the host reads
		// «Не ограничен — ограничений не обнаружено».
		if s.KindCounts["uncertain"] != 1 || len(s.KindCounts) != 1 {
			t.Fatalf("empty verdicts must count as uncertain, got %v", s.KindCounts)
		}
		if s.Verdict != "Не ограничен — ограничений не обнаружено" {
			t.Errorf("expected unrestricted verdict for uncertain-only, got %q", s.Verdict)
		}
	})
}

// TestCheburcheck_MultiVerdictScannerCountsInEachKind pins the site-style
// aggregation rule from the scanner methodology article
// (https://cheburcheck.ru/kb/probing, «Один сканер может сообщить сразу о
// нескольких признаках»): one scanner carrying [ok, cdn_block] increments
// BOTH kinds, and a CDN-family verdict is informational — the overall verdict
// stays «Не ограничен».
func TestCheburcheck_MultiVerdictScannerCountsInEachKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "multi"})
		case r.URL.Path == "/probe/multi":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				cheburSSE("started", `{"id":"multi","online_probes":1}`) +
					cheburSSE("result", `{"probe_id":"p1","verdicts":["ok","cdn_block"]}`) +
					cheburSSE("done", `{"id":"multi","response_count":1,"online_probes":1}`)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 2*time.Second, []ScannerCred{{Name: "s1", Token: "t1"}})
	cl.limiter = nil

	s, err := cl.CheckTarget(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.KindCounts["ok"] != 1 || s.KindCounts["cdn_block"] != 1 || len(s.KindCounts) != 2 {
		t.Fatalf("multi-verdict scanner must count in EACH kind, got %v", s.KindCounts)
	}
	if s.Verdict != "Не ограничен — ограничений не обнаружено" {
		t.Fatalf("cdn_block alone must NOT restrict the verdict, got %q", s.Verdict)
	}
}

// TestCheburcheck_CDNBlockHeavyHostNotRestricted reproduces the user's live
// rejected case (v2.8.0): 24×cdn_block + 16×ok + 2×uncertain scanners must
// yield «Не ограничен — ограничений не обнаружено» (the site reports
// «Не ограничен» for such hosts; cdn_block is CDN-exclusion info), NOT the
// old «⛔ блокировка: cdn_block — 24 из 42 сканеров» aggregate.
func TestCheburcheck_CDNBlockHeavyHostNotRestricted(t *testing.T) {
	var stream strings.Builder
	stream.WriteString(cheburSSE("started", `{"id":"cdnjob","target":"cdn.example","online_probes":42}`))
	for i := 0; i < 24; i++ {
		stream.WriteString(cheburSSE("result", fmt.Sprintf(`{"probe_id":"c%d","verdicts":["cdn_block"]}`, i)))
	}
	for i := 0; i < 16; i++ {
		stream.WriteString(cheburSSE("result", fmt.Sprintf(`{"probe_id":"o%d","verdicts":["ok"]}`, i)))
	}
	stream.WriteString(cheburSSE("result", `{"probe_id":"u0","verdicts":[]}`))
	stream.WriteString(cheburSSE("result", `{"probe_id":"u1","verdicts":[]}`))
	stream.WriteString(cheburSSE("done", `{"id":"cdnjob","status":"finished","response_count":42,"online_probes":42}`))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "cdnjob"})
		case r.URL.Path == "/probe/cdnjob":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(stream.String()))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 5*time.Second, nil)
	cl.SetAPIKey("test-key")
	cl.limiter = nil

	s, err := cl.CheckTarget(context.Background(), "cdn.example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.OnlineProbes != 42 || s.Answered != 42 {
		t.Fatalf("started/done stats wrong: online=%d answered=%d", s.OnlineProbes, s.Answered)
	}
	if s.KindCounts["cdn_block"] != 24 || s.KindCounts["ok"] != 16 || s.KindCounts["uncertain"] != 2 || len(s.KindCounts) != 3 {
		t.Fatalf("kind counts wrong: %v", s.KindCounts)
	}
	if s.Verdict != "Не ограничен — ограничений не обнаружено" {
		t.Fatalf("cdn-heavy host must read as unrestricted (site-style), got %q", s.Verdict)
	}

	out := FormatCheburcheckReport(s)
	if !strings.Contains(out, "Ответы: 42 из 42 сканеров") {
		t.Errorf("report must carry the answered ratio, got:\n%s", out)
	}
	for _, want := range []string{"Доступен: 16", "CDN-блокировка: 24", "Неясно: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("counts line missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Обнаружены ограничения") {
		t.Errorf("cdn-heavy host must not read as restricted, got:\n%s", out)
	}
	if strings.Contains(out, "⛔") {
		t.Errorf("informational CDN family must not render the blocking icon, got:\n%s", out)
	}
}

// TestCheburcheck_SNIBlockRealRestrictionVerdict: sni_block IS a real
// restriction — 3 scanners reporting it must produce the site-style
// «Обнаружены ограничения: sni_block — 3 сканеров» verdict.
func TestCheburcheck_SNIBlockRealRestrictionVerdict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "sni3"})
		case r.URL.Path == "/probe/sni3":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				cheburSSE("started", `{"id":"sni3","online_probes":3}`) +
					cheburSSE("result", `{"probe_id":"p","verdicts":["sni_block"]}`) +
					cheburSSE("done", `{"id":"sni3","response_count":1,"online_probes":3}`)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 5*time.Second, []ScannerCred{
		{Name: "msk-a", Token: "tok-a"},
		{Name: "msk-b", Token: "tok-b"},
		{Name: "msk-c", Token: "tok-c"},
	})
	cl.limiter = nil

	s, err := cl.CheckTarget(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.KindCounts["sni_block"] != 3 || len(s.KindCounts) != 1 {
		t.Fatalf("kind counts wrong: %v", s.KindCounts)
	}
	if s.OnlineProbes != 3 || s.Answered != 3 {
		t.Fatalf("started/done stats wrong: online=%d answered=%d", s.OnlineProbes, s.Answered)
	}
	if s.Verdict != "Обнаружены ограничения: sni_block — 3 сканеров" {
		t.Fatalf("sni_block must produce the restricted verdict, got %q", s.Verdict)
	}
}

// TestCheburcheck_ScannerErrorTolerance: one failing scanner must not fail the
// whole summary (warn + skip), while ALL failing scanners yield an error that
// mentions the API's info.
func TestCheburcheck_ScannerErrorTolerance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "tol-id"})
		case r.URL.Path == "/probe/tol-id":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("token") == "tok-a" {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 404, "info": "Not Found"})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				cheburSSE("result", `{"probe_id":"pb","verdicts":["ok"]}`) +
					cheburSSE("done", `{"id":"tol-id","response_count":1}`)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 3*time.Second, []ScannerCred{
		{Name: "msk-a", Token: "tok-a"},
		{Name: "msk-b", Token: "tok-b"},
	})
	cl.limiter = nil

	s, err := cl.CheckTarget(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("one failing scanner must not fail the summary, got: %v", err)
	}
	if len(s.Results) != 1 || s.Results[0].Name != "msk-b" || s.KindCounts["ok"] != 1 {
		t.Fatalf("expected only msk-b's result, got %+v", s)
	}

	// ALL scanners failing → error mentioning the last API info.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "tol2-id"})
		case r.URL.Path == "/probe/tol2-id":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 404, "info": "Not Found"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv2.Close()

	cl2 := NewCheburcheckClient(srv2.URL, 3*time.Second, []ScannerCred{
		{Name: "msk-a", Token: "tok-a"},
		{Name: "msk-b", Token: "tok-b"},
	})
	cl2.limiter = nil

	_, err = cl2.CheckTarget(context.Background(), "example.com")
	if err == nil {
		t.Fatalf("all-scanners-failed must return an error")
	}
	var httpErr *CheburHTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != 404 {
		t.Fatalf("error must wrap CheburHTTPError 404, got %v", err)
	}
	if !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("error must mention the API info, got %q", err.Error())
	}
}

// TestCheburcheck_PreStreamErrors: typed errors carry the API's {code,info}.
func TestCheburcheck_PreStreamErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/check" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 500, "info": "boom"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 2*time.Second, []ScannerCred{{Name: "s", Token: "t"}})
	cl.limiter = nil

	_, err := cl.CheckTarget(context.Background(), "example.com")
	var httpErr *CheburHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected typed CheburHTTPError, got %v", err)
	}
	if httpErr.Code != 500 || httpErr.Info != "boom" {
		t.Fatalf("typed error fields wrong: %+v", httpErr)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error message must include info, got %q", err.Error())
	}

	// probe 429 → typed error, message contains info, no auto-retry.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "id429"})
		case r.URL.Path == "/probe/id429":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 429, "info": "slow down"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv2.Close()

	cl2 := NewCheburcheckClient(srv2.URL, 2*time.Second, []ScannerCred{{Name: "s", Token: "t"}})
	cl2.limiter = nil

	_, err = cl2.CheckTarget(context.Background(), "example.com")
	if err == nil {
		t.Fatalf("429 must produce an error")
	}
	httpErr = nil
	if !errors.As(err, &httpErr) || httpErr.Code != 429 {
		t.Fatalf("expected typed CheburHTTPError 429, got %v", err)
	}
	if !strings.Contains(err.Error(), "slow down") {
		t.Fatalf("429 error message must include info, got %q", err.Error())
	}
}

// TestFormatCheburcheckReport_SummaryFirstAndCompact pins the wave G frozen
// layout: 🧀 header, Статус line, Ответы ratio, per-kind counts line with
// site labels, then the compact per-scanner section grouped by dominant
// verdict («+N» for hidden scanners, (кеш) markers, 12-line cap with
// «+ ещё N сканеров»).
func TestFormatCheburcheckReport_SummaryFirstAndCompact(t *testing.T) {
	t.Run("summary-first layout", func(t *testing.T) {
		s := &CheburcheckSummary{
			Target:       "cdn.example",
			OnlineProbes: 42,
			Answered:     42,
			KindCounts:   map[string]int{"cdn_block": 24, "ok": 16, "uncertain": 2},
			Results:      make([]CheburProbeResult, 42),
			Verdict:      "Не ограничен — ограничений не обнаружено",
		}
		out := FormatCheburcheckReport(s)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		want := []string{
			"🧀 <b>Cheburcheck</b>: <code>cdn.example</code>",
			"Статус: <i>Не ограничен — ограничений не обнаружено</i>",
			"Ответы: 42 из 42 сканеров",
			"• Доступен: 16 · CDN-блокировка: 24 · Неясно: 2",
		}
		for i, w := range want {
			if i >= len(lines) {
				t.Fatalf("report too short, want line %d %q in:\n%s", i, w, out)
			}
			if lines[i] != w {
				t.Errorf("line %d = %q, want %q\nfull report:\n%s", i, lines[i], w, out)
			}
		}
	})

	t.Run("compact grouping with plus-count", func(t *testing.T) {
		s := &CheburcheckSummary{
			Target:     "sni.example",
			KindCounts: map[string]int{"sni_block": 14},
			Verdict:    "Обнаружены ограничения: sni_block — 14 сканеров",
		}
		for i := 1; i <= 14; i++ {
			s.Results = append(s.Results, CheburProbeResult{Region: fmt.Sprintf("r%d", i), Verdicts: []string{"sni_block"}})
		}
		out := FormatCheburcheckReport(s)
		// First 3 distinct region identifiers, then the remaining scanner
		// count («Москва, СПб +12» site style).
		if !strings.Contains(out, "  • ⛔ SNI Блок: r1, r2, r3 +11\n") {
			t.Errorf("group line must list first idents + hidden scanner count, got:\n%s", out)
		}
		if !strings.Contains(out, "• SNI Блок: 14\n") {
			t.Errorf("counts line must carry the site label with count, got:\n%s", out)
		}
	})

	t.Run("cache marker kept", func(t *testing.T) {
		s := &CheburcheckSummary{
			Target:     "ok.example",
			KindCounts: map[string]int{"ok": 2},
			Verdict:    "Не ограничен — ограничений не обнаружено",
			Results: []CheburProbeResult{
				{Name: "scanA", Verdicts: []string{"ok"}, Cached: true},
				{Name: "scanB", Verdicts: []string{"ok"}},
			},
		}
		out := FormatCheburcheckReport(s)
		if !strings.Contains(out, "  • ✅ Доступен: scanA, scanB (кеш)\n") {
			t.Errorf("cache marker must be kept on the group line, got:\n%s", out)
		}
	})

	t.Run("detail capped at 12 lines", func(t *testing.T) {
		s := &CheburcheckSummary{Target: "many.example", KindCounts: map[string]int{}}
		for i := 1; i <= 15; i++ {
			k := fmt.Sprintf("u%d", i) // unknown kinds -> 15 distinct groups
			s.KindCounts[k] = 1
			s.Results = append(s.Results, CheburProbeResult{Region: fmt.Sprintf("reg%d", i), Verdicts: []string{k}})
		}
		s.Verdict = "Не ограничен — ограничений не обнаружено"
		out := FormatCheburcheckReport(s)
		if n := strings.Count(out, "\n  • "); n != 12 {
			t.Errorf("detail section must cap at 12 group lines, got %d in:\n%s", n, out)
		}
		if !strings.Contains(out, "  + ещё 3 сканеров\n") {
			t.Errorf("dropped groups must be summarized as «+ ещё N сканеров», got:\n%s", out)
		}
	})
}

// TestFormatCheburcheckReport_EscapesExternalData pins the F6 lesson: every
// external string (target, scanner name, region, provider, verdict fallback,
// hand-set Verdict) is html-escaped exactly once in the Telegram HTML report.
// Wave G rework: grouped detail — ident priority is region > provider > name >
// probe id; an unknown verdict kind becomes its own group labeled by the raw
// (escaped) kind; the Verdict renders escaped in the Статус line.
func TestFormatCheburcheckReport_EscapesExternalData(t *testing.T) {
	s := &CheburcheckSummary{
		Target:       "ex<b>ample.com",
		OnlineProbes: 4,
		Answered:     4,
		KindCounts:   map[string]int{"ok": 1, "cdn_block": 1, "tspu_block": 1, "weird<v": 1},
		Results: []CheburProbeResult{
			{Name: "S<b>1", ASN: "AS123", Verdicts: []string{"ok"}, Cached: true},
			{Region: "A<b>", Verdicts: []string{"cdn_block"}},
			{Provider: "P<i>", Verdicts: []string{"tspu_block"}},
			{Verdicts: []string{"weird<v"}},
		},
		Verdict: "Обнаружены ограничения: sni_<b>block — 1 сканеров",
	}
	out := FormatCheburcheckReport(s)

	for _, want := range []string{
		"ex&lt;b&gt;ample.com",
		"S&lt;b&gt;1",
		"A&lt;b&gt;",
		"P&lt;i&gt;",
		"weird&lt;v",
		"(кеш)",
		"Статус:",
		"sni_&lt;b&gt;block",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing escaped %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"ex<b>ample.com", "S<b>1", "A<b>", "P<i>", "weird<v", "sni_<b>block"} {
		if strings.Contains(out, bad) {
			t.Errorf("report contains UNESCAPED external data %q in:\n%s", bad, out)
		}
	}
}

// TestCheburcheck_APIKeyModeSingleStreamAllScanners pins the API-key probe
// mode (CHEBURCHECK_API_KEY): one Bearer-authenticated /probe stream with NO
// token query param; the server fans the probe out to ALL online public
// scanners, so MULTIPLE result events in that single stream must each be
// collected. Scanner credentials present on the client are ignored.
// Wave G: started.online_probes → OnlineProbes, done.response_count →
// Answered; counts are per verdict kind.
func TestCheburcheck_APIKeyModeSingleStreamAllScanners(t *testing.T) {
	var mu sync.Mutex
	var probeAuths []string
	var probeTokenParams []string
	var probeCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "keyjob"})
		case r.URL.Path == "/probe/keyjob":
			probeCalls++
			probeAuths = append(probeAuths, r.Header.Get("Authorization"))
			probeTokenParams = append(probeTokenParams, r.URL.Query().Get("token"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				cheburSSE("started", `{"id":"keyjob","target":"example.com","online_probes":9}`) +
					cheburSSE("result", `{"job_id":"j1","probe_id":"pub-1","cached":false,"verdicts":["ok"],"region":"moscow","provider":"prov-1","asn":"AS1000"}`) +
					cheburSSE("result", `{"job_id":"j2","probe_id":"pub-2","cached":false,"verdicts":["sni_block"],"region":"spb","provider":"prov-2"}`) +
					cheburSSE("done", `{"id":"keyjob","status":"finished","response_count":2,"online_probes":9}`)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 5*time.Second, []ScannerCred{
		{Name: "msk-a", Token: "tok-a"},
		{Name: "msk-b", Token: "tok-b"},
	})
	cl.SetAPIKey("test-key")
	cl.limiter = nil

	s, err := cl.CheckTarget(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Results) != 2 {
		t.Fatalf("expected both public-scanner results from the single key-mode stream, got %d: %+v", len(s.Results), s.Results)
	}
	if s.KindCounts["ok"] != 1 || s.KindCounts["sni_block"] != 1 || len(s.KindCounts) != 2 {
		t.Errorf("kind counts wrong: %v", s.KindCounts)
	}
	if s.OnlineProbes != 9 || s.Answered != 2 {
		t.Errorf("started/done stats wrong: online=%d answered=%d", s.OnlineProbes, s.Answered)
	}

	mu.Lock()
	defer mu.Unlock()
	if probeCalls != 1 {
		t.Fatalf("key mode must issue exactly ONE /probe request, got %d", probeCalls)
	}
	if probeAuths[0] != "Bearer test-key" {
		t.Errorf("expected Authorization header 'Bearer test-key', got %q", probeAuths[0])
	}
	if probeTokenParams[0] != "" {
		t.Errorf("key-mode /probe must NOT carry a token query param, got %q", probeTokenParams[0])
	}
}

// TestCheburcheck_APIKeyWinsOverScanners: when BOTH an API key and scanner
// credentials are configured, the key path wins — a single /probe request
// with the Bearer header, no per-scanner token requests.
func TestCheburcheck_APIKeyWinsOverScanners(t *testing.T) {
	var mu sync.Mutex
	var probeCalls int
	var sawAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "winjob"})
		case r.URL.Path == "/probe/winjob":
			probeCalls++
			sawAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				cheburSSE("result", `{"probe_id":"pub","verdicts":["ok"]}`) +
					cheburSSE("done", `{"id":"winjob","response_count":1}`)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 3*time.Second, []ScannerCred{{Name: "msk-a", Token: "tok-a"}})
	cl.SetAPIKey("test-key")
	cl.limiter = nil

	if _, err := cl.CheckTarget(context.Background(), "example.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if probeCalls != 1 {
		t.Fatalf("key-wins mode must issue exactly ONE /probe request, got %d", probeCalls)
	}
	if sawAuth != "Bearer test-key" {
		t.Errorf("single probe must use the API key, got Authorization %q", sawAuth)
	}
}

// TestCheburcheck_APIKeyPreStream403: a rejected key (403 {code,info})
// surfaces as the typed pre-stream error including the API info, and the
// probe is NOT retried.
func TestCheburcheck_APIKeyPreStream403(t *testing.T) {
	var mu sync.Mutex
	var probeCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "deny403"})
		case r.URL.Path == "/probe/deny403":
			probeCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 403, "info": "invalid api key"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := NewCheburcheckClient(srv.URL, 2*time.Second, nil)
	cl.SetAPIKey("test-key")
	cl.limiter = nil

	_, err := cl.CheckTarget(context.Background(), "example.com")
	if err == nil {
		t.Fatalf("403 key rejection must produce an error")
	}
	var httpErr *CheburHTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != 403 {
		t.Fatalf("expected typed CheburHTTPError 403, got %v", err)
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("error must include the API info, got %q", err.Error())
	}

	mu.Lock()
	defer mu.Unlock()
	if probeCalls != 1 {
		t.Fatalf("probe must not be retried after a 403, got %d calls", probeCalls)
	}
}

// TestCheburcheck_HasCredentialsMatrix pins the feature-activation gate:
// neither key nor scanners -> unconfigured (ErrCheburInactive); key only or
// scanners only -> configured. HasActiveScanners keeps its scanner-specific
// meaning (an API key alone is NOT an active scanner).
func TestCheburcheck_HasCredentialsMatrix(t *testing.T) {
	empty := NewCheburcheckClient("", time.Second, nil)
	if empty.HasCredentials() {
		t.Fatalf("client with no key and no scanners must be unconfigured")
	}
	if empty.HasActiveScanners() {
		t.Fatalf("client with no scanners must report HasActiveScanners=false")
	}
	if _, err := empty.CheckTarget(context.Background(), "example.com"); !errors.Is(err, ErrCheburInactive) {
		t.Fatalf("expected ErrCheburInactive, got %v", err)
	}

	keyOnly := NewCheburcheckClient("", time.Second, nil)
	keyOnly.SetAPIKey("test-key")
	if !keyOnly.HasCredentials() {
		t.Fatalf("API key alone must count as configured")
	}
	if keyOnly.HasActiveScanners() {
		t.Fatalf("API key must NOT count as an active scanner")
	}

	scannersOnly := NewCheburcheckClient("", time.Second, []ScannerCred{{Name: "s", Token: "t"}})
	if !scannersOnly.HasCredentials() || !scannersOnly.HasActiveScanners() {
		t.Fatalf("scanner credentials must keep reporting configured and HasActiveScanners=true")
	}
}

// TestCheburcheck_KeyModeResultLabeling: key-mode results arrive with Name=""
// (no per-scanner credential). Wave G grouped report: the scanner identifier
// prefers the region, then provider, then probe id — the old "probe #pub-9"
// expectation is REPLACED (register entry) because the region is the primary
// compact identifier per the frozen format («Москва, СПб +12»).
func TestCheburcheck_KeyModeResultLabeling(t *testing.T) {
	s := &CheburcheckSummary{
		Target: "example.com",
		Results: []CheburProbeResult{
			{ProbeID: "pub-9", Region: "moscow", Provider: "prov-x", Verdicts: []string{"ok"}},
			{ProbeID: "pub-3", Verdicts: []string{"dns_spoofing"}},
		},
		Verdict: "Обнаружены ограничения: dns_spoofing — 1 сканеров",
	}
	out := FormatCheburcheckReport(s)
	if !strings.Contains(out, "moscow") {
		t.Errorf("region-bearing result must be labeled by its region, got:\n%s", out)
	}
	if strings.Contains(out, "probe #pub-9") {
		t.Errorf("region must win over probe id in the grouped ident, got:\n%s", out)
	}
	if !strings.Contains(out, "Подмена DNS: probe #pub-3") {
		t.Errorf("result without region/provider/name must fall back to probe id, got:\n%s", out)
	}
	if strings.Contains(out, "()") {
		t.Errorf("report must not contain an empty () label pair, got:\n%s", out)
	}
}

// TestEnrichVerdictWithChebur: deep-diag enrichment appends ONLY real
// restrictions (sni_block/tspu_block/dns_spoofing) with per-kind counts; the
// CDN family never reads as «blocked», and a summary without real
// restrictions keeps the offline-contradiction note (ok scanners CONTRADICT
// the offline status — local-vs-RF asymmetry).
// Replaced-test register (wave G, user-authorized rework): old assertions
// " | Cheburcheck: все сканеры видят доступность" and
// "Cheburcheck: sni_block" + "1/2" are REPLACED by the site-style wording.
func TestEnrichVerdictWithChebur(t *testing.T) {
	if got := EnrichVerdictWithChebur("Нода не отвечает", nil); got != "Нода не отвечает" {
		t.Fatalf("nil summary must not change verdict, got %q", got)
	}

	// Real restriction: kind + count appended, original verdict preserved.
	sni := &CheburcheckSummary{
		KindCounts: map[string]int{"sni_block": 1, "ok": 1},
		Results: []CheburProbeResult{
			{Name: "a", Verdicts: []string{"sni_block"}},
			{Name: "b", Verdicts: []string{"ok"}},
		},
	}
	got := EnrichVerdictWithChebur("Нода не отвечает", sni)
	if !strings.Contains(got, "Cheburcheck: обнаружены ограничения: sni_block — 1 сканеров") {
		t.Fatalf("real restriction must be appended with its kind count, got %q", got)
	}
	if !strings.HasPrefix(got, "Нода не отвечает") {
		t.Fatalf("original verdict must be preserved, got %q", got)
	}

	// The user-rejected live case: a cdn_block-heavy host must NOT read as
	// blocked; ok scanners keep the contradiction note with the ok ratio.
	cdnHeavy := &CheburcheckSummary{
		KindCounts: map[string]int{"cdn_block": 24, "ok": 16, "uncertain": 2},
		Results:    make([]CheburProbeResult, 42),
	}
	got = EnrichVerdictWithChebur("Нода не отвечает", cdnHeavy)
	if !strings.Contains(got, "Cheburcheck: сканеры РФ видят доступность (16/42)") {
		t.Fatalf("cdn-heavy summary must keep the contradiction note with the ok ratio, got %q", got)
	}
	if strings.Contains(got, "обнаружены ограничения") || strings.Contains(got, "cdn_block") {
		t.Fatalf("CDN family is informational and must not surface as a restriction, got %q", got)
	}

	// Defensive: a consumer-built summary without KindCounts is still
	// enriched via counts derived from Results.
	derived := &CheburcheckSummary{Results: []CheburProbeResult{{Verdicts: []string{"tspu_block"}}}}
	if got := EnrichVerdictWithChebur("V", derived); !strings.Contains(got, "tspu_block — 1 сканеров") {
		t.Fatalf("results-only summary must derive kind counts, got %q", got)
	}
}
