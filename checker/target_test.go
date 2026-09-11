package checker

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTargetManager_AddRemove(t *testing.T) {
	tm := NewTargetManager([]string{
		"https://cp.cloudflare.com/generate_204",
		"https://www.gstatic.com/generate_204",
	})

	targets := tm.GetTargets()
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}

	err := tm.AddTarget("https://example.com/check")
	if err != nil {
		t.Fatalf("AddTarget failed: %v", err)
	}
	if len(tm.GetTargets()) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(tm.GetTargets()))
	}

	// Duplicate add
	err = tm.AddTarget("https://example.com/check")
	if err == nil {
		t.Errorf("expected error on duplicate target, got nil")
	}

	// Remove target
	err = tm.RemoveTarget("https://example.com/check")
	if err != nil {
		t.Fatalf("RemoveTarget failed: %v", err)
	}
	if len(tm.GetTargets()) != 2 {
		t.Fatalf("expected 2 targets after removal, got %d", len(tm.GetTargets()))
	}
}

func TestCheckSingleTarget(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	client := ts.Client()
	client.Timeout = 2 * time.Second

	res := CheckSingleTarget(client, ts.URL)
	if !res.Success {
		t.Errorf("expected success, got error: %s", res.Error)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", res.StatusCode)
	}
	if res.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", res.Latency)
	}
}
