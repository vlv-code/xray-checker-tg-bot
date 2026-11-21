package telegram

import (
	"fmt"
)

// nodeHealthBadge renders the binary failure indicator shared by node tabs
// and /nodes: «⚠️» only when ACTIVE proxies are failing (manually disabled ⏸
// proxies are never failures); empty when everything active is up. The
// detailed per-proxy state lives inside the tab/screen itself (user decision:
// «просто — есть падения / нет падений»).
func nodeHealthBadge(online, total, disabled int) string {
	active := total - disabled
	if active > 0 && online < active {
		return "⚠️"
	}
	return ""
}

// degradedLabel is the single RU wording for the «degraded» verdict across
// all diagnostic surfaces (per-proxy detail line, L1 summary, rich table):
// the tunnel is up but the target behind it is unreachable.
func degradedLabel() string {
	return "туннель работает, но цель недоступна"
}

// getNodeTabs returns the navigation tabs for Master and remote nodes.
func (b *Bot) getNodeTabs(activeTarget string) []NodeTabItem {
	if b.nodeMgr == nil {
		return nil
	}
	nodes := b.nodeMgr.Nodes()
	if len(nodes) == 0 {
		return nil
	}

	masterOnline := 0
	masterTotal := 0
	masterDisabled := 0
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.NodeName == "" {
				masterTotal++
				if pm.Disabled {
					masterDisabled++
				} else if pm.Online {
					masterOnline++
				}
			}
		}
	}

	masterActive := activeTarget == "local" || activeTarget == ""
	tabs := []NodeTabItem{
		{
			ID:       "local",
			Label:    "🏠 Мастер",
			Status:   nodeHealthBadge(masterOnline, masterTotal, masterDisabled),
			IsActive: masterActive,
		},
	}

	for _, n := range nodes {
		badge := ""
		if !n.Up {
			badge = "🔴 оффлайн"
		} else {
			badge = nodeHealthBadge(n.Online, n.Total, n.DisabledProxies)
		}

		tabs = append(tabs, NodeTabItem{
			ID:       n.Name,
			Label:    fmt.Sprintf("🖥 %s", n.Name),
			Status:   badge,
			IsActive: activeTarget == n.Name,
		})
	}
	return tabs
}

// getNodeASN returns the ASN of a remote node by name, or empty string.
func (b *Bot) getNodeASN(name string) string {
	if b.nodeMgr == nil {
		return ""
	}
	for _, n := range b.nodeMgr.Nodes() {
		if n.Name == name {
			return n.ASN
		}
	}
	return ""
}
