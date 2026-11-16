package telegram

import (
	"fmt"
	"testing"
	"time"
)

func TestIsQuietTime(t *testing.T) {
	// Spanning midnight: 23:00 to 08:00
	cfg := BotConfig{
		QuietHoursEnabled: true,
		QuietHoursStart:   "23:00",
		QuietHoursEnd:     "08:00",
	}

	// 1. 23:30 should be quiet
	t1 := time.Date(2026, 9, 11, 23, 30, 0, 0, time.UTC)
	if !IsQuietTime(t1, cfg) {
		t.Errorf("expected 23:30 to be quiet")
	}

	// 2. 03:15 should be quiet
	t2 := time.Date(2026, 9, 12, 3, 15, 0, 0, time.UTC)
	if !IsQuietTime(t2, cfg) {
		t.Errorf("expected 03:15 to be quiet")
	}

	// 3. 07:59 should be quiet
	t3 := time.Date(2026, 9, 12, 7, 59, 0, 0, time.UTC)
	if !IsQuietTime(t3, cfg) {
		t.Errorf("expected 07:59 to be quiet")
	}

	// 4. 08:00 should NOT be quiet
	t4 := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	if IsQuietTime(t4, cfg) {
		t.Errorf("expected 08:00 NOT to be quiet")
	}

	// 5. 14:00 should NOT be quiet
	t5 := time.Date(2026, 9, 12, 14, 0, 0, 0, time.UTC)
	if IsQuietTime(t5, cfg) {
		t.Errorf("expected 14:00 NOT to be quiet")
	}

	// 6. Snooze override
	cfgDisabled := BotConfig{
		QuietHoursEnabled: false,
		QuietSnoozeUntil:  time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC).Unix(),
	}
	if !IsQuietTime(t5, cfgDisabled) {
		t.Errorf("expected 14:00 to be quiet due to snooze")
	}

	// 7. Timezone conversion: UTC 18:30 is 23:30 in Asia/Yekaterinburg (UTC+5)
	cfgTz := BotConfig{
		QuietHoursEnabled: true,
		QuietHoursStart:   "23:00",
		QuietHoursEnd:     "08:00",
		Timezone:          "Asia/Yekaterinburg",
	}
	tUtc := time.Date(2026, 9, 11, 18, 30, 0, 0, time.UTC)
	if !IsQuietTime(tUtc, cfgTz) {
		t.Errorf("expected 18:30 UTC to be quiet in Asia/Yekaterinburg (23:30 YEKT)")
	}
}

func TestEventBuffer(t *testing.T) {
	buf := NewEventBuffer()

	buf.Add(BufferedEvent{
		Timestamp: time.Now(),
		Type:      "down",
		ProxyName: "Proxy A",
		Reason:    "Timeout",
	})
	buf.Add(BufferedEvent{
		Timestamp: time.Now(),
		Type:      "up",
		ProxyName: "Proxy A",
		Downtime:  10 * time.Minute,
	})

	events := buf.Drain()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Should be empty after drain
	if len(buf.Drain()) != 0 {
		t.Fatalf("expected buffer to be empty after drain")
	}
}

// TestEventBuffer_CapDropsOldest (FIX: unbounded EventBuffer): Add beyond the
// hard cap must keep exactly maxBufferedEvents events, dropping the OLDEST
// ones, and count every dropped event.
func TestEventBuffer_CapDropsOldest(t *testing.T) {
	buf := NewEventBuffer()

	total := maxBufferedEvents + 50
	for i := 0; i < total; i++ {
		buf.Add(BufferedEvent{Type: "down", ProxyName: fmt.Sprintf("p%d", i)})
	}

	events := buf.Drain()
	if len(events) != maxBufferedEvents {
		t.Fatalf("buffer must be capped at %d events, got %d", maxBufferedEvents, len(events))
	}
	if events[0].ProxyName != "p50" {
		t.Fatalf("oldest events must be dropped first, expected first kept event p50, got %s", events[0].ProxyName)
	}
	if last := events[len(events)-1].ProxyName; last != fmt.Sprintf("p%d", total-1) {
		t.Fatalf("newest event must be kept, expected p%d, got %s", total-1, last)
	}
}

// TestEventBuffer_DrainResetsDroppedCounter (FIX: unbounded EventBuffer):
// DroppedCount reports the overflow since the last drain and Drain resets it.
func TestEventBuffer_DrainResetsDroppedCounter(t *testing.T) {
	buf := NewEventBuffer()

	if got := buf.DroppedCount(); got != 0 {
		t.Fatalf("fresh buffer must report 0 dropped events, got %d", got)
	}

	for i := 0; i < maxBufferedEvents+10; i++ {
		buf.Add(BufferedEvent{Type: "down", ProxyName: fmt.Sprintf("p%d", i)})
	}
	if got := buf.DroppedCount(); got != 10 {
		t.Fatalf("expected 10 dropped events, got %d", got)
	}

	buf.Drain()
	if got := buf.DroppedCount(); got != 0 {
		t.Fatalf("Drain must reset the dropped counter, got %d", got)
	}
}

// TestEventBuffer_NoDropUnderCap (FIX: unbounded EventBuffer): below the cap
// nothing is dropped and ordering is preserved.
func TestEventBuffer_NoDropUnderCap(t *testing.T) {
	buf := NewEventBuffer()

	for i := 0; i < 5; i++ {
		buf.Add(BufferedEvent{Type: "up", ProxyName: fmt.Sprintf("p%d", i)})
	}

	events := buf.Drain()
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}
	for i, e := range events {
		if e.ProxyName != fmt.Sprintf("p%d", i) {
			t.Fatalf("event order must be preserved, position %d holds %s", i, e.ProxyName)
		}
	}
	if got := buf.DroppedCount(); got != 0 {
		t.Fatalf("under-cap Adds must not drop events, got %d dropped", got)
	}
}
