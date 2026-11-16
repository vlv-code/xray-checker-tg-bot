package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	telegoapi "github.com/mymmrac/telego/telegoapi"
)

// --- Fake Telegram Bot API with scripted 429 sequences ----------------------
//
// rateFakeAPI follows the robFakeAPI pattern (bot_robustness_test.go): an
// httptest-backed fake of the Telegram Bot API. Each method consults a script
// of responses; every entry is either "ok" or "429:<retry_after>" and is
// consumed once, so tests can pin exact attempt counts.

type rateFakeAPI struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  map[string]int
	script map[string][]string // method -> pending responses, "ok" after exhaustion
}

func (f *rateFakeAPI) record(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[method]++
	return f.calls[method]
}

func (f *rateFakeAPI) callsOf(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *rateFakeAPI) respond(method string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	script := f.script[method]
	if len(script) == 0 {
		return `{"ok":true,"result":{"message_id":555,"date":1700000000,"chat":{"id":100,"type":"private"}}}`
	}
	next := script[0]
	f.script[method] = script[1:]
	switch {
	case next == "ok":
		return `{"ok":true,"result":{"message_id":555,"date":1700000000,"chat":{"id":100,"type":"private"}}}`
	case strings.HasPrefix(next, "429:"):
		retryAfter := strings.TrimPrefix(next, "429:")
		return `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after ` + retryAfter + `","parameters":{"retry_after":` + retryAfter + `}}`
	default:
		return next
	}
}

func newRateFakeAPI(t *testing.T, script map[string][]string) (*rateFakeAPI, *telego.Bot) {
	t.Helper()
	f := &rateFakeAPI{calls: make(map[string]int), script: script}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		_, _ = io.Copy(io.Discard, r.Body)
		f.record(method)
		body := f.respond(method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
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

// --- FIX: 429 flood-control retry -------------------------------------------

// TestBot_Send_RetriesOnceOn429AndDelivers (contract a): the first sendMessage
// answers 429 with retry_after=1, the second succeeds. The message must be
// delivered exactly once and the API must see exactly 2 attempts.
func TestBot_Send_RetriesOnceOn429AndDelivers(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"sendMessage": {"429:1", "ok"},
	})

	b := &Bot{api: api, ctx: context.Background(), targets: []ChatTarget{{ChatID: 100}}}

	b.send(ChatTarget{ChatID: 100}, "hello")

	if got := fake.callsOf("sendMessage"); got != 2 {
		t.Fatalf("expected exactly 2 sendMessage attempts after one 429, got %d", got)
	}
}

// TestBot_SendAndReturn_RetriesOnceOn429 (contract a, returning helper): the
// message is delivered and no error surfaces to the caller after a successful
// retry.
func TestBot_SendAndReturn_RetriesOnceOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"sendMessage": {"429:1", "ok"},
	})

	b := &Bot{api: api, ctx: context.Background()}

	sent, err := b.sendAndReturn(ChatTarget{ChatID: 100}, "hello")
	if err != nil {
		t.Fatalf("expected send to succeed after retry, got error: %v", err)
	}
	if sent == nil || sent.GetMessageID() != 555 {
		t.Fatalf("expected delivered message id 555, got %+v", sent)
	}
	if got := fake.callsOf("sendMessage"); got != 2 {
		t.Fatalf("expected exactly 2 sendMessage attempts, got %d", got)
	}
}

// TestBot_Send_429WaitCapped (contract b): a retry_after of 30 seconds must be
// capped by the max-wait seam, so the call finishes long before 6s and still
// retries successfully.
func TestBot_Send_429WaitCapped(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"sendMessage": {"429:30", "ok"},
	})

	b := &Bot{api: api, ctx: context.Background()}
	b.rateLimitMaxWait = 200 * time.Millisecond // small seam; production default is 10s

	start := time.Now()
	b.send(ChatTarget{ChatID: 100}, "hello")
	elapsed := time.Since(start)

	if elapsed >= 6*time.Second {
		t.Fatalf("429 retry wait must be capped, took %s (uncapped retry_after=30s would exceed 6s)", elapsed)
	}
	if got := fake.callsOf("sendMessage"); got != 2 {
		t.Fatalf("expected exactly 2 sendMessage attempts, got %d", got)
	}
}

// TestBot_Send_Non429ErrorNotRetried (contract c): a plain 400 API error must
// not trigger any retry — exactly one attempt.
func TestBot_Send_Non429ErrorNotRetried(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"sendMessage": {`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`},
	})

	b := &Bot{api: api, ctx: context.Background()}

	_, err := b.sendAndReturn(ChatTarget{ChatID: 100}, "hello")
	if err == nil {
		t.Fatalf("expected non-429 error to surface to the caller")
	}
	if got := fake.callsOf("sendMessage"); got != 1 {
		t.Fatalf("non-429 error must not be retried, got %d attempts", got)
	}
}

// TestBot_Send_GivesUpAfterTwoRetries (contract d): three consecutive 429
// answers exhaust the 2 retries; the helper makes exactly 3 attempts and the
// returning helper surfaces the error per existing semantics.
func TestBot_Send_GivesUpAfterTwoRetries(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"sendMessage": {"429:1", "429:1", "429:1"},
	})

	b := &Bot{api: api, ctx: context.Background()}
	b.rateLimitMaxWait = 50 * time.Millisecond // keep the test fast

	_, err := b.sendAndReturn(ChatTarget{ChatID: 100}, "hello")
	if err == nil {
		t.Fatalf("expected error after exhausting 2 retries")
	}
	if got := fake.callsOf("sendMessage"); got != 3 {
		t.Fatalf("expected exactly 3 attempts (initial + 2 retries), got %d", got)
	}
}

// TestBot_EditMessageText_RetriesOn429: the central edit helper must retry
// after a 429 too — menus are edited constantly and a flood limit must not
// silently lose the edit.
func TestBot_EditMessageText_RetriesOn429(t *testing.T) {
	fake, api := newRateFakeAPI(t, map[string][]string{
		"editMessageText": {"429:1", "ok"},
	})

	b := &Bot{api: api, ctx: context.Background()}
	b.rateLimitMaxWait = 100 * time.Millisecond

	b.edit(100, 5, "updated text")

	if got := fake.callsOf("editMessageText"); got != 2 {
		t.Fatalf("expected exactly 2 editMessageText attempts after one 429, got %d", got)
	}
}

// TestBot_RateLimitWait_Extraction pins the telego error contract: a wrapped
// *telegoapi.Error with ErrorCode 429 yields the retry delay (capped), a
// missing/invalid retry_after yields the 3s default, and non-429 errors are
// not retryable.
func TestBot_RateLimitWait_Extraction(t *testing.T) {
	apiErr := &telegoapi.Error{
		ErrorCode:   429,
		Description: "Too Many Requests",
		Parameters:  &telegoapi.ResponseParameters{RetryAfter: 30},
	}
	wrapped := fmt.Errorf("telego: sendMessage: %w", fmt.Errorf("api: %w", apiErr))

	wait, retryable := rateLimitWait(wrapped)
	if !retryable {
		t.Fatalf("429 must be retryable")
	}
	if wait != 10*time.Second {
		t.Fatalf("retry_after=30 must be capped at 10s, got %s", wait)
	}

	apiErrDefault := &telegoapi.Error{ErrorCode: 429, Parameters: &telegoapi.ResponseParameters{RetryAfter: 0}}
	wait, retryable = rateLimitWait(fmt.Errorf("api: %w", apiErrDefault))
	if !retryable || wait != 3*time.Second {
		t.Fatalf("retry_after<=0 must fall back to 3s, got retryable=%v wait=%s", retryable, wait)
	}

	badStatus := &telegoapi.Error{ErrorCode: 400, Description: "Bad Request: chat not found"}
	if _, retryable := rateLimitWait(fmt.Errorf("api: %w", badStatus)); retryable {
		t.Fatalf("400 must not be retryable")
	}

	if _, retryable := rateLimitWait(context.DeadlineExceeded); retryable {
		t.Fatalf("non-API error must not be retryable")
	}
}

// --- FIX: digest rendering of dropped / snooze-buffered events --------------

// TestBot_MorningDigest_MentionsDroppedEvents: when the event buffer had to
// drop the oldest events, the morning digest must say so instead of silently
// losing incident history.
func TestBot_MorningDigest_MentionsDroppedEvents(t *testing.T) {
	fake, api := newRobFakeAPI(t)

	b := &Bot{
		api:         api,
		ctx:         context.Background(),
		source:      &mockSource{},
		targets:     []ChatTarget{{ChatID: 100}},
		eventBuffer: NewEventBuffer(),
	}

	for i := 0; i < maxBufferedEvents+3; i++ {
		b.eventBuffer.Add(BufferedEvent{Timestamp: time.Now(), Type: "down", ProxyName: fmt.Sprintf("p%d", i), Reason: "timeout"})
	}

	b.sendMorningDigest(time.Now())

	sends := fake.robCallsOf("sendMessage")
	if len(sends) == 0 {
		t.Fatalf("expected the digest to be sent")
	}
	// 503 event lines exceed one message: the digest is delivered in chunks.
	var text strings.Builder
	for _, s := range sends {
		chunk, _ := s.Body["text"].(string)
		text.WriteString(chunk)
	}
	if !strings.Contains(text.String(), "…и ещё 3 более старых событий") {
		t.Fatalf("digest must mention the 3 dropped events, got: %s", text.String())
	}
	if !strings.Contains(text.String(), "Инциденты за ночь") {
		t.Fatalf("digest must list buffered incidents, got: %s", text.String())
	}
}

// TestBot_DaytimeDigest_DrainsBufferedEvents: events buffered while a snooze
// kept quiet hours active must be delivered with the daytime digest instead of
// waiting for the next morning, and the buffer must be empty afterwards.
func TestBot_DaytimeDigest_DrainsBufferedEvents(t *testing.T) {
	fake, api := newRobFakeAPI(t)

	b := &Bot{
		api:         api,
		ctx:         context.Background(),
		source:      &mockSource{},
		targets:     []ChatTarget{{ChatID: 100}},
		eventBuffer: NewEventBuffer(),
	}

	b.eventBuffer.Add(BufferedEvent{Timestamp: time.Now(), Type: "down", ProxyName: "Proxy A", Reason: "timeout"})
	b.eventBuffer.Add(BufferedEvent{Timestamp: time.Now(), Type: "up", ProxyName: "Proxy A", Downtime: 5 * time.Minute})

	b.sendDaytimeDigest(time.Now())

	sends := fake.robCallsOf("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("expected exactly 1 digest message, got %d", len(sends))
	}
	text, _ := sends[0].Body["text"].(string)
	if !strings.Contains(text, "Инциденты с последней сводки") {
		t.Fatalf("daytime digest must carry buffered incidents, got: %s", text)
	}
	if !strings.Contains(text, "Proxy A") {
		t.Fatalf("daytime digest must name the affected proxy, got: %s", text)
	}
	if rest := b.eventBuffer.Drain(); len(rest) != 0 {
		t.Fatalf("daytime digest must drain the event buffer, %d events left", len(rest))
	}
}
