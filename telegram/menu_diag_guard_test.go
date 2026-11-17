package telegram

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
)

// callbackRoutes reports whether a callback_data value has a routing rule in
// callbacks.go handleCallbackQuery (exact switch cases or prefix branches).
// Wave-F B1/B2 pin: every emitted callback must route — no dead buttons.
func callbackRoutes(data string) bool {
	switch data {
	// Wave H2/D4 register: the global subs screen route menu:nodes:subs was
	// removed — it is intentionally UNROUTED now.
	// Wave H4 register: menu:cheburin (manual Cheburcheck host input) is an
	// exact route.
	// Wave H6 register: menu:checkhostadd (manual custom Check-Host host
	// input) is an exact route.
	case "menu:main", "menu:noop", "menu:diag", "menu:nodes", "menu:nodes:settings", "menu:cheburin", "menu:checkhostadd":
		return true
	}
	prefixes := []string{
		"menu:diag:node:",
		"menu:diag:details:",
		"menu:diag:pick_deep:",
		"menu:diag:deep:",
		"menu:diag:refresh:",
		"menu:diag:noop:",
		"menu:nodes:subnode:",
		"menu:nodes:delsub:",
		"menu:nodes:addsub:",
		// Wave H1/H2 register: the per-node cfg screen and exclusions routes
		// pre-existed in callbacks.go but are listed only now that the helper
		// covers their markups (NodeCfgMarkup / exclusions screen), together
		// with the new functional-exclusions routes.
		"menu:nodes:cfg:",
		"menu:nodes:excl:",
		"menu:nodes:nx:",
		"menu:nodes:exclpg:",
		// Wave G: the stableID route menu:chebur:<id> is replaced by the
		// host-based probe + pager routes.
		"menu:cheburh:",
		"menu:cheburpg:",
		// Wave H6 register: the Check-Host submenu became the union host list —
		// the per-host check route menu:checkhosth:<key> and the custom-delete
		// route menu:checkhostdel:<key> replace the per-proxy route
		// menu:checkhost:run:<stableID>, whose emitter (the old proxy-list
		// markup) is gone — the legacy entry is intentionally removed.
		"menu:checkhosth:",
		"menu:checkhostdel:",
		"menu:checkhostpg:",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(data, p) {
			return true
		}
	}
	return false
}

// requireAllCallbacksRoutable asserts the hard 64-byte callback_data limit and
// the no-dead-buttons invariant for every button of the markup.
func requireAllCallbacksRoutable(t *testing.T, surface string, markup *telego.InlineKeyboardMarkup) {
	t.Helper()
	for _, cb := range markupCallbacks(markup) {
		if len(cb) > 64 {
			t.Errorf("%s: callback_data exceeds 64 bytes: %q (%d bytes)", surface, cb, len(cb))
		}
		if !callbackRoutes(cb) {
			t.Errorf("%s: emitted callback does not route in callbacks.go: %q", surface, cb)
		}
	}
}

// maxStableID builds the widest reachable stableID for a node with the given
// name: nodeName + "/" + 16-hex (nodes/report.go namespaced form).
func maxStableID(nodeName string) string {
	return nodeName + "/" + strings.Repeat("a", 16)
}

// TestDiagDeepSurfacesCallback64ByteGuard (Wave-F B1): with a 32-char node
// name the deep recheck/pick callbacks are 15+49+2=66 bytes — over Telegram's
// hard 64-byte callback_data limit — so every over-limit button must be
// omitted via cbNodeCfg (full callback incl. the page suffix), a routable
// hint row must explain the omission, short-name controls stay unchanged,
// and no emitted button may be dead.
func TestDiagDeepSurfacesCallback64ByteGuard(t *testing.T) {
	nodeName := strings.Repeat("n", 32) // max validNodeName length
	stableID := maxStableID(nodeName)   // 49 bytes, "node/id" form

	// 1. DeepDiagnosticsMarkup: recheck button omitted, fitting back stays.
	mk := DeepDiagnosticsMarkup(stableID, 1)
	requireAllCallbacksRoutable(t, "DeepDiagnosticsMarkup", mk)
	for _, cb := range markupCallbacks(mk) {
		if strings.HasPrefix(cb, "menu:diag:deep:") {
			t.Errorf("recheck button must be omitted for a maxed stableID, got %q", cb)
		}
	}
	if !hasCallback(mk, "menu:diag:details:"+nodeName+":1") {
		t.Errorf("fitting back button must remain, got %v", markupCallbacks(mk))
	}

	// 2. Short-name control unchanged: both buttons survive.
	ctl := DeepDiagnosticsMarkup("proxy-x", 2)
	if !hasCallback(ctl, "menu:diag:deep:proxy-x:2") || !hasCallback(ctl, "menu:diag:details:2") {
		t.Errorf("short-name deep markup must keep both buttons, got %v", markupCallbacks(ctl))
	}

	// 3. Defensive 45-char node name (possible via unvalidated NODES env):
	// pick_deep tabs and details callbacks overflow and must be omitted.
	longName := strings.Repeat("x", 45)
	tabs := []NodeTabItem{{ID: longName, Label: "x45"}}
	reports := []checker.ProxyDiagReport{{ProxyName: "P", StableID: stableID, Status: "online"}}
	pick := PickDeepDiagnosticsMarkupWithTabs("local", tabs, reports, 1)
	requireAllCallbacksRoutable(t, "PickDeepDiagnosticsMarkupWithTabs", pick)
	for _, cb := range markupCallbacks(pick) {
		if strings.HasPrefix(cb, "menu:diag:deep:") ||
			strings.HasPrefix(cb, "menu:diag:pick_deep:") ||
			strings.HasPrefix(cb, "menu:diag:details:"+longName+":") {
			t.Errorf("over-limit button must be omitted, got %q", cb)
		}
	}
	if !hasCallback(pick, "menu:noop") {
		t.Errorf("expected a routable hint row (menu:noop) when buttons were omitted, got %v", markupCallbacks(pick))
	}

	// 4. Short-name pick control: tab and deep buttons survive.
	pickCtl := PickDeepDiagnosticsMarkupWithTabs("local",
		[]NodeTabItem{{ID: "m31a", Label: "m31a"}, {ID: "msk-1", Label: "msk-1"}},
		[]checker.ProxyDiagReport{{ProxyName: "P", StableID: "p1", Status: "online"}}, 1)
	if !hasCallback(pickCtl, "menu:diag:pick_deep:m31a:1") || !hasCallback(pickCtl, "menu:diag:deep:p1:1") {
		t.Errorf("short-name pick markup must keep tab and deep buttons, got %v", markupCallbacks(pickCtl))
	}

	// 5. RichReportMarkupWithTabs: details + deep links omitted for long name.
	rich := RichReportMarkupWithTabs(longName, nil, DiagDeepLink{Name: "P", StableID: stableID})
	requireAllCallbacksRoutable(t, "RichReportMarkupWithTabs", rich)
	for _, cb := range markupCallbacks(rich) {
		if strings.HasPrefix(cb, "menu:diag:deep:") ||
			strings.HasPrefix(cb, "menu:diag:details:"+longName+":") {
			t.Errorf("over-limit rich-report button must be omitted, got %q", cb)
		}
	}
	if !hasCallback(rich, "menu:diag:refresh:"+longName) {
		t.Errorf("fitting refresh button must remain, got %v", markupCallbacks(rich))
	}

	// 6. DiagnosticsPaginationMarkupWithTabs: node tabs, prev/next, pick_deep
	// and refresh:details guards.
	pag := DiagnosticsPaginationMarkupWithTabs(longName, 2, 3, tabs)
	requireAllCallbacksRoutable(t, "DiagnosticsPaginationMarkupWithTabs", pag)
	for _, cb := range markupCallbacks(pag) {
		if strings.HasPrefix(cb, "menu:diag:details:"+longName+":") ||
			strings.HasPrefix(cb, "menu:diag:pick_deep:") ||
			strings.HasPrefix(cb, "menu:diag:refresh:details:") {
			t.Errorf("over-limit pagination button must be omitted, got %q", cb)
		}
	}

	// 7. Short-name pagination control keeps its callbacks.
	pagCtl := DiagnosticsPaginationMarkupWithTabs("m31a", 2, 3,
		[]NodeTabItem{{ID: "m31a", Label: "m31a"}, {ID: "msk-1", Label: "msk-1"}})
	for _, want := range []string{
		"menu:diag:details:m31a:1",
		"menu:diag:details:m31a:3",
		"menu:diag:pick_deep:m31a:2",
		"menu:diag:refresh:details:m31a:2",
	} {
		if !hasCallback(pagCtl, want) {
			t.Errorf("short-name pagination must keep %q, got %v", want, markupCallbacks(pagCtl))
		}
	}
}

// TestNodesSubsCallbacksGuardAndDelsubParse (Wave-F B2): over-limit node
// names must drop their delsub/addsub buttons (hint row instead), 40-char
// names still fit, and the delsub parser must split on the LAST
// colon so names containing ':' round-trip through the emitted button.
// Wave H2/D4 register: the former NodesSubsListMarkup sections were removed
// together with the global subs screen; the manage-screen guard pins remain.
func TestNodesSubsCallbacksGuardAndDelsubParse(t *testing.T) {
	longName := strings.Repeat("w", 50)

	// 1. Manage screen: delsub/addsub omitted for the long name + hint.
	manage := NodeSubsManageMarkup(longName, []ManagedSubInfo{{URL: "https://s/1", ProxyCount: 1}})
	requireAllCallbacksRoutable(t, "NodeSubsManageMarkup", manage)
	if hasCallback(manage, "menu:nodes:delsub:"+longName+":0") {
		t.Errorf("over-limit delsub button must be omitted, got %v", markupCallbacks(manage))
	}
	if hasCallback(manage, "menu:nodes:addsub:"+longName) {
		t.Errorf("over-limit addsub button must be omitted, got %v", markupCallbacks(manage))
	}

	// 2. 40-char control: 18+40+2=60 bytes still fit.
	name40 := strings.Repeat("w", 40)
	manage40 := NodeSubsManageMarkup(name40, []ManagedSubInfo{{URL: "https://s/1", ProxyCount: 1}})
	if !hasCallback(manage40, "menu:nodes:delsub:"+name40+":0") {
		t.Errorf("40-char delsub button (60 bytes) must be kept, got %v", markupCallbacks(manage40))
	}

	// 3. Colon round-trip: a node named "a:b" emits menu:nodes:delsub:a:b:0
	// and the LastIndex-colon parser must resolve name="a:b", idx=0.
	fnm := &fakeNodeManager{
		nodes: map[string]string{"a:b": "tok"},
		subs:  map[string][]string{"a:b": {"https://s/x"}},
	}
	manageColon := NodeSubsManageMarkup("a:b", []ManagedSubInfo{{URL: "https://s/x", ProxyCount: 5}})
	requireAllCallbacksRoutable(t, "NodeSubsManageMarkup(colon)", manageColon)
	var delCb string
	for _, cb := range markupCallbacks(manageColon) {
		if strings.HasPrefix(cb, "menu:nodes:delsub:") {
			delCb = cb
		}
	}
	if delCb != "menu:nodes:delsub:a:b:0" {
		t.Fatalf("expected delsub callback to carry the colon name verbatim, got %q", delCb)
	}
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)
	b.nodeMgr = fnm
	b.handleCallbackQuery(gateCallback(1001, delCb))
	if edits := ft.callsOf("editMessageText"); len(edits) == 0 {
		t.Fatalf("expected the manage view to re-render after delsub")
	}
	subs, err := fnm.ManagedSubs("a:b")
	if err != nil {
		t.Fatalf("ManagedSubs failed: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("expected delsub with colon node name to remove the sub, got %v", subs)
	}
}

// TestIsToastCallbackDataMembership (Wave-F B3): menu:checkupdate answers with
// its own custom toast ("Проверяю релизы..."), so it must join the toast
// membership set and never get the extra generic acknowledgement.
func TestIsToastCallbackDataMembership(t *testing.T) {
	members := []string{
		"menu:toggle_proxy:p:1",
		"menu:toggle_host:h:1",
		"menu:nodes:ncfg:n:check_method:status",
		"menu:nodes:nx:m31a:1:1",
		"menu:nodes:nreset:n",
		"menu:checkhost:run_now",
		"menu:checkupdate",
		// Wave H6 register: the add-host arm route answers with its own toast,
		// the delete route answers with the «Удалён: …» toast.
		"menu:checkhostadd",
		"menu:checkhostdel:9.9.9.9:443",
	}
	for _, d := range members {
		if !isToastCallbackData(d) {
			t.Errorf("expected isToastCallbackData(%q) = true", d)
		}
	}
	nonMembers := []string{
		"menu:main",
		"menu:settings",
		"menu:diag",
		"menu:nodes:exclpg:m31a:2", // Wave H1 register: pager is a view route (the removed menu:nodes:subs entry left the list)
		// Wave H6 register: the Check-Host per-host check and pager are view
		// routes (open to allowed chats, generic acknowledgement).
		"menu:checkhosth:1.1.1.1:443",
		"menu:checkhostpg:2",
		"menu:quiet:toggle",
		"menu:nodes:delsub:n:0",
	}
	for _, d := range nonMembers {
		if isToastCallbackData(d) {
			t.Errorf("expected isToastCallbackData(%q) = false", d)
		}
	}
}

// TestCheburMenuMarkupCapOverflowAndEmptyState (Wave-F B4) was REPLACED by
// TestCheburMenuMarkupPagination + TestCheburMenuTextEmptyState (wave G4):
// the cap+stub approach was removed in favor of pagination — authorized
// replaced-test entry 2026-11-17.
