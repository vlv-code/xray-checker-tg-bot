package telegram

import (
	"testing"
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

	// 1. Single page pagination markup
	singlePageMarkup := DiagPaginationMarkup(1, 1)
	if len(singlePageMarkup.InlineKeyboard) != 2 {
		t.Errorf("expected 2 rows for single page markup (actions + back), got %d", len(singlePageMarkup.InlineKeyboard))
	}
	if singlePageMarkup.InlineKeyboard[0][0].CallbackData != "menu:diag:rich" {
		t.Errorf("expected rich report button, got %s", singlePageMarkup.InlineKeyboard[0][0].CallbackData)
	}

	// 2. Multi-page pagination markup
	multiPageMarkup := DiagPaginationMarkup(2, 4)
	if len(multiPageMarkup.InlineKeyboard) != 3 {
		t.Fatalf("expected 3 rows for multi-page markup, got %d", len(multiPageMarkup.InlineKeyboard))
	}
	navRow := multiPageMarkup.InlineKeyboard[0]
	if len(navRow) != 3 {
		t.Fatalf("expected 3 navigation buttons, got %d", len(navRow))
	}
	if navRow[0].CallbackData != "menu:diag:p:1" {
		t.Errorf("expected prev page 1, got %s", navRow[0].CallbackData)
	}
	if navRow[2].CallbackData != "menu:diag:p:3" {
		t.Errorf("expected next page 3, got %s", navRow[2].CallbackData)
	}

	// 3. Rich report markup
	richMarkup := RichReportMarkup()
	if len(richMarkup.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows in RichReportMarkup, got %d", len(richMarkup.InlineKeyboard))
	}
	if richMarkup.InlineKeyboard[0][0].CallbackData != "menu:diag:p:1" {
		t.Errorf("expected switch to page 1 button, got %s", richMarkup.InlineKeyboard[0][0].CallbackData)
	}
}

