package telegram

import (
	"strings"
	"testing"
	"time"

	"xray-checker/metrics"
)

func TestUpdateNodeHealthTransitions(t *testing.T) {
	b := &Bot{lastNodeHealth: make(map[string]nodeHealthState)}
	base := time.Now()

	up := []NodeInfo{{Name: "n1", Up: true, ASN: "AS9009 M247"}}
	down := []NodeInfo{{Name: "n1", Up: false, ASN: "AS9009 M247"}}

	// Seed: no alerts.
	alerts := b.updateNodeHealthLocked(up, base)
	if len(alerts) != 0 {
		t.Fatalf("seed must not alert: %v", alerts)
	}

	// Down: one alert mentioning node and ASN.
	alerts = b.updateNodeHealthLocked(down, base.Add(time.Minute))
	if len(alerts) != 1 {
		t.Fatalf("want 1 down alert, got %v", alerts)
	}
	if !strings.Contains(alerts[0], "n1") || !strings.Contains(alerts[0], "AS9009 M247") {
		t.Errorf("down alert content: %q", alerts[0])
	}

	// Up again: recovery alert with downtime.
	alerts = b.updateNodeHealthLocked(up, base.Add(3*time.Minute))
	if len(alerts) != 1 || !strings.Contains(alerts[0], "n1") {
		t.Fatalf("want recovery alert, got %v", alerts)
	}

	// Steady state: silence.
	alerts = b.updateNodeHealthLocked(up, base.Add(4*time.Minute))
	if len(alerts) != 0 {
		t.Errorf("steady state must not alert: %v", alerts)
	}

	// Vanished nodes drop out of state; re-appearing seeds silently again.
	b.updateNodeHealthLocked(nil, base.Add(5*time.Minute))
	if len(b.lastNodeHealth) != 0 {
		t.Errorf("state must be pruned, got %v", b.lastNodeHealth)
	}
	if alerts := b.updateNodeHealthLocked(up, base.Add(6*time.Minute)); len(alerts) != 0 {
		t.Errorf("re-appeared node must seed silently, got %v", alerts)
	}
}

func TestUpdateNodeHealthSilentDownRecovery(t *testing.T) {
	b := &Bot{lastNodeHealth: make(map[string]nodeHealthState)}
	base := time.Now()

	// A node seeded while down (never alerted, e.g. first sight after restart)
	// must not send a recovery alert when it comes up.
	if alerts := b.updateNodeHealthLocked([]NodeInfo{{Name: "n2", Up: false}}, base); len(alerts) != 0 {
		t.Fatalf("down seed must be silent: %v", alerts)
	}
	if alerts := b.updateNodeHealthLocked([]NodeInfo{{Name: "n2", Up: true}}, base.Add(time.Minute)); len(alerts) != 0 {
		t.Fatalf("silent-down recovery must not alert: %v", alerts)
	}
}

func TestNodeHealthStatsAndAlertSuppression(t *testing.T) {
	tmpDir := t.TempDir()
	statsStore, err := NewStatsStore(tmpDir + "/stats.json")
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}

	cfg := DefaultBotConfig()
	cfg.NodeAlertsEnabled = false // Chat alerts muted

	b := &Bot{
		lastNodeHealth: make(map[string]nodeHealthState),
		statsStore:     statsStore,
	}
	cfgMgr, err := NewConfigManager(tmpDir+"/cfg.json", cfg)
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	b.SetConfigManager(cfgMgr)

	base := time.Now()
	up := []NodeInfo{{Name: "agent-1", Up: true, ASN: "AS1234"}}
	down := []NodeInfo{{Name: "agent-1", Up: false, ASN: "AS1234"}}

	// Seed node
	_ = b.updateNodeHealthLocked(up, base)

	// Transition down: alerts suppressed because NodeAlertsEnabled is false
	alerts := b.updateNodeHealthLocked(down, base.Add(time.Minute))
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts when NodeAlertsEnabled=false, got %v", alerts)
	}

	// Verify stats store recorded the incident!
	incidents := statsStore.GetRecentIncidents(10)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident recorded in statsStore, got %d", len(incidents))
	}
	if incidents[0].StableID != "node:agent-1" || !strings.Contains(incidents[0].ProxyName, "[Агент] agent-1") {
		t.Errorf("unexpected incident recorded: %+v", incidents[0])
	}
	if incidents[0].Reason != "Потеря связи с чекер-нодой" {
		t.Errorf("unexpected incident reason: %s", incidents[0].Reason)
	}
}

func TestRemoteProxyAlertJournalingAndSuppression(t *testing.T) {
	tmpDir := t.TempDir()
	statsStore, err := NewStatsStore(tmpDir + "/stats.json")
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}

	cfg := DefaultBotConfig()
	cfg.NodeProxyAlertsChat = false // Chat alerts muted for remote nodes

	b := &Bot{
		lastSeen:   make(map[string]bool),
		statsStore: statsStore,
		tracker:    NewAlertTracker(""),
	}
	cfgMgr, err := NewConfigManager(tmpDir+"/cfg.json", cfg)
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}
	b.SetConfigManager(cfgMgr)

	p := metrics.ProxyMetric{
		StableID: "node_p1",
		Name:     "Proxy 1",
		NodeName: "finland-node",
		Online:   true,
	}

	// Seed
	b.ProcessSnapshot([]metrics.ProxyMetric{p})

	// Drop
	pDown := p
	pDown.Online = false
	pDown.LastErrorMsg = "connection refused"
	b.ProcessSnapshot([]metrics.ProxyMetric{pDown})

	// Check statsStore: should be recorded with [finland-node] prefix
	incidents := statsStore.GetRecentIncidents(10)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident in stats store, got %d", len(incidents))
	}
	if incidents[0].ProxyName != "[finland-node] Proxy 1" {
		t.Errorf("expected proxy name with node prefix '[finland-node] Proxy 1', got %q", incidents[0].ProxyName)
	}
}
