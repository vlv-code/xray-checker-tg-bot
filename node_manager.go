package main

import (
	"fmt"
	"strings"

	"xray-checker/checker"
	"xray-checker/metrics"
	"xray-checker/nodes"
	"xray-checker/telegram"
)

// nodeManagerAdapter implements telegram.NodeManager over the master's
// nodes.Registry (live state) and nodes.NodeSubsStore (desired state).
type nodeManagerAdapter struct {
	reg  *nodes.Registry
	subs *nodes.NodeSubsStore
}

func toNodeInfos(hs []nodes.NodeHealth) []telegram.NodeInfo {
	out := make([]telegram.NodeInfo, 0, len(hs))
	for _, h := range hs {
		out = append(out, telegram.NodeInfo{
			Name: h.Name, Up: h.Up, EverReported: h.EverReported,
			Version: h.Version, HostIP: h.HostIP, ASN: h.ASN,
			Online: h.Online, Total: h.Total, LastReport: h.LastReport,
			IntervalSec: h.CheckIntervalSec,
		})
	}
	return out
}

func (a *nodeManagerAdapter) Nodes() []telegram.NodeInfo {
	return toNodeInfos(a.reg.HealthSnapshot())
}

func (a *nodeManagerAdapter) ManagedSubs(node string) ([]telegram.ManagedSubInfo, error) {
	if !a.reg.NodeExists(node) {
		return nil, fmt.Errorf("неизвестная нода: %s", node)
	}
	counts := a.reg.SubCounts(node)
	urls := a.subs.Get(node)
	out := make([]telegram.ManagedSubInfo, 0, len(urls))

	totalFromCounts := 0
	for _, c := range counts {
		totalFromCounts += c
	}

	everReported := false
	totalProxies := totalFromCounts
	for _, h := range a.reg.HealthSnapshot() {
		if h.Name == node {
			everReported = h.EverReported
			if totalProxies == 0 && everReported {
				totalProxies = h.Total
			}
			break
		}
	}

	for _, u := range urls {
		count := -1
		frag := ""
		if idx := strings.LastIndex(u, "#"); idx != -1 {
			frag = u[idx+1:]
		}
		if c, ok := counts[u]; ok {
			count = c
		} else if frag != "" && counts[frag] > 0 {
			count = counts[frag]
		} else if len(urls) == 1 && everReported {
			count = totalProxies
		}
		out = append(out, telegram.ManagedSubInfo{URL: u, ProxyCount: count})
	}
	return out, nil
}

func (a *nodeManagerAdapter) AddSub(node, url string) error {
	if !a.reg.NodeExists(node) {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	added, err := a.subs.Add(node, url)
	if err != nil {
		return err
	}
	if !added {
		return fmt.Errorf("эта подписка уже назначена ноде")
	}
	return nil
}

func (a *nodeManagerAdapter) RemoveSub(node, url string) error {
	if !a.reg.NodeExists(node) {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	removed, err := a.subs.Remove(node, url)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("подписка не найдена среди назначенных ноде")
	}
	return nil
}

func (a *nodeManagerAdapter) AddNode(name, token string) error {
	return a.reg.RegisterNode(nodes.NodeConfig{Name: name, Token: token})
}

func (a *nodeManagerAdapter) RemoveNode(name string) error {
	return a.reg.RemoveNode(name)
}

func (a *nodeManagerAdapter) NodeSnapshot(node string) []metrics.ProxyMetric {
	return a.reg.NodeSnapshot(node)
}

func (a *nodeManagerAdapter) NodeDiagReports(node string) []checker.ProxyDiagReport {
	return a.reg.NodeDiagReports(node)
}

// mergedMetricsSource combines local master metrics with metrics reported by remote nodes.
type mergedMetricsSource struct {
	local metrics.MetricsSource
	reg   *nodes.Registry
}

func (m *mergedMetricsSource) MetricsSnapshot() []metrics.ProxyMetric {
	var local []metrics.ProxyMetric
	if m.local != nil {
		local = m.local.MetricsSnapshot()
	}
	if m.reg != nil {
		return m.reg.MergedSnapshot(local)
	}
	return local
}
