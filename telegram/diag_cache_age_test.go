package telegram

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"xray-checker/checker"
)

// Wave M9(3): cached diagnostics outputs must carry the staleness line
// «данные от HH:MM» (source: the diagnostics cache write timestamp exposed
// by DiagCacheTimestamp). Freshly probed (cold-cache) renders must NOT carry
// it — the data is current there. Coordinator-wired after the M-wave: the
// accessor existed but had no consumer.

var diagAgeLine = regexp.MustCompile(`данные от \d{2}:\d{2}`)

func warmDiagCacheAt(b *Bot, ts time.Time, reports []checker.ProxyDiagReport) {
	b.diagMu.Lock()
	b.cachedDiag = reports
	b.cachedDiagAt = ts
	b.diagMu.Unlock()
}

func TestCachedDiagDetailsCarryAgeLine(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	ds := newBlockingDiagSource(nil)
	b.diagSource = ds

	warmDiagCacheAt(b, time.Now().Add(-2*time.Minute), []checker.ProxyDiagReport{
		{ProxyName: "Cached-Proxy", Protocol: "vless", StableID: "cached-proxy", Status: "online"},
	})

	b.handleCallbackQuery(gateCallback(9999, "menu:diag:details"))

	edits := ft.callsOf("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("fresh cache must render synchronously with a single edit, got %d", len(edits))
	}
	text, _ := edits[0].Params["text"].(string)
	if !diagAgeLine.MatchString(text) {
		t.Fatalf("cached details render must carry the «данные от HH:MM» line, got: %s", text)
	}
	if calls := ds.callCount(); calls != 0 {
		t.Fatalf("cached path must not probe, got %d RunDiagnostics calls", calls)
	}
}

func TestCachedDiagPickDeepCarryAgeLine(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	ds := newBlockingDiagSource(nil)
	b.diagSource = ds

	warmDiagCacheAt(b, time.Now().Add(-2*time.Minute), []checker.ProxyDiagReport{
		{ProxyName: "Cached-Proxy", Protocol: "vless", StableID: "cached-proxy", Status: "online"},
	})

	b.handleCallbackQuery(gateCallback(9999, "menu:diag:pick_deep:1"))

	edits := ft.callsOf("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("fresh cache must render synchronously with a single edit, got %d", len(edits))
	}
	text, _ := edits[0].Params["text"].(string)
	if !diagAgeLine.MatchString(text) {
		t.Fatalf("cached pick_deep render must carry the «данные от HH:MM» line, got: %s", text)
	}
}

func TestFreshProbeDiagRenderHasNoAgeLine(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	// Cold cache + immediately-returning source: the sweep result is fresh.
	b.diagSource = newBlockingDiagSource([]checker.ProxyDiagReport{
		{ProxyName: "Fresh-Proxy", Protocol: "vless", StableID: "fresh-proxy", Status: "online"},
	})

	b.handleCallbackQuery(gateCallback(9999, "menu:diag:details"))

	var text string
	for i := 0; i < 200; i++ {
		edits := ft.callsOf("editMessageText")
		if len(edits) >= 2 {
			text, _ = edits[len(edits)-1].Params["text"].(string)
			if strings.Contains(text, "Fresh-Proxy") {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(text, "Fresh-Proxy") {
		t.Fatalf("probe result never rendered, last: %s", text)
	}
	if diagAgeLine.MatchString(text) {
		t.Fatalf("fresh probe render must NOT carry the cache-age line, got: %s", text)
	}
}
