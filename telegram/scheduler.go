package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BufferedEvent captures state transitions that occur during quiet hours.
type BufferedEvent struct {
	Timestamp time.Time
	Type      string // "down" or "up"
	ProxyName string
	Reason    string
	LatencyMs float64
	Downtime  time.Duration
}

// EventBuffer buffers events for digests.
type EventBuffer struct {
	mu     sync.Mutex
	events []BufferedEvent
}

// NewEventBuffer creates an empty EventBuffer.
func NewEventBuffer() *EventBuffer {
	return &EventBuffer{
		events: make([]BufferedEvent, 0),
	}
}

// Add appends an event to the buffer.
func (eb *EventBuffer) Add(e BufferedEvent) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.events = append(eb.events, e)
}

// Drain retrieves all buffered events and empties the buffer.
func (eb *EventBuffer) Drain() []BufferedEvent {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	out := eb.events
	eb.events = make([]BufferedEvent, 0)
	return out
}

// IsQuietTime determines if the given time falls within configured quiet hours
// or an active snooze.
func IsQuietTime(now time.Time, cfg BotConfig) bool {
	if cfg.QuietSnoozeUntil > now.Unix() {
		return true
	}
	if !cfg.QuietHoursEnabled {
		return false
	}

	startHour, startMin, err1 := parseTimeOfDay(cfg.QuietHoursStart)
	endHour, endMin, err2 := parseTimeOfDay(cfg.QuietHoursEnd)
	if err1 != nil || err2 != nil {
		return false
	}

	nowMinutes := now.Hour()*60 + now.Minute()
	startMinutes := startHour*60 + startMin
	endMinutes := endHour*60 + endMin

	if startMinutes < endMinutes {
		// e.g. 13:00 to 15:00
		return nowMinutes >= startMinutes && nowMinutes < endMinutes
	}

	// e.g. 23:00 to 08:00 (spans midnight)
	return nowMinutes >= startMinutes || nowMinutes < endMinutes
}

func parseTimeOfDay(s string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid time format, expected HH:MM")
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("invalid hour: %s", parts[0])
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("invalid minute: %s", parts[1])
	}
	return h, m, nil
}
