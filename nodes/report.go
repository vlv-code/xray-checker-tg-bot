package nodes

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// ReportProxy is one proxy result inside a node report. Field set mirrors
// what the master needs for alerts; custom labels are not transported (v1).
type ReportProxy struct {
	StableID          string                     `json:"stableId"`
	Name              string                     `json:"name"`
	SubName           string                     `json:"subName"`
	GroupName         string                     `json:"groupName"`
	Protocol          string                     `json:"protocol"`
	Address           string                     `json:"address"`
	Online            bool                       `json:"online"`
	Disabled          bool                       `json:"disabled"`
	LatencyMs         float64                    `json:"latencyMs"`
	LastCheck         int64                      `json:"lastCheck"`
	LastErrorCategory int                        `json:"lastErrorCategory"`
	LastErrorMsg      string                     `json:"lastErrorMsg"`
	NodeHealth        *checker.NodeHealth        `json:"nodeHealth,omitempty"`
	Targets           []checker.TargetDiagResult `json:"targets,omitempty"`
	Verdict           string                     `json:"verdict,omitempty"`
	CheckHost         *checker.CheckHostSummary  `json:"checkHost,omitempty"`
}

// ReportPayload is the body a node POSTs to the master after each check cycle.
type ReportPayload struct {
	Version          string        `json:"version"`
	CheckIntervalSec int           `json:"checkIntervalSec"`
	CheckMethod      string        `json:"checkMethod"`
	HostIP           string        `json:"hostIP"`
	Proxies          []ReportProxy `json:"proxies"`
}

// NodeConfigSync contains runtime settings transmitted from the master bot to nodes.
type NodeConfigSync struct {
	SyncEnabled            bool     `json:"syncEnabled"`
	DisabledProxies        []string `json:"disabledProxies,omitempty"`
	DisabledHosts          []string `json:"disabledHosts,omitempty"`
	CheckHostBgEnabled     bool     `json:"checkHostBgEnabled"`
	CheckHostIntervalHours int      `json:"checkHostIntervalHours,omitempty"`
	CheckIntervalSec       int      `json:"checkIntervalSec,omitempty"`
	TargetURLs             []string `json:"targetUrls,omitempty"`
	QuietHoursEnabled      bool     `json:"quietHoursEnabled"`
	AlertMode              string   `json:"alertMode,omitempty"`
	NodeAlertsEnabled      bool     `json:"nodeAlertsEnabled"`
	NodeProxyAlertsChat    bool     `json:"nodeProxyAlertsChat"`
	NodeStaleTimeoutSec    int      `json:"nodeStaleTimeoutSec,omitempty"`
}

// IngestResponse is the master's reply: the full desired list of managed
// subscription URLs and optional synchronized runtime settings for the reporting node.
type IngestResponse struct {
	ManagedSubs []string        `json:"managedSubs"`
	ConfigSync  *NodeConfigSync `json:"configSync,omitempty"`
}

// BuildReport converts a local check snapshot into the wire payload.
// Node-scoped fields are dropped: they only exist on the master after ingest.
func BuildReport(pm []metrics.ProxyMetric, version string, intervalSec int, checkMethod, hostIP string) ReportPayload {
	p := ReportPayload{
		Version:          version,
		CheckIntervalSec: intervalSec,
		CheckMethod:      checkMethod,
		HostIP:           hostIP,
		Proxies:          make([]ReportProxy, 0, len(pm)),
	}
	for _, m := range pm {
		p.Proxies = append(p.Proxies, ReportProxy{
			StableID:          m.StableID,
			Name:              m.Name,
			SubName:           m.SubName,
			GroupName:         m.GroupName,
			Protocol:          m.Protocol,
			Address:           m.Address,
			Online:            m.Online,
			Disabled:          m.Disabled,
			LatencyMs:         m.LatencyMs,
			LastCheck:         m.LastCheckSec,
			LastErrorCategory: m.LastErrorCategory,
			LastErrorMsg:      m.LastErrorMsg,
		})
	}
	return p
}

// ProxyMetricsFromReport maps an accepted report into master-side metrics,
// namespacing every identity under the reporting node so remote proxies never
// collide with local ones or across nodes.
func ProxyMetricsFromReport(nodeName, nodeASN string, p ReportPayload) []metrics.ProxyMetric {
	out := make([]metrics.ProxyMetric, 0, len(p.Proxies))
	for _, rp := range p.Proxies {
		out = append(out, metrics.ProxyMetric{
			Protocol:          rp.Protocol,
			Address:           rp.Address,
			Name:              rp.Name,
			SubName:           rp.SubName,
			StableID:          nodeName + "/" + rp.StableID,
			GroupName:         rp.GroupName,
			Online:            rp.Online,
			Disabled:          rp.Disabled,
			LatencyMs:         rp.LatencyMs,
			LastErrorCategory: rp.LastErrorCategory,
			LastErrorMsg:      rp.LastErrorMsg,
			LastCheckSec:      rp.LastCheck,
			NodeName:          nodeName,
			NodeASN:           nodeASN,
		})
	}
	return out
}

// BuildReportFromDiag converts a slice of ProxyDiagReport into a wire ReportPayload.
func BuildReportFromDiag(
	reports []checker.ProxyDiagReport,
	version string,
	intervalSec int,
	checkMethod, hostIP string,
) ReportPayload {
	return BuildReportFromDiagWithMetrics(reports, nil, version, intervalSec, checkMethod, hostIP)
}

// BuildReportFromDiagWithMetrics converts a slice of ProxyDiagReport into a wire ReportPayload,
// enriching proxies with subscription metadata (SubName, GroupName, LastErrorCategory) from
// the provided metrics snapshot when available.
func BuildReportFromDiagWithMetrics(
	reports []checker.ProxyDiagReport,
	snap []metrics.ProxyMetric,
	version string,
	intervalSec int,
	checkMethod, hostIP string,
) ReportPayload {
	p := ReportPayload{
		Version:          version,
		CheckIntervalSec: intervalSec,
		CheckMethod:      checkMethod,
		HostIP:           hostIP,
		Proxies:          make([]ReportProxy, 0, len(reports)),
	}

	snapMap := make(map[string]metrics.ProxyMetric, len(snap))
	for _, m := range snap {
		snapMap[m.StableID] = m
	}

	now := time.Now().Unix()
	for _, rep := range reports {
		online := rep.Status == "online" || rep.Status == "degraded"
		var latencyMs float64
		for _, tr := range rep.Targets {
			if tr.Success {
				latencyMs = float64(tr.Latency.Milliseconds())
				break
			}
		}
		addr := rep.Server
		if rep.Port > 0 {
			addr = net.JoinHostPort(rep.Server, fmt.Sprintf("%d", rep.Port))
		}
		nh := rep.NodeHealth
		rp := ReportProxy{
			StableID:     rep.StableID,
			Name:         rep.ProxyName,
			Protocol:     rep.Protocol,
			Address:      addr,
			Online:       online,
			Disabled:     rep.Disabled,
			LatencyMs:    latencyMs,
			LastCheck:    now,
			LastErrorMsg: rep.Verdict,
			NodeHealth:   &nh,
			Targets:      rep.Targets,
			Verdict:      rep.Verdict,
			CheckHost:    rep.CheckHost,
		}
		if m, ok := snapMap[rep.StableID]; ok {
			rp.SubName = m.SubName
			rp.GroupName = m.GroupName
			rp.LastErrorCategory = m.LastErrorCategory
			if m.LastCheckSec > 0 {
				rp.LastCheck = m.LastCheckSec
			}
		}
		p.Proxies = append(p.Proxies, rp)
	}
	return p
}

// ProxyDiagReportsFromReport converts wire report proxies into a slice of checker.ProxyDiagReport.
func ProxyDiagReportsFromReport(p ReportPayload) []checker.ProxyDiagReport {
	out := make([]checker.ProxyDiagReport, 0, len(p.Proxies))
	for _, rp := range p.Proxies {
		status := "online"
		if rp.Disabled {
			status = "disabled"
		} else if !rp.Online {
			status = "offline"
		}
		var nh checker.NodeHealth
		if rp.NodeHealth != nil {
			nh = *rp.NodeHealth
		}
		server, portStr, _ := net.SplitHostPort(rp.Address)
		port, _ := strconv.Atoi(portStr)
		if server == "" && rp.Address != "" {
			server = rp.Address
		}

		targets := rp.Targets
		if len(targets) == 0 && rp.LatencyMs > 0 {
			targets = []checker.TargetDiagResult{
				{
					URL:     "node-check",
					Latency: time.Duration(rp.LatencyMs * float64(time.Millisecond)),
					Success: rp.Online,
					Error:   rp.LastErrorMsg,
				},
			}
		}

		verdict := rp.Verdict
		if verdict == "" {
			verdict = rp.LastErrorMsg
		}

		rep := checker.ProxyDiagReport{
			ProxyName:  rp.Name,
			Protocol:   rp.Protocol,
			Server:     server,
			Port:       port,
			StableID:   rp.StableID,
			Status:     status,
			NodeHealth: nh,
			Targets:    targets,
			Verdict:    verdict,
			Disabled:   rp.Disabled,
			CheckHost:  rp.CheckHost,
		}
		out = append(out, rep)
	}
	return out
}
