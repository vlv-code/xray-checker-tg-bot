package telegram

import (
	"fmt"
	"time"
)

// NodeInfo is the bot's view of one remote checker node. It mirrors
// nodes.NodeHealth but keeps the telegram package decoupled from the nodes
// package (mirrors the SubscriptionManager pattern).
type NodeInfo struct {
	Name         string
	Up           bool
	EverReported bool
	Version      string
	HostIP       string
	ASN          string
	Online       int
	Total        int
	LastReport   time.Time
	IntervalSec  int
}

// ManagedSubInfo is one desired subscription on a node. ProxyCount is taken
// from the node's last report; -1 means no data yet.
type ManagedSubInfo struct {
	URL        string
	ProxyCount int
}

// NodeManager lets the bot inspect remote nodes and edit their desired
// managed subscriptions. Implemented in main over nodes.Registry and
// nodes.NodeSubsStore.
type NodeManager interface {
	// Nodes returns every configured node, name-sorted.
	Nodes() []NodeInfo
	// ManagedSubs returns node's desired subscriptions with last-report counts.
	ManagedSubs(node string) ([]ManagedSubInfo, error)
	// AddSub appends url to node's desired list; applied on next report.
	AddSub(node, url string) error
	// RemoveSub drops url from node's desired list.
	RemoveSub(node, url string) error
	// AddNode registers a new node dynamically with name and token.
	AddNode(name, token string) error
	// RemoveNode unregisters a node dynamically.
	RemoveNode(name string) error
}

// nodeHealthState tracks what the alert loop knows about a node. alerted
// distinguishes "we told the user it's down" from a silent seed (first sight
// or pending node), so a silent-down node never sends a bogus recovery.
type nodeHealthState struct {
	up      bool
	since   time.Time
	alerted bool
}

// SetNodeManager wires the node manager; nil disables node commands.
func (b *Bot) SetNodeManager(m NodeManager) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nodeMgr = m
}

// updateNodeHealthLocked folds a states slice into lastNodeHealth and returns
// the alert texts for down/recovery transitions. Caller holds nodeHealthMu;
// now is pre-localized by the caller. Lazily initializes the map so a
// struct-literal Bot (tests) works.
func (b *Bot) updateNodeHealthLocked(states []NodeInfo, now time.Time) []string {
	if b.lastNodeHealth == nil {
		b.lastNodeHealth = make(map[string]nodeHealthState)
	}
	cfg := b.GetConfig()
	var alerts []string
	seen := make(map[string]bool, len(states))
	for _, st := range states {
		seen[st.Name] = true
		nodeID := "node:" + st.Name
		nodeDisplayName := "🖥 [Нода] " + st.Name

		if b.statsStore != nil {
			b.statsStore.RecordCheck(nodeID, nodeDisplayName, st.Up, 0)
		}

		prev, known := b.lastNodeHealth[st.Name]
		if !known {
			b.lastNodeHealth[st.Name] = nodeHealthState{up: st.Up, since: now}
			if b.statsStore != nil {
				if !st.Up {
					b.statsStore.RecordInitialDown(nodeID, nodeDisplayName, now)
				} else {
					b.statsStore.SyncOnlineState(nodeID, now)
				}
			}
			continue
		}
		asnSuffix := ""
		if st.ASN != "" {
			asnSuffix = " (" + escapeHTML(st.ASN) + ")"
		}
		switch {
		case prev.up && !st.Up:
			b.lastNodeHealth[st.Name] = nodeHealthState{up: false, since: now, alerted: true}
			if b.statsStore != nil {
				b.statsStore.RecordTransition(nodeID, nodeDisplayName, false, "Потеря связи с чекер-нодой", now)
			}
			if cfg.NodeAlertsEnabled {
				timeStr := now.Format("15:04")
				alerts = append(alerts, fmt.Sprintf("🔴 <b>Нода %s</b> недоступна%s\n⏱ %s",
					escapeHTML(st.Name), asnSuffix, timeStr))
			}
		case !prev.up && st.Up:
			b.lastNodeHealth[st.Name] = nodeHealthState{up: true, since: now}
			var downtime time.Duration
			if b.statsStore != nil {
				downtime = b.statsStore.RecordTransition(nodeID, nodeDisplayName, true, "", now)
			} else {
				downtime = now.Sub(prev.since)
			}
			if prev.alerted && cfg.NodeAlertsEnabled {
				alerts = append(alerts, fmt.Sprintf("✅ <b>Нода %s</b> вернулась%s\n⏱ простой: %s",
					escapeHTML(st.Name), asnSuffix, FormatDowntime(downtime)))
			}
		}
	}
	for name := range b.lastNodeHealth {
		if !seen[name] {
			delete(b.lastNodeHealth, name)
		}
	}
	return alerts
}

// ProcessNodesHealth alerts on node up/down transitions. Node alerts are
// infrastructure-level and bypass quiet hours; send failures are logged by
// sendAndReturn and skipped so one bad chat can't stall the loop.
func (b *Bot) ProcessNodesHealth(states []NodeInfo) {
	if len(states) == 0 {
		return
	}
	b.nodeHealthMu.Lock()
	alerts := b.updateNodeHealthLocked(states, b.now())
	b.nodeHealthMu.Unlock()

	for _, text := range alerts {
		for _, t := range b.targets {
			_, _ = b.sendAndReturn(t, text)
		}
	}
}
