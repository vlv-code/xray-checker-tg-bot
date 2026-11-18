package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

// Wave L-1 pins for the unified waiting-state FSM: one pendingInput per chat
// (kind + payload + 5-minute lazy TTL) replaces the former five per-flow
// waiting maps; a single disarm hook in handleCallbackQuery clears it on any
// screen-changing callback; every input-arming prompt carries «Отмена»
// (menu:cancelinput).

// TestPendingInputRoundTrip_AllKinds: each armed kind round-trips with the
// exact payload; taking is consuming (second take misses) and clears the
// armed flag; chats are isolated.
func TestPendingInputRoundTrip_AllKinds(t *testing.T) {
	cases := []struct {
		kind    string
		payload string
	}{
		{pendingKindNodeAdd, ""},
		{pendingKindNodeSub, "m31a"},
		{pendingKindNodeRename, "msk-1"},
		{pendingKindCheburHost, ""},
		{pendingKindCheckHostHost, ""},
		{pendingKindSubURL, ""},
	}
	for _, tc := range cases {
		b := &Bot{}
		b.armPending(123, tc.kind, tc.payload)
		if !b.hasPending(123) {
			t.Fatalf("kind %s: hasPending must be true right after arming", tc.kind)
		}
		// A different chat must stay unarmed.
		if b.hasPending(456) {
			t.Fatalf("kind %s: pending input must be per-chat", tc.kind)
		}
		kind, payload, ok := b.takePending(123)
		if !ok || kind != tc.kind || payload != tc.payload {
			t.Fatalf("kind %s: takePending must return the armed (kind,payload), got ok=%v kind=%q payload=%q", tc.kind, ok, kind, payload)
		}
		if b.hasPending(123) {
			t.Fatalf("kind %s: takePending must consume the pending input", tc.kind)
		}
		if _, _, ok := b.takePending(123); ok {
			t.Fatalf("kind %s: second takePending must miss (single-shot)", tc.kind)
		}
	}
}

// TestPendingInputTTLExpiry_LazyClear: with the injectable clock, an armed
// input is live just under the TTL and absent after it; expiry is lazy — the
// read clears the entry, so the stale value never resurfaces even when the
// clock is restored.
func TestPendingInputTTLExpiry_LazyClear(t *testing.T) {
	b := &Bot{}
	base := time.Now()
	b.nowFunc = func() time.Time { return base }

	b.armPending(123, pendingKindSubURL, "")

	b.nowFunc = func() time.Time { return base.Add(pendingInputTTL - time.Second) }
	if !b.hasPending(123) {
		t.Fatalf("the pending input must stay live just under the TTL")
	}

	b.nowFunc = func() time.Time { return base.Add(pendingInputTTL + time.Minute) }
	if b.hasPending(123) {
		t.Fatalf("an expired pending input must read as absent")
	}
	if _, _, ok := b.takePending(123); ok {
		t.Fatalf("takePending on an expired input must miss")
	}
	// Lazy clear: even with the clock wound back the stale entry is gone.
	b.nowFunc = func() time.Time { return base }
	if b.hasPending(123) {
		t.Fatalf("the expired entry must be cleared on read, not resurface")
	}
}

// TestExpiredPendingDoesNotSwallowPlainText: a TTL-expired pending input must
// not swallow a plain text into the armed flow — the text goes down the
// normal message path (no rename, no denial); a FRESH input with the same
// text still renames (control arm).
func TestExpiredPendingDoesNotSwallowPlainText(t *testing.T) {
	fnm := &fakeNodeManager{nodes: map[string]string{"m31a": "tok"}}
	b := &Bot{
		nodeMgr:        fnm,
		allowedChatIDs: map[int64]bool{123: true},
	}
	base := time.Now()
	b.nowFunc = func() time.Time { return base }
	b.armPending(123, pendingKindNodeRename, "m31a")

	// Wind past the TTL: the armed rename input is expired.
	b.nowFunc = func() time.Time { return base.Add(pendingInputTTL + time.Minute) }
	b.handleMessage(&telego.Message{Chat: telego.Chat{ID: 123}, Text: "msk-2"})
	if len(fnm.renames) != 0 {
		t.Fatalf("an expired pending input must not swallow plain text into the flow, got %v", fnm.renames)
	}

	// Control: the same text with a fresh input IS consumed as the rename.
	b.nowFunc = nil
	b.armPending(123, pendingKindNodeRename, "m31a")
	b.handleMessage(&telego.Message{Chat: telego.Chat{ID: 123}, Text: "msk-2"})
	if len(fnm.renames) != 1 || fnm.renames[0] != "m31a->msk-2" {
		t.Fatalf("a fresh pending input must still consume the text, got %v", fnm.renames)
	}
}

// TestNavDisarmHook: any screen-changing callback disarms the pending input;
// pure-ack toast routes (menu:noop, menu:diag:noop:*, menu:checkhost:run_now)
// keep it, and re-arming routes win over the hook clear.
func TestNavDisarmHook(t *testing.T) {
	_, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)

	// A navigation to the main menu clears the armed input.
	b.armPending(12345, pendingKindCheburHost, "")
	b.handleCallbackQuery(gateCallback(9999, "menu:main"))
	if b.hasPending(12345) {
		t.Fatalf("menu:main must disarm the pending input")
	}

	// Pure-ack toast routes keep the armed input.
	b.armPending(12345, pendingKindCheburHost, "")
	b.handleCallbackQuery(gateCallback(9999, "menu:noop"))
	if !b.hasPending(12345) {
		t.Fatalf("menu:noop (pure ack) must keep the pending input")
	}
	b.handleCallbackQuery(gateCallback(9999, "menu:diag:noop:2:3"))
	if !b.hasPending(12345) {
		t.Fatalf("menu:diag:noop:* (page ack) must keep the pending input")
	}

	// Re-arming routes clear-then-arm: the input ends up armed again.
	b.handleCallbackQuery(gateCallback(9999, "menu:cheburin"))
	if !b.hasPending(12345) {
		t.Fatalf("the arming route must leave the input armed")
	}
	kind, _, ok := b.takePending(12345)
	if !ok || kind != pendingKindCheburHost {
		t.Fatalf("expected the re-armed cheburhost input, got ok=%v kind=%q", ok, kind)
	}
}

// TestPendingKeepRouteClassification pins the small keep-allowlist of the
// disarm hook: pure acknowledgement toasts only.
func TestPendingKeepRouteClassification(t *testing.T) {
	keep := []string{"menu:noop", "menu:diag:noop:1:2", "menu:checkhost:run_now"}
	for _, d := range keep {
		if !isPendingKeepRoute(d) {
			t.Errorf("expected isPendingKeepRoute(%q) = true", d)
		}
	}
	change := []string{
		"menu:main", "menu:settings", "menu:stats", "menu:cancelinput",
		"menu:cheburin", "menu:checkhostadd", "menu:subsadd", "menu:nodes:add",
		"menu:nodes:ren:x", "menu:nodes:addsub:x", "menu:tz:Europe/Moscow",
		"menu:toggle_icons",
	}
	for _, d := range change {
		if isPendingKeepRoute(d) {
			t.Errorf("expected isPendingKeepRoute(%q) = false (screen-changing)", d)
		}
	}
}

// TestCancelInputRoute: menu:cancelinput clears the pending input, answers
// with the «Ввод отменён» toast, is NOT admin-gated (a clear is harmless) and
// IS a self-toasting route.
func TestCancelInputRoute(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)

	b.armPending(12345, pendingKindCheckHostHost, "")
	b.handleCallbackQuery(gateCallback(9999, "menu:cancelinput"))
	if b.hasPending(12345) {
		t.Fatalf("menu:cancelinput must clear the pending input")
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "Ввод отменён") {
		t.Fatalf("expected the «Ввод отменён» toast, got ok=%v toast=%q", ok, toast)
	}

	// Classification pins: exact route, ≤64B, not mutating, self-toasting.
	if len("menu:cancelinput") > 64 {
		t.Fatalf("callback_data exceeds 64 bytes")
	}
	if isMutatingCallback("menu:cancelinput") {
		t.Errorf("menu:cancelinput must not be admin-gated (it only clears)")
	}
	if !isToastCallbackData("menu:cancelinput") {
		t.Errorf("menu:cancelinput answers with its own toast")
	}
}

// TestCancelInputButtonOnEveryPromptScreen: every input-arming prompt screen
// carries the «Отмена» button (menu:cancelinput) and emits only routable
// callbacks — chebur manual host, checkhost add host, node add, node add sub,
// node rename, and the subscription add prompt.
func TestCancelInputButtonOnEveryPromptScreen(t *testing.T) {
	armingRoutes := []string{
		"menu:cheburin",
		"menu:checkhostadd",
		"menu:nodes:add",
		"menu:nodes:addsub:m31a",
		"menu:nodes:ren:m31a",
		"menu:subsadd",
	}
	for _, route := range armingRoutes {
		t.Run(route, func(t *testing.T) {
			ft, api := newFakeTelegramBot(t)
			b, _ := newGateTestBot(t, api, []int64{1001})

			b.handleCallbackQuery(gateCallback(1001, route))
			edits := ft.callsOf("editMessageText")
			if len(edits) == 0 {
				t.Fatalf("expected %s to render the prompt", route)
			}
			mk := decodedReplyMarkup(t, edits[len(edits)-1])
			if mk == nil {
				t.Fatalf("expected reply_markup on the %s prompt", route)
			}
			requireAllCallbacksRoutable(t, route+" prompt", mk)
			if !hasCallback(mk, "menu:cancelinput") {
				t.Fatalf("%s prompt must carry the «Отмена» button (menu:cancelinput), got %v", route, markupCallbacks(mk))
			}
		})
	}
}
