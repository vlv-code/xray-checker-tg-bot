package telegram

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// Wave K-B1 (content cluster) pins: SF-legend icon migration on the status
// surface, expandable <blockquote> справка walls, fmtAgeIcon durations, the
// deep-diag <pre> stage block, the unified degraded wording and the /subs
// management hint. Pagination and custom-emoji wiring pins live in
// views_kb1_pager_test.go / icons_test.go respectively.

// TestGetStatusText_StateIconsAndLatencyUnits pins the /status row legend:
// proxy STATE is 🟢 (never ✅ — checkmark is reserved for step success) and
// latency always renders via the legend format «⚡N мс» (never "ms").
func TestGetStatusText_StateIconsAndLatencyUnits(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "Proxy1", Online: true, LatencyMs: 45},
			{StableID: "p2", Name: "Proxy2", Online: false},
		},
	}
	b := &Bot{source: src}

	text := b.getStatusText()
	if strings.Contains(text, "✅") {
		t.Errorf("proxy state must not use ✅ (step-success role), got:\n%s", text)
	}
	if !strings.Contains(text, "🟢 <b>Proxy1</b> — ⚡45 мс") {
		t.Errorf("expected legend status row «🟢 <b>Proxy1</b> — ⚡45 мс», got:\n%s", text)
	}
	if !strings.Contains(text, "🔴 <b>Proxy2</b> — недоступен") {
		t.Errorf("expected down row «🔴 <b>Proxy2</b> — недоступен», got:\n%s", text)
	}
	if strings.Contains(text, " ms") || strings.Contains(text, "ms\n") {
		t.Errorf("latency units must be «мс», got:\n%s", text)
	}
}

// TestGetStatusText_GroupedRowsLegend extends the state-icon pin to the
// grouped (nodes) layout of /status.
func TestGetStatusText_GroupedRowsLegend(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "MasterProxy", Online: true, LatencyMs: 1200},
			{StableID: "p2", Name: "NodeProxy1", Online: true, LatencyMs: 50, NodeName: "m31a"},
		},
	}
	mgr := &viewsTestNodeManager{
		nodes: []NodeInfo{
			{Name: "m31a", Up: true, Online: 1, Total: 1, EverReported: true, LastReport: time.Now()},
		},
	}
	b := &Bot{source: src, nodeMgr: mgr}

	text := b.getStatusText()
	if !strings.Contains(text, "🟢 <b>MasterProxy</b> — ⚡1.2 с") {
		t.Errorf("expected seconds-format latency row, got:\n%s", text)
	}
	if !strings.Contains(text, "🟢 <b>NodeProxy1</b> — ⚡50 мс") {
		t.Errorf("expected node proxy row in legend format, got:\n%s", text)
	}
}

// assertExpandableSpravka verifies the справка wall of a stats screen is ONE
// expandable blockquote holding the bullets, with the «Справка» title kept
// visible outside (the collapsed blockquote must not hide the section name).
func assertExpandableSpravka(t *testing.T, name, text string) {
	t.Helper()
	bqStart := strings.Index(text, "<blockquote expandable>")
	bqClose := strings.Index(text, "</blockquote>")
	if bqStart == -1 || bqClose == -1 || bqClose < bqStart {
		t.Errorf("%s: expected expandable <blockquote expandable>…</blockquote> справка, got:\n%s", name, text)
		return
	}
	if !strings.Contains(text, "Справка") {
		t.Errorf("%s: «Справка» title must stay visible, got:\n%s", name, text)
	}
	if inside := text[bqStart:bqClose]; !strings.Contains(inside, "•") {
		t.Errorf("%s: справка bullets must live inside the expandable blockquote, got:\n%s", name, text)
	}
	if strings.Count(text, "<blockquote") != 1 {
		t.Errorf("%s: expected exactly one blockquote per screen, got:\n%s", name, text)
	}
}

// TestStatsScreens_SpravkaInExpandableBlockquote pins the справка-wall
// collapse: every stats screen wraps its help section in ONE expandable
// blockquote instead of a wall of bullets.
func TestStatsScreens_SpravkaInExpandableBlockquote(t *testing.T) {
	tmpDir := t.TempDir()
	ss, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ss.RecordCheck("p1", "Proxy1", true, 45)

	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "Proxy1", Online: true, LatencyMs: 45, Protocol: "vless"},
		},
	}
	b := &Bot{
		source:     src,
		statsStore: ss,
		nowFunc:    func() time.Time { return now },
	}

	assertExpandableSpravka(t, "getStatsOverviewText", b.getStatsOverviewText())
	assertExpandableSpravka(t, "getProtocolsStatsText", b.getProtocolsStatsText())
	assertExpandableSpravka(t, "getHeatmapText", b.getHeatmapText())
	assertExpandableSpravka(t, "getIncidentsTextFiltered(empty)", b.getIncidentsTextFiltered(""))
	assertExpandableSpravka(t, "getTopProblematicText", b.getTopProblematicText())
}

// TestGetStatsOverviewText_LastIncidentAgeIcon pins the age icon on the
// stats-overview last-incident duration.
func TestGetStatsOverviewText_LastIncidentAgeIcon(t *testing.T) {
	tmpDir := t.TempDir()
	ss, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ss.RecordTransition("p1", "Proxy1", false, "timeout", now.Add(-2*time.Hour))
	ss.RecordTransition("p1", "Proxy1", true, "", now.Add(-90*time.Minute))

	src := &mockSource{
		metrics: []metrics.ProxyMetric{{StableID: "p1", Name: "Proxy1", Online: true}},
	}
	b := &Bot{source: src, statsStore: ss, nowFunc: func() time.Time { return now }}

	text := b.getStatsOverviewText()
	if !strings.Contains(text, "простой: ⏱️ 30 мин") {
		t.Errorf("expected last-incident downtime in fmtAgeIcon form «⏱️ 30 мин», got:\n%s", text)
	}
}

// TestGetIncidentsText_DurationsUseAgeIcon pins the incidents journal
// durations on fmtAgeIcon («⏱️ 5 мин»), replacing the raw FormatDowntime
// shorthand.
func TestGetIncidentsText_DurationsUseAgeIcon(t *testing.T) {
	tmpDir := t.TempDir()
	ss, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ss.RecordTransition("p1", "Proxy1", false, "timeout", now.Add(-time.Hour))
	ss.RecordTransition("p1", "Proxy1", true, "", now.Add(-55*time.Minute))

	b := &Bot{statsStore: ss, nowFunc: func() time.Time { return now }}

	text := b.getIncidentsText()
	if !strings.Contains(text, "простой: ⏱️ 5 мин") {
		t.Errorf("expected recovered incident downtime «⏱️ 5 мин», got:\n%s", text)
	}
	if strings.Contains(text, "5м 0с") {
		t.Errorf("raw FormatDowntime shorthand must not leak into incidents, got:\n%s", text)
	}
}

// TestGetTopProblematicText_AgeIcons pins ⏱️ on the reliability durations
// (MTBF / MTTR / total downtime) of the top-problematic screen.
func TestGetTopProblematicText_AgeIcons(t *testing.T) {
	tmpDir := t.TempDir()
	ss, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ss.RecordCheck("p1", "BadProxy", false, 0)
	ss.RecordTransition("p1", "BadProxy", false, "Offline", now.Add(-2*time.Hour))
	ss.RecordTransition("p1", "BadProxy", true, "", now.Add(-1*time.Hour))

	b := &Bot{statsStore: ss, nowFunc: func() time.Time { return now }}

	text := b.getTopProblematicText()
	if !strings.Contains(text, "MTBF: ⏱️") || !strings.Contains(text, "MTTR: ⏱️") {
		t.Errorf("expected MTBF/MTTR durations in fmtAgeIcon form, got:\n%s", text)
	}
	if !strings.Contains(text, "суммарный простой: ⏱️") {
		t.Errorf("expected total downtime in fmtAgeIcon form, got:\n%s", text)
	}
}

// TestGetSubsText_AddRemoveHint pins the management hint line on the
// subscriptions screen (the ➕ arm button itself belongs to the menus cluster).
func TestGetSubsText_AddRemoveHint(t *testing.T) {
	b := &Bot{
		subs: &mockSubFreshness{
			static:  []string{"https://example.com/sub1"},
			dynamic: []string{"https://example.com/sub2"},
		},
	}

	text := b.getSubsText()
	if !strings.Contains(text, "➕ /addsub &lt;URL&gt; — добавить · /delsub &lt;URL&gt; — удалить") {
		t.Errorf("expected /addsub + /delsub hint line (HTML-escaped args), got:\n%s", text)
	}
}

// TestFormatDeepDiagnostics_StagesInPreBlock pins the monospace stage block:
// the numbered check stages render inside <pre> so the dotted alignment
// survives Telegram's proportional font, and the section closes before ИТОГ.
func TestFormatDeepDiagnostics_StagesInPreBlock(t *testing.T) {
	b := &Bot{}
	metric := metrics.ProxyMetric{
		Name:           "PL-main-01",
		Protocol:       "vless",
		Address:        "185.120.45.10:443",
		StableID:       "pl-main-01",
		Online:         true,
		CanConnect:     true,
		CanTransfer:    true,
		LatencyMs:      42,
		TLSHandshakeMs: 35,
		TTFBMs:         120,
	}
	health := checker.NodeHealth{
		ResolvedIP: "185.120.45.10",
		DNSLatency: 15 * time.Millisecond,
		TCPPing:    28 * time.Millisecond,
		TLSLatency: 35 * time.Millisecond,
	}

	text := b.formatDeepDiagnostics(metric, health, nil)

	stages := strings.Index(text, "ЭТАПЫ ПРОВЕРКИ:")
	pre := strings.Index(text, "<pre>")
	if stages == -1 || pre < stages {
		t.Fatalf("expected stage block to open <pre> after the ЭТАПЫ ПРОВЕРКИ header, got:\n%s", text)
	}
	if !strings.Contains(text, "1. DNS-резолв") || !strings.Contains(text, "6. Полный ответ") {
		t.Errorf("expected stages 1..6 inside the pre block, got:\n%s", text)
	}
	preClose := strings.Index(text, "</pre>")
	itog := strings.Index(text, "<b>ИТОГ:</b>")
	if preClose == -1 || itog == -1 || preClose > itog {
		t.Errorf("expected </pre> to close before ИТОГ, got:\n%s", text)
	}
}

// TestFormatSingleProxyDiag_LatencyUnitsUnified pins the мс-unit unification
// across ALL diagnostic stage lines (DNS/TCP/TLS/targets) — the old output
// mixed «мс» and "ms".
func TestFormatSingleProxyDiag_LatencyUnitsUnified(t *testing.T) {
	b := &Bot{}
	rep := checker.ProxyDiagReport{
		ProxyName: "PL-units",
		Protocol:  "vless",
		StableID:  "p-units",
		Status:    "online",
		NodeHealth: checker.NodeHealth{
			ResolvedIP: "185.120.45.10",
			DNSLatency: 18 * time.Millisecond,
			TCPPing:    42 * time.Millisecond,
			TLSLatency: 58 * time.Millisecond,
		},
		Targets: []checker.TargetDiagResult{
			{URL: "https://cp.cloudflare.com/generate_204", Success: true, Latency: 65 * time.Millisecond},
			{URL: "https://www.gstatic.com/generate_204", Success: true, Latency: 71 * time.Millisecond},
		},
	}

	var sb strings.Builder
	b.formatSingleProxyDiagWithStats(&sb, rep)
	text := sb.String()

	if strings.Contains(text, " ms") {
		t.Errorf("diagnostic stage lines must use «мс», got:\n%s", text)
	}
	for _, want := range []string{"DNS: ✅ <code>185.120.45.10</code> (18 мс)", "TLS: ✅ 58 мс", "Cloudflare 204: ✅ 65 мс"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in diag output, got:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "Доступен: 🟢 ⚡65 мс") {
		t.Errorf("expected headline latency in legend format «⚡65 мс», got:\n%s", text)
	}
}

// TestDiagnosticsSummaryText_DegradedUnifiedWording pins the single degraded
// wording across surfaces: the L1 summary uses the same explanatory phrase as
// the per-proxy detail line (no divergent bare «ошибка»).
func TestDiagnosticsSummaryText_DegradedUnifiedWording(t *testing.T) {
	b := &Bot{}
	reports := []checker.ProxyDiagReport{
		{
			ProxyName: "PL-degraded",
			Protocol:  "vless",
			Status:    "degraded",
			Targets: []checker.TargetDiagResult{
				{Success: true, Latency: 40 * time.Millisecond},
			},
		},
	}

	summary := b.getDiagnosticsSummaryTextWithTarget(reports, "", "")
	if !strings.Contains(summary, "туннель работает, но цель недоступна") {
		t.Errorf("expected unified degraded wording in summary, got:\n%s", summary)
	}
	if strings.Contains(summary, "ошибка") {
		t.Errorf("bare «ошибка» wording must be replaced by the unified phrase, got:\n%s", summary)
	}

}
