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
	if mainMarkup.InlineKeyboard[1][0].Text != "🔎 Подробнее" {
		t.Errorf("expected button text '🔎 Подробнее', got '%s'", mainMarkup.InlineKeyboard[1][0].Text)
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
	if singlePageMarkup.InlineKeyboard[1][0].Text != "🔙 К подробной сводке" {
		t.Errorf("expected button text '🔙 К подробной сводке', got '%s'", singlePageMarkup.InlineKeyboard[1][0].Text)
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
	if backBtn.Text != "🔙 К детальному отчёту" {
		t.Errorf("expected button text '🔙 К детальному отчёту', got '%s'", backBtn.Text)
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

	// 3. NodesSettingsMarkup
	cfg := BotConfig{
		NodeSyncEnabled:     true,
		NodeAlertsEnabled:   true,
		NodeProxyAlertsChat: false,
	}
	setMenu := NodesSettingsMarkup(cfg)
	if len(setMenu.InlineKeyboard) < 4 {
		t.Fatalf("expected at least 4 rows in NodesSettingsMarkup, got %d", len(setMenu.InlineKeyboard))
	}
	if setMenu.InlineKeyboard[3][0].CallbackData != "menu:nodes:subs" {
		t.Errorf("expected menu:nodes:subs callback, got %s", setMenu.InlineKeyboard[3][0].CallbackData)
	}

	// 4. NodesSubsListMarkup
	subItems := []NodeSubListItem{
		{Name: "m31a", SubCount: 2},
		{Name: "msk-1", SubCount: 0},
	}
	listMarkup := NodesSubsListMarkup(subItems)
	if len(listMarkup.InlineKeyboard) < 3 {
		t.Fatalf("expected at least 3 rows in NodesSubsListMarkup, got %d", len(listMarkup.InlineKeyboard))
	}
	if listMarkup.InlineKeyboard[0][0].CallbackData != "menu:nodes:subnode:m31a" {
		t.Errorf("expected menu:nodes:subnode:m31a, got %s", listMarkup.InlineKeyboard[0][0].CallbackData)
	}
	if !strings.Contains(listMarkup.InlineKeyboard[0][0].Text, "m31a (2 подп.)") {
		t.Errorf("expected 'm31a (2 подп.)' in button text, got %s", listMarkup.InlineKeyboard[0][0].Text)
	}
	if listMarkup.InlineKeyboard[1][0].CallbackData != "menu:nodes:subnode:msk-1" {
		t.Errorf("expected menu:nodes:subnode:msk-1, got %s", listMarkup.InlineKeyboard[1][0].CallbackData)
	}
	// Back button should lead to node settings
	lastRow := listMarkup.InlineKeyboard[len(listMarkup.InlineKeyboard)-1]
	if lastRow[0].CallbackData != "menu:nodes:settings" {
		t.Errorf("expected back button to menu:nodes:settings, got %s", lastRow[0].CallbackData)
	}

	// 5. NodeSubsManageMarkup
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
	// Back buttons
	backRow := manageMarkup.InlineKeyboard[len(manageMarkup.InlineKeyboard)-1]
	if backRow[0].CallbackData != "menu:nodes:subs" {
		t.Errorf("expected back to menu:nodes:subs, got %s", backRow[0].CallbackData)
	}
}

