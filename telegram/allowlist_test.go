package telegram

import (
	"testing"

	"github.com/mymmrac/telego"
)

// Security invariant (AGENTS.md §1): "Telegram chat allowlist enforcement".
// These tests are REGRESSION PINS, not RED→GREEN pairs: the enforcement
// already exists in handleMessage (commands.go) and handleCallbackQuery
// (callbacks.go) and was verified in the audit, so they are expected to PASS
// immediately. Their purpose is to fail loudly if the allowlist gate is ever
// weakened or removed by a future change.

func allowlistUnauthorizedMessage(chatID int64, text string) *telego.Message {
	return &telego.Message{
		MessageID: 77,
		Chat:      telego.Chat{ID: chatID, Type: "private"},
		From:      &telego.User{ID: 4242, Username: "intruder"},
		Text:      text,
	}
}

func allowlistUnauthorizedCallback(chatID int64, data string) *telego.CallbackQuery {
	return &telego.CallbackQuery{
		ID:   "cbq-allowlist",
		From: telego.User{ID: 4242, Username: "intruder"},
		Message: &telego.Message{
			MessageID: 10,
			Chat:      telego.Chat{ID: chatID, Type: "private"},
		},
		Data: data,
	}
}

// TestAllowlist_MessageFromUnauthorizedChatIgnored: a command message from a
// chat that is not on the allowlist must produce NO reply and NO side effects
// — not even the mutating-command denial reply.
func TestAllowlist_MessageFromUnauthorizedChatIgnored(t *testing.T) {
	for _, text := range []string{
		"/status",          // informational command
		"/nodeadd intrude", // mutating command
	} {
		ft, api := newFakeTelegramBot(t)
		b, _ := newGateTestBot(t, api, nil) // empty admin list: only the chat allowlist matters here

		b.handleMessage(allowlistUnauthorizedMessage(9999, text))

		if calls := ft.callsOf("sendMessage"); len(calls) != 0 {
			t.Errorf("message %q from non-allowed chat must be ignored silently, got %d sendMessage calls", text, len(calls))
		}
		if calls := ft.callsOf("editMessageText"); len(calls) != 0 {
			t.Errorf("message %q from non-allowed chat must not edit any message, got %d editMessageText calls", text, len(calls))
		}
	}
}

// TestAllowlist_CallbackFromUnauthorizedChatIgnored: a callback query whose
// message lives in a non-allowed chat must produce NO API traffic at all
// (no edit, not even the spinner-acknowledgement answerCallbackQuery).
func TestAllowlist_CallbackFromUnauthorizedChatIgnored(t *testing.T) {
	for _, data := range []string{
		"menu:status",            // informational route
		"menu:toggle_proxy:p1:1", // mutating route
	} {
		ft, api := newFakeTelegramBot(t)
		b, _ := newGateTestBot(t, api, nil)

		b.handleCallbackQuery(allowlistUnauthorizedCallback(9999, data))

		if calls := ft.callsOf("editMessageText"); len(calls) != 0 {
			t.Errorf("callback %q from non-allowed chat must not edit any message, got %d editMessageText calls", data, len(calls))
		}
		if calls := ft.callsOf("answerCallbackQuery"); len(calls) != 0 {
			t.Errorf("callback %q from non-allowed chat must not even be acknowledged, got %d answerCallbackQuery calls", data, len(calls))
		}
	}
}

// TestAllowlist_MessageFromAllowedChatProcessed: positive control proving the
// harness wiring works — the identical command from the allowed chat must
// reach the Telegram API.
func TestAllowlist_MessageFromAllowedChatProcessed(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)

	b.handleMessage(allowlistUnauthorizedMessage(12345, "/status"))

	if calls := ft.callsOf("sendMessage"); len(calls) == 0 {
		t.Errorf("positive control: /status from the allowed chat 12345 must produce a sendMessage, got none")
	}
}

// TestAllowlist_CallbackFromAllowedChatProcessed: positive control for the
// callback path — the identical route from the allowed chat must edit the
// message.
func TestAllowlist_CallbackFromAllowedChatProcessed(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, nil)

	b.handleCallbackQuery(gateCallback(9999, "menu:status"))

	if calls := ft.callsOf("editMessageText"); len(calls) == 0 {
		t.Errorf("positive control: menu:status callback in the allowed chat 12345 must edit the message, got none")
	}
}
