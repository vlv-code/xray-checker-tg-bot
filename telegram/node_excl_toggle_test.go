package telegram

import (
	"sort"
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"xray-checker/metrics"
)

// newExclTestBot builds a bot whose merged snapshot carries two proxies of
// node m31a (StableID «m31a/idx1» name Alpha, «m31a/idx2» name Beta), one
// master-local proxy and one foreign-node proxy, plus the given prefixed
// DisabledProxies entries. In the merged snapshot a node proxy's StableID is
// already namespaced «node/localID» (nodes/report.go), which is exactly the
// DisabledProxies entry format for per-node exclusions.
func newExclTestBot(t *testing.T, adminIDs []int64, disabled []string) (*Bot, *fakeTelegramAPI, *ConfigManager, *cfgRecordingManager) {
	t.Helper()
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a")
	b, cm := newCfgTestBot(t, api, adminIDs, mgr)
	if len(disabled) > 0 {
		_ = cm.Update(func(c *BotConfig) {
			c.DisabledProxies = append([]string(nil), disabled...)
		})
	}
	b.source = &mockSource{metrics: []metrics.ProxyMetric{
		{Name: "Local", StableID: "local-1", Address: "9.9.9.9:443", Protocol: "vless"},
		{Name: "Foreign", StableID: "other/zzz", Address: "3.3.3.3:443", Protocol: "vless", NodeName: "other"},
		{Name: "Beta", StableID: "m31a/idx2", Address: "2.2.2.2:443", Protocol: "vless", NodeName: "m31a"},
		{Name: "Alpha", StableID: "m31a/idx1", Address: "1.1.1.1:443", Protocol: "vless", NodeName: "m31a"},
	}}
	return b, ft, cm, mgr
}

// TestNodeExclScreenFunctionalList: the exclusions screen is a FUNCTIONAL
// paginated list of the node's own proxies (merged snapshot, all statuses,
// sorted by StableID) with ⏸️/▶️ toggle rows reflecting the current prefixed
// cfg state; master-local and foreign-node proxies stay out.
func TestNodeExclScreenFunctionalList(t *testing.T) {
	b, _, _, _ := newExclTestBot(t, nil, []string{"m31a/idx2"})

	text, markup := b.getNodeExclusionsView("m31a", 1, "")

	if markup == nil {
		t.Fatalf("expected exclusions screen markup")
	}
	var rows []string
	for _, cb := range markupCallbacks(markup) {
		if strings.HasPrefix(cb, "menu:nodes:nx:") {
			rows = append(rows, cb)
		}
	}
	// Sorted by StableID: idx1 → Alpha (row 0), idx2 → Beta (row 1).
	if len(rows) != 2 || rows[0] != "menu:nodes:nx:m31a:0:1" || rows[1] != "menu:nodes:nx:m31a:1:1" {
		t.Fatalf("expected toggle rows [m31a:0:1 m31a:1:1], got %v", rows)
	}
	row0 := markup.InlineKeyboard[0][0]
	row1 := markup.InlineKeyboard[1][0]
	if row0.Text != "▶️ Alpha" || row0.CallbackData != "menu:nodes:nx:m31a:0:1" {
		t.Errorf("row 0 must be the enabled «▶️ Alpha», got %q / %q", row0.Text, row0.CallbackData)
	}
	if row1.Text != "⏸️ Beta" || row1.CallbackData != "menu:nodes:nx:m31a:1:1" {
		t.Errorf("row 1 must be the excluded «⏸️ Beta», got %q / %q", row1.Text, row1.CallbackData)
	}

	// Master-local and foreign-node proxies must not appear.
	for _, cb := range rows {
		if cb == "menu:nodes:nx:m31a:2:1" || cb == "menu:nodes:nx:m31a:3:1" {
			t.Errorf("non-node proxy leaked into the per-node list: %q", cb)
		}
	}
	if strings.Contains(text, "Local") || strings.Contains(text, "Foreign") {
		t.Errorf("non-node proxies must not be listed, got: %s", text)
	}

	// Header shows the counts, the master-only hint is gone (Wave H1 made the
	// screen functional), and the back button stays on the node screen.
	if !strings.Contains(text, "Всего прокси: <b>2</b>") || !strings.Contains(text, "Исключено: <b>1</b>") {
		t.Errorf("expected «Всего прокси: 2 · Исключено: 1» header, got: %s", text)
	}
	if strings.Contains(text, "только в мастер-экранах") {
		t.Errorf("read-only master-screen hint must be removed, got: %s", text)
	}
	if !hasCallback(markup, "menu:nodes:cfg:m31a") || !hasCallback(markup, "menu:main") {
		t.Errorf("expected back-to-node-screen and main-menu buttons, got %v", markupCallbacks(markup))
	}
	for _, cb := range markupCallbacks(markup) {
		if len(cb) > 64 {
			t.Errorf("callback_data exceeds 64 bytes: %q (%d)", cb, len(cb))
		}
	}

	// A node with no reported proxies renders an empty-state notice.
	bEmpty, _, _, _ := newExclTestBot(t, nil, nil)
	bEmpty.nodeMgr = newCfgRecordingManager("m31a", "quiet-node")
	emptyText, emptyMarkup := bEmpty.getNodeExclusionsView("quiet-node", 1, "")
	if !strings.Contains(emptyText, "нет прокси") {
		t.Errorf("expected empty-state notice, got: %s", emptyText)
	}
	for _, cb := range markupCallbacks(emptyMarkup) {
		if strings.HasPrefix(cb, "menu:nodes:nx:") || strings.HasPrefix(cb, "menu:nodes:exclpg:") {
			t.Errorf("empty list must have no toggle/pager buttons, got %q", cb)
		}
	}
}

// TestNodeExclToggleWritesViaUpdateConfig: tapping a row toggles the prefixed
// entry `node/stableID` in cfg.DisabledProxies through the updateConfig seam
// (never nodeMgr settings), echoes the proxy NAME in the toast (safety echo
// against index shifts), and re-renders the screen with the flipped row.
func TestNodeExclToggleWritesViaUpdateConfig(t *testing.T) {
	b, ft, cm, mgr := newExclTestBot(t, []int64{1001}, nil)

	// 1. Exclude Beta (sorted idx 1): entry added, toast echoes the name.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:nx:m31a:1:1"))

	got := cm.Get().DisabledProxies
	if len(got) != 1 || got[0] != "m31a/idx2" {
		t.Fatalf("expected DisabledProxies [m31a/idx2] after exclude, got %v", got)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "Исключён: Beta") {
		t.Errorf("expected safety-echo toast «Исключён: Beta», got ok=%v toast=%q", ok, toast)
	}
	if len(mgr.setCalls) != 0 {
		t.Errorf("exclusions toggle must not write node settings, got setCalls %v", mgr.setCalls)
	}
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected the exclusions screen to re-render after the toggle")
	}
	if flipped := edits[len(edits)-1].Params["text"].(string); !strings.Contains(flipped, "Исключено: <b>1</b>") {
		t.Errorf("re-rendered screen must show the new excluded count, got: %s", flipped)
	}

	// 2. Toggle again: entry removed, toast echoes the restore.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:nx:m31a:1:1"))

	got = cm.Get().DisabledProxies
	if len(got) != 0 {
		t.Fatalf("expected empty DisabledProxies after restore, got %v", got)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "Возвращён: Beta") {
		t.Errorf("expected toast «Возвращён: Beta», got ok=%v toast=%q", ok, toast)
	}
	// Foreign entries survive the rewrite untouched.
	// (Coordinator fix of a killed-run scaffold contradiction: re-exclude
	// Beta (idx1) rather than Alpha — the preset already contains m31a/idx1,
	// so restoring it must keep only the foreign other/zzz entry.)
	_ = cm.Update(func(c *BotConfig) { c.DisabledProxies = []string{"other/zzz"} })
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:nx:m31a:1:1"))
	got = cm.Get().DisabledProxies
	if len(got) != 2 || got[0] != "other/zzz" || got[1] != "m31a/idx2" {
		t.Fatalf("exclude of Beta must keep the foreign entry and add m31a/idx2, got %v", got)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "Исключён: Beta") {
		t.Errorf("expected toast «Исключён: Beta», got ok=%v toast=%q", ok, toast)
	}
}

// TestNodeExclToggleIdxShiftNoWrite: an out-of-range row index (subscription
// changed between render and tap) must NOT write anything — the screen
// re-renders with a «список изменился» notice and the toast carries no
// exclusion confirmation.
func TestNodeExclToggleIdxShiftNoWrite(t *testing.T) {
	b, ft, cm, _ := newExclTestBot(t, []int64{1001}, nil)

	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:nx:m31a:9:1"))

	if got := cm.Get().DisabledProxies; len(got) != 0 {
		t.Fatalf("idx-shift toggle must not write cfg, got %v", got)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); ok && strings.Contains(toast, "Исключён") {
		t.Errorf("idx-shift must not confirm an exclusion, got toast %q", toast)
	}
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("idx-shift must re-render the screen")
	}
	if text := edits[len(edits)-1].Params["text"].(string); !strings.Contains(text, "Список изменился") {
		t.Errorf("expected «Список изменился» notice, got: %s", text)
	}
}

// TestNodeExclToggleGatedByAdmin: the toggle route is mutating — a non-admin
// is denied (no write, no re-render) while the admin toggle passes.
func TestNodeExclToggleGatedByAdmin(t *testing.T) {
	b, ft, cm, _ := newExclTestBot(t, []int64{1001}, nil)

	b.handleCallbackQuery(gateCallback(9999, "menu:nodes:nx:m31a:1:1"))

	if got := cm.Get().DisabledProxies; len(got) != 0 {
		t.Fatalf("non-admin toggle must not write cfg, got %v", got)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "только администраторам") {
		t.Errorf("expected admin-denial toast, got ok=%v toast=%q", ok, toast)
	}
	if edits := ft.callsOf("editMessageText"); len(edits) != 0 {
		t.Errorf("denied toggle must not edit any message, got %d edits", len(edits))
	}
}

// TestNodeExclPagination: more than menuListPageSize proxies paginate —
// 20 toggle rows plus a «⬅️ Стр. 1/2 ➡️» row (menu:nodes:exclpg:) on page 1,
// the remainder on page 2, and page numbers clamp into range.
func TestNodeExclPagination(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a")
	b, _ := newCfgTestBot(t, api, nil, mgr)
	var snap []metrics.ProxyMetric
	for i := 0; i < 25; i++ {
		snap = append(snap, metrics.ProxyMetric{
			Name:     "P" + pad2(i),
			StableID: "m31a/p" + pad2(i),
			Address:  "10.0.0." + pad2(i) + ":443",
			Protocol: "vless",
			NodeName: "m31a",
		})
	}
	// Deterministic order sanity for the fixture itself.
	sort.Slice(snap, func(i, j int) bool { return snap[i].StableID < snap[j].StableID })
	b.source = &mockSource{metrics: snap}

	text, markup := b.getNodeExclusionsView("m31a", 1, "")
	toggle := 0
	pagerNext := false
	for _, cb := range markupCallbacks(markup) {
		if strings.HasPrefix(cb, "menu:nodes:nx:") {
			toggle++
			if !strings.HasSuffix(cb, ":1") {
				t.Errorf("page-1 toggle callbacks must carry page 1, got %q", cb)
			}
		}
		if cb == "menu:nodes:exclpg:m31a:2" {
			pagerNext = true
		}
	}
	if toggle != menuListPageSize {
		t.Fatalf("expected %d toggle rows on page 1, got %d", menuListPageSize, toggle)
	}
	if !pagerNext {
		t.Errorf("expected the pager row to offer page 2, got %v", markupCallbacks(markup))
	}
	if !strings.Contains(text, "Стр. 1/2") {
		t.Errorf("expected «Стр. 1/2» in the pager row, got %v", markupCallbacks(markup))
	}

	// Hop through the pager route: page 2 shows the remaining 5 proxies.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:exclpg:m31a:2"))
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected the pager to render page 2")
	}
	page2Toggle := 0
	pagerPrev := false
	_ = page2Toggle
	_, p2 := b.getNodeExclusionsView("m31a", 2, "")
	for _, cb := range markupCallbacks(p2) {
		if strings.HasPrefix(cb, "menu:nodes:nx:") {
			page2Toggle++
		}
		if cb == "menu:nodes:exclpg:m31a:1" {
			pagerPrev = true
		}
	}
	if page2Toggle != 5 {
		t.Errorf("expected 5 toggle rows on page 2, got %d", page2Toggle)
	}
	if !pagerPrev {
		t.Errorf("expected the pager row to offer page 1 back, got %v", markupCallbacks(p2))
	}

	// Out-of-range page clamps to the last page.
	_, clamped := b.getNodeExclusionsView("m31a", 99, "")
	if n := countPrefixCallbacks(clamped, "menu:nodes:nx:"); n != 5 {
		t.Errorf("page 99 must clamp to page 2 (5 rows), got %d rows", n)
	}
}

func pad2(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// TestNodeExclLongNameCallbackGuard: a node name longer than the raw-key cap
// (cbNodeKeyMaxRawLen) is hashed by cbNodeKey, so the REAL exclusions markup
// emits hashed-key toggle callbacks that stay well under the 64-byte
// callback_data limit, every emitted callback routes, and the toggle still
// writes the correct prefixed entry — pinning the hash→name round-trip
// (nodeByKey) end-to-end.
func TestNodeExclLongNameCallbackGuard(t *testing.T) {
	long := strings.Repeat("n", 45) // 45 > cbNodeKeyMaxRawLen → hashed key
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager(long)
	b, cm := newCfgTestBot(t, api, []int64{1001}, mgr)
	b.source = &mockSource{metrics: []metrics.ProxyMetric{
		{Name: "OnlyOne", StableID: "zz1", Address: "5.5.5.5:443", Protocol: "vless", NodeName: long},
	}}

	text, markup := b.getNodeExclusionsView(long, 1, "")
	for _, cb := range markupCallbacks(markup) {
		if len(cb) > 64 {
			t.Errorf("callback_data exceeds 64 bytes: %q (%d)", cb, len(cb))
		}
		if !callbackRoutes(cb) {
			t.Errorf("emitted callback does not route in callbacks.go: %q", cb)
		}
		if strings.HasPrefix(cb, "menu:nodes:nx:") && !strings.HasPrefix(cb, "menu:nodes:nx:n:") {
			t.Errorf("long node name must be hashed in the toggle key, got %q", cb)
		}
	}
	if !strings.Contains(text, "Всего прокси: <b>1</b>") {
		t.Errorf("expected the long-name screen header, got: %s", text)
	}

	// Tap the hashed-key row: the toggle resolves the node name back and
	// writes the prefixed entry.
	var nx string
	for _, cb := range markupCallbacks(markup) {
		if strings.HasPrefix(cb, "menu:nodes:nx:") {
			nx = cb
		}
	}
	if nx == "" {
		t.Fatalf("expected a hashed-key toggle row, got %v", markupCallbacks(markup))
	}
	b.handleCallbackQuery(gateCallback(1001, nx))

	got := cm.Get().DisabledProxies
	if len(got) != 1 || got[0] != long+"/zz1" {
		t.Fatalf("hashed-key toggle must write %q, got %v", long+"/zz1", got)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "Исключён: OnlyOne") {
		t.Errorf("expected safety-echo toast «Исключён: OnlyOne», got ok=%v toast=%q", ok, toast)
	}
}

// TestWaveH2NavHopChain pins the full navigation tree after the D4 rework:
// cfg picker → node screen → subs manage (back to node screen) and node
// screen → exclusions (back to node screen); the removed global route
// menu:nodes:subs must be UNROUTED (no edit on tap) and no node-section
// markup may still emit it (dead-button invariant).
func TestWaveH2NavHopChain(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a")
	b, _ := newCfgTestBot(t, api, []int64{1001}, mgr)
	b.source = &mockSource{metrics: []metrics.ProxyMetric{
		{Name: "Alpha", StableID: "m31a/idx1", Address: "1.1.1.1:443", Protocol: "vless", NodeName: "m31a"},
	}}

	// Hop 1: picker — one node button, back to menu:nodes.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:cfg"))
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected the cfg picker to render")
	}
	if text, _ := edits[len(edits)-1].Params["text"].(string); !strings.Contains(text, "Выберите ноду") {
		t.Errorf("expected picker prompt, got: %s", text)
	}

	// Hop 2: node screen — subs and exclusions entries present.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:cfg:m31a"))
	if text, _ := ft.callsOf("editMessageText")[len(ft.callsOf("editMessageText"))-1].Params["text"].(string); !strings.Contains(text, "Настройки ноды m31a") {
		t.Errorf("expected node screen, got: %s", text)
	}
	_, nodeMarkup := b.getNodeCfgScreen("m31a")
	if !hasCallback(nodeMarkup, "menu:nodes:subnode:m31a") || !hasCallback(nodeMarkup, "menu:nodes:excl:m31a") {
		t.Errorf("expected subs+excl entries on the node screen, got %v", markupCallbacks(nodeMarkup))
	}

	// Hop 3: subs manage screen — back row targets the NODE screen, never
	// the removed global route.
	_, manageMarkup := b.getNodeSubsManageView("m31a")
	requireAllCallbacksRoutable(t, "NodeSubsManageMarkup", manageMarkup)
	if !hasCallback(manageMarkup, "menu:nodes:cfg:m31a") {
		t.Errorf("expected manage back row to target menu:nodes:cfg:m31a, got %v", markupCallbacks(manageMarkup))
	}

	// Hop 4: exclusions screen — back to the node screen, toggle rows live.
	_, exclMarkup := b.getNodeExclusionsView("m31a", 1, "")
	requireAllCallbacksRoutable(t, "getNodeExclusionsView", exclMarkup)
	if !hasCallback(exclMarkup, "menu:nodes:cfg:m31a") {
		t.Errorf("expected exclusions back row to target menu:nodes:cfg:m31a, got %v", markupCallbacks(exclMarkup))
	}
	if !hasCallback(exclMarkup, "menu:nodes:nx:m31a:0:1") {
		t.Errorf("expected a toggle row on the exclusions screen, got %v", markupCallbacks(exclMarkup))
	}

	// Global surfaces: neither NodesSettingsMarkup nor the manage screen may
	// still emit the removed route.
	for _, row := range NodesSettingsMarkup(b.GetConfig()).InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "menu:nodes:subs" {
				t.Errorf("NodesSettingsMarkup still emits the removed route menu:nodes:subs")
			}
		}
	}

	// Hop 5: menu:nodes:subs is UNROUTED — the tap produces no edit at all.
	editsBefore := len(ft.callsOf("editMessageText"))
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:subs"))
	if editsAfter := len(ft.callsOf("editMessageText")); editsAfter != editsBefore {
		t.Errorf("removed route menu:nodes:subs must not render anything, got %d new edits", editsAfter-editsBefore)
	}
}

func countPrefixCallbacks(markup *telego.InlineKeyboardMarkup, prefix string) int {
	n := 0
	for _, cb := range markupCallbacks(markup) {
		if strings.HasPrefix(cb, prefix) {
			n++
		}
	}
	return n
}

// TestCbNodeKey: short node names are used raw as callback keys; long names
// hash to a fixed "n:"+12hex key so the toggle callbacks always fit the
// 64-byte callback_data limit regardless of the node name length.
func TestCbNodeKey(t *testing.T) {
	if got := cbNodeKey("m31a"); got != "m31a" {
		t.Errorf("short node name must stay raw, got %q", got)
	}
	long := strings.Repeat("n", 40)
	h1 := cbNodeKey(long)
	h2 := cbNodeKey(long)
	if h1 == long || !strings.HasPrefix(h1, "n:") || len(h1) != 14 {
		t.Errorf("long node name must hash to n:+12hex, got %q", h1)
	}
	if h1 != h2 {
		t.Errorf("cbNodeKey must be deterministic, got %q vs %q", h1, h2)
	}
	// Composed toggle callback for a long-name node stays ≤64 bytes.
	cb := "menu:nodes:nx:" + h1 + ":999:999"
	if len(cb) > 64 {
		t.Errorf("toggle callback exceeds 64 bytes: %q (%d)", cb, len(cb))
	}
}
