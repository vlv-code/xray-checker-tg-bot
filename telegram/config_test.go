package telegram

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigManager_DefaultsAndPersist(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "bot_config.json")

	defaults := BotConfig{
		QuietHoursEnabled:      true,
		QuietHoursStart:        "23:00",
		QuietHoursEnd:          "08:00",
		DayDigestEnabled:       true,
		DayDigestIntervalHours: 6,
		AlertMode:              AlertModeLive,
		TargetURLs:             []string{"https://cp.cloudflare.com/generate_204"},
	}

	cm, err := NewConfigManager(configPath, defaults)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	cfg := cm.Get()
	if cfg.AlertMode != AlertModeLive {
		t.Errorf("expected alert mode %s, got %s", AlertModeLive, cfg.AlertMode)
	}
	if len(cfg.TargetURLs) != 1 {
		t.Errorf("expected 1 target URL, got %d", len(cfg.TargetURLs))
	}

	// Update configuration
	snoozeUntil := time.Now().Add(time.Hour).Unix()
	err = cm.Update(func(c *BotConfig) {
		c.AlertMode = AlertModeClean
		c.QuietSnoozeUntil = snoozeUntil
		c.TargetURLs = append(c.TargetURLs, "https://www.gstatic.com/generate_204")
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Reload from disk
	cm2, err := NewConfigManager(configPath, defaults)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	cfg2 := cm2.Get()
	if cfg2.AlertMode != AlertModeClean {
		t.Errorf("expected alert mode %s after reload, got %s", AlertModeClean, cfg2.AlertMode)
	}
	if cfg2.QuietSnoozeUntil != snoozeUntil {
		t.Errorf("expected snoozeUntil %d, got %d", snoozeUntil, cfg2.QuietSnoozeUntil)
	}
	if len(cfg2.TargetURLs) != 2 {
		t.Errorf("expected 2 target URLs after reload, got %d", len(cfg2.TargetURLs))
	}

	// Verify file actually exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("expected config file to exist at %s", configPath)
	}
}
