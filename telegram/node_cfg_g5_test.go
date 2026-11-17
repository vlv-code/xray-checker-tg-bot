package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

// --- G5c: route classification pins ---

// TestNodeRenameToggleCallbacksClassified: menu:nodes:ren: / menu:nodes:tg:
// are mutating (admin-gated) routes that answer their own toasts; the cfg
// screens stay open views.
func TestNodeRenameToggleCallbacksClassified(t *testing.T) {
	for _, data := range []string{"menu:nodes:ren:n1", "menu:nodes:tg:n1:off", "menu:nodes:tg:n1:on"} {
		if !isMutatingCallback(data) {
			t.Errorf("%q must be admin-gated (mutating)", data)
		}
		if !isToastCallbackData(data) {
			t.Errorf("%q must answer with its own toast (generic early-ack would consume the callback)", data)
		}
	}
	for _, data := range []string{"menu:nodes:cfg", "menu:nodes:cfg:n1", "menu:nodes:excl:n1"} {
		if isMutatingCallback(data) {
			t.Errorf("%q is a view route and must stay open", data)
		}
	}
}

// --- G5b/G5c: per-node screen buttons ---

// TestNodeCfgScreen_ToggleAndRenameButtons: the screen offers the toggle with
// the action matching the node's current state and a rename button; a
// disabled node carries the ⏸️ notice.
func TestNodeCfgScreen_ToggleAndRenameButtons(t *testing.T) {
	_, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a")
	b, _ := newCfgTestBot(t, api, nil, mgr)

	// Enabled node: «⏸️ Отключить ноду» → :off; no toggle-on; rename present.
	_, markup := b.getNodeCfgScreen("m31a")
	if !hasCallback(markup, "menu:nodes:tg:m31a:off") {
		t.Errorf("enabled node screen must offer toggle-off, got %v", markupCallbacks(markup))
	}
	if hasCallback(markup, "menu:nodes:tg:m31a:on") {
		t.Errorf("enabled node screen must not offer toggle-on")
	}
	if !hasCallback(markup, "menu:nodes:ren:m31a") {
		t.Errorf("node screen must offer rename, got %v", markupCallbacks(markup))
	}

	// Disabled node: «▶️ Включить ноду» → :on + ⏸️ notice text.
	mgr.disabled = map[string]bool{"m31a": true}
	text, markupDisabled := b.getNodeCfgScreen("m31a")
	if !strings.Contains(text, "⏸️") || !strings.Contains(text, "отключена") {
		t.Errorf("disabled node screen must carry the ⏸️ notice, got: %s", text)
	}
	if !hasCallback(markupDisabled, "menu:nodes:tg:m31a:on") {
		t.Errorf("disabled node screen must offer toggle-on, got %v", markupCallbacks(markupDisabled))
	}
	if hasCallback(markupDisabled, "menu:nodes:tg:m31a:off") {
		t.Errorf("disabled node screen must not offer toggle-off")
	}
}

// TestNodeCfgScreen_Callback64ByteGuardG5: every G5 button is composed via
// cbNodeCfg — a 49-char name keeps the rename button alive at exactly 64
// bytes but must omit the toggle (67 bytes); a 40-char name keeps both. All
// emitted callbacks stay ≤64 bytes.
func TestNodeCfgScreen_Callback64ByteGuardG5(t *testing.T) {
	name40 := strings.Repeat("n", 40)
	mgr := newCfgRecordingManager(name40)
	_, api := newFakeTelegramBot(t)
	b, _ := newCfgTestBot(t, api, nil, mgr)

	_, markup40 := b.getNodeCfgScreen(name40)
	if !hasCallback(markup40, "menu:nodes:tg:"+name40+":off") {
		t.Errorf("40-char name: toggle callback is 58 bytes and must stay")
	}
	if !hasCallback(markup40, "menu:nodes:ren:"+name40) {
		t.Errorf("40-char name: rename callback is 55 bytes and must stay")
	}

	name49 := strings.Repeat("n", 49)
	mgr.nodes[name49] = true // the double must know both names
	_, markup49 := b.getNodeCfgScreen(name49)
	for _, cb := range markupCallbacks(markup49) {
		if len(cb) > 64 {
			t.Errorf("callback_data exceeds 64 bytes: %q (%d bytes)", cb, len(cb))
		}
		if strings.HasPrefix(cb, "menu:nodes:tg:") {
			t.Errorf("toggle button must be omitted for 49-char names, got %q", cb)
		}
	}
	if !hasCallback(markup49, "menu:nodes:ren:"+name49) {
		t.Errorf("rename callback is exactly 64 bytes for a 49-char name and must stay")
	}
	if !strings.Contains(markupText(t, b, name49), "часть кнопок недоступна") {
		t.Error("omitted G5 buttons must extend the long-name hint")
	}
}

// markupText renders the screen text for a node (helper for guard assertions).
func markupText(t *testing.T, b *Bot, node string) string {
	t.Helper()
	text, _ := b.getNodeCfgScreen(node)
	return text
}

// --- G5b/G5c: toggle callback (menu:nodes:tg:) ---

// TestMenuNodesToggleCallback_GatedAndRouted: non-admins are denied without
// any state change; admins toggle through nodeMgr.SetNodeEnabled with a toast
// and a screen re-render; bogus actions are inert.
func TestMenuNodesToggleCallback_GatedAndRouted(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a")
	b, _ := newCfgTestBot(t, api, []int64{1001}, mgr)

	// 1. Non-admin: denied, nothing toggled, nothing edited.
	b.handleCallbackQuery(gateCallback(9999, "menu:nodes:tg:m31a:off"))
	if len(mgr.enabledCalls) != 0 {
		t.Fatalf("non-admin toggle must be denied, got %v", mgr.enabledCalls)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "только администраторам") {
		t.Errorf("expected admin-denial toast, got ok=%v toast=%q", ok, toast)
	}
	if edits := ft.callsOf("editMessageText"); len(edits) != 0 {
		t.Errorf("denied toggle must not edit any message, got %d edits", len(edits))
	}

	// 2. Admin toggle-off: SetNodeEnabled(false) + ⏸️ toast + screen re-render.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:tg:m31a:off"))
	if len(mgr.enabledCalls) != 1 || mgr.enabledCalls[0] != "m31a=false" {
		t.Fatalf("expected SetNodeEnabled(m31a,false), got %v", mgr.enabledCalls)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "⏸️") {
		t.Errorf("expected toggle-off toast, got ok=%v toast=%q", ok, toast)
	}
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatal("toggle must re-render the node screen")
	}
	lastText, _ := edits[len(edits)-1].Params["text"].(string)
	if !strings.Contains(lastText, "Настройки ноды m31a") {
		t.Errorf("expected the node screen to re-render, got: %s", lastText)
	}

	// 3. Admin toggle-on.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:tg:m31a:on"))
	if len(mgr.enabledCalls) != 2 || mgr.enabledCalls[1] != "m31a=true" {
		t.Fatalf("expected SetNodeEnabled(m31a,true), got %v", mgr.enabledCalls)
	}

	// 4. Bogus action: inert (no manager call, no panic).
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:tg:m31a:bogus"))
	if len(mgr.enabledCalls) != 2 {
		t.Fatalf("bogus action must not toggle, got %v", mgr.enabledCalls)
	}

	// 5. Unknown node: error toast from the manager rejection.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:tg:unknown:off"))
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "❌") {
		t.Errorf("expected an error toast for unknown node, got ok=%v toast=%q", ok, toast)
	}
}

// --- G5a/G5c: rename callback (menu:nodes:ren:) + waiting state ---

// TestMenuNodesRenameCallback_SetsWaitingState: admin tap arms the waiting
// state, renders the prompt with a working cancel button and answers with a
// toast; non-admins are denied and nothing is armed.
func TestMenuNodesRenameCallback_SetsWaitingState(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("m31a")
	b, _ := newCfgTestBot(t, api, []int64{1001}, mgr)

	// Non-admin: denied, nothing armed.
	b.handleCallbackQuery(gateCallback(9999, "menu:nodes:ren:m31a"))
	if len(b.waitingNodeRename) != 0 {
		t.Fatalf("non-admin must not arm the rename waiting state, got %v", b.waitingNodeRename)
	}

	// Admin: waiting state armed + prompt edit + toast.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:ren:m31a"))
	if b.waitingNodeRename[12345] != "m31a" {
		t.Fatalf("expected waitingNodeRename[12345]=m31a, got %v", b.waitingNodeRename)
	}
	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatal("expected the rename prompt to render")
	}
	text, _ := edits[0].Params["text"].(string)
	if !strings.Contains(text, "Переименование") || !strings.Contains(text, "m31a") {
		t.Errorf("expected the rename prompt to name the node, got: %s", text)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "новое имя") {
		t.Errorf("expected a rename toast, got ok=%v toast=%q", ok, toast)
	}
}

// TestHandleNodeRename_WaitingTextApplies: the next plain text message in the
// chat is consumed as the new node name — valid names rename through
// nodeMgr, invalid ones are rejected, non-admins are denied, and the waiting
// state is always consumed (single-shot, self-clearing).
func TestHandleNodeRename_WaitingTextApplies(t *testing.T) {
	// 1. Happy path.
	fnm := &fakeNodeManager{nodes: map[string]string{"m31a": "tok"}}
	b := &Bot{
		nodeMgr:           fnm,
		allowedChatIDs:    map[int64]bool{123: true},
		waitingNodeRename: map[int64]string{123: "m31a"},
	}
	b.handleMessage(&telego.Message{Chat: telego.Chat{ID: 123}, Text: "msk-2"})
	if len(fnm.renames) != 1 || fnm.renames[0] != "m31a->msk-2" {
		t.Fatalf("waiting text must rename through nodeMgr, got %v", fnm.renames)
	}
	if b.waitingNodeRename[123] != "" {
		t.Fatalf("waiting state must be consumed, got %v", b.waitingNodeRename)
	}

	// 2. Invalid name: rejected, waiting state still consumed.
	fnm2 := &fakeNodeManager{nodes: map[string]string{"m31a": "tok"}}
	b2 := &Bot{
		nodeMgr:           fnm2,
		allowedChatIDs:    map[int64]bool{123: true},
		waitingNodeRename: map[int64]string{123: "m31a"},
	}
	b2.handleMessage(&telego.Message{Chat: telego.Chat{ID: 123}, Text: "bad name!"})
	if len(fnm2.renames) != 0 {
		t.Fatalf("invalid name must not rename, got %v", fnm2.renames)
	}
	if b2.waitingNodeRename[123] != "" {
		t.Fatalf("waiting state must be consumed even on rejection, got %v", b2.waitingNodeRename)
	}

	// 3. Non-admin text: denied before any rename.
	fnm3 := &fakeNodeManager{nodes: map[string]string{"m31a": "tok"}}
	b3 := &Bot{
		nodeMgr:           fnm3,
		allowedChatIDs:    map[int64]bool{123: true},
		waitingNodeRename: map[int64]string{123: "m31a"},
	}
	b3.SetAdminUserIDs([]int64{1001})
	b3.handleMessage(&telego.Message{
		Chat: telego.Chat{ID: 123},
		From: &telego.User{ID: 9999, Username: "intruder"},
		Text: "msk-2",
	})
	if len(fnm3.renames) != 0 {
		t.Fatalf("non-admin waiting text must be denied, got %v", fnm3.renames)
	}
}

// TestHandleNodeRenameCommand: /noderename <old> <new> renames through
// nodeMgr; wrong arity, reserved targets and invalid names are rejected
// before any call.
func TestHandleNodeRenameCommand(t *testing.T) {
	fnm := &fakeNodeManager{nodes: map[string]string{"m31a": "tok", "msk-2": "tok2"}}
	b := &Bot{nodeMgr: fnm, allowedChatIDs: map[int64]bool{123: true}}

	b.handleMessage(&telego.Message{Chat: telego.Chat{ID: 123}, Text: "/noderename m31a new-name"})
	if len(fnm.renames) != 1 || fnm.renames[0] != "m31a->new-name" {
		t.Fatalf("command must rename, got %v", fnm.renames)
	}

	before := len(fnm.renames)
	b.handleMessage(&telego.Message{Chat: telego.Chat{ID: 123}, Text: "/noderename new-name"})          // arity
	b.handleMessage(&telego.Message{Chat: telego.Chat{ID: 123}, Text: "/noderename new-name local"})    // reserved
	b.handleMessage(&telego.Message{Chat: telego.Chat{ID: 123}, Text: "/noderename new-name bad!name"}) // pattern
	b.handleMessage(&telego.Message{Chat: telego.Chat{ID: 123}, Text: "/noderename nope x"})            // unknown
	if len(fnm.renames) != before {
		t.Fatalf("rejected command forms must not rename, got %v", fnm.renames)
	}
}

// --- G5b: /nodes ⏸️ ---

// TestReplyNodes_ShowsDisabledNode: disabled nodes render with ⏸️ and an
// explicit «отключена» marker instead of the up/down icons.
func TestReplyNodes_ShowsDisabledNode(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	fnm := &fakeNodeManager{
		nodes:    map[string]string{"m31a": "t1", "n2": "t2"},
		disabled: map[string]bool{"n2": true},
	}
	b, _ := newCfgTestBot(t, api, nil, fnm)

	b.replyNodes(ChatTarget{ChatID: 12345})

	sent := append(ft.callsOf("sendMessage"), ft.callsOf("editMessageText")...)
	if len(sent) == 0 {
		t.Fatal("expected /nodes to send a message")
	}
	text, _ := sent[0].Params["text"].(string)
	if !strings.Contains(text, "⏸️") || !strings.Contains(text, "отключена") {
		t.Errorf("disabled node must show ⏸️ + «отключена», got: %s", text)
	}
	if !strings.Contains(text, "🟢") || strings.Contains(text, "⏸️ n2") && !strings.Contains(text, "n2") {
		t.Errorf("enabled node must keep its 🟢 icon, got: %s", text)
	}
}

// --- G5b: alert suppression for disabled nodes ---

// TestUpdateNodeHealthLocked_DisabledNodeSuppressed: a disabled node is
// invisible to the alert loop — its state is dropped, no alerts fire for it,
// and a later re-enable seeds silently (no bogus recovery).
func TestUpdateNodeHealthLocked_DisabledNodeSuppressed(t *testing.T) {
	b := &Bot{lastNodeHealth: make(map[string]nodeHealthState)}
	base := time.Now()

	// Seed up, alert down (precondition: the node was being watched).
	if alerts := b.updateNodeHealthLocked([]NodeInfo{{Name: "n1", Up: true}}, base); len(alerts) != 0 {
		t.Fatalf("seed must be silent: %v", alerts)
	}
	if alerts := b.updateNodeHealthLocked([]NodeInfo{{Name: "n1", Up: false}}, base.Add(time.Minute)); len(alerts) != 1 {
		t.Fatalf("precondition: want 1 down alert, got %v", alerts)
	}

	// Disabled: no alerts, tracked state dropped.
	if alerts := b.updateNodeHealthLocked([]NodeInfo{{Name: "n1", Up: false, Disabled: true}}, base.Add(2*time.Minute)); len(alerts) != 0 {
		t.Fatalf("disabled node must be suppressed, got %v", alerts)
	}
	if len(b.lastNodeHealth) != 0 {
		t.Fatalf("disabled node state must be pruned, got %v", b.lastNodeHealth)
	}

	// Re-enabled and up: seeds silently — no bogus recovery.
	if alerts := b.updateNodeHealthLocked([]NodeInfo{{Name: "n1", Up: true}}, base.Add(3*time.Minute)); len(alerts) != 0 {
		t.Fatalf("re-enabled node must seed silently, got %v", alerts)
	}
}
