package telegram

// Wave N-A FIX 1: the two-step confirm flows must answer the CONFIRM-SCREEN
// tap with an AnswerCallbackQuery toast. Before the fix only the delsub route
// acknowledged the arm tap — the ndel/nreset confirm screens left the client
// spinner hanging until Telegram's ~5s callback timeout (wave-M observation).
// The «:yes» steps keep their own toasts (pinned by the existing K-B2/M1
// tests) and are NOT covered here.

import (
	"strings"
	"testing"
)

// TestConfirmScreenAcksCallbackQuery pins the ack-wart fix: tapping the ARM
// button of a two-step confirm flow (no «:yes» suffix) renders the confirm
// screen AND answers the callback query with a short RU toast; the toast is
// a plain notification (not an alert), matching the delsub model site.
func TestConfirmScreenAcksCallbackQuery(t *testing.T) {
	cases := []struct {
		name     string
		data     string
		wantEdit string // confirm-screen text marker
		wantAck  string // ack toast substring
	}{
		{
			name:     "ndel arm tap",
			data:     "menu:nodes:ndel:x",
			wantEdit: "Точно удалить ноду",
			wantAck:  "Подтвердите удаление",
		},
		{
			name:     "nreset arm tap",
			data:     "menu:nodes:nreset:x",
			wantEdit: "Точно сбросить ВСЕ",
			wantAck:  "Подтвердите сброс",
		},
		{
			name:     "delsub arm tap (model site, regression pin)",
			data:     "menu:nodes:delsub:n:0",
			wantEdit: "Точно удалить подписку",
			wantAck:  "Подтвердите удаление",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft, api := newFakeTelegramBot(t)
			mgr := &fakeNodeManager{
				nodes: map[string]string{"x": "tok", "n": "tok"},
				subs:  map[string][]string{"n": {"https://s/x"}},
			}
			b, _ := newCfgTestBot(t, api, nil, mgr)

			b.handleCallbackQuery(gateCallback(1001, tc.data))

			// The confirm screen renders and nothing is mutated yet.
			edits := ft.callsOf("editMessageText")
			if len(edits) != 1 {
				t.Fatalf("arm tap must render exactly the confirm screen, got %d edits", len(edits))
			}
			text, _ := edits[0].Params["text"].(string)
			if !strings.Contains(text, tc.wantEdit) {
				t.Fatalf("expected the confirm screen marker %q, got: %s", tc.wantEdit, text)
			}
			// Nothing is mutated on the arm tap: the node and the subscription
			// both survive until the explicit «:yes» tap.
			if _, ok := mgr.nodes["x"]; !ok {
				t.Errorf("arm tap must not remove the node")
			}
			if got := mgr.subs["n"]; len(got) != 1 || got[0] != "https://s/x" {
				t.Errorf("arm tap must not remove the subscription, got %v", got)
			}

			// The fix: the confirm-screen tap must be acknowledged.
			if acks := ft.callsOf("answerCallbackQuery"); len(acks) == 0 {
				t.Fatalf("confirm-screen tap must answer the callback query (no spinner hang), got 0 acks")
			}
			toast, alert := toastParams(t, ft)
			if !strings.Contains(toast, tc.wantAck) {
				t.Errorf("ack toast must contain %q, got %q", tc.wantAck, toast)
			}
			if alert {
				t.Errorf("confirm ack must be a plain notification, not an alert")
			}
		})
	}
}
