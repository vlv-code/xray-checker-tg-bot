package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// fakeTelegramAPI intercepts Telegram Bot API calls from a real telego.Bot
// pointed at a local httptest server, recording every call for assertions.
type fakeTelegramAPI struct {
	mu        sync.Mutex
	calls     []fakeAPICall
	nextMsgID int
}

type fakeAPICall struct {
	Method string
	Params map[string]interface{}
}

func newFakeTelegramBot(t *testing.T) (*fakeTelegramAPI, *telego.Bot) {
	t.Helper()
	ft := &fakeTelegramAPI{nextMsgID: 100}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var params map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&params)

		ft.mu.Lock()
		ft.calls = append(ft.calls, fakeAPICall{Method: method, Params: params})
		ft.nextMsgID++
		msgID := ft.nextMsgID
		ft.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": true,
			"result": map[string]interface{}{
				"message_id": msgID,
				"date":       1700000000,
				"chat":       map[string]interface{}{"id": 12345, "type": "private"},
			},
		})
	}))
	t.Cleanup(ts.Close)

	api, err := telego.NewBot("12345:"+strings.Repeat("t", 35),
		telego.WithAPIServer(ts.URL),
		telego.WithHTTPClient(ts.Client()),
		telego.WithDiscardLogger(),
	)
	if err != nil {
		t.Fatalf("failed to create fake telego bot: %v", err)
	}
	return ft, api
}

// callsOf returns all recorded calls of the given Bot API method.
func (f *fakeTelegramAPI) callsOf(method string) []fakeAPICall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeAPICall, 0, len(f.calls))
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// lastParamOf returns the value of the string parameter `key` from the most
// recent recorded call of `method`.
func (f *fakeTelegramAPI) lastParamOf(method, key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		c := f.calls[i]
		if c.Method != method {
			continue
		}
		if v, ok := c.Params[key].(string); ok {
			return v, true
		}
	}
	return "", false
}

// newGateTestBot builds a Bot wired to the fake Telegram API with a single
// allowed chat 12345 and one proxy (StableID "proxy-x") in the snapshot.
func newGateTestBot(t *testing.T, api *telego.Bot, adminIDs []int64) (*Bot, *ConfigManager) {
	t.Helper()
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{CheckIntervalSec: 300})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	b := &Bot{
		api:            api,
		ctx:            context.Background(),
		targets:        []ChatTarget{{ChatID: 12345}},
		allowedChatIDs: map[int64]bool{12345: true},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Proxy-X", StableID: "proxy-x", Address: "1.1.1.1:443", Protocol: "vless", Online: true},
		}},
		configMgr:   cm,
		tracker:     NewAlertTracker(),
		eventBuffer: NewEventBuffer(),
		lastSeen:    make(map[string]bool),
	}
	b.SetAdminUserIDs(adminIDs)
	return b, cm
}

func gateCallback(userID int64, data string) *telego.CallbackQuery {
	return &telego.CallbackQuery{
		ID:   "cbq-test",
		From: telego.User{ID: userID, Username: "user"},
		Message: &telego.Message{
			MessageID: 10,
			Chat:      telego.Chat{ID: 12345, Type: "private"},
		},
		Data: data,
	}
}

// --- FIX 1: admin gate for mutating callbacks ---

// TestIsMutatingCallback_Classification locks the full route classification:
// every mutating route must be gated, every informational route must stay open.
func TestIsMutatingCallback_Classification(t *testing.T) {
	mutating := []string{
		// config-persisting exact matches
		"menu:quiet:toggle",
		"menu:quiet:snooze:1h",
		"menu:quiet:snooze:4h",
		"menu:quiet:snooze:morning",
		"menu:quiet:unsnooze",
		"menu:alert_mode:live",
		"menu:alert_mode:clean",
		"menu:checkhost:toggle_bg",
		"menu:checkhost:toggle_alert",
		"menu:checkhost:run_now",
		"menu:checkupdate",
		"menu:nodes:toggle_sync",
		"menu:nodes:toggle_alerts",
		"menu:nodes:toggle_proxy_alerts",
		// Wave H6: the custom Check-Host host add/del routes persist config.
		"menu:checkhostadd",
		"menu:checkhostdel:9.9.9.9:443",
		// mutating prefix routes (with payloads)
		"menu:nodes:delsub:node-1:0",
		"menu:nodes:nx:node-1:0:1", // Wave H1: per-proxy exclusion toggle
		"menu:tz:Europe/Moscow",
		"menu:checkhost:int:2",
		"menu:toggle_proxy:proxy-x:1",
		"menu:toggle_host:node1.example.com:1",
		"menu:interval:set:60",
	}
	for _, data := range mutating {
		if !isMutatingCallback(data) {
			t.Errorf("expected isMutatingCallback(%q) = true", data)
		}
	}

	informational := []string{
		"menu:main", "menu:settings", "menu:status",
		"menu:diag", "menu:diag:summary", "menu:diag:rich",
		"menu:stats", "menu:stats:incidents", "menu:stats:top",
		"menu:stats:protocols", "menu:stats:heatmap",
		"menu:timezone", "menu:quiet", "menu:alert_mode",
		"menu:targets", "menu:interval", "menu:subs",
		"menu:checkhost", "menu:checkhost_cfg",
		"menu:nodes", "menu:nodes:refresh", "menu:nodes:add",
		"menu:nodes:install", "menu:nodes:health",
		"menu:nodes:settings",
		// Wave H1/H2 register: the removed global subs screen entry
		// menu:nodes:subs left the informational list; the exclusions view +
		// pager routes (functional screen) stay open to non-admins.
		"menu:nodes:cfg:node-1", "menu:nodes:excl:node-1", "menu:nodes:exclpg:node-1:2",
		"menu:noop", "menu:diag:noop:2:3",
		// prefix routes that only display views or set ephemeral per-chat prompts
		"menu:nodes:subnode:node-1",
		"menu:nodes:addsub:node-1",
		"menu:disabled_proxies:2", "menu:disabled_hosts:2",
		"menu:diag:node:local", "menu:diag:details:nodes:2",
		"menu:stats:incidents:all", "menu:diag:pick_deep:nodes:2",
		"menu:diag:deep:proxy-x:1", "menu:diag:refresh:summary",
		// Wave H6 register: the per-host Check-Host check and pager stay
		// informational (the former per-proxy entry menu:checkhost:run:proxy-x
		// left the list together with its removed route).
		"menu:checkhosth:1.1.1.1:443", "menu:checkhostpg:2",
	}
	for _, data := range informational {
		if isMutatingCallback(data) {
			t.Errorf("expected isMutatingCallback(%q) = false (informational route)", data)
		}
	}

	// Guard against prefix collisions: the view routes must not be swallowed
	// by the mutating prefixes.
	if isMutatingCallback("menu:timezone") || isMutatingCallback("menu:alert_mode") ||
		isMutatingCallback("menu:quiet") || isMutatingCallback("menu:interval") {
		t.Errorf("view routes for tz/alert_mode/quiet/interval must stay informational")
	}
}

func TestCallbackGate_NonAdminMutatingCallbackDenied(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, cm := newGateTestBot(t, api, []int64{1001})

	b.handleCallbackQuery(gateCallback(9999, "menu:toggle_proxy:proxy-x:1"))

	if cm.Get().IsProxyDisabled("proxy-x") {
		t.Errorf("non-admin callback must not toggle the proxy, but proxy-x got disabled")
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "только администраторам") {
		t.Errorf("expected admin-denial toast, got ok=%v toast=%q", ok, toast)
	}
	if edits := ft.callsOf("editMessageText"); len(edits) != 0 {
		t.Errorf("denied mutating callback must not edit any message, got %d edits", len(edits))
	}
}

func TestCallbackGate_AdminMutatingCallbackAllowed(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, cm := newGateTestBot(t, api, []int64{1001})

	b.handleCallbackQuery(gateCallback(1001, "menu:toggle_proxy:proxy-x:1"))

	if !cm.Get().IsProxyDisabled("proxy-x") {
		t.Fatalf("admin callback must toggle the proxy, but proxy-x stayed enabled")
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "выключен") {
		t.Errorf("expected toggle toast for admin, got ok=%v toast=%q", ok, toast)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); ok && strings.Contains(toast, "только администраторам") {
		t.Errorf("admin must not receive the denial toast, got %q", toast)
	}
}

func TestCallbackGate_InformationalCallbackOpenToNonAdmin(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b, _ := newGateTestBot(t, api, []int64{1001})

	b.handleCallbackQuery(gateCallback(9999, "menu:status"))

	if edits := ft.callsOf("editMessageText"); len(edits) == 0 {
		t.Errorf("informational callback (menu:status) must still work for non-admin, no edits recorded")
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); ok && strings.Contains(toast, "только администраторам") {
		t.Errorf("informational callback must not be admin-gated, got toast %q", toast)
	}
}

func TestCallbackGate_EmptyAdminListBackwardCompat(t *testing.T) {
	_, api := newFakeTelegramBot(t)
	b, cm := newGateTestBot(t, api, nil) // empty admin list: everyone authorized

	b.handleCallbackQuery(gateCallback(9999, "menu:toggle_proxy:proxy-x:1"))

	if !cm.Get().IsProxyDisabled("proxy-x") {
		t.Errorf("with empty admin list a mutating callback must work for non-admins (backward compat)")
	}
}

// --- FIX 2: dead "Запустить проверку сейчас" button ---

func TestCallbackRunNow_TriggersAuditDespiteBgDisabled(t *testing.T) {
	ft, api := newFakeTelegramBot(t)

	// Fake Check-Host API: RU node always times out, world node succeeds ->
	// RU❌+World✅ signature -> RU-block alert fires (see F4 alert-gate change).
	chTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "audit1",
				"permanent_link": "https://check-host.net/check-report/audit1",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "1.2.3.4", "AS1"},
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/audit1") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ru2.node.check-host.net": []interface{}{map[string]interface{}{"error": "Connection timed out"}},
				"de1.node.check-host.net": []interface{}{map[string]interface{}{"time": 0.02, "address": "1.1.1.1"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer chTS.Close()

	chClient := checker.NewCheckHostClient(chTS.URL, 10*time.Millisecond)
	chClient.HTTPClient = chTS.Client()

	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{
		CheckIntervalSec:      300,
		CheckHostBgEnabled:    false, // background audit disabled: explicit run must still work
		CheckHostAlertEnabled: true,
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	tracker := NewAlertTracker()
	b := &Bot{
		api:            api,
		ctx:            context.Background(),
		targets:        []ChatTarget{{ChatID: 12345}},
		allowedChatIDs: map[int64]bool{12345: true},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Proxy-X", StableID: "proxy-x", Address: "1.1.1.1:443", Protocol: "vless", Online: true},
		}},
		configMgr:       cm,
		tracker:         tracker,
		eventBuffer:     NewEventBuffer(),
		checkHostClient: chClient,
		checkHostNodes:  []string{"ru2.node.check-host.net"},
	}
	// Empty admin list: gate must not interfere with this FIX 2 test.

	b.handleCallbackQuery(gateCallback(9999, "menu:checkhost:run_now"))

	alertKey := "checkhost:1.1.1.1:443"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !tracker.HasAlert(12345, 0, alertKey) {
		time.Sleep(20 * time.Millisecond)
	}
	if !tracker.HasAlert(12345, 0, alertKey) {
		t.Fatalf("menu:checkhost:run_now must trigger the audit even when CheckHostBgEnabled=false (no alert for %s)", alertKey)
	}
	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "Запуск фоновой проверки") {
		t.Errorf("expected run_now toast, got ok=%v toast=%q", ok, toast)
	}
}

// TestRunCheckHostAudit_BgGateAndExplicitBypass verifies the seam: the
// scheduler entry point stays behind the CheckHostBgEnabled gate while the
// explicit runAuditNow (used by the run_now button) bypasses it.
func TestRunCheckHostAudit_BgGateAndExplicitBypass(t *testing.T) {
	chTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "audit1",
				"permanent_link": "https://check-host.net/check-report/audit1",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "1.2.3.4", "AS1"},
					// world node succeeds so the summary keeps the RU-block signature (RU❌+World✅) after the F4 alert-gate change
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/audit1") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ru2.node.check-host.net": []interface{}{map[string]interface{}{"error": "Connection timed out"}},
				"de1.node.check-host.net": []interface{}{map[string]interface{}{"time": 0.02, "address": "1.1.1.1"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer chTS.Close()

	chClient := checker.NewCheckHostClient(chTS.URL, 10*time.Millisecond)
	chClient.HTTPClient = chTS.Client()

	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{
		CheckIntervalSec:      300,
		CheckHostBgEnabled:    false,
		CheckHostAlertEnabled: true,
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	alertKey := "checkhost:1.1.1.1:443"

	// 1. Scheduler entry: bg disabled -> no audit.
	tracker := NewAlertTracker()
	b := &Bot{
		ctx:     context.Background(),
		targets: []ChatTarget{{ChatID: 12345}},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Proxy-X", StableID: "proxy-x", Address: "1.1.1.1:443", Protocol: "vless", Online: true},
		}},
		configMgr:       cm,
		tracker:         tracker,
		eventBuffer:     NewEventBuffer(),
		checkHostClient: chClient,
		checkHostNodes:  []string{"ru2.node.check-host.net"},
	}
	b.RunCheckHostAudit()
	if tracker.HasAlert(12345, 0, alertKey) {
		t.Fatalf("RunCheckHostAudit must stay gated by CheckHostBgEnabled=false")
	}

	// 2. Explicit entry: bypasses the gate -> audit performed.
	b.runAuditNow()
	if !tracker.HasAlert(12345, 0, alertKey) {
		t.Fatalf("runAuditNow must perform the audit despite CheckHostBgEnabled=false")
	}
}
