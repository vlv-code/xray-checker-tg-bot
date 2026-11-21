package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xray-checker/nodes"
	"xray-checker/subscription"
)

// TestSafeGoBody_RunsFunctionAndRecoversPanic covers the synchronous core of
// safeGo: the function must run, and a panic inside it must be recovered so
// a panicking worker never brings the whole process down.
func TestSafeGoBody_RunsFunctionAndRecoversPanic(t *testing.T) {
	ran := false
	safeGoBody("test-normal", func() { ran = true })
	if !ran {
		t.Fatal("expected safeGoBody to run the function synchronously")
	}

	// A panicking function must not let the panic escape safeGoBody.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped safeGoBody: %v", r)
		}
	}()
	safeGoBody("test-panic", func() { panic("boom") })
}

// TestSafeGo_RecoversPanicInGoroutine verifies the spawned goroutine path:
// a panic inside the launched function must be contained in that goroutine.
// Without the recover, this test process crashes (which counts as a failure).
func TestSafeGo_RecoversPanicInGoroutine(t *testing.T) {
	entered := make(chan struct{})
	safeGo("test-goroutine-panic", func() {
		close(entered)
		panic("boom in goroutine")
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo goroutine never ran the function")
	}
	// Give the goroutine a moment to panic; if the recover is missing the
	// whole test binary dies before reaching this point.
	time.Sleep(50 * time.Millisecond)
}

// TestFlushReporterOnce_DeliversReportBeforeReturn pins the --run-once
// contract: when flushReporterOnce returns, the master must have already
// received the node report. The daemon path is fire-and-forget and loses the
// report there, because main returns right after the single check iteration.
func TestFlushReporterOnce_DeliversReportBeforeReturn(t *testing.T) {
	var reports atomic.Int32
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reports.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer master.Close()

	reporter := nodes.NewReporter(master.URL+"/api/v1/nodes/report", "test-token")
	payload := nodes.BuildReportFromDiag(nil, "test", 30, "ip", "203.0.113.10")

	if _, err := flushReporterOnce(reporter, payload); err != nil {
		t.Fatalf("flushReporterOnce returned error: %v", err)
	}
	if got := reports.Load(); got == 0 {
		t.Fatal("master received 0 reports after flushReporterOnce returned: report was delivered asynchronously and lost")
	}
}

// TestFlushReporterOnce_PermanentErrorNotRetried: a permanent HTTP 400 must
// surface as an error without retrying (SendWithRetry contract), i.e. exactly
// one request hits the master.
func TestFlushReporterOnce_PermanentErrorNotRetried(t *testing.T) {
	var requests atomic.Int32
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer master.Close()

	reporter := nodes.NewReporter(master.URL+"/api/v1/nodes/report", "test-token")
	payload := nodes.BuildReportFromDiag(nil, "test", 30, "ip", "203.0.113.10")

	if _, err := flushReporterOnce(reporter, payload); err == nil {
		t.Fatal("expected an error for permanent HTTP 400 response")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected exactly 1 request for a permanent error, got %d", got)
	}
}

// Regression (Guardian P0-2, 2026-11-16): the check-scheduler shutdown defer
// holds checkSchedulerMu while gocron Stop() waits for the running singleton
// check job; the job's interval read used to re-acquire checkSchedulerMu —
// a circular wait that hung the whole LIFO shutdown chain (bot stop, xray
// stop, everything after it). Contract: the interval read inside the check
// job must need ONLY RuntimeMu. Compile-RED seam: currentCheckInterval did
// not exist before the fix (the read was inline under checkSchedulerMu).
func TestCurrentCheckIntervalNeedsOnlyRuntimeMu(t *testing.T) {
	checkSchedulerMu.Lock()
	defer checkSchedulerMu.Unlock()

	done := make(chan int, 1)
	go func() { done <- currentCheckInterval() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("interval read blocked on checkSchedulerMu — shutdown deadlock regression")
	}
}

// TestMasterReportSecurityNote pins the operator warning text for the
// plaintext node-report fallback: with MASTER_PUBLIC_URL unset, /nodeadd
// instructions tell nodes to report to http://<host-ip>:2112 — tokens ride
// cleartext, so the master must log a TLS-fronting hint at startup. A
// configured URL silences the note. Only the pure note function is pinned
// here; the logging call site itself is not tested.
func TestMasterReportSecurityNote(t *testing.T) {
	cases := []struct {
		name         string
		masterURL    string
		wantContains string // "" means the note must be empty
	}{
		{"unset URL returns plaintext warning", "", "MASTER_PUBLIC_URL is not set"},
		{"configured URL returns empty note", "https://master.example.com/api/v1/nodes/report", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := masterReportSecurityNote(tc.masterURL)
			if tc.wantContains == "" {
				if got != "" {
					t.Fatalf("expected empty note for url %q, got %q", tc.masterURL, got)
				}
				return
			}
			if !strings.Contains(got, tc.wantContains) {
				t.Fatalf("expected note containing %q for url %q, got %q", tc.masterURL, tc.wantContains, got)
			}
		})
	}
}

// Wave N-G FIX 2: freshness telemetry honesty. reloadSubscriptions used to
// stamp URLStore.RecordUpdate(LastUpdate=now) for EVERY source in the counts
// map — including sources that FAILED and only served last-known-good cached
// configs, presenting stale proxies as freshly fetched in the /subs view.
// Contract: only sources that actually refreshed get a record; a failed
// source keeps its previous (honest) stamp untouched, or stays unknown when
// it never succeeded (the view omits the freshness line for zero LastUpdate).
func TestRecordSubFreshness_SkipsFailedSources(t *testing.T) {
	const okURL = "https://sub.example/ok"
	const failedURL = "https://sub.example/failing"

	store, err := subscription.NewURLStore([]string{okURL, failedURL}, "") // no persistence
	if err != nil {
		t.Fatalf("NewURLStore: %v", err)
	}
	now := time.Now()

	counts := map[string]int{okURL: 3, failedURL: 2} // failed source's cached count IS in counts
	failed := map[string]bool{failedURL: true}
	recordSubFreshness(store, counts, failed, now)

	if meta, ok := store.GetMeta(failedURL); ok {
		t.Errorf("failed-but-cached source must not be recorded (stale data presented as fresh), got meta %+v", meta)
	}
	meta, ok := store.GetMeta(okURL)
	if !ok || meta.Count != 3 || meta.LastUpdate.IsZero() {
		t.Errorf("healthy source must be recorded, got meta %+v ok=%v", meta, ok)
	}

	// A previously recorded source that later fails keeps its last honest
	// stamp: no LastUpdate=now overwrite, no count/delta clobbering.
	store.RecordUpdate(okURL, 3, now)
	later := now.Add(time.Minute)
	recordSubFreshness(store, map[string]int{okURL: 2}, map[string]bool{okURL: true}, later)
	meta, _ = store.GetMeta(okURL)
	if !meta.LastUpdate.Equal(now) || meta.Count != 3 {
		t.Errorf("previous stamp of a later-failed source must survive untouched, got %+v", meta)
	}
}
