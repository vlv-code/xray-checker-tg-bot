package telegram

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"
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
	if len(singlePageMarkup.InlineKeyboard) != 2 {
		t.Errorf("expected 2 rows for single page markup (actions + back), got %d", len(singlePageMarkup.InlineKeyboard))
	}
	if singlePageMarkup.InlineKeyboard[0][0].CallbackData != "menu:diag" {
		t.Errorf("expected back to summary button menu:diag, got %s", singlePageMarkup.InlineKeyboard[0][0].CallbackData)
	}
	if singlePageMarkup.InlineKeyboard[0][0].Text != "🔙 К подробной сводке" {
		t.Errorf("expected button text '🔙 К подробной сводке', got '%s'", singlePageMarkup.InlineKeyboard[0][0].Text)
	}

	// 2. Multi-page pagination markup (Details view)
	multiPageMarkup := DiagPaginationMarkup(2, 4)
	if len(multiPageMarkup.InlineKeyboard) != 3 {
		t.Fatalf("expected 3 rows for multi-page markup, got %d", len(multiPageMarkup.InlineKeyboard))
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
	actionRow := multiPageMarkup.InlineKeyboard[1]
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
