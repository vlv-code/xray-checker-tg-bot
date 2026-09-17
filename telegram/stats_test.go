package telegram

import (
	"math"
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

	uptime1, ok := store.GetUptimePercent("proxy-1")
	if !ok || uptime1 != 100.0 {
		t.Errorf("expected 100%% uptime, got %.2f%% (ok=%v)", uptime1, ok)
	}
	if _, ok := store.GetUptimePercent("non-existent"); ok {
		t.Errorf("expected ok=false for non-existent proxy")
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

func TestLatencySamples_Interpolation(t *testing.T) {
	ls := NewLatencySamples(10)
	if p := ls.Percentile(0.5); p != 0 {
		t.Errorf("expected 0 for empty samples, got %v", p)
	}

	// Single sample
	ls.Add(42.0)
	if p := ls.Percentile(0.5); p != 42.0 {
		t.Errorf("expected 42.0 for single sample, got %v", p)
	}
	if p := ls.Percentile(0.0); p != 42.0 {
		t.Errorf("expected 42.0 for p=0, got %v", p)
	}
	if p := ls.Percentile(1.0); p != 42.0 {
		t.Errorf("expected 42.0 for p=1, got %v", p)
	}

	// 5 samples: 10, 20, 30, 40, 50
	ls2 := NewLatencySamples(10)
	for _, v := range []float64{10, 20, 30, 40, 50} {
		ls2.Add(v)
	}
	// p50: r = 4 * 0.5 = 2.0 -> 30.0
	if p50 := ls2.Percentile(0.5); p50 != 30.0 {
		t.Errorf("expected p50 = 30.0, got %v", p50)
	}
	// p95: r = 4 * 0.95 = 3.8 -> 40 + 0.8 * 10 = 48.0
	if p95 := ls2.Percentile(0.95); math.Abs(p95-48.0) > 1e-9 {
		t.Errorf("expected p95 = 48.0, got %v", p95)
	}
	// p99: r = 4 * 0.99 = 3.96 -> 40 + 0.96 * 10 = 49.6
	if p99 := ls2.Percentile(0.99); math.Abs(p99-49.6) > 1e-9 {
		t.Errorf("expected p99 = 49.6, got %v", p99)
	}
}

func TestStatsStore_DuplicateDown_SingleIncident(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("NewStatsStore failed: %v", err)
	}

	now := time.Now()
	// Call RecordTransition down multiple times in a row
	store.RecordTransition("p-dup", "Proxy Dup", false, "Fail 1", now)
	store.RecordTransition("p-dup", "Proxy Dup", false, "Fail 2", now.Add(10*time.Second))
	store.RecordTransition("p-dup", "Proxy Dup", false, "Fail 3", now.Add(20*time.Second))

	ps := store.GetProxyStats("p-dup")
	if ps.DropCount != 1 {
		t.Errorf("expected DropCount 1, got %d", ps.DropCount)
	}
	if ps.CurrentDownAt != now.Unix() {
		t.Errorf("expected CurrentDownAt to remain original %d, got %d", now.Unix(), ps.CurrentDownAt)
	}

	incidents := store.GetRecentIncidents(10)
	if len(incidents) != 1 {
		t.Fatalf("expected exactly 1 incident, got %d", len(incidents))
	}
	if incidents[0].Reason != "Fail 1" {
		t.Errorf("expected reason 'Fail 1', got %s", incidents[0].Reason)
	}

	// Recovery
	downtime := store.RecordTransition("p-dup", "Proxy Dup", true, "", now.Add(60*time.Second))
	if downtime != 60*time.Second {
		t.Errorf("expected downtime 60s, got %v", downtime)
	}
	incidents = store.GetRecentIncidents(10)
	if incidents[0].UpAt == 0 || incidents[0].DurationSec != 60 {
		t.Errorf("expected incident to be closed with duration 60s, got UpAt=%d DurationSec=%d",
			incidents[0].UpAt, incidents[0].DurationSec)
	}
}

func TestStatsStore_FlapperQuota(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("NewStatsStore failed: %v", err)
	}

	now := time.Now()
	// Add an incident from a normal proxy
	store.RecordTransition("normal-node", "Normal Node", false, "timeout", now.Add(-10*time.Hour))
	store.RecordTransition("normal-node", "Normal Node", true, "", now.Add(-9*time.Hour))

	// Flapper generates 30 down/up cycles
	for i := 0; i < 30; i++ {
		tDown := now.Add(time.Duration(i*2) * time.Minute)
		tUp := tDown.Add(1 * time.Minute)
		store.RecordTransition("flapper", "Flapper Node", false, "flap", tDown)
		store.RecordTransition("flapper", "Flapper Node", true, "", tUp)
	}

	incidents := store.GetRecentIncidents(100)
	flapperCount := 0
	normalFound := false
	for _, inc := range incidents {
		if inc.StableID == "flapper" {
			flapperCount++
		}
		if inc.StableID == "normal-node" {
			normalFound = true
		}
	}

	if flapperCount > 15 {
		t.Errorf("expected flapper count <= 15 due to per-proxy quota, got %d", flapperCount)
	}
	if !normalFound {
		t.Errorf("expected normal-node incident to be preserved, but it was evicted")
	}
}

func TestStatsStore_PersistenceOnDisk(t *testing.T) {
	tmpDir := t.TempDir()
	statsPath := filepath.Join(tmpDir, "stats.json")

	store, err := NewStatsStore(statsPath)
	if err != nil {
		t.Fatalf("NewStatsStore failed: %v", err)
	}

	downTime := time.Now().Add(-5 * time.Minute)
	store.RecordTransition("persist-1", "Persist Node", false, "connection refused", downTime)
	store.RecordTransition("persist-1", "Persist Node", true, "", time.Now())

	// Reload from the same file in a brand new store
	reloaded, err := NewStatsStore(statsPath)
	if err != nil {
		t.Fatalf("NewStatsStore reload failed: %v", err)
	}

	incidents := reloaded.GetRecentIncidents(10)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident after reload, got %d", len(incidents))
	}
	if incidents[0].StableID != "persist-1" {
		t.Errorf("expected stable_id 'persist-1', got %q", incidents[0].StableID)
	}
	if incidents[0].Reason != "connection refused" {
		t.Errorf("expected reason 'connection refused', got %q", incidents[0].Reason)
	}
	ps := reloaded.GetProxyStats("persist-1")
	if ps == nil {
		t.Fatalf("expected ProxyStats for 'persist-1' after reload, got nil")
	}
	if ps.DropCount != 1 {
		t.Errorf("expected DropCount=1 after reload, got %d", ps.DropCount)
	}
}
