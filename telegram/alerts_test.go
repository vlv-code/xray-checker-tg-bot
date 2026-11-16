package telegram

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/metrics"
)

// newAlertsTestBot builds a Bot wired to the fake Telegram API for
// ProcessSnapshot lifecycle tests: single allowed chat 12345, fixed clock.
func newAlertsTestBot(t *testing.T, api *telego.Bot, alertMode string, notifyOnRecovery bool) (*Bot, *AlertTracker) {
	t.Helper()
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "cfg.json"), BotConfig{AlertMode: alertMode})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	testNow := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tracker := NewAlertTracker()
	b := &Bot{
		api:              api,
		ctx:              context.Background(),
		targets:          []ChatTarget{{ChatID: 12345}},
		allowedChatIDs:   map[int64]bool{12345: true},
		configMgr:        cm,
		notifyOnRecovery: notifyOnRecovery,
		seeded:           true,
		lastSeen:         make(map[string]bool),
		lastFlapAlert:    make(map[string]time.Time),
		tracker:          tracker,
		eventBuffer:      NewEventBuffer(),
		nowFunc:          func() time.Time { return testNow },
	}
	return b, tracker
}

func snapshotFor(stableID, name string, online bool) []metrics.ProxyMetric {
	return []metrics.ProxyMetric{
		{StableID: stableID, Name: name, Address: "1.2.3.4:443", Protocol: "vless", Online: online, LatencyMs: 42},
	}
}

// FIX 3b: a proxy that went down WITH a visible alert and recovered must be
// handled per alert mode (Live: edit message; Clean: delete message).
func TestProcessSnapshot_AlertedDownThenUp_LiveEditsMessage(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, tracker := newAlertsTestBot(t, api, AlertModeLive, true)

	downAt := time.Now().Add(-10 * time.Minute)
	tracker.Track(12345, 0, 777, "p1", "Proxy 1", downAt, "timeout")
	b.lastSeen["p1"] = false

	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", true))

	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected tracker alert to be resolved after recovery")
	}
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("Live mode must edit the alert message upon recovery")
	}
	found := false
	for _, c := range edits {
		text, _ := c.Params["text"].(string)
		if strings.Contains(text, "восстановлен") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected recovery text in edited message, got %v", edits)
	}
}

func TestProcessSnapshot_AlertedDownThenUp_CleanDeletesMessage(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, tracker := newAlertsTestBot(t, api, AlertModeClean, false)

	downAt := time.Now().Add(-10 * time.Minute)
	tracker.Track(12345, 0, 777, "p1", "Proxy 1", downAt, "timeout")
	b.lastSeen["p1"] = false

	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", true))

	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected tracker alert to be resolved after recovery")
	}
	if dels := ft.callsOf("deleteMessage"); len(dels) == 0 {
		t.Fatalf("Clean mode must delete the alert message upon recovery")
	}
	for _, c := range ft.callsOf("sendMessage") {
		if text, _ := c.Params["text"].(string); strings.Contains(text, "восстановлен") {
			t.Errorf("Clean mode with notifyOnRecovery=false must not send recovery texts, got: %s", text)
		}
	}
}

// FIX 3a: a down transition suppressed by flap-protection produces no alert;
// the later up transition must NOT emit an orphan recovery message.
func TestProcessSnapshot_SuppressedDownThenUp_NoOrphanRecoveryLive(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, tracker := newAlertsTestBot(t, api, AlertModeLive, true)

	tmpDir := t.TempDir()
	store, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}
	b.statsStore = store

	testNow := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	// 24 transitions -> flap count > 10; flap alert cooldown active (just now).
	for i := 0; i < 24; i++ {
		online := i%2 == 0
		store.RecordTransition("p1", "Proxy 1", online, "test", testNow.Add(time.Duration(-60+i)*time.Minute))
	}
	b.lastFlapAlert["p1"] = testNow

	b.lastSeen["p1"] = true
	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", false)) // down, suppressed

	if tracker.HasAlert(12345, 0, "p1") {
		t.Fatalf("suppressed down must not create a tracker alert")
	}
	if _, ok := ft.lastParamOf("sendMessage", "text"); ok {
		t.Fatalf("suppressed down must not send any message")
	}

	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", true)) // up again

	for _, c := range ft.callsOf("sendMessage") {
		if text, _ := c.Params["text"].(string); strings.Contains(text, "восстановлен") {
			t.Errorf("Live mode must not send an orphan recovery message after a flap-suppressed down, got: %s", text)
		}
	}
	for _, c := range ft.callsOf("editMessageText") {
		if text, _ := c.Params["text"].(string); strings.Contains(text, "восстановлен") {
			t.Errorf("Live mode must not edit any message into recovery after a flap-suppressed down, got: %s", text)
		}
	}
}

func TestProcessSnapshot_SuppressedDownThenUp_NoOrphanRecoveryClean(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, tracker := newAlertsTestBot(t, api, AlertModeClean, true)

	tmpDir := t.TempDir()
	store, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}
	b.statsStore = store

	testNow := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 24; i++ {
		online := i%2 == 0
		store.RecordTransition("p1", "Proxy 1", online, "test", testNow.Add(time.Duration(-60+i)*time.Minute))
	}
	b.lastFlapAlert["p1"] = testNow

	b.lastSeen["p1"] = true
	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", false)) // down, suppressed
	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", true))  // up again

	for _, c := range ft.callsOf("sendMessage") {
		if text, _ := c.Params["text"].(string); strings.Contains(text, "восстановлен") {
			t.Errorf("Clean mode must not send an orphan recovery message after a flap-suppressed down, got: %s", text)
		}
	}
	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("no alert must remain tracked after suppressed down + up")
	}
}

// FIX 3c: a proxy removed from the snapshot while its outage alert is still
// active must have the tracker entry resolved and the alert message deleted
// (mirror of the disabled-proxy cleanup).
func TestProcessSnapshot_RemovedProxyWithActiveAlert_ResolvedAndDeleted(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, tracker := newAlertsTestBot(t, api, AlertModeLive, true)

	downAt := time.Now().Add(-30 * time.Minute)
	tracker.Track(12345, 0, 777, "p1", "Proxy 1", downAt, "timeout")
	b.lastSeen["p1"] = false

	// Proxy disappears from the snapshot entirely.
	b.ProcessSnapshot([]metrics.ProxyMetric{})

	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected tracker alert for removed proxy to be resolved")
	}
	dels := ft.callsOf("deleteMessage")
	if len(dels) == 0 {
		t.Fatalf("expected alert message deletion for removed proxy")
	}
	found := false
	for _, c := range dels {
		if id, ok := c.Params["message_id"].(float64); ok && int(id) == 777 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected deleteMessage for alert message 777, got %v", dels)
	}
}
