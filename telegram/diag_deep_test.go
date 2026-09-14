package telegram

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

func TestSortDiagnosticsReports_SeverityOrder(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := NewAlertTracker(filepath.Join(tmpDir, "alerts.json"))

	// Setup fake downtime in tracker
	now := time.Now()
	tracker.Track(1, 100, "off-short", "OfflineShort", now.Add(-10*time.Minute), "timeout")
	tracker.Track(1, 101, "off-long", "OfflineLong", now.Add(-2*time.Hour), "timeout")

	b := &Bot{
		tracker: tracker,
	}

	reports := []checker.ProxyDiagReport{
		{ProxyName: "OnlineSlow", StableID: "on-slow", Status: "online", Targets: []checker.TargetDiagResult{{Success: true, Latency: 150 * time.Millisecond}}},
		{ProxyName: "OfflineShort", StableID: "off-short", Status: "offline"},
		{ProxyName: "DisabledProxy", StableID: "dis", Status: "disabled", Disabled: true},
		{ProxyName: "OfflineLong", StableID: "off-long", Status: "offline"},
		{ProxyName: "OnlineFast", StableID: "on-fast", Status: "online", Targets: []checker.TargetDiagResult{{Success: true, Latency: 25 * time.Millisecond}}},
		{ProxyName: "DegradedProxy", StableID: "deg", Status: "degraded", Targets: []checker.TargetDiagResult{{Success: true, Latency: 80 * time.Millisecond}, {Success: false, Error: "timeout"}}},
	}

	b.sortDiagnosticsReports(reports)

	// Expected order:
	// 0: OfflineLong (2h downtime)
	// 1: OfflineShort (10m downtime)
	// 2: DegradedProxy
	// 3: OnlineFast (25ms)
	// 4: OnlineSlow (150ms)
	// 5: DisabledProxy
	expected := []string{"OfflineLong", "OfflineShort", "DegradedProxy", "OnlineFast", "OnlineSlow", "DisabledProxy"}
	for i, want := range expected {
		if reports[i].ProxyName != want {
			t.Errorf("pos %d: expected %s, got %s", i, want, reports[i].ProxyName)
		}
	}
}

func TestFormatSingleProxyDiag_SoftHintsAndFlapping(t *testing.T) {
	tmpDir := t.TempDir()
	statsStore, _ := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	now := time.Now()

	// Simulate 12 transitions for "p-flap" to trigger flapping (>10)
	for i := 0; i < 12; i++ {
		statsStore.RecordTransition("p-flap", "PL-flap", i%2 == 0, "flap test", now.Add(-time.Duration(12-i)*time.Minute))
	}

	// Add latency samples
	statsStore.RecordLatency("p-fast", 40.0)
	statsStore.RecordLatency("p-fast", 42.0)
	statsStore.RecordLatency("p-fast", 45.0)
	statsStore.RecordLatency("p-fast", 43.0)
	statsStore.RecordLatency("p-fast", 41.0)

	b := &Bot{
		statsStore: statsStore,
	}

	// 1. Test flapping indicator & soft hint on offline proxy
	var sb strings.Builder
	offRep := checker.ProxyDiagReport{
		ProxyName: "PL-flap",
		Protocol:  "vless",
		StableID:  "p-flap",
		Status:    "offline",
		NodeHealth: checker.NodeHealth{
			TCPErr: "connection timed out",
		},
	}
	b.formatSingleProxyDiagWithStats(&sb, offRep)
	outOff := sb.String()

	if !strings.Contains(outOff, "⚠️ флап") {
		t.Errorf("expected '⚠️ флап' for flapping proxy, got:\n%s", outOff)
	}
	if !strings.Contains(outOff, "вероятно:") {
		t.Errorf("expected soft hint with 'вероятно:', got:\n%s", outOff)
	}

	// 2. Test latency p95 and jitter on online proxy
	sb.Reset()
	onRep := checker.ProxyDiagReport{
		ProxyName: "PL-fast",
		Protocol:  "vless",
		StableID:  "p-fast",
		Status:    "online",
		Targets: []checker.TargetDiagResult{
			{Success: true, Latency: 42 * time.Millisecond},
		},
	}
	b.formatSingleProxyDiagWithStats(&sb, onRep)
	outOn := sb.String()

	if !strings.Contains(outOn, "p95:") || !strings.Contains(outOn, "σ=") {
		t.Errorf("expected p95 and jitter in output, got:\n%s", outOn)
	}
}

func TestFormatDeepDiagnostics(t *testing.T) {
	b := &Bot{}

	metric := metrics.ProxyMetric{
		Name:               "PL-main-01",
		Protocol:           "vless",
		Address:            "185.120.45.10:443",
		StableID:           "pl-main-01",
		Online:             true,
		CanConnect:         true,
		CanTransfer:        true,
		LatencyMs:          42,
		TLSHandshakeMs:     35,
		TTFBMs:             120,
		DirectProbeSuccess: true,
		DirectProbeRTTMs:   28,
	}

	health := checker.NodeHealth{
		ResolvedIP: "185.120.45.10",
		DNSLatency: 15 * time.Millisecond,
		TCPPing:    28 * time.Millisecond,
		TLSLatency: 35 * time.Millisecond,
	}

	ch := &checker.CheckHostSummary{
		RUAvailable:    true,
		WorldAvailable: true,
		PermanentLink:  "https://check-host.net/check-report/test1234",
		Results: []checker.CheckHostNodeResult{
			{Country: "ru", Success: true, Latency: 87 * time.Millisecond},
			{Country: "ru", Success: true, Latency: 85 * time.Millisecond},
			{Country: "ru", Success: true, Latency: 89 * time.Millisecond},
			{Country: "ru", Success: false},
			{Country: "de", Success: true, Latency: 120 * time.Millisecond},
			{Country: "nl", Success: true, Latency: 125 * time.Millisecond},
			{Country: "fi", Success: true, Latency: 122 * time.Millisecond},
			{Country: "us", Success: true, Latency: 129 * time.Millisecond},
		},
	}

	text := b.formatDeepDiagnostics(metric, health, ch)

	if !strings.Contains(text, "🔬 <b>Углублённая проверка — PL-main-01</b>") {
		t.Errorf("missing header in deep diag text:\n%s", text)
	}
	if !strings.Contains(text, "ЭТАПЫ ПРОВЕРКИ:") {
		t.Errorf("missing stages header:\n%s", text)
	}
	if !strings.Contains(text, "1. DNS-резолв") || !strings.Contains(text, "2. TCP-рукопожатие") {
		t.Errorf("missing stage 1 or 2:\n%s", text)
	}
	if !strings.Contains(text, "HOST-CHECK (Check-Host.net):") {
		t.Errorf("missing host-check section:\n%s", text)
	}
	if !strings.Contains(text, "<b>ИТОГ:</b> ✅ Доступен") {
		t.Errorf("missing status line:\n%s", text)
	}
}
