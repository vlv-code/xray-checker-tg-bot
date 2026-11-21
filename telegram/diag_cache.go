package telegram

import (
	"net"
	"sort"
	"strconv"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

func (b *Bot) getCachedDiagnosticsReports() []checker.ProxyDiagReport {
	if b.diagSource == nil {
		return nil
	}
	b.diagMu.Lock()
	defer b.diagMu.Unlock()
	if len(b.cachedDiag) > 0 {
		reports := make([]checker.ProxyDiagReport, len(b.cachedDiag))
		copy(reports, b.cachedDiag)
		return reports
	}
	return nil
}

// DiagCacheTimestamp reports when the diagnostics cache was last filled by
// getDiagnosticsReports (the RunDiagnostics producer). ok=false when no
// report has been cached yet. Wave M9.3: cached serve paths use it to render
// the staleness line «данные от HH:MM».
func (b *Bot) DiagCacheTimestamp() (time.Time, bool) {
	if b.diagSource == nil {
		return time.Time{}, false
	}
	b.diagMu.Lock()
	defer b.diagMu.Unlock()
	if b.cachedDiagAt.IsZero() {
		return time.Time{}, false
	}
	return b.cachedDiagAt, true
}

// diagCacheAgeNote renders the staleness line for CACHED diagnostics outputs
// («\n\n<i>данные от 15:04</i>»); empty when no cache exists. Freshly probed
// renders pass "" — the line belongs to cache-served data only (wave M9.3).
func (b *Bot) diagCacheAgeNote() string {
	ts, ok := b.DiagCacheTimestamp()
	if !ok {
		return ""
	}
	return "\n\n<i>данные от " + ts.In(b.loc()).Format("15:04") + "</i>"
}

func (b *Bot) getDiagnosticsReports(force bool) []checker.ProxyDiagReport {
	if b.diagSource == nil {
		return nil
	}

	b.diagMu.Lock()
	if !force && len(b.cachedDiag) > 0 && time.Since(b.cachedDiagAt) < 60*time.Second {
		reports := make([]checker.ProxyDiagReport, len(b.cachedDiag))
		copy(reports, b.cachedDiag)
		b.diagMu.Unlock()
		return reports
	}
	b.diagMu.Unlock()

	targets := []string{
		"https://cp.cloudflare.com/generate_204",
		"https://www.gstatic.com/generate_204",
	}
	if tm := b.diagSource.GetTargetManager(); tm != nil {
		configured := tm.GetTargets()
		if len(configured) > 0 {
			targets = configured
		}
	}

	reports := b.diagSource.RunDiagnostics(targets)
	b.sortDiagnosticsReports(reports)

	b.diagMu.Lock()
	b.cachedDiag = make([]checker.ProxyDiagReport, len(reports))
	copy(b.cachedDiag, reports)
	b.cachedDiagAt = time.Now()
	b.diagMu.Unlock()

	return reports
}

// getNodeDiagnosticsReports converts metrics for a specific remote node into ProxyDiagReport slice.
func (b *Bot) getNodeDiagnosticsReports(nodeName string) []checker.ProxyDiagReport {
	if b.nodeMgr != nil {
		if diag := b.nodeMgr.NodeDiagReports(nodeName); len(diag) > 0 {
			b.sortDiagnosticsReports(diag)
			return diag
		}
	}

	var snapshot []metrics.ProxyMetric
	if b.nodeMgr != nil {
		snapshot = b.nodeMgr.NodeSnapshot(nodeName)
	}
	if len(snapshot) == 0 && b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.NodeName == nodeName {
				snapshot = append(snapshot, pm)
			}
		}
	}
	if len(snapshot) == 0 {
		return nil
	}

	var reports []checker.ProxyDiagReport
	for _, pm := range snapshot {
		status := "online"
		if pm.Disabled {
			status = "disabled"
		} else if !pm.Online {
			status = "offline"
		}
		targets := []checker.TargetDiagResult{
			{
				URL:     "node-check",
				Latency: time.Duration(pm.LatencyMs * float64(time.Millisecond)),
				Success: pm.Online,
				Error:   pm.LastErrorMsg,
			},
		}
		server, portStr, _ := net.SplitHostPort(pm.Address)
		port, _ := strconv.Atoi(portStr)
		rep := checker.ProxyDiagReport{
			ProxyName: pm.Name,
			Protocol:  pm.Protocol,
			Server:    server,
			Port:      port,
			StableID:  pm.StableID,
			Status:    status,
			Targets:   targets,
			Verdict:   pm.LastErrorMsg,
			Disabled:  pm.Disabled,
		}
		reports = append(reports, rep)
	}
	b.sortDiagnosticsReports(reports)
	return reports
}

func (b *Bot) sortDiagnosticsReports(reports []checker.ProxyDiagReport) {
	now := b.now()
	getSeverityRank := func(rep *checker.ProxyDiagReport) int {
		if rep.Disabled || rep.Status == "disabled" {
			return 4
		}
		if rep.Status == "offline" {
			return 1
		}
		if rep.Status == "degraded" {
			return 2
		}
		return 3 // online
	}

	sort.SliceStable(reports, func(i, j int) bool {
		ri, rj := &reports[i], &reports[j]
		rankI, rankJ := getSeverityRank(ri), getSeverityRank(rj)
		if rankI != rankJ {
			return rankI < rankJ
		}

		if rankI == 1 { // offline: longer downtime first
			var dtI, dtJ time.Duration
			if b.tracker != nil {
				dtI = b.tracker.GetDowntime(ri.StableID, now)
				dtJ = b.tracker.GetDowntime(rj.StableID, now)
			}
			if dtI != dtJ {
				return dtI > dtJ
			}
			return ri.ProxyName < rj.ProxyName
		}

		if rankI == 3 { // online: lower latency first
			var latI, latJ time.Duration
			for _, tr := range ri.Targets {
				if tr.Success {
					latI = tr.Latency
					break
				}
			}
			for _, tr := range rj.Targets {
				if tr.Success {
					latJ = tr.Latency
					break
				}
			}
			if latI != latJ {
				return latI < latJ
			}
			return ri.ProxyName < rj.ProxyName
		}

		return ri.ProxyName < rj.ProxyName
	})
}

func getDeepLinks(reports []checker.ProxyDiagReport, maxCount int) []DiagDeepLink {
	var links []DiagDeepLink
	for _, rep := range reports {
		if rep.Status == "offline" || rep.Status == "degraded" {
			links = append(links, DiagDeepLink{
				Name:     rep.ProxyName,
				StableID: rep.StableID,
			})
			if maxCount > 0 && len(links) >= maxCount {
				break
			}
		}
	}
	return links
}
