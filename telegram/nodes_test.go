package telegram

import (
	"strings"
	"testing"
	"time"
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
