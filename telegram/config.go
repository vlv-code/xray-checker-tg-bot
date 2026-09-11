package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	AlertModeLive  = "live"  // Edit outage message to show recovery duration
	AlertModeClean = "clean" // Delete outage message upon recovery, delete recovery message shortly after
)

// BotConfig holds runtime-configurable settings that can be toggled via Telegram
// and are persisted across restarts.
type BotConfig struct {
	QuietHoursEnabled      bool     `json:"quiet_hours_enabled"`
	QuietHoursStart        string   `json:"quiet_hours_start"` // e.g. "23:00"
	QuietHoursEnd          string   `json:"quiet_hours_end"`   // e.g. "08:00"
	QuietSnoozeUntil       int64    `json:"quiet_snooze_until"` // Unix timestamp
	DayDigestEnabled       bool     `json:"day_digest_enabled"`
	DayDigestIntervalHours int      `json:"day_digest_interval_hours"` // e.g. 6
	AlertMode              string   `json:"alert_mode"` // AlertModeLive or AlertModeClean
	TargetURLs             []string `json:"target_urls"`
}

// ConfigManager handles thread-safe access and persistence for BotConfig.
type ConfigManager struct {
	mu   sync.RWMutex
	path string
	cfg  BotConfig
}

// NewConfigManager initializes or loads BotConfig from path. If file does not exist,
// defaults are written.
func NewConfigManager(path string, defaults BotConfig) (*ConfigManager, error) {
	cm := &ConfigManager{
		path: path,
		cfg:  defaults,
	}

	if err := cm.load(); err != nil {
		if os.IsNotExist(err) {
			if err := cm.saveLocked(); err != nil {
				return nil, fmt.Errorf("failed to save initial bot config: %w", err)
			}
			return cm, nil
		}
		return nil, fmt.Errorf("failed to load bot config: %w", err)
	}

	return cm, nil
}

func (cm *ConfigManager) load() error {
	data, err := os.ReadFile(cm.path)
	if err != nil {
		return err
	}

	var loaded BotConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}

	if loaded.AlertMode == "" {
		loaded.AlertMode = AlertModeLive
	}
	if loaded.QuietHoursStart == "" {
		loaded.QuietHoursStart = "23:00"
	}
	if loaded.QuietHoursEnd == "" {
		loaded.QuietHoursEnd = "08:00"
	}
	if loaded.DayDigestIntervalHours <= 0 {
		loaded.DayDigestIntervalHours = 6
	}

	cm.cfg = loaded
	return nil
}

// Get returns a copy of the current configuration.
func (cm *ConfigManager) Get() BotConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	c := cm.cfg
	if len(cm.cfg.TargetURLs) > 0 {
		c.TargetURLs = make([]string, len(cm.cfg.TargetURLs))
		copy(c.TargetURLs, cm.cfg.TargetURLs)
	}
	return c
}

// Update modifies the configuration atomically and persists to disk.
func (cm *ConfigManager) Update(fn func(*BotConfig)) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	fn(&cm.cfg)
	return cm.saveLocked()
}

func (cm *ConfigManager) saveLocked() error {
	if cm.path == "" {
		return nil
	}

	dir := filepath.Dir(cm.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(cm.cfg, "", "  ")
	if err != nil {
		return err
	}

	tmp := cm.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, cm.path)
}
