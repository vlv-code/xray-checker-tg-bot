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

// newCheckHostAuditMock builds a Check-Host client against a local mock whose
// results are controlled by ruOK/worldOK (true = successful result, false =
// connection-timeout error result for the ru2/de1 nodes respectively).
func newCheckHostAuditMock(t *testing.T, ruOK, worldOK bool) *checker.CheckHostClient {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "auditf4",
				"permanent_link": "https://check-host.net/check-report/auditf4",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "1.2.3.4", "AS1"},
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/auditf4") {
			ruRes := []interface{}{map[string]interface{}{"error": "Connection timed out"}}
			if ruOK {
				ruRes = []interface{}{map[string]interface{}{"time": 0.05, "address": "1.1.1.1"}}
			}
			worldRes := []interface{}{map[string]interface{}{"error": "Connection timed out"}}
			if worldOK {
				worldRes = []interface{}{map[string]interface{}{"time": 0.02, "address": "1.1.1.1"}}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ru2.node.check-host.net": ruRes,
				"de1.node.check-host.net": worldRes,
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)

	chClient := checker.NewCheckHostClient(ts.URL, 10*time.Millisecond)
	chClient.HTTPClient = ts.Client()
	return chClient
}

// newAuditTestBot builds a Bot wired for runAuditNow with a single local
// proxy target and a nil api (sendAndReturn degrades to MessageID 0 without
// error, so tracker transitions remain observable).
func newAuditTestBot(t *testing.T, chClient *checker.CheckHostClient) (*Bot, *AlertTracker) {
	t.Helper()
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{
		CheckHostBgEnabled:    true,
		CheckHostAlertEnabled: true,
		AlertMode:             AlertModeClean,
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	tracker := NewAlertTracker()
	bot := &Bot{
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Node-1", Address: "1.1.1.1:443", StableID: "node-1", Protocol: "vless", Online: true},
		}},
		configMgr:       cm,
		tracker:         tracker,
		targets:         []ChatTarget{{ChatID: 12345}},
		checkHostClient: chClient,
		checkHostNodes:  []string{"ru2.node.check-host.net", "de1.node.check-host.net"},
		ctx:             context.Background(),
	}
	return bot, tracker
}

// TestRunAuditNow_UnreachableEverywhereNoAlert pins the RU-block alert
// signature: alerts fire only for RU down + world up (real RU block).
// Hosts unreachable from both RU and world (ICMP-filtered or fully down)
// must not raise an alert, because such a tracked alert can never resolve
// (resolution requires an RU success) and would stick forever.
func TestRunAuditNow_UnreachableEverywhereNoAlert(t *testing.T) {
	// RU=false && World=false: no alert, no tracking.
	bot, tracker := newAuditTestBot(t, newCheckHostAuditMock(t, false, false))
	bot.runAuditNow()
	if tracker.HasAlert(12345, 0, "checkhost:1.1.1.1:443") {
		t.Errorf("host unreachable from RU and world must NOT raise a RU-block alert")
	}

	// Positive control: RU=false && World=true (RU-block signature) -> alert tracked.
	bot2, tracker2 := newAuditTestBot(t, newCheckHostAuditMock(t, false, true))
	bot2.runAuditNow()
	if !tracker2.HasAlert(12345, 0, "checkhost:1.1.1.1:443") {
		t.Errorf("RU-block signature (RU down, world up) must raise and track an alert")
	}
}

// TestRunAuditNow_SkipsUDPTargets pins the passive-audit UDP exclusion
// (user decision 2026-11-16): UDP-protocol proxies are NEVER sent to the
// Check-Host API by background audits — the API cannot verify UDP services —
// while TCP proxies in the same snapshot are still audited normally.
func TestRunAuditNow_SkipsUDPTargets(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var tcpHosts []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				"request_id":     "skipreq",
				"permanent_link": "https://check-host.net/check-report/skipreq",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "1.2.3.4", "AS1"},
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Frankfurt", "5.6.7.8", "AS2"},
				},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/check-result/skipreq") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ru2.node.check-host.net": []interface{}{map[string]interface{}{"time": 0.05, "address": "1.1.1.1"}},
				"de1.node.check-host.net": []interface{}{map[string]interface{}{"time": 0.02, "address": "1.1.1.1"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)

	chClient := checker.NewCheckHostClient(ts.URL, 10*time.Millisecond)
	chClient.HTTPClient = ts.Client()

	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{
		CheckHostBgEnabled:    true,
		CheckHostAlertEnabled: true,
		AlertMode:             AlertModeClean,
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	bot := &Bot{
		source: &mockSource{metrics: []metrics.ProxyMetric{
			{Name: "Hys-Skip", Address: "9.9.9.9:5678", StableID: "udp-1", Protocol: "hysteria2", Online: true},
			{Name: "Vless-Keep", Address: "1.1.1.1:443", StableID: "tcp-1", Protocol: "vless", Online: true},
		}},
		configMgr:       cm,
		tracker:         NewAlertTracker(),
		targets:         []ChatTarget{{ChatID: 12345}},
		checkHostClient: chClient,
		checkHostNodes:  []string{"ru2.node.check-host.net", "de1.node.check-host.net"},
		ctx:             context.Background(),
	}

	bot.runAuditNow()

	mu.Lock()
	defer mu.Unlock()
	for _, h := range tcpHosts {
		if strings.Contains(h, "9.9.9.9") {
			t.Errorf("UDP proxy must never reach the Check-Host API in passive audits, saw host %q", h)
		}
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "/check-ping") {
			t.Errorf("UDP proxy must not be ping-checked in passive audits, saw %s", p)
		}
	}
	kept := false
	for _, h := range tcpHosts {
		if h == "1.1.1.1:443" {
			kept = true
		}
	}
	if !kept {
		t.Errorf("TCP proxy in the same snapshot must still be audited, saw hosts %v (paths %v)", tcpHosts, paths)
	}
}

// TestProcessNodeCheckHostAudits_UnreachableEverywhereNoAlert pins the same
// RU-block signature gate for the map-based path of node-shipped audits.
func TestProcessNodeCheckHostAudits_UnreachableEverywhereNoAlert(t *testing.T) {
	tmpDir := t.TempDir()
	cm, err := NewConfigManager(filepath.Join(tmpDir, "bot_cfg.json"), BotConfig{
		CheckHostAlertEnabled: true,
		AlertMode:             AlertModeClean,
	})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	tracker := NewAlertTracker()
	bot := &Bot{
		configMgr: cm,
		tracker:   tracker,
		targets:   []ChatTarget{{ChatID: 12345}},
		ctx:       context.Background(),
	}

	// RU=false && World=false: nothing sent, nothing tracked.
	bot.ProcessNodeCheckHostAudits("node-a", map[string]checker.CheckHostSummary{
		"5.5.5.5:443": {RUAvailable: false, WorldAvailable: false},
	})
	if tracker.HasAlert(12345, 0, "checkhost:5.5.5.5:443") {
		t.Errorf("unreachable-everywhere node audit must NOT raise a RU-block alert")
	}

	// Positive control: RU=false && World=true -> alert tracked.
	bot.ProcessNodeCheckHostAudits("node-a", map[string]checker.CheckHostSummary{
		"6.6.6.6:443": {RUAvailable: false, WorldAvailable: true},
	})
	if !tracker.HasAlert(12345, 0, "checkhost:6.6.6.6:443") {
		t.Errorf("RU-block signature (RU down, world up) must raise and track an alert")
	}
}
