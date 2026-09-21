package nodes

import (
	"net/http"
	"net/http/httptest"
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

