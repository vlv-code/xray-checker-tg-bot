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
}
