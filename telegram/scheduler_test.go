package telegram

import (
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
