package telegram

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// Wave N-K (429 polish tail, mirror of alerts_rate_test.go): the Check-Host
// alert-resolution delete/edit paths (telegram/checkhost.go) are Bot API calls
// like any other and must be routed through callWithRateLimitRetry — a flood
// limit must not silently lose a recovery edit or leave a stale alert message
// behind. Each test scripts 429-then-ok on the rate fake and pins EXACTLY 2
// attempts (pre-fix: 1 attempt, no retry = RED).

// newRateCheckHostAuditBot builds a Bot wired to the scripted-429 fake with
// one local TCP proxy target and Check-Host alerting enabled in the given
// alert mode (quiet hours off: the down path goes through sendAndReturn).
func newRateCheckHostAuditBot(t *testing.T, api *telego.Bot, alertMode string) (*Bot, *AlertTracker) {
	t.Helper()
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{
		CheckHostBgEnabled:    true,
		CheckHostAlertEnabled: true,
		AlertMode:             alertMode,
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	tracker := NewAlertTracker()
	bot := &Bot{
		api:            api,
		ctx:            context.Background(),
		targets:        []ChatTarget{{ChatID: 12345}},
		allowedChatIDs: map[int64]bool{12345: true},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Proxy-X", StableID: "proxy-x", Address: "1.1.1.1:443", Protocol: "vless", Online: true},
		}},
		configMgr:      cm,
		tracker:        tracker,
		eventBuffer:    NewEventBuffer(),
		checkHostNodes: []string{"ru2.node.check-host.net", "de1.node.check-host.net"},
	}
	return bot, tracker
}

// TestRunAuditNow_CleanRecovery_RetriesDeleteOn429: the master-audit Clean-mode
// recovery deletes the tracked alert message; the first delete answers 429 and
// the SAME delete must be retried to success (exactly 2 API attempts).
func TestRunAuditNow_CleanRecovery_RetriesDeleteOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"deleteMessage": {"429:1", "ok"},
	})
	b, tracker := newRateCheckHostAuditBot(t, api, AlertModeClean)
	b.rateLimitMaxWait = 200 * time.Millisecond

	// Run 1: RU-block signature (RU down + world up) raises and tracks the alert.
	b.checkHostClient = newCheckHostAuditMock(t, false, true)
	b.runAuditNow()
	if !tracker.HasAlert(12345, 0, "checkhost:1.1.1.1:443") {
		t.Fatalf("precondition: RU-block alert must be tracked after the first audit")
	}

	// Run 2: RU recovered -> the tracked alert message is deleted via retry.
	b.checkHostClient = newCheckHostAuditMock(t, true, true)
	b.runAuditNow()

	if got := fake.callsOf("deleteMessage"); got != 2 {
		t.Fatalf("checkhost recovery delete must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if tracker.HasAlert(12345, 0, "checkhost:1.1.1.1:443") {
		t.Errorf("expected tracker alert to be resolved after checkhost recovery")
	}
}

// TestRunAuditNow_LiveRecovery_RetriesEditOn429: the master-audit Live-mode
// recovery edits the tracked alert message in place; 429-then-success means
// exactly 2 attempts and the edit applied.
func TestRunAuditNow_LiveRecovery_RetriesEditOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"editMessageText": {"429:1", "ok"},
	})
	b, tracker := newRateCheckHostAuditBot(t, api, AlertModeLive)
	b.rateLimitMaxWait = 200 * time.Millisecond

	b.checkHostClient = newCheckHostAuditMock(t, false, true)
	b.runAuditNow()
	if !tracker.HasAlert(12345, 0, "checkhost:1.1.1.1:443") {
		t.Fatalf("precondition: RU-block alert must be tracked after the first audit")
	}

	b.checkHostClient = newCheckHostAuditMock(t, true, true)
	b.runAuditNow()

	if got := fake.callsOf("editMessageText"); got != 2 {
		t.Fatalf("checkhost recovery edit must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if tracker.HasAlert(12345, 0, "checkhost:1.1.1.1:443") {
		t.Errorf("expected tracker alert to be resolved after checkhost recovery")
	}
}

// TestProcessNodeCheckHostAudits_RetriesDeleteOn429: the node-shipped-audit
// Clean-mode recovery (ProcessNodeCheckHostAudits) must route its tracked-alert
// delete through the 429 retry helper — the down path and signature gates are
// already pinned by TestProcessNodeCheckHostAudits_UnreachableEverywhereNoAlert.
func TestProcessNodeCheckHostAudits_RetriesDeleteOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"deleteMessage": {"429:1", "ok"},
	})
	b, tracker := newRateCheckHostAuditBot(t, api, AlertModeClean)
	b.rateLimitMaxWait = 200 * time.Millisecond

	tracker.Track(12345, 0, 777, "checkhost:6.6.6.6:443", "6.6.6.6:443", time.Now().Add(-10*time.Minute), "CheckHost RU Block")

	b.ProcessNodeCheckHostAudits("node-a", map[string]checker.CheckHostSummary{
		"6.6.6.6:443": {RUAvailable: true, WorldAvailable: true},
	})

	if got := fake.callsOf("deleteMessage"); got != 2 {
		t.Fatalf("node-audit recovery delete must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if tracker.HasAlert(12345, 0, "checkhost:6.6.6.6:443") {
		t.Errorf("expected tracker alert to be resolved after node-audit recovery")
	}
}
