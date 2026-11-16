package telegram

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"xray-checker/checker"
)

// blockingDiagSource mirrors mockDiagSource (bot_test.go) but RunDiagnostics
// blocks until release() (or a safety timeout), so tests can observe the
// callback behavior WHILE a full probe sweep is still in flight. The safety
// timeout keeps a pre-fix synchronous caller from hanging the test binary:
// the assertions below fail instead.
type blockingDiagSource struct {
	mu      sync.Mutex
	calls   int
	block   chan struct{}
	reports []checker.ProxyDiagReport
}

func newBlockingDiagSource(reports []checker.ProxyDiagReport) *blockingDiagSource {
	return &blockingDiagSource{
		block:   make(chan struct{}),
		reports: reports,
	}
}

func (f *blockingDiagSource) RunDiagnostics(targets []string) []checker.ProxyDiagReport {
	f.mu.Lock()
	f.calls++
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-time.After(500 * time.Millisecond):
		}
	}
	return f.reports
}

func (f *blockingDiagSource) GetTargetManager() *checker.TargetManager { return nil }
func (f *blockingDiagSource) GetUniqueHosts() []string                 { return nil }
func (f *blockingDiagSource) GetCurrentIP() (string, error)            { return "", nil }

func (f *blockingDiagSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *blockingDiagSource) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.block != nil {
		close(f.block)
		f.block = nil
	}
}

// waitForEditCount polls the fake API until the given number of
// editMessageText calls is recorded (or a deadline passes).
func waitForEditCount(ft *fakeTelegramAPI, want int) []fakeAPICall {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if edits := ft.callsOf("editMessageText"); len(edits) >= want {
			return edits
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ft.callsOf("editMessageText")
}

func warmDiagnosticsCache(b *Bot, reports []checker.ProxyDiagReport) {
	b.diagMu.Lock()
	b.cachedDiag = reports
	b.cachedDiagAt = time.Now()
	b.diagMu.Unlock()
}

// --- FIX: synchronous diagnostics probes must not block the update loop -----

// TestCallbackDiagDetails_SlowProbeShowsPlaceholderThenResult: with a cold
// diagnostics cache the details route must behave like menu:diag — edit a
// placeholder first, run the probe sweep in the background, then edit the
// result. handleCallbackQuery must return before the probe finishes.
func TestCallbackDiagDetails_SlowProbeShowsPlaceholderThenResult(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	ds := newBlockingDiagSource([]checker.ProxyDiagReport{
		{ProxyName: "Proxy-X", Protocol: "vless", StableID: "proxy-x", Status: "online"},
	})
	b.diagSource = ds

	b.handleCallbackQuery(gateCallback(9999, "menu:diag:details"))

	// The probe is still blocked, so exactly one edit (the placeholder) must
	// exist right now; a synchronous probe would have produced the result
	// text instead.
	edits := ft.callsOf("editMessageText")
	if len(edits) != 1 {
		var dump strings.Builder
		for i, c := range edits {
			txt, _ := c.Params["text"].(string)
			dump.WriteString(fmt.Sprintf("\n[%d] %s", i, txt))
		}
		t.Fatalf("expected exactly 1 edit (placeholder) while the probe is in flight, got %d:%s", len(edits), dump.String())
	}
	if text, _ := edits[0].Params["text"].(string); !strings.Contains(text, "⏳") {
		t.Fatalf("expected a placeholder edit first while the probe runs, got: %s", text)
	}

	ds.release()
	edits = waitForEditCount(ft, 2)
	if len(edits) < 2 {
		t.Fatalf("expected the result edit after the probe finished, got %d edits", len(edits))
	}
	result, _ := edits[len(edits)-1].Params["text"].(string)
	if !strings.Contains(result, "Детальный отчёт") || !strings.Contains(result, "Proxy-X") {
		t.Fatalf("expected details page for Proxy-X as the final edit, got: %s", result)
	}
}

// TestCallbackDiagPickDeep_SlowProbeShowsPlaceholderThenResult: same contract
// for the pick_deep route.
func TestCallbackDiagPickDeep_SlowProbeShowsPlaceholderThenResult(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	ds := newBlockingDiagSource([]checker.ProxyDiagReport{
		{ProxyName: "Proxy-X", Protocol: "vless", StableID: "proxy-x", Status: "online"},
	})
	b.diagSource = ds

	b.handleCallbackQuery(gateCallback(9999, "menu:diag:pick_deep:1"))

	edits := ft.callsOf("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("expected exactly 1 edit (placeholder) while the probe is in flight, got %d", len(edits))
	}
	if text, _ := edits[0].Params["text"].(string); !strings.Contains(text, "⏳") {
		t.Fatalf("expected a placeholder edit first while the probe runs, got: %s", text)
	}

	ds.release()
	edits = waitForEditCount(ft, 2)
	if len(edits) < 2 {
		t.Fatalf("expected the result edit after the probe finished, got %d edits", len(edits))
	}
	result, _ := edits[len(edits)-1].Params["text"].(string)
	if !strings.Contains(result, "Углубленная проверка соединения") {
		t.Fatalf("expected pick_deep host picker as the final edit, got: %s", result)
	}
}

// TestCallbackDiagDetails_StaleAsyncEditSuppressedByMsgSeq: if the user taps
// another button on the same message while the probe is running, the stale
// background edit must be dropped (msg-seq guard), leaving the newer content.
func TestCallbackDiagDetails_StaleAsyncEditSuppressedByMsgSeq(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	ds := newBlockingDiagSource([]checker.ProxyDiagReport{
		{ProxyName: "Proxy-X", Protocol: "vless", StableID: "proxy-x", Status: "online"},
	})
	b.diagSource = ds

	b.handleCallbackQuery(gateCallback(9999, "menu:diag:details"))
	edits := ft.callsOf("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("expected exactly 1 edit (placeholder) while the probe is in flight, got %d", len(edits))
	}
	if text, _ := edits[0].Params["text"].(string); !strings.Contains(text, "⏳") {
		t.Fatalf("expected a placeholder edit first while the probe runs, got: %s", text)
	}

	// The user taps another button on the same message mid-probe.
	b.handleCallbackQuery(gateCallback(9999, "menu:main"))
	edits = ft.callsOf("editMessageText")
	if len(edits) != 2 {
		t.Fatalf("expected menu edit as the second edit, got %d", len(edits))
	}

	ds.release()
	time.Sleep(300 * time.Millisecond) // let the stale goroutine finish
	edits = ft.callsOf("editMessageText")
	if len(edits) != 2 {
		t.Fatalf("stale async edit must be suppressed by the msg-seq guard, got %d edits", len(edits))
	}
	if text, _ := edits[len(edits)-1].Params["text"].(string); !strings.Contains(text, "Сводка Xray Checker") {
		t.Fatalf("final edit must remain the main menu, got: %s", text)
	}
}

// TestCallbackDiagDetails_FreshCacheRendersSynchronously: with a fresh
// non-empty diagnostics cache there is no network work — the result must be
// edited synchronously (single edit, no placeholder, no probe sweep).
func TestCallbackDiagDetails_FreshCacheRendersSynchronously(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	ds := newBlockingDiagSource(nil)
	b.diagSource = ds

	warmDiagnosticsCache(b, []checker.ProxyDiagReport{
		{ProxyName: "Cached-Proxy", Protocol: "vless", StableID: "cached-proxy", Status: "online"},
	})

	b.handleCallbackQuery(gateCallback(9999, "menu:diag:details"))

	edits := ft.callsOf("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("fresh cache must render synchronously with a single edit, got %d", len(edits))
	}
	text, _ := edits[0].Params["text"].(string)
	if strings.Contains(text, "⏳") {
		t.Fatalf("fresh cache must not show a placeholder, got: %s", text)
	}
	if !strings.Contains(text, "Детальный отчёт") || !strings.Contains(text, "Cached-Proxy") {
		t.Fatalf("expected immediate details render from cache, got: %s", text)
	}
	if calls := ds.callCount(); calls != 0 {
		t.Fatalf("fresh cache must not trigger a new probe sweep, got %d RunDiagnostics calls", calls)
	}
}

// TestCallbackDiagPickDeep_FreshCacheRendersSynchronously: same fast-path
// contract for the pick_deep route.
func TestCallbackDiagPickDeep_FreshCacheRendersSynchronously(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	ds := newBlockingDiagSource(nil)
	b.diagSource = ds

	warmDiagnosticsCache(b, []checker.ProxyDiagReport{
		{ProxyName: "Cached-Proxy", Protocol: "vless", StableID: "cached-proxy", Status: "online"},
	})

	b.handleCallbackQuery(gateCallback(9999, "menu:diag:pick_deep:1"))

	edits := ft.callsOf("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("fresh cache must render synchronously with a single edit, got %d", len(edits))
	}
	text, _ := edits[0].Params["text"].(string)
	if strings.Contains(text, "⏳") {
		t.Fatalf("fresh cache must not show a placeholder, got: %s", text)
	}
	if !strings.Contains(text, "Углубленная проверка соединения") {
		t.Fatalf("expected immediate pick_deep render from cache, got: %s", text)
	}
	if calls := ds.callCount(); calls != 0 {
		t.Fatalf("fresh cache must not trigger a new probe sweep, got %d RunDiagnostics calls", calls)
	}
}
