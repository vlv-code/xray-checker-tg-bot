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

// TestHandleCheburcheckHost_UsesInjectedClient (replaces
// TestHandleCheburcheckProxy_UsesInjectedClient — authorized rework, wave G1
// host-based menu): an injected chebur client must be used, the host resolved
// from the menu key must be probed on the BARE host (no port in /check
// target), the per-scanner token must be sent, and the final report must
// carry the 🧀 header with the wave G summary-first format.
func TestHandleCheburcheckHost_UsesInjectedClient(t *testing.T) {
	var mu sync.Mutex
	var checkTargets []string
	var probeTokens []string

	chebMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/check":
			checkTargets = append(checkTargets, r.URL.Query().Get("target"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "job77"})
		case r.URL.Path == "/probe/job77":
			probeTokens = append(probeTokens, r.URL.Query().Get("token"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"event: started\ndata: {\"id\":\"job77\",\"target\":\"1.2.3.4\",\"online_probes\":1}\n\n" +
					"event: result\ndata: {\"job_id\":\"j\",\"probe_id\":\"p1\",\"cached\":false,\"verdicts\":[\"ok\"]}\n\n" +
					"event: done\ndata: {\"id\":\"job77\",\"status\":\"finished\",\"response_count\":1,\"online_probes\":1}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer chebMock.Close()

	chebClient := checker.NewCheburcheckClient(chebMock.URL, 5*time.Second, []checker.ScannerCred{
		{Name: "test-scanner", Token: "tok-1"},
	})

	ft, api := newFakeTelegramBot(t)
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "chebur_cfg.json"), BotConfig{CheckIntervalSec: 300})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	b := &Bot{
		api:     api,
		ctx:     context.Background(),
		targets: []ChatTarget{{ChatID: 12345}},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Hys-C", StableID: "hys-c", Address: "1.2.3.4:5678", Protocol: "hysteria2", Online: false},
		}},
		configMgr:    cm,
		tracker:      NewAlertTracker(),
		eventBuffer:  NewEventBuffer(),
		cheburClient: chebClient,
	}

	b.handleCheburcheckHost(12345, 10, HostCallbackKey("1.2.3.4"))

	mu.Lock()
	gotTargets := append([]string(nil), checkTargets...)
	gotTokens := append([]string(nil), probeTokens...)
	mu.Unlock()

	if len(gotTargets) != 1 || gotTargets[0] != "1.2.3.4" {
		t.Fatalf("expected /check with bare host 1.2.3.4 (port stripped), got %v", gotTargets)
	}
	if len(gotTokens) != 1 || gotTokens[0] != "tok-1" {
		t.Fatalf("expected probe with per-scanner token tok-1, got %v", gotTokens)
	}

	edits := ft.callsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatal("expected editMessageText calls, got none")
	}
	final, _ := edits[len(edits)-1].Params["text"].(string)
	for _, want := range []string{"🧀", "1.2.3.4", "Ответы: 1 из 1", "Не ограничен"} {
		if !strings.Contains(final, want) {
			t.Errorf("final report missing %q, got: %s", want, final)
		}
	}
}

// TestHandleCheburcheckHost_UnknownKeyGraceful (wave G1): a host key that
// does not resolve in the snapshot (e.g. a stale button after the proxy set
// changed) must answer with a graceful RU error and never touch the network.
func TestHandleCheburcheckHost_UnknownKeyGraceful(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b := &Bot{
		api:     api,
		ctx:     context.Background(),
		targets: []ChatTarget{{ChatID: 12345}},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "P", StableID: "p", Address: "1.2.3.4:443", Protocol: "vless"},
		}},
		tracker:     NewAlertTracker(),
		eventBuffer: NewEventBuffer(),
		// cheburClient stays nil: with the reverse lookup failing first, the
		// client seam must never be reached.
	}

	b.handleCheburcheckHost(12345, 10, "not-in-snapshot.example.com")

	edits := ft.callsOf("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("expected exactly one error edit, got %d", len(edits))
	}
	text, _ := edits[0].Params["text"].(string)
	if !strings.Contains(text, "Хост не найден в текущей конфигурации") {
		t.Errorf("expected graceful host-not-found message, got: %s", text)
	}
}

// TestHandleCheburcheckCommand_NoScanners_GracefulMessage: with no scanners
// configured (public repo default) the command must answer with the graceful
// RU message and never touch the network or render a placeholder.
func TestHandleCheburcheckCommand_NoScanners_GracefulMessage(t *testing.T) {
	ft, api := newFakeTelegramBot(t)
	b := &Bot{
		api:          api,
		ctx:          context.Background(),
		targets:      []ChatTarget{{ChatID: 12345}},
		source:       &mockSource{metrics: []metrics.ProxyMetric{}},
		tracker:      NewAlertTracker(),
		eventBuffer:  NewEventBuffer(),
		cheburClient: checker.NewCheburcheckClient("", time.Second, nil),
	}

	b.handleCheburcheckCommand(&telego.Message{
		MessageID: 7,
		Chat:      telego.Chat{ID: 12345, Type: "private"},
		Text:      "/cheburcheck example.com",
	})

	sends := ft.callsOf("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("expected exactly one reply, got %d", len(sends))
	}
	text, _ := sends[0].Params["text"].(string)
	if !strings.Contains(text, "Сканеры Cheburcheck не настроены (DefaultCheburScanners пуст)") {
		t.Errorf("expected graceful no-scanners message, got: %s", text)
	}
	if len(ft.callsOf("editMessageText")) != 0 {
		t.Errorf("no editMessageText expected on the no-scanners path")
	}
}

// TestCheburcheckHostButton_LongHostRoundTrip (replaces
// TestCheburcheckMenuButton_CallbackDataWithin64Bytes — authorized rework,
// wave G1): the chebur host button for a >36-byte host hashes via
// HostCallbackKey to a short callback that (a) stays within Telegram's
// 64-byte limit and (b) round-trips: tapping it resolves the ORIGINAL host
// through the snapshot reverse lookup and probes it.
func TestCheburcheckHostButton_LongHostRoundTrip(t *testing.T) {
	longHost := strings.Repeat("z", 50) + ".example.com"

	var mu sync.Mutex
	var checkTargets []string
	chebMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Path == "/check" {
			checkTargets = append(checkTargets, r.URL.Query().Get("target"))
		}
		mu.Unlock()
		switch {
		case r.URL.Path == "/check":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "job77"})
		case r.URL.Path == "/probe/job77":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"event: started\ndata: {\"id\":\"job77\",\"target\":\"h\",\"online_probes\":1}\n\n" +
					"event: result\ndata: {\"job_id\":\"j\",\"probe_id\":\"p1\",\"cached\":false,\"verdicts\":[\"ok\"]}\n\n" +
					"event: done\ndata: {\"id\":\"job77\",\"status\":\"finished\",\"response_count\":1,\"online_probes\":1}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer chebMock.Close()

	chebClient := checker.NewCheburcheckClient(chebMock.URL, 5*time.Second, []checker.ScannerCred{
		{Name: "test-scanner", Token: "tok-1"},
	})

	ft, api := newFakeTelegramBot(t)
	b := &Bot{
		api:     api,
		ctx:     context.Background(),
		targets: []ChatTarget{{ChatID: 12345}},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Long", StableID: "long", Address: longHost + ":443", Protocol: "vless"},
		}},
		tracker:      NewAlertTracker(),
		eventBuffer:  NewEventBuffer(),
		cheburClient: chebClient,
	}

	markup := CheburMenuMarkup([]string{longHost}, 1, 1)
	var cb string
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, "menu:cheburh:") {
				cb = btn.CallbackData
			}
		}
	}
	if cb == "" {
		t.Fatal("expected a menu:cheburh: host button in the Cheburcheck submenu")
	}
	if len(cb) > 64 {
		t.Fatalf("callback_data exceeds 64 bytes: %q (%d bytes)", cb, len(cb))
	}
	if cb == "menu:cheburh:"+longHost {
		t.Errorf("a >36-byte host must be hashed via HostCallbackKey, not embedded raw")
	}
	if want := "menu:cheburh:" + HostCallbackKey(longHost); cb != want {
		t.Fatalf("expected callback %q, got %q", want, cb)
	}

	// Round-trip: tapping the hashed-key button probes the ORIGINAL host.
	b.handleCheburcheckHost(12345, 10, strings.TrimPrefix(cb, "menu:cheburh:"))

	deadline := time.Now().Add(2 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		mu.Lock()
		got = append([]string(nil), checkTargets...)
		mu.Unlock()
		if len(got) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 || got[0] != longHost {
		t.Fatalf("expected the hashed-key tap to probe the original host %s, got %v", longHost, got)
	}
	if edits := ft.callsOf("editMessageText"); len(edits) == 0 {
		t.Fatal("expected the report edit after the probe")
	}
}

// TestCheburProbeIconCDNFamilyIsInfo (wave G reconciliation): the deep-diag
// per-scanner icon treats the informational CDN family (cdn_block/whitelist)
// as ℹ️ — never ⛔; only the real restrictions (sni_block/tspu_block/
// dns_spoofing) are ⛔; ok ✅; unknown/empty ❓.
func TestCheburProbeIconCDNFamilyIsInfo(t *testing.T) {
	cases := []struct {
		verdicts []string
		want     string
	}{
		{[]string{"cdn_block"}, "ℹ️"},
		{[]string{"whitelist"}, "ℹ️"},
		{[]string{"ok", "cdn_block"}, "ℹ️"},
		{[]string{"sni_block"}, "⛔"},
		{[]string{"tspu_block"}, "⛔"},
		{[]string{"dns_spoofing"}, "⛔"},
		{[]string{"ok"}, "✅"},
		{[]string{"ok", "sni_block"}, "⛔"},
		{nil, "❓"},
		{[]string{"unknown_kind"}, "❓"},
	}
	for _, tc := range cases {
		if got := cheburProbeIcon(tc.verdicts); got != tc.want {
			t.Errorf("cheburProbeIcon(%v) = %s, want %s", tc.verdicts, got, tc.want)
		}
	}
}

// TestFormatCheburDiagBlock_CountsLine (wave G reconciliation): the deep-diag
// chebur block surfaces the counts line (KindCounts / OnlineProbes /
// Answered) in addition to the verdict and the per-scanner lines.
func TestFormatCheburDiagBlock_CountsLine(t *testing.T) {
	cheb := &checker.CheburcheckSummary{
		Target:       "example.com",
		OnlineProbes: 4,
		Answered:     3,
		KindCounts:   map[string]int{"ok": 2, "cdn_block": 1, "sni_block": 2},
		Results: []checker.CheburProbeResult{
			{Name: "msk-1", Verdicts: []string{"ok", "cdn_block"}},
			{Name: "spb-1", Verdicts: []string{"ok"}},
			{Name: "kzn-1", Verdicts: []string{"sni_block"}},
		},
	}
	block := formatCheburDiagBlock(cheb)
	for _, want := range []string{
		"Ответы: 3 из 4 сканеров",
		"Доступен: 2",
		"CDN-блокировка: 1",
		"SNI Блок: 2",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("diag block missing %q, got: %s", want, block)
		}
	}
	// The multi-verdict scanner (ok + cdn_block) renders ℹ️, never ⛔.
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, "msk-1") {
			if !strings.Contains(line, "ℹ️") || strings.Contains(line, "⛔") {
				t.Errorf("cdn_block scanner line must use ℹ️ (not ⛔), got: %s", line)
			}
		}
	}
}
