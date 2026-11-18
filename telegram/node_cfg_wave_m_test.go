package telegram

// Wave M-A (UX polish) tests:
//   - M1 (U-1b): /nodedel two-step confirm (confirm helpers, command screen,
//     :yes callback removal, admin gate) — mirrors the nreset confirm tests
//     in node_cfg_menu_test.go.
//   - M6 (N-3): node picker buttons carry the 🟢/🔴/⏸️ state icon (disabled
//     wins), the omit-on-overflow discipline survives.
//   - M7: ladder labels show the current → next transition (with
//     concurrencyLabel on both sides and wrap-around to the first rung).

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

// --- M1 (U-1b): /nodedel two-step confirm ---

// TestNodeDelConfirmHelpers pins the ndel confirm-screen contract for the
// callbacks.go handler: text «Точно удалить ноду …» with the escaped name
// echo and the honest effect notice, [🗑 Да, удалить] →
// menu:nodes:ndel:<name>:yes, [↩️ Отмена] back to the node cfg screen
// (static picker fallback beyond 64 bytes); every callback routes and the
// :yes confirm stays inside the admin-gated ndel prefix.
func TestNodeDelConfirmHelpers(t *testing.T) {
	text := nodeDelConfirmText("m31a")
	for _, want := range []string{"Точно удалить", "<code>m31a</code>"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirm text must contain %q, got: %s", want, text)
		}
	}
	// escapeHTML contract on the runtime name echo.
	if esc := nodeDelConfirmText("a&b"); !strings.Contains(esc, "a&amp;b") {
		t.Errorf("confirm text must escape the node name, got: %s", esc)
	}

	m := nodeDelConfirmMarkup("m31a")
	yesCb, yesOK := nodeDelConfirmCallback("m31a")
	if !yesOK || yesCb != "menu:nodes:ndel:m31a:yes" {
		t.Fatalf("expected confirm callback menu:nodes:ndel:m31a:yes, got (%q, %v)", yesCb, yesOK)
	}
	if !hasCallback(m, yesCb) {
		t.Errorf("expected the confirm row, got %v", markupCallbacks(m))
	}
	if findButtonText(m, "🗑 Да, удалить") == "" {
		t.Errorf("expected the «🗑 Да, удалить» button, got %v", markupCallbacks(m))
	}
	cancel := findButtonText(m, "Отмена")
	if !strings.HasPrefix(cancel, "↩️") {
		t.Errorf("cancel button must use «↩️» wording, got %q", cancel)
	}
	if !hasCallback(m, "menu:nodes:cfg:m31a") {
		t.Errorf("cancel must return to the node cfg screen, got %v", markupCallbacks(m))
	}
	for _, cb := range markupCallbacks(m) {
		if len(cb) > 64 {
			t.Errorf("callback_data exceeds 64 bytes: %q (%d)", cb, len(cb))
		}
		if !callbackRoutes(cb) {
			t.Errorf("confirm callback does not route in callbacks.go: %q", cb)
		}
	}
	if !isMutatingCallback(yesCb) {
		t.Errorf("confirm callback %q must stay admin-gated", yesCb)
	}

	// 49-char node: menu:nodes:ndel:<name>:yes is 69 bytes — the confirm row
	// is omitted; the cancel callback «menu:nodes:cfg:<name>» is exactly 64
	// bytes and still fits, so it stays (static picker fallback only beyond).
	long := strings.Repeat("n", 49)
	mLong := nodeDelConfirmMarkup(long)
	if hasCallback(mLong, "menu:nodes:ndel:"+long+":yes") {
		t.Errorf("over-limit confirm callback must be omitted")
	}
	if !hasCallback(mLong, "menu:nodes:cfg:"+long) {
		t.Errorf("cancel must keep the fitting per-node cfg callback, got %v", markupCallbacks(mLong))
	}
	for _, cb := range markupCallbacks(mLong) {
		if len(cb) > 64 {
			t.Errorf("callback_data exceeds 64 bytes: %q (%d)", cb, len(cb))
		}
	}
}

// TestHandleNodeDelRequiresConfirm: /nodedel <existing> must NOT remove the
// node — it validates the name and renders the K-B2-style confirm screen;
// an unknown name gets an honest error without any confirm screen.
func TestHandleNodeDelRequiresConfirm(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("x")
	b, _ := newCfgTestBot(t, api, []int64{1001}, mgr)

	msg := &telego.Message{MessageID: 7, Chat: telego.Chat{ID: 12345, Type: "private"}, Text: "/nodedel x"}
	b.handleNodeDel(msg, "x")

	if _, ok := mgr.nodes["x"]; !ok {
		t.Fatalf("/nodedel must NOT remove the node before the confirm tap")
	}
	sent := ft.callsOf("sendMessage")
	if len(sent) == 0 {
		t.Fatalf("expected the confirm screen to be sent")
	}
	text, _ := sent[0].Params["text"].(string)
	if !strings.Contains(text, "Точно удалить ноду") || !strings.Contains(text, "<code>x</code>") {
		t.Errorf("expected the confirm screen text, got: %s", text)
	}

	// Unknown name: error reply, no confirm screen.
	ft2, api2 := newFakeTelegramBot(t)
	mgr2 := newCfgRecordingManager("x")
	b2, _ := newCfgTestBot(t, api2, nil, mgr2)
	b2.handleNodeDel(msg, "ghost")
	sent2 := ft2.callsOf("sendMessage")
	if len(sent2) == 0 {
		t.Fatalf("expected an error reply for an unknown node")
	}
	text2, _ := sent2[0].Params["text"].(string)
	if strings.Contains(text2, "Точно удалить") {
		t.Errorf("unknown node must not get a confirm screen, got: %s", text2)
	}
	if !strings.Contains(text2, "не найдена") {
		t.Errorf("expected the not-found error, got: %s", text2)
	}
}

// TestNodeDelYesRemovesAndShowsList: the bare ndel tap renders the confirm
// screen (nothing removed); the explicit «:yes» callback removes the node
// through nodeMgr (only the target), edits to the removal notice with the
// nodes-list markup; a failed removal answers with the ❌ error alert toast.
func TestNodeDelYesRemovesAndShowsList(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("x", "keep")
	b, _ := newCfgTestBot(t, api, []int64{1001}, mgr)

	// 1. Bare tap: confirm screen only.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:ndel:x"))
	if _, ok := mgr.nodes["x"]; !ok {
		t.Fatalf("bare ndel tap must not remove the node")
	}
	if edits := ft.callsOf("editMessageText"); len(edits) != 1 {
		t.Fatalf("bare ndel tap must render the confirm screen, got %d edits", len(edits))
	}
	if text, _ := ft.callsOf("editMessageText")[0].Params["text"].(string); !strings.Contains(text, "Точно удалить ноду") {
		t.Errorf("bare tap must render the confirm text, got: %s", text)
	}

	// 2. :yes tap: removal + success screen.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:ndel:x:yes"))
	if _, ok := mgr.nodes["x"]; ok {
		t.Fatalf("expected RemoveNode after :yes")
	}
	if _, ok := mgr.nodes["keep"]; !ok {
		t.Fatalf("RemoveNode must touch only the target node")
	}
	edits := ft.callsOf("editMessageText")
	if len(edits) != 2 {
		t.Fatalf("expected the success edit after :yes, got %d edits", len(edits))
	}
	lastText, _ := edits[len(edits)-1].Params["text"].(string)
	if !strings.Contains(lastText, "✅ Нода <b>x</b> удалена с мастера.") {
		t.Errorf("expected the removal notice, got: %s", lastText)
	}

	// 3. Error path: unknown node on :yes → ❌ error alert toast.
	b.handleCallbackQuery(gateCallback(1001, "menu:nodes:ndel:ghost:yes"))
	if text, alert := toastParams(t, ft); !alert || !strings.HasPrefix(text, "❌") {
		t.Errorf("failed removal must answer with the ❌ error alert toast, got text=%q alert=%v", text, alert)
	}
}

// TestNodeDelCallbackGatedByAdmin: menu:nodes:ndel:<name>:yes is a mutating
// route — a non-admin is denied when admins are configured (node survives,
// denial toast, no screen edit).
func TestNodeDelCallbackGatedByAdmin(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	mgr := newCfgRecordingManager("x")
	b, _ := newCfgTestBot(t, api, []int64{1001}, mgr)

	b.handleCallbackQuery(gateCallback(9999, "menu:nodes:ndel:x:yes"))
	if _, ok := mgr.nodes["x"]; !ok {
		t.Fatalf("non-admin :yes must be denied")
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "только администраторам") {
		t.Errorf("expected admin-denial toast, got ok=%v toast=%q", ok, toast)
	}
	if edits := ft.callsOf("editMessageText"); len(edits) != 0 {
		t.Errorf("denied :yes must not edit any message, got %d edits", len(edits))
	}
}

// --- M6 (N-3): node picker with status icons ---

// TestNodesCfgListMarkupStatusIcons: each picker row is prefixed with the
// plain state circle — 🟢 up, 🔴 down, ⏸️ disabled (disabled wins over both)
// — and the omit-on-overflow discipline survives.
func TestNodesCfgListMarkupStatusIcons(t *testing.T) {
	m := NodesCfgListMarkup([]NodeInfo{
		{Name: "up1", Up: true},
		{Name: "down1"},
		{Name: "dis1", Up: true, Disabled: true},
		{Name: "downDis", Disabled: true},
	})
	wants := []string{"🟢 🖥 up1", "🔴 🖥 down1", "⏸️ 🖥 dis1", "⏸️ 🖥 downDis"}
	var got []string
	for _, row := range m.InlineKeyboard {
		got = append(got, row[0].Text)
	}
	for _, w := range wants {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("expected button %q in picker, got %v", w, got)
		}
	}
	if !hasCallback(m, "menu:nodes:cfg:up1") || !hasCallback(m, "menu:nodes:cfg:down1") || !hasCallback(m, "menu:nodes:cfg:dis1") {
		t.Errorf("per-node callbacks must survive, got %v", markupCallbacks(m))
	}

	// Long-name overflow still omitted: menu:nodes:cfg:<50 chars> is 65 bytes.
	long := strings.Repeat("n", 50)
	mLong := NodesCfgListMarkup([]NodeInfo{{Name: long, Up: true}})
	if hasCallback(mLong, "menu:nodes:cfg:"+long) {
		t.Errorf("over-limit node button must be omitted, got %v", markupCallbacks(mLong))
	}
}

// --- M7: ladder labels show current → next ---

// labelText returns the full text of the first button whose label contains
// the given prefix.
func labelText(m *telego.InlineKeyboardMarkup, prefix string) string {
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.Text, prefix) {
				return b.Text
			}
		}
	}
	return ""
}

// TestNodeCfgLadderLabelsShowNext pins the M7 label format «current → next»:
// the interval and method labels append the next ladder value, and the
// concurrency label uses concurrencyLabel on BOTH sides (0 renders
// «без лимита»).
func TestNodeCfgLadderLabelsShowNext(t *testing.T) {
	entries := []NodeSettingEntry{
		{Key: "check_method", Value: "ip"},
		{Key: "check_interval", Value: "60"},
		{Key: "check_concurrency", Value: "4"},
	}
	m := NodeCfgMarkup("m31a", true, entries)
	for _, want := range []string{
		"⏱ Интервал: 60с → 300с",
		"♻️ Метод: ip → status",
		"🧵 Параллелизм: 4 → 8",
	} {
		if got := labelText(m, want[:strings.IndexByte(want, ' ')+1]); got != want {
			t.Errorf("ladder label: got %q, want %q", got, want)
		}
	}

	// Wrap-around: the last rung of each ladder cycles to the first.
	wrap := []NodeSettingEntry{
		{Key: "check_method", Value: "download"},
		{Key: "check_interval", Value: "3600"},
		{Key: "check_concurrency", Value: "8"},
	}
	mWrap := NodeCfgMarkup("m31a", true, wrap)
	for _, want := range []string{
		"⏱ Интервал: 3600с → 60с",
		"♻️ Метод: download → ip",
		"🧵 Параллелизм: 8 → без лимита",
	} {
		if got := labelText(mWrap, want[:strings.IndexByte(want, ' ')+1]); got != want {
			t.Errorf("wrap-around ladder label: got %q, want %q", got, want)
		}
	}

	// Zero concurrency renders «без лимита» on the current side too.
	zero := []NodeSettingEntry{
		{Key: "check_method", Value: "ip"},
		{Key: "check_interval", Value: "300"},
		{Key: "check_concurrency", Value: "0"},
	}
	mZero := NodeCfgMarkup("m31a", true, zero)
	if got := labelText(mZero, "🧵 Параллелизм"); got != "🧵 Параллелизм: без лимита → 2" {
		t.Errorf("zero-concurrency ladder label: got %q", got)
	}
}
