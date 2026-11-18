package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// Wave M3 (U-8): an explicit user-triggered Check-Host audit (the
// menu:checkhost:run_now button and the /checkhost_bg run command) must
// report its completion aggregates back to the invoking chat, while the
// scheduler-driven background audit stays silent (pre-existing contract).

// newBlockingCheckHostMock builds a Check-Host client against a local mock
// whose /check-result response blocks until release is closed (or a safety
// timeout fires), so tests can interact with the bot WHILE the audit sweep is
// still in flight. served is closed once the result response has been written.
func newBlockingCheckHostMock(t *testing.T) (chClient *checker.CheckHostClient, release func(), served <-chan struct{}) {
	t.Helper()
	releaseCh := make(chan struct{})
	servedCh := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "blockrep",
				"permanent_link": "https://check-host.net/check-report/blockrep",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "1.2.3.4", "AS1"},
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/blockrep") {
			select {
			case <-releaseCh:
			case <-time.After(5 * time.Second):
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ru2.node.check-host.net": []interface{}{map[string]interface{}{"time": 0.05, "address": "1.1.1.1"}},
				"de1.node.check-host.net": []interface{}{map[string]interface{}{"time": 0.02, "address": "1.1.1.1"}},
			})
			select {
			case <-servedCh:
			default:
				close(servedCh)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)

	chClient = checker.NewCheckHostClient(ts.URL, 10*time.Millisecond)
	chClient.HTTPClient = ts.Client()
	return chClient, func() { close(releaseCh) }, servedCh
}

// newReportTestBot builds a Bot wired to the fake Telegram API with one local
// proxy target and the given Check-Host client (bg gate configurable).
func newReportTestBot(t *testing.T, api *telego.Bot, chClient *checker.CheckHostClient, bgEnabled bool) *Bot {
	t.Helper()
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{
		CheckIntervalSec:      300,
		CheckHostBgEnabled:    bgEnabled,
		CheckHostAlertEnabled: true,
		AlertMode:             AlertModeClean,
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	return &Bot{
		api:            api,
		ctx:            context.Background(),
		targets:        []ChatTarget{{ChatID: 12345}},
		allowedChatIDs: map[int64]bool{12345: true},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Proxy-X", StableID: "proxy-x", Address: "1.1.1.1:443", Protocol: "vless", Online: true},
		}},
		configMgr:       cm,
		tracker:         NewAlertTracker(),
		eventBuffer:     NewEventBuffer(),
		checkHostClient: chClient,
		checkHostNodes:  []string{"ru2.node.check-host.net", "de1.node.check-host.net"},
	}
}

// waitForCompletionEdit polls the fake API for an editMessageText carrying the
// manual-audit completion report and returns its text ("" on timeout).
func waitForCompletionEdit(ft *fakeTelegramAPI) string {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range ft.callsOf("editMessageText") {
			if txt, _ := c.Params["text"].(string); strings.Contains(txt, "Проверка завершена") {
				return txt
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
}

// waitForCompletionSend polls the fake API for a sendMessage carrying the
// manual-audit completion report and returns its text ("" on timeout).
func waitForCompletionSend(ft *fakeTelegramAPI) string {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range ft.callsOf("sendMessage") {
			if txt, _ := c.Params["text"].(string); strings.Contains(txt, "Проверка завершена") {
				return txt
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
}

// completionEditCalls returns all edits carrying the completion report.
func completionEditCalls(ft *fakeTelegramAPI) []fakeAPICall {
	var out []fakeAPICall
	for _, c := range ft.callsOf("editMessageText") {
		if txt, _ := c.Params["text"].(string); strings.Contains(txt, "Проверка завершена") {
			out = append(out, c)
		}
	}
	return out
}

// TestRunAuditNowAndReport_EditsOriginWithAggregates: the manual wrapper edits
// the ORIGIN message with the RU completion report; a clean sweep (RU+world
// OK) reports one probed host, zero RU blocks and «проблем не найдено».
func TestRunAuditNowAndReport_EditsOriginWithAggregates(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	chClient := newCheckHostAuditMock(t, true, true)
	b := newReportTestBot(t, api, chClient, false) // bg gate OFF: manual run bypasses it, as before

	b.runAuditNowAndReport(12345, 10)

	text := waitForCompletionEdit(ft)
	if text == "" {
		t.Fatalf("manual audit must report its completion on the origin message, got no report edit")
	}
	if msgID, ok := completionEditCalls(ft)[0].Params["message_id"].(float64); !ok || int(msgID) != 10 {
		t.Fatalf("report must edit the origin message 10, got message_id=%v (ok=%v)", completionEditCalls(ft)[0].Params["message_id"], ok)
	}
	if !strings.Contains(text, "Проверка завершена: 1 хост · блокировок РФ: 0") {
		t.Fatalf("report must carry the sweep aggregates, got: %s", text)
	}
	if !strings.Contains(text, "проблем не найдено") {
		t.Fatalf("a clean sweep (K=0) must append «проблем не найдено», got: %s", text)
	}
}

// TestRunAuditNowAndReport_RUBlockedCounted: with the RU-block signature
// (RU down + world up) the report counts the blocked host and drops the
// «проблем не найдено» tail.
func TestRunAuditNowAndReport_RUBlockedCounted(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	chClient := newCheckHostAuditMock(t, false, true)
	b := newReportTestBot(t, api, chClient, true)

	b.runAuditNowAndReport(12345, 10)

	text := waitForCompletionEdit(ft)
	if text == "" {
		t.Fatalf("manual audit must report its completion, got no report edit")
	}
	if !strings.Contains(text, "блокировок РФ: 1") {
		t.Fatalf("RU-block signature must be counted in the report, got: %s", text)
	}
	if strings.Contains(text, "проблем не найдено") {
		t.Fatalf("«проблем не найдено» must be omitted when K>0, got: %s", text)
	}
}

// TestRunAuditNowAndReport_StaleEditSuppressedByMsgSeq: while the sweep is in
// flight the user taps another button on the same message (invalidateMsgSeq);
// the completion report must then be dropped — the stale edit never lands.
func TestRunAuditNowAndReport_StaleEditSuppressedByMsgSeq(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	chClient, release, served := newBlockingCheckHostMock(t)
	b := newReportTestBot(t, api, chClient, true)

	b.runAuditNowAndReport(12345, 10)

	// The sweep is blocked at the Check-Host API: no completion report yet.
	if edits := completionEditCalls(ft); len(edits) != 0 {
		t.Fatalf("no completion report may land while the sweep is in flight, got %d", len(edits))
	}

	// The user interacts with the same message mid-sweep.
	b.invalidateMsgSeq(12345, 10)

	release()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatalf("the mock never served the check-result")
	}
	time.Sleep(300 * time.Millisecond) // let the reporting goroutine finish

	if edits := completionEditCalls(ft); len(edits) != 0 {
		t.Fatalf("stale completion report must be suppressed by the msg-seq guard, got %d edits", len(edits))
	}
}

// TestRunAuditNowAndReport_NoOriginSendsNewMessage: with no origin message to
// edit (msgID=0, e.g. a failed placeholder send) the completion report is
// delivered as a NEW message to the invoking chat.
func TestRunAuditNowAndReport_NoOriginSendsNewMessage(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	chClient := newCheckHostAuditMock(t, true, true)
	b := newReportTestBot(t, api, chClient, true)

	b.runAuditNowAndReport(12345, 0)

	text := waitForCompletionSend(ft)
	if text == "" {
		t.Fatalf("without an origin message the completion report must be sent as a new message")
	}
	if edits := completionEditCalls(ft); len(edits) != 0 {
		t.Fatalf("no edits may be attempted without an origin message, got %d", len(edits))
	}
}

// TestRunAuditNowAndReport_ConcurrentRunSkipped: when another audit is already
// in flight the report says so instead of a fake «проблем не найдено».
func TestRunAuditNowAndReport_ConcurrentRunSkipped(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	chClient := newCheckHostAuditMock(t, true, true)
	b := newReportTestBot(t, api, chClient, true)

	b.checkHostMu.Lock()
	b.checkHostRunning = true
	b.checkHostMu.Unlock()

	b.runAuditNowAndReport(12345, 10)

	deadline := time.Now().Add(3 * time.Second)
	var text string
	for time.Now().Before(deadline) && text == "" {
		for _, c := range ft.callsOf("editMessageText") {
			if txt, _ := c.Params["text"].(string); strings.Contains(txt, "уже выполняется") {
				text = txt
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if text == "" {
		t.Fatalf("a skipped (already-running) audit must report the collision instead of fake aggregates")
	}
	if strings.Contains(text, "Проверка завершена") {
		t.Fatalf("a skipped audit must not claim completion, got: %s", text)
	}
}

// TestRunAuditNow_BgPathStaysSilent pins the pre-existing background contract:
// the scheduler entry points (RunCheckHostAudit / runAuditNow) perform the
// audit but NEVER emit the manual-run completion report.
func TestRunAuditNow_BgPathStaysSilent(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	chClient := newCheckHostAuditMock(t, true, true)
	b := newReportTestBot(t, api, chClient, true)

	b.RunCheckHostAudit()
	b.runAuditNow()

	if edits := ft.callsOf("editMessageText"); len(edits) != 0 {
		t.Fatalf("background audit must not edit any message, got %d edits", len(edits))
	}
	for _, c := range ft.callsOf("sendMessage") {
		if txt, _ := c.Params["text"].(string); strings.Contains(txt, "Проверка завершена") {
			t.Fatalf("background audit must not send the completion report, got: %s", txt)
		}
	}
}

// TestCallbackRunNow_ReportsCompletion: the full run_now wiring — the toast
// stays, the audit still bypasses the bg gate, and the completion report is
// edited onto the origin message.
func TestCallbackRunNow_ReportsCompletion(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	chClient := newCheckHostAuditMock(t, true, true)
	b := newReportTestBot(t, api, chClient, false) // bg gate OFF: explicit run must still work

	b.handleCallbackQuery(gateCallback(9999, "menu:checkhost:run_now"))

	if toast, ok := ft.lastParamOf("answerCallbackQuery", "text"); !ok || !strings.Contains(toast, "Запуск фоновой проверки") {
		t.Errorf("run_now must keep the launch toast, got ok=%v toast=%q", ok, toast)
	}
	text := waitForCompletionEdit(ft)
	if text == "" {
		t.Fatalf("run_now must report the audit completion on the origin message")
	}
	if msgID, ok := completionEditCalls(ft)[0].Params["message_id"].(float64); !ok || int(msgID) != 10 {
		t.Fatalf("report must edit the origin message 10, got message_id=%v (ok=%v)", completionEditCalls(ft)[0].Params["message_id"], ok)
	}
}

// TestCheckHostBgRunCommand_ReportsCompletionAndGate: the /checkhost_bg run
// command reports completion by editing its launch-notice placeholder; with
// the bg gate off the command stays the historical no-op (notice only).
func TestCheckHostBgRunCommand_ReportsCompletionAndGate(t *testing.T) {
	// 1. Gate ON: placeholder edited into the completion report.
	ft, api := newFakeTelegramBot(t)
	chClient := newCheckHostAuditMock(t, true, true)
	b := newReportTestBot(t, api, chClient, true)

	b.handleCheckHostBgCommand(&telego.Message{
		MessageID: 55,
		Chat:      telego.Chat{ID: 12345, Type: "private"},
		Text:      "/checkhost_bg run",
		From:      &telego.User{ID: 1001},
	})

	sends := ft.callsOf("sendMessage")
	if len(sends) == 0 {
		t.Fatalf("run command must send the launch notice first")
	}
	if txt, _ := sends[0].Params["text"].(string); !strings.Contains(txt, "Запуск фоновой проверки") {
		t.Fatalf("run command must keep the launch notice, got: %s", txt)
	}
	text := waitForCompletionEdit(ft)
	if text == "" {
		t.Fatalf("run command must report the audit completion")
	}
	// The fake API hands out deterministic ids starting at 101; the launch
	// notice is the first generated message, so the report must edit 101 —
	// proving the placeholder captured via sendAndReturn is the edit target.
	if msgID, ok := completionEditCalls(ft)[0].Params["message_id"].(float64); !ok || int(msgID) != 101 {
		t.Fatalf("report must edit the launch-notice placeholder 101, got message_id=%v (ok=%v)",
			completionEditCalls(ft)[0].Params["message_id"], ok)
	}

	// 2. Gate OFF: the command stays the historical no-op — notice only, no
	// completion report (bg gate semantics preserved for the command path).
	ft2, api2 := newFakeTelegramBot(t)
	chClient2 := newCheckHostAuditMock(t, true, true)
	b2 := newReportTestBot(t, api2, chClient2, false)

	b2.handleCheckHostBgCommand(&telego.Message{
		MessageID: 56,
		Chat:      telego.Chat{ID: 12345, Type: "private"},
		Text:      "/checkhost_bg run",
		From:      &telego.User{ID: 1001},
	})

	if sends := ft2.callsOf("sendMessage"); len(sends) == 0 {
		t.Fatalf("gate-off run command must still send the launch notice")
	}
	time.Sleep(300 * time.Millisecond)
	if edits := completionEditCalls(ft2); len(edits) != 0 {
		t.Fatalf("gate-off run command must stay a silent no-op (no completion report), got %d edits", len(edits))
	}
}
