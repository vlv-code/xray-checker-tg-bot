package app

import (
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"xray-checker/asn"
	"xray-checker/checker"
	"xray-checker/config"
	"xray-checker/logger"
	"xray-checker/nodes"
	"xray-checker/subscription"
	"xray-checker/telegram"
)

// initMasterTopology sets up the master-mode node registry, per-node
// subscription/settings stores and the ASN lookup. No-op on a reporting node
// (Report.URL != ""). The former main() registered db.Close() and later the
// auditor Stop as defers right here; Run now registers both in the same
// relative positions of the LIFO chain.
func (a *App) initMasterTopology() {
	if config.CLIConfig.Report.URL == "" {
		var nodeCfgs []nodes.NodeConfig
		if len(config.CLIConfig.Nodes.List) > 0 {
			var err error
			nodeCfgs, err = nodes.ParseNodes(config.CLIConfig.Nodes.List)
			if err != nil {
				logger.Fatal("Invalid NODES entry: %v", err)
			}
		}
		subsStorePath := config.CLIConfig.Nodes.StorePath
		if subsStorePath == "" {
			subsStorePath = "node_subs.json"
		}
		var err error
		a.NodeSubsStore, err = nodes.NewNodeSubsStore(subsStorePath)
		if err != nil {
			logger.Warn("Error loading node subscriptions store: %v", err)
		}
		nodesListPath := filepath.Join(filepath.Dir(subsStorePath), "nodes.json")
		a.NodesStore, err = nodes.NewNodesStore(nodesListPath)
		if err != nil {
			logger.Warn("Error loading dynamic nodes store: %v", err)
		}
		asnPath := "geo/asn.mmdb"
		if err := asn.EnsureDB(asnPath, config.CLIConfig.ASN.DBURL); err != nil {
			logger.Warn("ASN database unavailable (node ASN will be empty): %v", err)
		} else if db, oerr := asn.Open(asnPath); oerr != nil {
			logger.Warn("ASN database failed to open (node ASN will be empty): %v", oerr)
		} else {
			a.asnDB = db // Run registers db.Close() in the original defer position
			a.asnLookup = db.Lookup
		}
		a.NodeRegistry = nodes.NewRegistry(nodeCfgs, a.NodeSubsStore, a.asnLookup)
		if config.CLIConfig.Nodes.StaleTimeoutSec > 0 {
			a.NodeRegistry.SetStaleCap(time.Duration(config.CLIConfig.Nodes.StaleTimeoutSec) * time.Second)
			logger.Info("Master node stale timeout cap: %ds", config.CLIConfig.Nodes.StaleTimeoutSec)
		}
		if a.NodesStore != nil {
			a.NodeRegistry.SetNodesStore(a.NodesStore)
		}
		dynCount := 0
		if a.NodesStore != nil {
			dynCount = len(a.NodesStore.All())
		}
		logger.Info("Remote nodes configured: %d (static %d, dynamic %d)",
			len(a.NodeRegistry.HealthSnapshot()), len(nodeCfgs), dynCount)
		if note := MasterReportSecurityNote(config.CLIConfig.Nodes.MasterPublicURL); note != "" {
			logger.Warn("%s", note)
		}
	}
}

// initReporterAndAuditor creates the node-mode reporter and the node's own
// background Check-Host auditor (audit ownership split: the master audits
// only its local hosts, each node audits its own). Schedule arrives via
// config sync; the node's env is the bootstrap until the first report.
func (a *App) initReporterAndAuditor() {
	if config.CLIConfig.Report.URL != "" {
		a.reporter = nodes.NewReporter(config.CLIConfig.Report.URL, config.CLIConfig.Report.Token)
	}

	if a.reporter != nil {
		a.nodeAuditor = nodes.NewAuditor(nil, func() []nodes.AuditTarget {
			seen := make(map[string]bool)
			var out []nodes.AuditTarget
			for _, p := range a.proxyChecker.GetProxies() {
				if a.proxyChecker.IsProxyDisabled(p) {
					continue
				}
				// The Check-Host API cannot verify UDP services: UDP-protocol
				// proxies are excluded from passive audits entirely (user
				// decision 2026-11-16).
				if checker.IsUDPProto(p.Protocol) {
					continue
				}
				dedupKey := net.JoinHostPort(p.Server, strconv.Itoa(p.Port))
				if seen[dedupKey] {
					continue
				}
				seen[dedupKey] = true
				out = append(out, nodes.AuditTarget{
					Address: dedupKey,
					Host:    p.Server,
					Name:    p.Name,
				})
			}
			return out
		})
		a.nodeAuditor.SetSchedule(config.CLIConfig.Telegram.CheckHostBgEnabled, config.CLIConfig.Telegram.CheckHostIntervalHours)
		// Run registers nodeAuditor.Stop() in the original defer position.
	}
}

// initNodeCallbacks registers the registry → bot push callbacks (health
// updates, per-node audits) and the per-node config-source resolver that
// computes what the master syncs to each node.
func (a *App) initNodeCallbacks() {
	if a.NodeRegistry != nil {
		a.NodeRegistry.SetOnUpdate(func(nodeName string) {
			a.emitSnapshot()
			if b := a.tgBot.Load(); b != nil {
				b.ProcessNodesHealth(ToNodeInfos(a.NodeRegistry.HealthSnapshot()))
				if nodeName != "" {
					b.ProcessNodeCheckHostAudits(nodeName, a.NodeRegistry.NodeAudits(nodeName))
				}
			}
		})
		a.NodeRegistry.SetConfigSource(func(nodeName string) *nodes.NodeConfigSync {
			base := func() *nodes.NodeConfigSync {
				if b := a.tgBot.Load(); b != nil {
					cfg := b.GetConfig()
					prefix := nodeName + "/"
					disabledForNode := make([]string, 0, len(cfg.DisabledProxies))
					for _, id := range cfg.DisabledProxies {
						if strings.HasPrefix(id, prefix) {
							disabledForNode = append(disabledForNode, strings.TrimPrefix(id, prefix))
						} else if !strings.Contains(id, "/") {
							disabledForNode = append(disabledForNode, id)
						}
					}
					return &nodes.NodeConfigSync{
						SyncEnabled:            cfg.NodeSyncEnabled,
						DisabledProxies:        disabledForNode,
						DisabledHosts:          cfg.DisabledHosts,
						CheckHostBgEnabled:     cfg.CheckHostBgEnabled,
						CheckHostIntervalHours: cfg.CheckHostIntervalHours,
						CheckIntervalSec:       cfg.CheckIntervalSec,
						TargetURLs:             cfg.TargetURLs,
						QuietHoursEnabled:      cfg.QuietHoursEnabled,
						AlertMode:              cfg.AlertMode,
						NodeAlertsEnabled:      cfg.NodeAlertsEnabled,
						NodeProxyAlertsChat:    cfg.NodeProxyAlertsChat,
						NodeStaleTimeoutSec:    cfg.NodeStaleTimeoutSec,
					}
				}
				// Lock-free snapshot read (config/snapshot.go): CheckInterval
				// is a runtime-mutated field served from the atomic snapshot.
				checkInterval := CurrentCheckInterval()
				return &nodes.NodeConfigSync{
					SyncEnabled:         true,
					CheckIntervalSec:    checkInterval,
					TargetURLs:          config.CLIConfig.Telegram.TargetURLs,
					QuietHoursEnabled:   config.CLIConfig.Telegram.QuietHoursEnabled,
					AlertMode:           config.CLIConfig.Telegram.AlertMode,
					NodeAlertsEnabled:   true,
					NodeProxyAlertsChat: true,
					NodeStaleTimeoutSec: 300,
				}
			}()

			// Full resolved check settings ride along on every sync: the master's
			// effective configuration is the inheritance base for every node.
			base.CheckMethod = config.CLIConfig.Proxy.CheckMethod
			base.IpCheckURL = config.CLIConfig.Proxy.IpCheckUrl
			base.StatusCheckURL = config.CLIConfig.Proxy.StatusCheckUrl
			base.DownloadURL = config.CLIConfig.Proxy.DownloadUrl
			base.ProxyTimeoutSec = config.CLIConfig.Proxy.Timeout
			base.DownloadTimeoutSec = config.CLIConfig.Proxy.DownloadTimeout
			base.DownloadMinSize = config.CLIConfig.Proxy.DownloadMinSize
			base.CheckConcurrency = config.CLIConfig.Proxy.CheckConcurrency
			// Lock-free snapshot read (config/snapshot.go).
			base.SubsUpdateIntervalSec = CurrentSubscriptionUpdateInterval()

			// Per-node overrides win over the master's base values.
			if a.NodesStore != nil {
				if ns, ok := a.NodesStore.Settings(nodeName); ok {
					merged := nodes.ResolveNodeSync(*base, ns)
					return &merged
				}
			}
			return base
		})
	}
}

// initReconciler assigns the master's desired managed-subscription applier
// (former main() closure, assigned at the same wiring point). It stays a
// nil-able func field because the report-delivery path nil-checks it — the
// assignment happens during Build, before anything can invoke it, exactly
// like the former closure variable.
func (a *App) initReconciler() {
	a.reconcileDesired = func(desired []string) error {
		changed, err := subscription.ReconcileManaged(a.SubURLStore, desired, a.ReloadSubscriptions, subscription.DefaultSubscriptionValidator)
		if err == nil && changed {
			logger.Info("Subscriptions updated via master reconciliation, check iteration completed")
		}
		return err
	}
}

// FlushReporterOnce synchronously delivers one node report to the master,
// retrying transient failures with nodes.DefaultReportBackoffs. In
// --run-once mode the process exits right after the check iteration, so the
// report must be delivered before return — the fire-and-forget goroutine
// used in daemon mode would be killed mid-send (SendWithRetry can retry for
// ~52s) and the node's report for that run would be lost.
func FlushReporterOnce(reporter *nodes.Reporter, payload nodes.ReportPayload) (*nodes.IngestResponse, error) {
	return reporter.SendWithRetry(payload, nodes.DefaultReportBackoffs)
}

// MasterReportSecurityNote returns the startup warning for the plaintext
// node-report fallback, or "" when no warning is needed. With
// MASTER_PUBLIC_URL unset, /nodeadd instructions fall back to
// http://<host-ip>:2112, so node tokens would ride cleartext.
func MasterReportSecurityNote(masterPublicURL string) string {
	if masterPublicURL != "" {
		return ""
	}
	return "MASTER_PUBLIC_URL is not set: nodes will be instructed to report over plaintext http://<host-ip>:2112 — consider TLS fronting"
}

// ToNodeInfos maps registry health snapshots into the bot-facing NodeInfo
// list. Exported because both the app wiring and the main-package
// nodeManagerAdapter (pinned by node_manager_test.go, which stays in main)
// need the same mapping.
func ToNodeInfos(hs []nodes.NodeHealth) []telegram.NodeInfo {
	out := make([]telegram.NodeInfo, 0, len(hs))
	for _, h := range hs {
		out = append(out, telegram.NodeInfo{
			Name: h.Name, Up: h.Up, EverReported: h.EverReported,
			Version: h.Version, HostIP: h.HostIP, ASN: h.ASN,
			Online: h.Online, Total: h.Total, LastReport: h.LastReport,
			DisabledProxies: h.Disabled,
			IntervalSec:     h.CheckIntervalSec,
			Disabled:        !h.Enabled,
		})
	}
	return out
}
