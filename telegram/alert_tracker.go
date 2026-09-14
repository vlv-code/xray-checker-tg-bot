package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ActiveAlert represents an alert message sent for an offline proxy.
type ActiveAlert struct {
	ChatID    int64     `json:"chat_id"`
	MessageID int       `json:"message_id"`
	StableID  string    `json:"stable_id"`
	ProxyName string    `json:"proxy_name"`
	DownAt    time.Time `json:"down_at"`
	Reason    string    `json:"reason"`
}

// AlertTracker manages active outage alert messages with optional disk persistence.
type AlertTracker struct {
	mu     sync.Mutex
	path   string
	alerts map[string]*ActiveAlert // key: "chatID:stableID"
}

// NewAlertTracker creates a new AlertTracker, optionally with disk persistence.
func NewAlertTracker(paths ...string) *AlertTracker {
	at := &AlertTracker{
		alerts: make(map[string]*ActiveAlert),
	}
	if len(paths) > 0 && paths[0] != "" {
		at.path = paths[0]
		_ = at.load()
	}
	return at
}

func (at *AlertTracker) load() error {
	if at.path == "" {
		return nil
	}
	data, err := os.ReadFile(at.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var loaded map[string]*ActiveAlert
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	if loaded != nil {
		at.alerts = loaded
	}
	return nil
}

func (at *AlertTracker) saveLocked() error {
	if at.path == "" {
		return nil
	}
	dir := filepath.Dir(at.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(at.alerts, "", "  ")
	if err != nil {
		return err
	}
	tmp := at.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, at.path)
}

func alertKey(chatID int64, stableID string) string {
	return fmt.Sprintf("%d:%s", chatID, stableID)
}

// HasAlert returns true if there is an active alert for the given chat and proxy.
func (at *AlertTracker) HasAlert(chatID int64, stableID string) bool {
	at.mu.Lock()
	defer at.mu.Unlock()

	_, found := at.alerts[alertKey(chatID, stableID)]
	return found
}

// Track stores an active outage alert message and persists if path is configured.
func (at *AlertTracker) Track(chatID int64, messageID int, stableID, proxyName string, downAt time.Time, reason string) {
	at.mu.Lock()
	defer at.mu.Unlock()

	key := alertKey(chatID, stableID)
	at.alerts[key] = &ActiveAlert{
		ChatID:    chatID,
		MessageID: messageID,
		StableID:  stableID,
		ProxyName: proxyName,
		DownAt:    downAt,
		Reason:    reason,
	}
	_ = at.saveLocked()
}

// Resolve removes, persists, and returns the active alert for a specific chat and proxy.
func (at *AlertTracker) Resolve(chatID int64, stableID string) (*ActiveAlert, bool) {
	at.mu.Lock()
	defer at.mu.Unlock()

	key := alertKey(chatID, stableID)
	alert, found := at.alerts[key]
	if found {
		delete(at.alerts, key)
		_ = at.saveLocked()
	}
	return alert, found
}

// GetAlertsForProxy returns all active alerts for a given proxy across all chats.
func (at *AlertTracker) GetAlertsForProxy(stableID string) []*ActiveAlert {
	at.mu.Lock()
	defer at.mu.Unlock()

	var list []*ActiveAlert
	for _, a := range at.alerts {
		if a.StableID == stableID {
			list = append(list, a)
		}
	}
	return list
}

// GetDowntime returns the duration since the earliest active alert for stableID was recorded.
func (at *AlertTracker) GetDowntime(stableID string, now time.Time) time.Duration {
	if at == nil {
		return 0
	}
	at.mu.Lock()
	defer at.mu.Unlock()

	var earliest time.Time
	for _, a := range at.alerts {
		if a.StableID == stableID {
			if earliest.IsZero() || a.DownAt.Before(earliest) {
				earliest = a.DownAt
			}
		}
	}
	if earliest.IsZero() {
		return 0
	}
	d := now.Sub(earliest)
	if d < 0 {
		return 0
	}
	return d
}

// FormatDowntime formats a duration in human-readable Russian shorthand.
func FormatDowntime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int64(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%dс", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dм %dс", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	if hours < 24 {
		return fmt.Sprintf("%dч %dм", hours, minutes)
	}
	days := hours / 24
	hours = hours % 24
	return fmt.Sprintf("%dд %dч", days, hours)
}
