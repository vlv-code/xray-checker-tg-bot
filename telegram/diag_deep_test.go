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
	tracker.Track(1, 0, 100, "off-short", "OfflineShort", now.Add(-10*time.Minute), "timeout")
	tracker.Track(1, 0, 101, "off-long", "OfflineLong", now.Add(-2*time.Hour), "timeout")

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

	// Simulate 24 transitions for "p-flap" to trigger flapping (12 drops > 10)
	for i := 0; i < 24; i++ {
		statsStore.RecordTransition("p-flap", "PL-flap", i%2 == 0, "flap test", now.Add(-time.Duration(24-i)*time.Minute))
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

func TestDiagnosticsSummaryText(t *testing.T) {
	b := &Bot{}
	reports := []checker.ProxyDiagReport{
		{ProxyName: "Proxy-1", Protocol: "vless", Status: "online", Targets: []checker.TargetDiagResult{{Success: true, Latency: 35 * time.Millisecond}}},
		{ProxyName: "Proxy-2", Protocol: "vmess", Status: "offline", Targets: []checker.TargetDiagResult{{Success: false, Error: "timeout"}}},
		{ProxyName: "Proxy-3", Protocol: "trojan", Status: "disabled", Disabled: true},
	}

	summary := b.getDiagnosticsSummaryText(reports)
	if !strings.Contains(summary, "📊 <b>Подробная сводка (3 всего):</b>") {
		t.Errorf("unexpected summary header:\n%s", summary)
	}
	if !strings.Contains(summary, "🟢 <b>Proxy-1</b> (VLESS) — 35 ms") {
		t.Errorf("missing Proxy-1 in summary:\n%s", summary)
	}
	if !strings.Contains(summary, "🔴 <b>Proxy-2</b> (VMESS) — недоступен") {
		t.Errorf("missing Proxy-2 in summary:\n%s", summary)
	}
	if !strings.Contains(summary, "⏸️ <b>Proxy-3</b> (TROJAN) — отключён") {
		t.Errorf("missing Proxy-3 in summary:\n%s", summary)
	}
	if !strings.Contains(summary, "В сети: 1 | Сбоев: 1 | Отключено: 1") {
		t.Errorf("missing status counts in summary:\n%s", summary)
	}
	if !strings.Contains(summary, "Нажмите «📑 Детальный отчёт» для постраничного разбора этапов.") {
		t.Errorf("missing details prompt in summary:\n%s", summary)
	}
}

func TestDiagnosticsRichBuilders(t *testing.T) {
	b := &Bot{}
	reports := []checker.ProxyDiagReport{
		{ProxyName: "P1", Protocol: "vless", Status: "online", Targets: []checker.TargetDiagResult{{Success: true, Latency: 30 * time.Millisecond}}},
		{ProxyName: "P2", Protocol: "shadowsocks", Status: "offline", NodeHealth: checker.NodeHealth{DNSErr: "no such host"}},
		{ProxyName: "P3", Protocol: "trojan", Status: "disabled", Disabled: true},
		{ProxyName: "P4", Protocol: "vmess", Status: "degraded"},
		{ProxyName: "P5", Protocol: "vless", Status: "online"},
		{ProxyName: "P6", Protocol: "vless", Status: "online"},
	}

	// Level 1: Summary rich message
	richSummary := b.buildDiagnosticsRichMessage(reports)
	if richSummary == nil || len(richSummary.Blocks) < 3 {
		t.Fatalf("expected at least 3 blocks in summary rich message, got %v", richSummary)
	}

	// Level 2: Details rich message (6 proxies, page size 5 -> 2 pages)
	richDetailsP1, totalPages := b.buildDiagnosticsDetailsRichMessage(reports, 1)
	if totalPages != 2 {
		t.Errorf("expected 2 total pages, got %d", totalPages)
	}
	if richDetailsP1 == nil || len(richDetailsP1.Blocks) != 6 { // 1 header + 5 proxy details
		t.Errorf("expected 6 blocks on page 1, got %d", len(richDetailsP1.Blocks))
	}

	richDetailsP2, totalPages2 := b.buildDiagnosticsDetailsRichMessage(reports, 2)
	if totalPages2 != 2 {
		t.Errorf("expected 2 total pages, got %d", totalPages2)
	}
	if richDetailsP2 == nil || len(richDetailsP2.Blocks) != 2 { // 1 header + 1 proxy detail
		t.Errorf("expected 2 blocks on page 2, got %d", len(richDetailsP2.Blocks))
	}
}

func TestGetDeepLinks(t *testing.T) {
	reports := []checker.ProxyDiagReport{
		{ProxyName: "Online-1", StableID: "on-1", Status: "online"},
		{ProxyName: "Offline-1", StableID: "off-1", Status: "offline"},
		{ProxyName: "Degraded-1", StableID: "deg-1", Status: "degraded"},
		{ProxyName: "Offline-2", StableID: "off-2", Status: "offline"},
	}

	linksAll := getDeepLinks(reports, 0)
	if len(linksAll) != 3 {
		t.Fatalf("expected 3 failing proxies, got %d", len(linksAll))
	}
	if linksAll[0].StableID != "off-1" || linksAll[1].StableID != "deg-1" || linksAll[2].StableID != "off-2" {
		t.Errorf("unexpected links order: %+v", linksAll)
	}

	linksCapped := getDeepLinks(reports, 2)
	if len(linksCapped) != 2 {
		t.Fatalf("expected 2 capped proxies, got %d", len(linksCapped))
	}
}
