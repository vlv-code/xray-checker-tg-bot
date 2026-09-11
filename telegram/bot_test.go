package telegram

import (
	"path/filepath"
	"strings"
	"testing"

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

