package telegram

import (
	"fmt"
	"sync"
	"time"
)

// ActiveAlert represents an alert message sent for an offline proxy.
type ActiveAlert struct {
	ChatID    int64
	MessageID int
	StableID  string
	ProxyName string
	DownAt    time.Time
	Reason    string
}

// AlertTracker manages active outage alert messages in memory.
type AlertTracker struct {
	mu     sync.Mutex
	alerts map[string]*ActiveAlert // key: "chatID:stableID"
}

// NewAlertTracker creates a new AlertTracker.
func NewAlertTracker() *AlertTracker {
	return &AlertTracker{
		alerts: make(map[string]*ActiveAlert),
	}
}

func alertKey(chatID int64, stableID string) string {
	return fmt.Sprintf("%d:%s", chatID, stableID)
}

// Track stores an active outage alert message.
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
}

// Resolve removes and returns the active alert for a specific chat and proxy.
func (at *AlertTracker) Resolve(chatID int64, stableID string) (*ActiveAlert, bool) {
	at.mu.Lock()
	defer at.mu.Unlock()

	key := alertKey(chatID, stableID)
	alert, found := at.alerts[key]
	if found {
		delete(at.alerts, key)
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
	return fmt.Sprintf("%dч %dм", hours, minutes)
}
