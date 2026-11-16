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

	"xray-checker/checker"
	"xray-checker/metrics"
)

// TestHandleCheckHostProxy_UDPProtoTCPWithWarning pins the user-authorized
// Check-Host semantics change (2026-11-16; REPLACES the former F2 pin
// TestHandleCheckHostProxy_UDPProtoUsesPing by explicit user instruction —
// the UDP→ICMP-ping design is intentionally reversed): a manual per-proxy
// Check-Host check ALWAYS sends host:port from the proxy's subscription entry
// as a TCP check, UDP protocols included, and both the placeholder and the
// final report carry a leading RU warning that only TCP reachability of the
// port can be verified (the API cannot probe UDP services).
func TestHandleCheckHostProxy_UDPProtoTCPWithWarning(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var tcpHosts []string

	chMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			tcpHosts = append(tcpHosts, r.URL.Query().Get("host"))
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "tcp1234",
				"permanent_link": "https://check-host.net/check-report/tcp1234",
				"nodes": map[string]interface{}{
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/tcp1234") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"de1.node.check-host.net": []interface{}{
					[]interface{}{
						[]interface{}{"OK", 0.02, "1.2.3.4"},
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer chMock.Close()

	chClient := checker.NewCheckHostClient(chMock.URL, 20*time.Millisecond)
	chClient.HTTPClient = chMock.Client()

	ft, api := newFakeTelegramBot(t)
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{CheckIntervalSec: 300})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	b := &Bot{
		api:     api,
		ctx:     context.Background(),
		targets: []ChatTarget{{ChatID: 12345}},
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Hys-1", StableID: "hys-1", Address: "1.2.3.4:5678", Protocol: "hysteria2", Online: true},
		}},
		configMgr:       cm,
		tracker:         NewAlertTracker(),
		eventBuffer:     NewEventBuffer(),
		checkHostClient: chClient,
	}

	b.handleCheckHostProxy(12345, 10, "hys-1")

	mu.Lock()
	gotTCPHosts := append([]string(nil), tcpHosts...)
	mu.Unlock()

	for _, p := range paths {
		if strings.HasPrefix(p, "/check-ping") {
			t.Errorf("UDP proxy must no longer be ping-checked, saw %s (all paths: %v)", p, paths)
		}
	}
	if len(gotTCPHosts) != 1 || gotTCPHosts[0] != "1.2.3.4:5678" {
		t.Errorf("expected exactly one /check-tcp with the subscription host:port 1.2.3.4:5678, got hosts %v (paths %v)", gotTCPHosts, paths)
	}

	calls := ft.callsOf("editMessageText")
	if len(calls) < 2 {
		t.Fatalf("expected placeholder and final edits, got %d editMessageText calls", len(calls))
	}
	const wantWarn = "сам UDP-сервис API не проверяет"
	placeholder, _ := calls[0].Params["text"].(string)
	final, _ := calls[len(calls)-1].Params["text"].(string)
	for name, text := range map[string]string{"placeholder": placeholder, "final": final} {
		if !strings.Contains(text, wantWarn) {
			t.Errorf("%s must carry the UDP TCP-only warning, got: %q", name, text)
		}
		if !strings.Contains(text, "<code>1.2.3.4:5678</code>") {
			t.Errorf("%s must name the checked host:port inside <code>, got: %q", name, text)
		}
		if strings.Contains(text, "(PING)") {
			t.Errorf("%s must not mention the removed (PING) note, got: %q", name, text)
		}
	}
}
