package telegram

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
)

func TestMenuMarkups(t *testing.T) {
	cfg := BotConfig{
		QuietHoursEnabled: true,
		QuietHoursStart:   "23:00",
		QuietHoursEnd:     "08:00",
		AlertMode:         AlertModeLive,
		TargetURLs:        []string{"https://cp.cloudflare.com/generate_204"},
	}

	mainMenu := MainMenuMarkup()
	if len(mainMenu.InlineKeyboard) < 3 {
		t.Errorf("expected at least 3 rows in main menu, got %d", len(mainMenu.InlineKeyboard))
	}

	quietMenu := QuietHoursMarkup(cfg)
	if len(quietMenu.InlineKeyboard) < 2 {
		t.Errorf("expected at least 2 rows in quiet menu, got %d", len(quietMenu.InlineKeyboard))
	}

	alertMenu := AlertModeMarkup(cfg)
	if len(alertMenu.InlineKeyboard) < 2 {
		t.Errorf("expected at least 2 rows in alert menu, got %d", len(alertMenu.InlineKeyboard))
	}

	intervalMenu := IntervalMenuMarkup(300)
	if len(intervalMenu.InlineKeyboard) < 3 {
		t.Errorf("expected at least 3 rows in interval menu, got %d", len(intervalMenu.InlineKeyboard))
	}

	mainMarkup := MainMenuMarkup()
	if len(mainMarkup.InlineKeyboard) < 4 {
		t.Fatalf("expected at least 4 rows in main menu, got %d", len(mainMarkup.InlineKeyboard))
	}
	// Wave K-B3 register (authorized legend rework 2026-11-18): the
	// diagnostics magnifier follows the frozen legend fallback 🔍
	// (IconDiagnostics), replacing the former «🔎 Подробнее».
	if mainMarkup.InlineKeyboard[1][0].Text != "🔍 Подробнее" {
		t.Errorf("expected button text '🔍 Подробнее', got '%s'", mainMarkup.InlineKeyboard[1][0].Text)
	}

	// 1. Single page pagination markup (Details view)
	singlePageMarkup := DiagPaginationMarkup(1, 1)
	if len(singlePageMarkup.InlineKeyboard) != 3 {
		t.Errorf("expected 3 rows for single page markup (deep check + actions + back), got %d", len(singlePageMarkup.InlineKeyboard))
	}
	if singlePageMarkup.InlineKeyboard[0][0].CallbackData != "menu:diag:pick_deep:1" {
		t.Errorf("expected deep check trigger button menu:diag:pick_deep:1, got %s", singlePageMarkup.InlineKeyboard[0][0].CallbackData)
	}
	if singlePageMarkup.InlineKeyboard[0][0].Text != "🔬 Углубленная проверка соединения" {
		t.Errorf("expected button text '🔬 Углубленная проверка соединения', got '%s'", singlePageMarkup.InlineKeyboard[0][0].Text)
	}
	if singlePageMarkup.InlineKeyboard[1][0].CallbackData != "menu:diag" {
		t.Errorf("expected back to summary button menu:diag, got %s", singlePageMarkup.InlineKeyboard[1][0].CallbackData)
	}
	// Wave K-B3 register (back-row unification): the parent-back arrow is the
	// frozen ↩️ (was «🔙 К подробной сводке»).
	if singlePageMarkup.InlineKeyboard[1][0].Text != "↩️ К подробной сводке" {
		t.Errorf("expected button text '↩️ К подробной сводке', got '%s'", singlePageMarkup.InlineKeyboard[1][0].Text)
	}

	// 2. Multi-page pagination markup (Details view)
	multiPageMarkup := DiagPaginationMarkup(2, 4)
	if len(multiPageMarkup.InlineKeyboard) != 4 {
		t.Fatalf("expected 4 rows for multi-page markup (nav + deep check + actions + back), got %d", len(multiPageMarkup.InlineKeyboard))
	}
	navRow := multiPageMarkup.InlineKeyboard[0]
	if len(navRow) != 3 {
		t.Fatalf("expected 3 navigation buttons, got %d", len(navRow))
	}
	if navRow[0].CallbackData != "menu:diag:details:1" && navRow[0].CallbackData != "menu:diag:p:1" {
		t.Errorf("expected prev page 1, got %s", navRow[0].CallbackData)
	}
	if navRow[1].CallbackData != "menu:diag:noop:2:4" {
		t.Errorf("expected noop counter callback, got %s", navRow[1].CallbackData)
	}
	if navRow[2].CallbackData != "menu:diag:details:3" && navRow[2].CallbackData != "menu:diag:p:3" {
		t.Errorf("expected next page 3, got %s", navRow[2].CallbackData)
	}
	deepRow := multiPageMarkup.InlineKeyboard[1]
	if deepRow[0].CallbackData != "menu:diag:pick_deep:2" {
		t.Errorf("expected deep check trigger callback menu:diag:pick_deep:2, got %s", deepRow[0].CallbackData)
	}
	actionRow := multiPageMarkup.InlineKeyboard[2]
	if actionRow[0].CallbackData != "menu:diag" {
		t.Errorf("expected back to summary callback menu:diag, got %s", actionRow[0].CallbackData)
	}
	if actionRow[1].CallbackData != "menu:diag:refresh:details:2" && actionRow[1].CallbackData != "menu:diag:refresh:2" {
		t.Errorf("expected refresh callback menu:diag:refresh:details:2, got %s", actionRow[1].CallbackData)
	}

	// 3. Rich report markup (Summary view)
	richMarkup := RichReportMarkup()
	if len(richMarkup.InlineKeyboard) < 2 {
		t.Fatalf("expected at least 2 rows in RichReportMarkup, got %d", len(richMarkup.InlineKeyboard))
	}
	if richMarkup.InlineKeyboard[0][0].CallbackData != "menu:diag:details:1" {
		t.Errorf("expected switch to details:1 button, got %s", richMarkup.InlineKeyboard[0][0].CallbackData)
	}
	if richMarkup.InlineKeyboard[0][0].Text != "📑 Детальный отчёт" {
		t.Errorf("expected button text '📑 Детальный отчёт', got '%s'", richMarkup.InlineKeyboard[0][0].Text)
	}
	if richMarkup.InlineKeyboard[1][0].CallbackData != "menu:diag:refresh:summary" && richMarkup.InlineKeyboard[1][0].CallbackData != "menu:diag:refresh:rich" {
		t.Errorf("expected refresh summary button, got %s", richMarkup.InlineKeyboard[1][0].CallbackData)
	}
}

func TestBackToStatsMarkup(t *testing.T) {
	markup := BackToStatsMarkup()
	if len(markup.InlineKeyboard) != 1 {
		t.Fatalf("expected 1 row in BackToStatsMarkup, got %d", len(markup.InlineKeyboard))
	}
	row := markup.InlineKeyboard[0]
	if len(row) != 2 {
		t.Fatalf("expected 2 buttons in BackToStatsMarkup, got %d", len(row))
	}
	if row[0].CallbackData != "menu:stats" {
		t.Errorf("expected first button to return to menu:stats, got %s", row[0].CallbackData)
	}
	if row[1].CallbackData != "menu:main" {
		t.Errorf("expected second button to return to menu:main, got %s", row[1].CallbackData)
	}
}

func TestSettingsMenuIncludesTimezone(t *testing.T) {
	markup := SettingsMenuMarkup()
	found := false
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "menu:timezone" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("expected SettingsMenuMarkup to contain menu:timezone button")
	}
}

func TestTimezoneMarkup(t *testing.T) {
	markup := TimezoneMarkup("Europe/Moscow")
	if len(markup.InlineKeyboard) < 4 {
		t.Fatalf("expected at least 4 rows in TimezoneMarkup, got %d", len(markup.InlineKeyboard))
	}

	foundActive := false
	foundBack := false
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:tz:Europe/Moscow" {
				if !strings.HasPrefix(b.Text, "🟢") {
					t.Errorf("expected active timezone Europe/Moscow to have green circle, got %s", b.Text)
				}
				foundActive = true
			}
			if b.CallbackData == "menu:settings" {
				foundBack = true
			}
		}
	}

	if !foundActive {
		t.Errorf("expected to find menu:tz:Europe/Moscow button")
	}
	if !foundBack {
		t.Errorf("expected to find back button to menu:settings")
	}
}

func TestSettingsMenuSubscriptionsVisibility(t *testing.T) {
	hasSubsBtn := func(markup *telego.InlineKeyboardMarkup) bool {
		for _, row := range markup.InlineKeyboard {
			for _, b := range row {
				if b.CallbackData == "menu:subs" {
					return true
				}
			}
		}
		return false
	}

	if !hasSubsBtn(SettingsMenuMarkup(true)) {
		t.Errorf("expected SettingsMenuMarkup(true) to include menu:subs")
	}
	if !hasSubsBtn(SettingsMenuMarkup()) {
		t.Errorf("expected default SettingsMenuMarkup() to include menu:subs for backward compatibility")
	}
	if hasSubsBtn(SettingsMenuMarkup(false)) {
		t.Errorf("expected SettingsMenuMarkup(false) to omit menu:subs")
	}
}

func TestDeepDiagnosticsMarkup(t *testing.T) {
	mk := DeepDiagnosticsMarkup("stable-123", 2)
	if len(mk.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows in DeepDiagnosticsMarkup, got %d", len(mk.InlineKeyboard))
	}
	backBtn := mk.InlineKeyboard[0][0]
	if backBtn.CallbackData != "menu:diag:details:2" {
		t.Errorf("expected back button to return to menu:diag:details:2, got %s", backBtn.CallbackData)
	}
	// Wave K-B3 register (back-row unification): the parent-back arrow is the
	// frozen ↩️ (was «🔙 К детальному отчёту»).
	if backBtn.Text != "↩️ К детальному отчёту" {
		t.Errorf("expected button text '↩️ К детальному отчёту', got '%s'", backBtn.Text)
	}
	recheckBtn := mk.InlineKeyboard[0][1]
	if recheckBtn.CallbackData != "menu:diag:deep:stable-123:2" {
		t.Errorf("expected recheck callback 'menu:diag:deep:stable-123:2', got %s", recheckBtn.CallbackData)
	}
}

func TestPickDeepDiagnosticsMarkup(t *testing.T) {
	reports := []checker.ProxyDiagReport{
		{ProxyName: "Proxy-1", StableID: "p1", Status: "online"},
		{ProxyName: "Proxy-2", StableID: "p2", Status: "offline"},
	}
	mk := PickDeepDiagnosticsMarkup(reports, 1)
	if len(mk.InlineKeyboard) != 3 { // 2 proxy buttons + 1 back button
		t.Fatalf("expected 3 rows in PickDeepDiagnosticsMarkup, got %d", len(mk.InlineKeyboard))
	}
	if mk.InlineKeyboard[0][0].CallbackData != "menu:diag:deep:p1:1" {
		t.Errorf("expected p1 callback, got %s", mk.InlineKeyboard[0][0].CallbackData)
	}
	if mk.InlineKeyboard[1][0].CallbackData != "menu:diag:deep:p2:1" {
		t.Errorf("expected p2 callback, got %s", mk.InlineKeyboard[1][0].CallbackData)
	}
	backRow := mk.InlineKeyboard[2]
	if backRow[0].CallbackData != "menu:diag:details:1" {
		t.Errorf("expected back callback menu:diag:details:1, got %s", backRow[0].CallbackData)
	}
}

func TestNodesMarkups(t *testing.T) {
	// 1. Main menu has Nodes button
	mainMenu := MainMenuMarkup()
	foundNodes := false
	for _, row := range mainMenu.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:nodes" && b.Text == "🖥 Ноды" {
				foundNodes = true
			}
		}
	}
	if !foundNodes {
		t.Errorf("expected MainMenuMarkup to contain button '🖥 Ноды' with callback 'menu:nodes'")
	}

	// 2. NodesMainMenuMarkup
	nodesMenu := NodesMainMenuMarkup()
	if len(nodesMenu.InlineKeyboard) < 5 {
		t.Fatalf("expected at least 5 rows in NodesMainMenuMarkup, got %d", len(nodesMenu.InlineKeyboard))
	}
	if nodesMenu.InlineKeyboard[0][0].CallbackData != "menu:nodes:add" {
		t.Errorf("expected add button callback, got %s", nodesMenu.InlineKeyboard[0][0].CallbackData)
	}
	if nodesMenu.InlineKeyboard[1][0].CallbackData != "menu:nodes:install" {
		t.Errorf("expected install button callback, got %s", nodesMenu.InlineKeyboard[1][0].CallbackData)
	}
	if nodesMenu.InlineKeyboard[2][0].CallbackData != "menu:nodes:health" {
		t.Errorf("expected health button callback, got %s", nodesMenu.InlineKeyboard[2][0].CallbackData)
	}
	if nodesMenu.InlineKeyboard[3][0].CallbackData != "menu:nodes:settings" {
		t.Errorf("expected settings button callback, got %s", nodesMenu.InlineKeyboard[3][0].CallbackData)
	}

	// 3. NodesSettingsMarkup (Wave H2/D4 register: the «📋 Подписки нод»
	// entry to the removed global subs screen is gone — the markup must not
	// emit menu:nodes:subs anywhere; the last row is the navigation row)
	cfg := BotConfig{
		NodeSyncEnabled:     true,
		NodeAlertsEnabled:   true,
		NodeProxyAlertsChat: false,
	}
	setMenu := NodesSettingsMarkup(cfg)
	if len(setMenu.InlineKeyboard) < 4 {
		t.Fatalf("expected at least 4 rows in NodesSettingsMarkup, got %d", len(setMenu.InlineKeyboard))
	}
	for _, row := range setMenu.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:nodes:subs" {
				t.Errorf("removed route menu:nodes:subs must not be emitted by NodesSettingsMarkup")
			}
		}
	}
	lastSetRow := setMenu.InlineKeyboard[len(setMenu.InlineKeyboard)-1]
	if lastSetRow[0].CallbackData != "menu:nodes" {
		t.Errorf("expected nav row back to menu:nodes, got %s", lastSetRow[0].CallbackData)
	}

	// 4. NodeSubsManageMarkup (Wave H2/D4 register: former NodesSubsListMarkup
	// section removed with the global subs screen)
	nodeSubs := []ManagedSubInfo{
		{URL: "https://sub.one/x", ProxyCount: 15},
		{URL: "https://sub.two/y", ProxyCount: -1},
	}
	manageMarkup := NodeSubsManageMarkup("m31a", nodeSubs)
	// Expect delete buttons for each sub, add button, and back buttons
	if len(manageMarkup.InlineKeyboard) < 4 {
		t.Fatalf("expected at least 4 rows in NodeSubsManageMarkup, got %d", len(manageMarkup.InlineKeyboard))
	}
	if manageMarkup.InlineKeyboard[0][0].CallbackData != "menu:nodes:delsub:m31a:0" {
		t.Errorf("expected delsub row 0, got %s", manageMarkup.InlineKeyboard[0][0].CallbackData)
	}
	if manageMarkup.InlineKeyboard[1][0].CallbackData != "menu:nodes:delsub:m31a:1" {
		t.Errorf("expected delsub row 1, got %s", manageMarkup.InlineKeyboard[1][0].CallbackData)
	}
	// Add sub button
	if manageMarkup.InlineKeyboard[2][0].CallbackData != "menu:nodes:addsub:m31a" {
		t.Errorf("expected addsub callback, got %s", manageMarkup.InlineKeyboard[2][0].CallbackData)
	}
	// Back buttons: per-node cfg screen + global settings (Wave H2/D4 — the
	// former menu:nodes:subs back target is removed)
	backRow := manageMarkup.InlineKeyboard[len(manageMarkup.InlineKeyboard)-1]
	if backRow[0].CallbackData != "menu:nodes:cfg:m31a" {
		t.Errorf("expected back to menu:nodes:cfg:m31a, got %s", backRow[0].CallbackData)
	}
	if len(backRow) < 2 || backRow[1].CallbackData != "menu:nodes:settings" {
		t.Errorf("expected global settings shortcut in back row, got %v", backRow)
	}
}

// --- Wave K-B3 (menus/navigation cluster): back-row unification, 🏠 collision,
// stats promotion, digest/icons toggles, subs arm-button, dual-layer icons. ---

// TestBackRowBuilder pins the unified back-row builder (wave K-B3): every
// back-row is [«↩️ <parent>», «☰ Главное меню»] — the arrow.uturn.backward
// legend icon for the parent navigation and ☰ (not 🏠) for the main menu,
// freeing 🏠 for the master checker tab only.
func TestBackRowBuilder(t *testing.T) {
	row := backRow("К настройкам", "menu:settings")
	if len(row) != 2 {
		t.Fatalf("expected 2 buttons in the unified back row, got %d", len(row))
	}
	if row[0].Text != "↩️ К настройкам" || row[0].CallbackData != "menu:settings" {
		t.Errorf("expected «↩️ К настройкам» -> menu:settings, got %q -> %q", row[0].Text, row[0].CallbackData)
	}
	if row[1].Text != "☰ Главное меню" || row[1].CallbackData != "menu:main" {
		t.Errorf("expected «☰ Главное меню» -> menu:main, got %q -> %q", row[1].Text, row[1].CallbackData)
	}
	for _, b := range row {
		if len(b.CallbackData) > 64 {
			t.Errorf("back-row callback exceeds 64 bytes: %q", b.CallbackData)
		}
	}
}

// kb3SweepMarkups returns every markup owned by menu.go with fixed inputs so
// the sweep covers all surfaces uniformly.
func kb3SweepMarkups(t *testing.T) map[string]*telego.InlineKeyboardMarkup {
	t.Helper()
	cfg := BotConfig{NodeSyncEnabled: true, NodeAlertsEnabled: true, NodeProxyAlertsChat: true}
	report := []checker.ProxyDiagReport{{ProxyName: "P", StableID: "p1", Status: "online"}}
	return map[string]*telego.InlineKeyboardMarkup{
		"MainMenuMarkup":                      MainMenuMarkup(),
		"NodesMainMenuMarkup":                 NodesMainMenuMarkup(),
		"NodesInstallMarkup":                  NodesInstallMarkup(),
		"NodesHealthMarkup":                   NodesHealthMarkup(),
		"NodesSettingsMarkup":                 NodesSettingsMarkup(cfg),
		"NodeSubsManageMarkup":                NodeSubsManageMarkup("m31a", nil),
		"SettingsMenuMarkup":                  SettingsMenuMarkup(BotConfig{}, true),
		"CheckHostSettingsMarkup":             CheckHostSettingsMarkup(cfg),
		"DisabledProxiesMarkup":               DisabledProxiesMarkup(nil, 1, 1),
		"DisabledHostsMarkup":                 DisabledHostsMarkup(nil, nil, 1, 1),
		"StatusMenuMarkup":                    StatusMenuMarkup(),
		"StatsMenuMarkup":                     StatsMenuMarkup(),
		"QuietHoursMarkup":                    QuietHoursMarkup(cfg),
		"AlertModeMarkup":                     AlertModeMarkup(cfg),
		"TargetsMenuMarkup":                   TargetsMenuMarkup(),
		"TimezoneMarkup":                      TimezoneMarkup("UTC"),
		"IntervalMenuMarkup":                  IntervalMenuMarkup(300),
		"CheckHostMenuMarkup":                 CheckHostMenuMarkup([]CheckHostListItem{{Address: "1.1.1.1:443", Auto: true}}, 1, 1),
		"CheburMenuMarkup":                    CheburMenuMarkup([]string{"1.1.1.1"}, 1, 1),
		"CheburResultMarkup":                  CheburResultMarkup(1),
		"SubsMenuMarkup":                      SubsMenuMarkup(),
		"DiagPaginationMarkup":                DiagPaginationMarkup(1, 1),
		"DeepDiagnosticsMarkup":               DeepDiagnosticsMarkup("proxy-x", 1),
		"PickDeepDiagnosticsMarkup":           PickDeepDiagnosticsMarkup(report, 1),
		"RichReportMarkup":                    RichReportMarkup(),
		"DiagnosticsPaginationMarkupWithTabs": DiagnosticsPaginationMarkupWithTabs("local", 1, 1, nil),
		"IncidentsFilterMarkup":               IncidentsFilterMarkup("", nil),
		"CheckHostResultMarkup":               CheckHostResultMarkup(),
		"BackToMenuMarkup":                    BackToMenuMarkup(),
		"BackToSettingsMarkup":                BackToSettingsMarkup(),
		"BackToStatsMarkup":                   BackToStatsMarkup(),
	}
}

// TestMenuMarkups_HomeFreedAndMainMenuUnified sweeps ALL markups owned by
// menu.go (wave K-B3 final 🏠 collision rule): 🏠 appears nowhere (it is
// reserved for the master checker tab labels built by the diagnostics node
// tabs), 🌐 appears nowhere (freed for web/dashboard mentions only), and every
// main-menu navigation button reads exactly «☰ Главное меню».
func TestMenuMarkups_HomeFreedAndMainMenuUnified(t *testing.T) {
	for name, markup := range kb3SweepMarkups(t) {
		if markup == nil {
			t.Fatalf("%s returned nil markup", name)
		}
		for _, row := range markup.InlineKeyboard {
			for _, b := range row {
				if strings.Contains(b.Text, "🏠") {
					t.Errorf("%s: 🏠 must not appear in any button (master-tab only), got %q", name, b.Text)
				}
				if strings.Contains(b.Text, "🌐") {
					t.Errorf("%s: 🌐 must not appear in any button (web-only), got %q", name, b.Text)
				}
				if b.CallbackData == "menu:main" && strings.Contains(b.Text, "Главное меню") {
					// Unified forms (wave K-B3): «☰ Главное меню» on the home
					// shortcut of mixed rows, «↩️ Главное меню» when the main
					// menu IS the parent of a single-button back row. Action
					// buttons targeting menu:main (the «🔄 Обновить» refresh
					// row of MainMenuMarkup) keep their own labels.
					if b.Text != "☰ Главное меню" && b.Text != "↩️ Главное меню" {
						t.Errorf("%s: main-menu navigation button must read «☰/↩️ Главное меню», got %q", name, b.Text)
					}
				}
			}
		}
	}
}

// TestBackRowsUseUnifiedArrow pins the ↩️ parent-back wording across the
// markups: no 🔙 arrows remain in the owned markups, and the back-to-settings
// rows carry the unified [«↩️ К настройкам», «☰ Главное меню»] pair.
func TestBackRowsUseUnifiedArrow(t *testing.T) {
	for name, markup := range kb3SweepMarkups(t) {
		for _, row := range markup.InlineKeyboard {
			for _, b := range row {
				if strings.Contains(b.Text, "🔙") {
					t.Errorf("%s: legacy 🔙 arrow must be replaced by the unified ↩️ back row, got %q", name, b.Text)
				}
			}
		}
	}
	pair := BackToSettingsMarkup()
	if pair.InlineKeyboard[0][0].Text != "↩️ К настройкам" || pair.InlineKeyboard[0][1].Text != "☰ Главное меню" {
		t.Errorf("expected unified [↩️ К настройкам, ☰ Главное меню], got %v", markupCallbacks(pair))
	}
	stats := BackToStatsMarkup()
	if stats.InlineKeyboard[0][0].Text != "↩️ К статистике" || stats.InlineKeyboard[0][1].Text != "☰ Главное меню" {
		t.Errorf("expected unified [↩️ К статистике, ☰ Главное меню], got %v", markupCallbacks(stats))
	}
}

// TestMainMenuMarkup_StatsRowPromoted pins the wave K-B3 IA change: the
// «📈 Статистика» row is promoted into MainMenuMarkup (row 6, between Ноды and
// Настройки) and REMOVED from SettingsMenuMarkup; the Check-Host row carries
// the 📡 antenna icon (never 🌐).
func TestMainMenuMarkup_StatsRowPromoted(t *testing.T) {
	markup := MainMenuMarkup()
	if len(markup.InlineKeyboard) != 7 {
		t.Fatalf("expected 7 rows in MainMenuMarkup (refresh, diag, checkhost, chebur, nodes, stats, settings), got %d", len(markup.InlineKeyboard))
	}
	if markup.InlineKeyboard[5][0].CallbackData != "menu:stats" {
		t.Errorf("expected row 6 to be the stats button menu:stats, got %s", markup.InlineKeyboard[5][0].CallbackData)
	}
	if !strings.Contains(markup.InlineKeyboard[5][0].Text, "Статистика") {
		t.Errorf("expected «Статистика» in the stats button label, got %q", markup.InlineKeyboard[5][0].Text)
	}
	if markup.InlineKeyboard[6][0].CallbackData != "menu:settings" {
		t.Errorf("expected row 7 to be settings, got %s", markup.InlineKeyboard[6][0].CallbackData)
	}
	checkhost := markup.InlineKeyboard[2][0]
	if checkhost.CallbackData != "menu:checkhost" || !strings.Contains(checkhost.Text, "📡") || strings.Contains(checkhost.Text, "🌐") {
		t.Errorf("expected the Check-Host row to carry 📡 (not 🌐), got %q -> %s", checkhost.Text, checkhost.CallbackData)
	}

	settings := SettingsMenuMarkup(BotConfig{}, true)
	for _, row := range settings.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:stats" {
				t.Errorf("menu:stats must be removed from SettingsMenuMarkup (promoted to the main menu)")
			}
		}
	}
}

// TestMainMenuMarkup_CheburShieldIcon pins wave M-D item M8: the Cheburcheck
// main-menu row carries the plain 🛡️ shield fallback via btnIcon (leading
// space, like the sibling icon rows). The pack catalog («Telegram iOS Icons»,
// fetched 2026-11-18) has NO clean shield custom emoji — all three shield
// candidates carry glyphs colliding with existing legend roles (see the
// IconChebur comment) — so even with the ✨ layer ON the button text stays the
// plain fallback (button text has NO parse_mode; raw tg-emoji is banned) and
// IconCustomEmojiID stays EMPTY, documenting the collision verdict against
// anyone "completing" the role with a colliding pack id later.
func TestMainMenuMarkup_CheburShieldIcon(t *testing.T) {
	SetCustomEmojiEnabled(true)
	t.Cleanup(func() { SetCustomEmojiEnabled(false) })

	markup := MainMenuMarkup()
	var chebur *telego.InlineKeyboardButton
	for r := range markup.InlineKeyboard {
		for c := range markup.InlineKeyboard[r] {
			if markup.InlineKeyboard[r][c].CallbackData == "menu:chebur" {
				chebur = &markup.InlineKeyboard[r][c]
			}
		}
	}
	if chebur == nil {
		t.Fatalf("MainMenuMarkup lost the Cheburcheck button (menu:chebur)")
	}
	if chebur.Text != "🛡️ Cheburcheck" {
		t.Errorf("Cheburcheck button Text = %q, want plain %q (Icon.Fallback + label in Text — never raw tg-emoji)", chebur.Text, "🛡️ Cheburcheck")
	}
	if strings.Contains(chebur.Text, "<tg-emoji") {
		t.Errorf("Cheburcheck button Text must never carry a raw <tg-emoji> tag (no parse_mode on button text), got %q", chebur.Text)
	}
	if chebur.IconCustomEmojiID != "" {
		t.Errorf("Cheburcheck button IconCustomEmojiID = %q, want empty — no clean shield in the pack (all three candidates collide with existing legend roles), plain 🛡️ fallback only", chebur.IconCustomEmojiID)
	}
}

// TestSettingsMenuMarkup_DigestAndIconsToggles pins the two new settings
// toggles (wave K-B3): «☀️ Дневная сводка: вкл/выкл» (route menu:nodes:digest)
// and «✨ Иконки (анимация)» (route menu:toggle_icons), both state-aware via
// SettingsMenuMarkupWithCfg, plus the Check-Host settings entry re-iconed to 📡.
func TestSettingsMenuMarkup_DigestAndIconsToggles(t *testing.T) {
	on := SettingsMenuMarkup(BotConfig{DayDigestEnabled: true, CustomEmoji: true}, true)
	var digestTxt, iconsTxt string
	hasDigest, hasIcons, hasCheckhostCfg, hasTimezone, hasQuiet, hasSubs := false, false, false, false, false, false
	for _, row := range on.InlineKeyboard {
		for _, b := range row {
			switch b.CallbackData {
			case "menu:nodes:digest":
				hasDigest = true
				digestTxt = b.Text
			case "menu:toggle_icons":
				hasIcons = true
				iconsTxt = b.Text
			case "menu:checkhost_cfg":
				hasCheckhostCfg = true
			case "menu:timezone":
				hasTimezone = true
			case "menu:quiet":
				hasQuiet = true
			case "menu:subs":
				hasSubs = true
			}
		}
	}
	if !hasDigest || !strings.Contains(digestTxt, "Дневная сводка") || !strings.Contains(digestTxt, "вкл") {
		t.Errorf("expected «☀️ Дневная сводка: вкл» toggle (menu:nodes:digest), got %q (found=%v)", digestTxt, hasDigest)
	}
	if !hasIcons || !strings.Contains(iconsTxt, "Иконки") || !strings.Contains(iconsTxt, "вкл") {
		t.Errorf("expected «✨ Иконки (анимация): вкл» toggle (menu:toggle_icons), got %q (found=%v)", iconsTxt, hasIcons)
	}
	if !hasCheckhostCfg {
		t.Errorf("expected the Check-Host settings entry to stay in settings")
	}
	for _, row := range on.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:checkhost_cfg" && strings.Contains(b.Text, "🌐") {
				t.Errorf("Check-Host settings label must not use 🌐, got %q", b.Text)
			}
		}
	}
	if !hasTimezone || !hasQuiet || !hasSubs {
		t.Errorf("existing settings entries (tz/quiet/subs) must stay, got tz=%v quiet=%v subs=%v", hasTimezone, hasQuiet, hasSubs)
	}

	off := SettingsMenuMarkup(BotConfig{DayDigestEnabled: false, CustomEmoji: false}, true)
	for _, row := range off.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:nodes:digest" && !strings.Contains(b.Text, "выкл") {
				t.Errorf("digest toggle must read «выкл» when off, got %q", b.Text)
			}
			if b.CallbackData == "menu:toggle_icons" && !strings.Contains(b.Text, "выкл") {
				t.Errorf("icons toggle must read «выкл» when off, got %q", b.Text)
			}
		}
	}

	// Subs visibility flag keeps working through the state-aware variant.
	if hasCallback(on, "menu:subs") != true {
		t.Fatalf("expected menu:subs present in SettingsMenuMarkup(cfg, true)")
	}
	if hasCallback(SettingsMenuMarkupWithCfg(BotConfig{}, false), "menu:subs") {
		t.Errorf("SettingsMenuMarkupWithCfg(cfg, false) must omit menu:subs")
	}
}

// TestSubsMenuMarkup_AddSubscriptionArm pins the subs screen markup (wave
// K-B3): «➕ Добавить подписку» arms the admin-gated input flow
// (menu:subsadd) and the back row is the unified pair.
func TestSubsMenuMarkup_AddSubscriptionArm(t *testing.T) {
	markup := SubsMenuMarkup()
	requireAllCallbacksRoutable(t, "SubsMenuMarkup", markup)
	if !hasButtonText(markup, "➕ Добавить подписку") || !hasCallback(markup, "menu:subsadd") {
		t.Fatalf("expected the «➕ Добавить подписку» arm button (menu:subsadd), got %v", markupCallbacks(markup))
	}
	last := markup.InlineKeyboard[len(markup.InlineKeyboard)-1]
	if last[0].CallbackData != "menu:settings" || last[0].Text != "↩️ К настройкам" {
		t.Errorf("expected the unified back row, got %v", last)
	}
}

// TestCheburResultMarkup_PreservesPage pins the Cheburcheck probe-result
// navigation (wave K-B3): «📡 К списку хостов» returns to the host list page
// the probe was launched from via menu:cheburpg:<page>; pages clamp >= 1.
func TestCheburResultMarkup_PreservesPage(t *testing.T) {
	mk := CheburResultMarkup(2)
	requireAllCallbacksRoutable(t, "CheburResultMarkup(2)", mk)
	if !hasCallback(mk, "menu:cheburpg:2") {
		t.Fatalf("expected menu:cheburpg:2 (page preserved), got %v", markupCallbacks(mk))
	}
	if !hasButtonText(mk, "📡 К списку хостов") {
		t.Errorf("expected the «📡 К списку хостов» button, got %v", markupCallbacks(mk))
	}
	if !hasCallback(mk, "menu:main") {
		t.Errorf("expected a main-menu button on the probe result, got %v", markupCallbacks(mk))
	}
	if hasCallback(CheburResultMarkup(0), "menu:cheburpg:0") {
		t.Errorf("page 0 must clamp to 1")
	}
	if !hasCallback(CheburResultMarkup(0), "menu:cheburpg:1") {
		t.Errorf("page 0 must clamp to menu:cheburpg:1, got %v", markupCallbacks(CheburResultMarkup(0)))
	}
}

// TestCheckHostMenuMarkup_AntennaRows pins the Check-Host de-🌐 sweep (wave
// K-B3): auto list rows and the «➕ Добавить хост» row carry the 📡 antenna
// icon (IconCheckHost) — 🌐 is freed for web/dashboard mentions only.
func TestCheckHostMenuMarkup_AntennaRows(t *testing.T) {
	items := []CheckHostListItem{
		{Address: "auto.example.com:443", Auto: true},
		{Address: "custom.example.com:443"},
	}
	mk := CheckHostMenuMarkup(items, 1, 1)
	requireAllCallbacksRoutable(t, "CheckHostMenuMarkup(antenna)", mk)
	foundAuto, foundAdd := false, false
	for _, row := range mk.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, "🌐") {
				t.Errorf("Check-Host list must not use 🌐, got %q", b.Text)
			}
			if strings.HasPrefix(b.CallbackData, "menu:checkhosth:auto") {
				foundAuto = strings.Contains(b.Text, "📡") && strings.Contains(b.Text, "· авто")
			}
			if b.CallbackData == "menu:checkhostadd" {
				foundAdd = strings.Contains(b.Text, "📡") && strings.Contains(b.Text, "Добавить хост")
			}
		}
	}
	if !foundAuto {
		t.Errorf("auto row must read «📡 <host> · авто», got %v", markupCallbacks(mk))
	}
	if !foundAdd {
		t.Errorf("the add-host row must carry the 📡 icon, got %v", markupCallbacks(mk))
	}
}

// TestMainMenuMarkup_CustomEmojiDualLayer pins the Bot API 9.4 dual layer on
// MainMenuMarkup (wave K-B3, Guardian P1 fix 2026-11-18): button text has NO
// parse_mode, so with the layer enabled the labels carry the PLAIN fallback
// emoji and the custom id goes to WithIconCustomEmojiID (Telegram draws it
// before the text); with the layer off the labels are the plain fallback
// emojis and no custom id is attached.
func TestMainMenuMarkup_CustomEmojiDualLayer(t *testing.T) {
	// B1's additive icons.go wiring (SetCustomEmojiEnabled / atomic
	// customEmojiEnabled flag) is the layer switch; nothing is added to
	// icons.go by this worker.
	SetCustomEmojiEnabled(true)
	t.Cleanup(func() { SetCustomEmojiEnabled(false) })

	mk := MainMenuMarkup()
	var checkhostBtn, diagBtn telego.InlineKeyboardButton
	for _, row := range mk.InlineKeyboard {
		for _, b := range row {
			switch b.CallbackData {
			case "menu:checkhost":
				checkhostBtn = b
			case "menu:diag":
				diagBtn = b
			}
		}
	}
	if checkhostBtn.IconCustomEmojiID != IconCheckHost.CustomID {
		t.Errorf("enabled layer must attach IconCustomEmojiID %q to the Check-Host button, got %q", IconCheckHost.CustomID, checkhostBtn.IconCustomEmojiID)
	}
	if !strings.HasPrefix(checkhostBtn.Text, IconCheckHost.Fallback) {
		t.Errorf("enabled Check-Host label must carry the PLAIN fallback %q (button text has no parse_mode), got %q", IconCheckHost.Fallback, checkhostBtn.Text)
	}
	if strings.Contains(checkhostBtn.Text, `<tg-emoji`) {
		t.Errorf("button labels must NEVER contain raw tg-emoji tags (Guardian P1), got %q", checkhostBtn.Text)
	}
	if strings.Contains(checkhostBtn.Text, "&lt;") {
		t.Errorf("button labels must never contain escaped HTML entities, got %q", checkhostBtn.Text)
	}
	if diagBtn.IconCustomEmojiID != IconDiagnostics.CustomID {
		t.Errorf("enabled layer must attach IconCustomEmojiID %q to the diag button, got %q", IconDiagnostics.CustomID, diagBtn.IconCustomEmojiID)
	}

	SetCustomEmojiEnabled(false)
	mkOff := MainMenuMarkup()
	for _, row := range mkOff.InlineKeyboard {
		for _, b := range row {
			if b.IconCustomEmojiID != "" {
				t.Errorf("disabled layer must not attach IconCustomEmojiID, got %q on %q", b.IconCustomEmojiID, b.Text)
			}
			if strings.Contains(b.Text, "tg-emoji") {
				t.Errorf("disabled layer must render plain fallback emojis, got %q", b.Text)
			}
		}
	}
	var checkhostOff telego.InlineKeyboardButton
	for _, row := range mkOff.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "menu:checkhost" {
				checkhostOff = b
			}
		}
	}
	if checkhostOff.Text != "📡 Проверка Check-Host.net" {
		t.Errorf("disabled layer Check-Host label must be the plain fallback «📡 Проверка Check-Host.net», got %q", checkhostOff.Text)
	}
}
