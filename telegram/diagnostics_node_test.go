package telegram

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

func TestGetNodeDiagnosticsReports(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "MasterProxy", Address: "1.1.1.1:443", Protocol: "vless", Online: true, LatencyMs: 40},
			{StableID: "p2", Name: "NodeProxy1", Address: "2.2.2.2:443", Protocol: "hysteria", Online: true, LatencyMs: 55, NodeName: "m31a", NodeASN: "AS60879 System Projects, LLC"},
			{StableID: "p3", Name: "NodeProxy2", Address: "3.3.3.3:443", Protocol: "vless", Online: false, LastErrorMsg: "EOF", NodeName: "m31a", NodeASN: "AS60879 System Projects, LLC"},
			{StableID: "p4", Name: "OtherNodeProxy", Address: "4.4.4.4:443", Protocol: "vless", Online: true, NodeName: "other"},
		},
	}
	b := &Bot{source: src}

	reports := b.getNodeDiagnosticsReports("m31a")
	if len(reports) != 2 {
		t.Fatalf("expected 2 reports for node m31a, got %d", len(reports))
	}
	// Check p2
	foundOnline := false
	foundOffline := false
	for _, r := range reports {
		if r.ProxyName == "NodeProxy1" {
			foundOnline = true
			if r.Status != "online" {
				t.Errorf("expected NodeProxy1 to be online, got %s", r.Status)
			}
			if len(r.Targets) == 0 || r.Targets[0].Latency.Milliseconds() != 55 {
				t.Errorf("expected 55ms latency for NodeProxy1")
			}
		}
		if r.ProxyName == "NodeProxy2" {
			foundOffline = true
			if r.Status != "offline" {
				t.Errorf("expected NodeProxy2 to be offline, got %s", r.Status)
			}
			if r.Verdict != "EOF" {
				t.Errorf("expected EOF verdict for NodeProxy2, got %s", r.Verdict)
			}
		}
	}
	if !foundOnline || !foundOffline {
		t.Errorf("expected to find both online and offline reports for node m31a")
	}
}

func TestDiagnosticsSummaryText_WithNodeContext(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p2", Name: "NodeProxy1", Address: "2.2.2.2:443", Protocol: "hysteria", Online: true, LatencyMs: 55, NodeName: "m31a", NodeASN: "AS60879 System Projects, LLC"},
		},
	}
	b := &Bot{source: src}
	reports := b.getNodeDiagnosticsReports("m31a")
	text := b.getDiagnosticsSummaryTextWithTarget(reports, "m31a", "AS60879 System Projects, LLC")
	if !strings.Contains(text, "🖥 Нода m31a") {
		t.Errorf("expected text to contain node name, got:\n%s", text)
	}
	if !strings.Contains(text, "AS60879") {
		t.Errorf("expected text to contain ASN, got:\n%s", text)
	}
}

func TestRichReportMarkup_WithNodeTabs(t *testing.T) {
	tabs := []NodeTabItem{
		{ID: "local", Label: "🏠 Мастер", Status: "14/15", IsActive: true},
		{ID: "m31a", Label: "🖥 m31a", Status: "⚠️ 14/17", IsActive: false},
	}
	markup := RichReportMarkupWithTabs("local", tabs)
	if len(markup.InlineKeyboard) < 3 {
		t.Fatalf("expected at least 3 rows in markup with tabs, got %d", len(markup.InlineKeyboard))
	}
	// First row should be node tabs
	row0 := markup.InlineKeyboard[0]
	if len(row0) != 2 {
		t.Fatalf("expected 2 buttons in tabs row, got %d", len(row0))
	}
	if !strings.Contains(row0[0].Text, "• 🏠 Мастер") {
		t.Errorf("expected active marker on Master tab, got %s", row0[0].Text)
	}
	if row0[1].CallbackData != "menu:diag:node:m31a" {
		t.Errorf("expected callback menu:diag:node:m31a, got %s", row0[1].CallbackData)
	}
}

func TestDiagnosticsPaginationMarkup_WithNodeTabs(t *testing.T) {
	tabs := []NodeTabItem{
		{ID: "local", Label: "🏠 Мастер", Status: "14/15", IsActive: false},
		{ID: "m31a", Label: "🖥 m31a", Status: "14/17", IsActive: true},
	}
	markup := DiagnosticsPaginationMarkupWithTabs("m31a", 1, 2, tabs)
	// Tabs row should be present
	row0 := markup.InlineKeyboard[0]
	if len(row0) != 2 {
		t.Fatalf("expected 2 buttons in tabs row, got %d", len(row0))
	}
	if row0[0].CallbackData != "menu:diag:details:local:1" {
		t.Errorf("expected tab switch to local page 1, got %s", row0[0].CallbackData)
	}
	if !strings.Contains(row0[1].Text, "• 🖥 m31a") {
		t.Errorf("expected active marker on m31a tab, got %s", row0[1].Text)
	}
}

func TestGetIncidentsText_WithFilter(t *testing.T) {
	store := &StatsStore{
		Incidents: []Incident{
			{ProxyName: "MasterProxy", StableID: "p1", DownAt: time.Now().Add(-10 * time.Minute).Unix(), Reason: "EOF"},
			{ProxyName: "[m31a] NodeProxy", StableID: "p2", DownAt: time.Now().Add(-5 * time.Minute).Unix(), Reason: "timeout"},
			{ProxyName: "🖥 [Нода] m31a", StableID: "node:m31a", DownAt: time.Now().Add(-20 * time.Minute).Unix(), Reason: "Потеря связи с чекер-нодой"},
		},
	}
	b := &Bot{statsStore: store}

	// Filter: all
	allText := b.getIncidentsTextFiltered("")
	if !strings.Contains(allText, "🏠 [Мастер]") || !strings.Contains(allText, "🖥 [m31a]") || !strings.Contains(allText, "🔌 [Связь]") {
		t.Errorf("expected all source tags in unfiltered incidents text, got:\n%s", allText)
	}

	// Filter: local
	localText := b.getIncidentsTextFiltered("local")
	if !strings.Contains(localText, "MasterProxy") {
		t.Errorf("expected MasterProxy in local filter, got:\n%s", localText)
	}
	if strings.Contains(localText, "NodeProxy") {
		t.Errorf("expected no NodeProxy in local filter, got:\n%s", localText)
	}

	// Filter: m31a
	nodeText := b.getIncidentsTextFiltered("m31a")
	if !strings.Contains(nodeText, "NodeProxy") {
		t.Errorf("expected NodeProxy in m31a filter, got:\n%s", nodeText)
	}
	if strings.Contains(nodeText, "MasterProxy") {
		t.Errorf("expected no MasterProxy in m31a filter, got:\n%s", nodeText)
	}
}

func TestDiagnosticsPageTextWithTarget(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p2", Name: "NodeProxy1", Address: "2.2.2.2:443", Protocol: "hysteria", Online: true, LatencyMs: 55, NodeName: "m31a"},
		},
	}
	b := &Bot{source: src}
	reports := b.getNodeDiagnosticsReports("m31a")
	pageText, totalPages := b.getDiagnosticsPageTextWithTarget(reports, "m31a", 1)
	if totalPages != 1 {
		t.Errorf("expected 1 page, got %d", totalPages)
	}
	if !strings.Contains(pageText, "🖥 Нода m31a") {
		t.Errorf("expected node name in detailed report page text, got:\n%s", pageText)
	}
	if !strings.Contains(pageText, "NodeProxy1") {
		t.Errorf("expected proxy name in detailed report page text, got:\n%s", pageText)
	}
}

func TestBuildDiagnosticsRichMessageWithTarget(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p2", Name: "NodeProxy1", Address: "2.2.2.2:443", Protocol: "hysteria", Online: true, LatencyMs: 55, NodeName: "m31a"},
		},
	}
	b := &Bot{source: src}
	reports := b.getNodeDiagnosticsReports("m31a")
	rich := b.buildDiagnosticsRichMessageWithTarget(reports, "m31a", "AS60879 System Projects, LLC")
	if rich == nil || len(rich.Blocks) == 0 {
		t.Fatalf("expected non-empty rich message")
	}
}

func TestIncidentsFilterMarkup(t *testing.T) {
	tabs := []NodeTabItem{
		{ID: "local", Label: "🏠 Мастер"},
		{ID: "m31a", Label: "🖥 m31a"},
	}
	markup := IncidentsFilterMarkup("m31a", tabs)
	if len(markup.InlineKeyboard) < 2 {
		t.Fatalf("expected at least 2 rows in IncidentsFilterMarkup, got %d", len(markup.InlineKeyboard))
	}
	filterRow := markup.InlineKeyboard[0]
	if len(filterRow) != 3 { // All, Master, m31a
		t.Fatalf("expected 3 filter buttons, got %d", len(filterRow))
	}
	if !strings.Contains(filterRow[2].Text, "• 🖥 m31a •") {
		t.Errorf("expected active marker on m31a filter button, got %s", filterRow[2].Text)
	}
	if filterRow[1].CallbackData != "menu:stats:incidents:local" {
		t.Errorf("expected callback menu:stats:incidents:local, got %s", filterRow[1].CallbackData)
	}
}

type mockNodeSnapshotManager struct {
	viewsTestNodeManager
	snapshots map[string][]metrics.ProxyMetric
}

func (m *mockNodeSnapshotManager) NodeSnapshot(node string) []metrics.ProxyMetric {
	return m.snapshots[node]
}

func TestGetNodeDiagnosticsReports_FromNodeManager(t *testing.T) {
	masterSrc := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "master1", Name: "MasterProxy", Address: "1.1.1.1:443", Online: true},
		},
	}
	mgr := &mockNodeSnapshotManager{
		snapshots: map[string][]metrics.ProxyMetric{
			"m31a": {
				{StableID: "node1", Name: "RemoteProxy1", Address: "2.2.2.2:443", Protocol: "vless", Online: true, LatencyMs: 45, NodeName: "m31a"},
				{StableID: "node2", Name: "RemoteProxy2", Address: "3.3.3.3:443", Protocol: "hysteria", Online: false, LastErrorMsg: "timeout", NodeName: "m31a"},
			},
		},
	}
	b := &Bot{source: masterSrc}
	b.SetNodeManager(mgr)

	reports := b.getNodeDiagnosticsReports("m31a")
	if len(reports) != 2 {
		t.Fatalf("expected 2 reports from nodeManager for node m31a, got %d", len(reports))
	}
	if reports[0].ProxyName != "RemoteProxy2" || reports[0].Status != "offline" || reports[0].Verdict != "timeout" {
		t.Errorf("expected offline RemoteProxy2 first due to severity sorting, got: %+v", reports[0])
	}
	if reports[1].ProxyName != "RemoteProxy1" || reports[1].Status != "online" {
		t.Errorf("expected online RemoteProxy1 second, got: %+v", reports[1])
	}
}

type mockNodeDiagManager struct {
	viewsTestNodeManager
	diagReports map[string][]checker.ProxyDiagReport
}

func (m *mockNodeDiagManager) NodeDiagReports(node string) []checker.ProxyDiagReport {
	return m.diagReports[node]
}

func TestGetNodeDiagnosticsReports_FullDiagnostics(t *testing.T) {
	mgr := &mockNodeDiagManager{
		diagReports: map[string][]checker.ProxyDiagReport{
			"m31a": {
				{
					ProxyName: "RemoteProxy1",
					Protocol:  "vless",
					Server:    "2.2.2.2",
					Port:      443,
					StableID:  "node1",
					Status:    "online",
					Verdict:   "Полностью исправен",
					NodeHealth: checker.NodeHealth{
						ResolvedIP: "2.2.2.2",
						DNSLatency: 10 * time.Millisecond,
						TCPPing:    35 * time.Millisecond,
					},
					Targets: []checker.TargetDiagResult{
						{URL: "https://cp.cloudflare.com/generate_204", Success: true, StatusCode: 204, Latency: 120 * time.Millisecond},
						{URL: "https://www.gstatic.com/generate_204", Success: true, StatusCode: 204, Latency: 135 * time.Millisecond},
					},
				},
			},
		},
	}
	b := &Bot{}
	b.SetNodeManager(mgr)

	reports := b.getNodeDiagnosticsReports("m31a")
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	rep := reports[0]
	if rep.ProxyName != "RemoteProxy1" || rep.Verdict != "Полностью исправен" {
		t.Errorf("unexpected report: %+v", rep)
	}
	if rep.NodeHealth.ResolvedIP != "2.2.2.2" || rep.NodeHealth.TCPPing != 35*time.Millisecond {
		t.Errorf("NodeHealth lost: %+v", rep.NodeHealth)
	}
	if len(rep.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(rep.Targets))
	}

	pageText, _ := b.getDiagnosticsPageTextWithTarget(reports, "m31a", 1)
	if !strings.Contains(pageText, "DNS:") || !strings.Contains(pageText, "2.2.2.2") {
		t.Errorf("expected DNS info in page text, got:\n%s", pageText)
	}
	if !strings.Contains(pageText, "TCP (443):") {
		t.Errorf("expected TCP info in page text, got:\n%s", pageText)
	}
	if !strings.Contains(pageText, "Cloudflare 204:") || !strings.Contains(pageText, "Google 204:") {
		t.Errorf("expected multi-target info in page text, got:\n%s", pageText)
	}
}

func TestPickDeepDiagnosticsMarkupWithTabs(t *testing.T) {
	tabs := []NodeTabItem{
		{ID: "local", Label: "🏠 Мастер", Status: "14/15", IsActive: false},
		{ID: "m31a", Label: "🖥 m31a", Status: "17/17", IsActive: true},
	}
	reports := []checker.ProxyDiagReport{
		{ProxyName: "RemoteProxy1", StableID: "m31a/rem1", Status: "online"},
	}

	markup := PickDeepDiagnosticsMarkupWithTabs("m31a", tabs, reports, 2)
	if len(markup.InlineKeyboard) < 3 {
		t.Fatalf("expected at least 3 rows (tabs, proxy, back), got %d", len(markup.InlineKeyboard))
	}

	// Tabs row
	tabRow := markup.InlineKeyboard[0]
	if len(tabRow) != 2 {
		t.Fatalf("expected 2 tab buttons, got %d", len(tabRow))
	}
	if tabRow[0].CallbackData != "menu:diag:pick_deep:local:1" {
		t.Errorf("expected tab callback menu:diag:pick_deep:local:1, got %s", tabRow[0].CallbackData)
	}
	if tabRow[1].CallbackData != "menu:diag:pick_deep:m31a:1" {
		t.Errorf("expected tab callback menu:diag:pick_deep:m31a:1, got %s", tabRow[1].CallbackData)
	}
	if !strings.Contains(tabRow[1].Text, "• 🖥 m31a") {
		t.Errorf("expected active marker on m31a tab, got %s", tabRow[1].Text)
	}

	// Proxy button
	proxyRow := markup.InlineKeyboard[1]
	if len(proxyRow) != 1 || proxyRow[0].CallbackData != "menu:diag:deep:m31a/rem1:2" {
		t.Errorf("unexpected proxy button callback: %v", proxyRow)
	}

	// Back button returns to m31a detailed report
	backRow := markup.InlineKeyboard[2]
	if len(backRow) != 1 || backRow[0].CallbackData != "menu:diag:details:m31a:2" {
		t.Errorf("expected back button callback menu:diag:details:m31a:2, got %v", backRow)
	}
}

func TestDiagnosticsPaginationMarkupWithTabs_NodeDeepButton(t *testing.T) {
	tabs := []NodeTabItem{
		{ID: "local", Label: "🏠 Мастер", Status: "14/15", IsActive: false},
		{ID: "m31a", Label: "🖥 m31a", Status: "17/17", IsActive: true},
	}

	markup := DiagnosticsPaginationMarkupWithTabs("m31a", 1, 2, tabs)

	// Search for deep diagnostics button
	found := false
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.Text, "Углубленная проверка") {
				found = true
				if btn.CallbackData != "menu:diag:pick_deep:m31a:1" {
					t.Errorf("expected callback 'menu:diag:pick_deep:m31a:1', got %q", btn.CallbackData)
				}
			}
		}
	}
	if !found {
		t.Errorf("expected deep diagnostics button on node detailed report page, but it was missing")
	}
}

func TestDeepDiagnosticsMarkup_NodeReturn(t *testing.T) {
	markup := DeepDiagnosticsMarkup("m31a/p1", 2)
	backBtn := markup.InlineKeyboard[0][0]
	if backBtn.CallbackData != "menu:diag:details:m31a:2" {
		t.Errorf("expected back button to return to node detailed report 'menu:diag:details:m31a:2', got %s", backBtn.CallbackData)
	}
}

func TestFormatDeepDiagnostics_NodeContext(t *testing.T) {
	b := &Bot{}
	pm := metrics.ProxyMetric{
		Name:     "NodeProxy",
		Address:  "2.2.2.2:443",
		Protocol: "vless",
		NodeName: "m31a",
		NodeASN:  "AS12345 Test ISP",
		Online:   true,
	}
	health := checker.NodeHealth{
		ResolvedIP: "2.2.2.2",
		DNSLatency: 15 * time.Millisecond,
		TCPPing:    45 * time.Millisecond,
	}

	text := b.formatDeepDiagnostics(pm, health, nil)
	if !strings.Contains(text, "🖥 [m31a] NodeProxy") {
		t.Errorf("expected node tag in deep diagnostics title, got:\n%s", text)
	}
	if !strings.Contains(text, "🖥 Нода m31a (AS12345 Test ISP)") {
		t.Errorf("expected node ASN in deep diagnostics, got:\n%s", text)
	}
}

func TestDeepLinks_NodeReports(t *testing.T) {
	reports := []checker.ProxyDiagReport{
		{ProxyName: "OnlineProxy", StableID: "m31a/p1", Status: "online"},
		{ProxyName: "OfflineProxy", StableID: "m31a/p2", Status: "offline"},
		{ProxyName: "DegradedProxy", StableID: "m31a/p3", Status: "degraded"},
	}

	links := getDeepLinks(reports, 3)
	if len(links) != 2 {
		t.Fatalf("expected 2 deep links for offline/degraded node proxies, got %d", len(links))
	}
	if links[0].Name != "OfflineProxy" || links[0].StableID != "m31a/p2" {
		t.Errorf("unexpected link 0: %+v", links[0])
	}
	if links[1].Name != "DegradedProxy" || links[1].StableID != "m31a/p3" {
		t.Errorf("unexpected link 1: %+v", links[1])
	}
}

func (m *mockNodeSnapshotManager) NodeSettingsView(node string) ([]NodeSettingEntry, error) {
	return nil, fmt.Errorf("не поддерживается в тесте")
}

func (m *mockNodeSnapshotManager) SetNodeSetting(node, key, value string) error {
	return fmt.Errorf("не поддерживается в тесте")
}

func (m *mockNodeSnapshotManager) ResetNodeSetting(node, key string) error {
	return fmt.Errorf("не поддерживается в тесте")
}
