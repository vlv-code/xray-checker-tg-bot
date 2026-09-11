package telegram

import (
	"fmt"
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
	if !strings.Contains(menuText, "• Текущий статус: <b>1/2 online</b>") {
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
	if len(mainMarkup.InlineKeyboard) != 4 {
		t.Errorf("expected 4 rows in MainMenuMarkup, got %d", len(mainMarkup.InlineKeyboard))
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
	if mainMarkup.InlineKeyboard[3][0].CallbackData != "menu:settings" {
		t.Errorf("expected settings button in row 4, got %s", mainMarkup.InlineKeyboard[3][0].CallbackData)
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



