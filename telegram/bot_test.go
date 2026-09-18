package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

func TestBot_IntervalHandling(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bot_cfg.json")
	cm, err := NewConfigManager(cfgPath, BotConfig{CheckIntervalSec: 300})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	b := &Bot{
		configMgr: cm,
	}

	if interval := b.getIntervalSec(); interval != 300 {
		t.Errorf("expected 300s default interval, got %d", interval)
	}

	var notifiedInterval int
	b.SetIntervalHandler(func(sec int) {
		notifiedInterval = sec
	})

	b.updateCheckInterval(60)

	if b.getIntervalSec() != 60 {
		t.Errorf("expected 60s, got %d", b.getIntervalSec())
	}
	if notifiedInterval != 60 {
		t.Errorf("expected callback with 60, got %d", notifiedInterval)
	}

	// Test minimum clamp (< 10 seconds)
	b.updateCheckInterval(5)
	if b.getIntervalSec() != 10 {
		t.Errorf("expected clamp to 10s, got %d", b.getIntervalSec())
	}
	if notifiedInterval != 10 {
		t.Errorf("expected callback with 10, got %d", notifiedInterval)
	}
}

func TestCheckHostMenuAndMarkup(t *testing.T) {
	// 1. Check MainMenuMarkup contains Check-Host
	mainMarkup := MainMenuMarkup()
	foundCheckHost := false
	for _, row := range mainMarkup.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "menu:checkhost" {
				foundCheckHost = true
				break
			}
		}
	}
	if !foundCheckHost {
		t.Errorf("expected menu:checkhost button in MainMenuMarkup")
	}

	// 2. Check CheckHostMenuMarkup generates proxy buttons
	proxies := []metrics.ProxyMetric{
		{Name: "Node-1", StableID: "id-1", Address: "1.1.1.1:443"},
		{Name: "Node-2", StableID: "id-2", Address: "2.2.2.2:443"},
	}
	chMarkup := CheckHostMenuMarkup(proxies)
	if chMarkup == nil || len(chMarkup.InlineKeyboard) < 2 {
		t.Fatalf("expected rows in CheckHostMenuMarkup, got %v", chMarkup)
	}

	foundID1 := false
	for _, row := range chMarkup.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "menu:checkhost:run:id-1" {
				foundID1 = true
			}
		}
	}
	if !foundID1 {
		t.Errorf("expected menu:checkhost:run:id-1 button in CheckHostMenuMarkup")
	}

	// 3. Check getCheckHostMenuText
	bot := &Bot{}
	txt := bot.getCheckHostMenuText()
	if txt == "" || !strings.Contains(txt, "/checkhost") {
		t.Errorf("expected /checkhost in menu text, got: %s", txt)
	}
}

func TestBot_DiagnosticsPaginationAndRichMessage(t *testing.T) {
	b := &Bot{}

	// 1. RichMode getter/setter
	if b.isRichMode() {
		t.Errorf("expected default richMode to be false")
	}
	b.SetRichMode(true)
	if !b.isRichMode() {
		t.Errorf("expected richMode to be true after SetRichMode(true)")
	}
	b.SetRichMode(false)

	// Test with ConfigManager
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_config.json"), BotConfig{})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	b.SetConfigManager(cm)
	if b.isRichMode() {
		t.Errorf("expected richMode to be false initially with empty configMgr")
	}
	b.SetRichMode(true)
	if !b.isRichMode() {
		t.Errorf("expected richMode to be true after SetRichMode(true) with configMgr")
	}
	if !cm.Get().RichMode {
		t.Errorf("expected configMgr.RichMode to be persisted as true")
	}
	b.SetRichMode(false)
	if b.isRichMode() {
		t.Errorf("expected richMode to be false after SetRichMode(false) with configMgr")
	}

	// 2. Empty reports
	emptyText, emptyPages := b.getDiagnosticsPageText(nil, 1)
	if emptyPages != 1 || !strings.Contains(emptyText, "Нет доступных прокси") {
		t.Errorf("unexpected empty reports result: pages=%d, text=%s", emptyPages, emptyText)
	}

	emptyRich := b.buildDiagnosticsRichMessage(nil)
	if emptyRich == nil || len(emptyRich.Blocks) == 0 {
		t.Errorf("expected non-empty rich message for empty reports")
	}

	// 3. Mock reports (12 proxies -> 3 pages of 5, 5, 2)
	reports := make([]checker.ProxyDiagReport, 12)
	for i := 0; i < 12; i++ {
		status := "online"
		if i == 1 {
			status = "degraded"
		} else if i == 5 {
			status = "offline"
		}
		reports[i] = checker.ProxyDiagReport{
			ProxyName: fmt.Sprintf("Proxy-%d", i+1),
			Protocol:  "vless",
			Port:      443,
			Status:    status,
			NodeHealth: checker.NodeHealth{
				ResolvedIP: "1.2.3.4",
				TCPPing:    50 * time.Millisecond,
			},
			Targets: []checker.TargetDiagResult{
				{
					URL:     "https://cp.cloudflare.com/generate_204",
					Success: status != "offline",
					Latency: 100 * time.Millisecond,
				},
			},
		}
	}

	// Test page 1
	p1Text, totalPages := b.getDiagnosticsPageText(reports, 1)
	if totalPages != 3 {
		t.Fatalf("expected 3 total pages, got %d", totalPages)
	}
	if !strings.Contains(p1Text, "Стр. 1 из 3") {
		t.Errorf("expected header with page 1 of 3, got: %s", p1Text)
	}
	if !strings.Contains(p1Text, "Proxy-1") || !strings.Contains(p1Text, "Proxy-5") {
		t.Errorf("expected Proxy-1 and Proxy-5 on page 1, got: %s", p1Text)
	}
	if strings.Contains(p1Text, "Proxy-6") {
		t.Errorf("Proxy-6 should not be on page 1")
	}

	// Test page 3
	p3Text, _ := b.getDiagnosticsPageText(reports, 3)
	if !strings.Contains(p3Text, "Proxy-11") || !strings.Contains(p3Text, "Proxy-12") {
		t.Errorf("expected Proxy-11 and Proxy-12 on page 3, got: %s", p3Text)
	}
	if strings.Contains(p3Text, "Proxy-10") {
		t.Errorf("Proxy-10 should not be on page 3")
	}

	// Test rich message generation
	richMsg := b.buildDiagnosticsRichMessage(reports)
	if richMsg == nil {
		t.Fatalf("expected non-nil rich message")
	}
	if len(richMsg.Blocks) < 3 {
		t.Errorf("expected at least heading, table, divider and detail blocks, got %d blocks", len(richMsg.Blocks))
	}

	// 4. Test UDP protocol formatting
	var udpSb strings.Builder
	formatSingleProxyDiag(&udpSb, checker.ProxyDiagReport{
		ProxyName: "Hysteria-Node",
		Protocol:  "hysteria",
		Port:      2077,
		Status:    "online",
		Targets: []checker.TargetDiagResult{
			{URL: "https://cp.cloudflare.com/generate_204", Success: true, Latency: 120 * time.Millisecond},
		},
	})
	udpStr := udpSb.String()
	if !strings.Contains(udpStr, "UDP / QUIC") {
		t.Errorf("expected UDP / QUIC in formatSingleProxyDiag output, got: %s", udpStr)
	}
	if strings.Contains(udpStr, "TCP") {
		t.Errorf("did not expect TCP in formatSingleProxyDiag for hysteria, got: %s", udpStr)
	}
}

type mockSource struct {
	metrics []metrics.ProxyMetric
}

func (m *mockSource) MetricsSnapshot() []metrics.ProxyMetric {
	return m.metrics
}

type mockDiagSource struct {
	reports []checker.ProxyDiagReport
	hosts   []string
}

func (m *mockDiagSource) RunDiagnostics(targets []string) []checker.ProxyDiagReport {
	return m.reports
}

func (m *mockDiagSource) GetTargetManager() *checker.TargetManager {
	return nil
}

func (m *mockDiagSource) GetUniqueHosts() []string {
	return m.hosts
}

func TestBot_MenuTextAndSettings(t *testing.T) {
	ms := &mockSource{
		metrics: []metrics.ProxyMetric{
			{Name: "Node-1", Online: true},
			{Name: "Node-2", Online: false},
			{Name: "Node-3", Online: true, Disabled: true},
		},
	}

	b := &Bot{
		source: ms,
	}

	menuText := b.getMenuText()
	if !strings.Contains(menuText, "📊 Сводка Xray Checker") {
		t.Errorf("expected header '📊 Сводка Xray Checker', got: %s", menuText)
	}
	if !strings.Contains(menuText, "• Текущий статус: <b>1/2 онлайн</b>") {
		t.Errorf("expected 1/2 online with disabled excluded, got: %s", menuText)
	}
	if !strings.Contains(menuText, "1 отключено") {
		t.Errorf("expected mention of 1 disabled node, got: %s", menuText)
	}
	if !strings.Contains(menuText, "🔴 Требуют внимания:") || !strings.Contains(menuText, "Node-2") {
		t.Errorf("expected offline Node-2 in menu summary, got: %s", menuText)
	}

	// Test MainMenuMarkup
	mainMarkup := MainMenuMarkup()
	if len(mainMarkup.InlineKeyboard) != 5 {
		t.Errorf("expected 5 rows in MainMenuMarkup, got %d", len(mainMarkup.InlineKeyboard))
	}
	if mainMarkup.InlineKeyboard[0][0].CallbackData != "menu:main" && mainMarkup.InlineKeyboard[0][0].CallbackData != "menu:main:refresh" {
		t.Errorf("expected refresh button in row 1, got %s", mainMarkup.InlineKeyboard[0][0].CallbackData)
	}
	if mainMarkup.InlineKeyboard[1][0].CallbackData != "menu:diag" {
		t.Errorf("expected diag button in row 2, got %s", mainMarkup.InlineKeyboard[1][0].CallbackData)
	}
	if mainMarkup.InlineKeyboard[2][0].CallbackData != "menu:checkhost" {
		t.Errorf("expected checkhost button in row 3, got %s", mainMarkup.InlineKeyboard[2][0].CallbackData)
	}
	if mainMarkup.InlineKeyboard[3][0].CallbackData != "menu:nodes" {
		t.Errorf("expected nodes button in row 4, got %s", mainMarkup.InlineKeyboard[3][0].CallbackData)
	}
	if mainMarkup.InlineKeyboard[4][0].CallbackData != "menu:settings" {
		t.Errorf("expected settings button in row 5, got %s", mainMarkup.InlineKeyboard[4][0].CallbackData)
	}

	// Verify SettingsMenuMarkup has no digest button and has disabled_proxies button
	settingsMarkup := SettingsMenuMarkup()
	for _, row := range settingsMarkup.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "menu:digest:now" {
				t.Errorf("redundant menu:digest:now button should be removed from SettingsMenuMarkup")
			}
		}
	}
}

func TestBot_DisabledProxiesView(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bot_cfg.json")
	cm, err := NewConfigManager(cfgPath, BotConfig{
		DisabledProxies: []string{"id-node2"},
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	ms := &mockSource{
		metrics: []metrics.ProxyMetric{
			{Name: "Node-1", StableID: "id-node1", Online: true},
			{Name: "Node-2", StableID: "id-node2", Online: false},
			{Name: "Node-3", StableID: "id-node3", Online: true},
		},
	}

	b := &Bot{
		source:    ms,
		configMgr: cm,
	}

	viewText, markup := b.getDisabledProxiesView(1)
	if !strings.Contains(viewText, "🚫 Управление прокси-хостами") {
		t.Errorf("expected view title, got: %s", viewText)
	}
	if !strings.Contains(viewText, "Всего прокси-хостов: <b>3</b> | Отключено: <b>1</b>") {
		t.Errorf("expected node counts, got: %s", viewText)
	}

	// Check buttons in markup
	foundDisabled := false
	foundEnabled := false
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.Text, "Node-2") && strings.Contains(btn.Text, "⏸️") {
				foundDisabled = true
			}
			if strings.Contains(btn.Text, "Node-1") && strings.Contains(btn.Text, "🟢") {
				foundEnabled = true
			}
		}
	}
	if !foundDisabled || !foundEnabled {
		t.Errorf("expected enabled and disabled buttons in markup, got: %v", markup)
	}

	// Verify menu text immediately reflects disabled node from cfg
	menuText := b.getMenuText()
	if !strings.Contains(menuText, "• Текущий статус: <b>2/2 онлайн</b>") {
		t.Errorf("expected 2/2 online in menu text, got: %s", menuText)
	}
	if !strings.Contains(menuText, "1 отключено") {
		t.Errorf("expected 1 отключено in menu text, got: %s", menuText)
	}
}

func TestBot_DisabledHostsView(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bot_cfg.json")
	cm, err := NewConfigManager(cfgPath, BotConfig{
		DisabledHosts: []string{"node2.example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	ds := &mockDiagSource{
		hosts: []string{"node1.example.com", "node2.example.com", "node3.example.com"},
	}

	b := &Bot{
		configMgr:  cm,
		diagSource: ds,
	}

	viewText, markup := b.getDisabledHostsView(1)
	if !strings.Contains(viewText, "🚫 Управление проверками хостов") {
		t.Errorf("expected view title, got: %s", viewText)
	}
	if !strings.Contains(viewText, "Всего обнаружено хостов: <b>3</b> | Отключено: <b>1</b>") {
		t.Errorf("expected host counts, got: %s", viewText)
	}

	// Check buttons in markup
	foundDisabled := false
	foundEnabled := false
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.Text, "node2.example.com") && strings.Contains(btn.Text, "⏸️") {
				foundDisabled = true
			}
			if strings.Contains(btn.Text, "node1.example.com") && strings.Contains(btn.Text, "🟢") {
				foundEnabled = true
			}
		}
	}
	if !foundDisabled || !foundEnabled {
		t.Errorf("expected enabled and disabled buttons in markup, got: %v", markup)
	}
}

func TestCheckHostSettingsMarkupAndText(t *testing.T) {
	cfg := BotConfig{
		CheckHostBgEnabled:     true,
		CheckHostIntervalHours: 2,
		CheckHostAlertEnabled:  true,
	}

	markup := CheckHostSettingsMarkup(cfg)
	if len(markup.InlineKeyboard) < 4 {
		t.Fatalf("expected at least 4 rows in CheckHostSettingsMarkup, got %d", len(markup.InlineKeyboard))
	}

	// Row 1: toggle bg
	if !strings.Contains(markup.InlineKeyboard[0][0].Text, "вкл") {
		t.Errorf("expected вкл in bg toggle button, got %s", markup.InlineKeyboard[0][0].Text)
	}
	if markup.InlineKeyboard[0][0].CallbackData != "menu:checkhost:toggle_bg" {
		t.Errorf("expected menu:checkhost:toggle_bg callback, got %s", markup.InlineKeyboard[0][0].CallbackData)
	}

	// Row 2: toggle alert
	if !strings.Contains(markup.InlineKeyboard[1][0].Text, "вкл") {
		t.Errorf("expected вкл in alert toggle button, got %s", markup.InlineKeyboard[1][0].Text)
	}

	// Row 3: interval 2h active
	found2h := false
	for _, b := range markup.InlineKeyboard[2] {
		if strings.Contains(b.Text, "🟢 2 ч") {
			found2h = true
		}
	}
	if !found2h {
		t.Errorf("expected active 🟢 2 ч button in row 3, got: %v", markup.InlineKeyboard[2])
	}

	// Test text
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bot_cfg.json")
	cm, _ := NewConfigManager(cfgPath, cfg)
	bot := &Bot{configMgr: cm}

	txt := bot.getCheckHostSettingsText()
	if !strings.Contains(txt, "Включена") || !strings.Contains(txt, "каждые 2 ч.") {
		t.Errorf("unexpected checkhost settings text: %s", txt)
	}
}

func TestBot_RunCheckHostAudit_RUBlock(t *testing.T) {
	ruSuccess := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "audit1",
				"permanent_link": "https://check-host.net/check-report/audit1",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "1.2.3.4", "AS1"},
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/audit1") {
			ruRes := []interface{}{map[string]interface{}{"error": "Connection timed out"}}
			if ruSuccess {
				ruRes = []interface{}{map[string]interface{}{"time": 0.05, "address": "1.1.1.1"}}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ru2.node.check-host.net": ruRes,
				"de1.node.check-host.net": []interface{}{map[string]interface{}{"time": 0.02, "address": "1.1.1.1"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	chClient := checker.NewCheckHostClient(ts.URL, 10*time.Millisecond)
	chClient.HTTPClient = ts.Client()

	ms := &mockSource{
		metrics: []metrics.ProxyMetric{
			{Name: "Node-1", Address: "1.1.1.1:443", StableID: "node-1", Protocol: "vless", Online: true},
			{Name: "Node-2-Disabled", Address: "2.2.2.2:443", StableID: "node-2", Protocol: "vless", Online: true, Disabled: true},
		},
	}

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bot_cfg.json")
	cm, _ := NewConfigManager(cfgPath, BotConfig{
		CheckHostBgEnabled:    true,
		CheckHostAlertEnabled: true,
		AlertMode:             AlertModeClean,
	})

	tracker := NewAlertTracker()
	bot := &Bot{
		source:          ms,
		configMgr:       cm,
		tracker:         tracker,
		targets:         []ChatTarget{{ChatID: 12345}},
		checkHostClient: chClient,
		checkHostNodes:  []string{"ru2.node.check-host.net", "de1.node.check-host.net"},
		ctx:             context.Background(),
	}

	// 1. First audit: RU is down -> alert tracked
	bot.RunCheckHostAudit()

	alertKey := "checkhost:1.1.1.1:443"
	if !tracker.HasAlert(12345, 0, alertKey) {
		t.Errorf("expected active alert for %s after RU block", alertKey)
	}
	// Verify disabled node-2 was not audited or tracked
	if tracker.HasAlert(12345, 0, "checkhost:2.2.2.2:443") {
		t.Errorf("disabled node should not have active alert")
	}

	// 2. Second audit with same down state -> deduplicated
	bot.RunCheckHostAudit()
	if !tracker.HasAlert(12345, 0, alertKey) {
		t.Errorf("expected alert to remain active")
	}

	// 3. RU recovers -> alert resolved
	ruSuccess = true
	bot.RunCheckHostAudit()
	if tracker.HasAlert(12345, 0, alertKey) {
		t.Errorf("expected alert to be resolved after RU recovery")
	}
}

func TestBot_Stop_SavesStatsStore(t *testing.T) {
	tmpDir := t.TempDir()
	statsPath := filepath.Join(tmpDir, "saved_stats.json")

	store, err := NewStatsStore(statsPath)
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}

	store.RecordCheck("p-stop", "Proxy Stop", true, 25.0)

	bot := &Bot{
		statsStore: store,
		stopChan:   make(chan struct{}),
	}

	bot.Stop()

	// Verify stats file was created on disk by Stop()
	reloaded, err := NewStatsStore(statsPath)
	if err != nil {
		t.Fatalf("failed to load stats saved on Stop: %v", err)
	}
	ps := reloaded.GetProxyStats("p-stop")
	if ps == nil || ps.TotalChecks != 1 {
		t.Errorf("expected 1 check persisted on Stop, got %+v", ps)
	}
}
