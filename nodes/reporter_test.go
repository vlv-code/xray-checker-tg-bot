package nodes

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReporterSend(t *testing.T) {
	var gotAuth, gotPath, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		w.Write([]byte(`{"managedSubs":["https://sub.example/one"]}`))
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL+"/api/v1/nodes/report", "secrettoken")
	resp, err := rep.Send(ReportPayload{Version: "v", CheckIntervalSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/nodes/report" {
		t.Errorf("path: %s", gotPath)
	}
	if gotAuth != "Bearer secrettoken" {
		t.Errorf("auth header: %s", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type: %s", gotCT)
	}
	if len(resp.ManagedSubs) != 1 || resp.ManagedSubs[0] != "https://sub.example/one" {
		t.Errorf("managed list: %v", resp.ManagedSubs)
	}
}

func TestReporterSendErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL, "tok")
	if _, err := rep.Send(ReportPayload{}); err == nil {
		t.Error("401 must be an error")
	}
	if _, err := NewReporter("http://127.0.0.1:1/", "tok").Send(ReportPayload{}); err == nil {
		t.Error("connection failure must be an error")
	}
}

func TestReporterSendWithRetry_SuccessAfterTransientFailure(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"managedSubs":["https://sub.example/retry"]}`))
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL, "tok")
	resp, err := rep.SendWithRetry(ReportPayload{}, []time.Duration{10 * time.Millisecond})
	if err != nil {
		t.Fatalf("expected success on retry, got err: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if len(resp.ManagedSubs) != 1 || resp.ManagedSubs[0] != "https://sub.example/retry" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestReporterSendWithRetry_DoesNotRetry401(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL, "tok")
	_, err := rep.SendWithRetry(ReportPayload{}, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt (no retries on 401), got %d", attempts)
	}
}

// countingRT is a RoundTripper that always fails with a fixed error,
// counting attempts — lets tests inject transport-level failures.
type countingRT struct {
	attempts int
	err      error
}

func (rt *countingRT) RoundTrip(*http.Request) (*http.Response, error) {
	rt.attempts++
	return nil, rt.err
}

// Adversarial: a transport error that merely CONTAINS the text "HTTP 401"
// (e.g. an intermediary's message wrapped in a network error) must not be
// classified as a permanent master rejection — only a real master status
// answer may stop retries.
func TestReporterSendWithRetry_WrappedSubstringErrorIsRetried(t *testing.T) {
	rt := &countingRT{err: fmt.Errorf("dial tcp: gateway interposed HTTP 401 during CONNECT")}
	rep := NewReporter("http://127.0.0.1:1/", "tok")
	rep.client = &http.Client{Transport: rt}

	_, err := rep.SendWithRetry(ReportPayload{}, []time.Duration{time.Millisecond, time.Millisecond})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if rt.attempts != 3 {
		t.Fatalf("wrapped error mentioning \"HTTP 401\" must stay transient and be retried, got %d attempts", rt.attempts)
	}
}

func TestReporterSendWithRetry_DoesNotRetry400(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL, "tok")
	_, err := rep.SendWithRetry(ReportPayload{}, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond})
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt (no retries on 400), got %d", attempts)
	}
	// The permanent error must surface the exact user-visible master message.
	if got := err.Error(); got != "master answered HTTP 400" {
		t.Fatalf("unexpected permanent error text: %q", got)
	}
}

func TestReporterSendWithRetry_Retries500UntilExhausted(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL, "tok")
	backoffs := []time.Duration{time.Millisecond, time.Millisecond}
	_, err := rep.SendWithRetry(ReportPayload{}, backoffs)
	if err == nil {
		t.Fatal("expected error after exhausting retries on 500")
	}
	if attempts != len(backoffs)+1 {
		t.Fatalf("500 must be retried per backoff config, got %d attempts, want %d", attempts, len(backoffs)+1)
	}
	if !strings.Contains(err.Error(), "failed after 3 attempts") {
		t.Fatalf("exhausted retries must wrap attempts count, got: %v", err)
	}
}
