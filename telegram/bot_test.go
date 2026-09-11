package telegram

import (
	"path/filepath"
	"testing"
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
