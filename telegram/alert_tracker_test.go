package telegram

import (
	"testing"
	"time"
)

func TestAlertTracker_TrackAndResolve(t *testing.T) {
	tracker := NewAlertTracker()

	downTime := time.Now().Add(-5 * time.Minute)
	tracker.Track(12345, 101, "vless-pl-1", "PL-main", downTime, "EOF")
	tracker.Track(67890, 102, "vless-pl-1", "PL-main", downTime, "EOF")

	alerts := tracker.GetAlertsForProxy("vless-pl-1")
	if len(alerts) != 2 {
		t.Fatalf("expected 2 active alerts, got %d", len(alerts))
	}

	// Verify alert details
	if alerts[0].MessageID != 101 && alerts[0].MessageID != 102 {
		t.Errorf("unexpected message ID: %d", alerts[0].MessageID)
	}

	// Resolve for chat 12345
	alert, found := tracker.Resolve(12345, "vless-pl-1")
	if !found || alert.MessageID != 101 {
		t.Fatalf("expected to resolve alert 101, got %v, found=%v", alert, found)
	}

	// Now only 1 alert remaining for vless-pl-1
	remaining := tracker.GetAlertsForProxy("vless-pl-1")
	if len(remaining) != 1 || remaining[0].ChatID != 67890 {
		t.Fatalf("expected 1 remaining alert for chat 67890, got %v", remaining)
	}

	// Resolve remaining
	tracker.Resolve(67890, "vless-pl-1")
	if len(tracker.GetAlertsForProxy("vless-pl-1")) != 0 {
		t.Fatalf("expected 0 remaining alerts")
	}
}

func TestFormatDowntime(t *testing.T) {
	cases := []struct {
		d        time.Duration
		expected string
	}{
		{45 * time.Second, "45с"},
		{5 * time.Minute, "5м 0с"},
		{15*time.Minute + 30*time.Second, "15м 30с"},
		{2*time.Hour + 10*time.Minute, "2ч 10м"},
	}

	for _, c := range cases {
		got := FormatDowntime(c.d)
		if got != c.expected {
			t.Errorf("for %v expected %s, got %s", c.d, c.expected, got)
		}
	}
}
