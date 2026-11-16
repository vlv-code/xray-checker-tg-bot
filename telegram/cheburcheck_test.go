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

// TestHandleCheburcheckProxy_UsesInjectedClient mirrors the fixed F2
// Check-Host per-proxy test: an injected chebur client must be used, a
// hysteria2 proxy must be probed on the BARE host (no port in /check target),
// the per-scanner token must be sent, and the final report must be rendered.
func TestHandleCheburcheckProxy_UsesInjectedClient(t *testing.T) {
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

	b.handleCheburcheckProxy(12345, 10, "hys-c")

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
	for _, want := range []string{"test-scanner", "✅", "Активный пробинг Cheburcheck"} {
		if !strings.Contains(final, want) {
			t.Errorf("final report missing %q, got: %s", want, final)
		}
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

// TestCheburcheckMenuButton_CallbackDataWithin64Bytes pins the hard Telegram
// constraint: the Cheburcheck submenu button callback_data must stay within
// 64 bytes even for the longest possible stableID (16 hex chars).
// Wave D: the per-proxy Cheburcheck button moved from the Check-Host list
// (sibling) into its own CheburMenuMarkup submenu — construction source
// updated accordingly, the ≤64-byte pin itself survives unchanged.
func TestCheburcheckMenuButton_CallbackDataWithin64Bytes(t *testing.T) {
	longest := strings.Repeat("a", 16) // max models.GenerateStableID length
	proxies := []metrics.ProxyMetric{
		{Name: "Proxy One", StableID: longest, Address: "1.1.1.1:443", Protocol: "vless"},
	}
	markup := CheburMenuMarkup(proxies)
	if markup == nil {
		t.Fatal("nil markup")
	}
	foundChebur := false
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if len(btn.CallbackData) > 64 {
				t.Errorf("callback_data exceeds 64 bytes: %q (%d bytes)", btn.CallbackData, len(btn.CallbackData))
			}
			if btn.CallbackData == "menu:chebur:"+longest {
				foundChebur = true
			}
		}
	}
	if !foundChebur {
		t.Fatalf("expected a menu:chebur:%s button in the Cheburcheck submenu", longest)
	}
}
