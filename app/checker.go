package app

import (
	"fmt"
	"strings"
	"time"

	"xray-checker/checker"
	"xray-checker/config"
	"xray-checker/logger"
	"xray-checker/metrics"
	"xray-checker/models"
	"xray-checker/nodes"
	"xray-checker/subscription"
	"xray-checker/web"
	"xray-checker/xray"

	"github.com/prometheus/client_golang/prometheus"
)

// initInitialConfigs resolves the starting proxy set (initial fetch with
// cache fallback), logs the load summary and the debug dump.
func (a *App) initInitialConfigs() {
	a.configFile = "xray_config.json"
	a.cachePath = config.CLIConfig.Subscription.CachePath

	proxyConfigs, isDegraded, err := ResolveInitialConfigs(a.configFile, a.cachePath, func() (*[]*models.ProxyConfig, error) {
		return subscription.InitializeConfiguration(a.configFile, a.version, a.SubURLStore.All())
	})
	if err != nil {
		logger.Fatal("Error initializing configuration: %v", err)
	}
	if isDegraded && config.CLIConfig.RunOnce && len(a.SubURLStore.All()) > 0 {
		logger.Fatal("Error initializing configuration in --run-once mode: initial fetch failed")
	}
	a.proxyConfigs = proxyConfigs
	a.isDegraded = isDegraded

	if len(*a.proxyConfigs) == 0 {
		logger.Info("Loaded 0 proxy configurations (waiting for subscriptions)")
	} else {
		logger.Info("Loaded %d proxy configurations", len(*a.proxyConfigs))
	}
	if dynamicURLs := a.SubURLStore.Dynamic(); len(dynamicURLs) > 0 {
		logger.Info("%d subscription(s) previously added via the Telegram bot", len(dynamicURLs))
	}

	if config.CLIConfig.Web.Public {
		if name := subscription.GetSubscriptionName(); name != "" {
			logger.Info("Subscription name for public status page: %s", name)
		}
	} else {
		subNames := web.CollectSubscriptionNames(*a.proxyConfigs)
		if len(subNames) > 0 {
			logger.Info("Subscriptions: %s", strings.Join(subNames, ", "))
		}
	}

	if a.logLevel == logger.LevelDebug {
		logger.Debug("=== Parsed Proxy Configurations ===")
		for _, pc := range *a.proxyConfigs {
			logger.Debug("%s", pc.DebugString())
		}
	}
}

// initChecker builds the proxy checker, the target manager, the shared
// Cheburcheck client and the prometheus registry/collector.
func (a *App) initChecker() {
	a.proxyChecker = checker.NewProxyChecker(
		*a.proxyConfigs,
		config.CLIConfig.Xray.StartPort,
		config.CLIConfig.Proxy.IpCheckUrl,
		config.CLIConfig.Proxy.Timeout,
		config.CLIConfig.Proxy.StatusCheckUrl,
		config.CLIConfig.Proxy.DownloadUrl,
		config.CLIConfig.Proxy.DownloadTimeout,
		config.CLIConfig.Proxy.DownloadMinSize,
		config.CLIConfig.Proxy.CheckMethod,
		config.CLIConfig.Proxy.CheckConcurrency,
	)

	targetMgr := checker.NewTargetManager(config.CLIConfig.Telegram.TargetURLs)
	a.proxyChecker.SetTargetManager(targetMgr)

	// One long-lived Cheburcheck client for the whole process (Guardian P2-1):
	// the diagnostics enrichment below AND the bot's manual probe commands
	// share a single token bucket. Fully inactive while neither the API key
	// nor per-scanner tokens are configured (graceful degradation).
	a.cheburClient = checker.NewCheburcheckClient("", 0, checker.DefaultCheburScanners)
	a.cheburClient.SetAPIKey(config.CLIConfig.Telegram.CheburcheckAPIKey)
	a.proxyChecker.SetCheburClient(a.cheburClient)

	// The collector renders metrics from the checker's current proxy snapshot on
	// each scrape, so custom metricsLabels (#124) can change across subscription
	// updates without resetting other series.
	a.registry = prometheus.NewRegistry()
	a.registry.MustRegister(metrics.NewCollector(config.CLIConfig.Metrics.Instance, a.proxyChecker))
}

// emitSnapshot pushes the combined local + remote snapshot into the bot,
// if one is running. Remote metrics are namespaced with the name of the
// reporting node. ProcessSnapshot drops state for absent IDs, so local and
// remote snapshots must never be fed separately.
func (a *App) emitSnapshot() {
	b := a.tgBot.Load()
	if b == nil {
		return
	}
	snap := a.proxyChecker.MetricsSnapshot()
	if a.NodeRegistry != nil {
		snap = a.NodeRegistry.MergedSnapshot(snap)
	}
	b.ProcessSnapshot(snap)
}

// runCheckIteration is one full proxy check cycle: skip if already running,
// hold the runner RLock, check all proxies, emit the bot snapshot, warn on
// overrun, push metrics, and (node mode) deliver the report to the master.
func (a *App) runCheckIteration() {
	if !a.checkIterationRunning.CompareAndSwap(false, true) {
		logger.Debug("Check iteration already running, skipping")
		return
	}
	defer a.checkIterationRunning.Store(false)

	a.checkRunnerMu.RLock()
	defer a.checkRunnerMu.RUnlock()

	logger.Info("Starting proxy check iteration")
	start := time.Now()
	a.proxyChecker.CheckAllProxies()
	elapsed := time.Since(start)

	a.emitSnapshot()

	interval := CurrentCheckInterval()
	if interval > 0 && elapsed > time.Duration(interval)*time.Second {
		// When a concurrency cap is set, raising it (or the interval) helps. When
		// unlimited (0), the cycle is already as parallel as it gets, so the only
		// useful lever is a longer interval.
		if config.CLIConfig.Proxy.CheckConcurrency > 0 {
			logger.Warn("Check cycle took %s, longer than PROXY_CHECK_INTERVAL=%ds — raise PROXY_CHECK_CONCURRENCY or PROXY_CHECK_INTERVAL", elapsed.Round(time.Second), interval)
		} else {
			logger.Warn("Check cycle took %s, longer than PROXY_CHECK_INTERVAL=%ds — raise PROXY_CHECK_INTERVAL", elapsed.Round(time.Second), interval)
		}
	}

	if config.CLIConfig.Metrics.PushURL != "" {
		pushConfig, err := metrics.ParseURL(config.CLIConfig.Metrics.PushURL)
		if err != nil {
			logger.Error("Error parsing push URL: %v", err)
			return
		}

		if pushConfig != nil {
			if err := metrics.PushMetrics(pushConfig, a.registry); err != nil {
				logger.Error("Error pushing metrics: %v", err)
			}
		}
	}

	if a.reporter != nil {
		// buildReportPayload assembles the node report payload. It is
		// shared by the daemon's fire-and-forget goroutine and the
		// --run-once synchronous path so both build identical payloads.
		buildReportPayload := func() nodes.ReportPayload {
			hostIP, err := a.proxyChecker.GetCurrentIP()
			if err != nil {
				hostIP = ""
			}
			// Lock-free snapshot read (config/snapshot.go): no
			// CheckSchedulerMu wrap anymore — the read never blocks and
			// taking schedulerMu here would only delay a fire-and-forget
			// report behind the shutdown scheduler drain.
			interval := CurrentCheckInterval()
			targets := []string{
				"https://cp.cloudflare.com/generate_204",
				"https://www.gstatic.com/generate_204",
			}
			if tm := a.proxyChecker.GetTargetManager(); tm != nil && len(tm.GetTargets()) > 0 {
				targets = tm.GetTargets()
			}
			a.checkRunnerMu.RLock()
			diagReports := a.proxyChecker.RunDiagnostics(targets)
			metricsSnap := a.proxyChecker.MetricsSnapshot()
			a.checkRunnerMu.RUnlock()

			payload := nodes.BuildReportFromDiagWithMetrics(
				diagReports, metricsSnap, a.version, interval,
				config.CLIConfig.Proxy.CheckMethod, hostIP,
			)
			if a.nodeAuditor != nil {
				payload.CheckHostAudits = a.nodeAuditor.Results()
			}
			return payload
		}

		// deliver sends the report and applies the master's desired
		// state. Daemon mode runs it as a fire-and-forget goroutine;
		// --run-once calls it synchronously: the process returns right
		// after the check iteration, so an async push would be killed
		// mid-send (SendWithRetry can take ~52s) and lose the run's
		// only report.
		deliver := func() {
			released := false
			defer func() {
				if !released {
					a.reporterRunning.Store(false)
				}
			}()
			desired, err := FlushReporterOnce(a.reporter, buildReportPayload())
			a.reporterRunning.Store(false)
			released = true
			if err != nil {
				logger.Warn("Report to master failed (next cycle will retry): %v", err)
				return
			}
			if desired != nil {
				if a.reconcileDesired != nil && len(desired.ManagedSubs) > 0 {
					if err := a.reconcileDesired(desired.ManagedSubs); err != nil {
						logger.Error("Reconciling managed subscriptions failed: %v", err)
					}
				}
				if desired.ConfigSync != nil && desired.ConfigSync.SyncEnabled {
					cs := desired.ConfigSync
					// Full resolved check settings from the master: the
					// payload is the complete configuration, including
					// meaningful zeros (concurrency 0 = unlimited).
					a.proxyChecker.SetRuntimeCheckSettings(nodes.SyncToRuntimeSettings(*cs))
					if cs.CheckIntervalSec > 0 {
						// Lock-free snapshot read (config/snapshot.go).
						if curInterval := CurrentCheckInterval(); curInterval != cs.CheckIntervalSec {
							logger.Info("Agent check interval updated by master: %ds", cs.CheckIntervalSec)
							a.rescheduleChecks(cs.CheckIntervalSec)
						}
					}
					// Resolved target list: empty means the master wants
					// the node back on built-in defaults.
					if tm := a.proxyChecker.GetTargetManager(); tm != nil {
						tm.SetTargets(cs.TargetURLs)
					}
					if cs.SubsUpdateIntervalSec > 0 && a.rescheduleSubscriptionUpdates != nil {
						a.rescheduleSubscriptionUpdates(cs.SubsUpdateIntervalSec)
					}
					if a.nodeAuditor != nil {
						a.nodeAuditor.SetSchedule(cs.CheckHostBgEnabled, cs.CheckHostIntervalHours)
					}
					if len(cs.DisabledHosts) > 0 || len(cs.DisabledProxies) > 0 {
						disHosts := cs.DisabledHosts
						disProxies := cs.DisabledProxies
						a.proxyChecker.SetDisabledFilter(func(server, stableID string) bool {
							for _, h := range disHosts {
								if strings.EqualFold(h, server) {
									return true
								}
							}
							for _, id := range disProxies {
								if id == stableID {
									return true
								}
							}
							return false
						})
					}
				}
			}
		}

		if config.CLIConfig.RunOnce {
			// deliver() re-enters checkRunnerMu.RLock() while this
			// iteration already holds it: safe here because --run-once
			// has no schedulers, no bot and exits before the degraded
			// retry goroutine starts, so no updateConfiguration writer
			// can block between the two read locks.
			deliver()
		} else if a.reporterRunning.CompareAndSwap(false, true) {
			SafeGo("node report push", deliver)
		} else {
			logger.Debug("Previous report to master still in flight, skipping overlapping push")
		}
	}
}

// ReloadSubscriptions re-fetches every subscription in the URL store and,
// if the resulting proxy set differs from the current one, applies it
// (rebuilds the Xray config, restarts Xray, and updates the checker).
// changed reports whether anything was applied; proxyCount is always the
// resulting total regardless. It's used both by the periodic subscription
// updater and by the Telegram bot's /addsub and /delsub (via the
// telegramSubscriptionManager adapter in package main). Exported because the
// main-package adapter factory binds it into the bot-facing manager.
func (a *App) ReloadSubscriptions() (changed bool, proxyCount int, err error) {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	newConfigs, subCounts, failedSrcs, err := subscription.ReadFromMultipleSourcesDetailed(a.SubURLStore.All())
	if err != nil {
		return false, len(*a.proxyConfigs), err
	}
	RecordSubFreshness(a.SubURLStore, subCounts, failedSrcs, time.Now())
	if b := a.tgBot.Load(); b != nil {
		for _, u := range a.SubURLStore.All() {
			if meta, ok := a.SubURLStore.GetMeta(u); ok {
				b.SetSubFreshness(u, meta.Count, meta.PrevCount, meta.Added, meta.Removed, meta.LastUpdate)
			}
		}
	}

	if config.CLIConfig.Proxy.ResolveDomains {
		resolved, rerr := subscription.ResolveDomainsForConfigs(newConfigs)
		if rerr != nil {
			logger.Error("Error resolving domains: %v", rerr)
		} else {
			newConfigs = resolved
		}
	}

	if xray.IsConfigsEqual(*a.proxyConfigs, newConfigs) {
		return false, len(*a.proxyConfigs), nil
	}

	updateErr := func() error {
		a.checkRunnerMu.Lock()
		defer a.checkRunnerMu.Unlock()
		return updateConfiguration(newConfigs, a.proxyConfigs, a.xrayRunner, a.proxyChecker)
	}()
	if updateErr != nil {
		return false, len(*a.proxyConfigs), updateErr
	}

	if len(newConfigs) > 0 {
		if err := subscription.SaveProxyCache(a.cachePath, newConfigs, subscription.GetSubscriptionName()); err != nil {
			logger.Debug("Could not update proxy cache: %v", err)
		}
	}

	// Immediately re-check the new proxy set so /metrics is repopulated
	// right away instead of staying empty until the next scheduled check
	// (up to PROXY_CHECK_INTERVAL), then drop series for removed proxies.
	a.runCheckIteration()
	a.proxyChecker.PruneStaleResults()
	return true, len(*a.proxyConfigs), nil
}

func updateConfiguration(newConfigs []*models.ProxyConfig, currentConfigs *[]*models.ProxyConfig,
	xrayRunner *xray.Runner, proxyChecker *checker.ProxyChecker) error {

	logger.Info("Subscription changed, updating configuration...")

	xray.PrepareProxyConfigs(newConfigs)

	configFile := "xray_config.json"
	configGenerator := xray.NewConfigGenerator()
	validProxies, err := configGenerator.GenerateValidatedConfig(
		newConfigs,
		config.CLIConfig.Xray.StartPort,
		configFile,
		config.CLIConfig.Xray.LogLevel,
	)
	if err != nil {
		return err
	}
	newConfigs = validProxies

	if err := xrayRunner.Stop(); err != nil {
		return err
	}

	if err := startXrayWithRetry(xrayRunner); err != nil {
		return err
	}

	proxyChecker.UpdateProxies(newConfigs)

	*currentConfigs = newConfigs

	if config.CLIConfig.Web.Enabled {
		web.RegisterConfigEndpoints(newConfigs, proxyChecker, config.CLIConfig.Xray.StartPort)
	}

	logger.Info("Configuration updated: %d proxies", len(newConfigs))
	return nil
}

// ResolveInitialConfigs fetches the initial proxy configuration with one
// inline retry, falling back to the persistent proxy cache and finally to a
// degraded empty start (web/bot/nodes stay active while a background retry
// goroutine keeps fetching). Exported for the root resilient_startup pins;
// the logic is unchanged from the former main-package function.
func ResolveInitialConfigs(configFile, cachePath string, initFetcher func() (*[]*models.ProxyConfig, error)) (*[]*models.ProxyConfig, bool, error) {
	var proxyConfigs *[]*models.ProxyConfig
	var initErr error

	for attempt := 1; attempt <= 2; attempt++ {
		proxyConfigs, initErr = initFetcher()
		if initErr == nil {
			break
		}
		if attempt < 2 {
			logger.Warn("Failed to fetch initial subscriptions (attempt %d/2): %v. Retrying in 2s...", attempt, initErr)
			time.Sleep(2 * time.Second)
		}
	}

	if initErr == nil {
		if proxyConfigs != nil && len(*proxyConfigs) > 0 {
			if err := subscription.SaveProxyCache(cachePath, *proxyConfigs, subscription.GetSubscriptionName()); err != nil {
				logger.Debug("Could not save proxy cache: %v", err)
			}
		}
		return proxyConfigs, false, nil
	}

	logger.Warn("Failed to fetch initial subscriptions: %v", initErr)

	// Try loading from persistent cache if available
	cachedProxies, cachedSubName, cacheErr := subscription.LoadProxyCache(cachePath)
	if cacheErr == nil && len(cachedProxies) > 0 {
		logger.Warn("Restored %d proxy configuration(s) from cache (%s)", len(cachedProxies), cachePath)
		if cachedSubName != "" {
			subscription.SetSubscriptionName(cachedSubName)
		}
		builtConfigs, buildErr := subscription.BuildValidatedConfiguration(configFile, cachedProxies)
		if buildErr != nil {
			logger.Error("Failed to build configuration from cached proxies: %v", buildErr)
		} else {
			return builtConfigs, true, nil
		}
	}

	// If no cache or cache build failed, start in degraded mode with 0 proxies
	logger.Warn("Starting in degraded mode with 0 proxies. Web server, Telegram bot, and remote nodes will remain active while subscriptions retry in background.")
	builtConfigs, buildErr := subscription.BuildValidatedConfiguration(configFile, []*models.ProxyConfig{})
	if buildErr != nil {
		return nil, true, fmt.Errorf("generating fallback configuration: %w", buildErr)
	}
	return builtConfigs, true, nil
}

// RecordSubFreshness stamps per-source freshness for the sources that
// actually refreshed this cycle (Wave N-G FIX 2). counts contains the proxy
// count of every source contributing to the merged set — including failed
// sources serving last-known-good cached configs; failed marks the sources
// whose fetch errored this cycle. Those are skipped: stamping
// RecordUpdate(LastUpdate=now) for them would present stale cached proxies
// as freshly fetched in the /subs view. Skipping reads honestly in both
// cases: a source that never succeeded stays unknown (the view omits the
// freshness line for a zero LastUpdate), and a previously updated source
// keeps showing its last real update instead of a false "just now".
func RecordSubFreshness(store *subscription.URLStore, counts map[string]int, failed map[string]bool, now time.Time) {
	for u, cnt := range counts {
		if failed[u] {
			continue
		}
		store.RecordUpdate(u, cnt, now)
	}
}
