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

	"github.com/mymmrac/telego"

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

	// 2. Check CheckHostMenuMarkup renders the union host list (Wave H6
	//    register: the per-proxy button pin menu:checkhost:run:id-1 was
	//    replaced together with the proxy-list source — authorized 2026-11-17).
	items := []CheckHostListItem{
		{Address: "1.1.1.1:443", Auto: true},
		{Address: "9.9.9.9:443"},
	}
	chMarkup := CheckHostMenuMarkup(items, 1, 1)
	if chMarkup == nil || len(chMarkup.InlineKeyboard) < 2 {
		t.Fatalf("expected rows in CheckHostMenuMarkup, got %v", chMarkup)
	}

	foundAuto := false
	foundCustomDel := false
	for _, row := range chMarkup.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:checkhosth:1.1.1.1:443" {
				foundAuto = true
			}
			if b.CallbackData == "menu:checkhostdel:9.9.9.9:443" {
				foundCustomDel = true
			}
		}
	}
	if !foundAuto {
		t.Errorf("expected menu:checkhosth:1.1.1.1:443 check button in CheckHostMenuMarkup")
	}
	if !foundCustomDel {
		t.Errorf("expected menu:checkhostdel:9.9.9.9:443 delete button in CheckHostMenuMarkup")
	}

	// 3. Check getCheckHostMenuText (wave K-B3 register: the header carries
	//    the 📡 antenna icon — 🌐 is freed for web mentions)
	bot := &Bot{}
	txt := bot.getCheckHostMenuText()
	if txt == "" || !strings.Contains(txt, "/checkhost") {
		t.Errorf("expected /checkhost in menu text, got: %s", txt)
	}
	if !strings.Contains(txt, "📡") || strings.Contains(txt, "🌐") {
		t.Errorf("expected the 📡 Check-Host header (no 🌐), got: %s", txt)
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

func (m *mockDiagSource) GetCurrentIP() (string, error) {
	return "1.2.3.4", nil
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

	// Test MainMenuMarkup (Wave D: Cheburcheck row inserted between the
	// Check-Host row and the Nodes row — plan D1/D3 placement mandate).
	// Wave K-B3 register (authorized IA rework 2026-11-18): the «📈
	// Статистика» row is promoted into the main menu (row 6) — 7 rows total.
	mainMarkup := MainMenuMarkup()
	if len(mainMarkup.InlineKeyboard) != 7 {
		t.Errorf("expected 7 rows in MainMenuMarkup, got %d", len(mainMarkup.InlineKeyboard))
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
	if mainMarkup.InlineKeyboard[3][0].CallbackData != "menu:chebur" {
		t.Errorf("expected chebur button in row 4, got %s", mainMarkup.InlineKeyboard[3][0].CallbackData)
	}
	if mainMarkup.InlineKeyboard[4][0].CallbackData != "menu:nodes" {
		t.Errorf("expected nodes button in row 5, got %s", mainMarkup.InlineKeyboard[4][0].CallbackData)
	}
	if mainMarkup.InlineKeyboard[5][0].CallbackData != "menu:stats" {
		t.Errorf("expected stats button in row 6 (wave K-B3 promotion), got %s", mainMarkup.InlineKeyboard[5][0].CallbackData)
	}
	if mainMarkup.InlineKeyboard[6][0].CallbackData != "menu:settings" {
		t.Errorf("expected settings button in row 7, got %s", mainMarkup.InlineKeyboard[6][0].CallbackData)
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

func TestBot_AdminUserIDs(t *testing.T) {
	b := &Bot{}
	// Empty admin IDs -> everyone authorized
	if !b.isAuthorizedMutating(12345) {
		t.Errorf("expected user 12345 to be authorized when adminUserIDs is empty")
	}

	// Configured admin IDs
	b.SetAdminUserIDs([]int64{1001, 1002})
	if !b.isAuthorizedMutating(1001) {
		t.Errorf("expected admin user 1001 to be authorized")
	}
	if !b.isAuthorizedMutating(1002) {
		t.Errorf("expected admin user 1002 to be authorized")
	}
	if b.isAuthorizedMutating(9999) {
		t.Errorf("expected non-admin user 9999 to be rejected")
	}
}

// --- Wave K-B3: command registry, settings toggles, subs arm flow, icon wiring. ---

// fakeSubsManager is a minimal SubscriptionManager stub for registry/flow tests.
type fakeSubsManager struct {
	static  []string
	dynamic []string
}

func (f *fakeSubsManager) Static() []string  { return f.static }
func (f *fakeSubsManager) Dynamic() []string { return f.dynamic }
func (f *fakeSubsManager) AddSubscription(url string) (int, error) {
	f.dynamic = append(f.dynamic, url)
	return 3, nil
}
func (f *fakeSubsManager) RemoveSubscription(url string) (bool, int, error) {
	for i, u := range f.dynamic {
		if u == url {
			f.dynamic = append(f.dynamic[:i], f.dynamic[i+1:]...)
			return true, 2, nil
		}
	}
	return false, 0, nil
}

// TestSetupBotMenuButton_FullRegistry pins the wave K-B3 SetMyCommands
// registry: the full ~25-command list mirrors the /help groups, the subs and
// node command groups are conditional on the managers being attached, and
// every RU description stays within the 64-char Bot API limit.
func TestSetupBotMenuButton_FullRegistry(t *testing.T) {
	descLimit := 64

	// 1. Bare bot: no subs, no node manager.
	ft, api := newFakeTelegramBot(t)
	b := &Bot{api: api, ctx: context.Background()}
	b.setupBotMenuButton()

	calls := ft.callsOf("setMyCommands")
	if len(calls) != 1 {
		t.Fatalf("expected exactly one setMyCommands call, got %d", len(calls))
	}
	raw, err := json.Marshal(calls[0].Params["commands"])
	if err != nil {
		t.Fatalf("failed to re-marshal commands: %v", err)
	}
	var cmds []telego.BotCommand
	if err := json.Unmarshal(raw, &cmds); err != nil {
		t.Fatalf("failed to decode commands: %v", err)
	}
	if len(cmds) < 15 {
		t.Errorf("expected the full base registry (>= 15 commands), got %d", len(cmds))
	}
	byName := make(map[string]string, len(cmds))
	for _, c := range cmds {
		if len([]rune(c.Description)) > descLimit {
			t.Errorf("command /%s description exceeds %d chars: %q", c.Command, descLimit, c.Description)
		}
		byName[c.Command] = c.Description
	}
	for _, want := range []string{"menu", "status", "diag", "stats", "settings", "checkhost", "checkhost_bg", "cheburcheck", "interval", "quiet", "tz", "targets", "checkupdate", "help", "id"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("expected /%s in the base registry, got %v", want, byName)
		}
	}
	for _, absent := range []string{"addsub", "delsub", "subs", "nodes", "nodeadd"} {
		if _, ok := byName[absent]; ok {
			t.Errorf("command /%s must be conditional on its manager, got present", absent)
		}
	}

	// 2. With subs + node manager the conditional groups join.
	ft2, api2 := newFakeTelegramBot(t)
	b2 := &Bot{api: api2, ctx: context.Background(), subs: &fakeSubsManager{}, nodeMgr: &fakeNodeManager{nodes: map[string]string{"m31a": "tok"}}}
	b2.setupBotMenuButton()
	calls2 := ft2.callsOf("setMyCommands")
	if len(calls2) != 1 {
		t.Fatalf("expected exactly one setMyCommands call with managers, got %d", len(calls2))
	}
	raw2, _ := json.Marshal(calls2[0].Params["commands"])
	var cmds2 []telego.BotCommand
	_ = json.Unmarshal(raw2, &cmds2)
	byName2 := make(map[string]string, len(cmds2))
	for _, c := range cmds2 {
		byName2[c.Command] = c.Description
	}
	for _, want := range []string{"subs", "addsub", "delsub", "nodes", "nodeadd", "nodedel", "nodesubs", "nodeaddsub", "nodedelsub", "nodeset", "nodereset", "noderename"} {
		if _, ok := byName2[want]; !ok {
			t.Errorf("expected /%s when its manager is attached, got %v", want, byName2)
		}
	}
	if len(cmds2) <= len(cmds) {
		t.Errorf("manager-attached registry must be larger: %d vs %d", len(cmds2), len(cmds))
	}
	if len(cmds2) < 25 {
		t.Errorf("expected the full ~25-command registry with managers attached, got %d", len(cmds2))
	}
}

// TestSettingsTogglesDigestAndIcons pins the wave K-B3 settings toggles: the
// exact routes menu:nodes:digest / menu:toggle_icons are admin-gated
// mutations, persist through updateConfig, and the icons toggle re-syncs the
// package-level custom-emoji layer.
func TestSettingsTogglesDigestAndIcons(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, cm := newGateTestBot(t, api, []int64{1001})

	// Non-admins are denied (mutations).
	b.handleCallbackQuery(gateCallback(9999, "menu:nodes:digest"))
	if cm.Get().DayDigestEnabled {
		t.Fatalf("non-admin must not toggle the digest")
	}

	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:digest"))
	if !cm.Get().DayDigestEnabled {
		t.Errorf("admin digest toggle must flip DayDigestEnabled on (persisted)")
	}
	// (The scheduler clamps a non-positive DayDigestIntervalHours to 6h at
	// runtime, so the toggle itself needs no interval normalization.)
	// Outlast the double-tap debounce window before the paired flip.
	time.Sleep(310 * time.Millisecond)
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:digest"))
	if cm.Get().DayDigestEnabled {
		t.Errorf("second digest toggle must flip DayDigestEnabled off")
	}

	b.handleCallbackQuery(gateCallback(1001, "menu:toggle_icons"))
	if !cm.Get().CustomEmoji {
		t.Errorf("admin icons toggle must persist CustomEmoji=true")
	}
	if !customEmojiEnabled.Load() {
		t.Errorf("the icons toggle must re-sync the package icon layer (B1's atomic flag) to enabled")
	}
	// The settings screen re-renders with the new state.
	if edit, ok := ft.lastParamOf("editMessageText", "text"); !ok || edit == "" {
		t.Errorf("expected the settings re-render after the icons toggle")
	}
	time.Sleep(310 * time.Millisecond)
	b.handleCallbackQuery(gateCallback(1001, "menu:toggle_icons"))
	if cm.Get().CustomEmoji {
		t.Errorf("second icons toggle must persist CustomEmoji=false")
	}
	if customEmojiEnabled.Load() {
		t.Errorf("the package icon layer must be re-synced to disabled")
	}
}

// TestSubsAddArmFlow pins the wave K-B3 «➕ Добавить подписку» arm flow
// (migrated to the wave L-1 pendingInput FSM): menu:subsadd is an
// admin-gated mutation that arms the per-chat pending input and renders the
// RU prompt; the next plain text is consumed in handleMessage via takePending
// → handleSubURLInput and persisted via the subscription manager; non-admin
// input is denied; navigation disarms.
func TestSubsAddArmFlow(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, []int64{1001})
	subs := &fakeSubsManager{}
	b.subs = subs

	// 1. The arm button lives on the subs screen markup.
	if !hasCallback(SubsMenuMarkup(), "menu:subsadd") {
		t.Fatalf("expected menu:subsadd on SubsMenuMarkup")
	}

	// 2. Admin taps «➕ Добавить подписку»: waiting state armed, prompt shown.
	b.handleCallbackQuery(gateCallback(1001, "menu:subsadd"))
	if !b.hasPending(12345) {
		t.Fatalf("menu:subsadd must arm the pending input for the chat")
	}
	if prompt, ok := ft.lastParamOf("editMessageText", "text"); !ok || !strings.Contains(prompt, "Добавление подписки") || !strings.Contains(prompt, "Отправьте") {
		t.Errorf("expected the RU add-subscription prompt, got %q", prompt)
	}

	// 3. The next plain text is consumed and persisted.
	b.dispatchUpdate(telego.Update{Message: &telego.Message{
		MessageID: 11,
		Chat:      telego.Chat{ID: 12345, Type: "private"},
		From:      &telego.User{ID: 1001, Username: "admin"},
		Text:      "https://example.com/sub",
	}})
	if b.hasPending(12345) {
		t.Fatalf("pending input must be consumed after the input")
	}
	if len(subs.dynamic) != 1 || subs.dynamic[0] != "https://example.com/sub" {
		t.Fatalf("expected the subscription persisted via the manager, got %v", subs.dynamic)
	}

	// 4. A non-http URL is rejected without a write.
	b.handleCallbackQuery(gateCallback(1001, "menu:subsadd"))
	b.dispatchUpdate(telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: 12345, Type: "private"},
		From: &telego.User{ID: 1001, Username: "admin"},
		Text: "file:///etc/passwd",
	}})
	if len(subs.dynamic) != 1 {
		t.Errorf("non-http input must not reach the manager, got %v", subs.dynamic)
	}

	// 5. Navigation to the main menu disarms the pending input.
	b.handleCallbackQuery(gateCallback(1001, "menu:subsadd"))
	if !b.hasPending(12345) {
		t.Fatalf("expected the armed state before navigation")
	}
	b.handleCallbackQuery(gateCallback(1001, "menu:main"))
	if b.hasPending(12345) {
		t.Fatalf("menu:main must disarm the pending input")
	}

	// 6. Non-admin input while armed: denied, no write.
	b.handleCallbackQuery(gateCallback(1001, "menu:subsadd"))
	b.dispatchUpdate(telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: 12345, Type: "private"},
		From: &telego.User{ID: 9999, Username: "intruder"},
		Text: "https://evil.example.com/sub",
	}})
	if b.hasPending(12345) {
		t.Fatalf("pending input must be consumed even when denied")
	}
	if len(subs.dynamic) != 1 {
		t.Errorf("non-admin input must not write subscriptions, got %v", subs.dynamic)
	}
}

// TestCustomEmojiWiring pins the wave K-B3 wiring: b.customEmojiEnabled reads
// the persisted BotConfig, and syncIconLayer (called on SetConfigManager and
// after every updateConfig) mirrors the setting into the package-level icon
// layer used by the markup builders.
func TestCustomEmojiWiring(t *testing.T) {
	t.Cleanup(func() { SetCustomEmojiEnabled(false) })

	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	b := &Bot{}
	b.SetConfigManager(cm)

	if b.customEmojiEnabled() {
		t.Errorf("default CustomEmoji must be off")
	}
	if customEmojiEnabled.Load() {
		t.Errorf("syncIconLayer must mirror the off default")
	}

	_ = cm.Update(func(c *BotConfig) { c.CustomEmoji = true })
	if b.customEmojiEnabled() != true {
		t.Errorf("customEmojiEnabled must read the persisted config")
	}
	_ = b.updateConfig(func(c *BotConfig) { c.RichMode = true })
	if !customEmojiEnabled.Load() {
		t.Errorf("updateConfig must re-sync the icon layer (reconfig wiring)")
	}
	_ = b.updateConfig(func(c *BotConfig) { c.CustomEmoji = false })
	if customEmojiEnabled.Load() {
		t.Errorf("updateConfig must re-sync the icon layer back to disabled")
	}
}
