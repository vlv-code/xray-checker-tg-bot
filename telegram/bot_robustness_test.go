package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mymmrac/telego"
)

// robRunCapturingPanic runs fn and reports whether it panicked, so tests can
// assert about panics without crashing the whole test binary.
func robRunCapturingPanic(fn func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	fn()
	return false
}

// robTypedNilCallbackUpdate returns an update whose callback carries a typed
// nil message — a payload Telegram really sends for callbacks attached to
// deleted or inaccessible messages. The pre-fix dispatch dereferenced it
// unguarded and panicked.
func robTypedNilCallbackUpdate() telego.Update {
	var inaccessible *telego.Message
	return telego.Update{CallbackQuery: &telego.CallbackQuery{ID: "cb-panic", Message: inaccessible}}
}

// TestBot_RawUpdateDispatch_PanicPropagates documents the pre-fix defect
// (FIX 1): the update-consumer loop body dispatched handlers directly, so a
// panic in any handler propagated out of the polling goroutine and killed the
// whole process. It pins the raw path as RED evidence for the defect.
func TestBot_RawUpdateDispatch_PanicPropagates(t *testing.T) {
	b := &Bot{allowedChatIDs: map[int64]bool{100: true}}

	u := robTypedNilCallbackUpdate()

	panicked := robRunCapturingPanic(func() {
		// Exact copy of the pre-fix dispatch loop body (bot.go, StartCommands).
		if u.Message != nil {
			b.handleMessage(u.Message)
		} else if u.CallbackQuery != nil {
			b.handleCallbackQuery(u.CallbackQuery)
		}
	})

	if !panicked {
		t.Fatalf("expected raw dispatch loop body to panic on a callback with typed-nil message (defect FIX 1); it returned normally")
	}
}

// TestBot_HandleUpdateSafely_RecoversHandlerPanic is the GREEN specification
// for FIX 1: every update dispatch is wrapped so a handler panic is converted
// into a log record and the polling loop keeps running.
func TestBot_HandleUpdateSafely_RecoversHandlerPanic(t *testing.T) {
	b := &Bot{allowedChatIDs: map[int64]bool{100: true}}

	// Mechanism: runUpdateSafely must swallow a handler panic.
	if panicked := robRunCapturingPanic(func() {
		b.runUpdateSafely(func() { panic("boom: handler panic") })
	}); panicked {
		t.Fatalf("runUpdateSafely must recover panics raised by handlers")
	}

	// End-to-end through the real dispatch: the malformed callback must not
	// propagate a panic out of the safe wrapper.
	if panicked := robRunCapturingPanic(func() {
		b.handleUpdateSafely(robTypedNilCallbackUpdate())
	}); panicked {
		t.Fatalf("handleUpdateSafely must recover panics from dispatching a malformed callback")
	}

	// A benign message update dispatches normally.
	if panicked := robRunCapturingPanic(func() {
		b.handleUpdateSafely(telego.Update{Message: &telego.Message{}})
	}); panicked {
		t.Fatalf("handleUpdateSafely must not panic on a benign message update")
	}
}

// --- Fake Telegram Bot API -------------------------------------------------

// robFakeAPICall records one Telegram API call made through the fake server.
type robFakeAPICall struct {
	Method string
	Body   map[string]any
}

// robIntParam extracts a numeric parameter from the recorded request body.
func (c robFakeAPICall) robIntParam(key string) (int, bool) {
	v, ok := c.Body[key]
	if !ok {
		return 0, false
	}
	f, isNum := v.(float64)
	if !isNum {
		return 0, false
	}
	return int(f), true
}

// robFakeAPI is an httptest-backed fake of the Telegram Bot API: it records
// every call and can be told to fail specific methods.
type robFakeAPI struct {
	server      *httptest.Server
	mu          sync.Mutex
	calls       []robFakeAPICall
	failSend    bool // sendMessage returns an API error
	editTooLong bool // editMessageText returns MESSAGE_TOO_LONG
}

func (f *robFakeAPI) robRecord(method string, body map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, robFakeAPICall{Method: method, Body: body})
}

func (f *robFakeAPI) robCallsOf(method string) []robFakeAPICall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []robFakeAPICall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *robFakeAPI) robRespond(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// newRobFakeAPI builds a *telego.Bot pointed at the fake server.
func newRobFakeAPI(t *testing.T) (*robFakeAPI, *telego.Bot) {
	t.Helper()
	f := &robFakeAPI{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var body map[string]any
		raw, err := io.ReadAll(r.Body)
		if err == nil {
			_ = json.Unmarshal(raw, &body)
		}
		f.robRecord(method, body)

		switch method {
		case "sendMessage":
			if f.failSend {
				f.robRespond(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
				return
			}
			f.robRespond(w, `{"ok":true,"result":{"message_id":555,"date":1700000000,"chat":{"id":100,"type":"private"}}}`)
		case "deleteMessage":
			f.robRespond(w, `{"ok":true,"result":true}`)
		case "editMessageText":
			if f.editTooLong {
				f.robRespond(w, `{"ok":false,"error_code":400,"description":"Bad Request: MESSAGE_TOO_LONG"}`)
				return
			}
			f.robRespond(w, `{"ok":true,"result":{"message_id":5,"date":1700000000,"chat":{"id":100,"type":"private"}}}`)
		default:
			f.robRespond(w, `{"ok":true,"result":true}`)
		}
	}))
	t.Cleanup(f.server.Close)

	api, err := telego.NewBot("123456789:"+strings.Repeat("A", 35),
		telego.WithAPIServer(f.server.URL),
		telego.WithHTTPClient(f.server.Client()),
		telego.WithDiscardLogger(),
	)
	if err != nil {
		t.Fatalf("failed to build fake telego bot: %v", err)
	}
	return f, api
}

// --- FIX 3: sendOrUpdateMenu must send first, delete after ------------------

// TestBot_SendOrUpdateMenu_SendFailureKeepsOldMenu (FIX 3): when sending the
// new menu fails, the old menu message must NOT be deleted and its cached id
// must stay untouched — the user never loses the menu.
func TestBot_SendOrUpdateMenu_SendFailureKeepsOldMenu(t *testing.T) {
	fake, api := newRobFakeAPI(t)
	fake.failSend = true

	b := &Bot{
		api:         api,
		ctx:         context.Background(),
		lastMenuMsg: map[string]int{"100": 42},
	}

	b.sendOrUpdateMenu(ChatTarget{ChatID: 100}, "menu text", nil)

	if got := len(fake.robCallsOf("deleteMessage")); got != 0 {
		t.Errorf("deleteMessage must never be called when sending the new menu fails, got %d calls", got)
	}
	if id := b.lastMenuMsg["100"]; id != 42 {
		t.Errorf("lastMenuMsg must keep the old id 42 on send failure, got %d", id)
	}
	if got := len(fake.robCallsOf("sendMessage")); got != 1 {
		t.Errorf("expected exactly one sendMessage attempt, got %d", got)
	}
}

// TestBot_SendOrUpdateMenu_SuccessReplacesOldMenu (FIX 3): on a successful
// send the old menu is deleted exactly once and the cached id is updated.
func TestBot_SendOrUpdateMenu_SuccessReplacesOldMenu(t *testing.T) {
	fake, api := newRobFakeAPI(t)

	b := &Bot{
		api:         api,
		ctx:         context.Background(),
		lastMenuMsg: map[string]int{"100": 42},
	}

	b.sendOrUpdateMenu(ChatTarget{ChatID: 100}, "menu text", nil)

	dels := fake.robCallsOf("deleteMessage")
	if len(dels) != 1 {
		t.Fatalf("expected exactly one deleteMessage after successful send, got %d", len(dels))
	}
	if id, ok := dels[0].robIntParam("message_id"); !ok || id != 42 {
		t.Errorf("expected old menu id 42 to be deleted, got %d (present=%v)", id, ok)
	}
	if id := b.lastMenuMsg["100"]; id != 555 {
		t.Errorf("lastMenuMsg must be updated to the new id 555, got %d", id)
	}
}

// --- FIX 4: MESSAGE_TOO_LONG fallback must keep the forum topic -------------

// TestBot_EditMarkup_TooLongFallbackKeepsThread (FIX 4): the overflow
// fallback re-send must carry the chat's configured forum topic instead of
// dropping into the general chat.
func TestBot_EditMarkup_TooLongFallbackKeepsThread(t *testing.T) {
	fake, api := newRobFakeAPI(t)
	fake.editTooLong = true

	b := &Bot{
		api:     api,
		ctx:     context.Background(),
		targets: []ChatTarget{{ChatID: 100, ThreadID: 77}},
	}

	b.editWithMarkup(100, 5, "too long text", nil)

	sends := fake.robCallsOf("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("expected exactly one fallback sendMessage, got %d", len(sends))
	}
	if threadID, ok := sends[0].robIntParam("message_thread_id"); !ok || threadID != 77 {
		t.Errorf("fallback send must carry message_thread_id 77, got %d (present=%v)", threadID, ok)
	}
	if chatID, ok := sends[0].robIntParam("chat_id"); !ok || chatID != 100 {
		t.Errorf("fallback send must target chat 100, got %d (present=%v)", chatID, ok)
	}
}
