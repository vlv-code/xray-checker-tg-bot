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

// TestHandleCheckHostProxy_UDPProtoUsesPing pins the UDP-protocol branch of
// the manual per-proxy Check-Host check: hysteria/hysteria2/tuic/wireguard
// hosts are ICMP-pinged on the bare host via the injectable checkHostClient
// seam, instead of a guaranteed-to-fail TCP connect to a UDP-only port.
func TestHandleCheckHostProxy_UDPProtoUsesPing(t *testing.T) {
	var mu sync.Mutex
	var paths []string

	chMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-ping") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "udp1234",
				"permanent_link": "https://check-host.net/check-report/udp1234",
				"nodes": map[string]interface{}{
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/udp1234") {
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
	sawPing, sawTCP := false, false
	for _, p := range paths {
		if strings.HasPrefix(p, "/check-ping") {
			sawPing = true
		}
		if strings.HasPrefix(p, "/check-tcp") {
			sawTCP = true
		}
	}
	mu.Unlock()

	if !sawPing {
		t.Fatalf("expected a /check-ping request for hysteria2 proxy, got paths: %v", paths)
	}
	if sawTCP {
		t.Errorf("did not expect a /check-tcp request for UDP-only proxy, got paths: %v", paths)
	}

	// The placeholder for the UDP path must say (PING), not imply TCP.
	calls := ft.callsOf("editMessageText")
	if len(calls) == 0 {
		t.Fatal("expected at least one editMessageText call, got none")
	}
	placeholder, _ := calls[0].Params["text"].(string)
	if !strings.Contains(placeholder, "(PING)") {
		t.Errorf("expected placeholder text to mention (PING) for UDP proxy, got: %q", placeholder)
	}
}
