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

func TestStatsStore_RecordInitialDown(t *testing.T) {
	tmpDir := t.TempDir()
	statsPath := filepath.Join(tmpDir, "stats.json")

	store, err := NewStatsStore(statsPath)
	if err != nil {
		t.Fatalf("NewStatsStore failed: %v", err)
	}

	now := time.Now()
	store.RecordInitialDown("dead-1", "Dead Node", now)

	ps, exists := store.Stats["dead-1"]
	if !exists {
		t.Fatalf("expected stats entry for dead-1")
	}
	if !ps.CurrentlyDown {
		t.Errorf("expected CurrentlyDown to be true")
	}
	if ps.CurrentDownAt != now.Unix() {
		t.Errorf("expected CurrentDownAt %d, got %d", now.Unix(), ps.CurrentDownAt)
	}
	if ps.DropCount != 1 {
		t.Errorf("expected DropCount 1, got %d", ps.DropCount)
	}

	incidents := store.GetRecentIncidents(10)
	if len(incidents) != 1 || incidents[0].Reason != "Offline at startup" {
		t.Errorf("expected startup incident, got %v", incidents)
	}
}

func TestRollingStats_Windows(t *testing.T) {
	rs := NewRollingStats(1000)
	now := time.Unix(1700000000, 0) // Fixed reference time

	// proxy1: 100% online
	rs.Record("p1", true, now.Add(-24*time.Hour).Unix())
	uptime1 := rs.UptimePercent24h("p1", now)
	if uptime1 < 99.9 {
		t.Errorf("expected ~100%% uptime for p1, got %.2f%%", uptime1)
	}

	// proxy2: went down 1 hour ago and stayed down
	rs.Record("p2", true, now.Add(-24*time.Hour).Unix())
	rs.Record("p2", false, now.Add(-1*time.Hour).Unix())
	uptime2 := rs.UptimePercent24h("p2", now)
	if uptime2 < 95.0 || uptime2 > 96.0 {
		t.Errorf("expected ~95.8%% uptime for p2, got %.2f%%", uptime2)
	}

	// proxy3: down for entire 24h
	rs.Record("p3", false, now.Add(-25*time.Hour).Unix())
	uptime3 := rs.UptimePercent24h("p3", now)
	if uptime3 != 0.0 {
		t.Errorf("expected 0%% uptime for p3, got %.2f%%", uptime3)
	}

	// Average across active
	avg := rs.UptimePercent24hAll(now, []string{"p1", "p2", "p3"})
	if avg < 64.0 || avg > 66.0 {
		t.Errorf("expected ~65.3%% avg uptime, got %.2f%%", avg)
	}
}

func TestLatencySamples_PercentilesAndJitter(t *testing.T) {
	ls := NewLatencySamples(50)
	if ls.Count() != 0 {
		t.Errorf("expected count 0, got %d", ls.Count())
	}

	for i := 1; i <= 50; i++ {
		ls.Add(float64(i))
	}

	if ls.Count() != 50 {
		t.Errorf("expected count 50, got %d", ls.Count())
	}

	p50 := ls.Percentile(0.5)
	if p50 < 24.0 || p50 > 26.0 {
		t.Errorf("expected p50 around 25, got %.2f", p50)
	}

	p95 := ls.Percentile(0.95)
	if p95 < 46.0 || p95 > 49.0 {
		t.Errorf("expected p95 around 47-48, got %.2f", p95)
	}

	stddev := ls.StdDev()
	if stddev < 14.0 || stddev > 15.0 {
		t.Errorf("expected stddev around 14.4, got %.2f", stddev)
	}
}
