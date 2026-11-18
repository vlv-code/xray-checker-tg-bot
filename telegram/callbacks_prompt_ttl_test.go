package telegram

import (
	"strings"
	"testing"
)

// Wave M5 (N-2): every input-arming prompt screen carries the uniform TTL
// hint «⏱ Ввод активен 5 минут · 🚫 Отмена — ниже», so users know the armed
// input expires lazily after pendingInputTTL (5 minutes) and where the
// cancel row sits. All six arm routes are driven through handleCallbackQuery
// like real button taps (mirroring TestCancelInputButtonOnEveryPromptScreen);
// the last editMessageText of each tap is the rendered prompt text.
func TestPromptScreensCarryTTLHint(t *testing.T) {
	cases := []struct {
		route string
		kind  string
	}{
		{"menu:subsadd", pendingKindSubURL},
		{"menu:checkhostadd", pendingKindCheckHostHost},
		{"menu:cheburin", pendingKindCheburHost},
		{"menu:nodes:add", pendingKindNodeAdd},
		{"menu:nodes:addsub:m31a", pendingKindNodeSub},
		{"menu:nodes:ren:m31a", pendingKindNodeRename},
	}
	for _, tc := range cases {
		t.Run(tc.route, func(t *testing.T) {
			ft, api := newFakeTelegramBot(t)
			b, _ := newGateTestBot(t, api, []int64{1001})

			b.handleCallbackQuery(gateCallback(1001, tc.route))

			edits := ft.callsOf("editMessageText")
			if len(edits) == 0 {
				t.Fatalf("expected %s to render the prompt screen", tc.route)
			}
			text, _ := edits[len(edits)-1].Params["text"].(string)
			if !strings.Contains(text, "⏱ Ввод активен 5 минут · 🚫 Отмена — ниже") {
				t.Fatalf("%s prompt must carry the uniform TTL hint line, got: %s", tc.route, text)
			}

			// The route must still arm its pending input (the hint is additive
			// only — no flow regressions).
			if !b.hasPending(12345) {
				t.Fatalf("%s must leave the pending input armed", tc.route)
			}
			kind, _, ok := b.takePending(12345)
			if !ok || kind != tc.kind {
				t.Fatalf("%s must arm kind %q, got ok=%v kind=%q", tc.route, tc.kind, ok, kind)
			}
		})
	}
}
