package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

const (
	AlertModeLive  = "live"  // Edit outage message to show recovery duration
	AlertModeClean = "clean" // Delete outage message upon recovery, delete recovery message shortly after
)

// BotConfig holds runtime-configurable settings that can be toggled via Telegram
// and are persisted across restarts.
type BotConfig struct {
	QuietHoursEnabled      bool     `json:"quiet_hours_enabled"`
	QuietHoursStart        string   `json:"quiet_hours_start"`  // e.g. "23:00"
	QuietHoursEnd          string   `json:"quiet_hours_end"`    // e.g. "08:00"
	QuietSnoozeUntil       int64    `json:"quiet_snooze_until"` // Unix timestamp
	DayDigestEnabled       bool     `json:"day_digest_enabled"`
	DayDigestIntervalHours int      `json:"day_digest_interval_hours"` // e.g. 6
	AlertMode              string   `json:"alert_mode"`                // AlertModeLive or AlertModeClean
	TargetURLs             []string `json:"target_urls"`
	CheckIntervalSec       int      `json:"check_interval_sec,omitempty"`
	RichMode               bool     `json:"rich_mode"`
	DisabledHosts          []string `json:"disabled_hosts,omitempty"`
	DisabledProxies        []string `json:"disabled_proxies,omitempty"`
	CheckHostBgEnabled     bool     `json:"checkhost_bg_enabled"`
	CheckHostIntervalHours int      `json:"checkhost_interval_hours"`
	CheckHostAlertEnabled  bool     `json:"checkhost_alert_enabled"`
	Timezone               string   `json:"timezone,omitempty"`
}

// Location returns the parsed *time.Location for the configured Timezone,
// or time.Local if unset or unrecognized.
func (c BotConfig) Location() *time.Location {
	if c.Timezone != "" && strings.ToLower(c.Timezone) != "local" {
		if loc, err := time.LoadLocation(c.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// IsHostDisabled checks if a server address/hostname is in the disabled list.
func (c BotConfig) IsHostDisabled(server string) bool {
	server = strings.ToLower(strings.TrimSpace(server))
	for _, h := range c.DisabledHosts {
		if strings.ToLower(strings.TrimSpace(h)) == server {
			return true
		}
	}
	return false
}

// IsProxyDisabled checks if a stable ID is in the disabled list.
func (c BotConfig) IsProxyDisabled(stableID string) bool {
	for _, id := range c.DisabledProxies {
		if id == stableID {
			return true
		}
	}
	return false
}

// IsDisabled checks if either the host or the proxy ID is disabled.
func (c BotConfig) IsDisabled(server, stableID string) bool {
	return c.IsHostDisabled(server) || c.IsProxyDisabled(stableID)
}

// ConfigManager handles thread-safe access and persistence for BotConfig.
type ConfigManager struct {
	mu   sync.RWMutex
	path string
	cfg  BotConfig
}

// NewConfigManager initializes a ConfigManager with a path and default values.
func NewConfigManager(path string, defaultCfg BotConfig) (*ConfigManager, error) {
	if defaultCfg.AlertMode == "" {
		defaultCfg.AlertMode = AlertModeClean
	}
	if defaultCfg.CheckHostIntervalHours <= 0 {
		defaultCfg.CheckHostIntervalHours = 1
	}

	cm := &ConfigManager{
		path: path,
		cfg:  defaultCfg,
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

	loaded := cm.cfg
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}

	if loaded.AlertMode == "" {
		loaded.AlertMode = AlertModeClean
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
	if loaded.CheckHostIntervalHours <= 0 {
		loaded.CheckHostIntervalHours = 1
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
	if len(cm.cfg.DisabledHosts) > 0 {
		c.DisabledHosts = make([]string, len(cm.cfg.DisabledHosts))
		copy(c.DisabledHosts, cm.cfg.DisabledHosts)
	}
	if len(cm.cfg.DisabledProxies) > 0 {
		c.DisabledProxies = make([]string, len(cm.cfg.DisabledProxies))
		copy(c.DisabledProxies, cm.cfg.DisabledProxies)
	}
	return c
}

// ToggleHost toggles the disabled status of a host and persists the change.
// Returns (newState, error) where newState is true if the host is now disabled.
func (cm *ConfigManager) ToggleHost(server string) (bool, error) {
	server = strings.ToLower(strings.TrimSpace(server))
	var newState bool
	err := cm.Update(func(cfg *BotConfig) {
		found := false
		var updated []string
		for _, h := range cfg.DisabledHosts {
			if strings.ToLower(strings.TrimSpace(h)) == server {
				found = true
			} else {
				updated = append(updated, h)
			}
		}
		if !found {
			updated = append(updated, server)
			newState = true // now disabled
		} else {
			newState = false // now enabled
		}
		cfg.DisabledHosts = updated
	})
	return newState, err
}

// ToggleProxy toggles the disabled status of a specific proxy by its StableID and persists the change.
// Returns (newState, error) where newState is true if the proxy is now disabled.
func (cm *ConfigManager) ToggleProxy(stableID string) (bool, error) {
	stableID = strings.TrimSpace(stableID)
	var newState bool
	err := cm.Update(func(cfg *BotConfig) {
		found := false
		var updated []string
		for _, id := range cfg.DisabledProxies {
			if id == stableID {
				found = true
			} else {
				updated = append(updated, id)
			}
		}
		if !found {
			updated = append(updated, stableID)
			newState = true // now disabled
		} else {
			newState = false // now enabled
		}
		cfg.DisabledProxies = updated
	})
	return newState, err
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
