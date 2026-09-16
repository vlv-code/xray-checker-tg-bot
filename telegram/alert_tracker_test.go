package telegram

import (
	"os"
	"testing"
	"time"
)

func TestAlertTracker_TrackAndResolve(t *testing.T) {
	tracker := NewAlertTracker()

	downTime := time.Now().Add(-5 * time.Minute)
	tracker.Track(12345, 0, 101, "vless-pl-1", "PL-main", downTime, "EOF")
	tracker.Track(67890, 0, 102, "vless-pl-1", "PL-main", downTime, "EOF")

	alerts := tracker.GetAlertsForProxy("vless-pl-1")
	if len(alerts) != 2 {
		t.Fatalf("expected 2 active alerts, got %d", len(alerts))
	}

	// Verify alert details
	if alerts[0].MessageID != 101 && alerts[0].MessageID != 102 {
		t.Errorf("unexpected message ID: %d", alerts[0].MessageID)
	}

	// Resolve for chat 12345
	alert, found := tracker.Resolve(12345, 0, "vless-pl-1")
	if !found || alert.MessageID != 101 {
		t.Fatalf("expected to resolve alert 101, got %v, found=%v", alert, found)
	}

	// Now only 1 alert remaining for vless-pl-1
	remaining := tracker.GetAlertsForProxy("vless-pl-1")
	if len(remaining) != 1 || remaining[0].ChatID != 67890 {
		t.Fatalf("expected 1 remaining alert for chat 67890, got %v", remaining)
	}

	// Resolve remaining
	tracker.Resolve(67890, 0, "vless-pl-1")
	if len(tracker.GetAlertsForProxy("vless-pl-1")) != 0 {
		t.Fatalf("expected 0 remaining alerts")
	}
}

func TestAlertTracker_PersistenceAndAntiFlapping(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/alerts.json"

	tracker := NewAlertTracker(path)
	if tracker.HasAlert(111, 0, "proxy-1") {
		t.Errorf("expected no alert initially")
	}

	down := time.Now()
	tracker.Track(111, 0, 201, "proxy-1", "Proxy 1", down, "Offline")
	if !tracker.HasAlert(111, 0, "proxy-1") {
		t.Errorf("expected alert to be tracked")
	}

	// Reload from disk
	tracker2 := NewAlertTracker(path)
	if !tracker2.HasAlert(111, 0, "proxy-1") {
		t.Fatalf("expected reloaded tracker to have alert")
	}

	alert, ok := tracker2.Resolve(111, 0, "proxy-1")
	if !ok || alert.MessageID != 201 {
		t.Fatalf("expected to resolve alert 201, got %v", alert)
	}

	// Verify persistence after resolve
	tracker3 := NewAlertTracker(path)
	if tracker3.HasAlert(111, 0, "proxy-1") {
		t.Errorf("expected alert to be resolved in persisted state")
	}
}

func TestAlertTracker_TopicsAreTrackedSeparately(t *testing.T) {
	tracker := NewAlertTracker()

	down := time.Now()
	// Same group, two different forum topics (and no-topic variant).
	tracker.Track(-100123, 0, 201, "proxy-1", "Proxy 1", down, "Offline")
	tracker.Track(-100123, 42, 202, "proxy-1", "Proxy 1", down, "Offline")

	if !tracker.HasAlert(-100123, 0, "proxy-1") {
		t.Errorf("expected General-topic alert")
	}
	if !tracker.HasAlert(-100123, 42, "proxy-1") {
		t.Errorf("expected topic-42 alert")
	}
	if tracker.HasAlert(-100123, 7, "proxy-1") {
		t.Errorf("topic 7 must not have an alert")
	}

	if len(tracker.GetAlertsForProxy("proxy-1")) != 2 {
		t.Fatalf("expected 2 alerts across topics")
	}

	// Resolving one topic must leave the other active.
	alert, ok := tracker.Resolve(-100123, 42, "proxy-1")
	if !ok || alert.MessageID != 202 {
		t.Fatalf("expected to resolve topic-42 alert, got %v ok=%v", alert, ok)
	}
	if !tracker.HasAlert(-100123, 0, "proxy-1") {
		t.Errorf("General-topic alert must remain")
	}
}

func TestAlertTracker_LegacyStoreMigratesToThreadAwareKeys(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/alerts.json"

	// Store written by a pre-topics version: flat "chatID:stableID" keys.
	legacy := `{
  "111:proxy-1": {
    "chat_id": 111,
    "message_id": 201,
    "stable_id": "proxy-1",
    "proxy_name": "Proxy 1",
    "down_at": "2026-01-01T00:00:00Z",
    "reason": "Offline"
  }
}`
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	tracker := NewAlertTracker(path)
	if !tracker.HasAlert(111, 0, "proxy-1") {
		t.Fatalf("legacy alert must be visible under thread 0 after migration")
	}
	alert, ok := tracker.Resolve(111, 0, "proxy-1")
	if !ok || alert.MessageID != 201 {
		t.Fatalf("expected to resolve legacy alert 201, got %v ok=%v", alert, ok)
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
		{5*24*time.Hour + 2*time.Hour, "5д 2ч"},
	}

	for _, c := range cases {
		got := FormatDowntime(c.d)
		if got != c.expected {
			t.Errorf("for %v expected %s, got %s", c.d, c.expected, got)
		}
	}
}
