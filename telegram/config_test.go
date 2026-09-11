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
		c.CheckIntervalSec = 60
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
	if cfg2.CheckIntervalSec != 60 {
		t.Errorf("expected CheckIntervalSec 60, got %d", cfg2.CheckIntervalSec)
	}

	// Verify file actually exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("expected config file to exist at %s", configPath)
	}
}

func TestConfigManager_ToggleProxy(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "bot_config.json")

	cm, err := NewConfigManager(configPath, BotConfig{})
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	stableID := "abc123stableid"

	// Initial toggle -> should become disabled (true)
	disabled, err := cm.ToggleProxy(stableID)
	if err != nil {
		t.Fatalf("ToggleProxy failed: %v", err)
	}
	if !disabled {
		t.Errorf("expected disabled to be true on first toggle")
	}
	if !cm.Get().IsProxyDisabled(stableID) {
		t.Errorf("expected IsProxyDisabled to be true")
	}

	// Second toggle -> should become enabled (false)
	disabled, err = cm.ToggleProxy(stableID)
	if err != nil {
		t.Fatalf("ToggleProxy second failed: %v", err)
	}
	if disabled {
		t.Errorf("expected disabled to be false on second toggle")
	}
	if cm.Get().IsProxyDisabled(stableID) {
		t.Errorf("expected IsProxyDisabled to be false")
	}
}

