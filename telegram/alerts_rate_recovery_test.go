package telegram

import (
	"testing"
	"time"

	"xray-checker/metrics"
)

// --- FIX: 429-retry symmetry on the NON-quiet alert paths -------------------
//
// The quiet-hours recovery path (alerts_rate_test.go, wave M) already routes
// its edit/delete through callWithRateLimitRetry. The non-quiet execution
// blocks ("0./1./1b./2./3." in ProcessSnapshot) must follow the same contract:
// every alert-path API call retries on 429 (429-then-success ⇒ exactly 2
// attempts, action applied). All sites run AFTER b.mu.Unlock(), so the retry
// helper may sleep there safely.

// disabledSnapshot returns a snapshot where the proxy is administratively
// disabled (ProcessSnapshot must clean up its active alert message).
func disabledSnapshot(stableID, name string) []metrics.ProxyMetric {
	return []metrics.ProxyMetric{
		{StableID: stableID, Name: name, Address: "1.2.3.4:443", Protocol: "vless", Online: false, Disabled: true},
	}
}

// TestProcessSnapshot_LiveRecovery_RetriesEditOn429: the non-quiet Live-mode
// recovery edit (execution block 3) is an EditMessageText like any other and
// must be routed through callWithRateLimitRetry: 429-then-success means
// exactly 2 attempts and the recovery applied.
func TestProcessSnapshot_LiveRecovery_RetriesEditOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"editMessageText": {"429:1", "ok"},
	})
	b, tracker := newAlertsTestBot(t, api, AlertModeLive, false)
	b.rateLimitMaxWait = 200 * time.Millisecond

	trackDownedAlert(b, tracker)

	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", true))

	if got := fake.callsOf("editMessageText"); got != 2 {
		t.Fatalf("non-quiet Live recovery edit must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected tracker alert to be resolved after recovery")
	}
}

// TestProcessSnapshot_CleanRecovery_RetriesDeleteOn429: the non-quiet
// Clean-mode alert-message delete (execution block 3) must retry after a 429.
func TestProcessSnapshot_CleanRecovery_RetriesDeleteOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"deleteMessage": {"429:1", "ok"},
	})
	b, tracker := newAlertsTestBot(t, api, AlertModeClean, false)
	b.rateLimitMaxWait = 200 * time.Millisecond

	trackDownedAlert(b, tracker)

	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", true))

	if got := fake.callsOf("deleteMessage"); got != 2 {
		t.Fatalf("non-quiet Clean recovery delete must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected tracker alert to be resolved after recovery")
	}
}

// TestProcessSnapshot_DisabledDeletion_RetriesDeleteOn429: the disabled-proxy
// alert cleanup (execution block 1) deletes the stale alert message and must
// survive a 429 via the retry helper.
func TestProcessSnapshot_DisabledDeletion_RetriesDeleteOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"deleteMessage": {"429:1", "ok"},
	})
	b, tracker := newAlertsTestBot(t, api, AlertModeLive, false)
	b.rateLimitMaxWait = 200 * time.Millisecond

	trackDownedAlert(b, tracker)

	b.ProcessSnapshot(disabledSnapshot("p1", "Proxy 1"))

	if got := fake.callsOf("deleteMessage"); got != 2 {
		t.Fatalf("disabled-proxy alert deletion must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected tracker alert to be resolved after disabled cleanup")
	}
}

// TestProcessSnapshot_RemovedDeletion_RetriesDeleteOn429: a proxy dropped from
// the snapshot (execution block 1b) resolves its still-active alert and the
// deletion must survive a 429 via the retry helper.
func TestProcessSnapshot_RemovedDeletion_RetriesDeleteOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"deleteMessage": {"429:1", "ok"},
	})
	b, tracker := newAlertsTestBot(t, api, AlertModeLive, false)
	b.rateLimitMaxWait = 200 * time.Millisecond

	trackDownedAlert(b, tracker)

	// Snapshot without p1: the proxy was removed from the upstream set.
	b.ProcessSnapshot(nil)

	if got := fake.callsOf("deleteMessage"); got != 2 {
		t.Fatalf("removed-proxy alert deletion must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected tracker alert to be resolved after removed cleanup")
	}
}

// TestProcessSnapshot_OutageSend_RetriesOn429 (regression pin, execution
// block 2): the outage alert send goes through sendAndReturn, which already
// routes through callWithRateLimitRetry — 429-then-success must deliver the
// alert exactly once and the outage must be tracked. This test pins that the
// send-side of the alert path keeps its retry coverage.
func TestProcessSnapshot_OutageSend_RetriesOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"sendMessage": {"429:1", "ok"},
	})
	b, tracker := newAlertsTestBot(t, api, AlertModeLive, false)
	b.rateLimitMaxWait = 200 * time.Millisecond

	// Proxy known and was online, now down: a non-quiet outage.
	b.lastSeen["p1"] = true

	b.ProcessSnapshot(snapshotFor("p1", "Proxy 1", false))

	if got := fake.callsOf("sendMessage"); got != 2 {
		t.Fatalf("outage alert send must retry after 429 (exactly 2 attempts), got %d", got)
	}
	if !tracker.HasAlert(12345, 0, "p1") {
		t.Errorf("expected the outage alert to be tracked after the delivered send")
	}
}
