package telegram

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

// newQuietAlertsTestBot mirrors newAlertsTestBot (alerts_test.go) but keeps
// quiet hours active via a snooze so ProcessSnapshot takes the quiet-hours
// recovery path (alert edit/delete instead of the recovery-message flow).
func newQuietAlertsTestBot(t *testing.T, api *telego.Bot, alertMode string) (*Bot, *AlertTracker) {
	t.Helper()
	tmpDir := t.TempDir()
	testNow := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	cm, err := NewConfigManager(filepath.Join(tmpDir, "cfg.json"), BotConfig{
		AlertMode:        alertMode,
		QuietSnoozeUntil: testNow.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	tracker := NewAlertTracker()
	b := &Bot{
		api:            api,
		ctx:            context.Background(),
		targets:        []ChatTarget{{ChatID: 12345}},
		allowedChatIDs: map[int64]bool{12345: true},
		configMgr:      cm,
		seeded:         true,
		lastSeen:       make(map[string]bool),
		lastFlapAlert:  make(map[string]time.Time),
		tracker:        tracker,
		eventBuffer:    NewEventBuffer(),
		nowFunc:        func() time.Time { return testNow },
	}
	return b, tracker
}

// trackDownedAlert seeds the tracker + lastSeen so the next online snapshot
// is a recovery of a proxy with a visible alert message (id 777).
func trackDownedAlert(b *Bot, tracker *AlertTracker) {
	downAt := time.Now().Add(-10 * time.Minute)
	tracker.Track(12345, 0, 777, "p1", "Proxy 1", downAt, "timeout")
	b.lastSeen["p1"] = false
}

// --- FIX: quiet-hours recovery API calls must go through callWithRateLimitRetry

// TestProcessSnapshot_QuietLiveRecovery_RetriesEditOn429: the quiet-hours
// Live-mode recovery edit is an EditMessageText like any other and must be
// routed through the 429 retry helper: 429-then-success means exactly 2
// attempts and the edit applied.
func TestProcessSnapshot_QuietLiveRecovery_RetriesEditOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"editMessageText": {"429:1", "ok"},
	})
	b, tracker := newQuietAlertsTestBot(t, api, AlertModeLive)
	b.rateLimitMaxWait = 200 * time.Millisecond

	trackDownedAlert(b, tracker)

	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", true))

	if got := fake.callsOf("editMessageText"); got != 2 {
		t.Fatalf("quiet-hours recovery edit must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected tracker alert to be resolved after quiet recovery")
	}
	if buffered := b.eventBuffer.Drain(); len(buffered) != 1 || buffered[0].Type != "up" {
		t.Errorf("quiet-hours recovery must still buffer exactly one up event, got %+v", buffered)
	}
}

// TestProcessSnapshot_QuietCleanRecovery_RetriesDeleteOn429: same contract
// for the Clean-mode alert-message delete in quiet hours.
func TestProcessSnapshot_QuietCleanRecovery_RetriesDeleteOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"deleteMessage": {"429:1", "ok"},
	})
	b, tracker := newQuietAlertsTestBot(t, api, AlertModeClean)
	b.rateLimitMaxWait = 200 * time.Millisecond

	trackDownedAlert(b, tracker)

	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", true))

	if got := fake.callsOf("deleteMessage"); got != 2 {
		t.Fatalf("quiet-hours recovery delete must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected tracker alert to be resolved after quiet recovery")
	}
}

// TestProcessSnapshot_429RetrySleepDoesNotHoldMu pins the lock-safety
// invariant of callWithRateLimitRetry ("sleeps between attempts, so callers
// must not hold b.mu across it"): while the quiet-hours recovery edit is
// sleeping off its first 429, another goroutine must be able to acquire
// b.mu. Pre-fix the edit ran under the lock, so b.mu stayed locked for the
// whole retry window and the probe could never acquire it while the first
// attempt was the latest one.
func TestProcessSnapshot_429RetrySleepDoesNotHoldMu(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"editMessageText": {"429:1", "ok"},
	})
	b, tracker := newQuietAlertsTestBot(t, api, AlertModeLive)
	b.rateLimitMaxWait = 500 * time.Millisecond // probe window inside the retry sleep

	trackDownedAlert(b, tracker)

	lockFreeDuringFirstAttempt := make(chan bool, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if b.mu.TryLock() {
				attempts := fake.callsOf("editMessageText")
				b.mu.Unlock()
				if attempts == 1 {
					// Lock became acquirable while the recovery edit had made
					// exactly one attempt — i.e. the retry sleeps unlocked.
					lockFreeDuringFirstAttempt <- true
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
		lockFreeDuringFirstAttempt <- false
	}()

	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", true))

	if !<-lockFreeDuringFirstAttempt {
		t.Fatalf("b.mu must be acquirable while the 429 retry sleeps (it must not be held across the recovery edit)")
	}
	if got := fake.callsOf("editMessageText"); got != 2 {
		t.Errorf("expected the recovery edit to complete via retry, got %d attempts", got)
	}
}
