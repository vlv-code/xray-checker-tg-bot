package telegram

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStatsStore_RecordAndAggregate(t *testing.T) {
	tmpDir := t.TempDir()
	statsPath := filepath.Join(tmpDir, "stats.json")

	store, err := NewStatsStore(statsPath)
	if err != nil {
		t.Fatalf("NewStatsStore failed: %v", err)
	}

	// 1. Initial check - all online
	store.RecordCheck("proxy-1", "Proxy One", true, 100)
	store.RecordCheck("proxy-2", "Proxy Two", true, 150)

	uptime1 := store.GetUptimePercent("proxy-1")
	if uptime1 != 100.0 {
		t.Errorf("expected 100%% uptime, got %.2f%%", uptime1)
	}

	// 2. Simulate proxy-1 going down
	downTime := time.Now().Add(-10 * time.Minute)
	store.RecordTransition("proxy-1", "Proxy One", false, "EOF / reset", downTime)

	// Check that incident is recorded as open
	incidents := store.GetRecentIncidents(10)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	if incidents[0].UpAt != 0 {
		t.Errorf("expected UpAt to be 0 for open incident, got %d", incidents[0].UpAt)
	}
	if incidents[0].Reason != "EOF / reset" {
		t.Errorf("expected reason 'EOF / reset', got %s", incidents[0].Reason)
	}

	// 3. Simulate proxy-1 recovering
	upTime := time.Now()
	store.RecordTransition("proxy-1", "Proxy One", true, "", upTime)

	incidents = store.GetRecentIncidents(10)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	if incidents[0].UpAt == 0 {
		t.Errorf("expected UpAt to be non-zero after recovery")
	}
	if incidents[0].DurationSec <= 0 {
		t.Errorf("expected positive duration, got %d", incidents[0].DurationSec)
	}

	// 4. Persistence verification
	err = store.Save()
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	store2, err := NewStatsStore(statsPath)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	reloadedIncidents := store2.GetRecentIncidents(10)
	if len(reloadedIncidents) != 1 {
		t.Fatalf("expected 1 reloaded incident, got %d", len(reloadedIncidents))
	}
	if reloadedIncidents[0].ProxyName != "Proxy One" {
		t.Errorf("expected Proxy One, got %s", reloadedIncidents[0].ProxyName)
	}
}
