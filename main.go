package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"xray-checker/asn"
	"xray-checker/checker"
	"xray-checker/config"
	"xray-checker/logger"
	"xray-checker/metrics"
	"xray-checker/models"
	"xray-checker/nodes"
	"xray-checker/subscription"
	"xray-checker/telegram"
	"xray-checker/web"
	"xray-checker/xray"

	"github.com/go-co-op/gocron"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	version   = "unknown"
	startTime = time.Now()
)

func main() {
	config.Parse(version)

	logLevel := logger.ParseLevel(config.CLIConfig.LogLevel)
	logger.SetLevel(logLevel)

	logger.Startup("Xray Checker %s", version)
	if logLevel == logger.LevelNone {
		logger.Startup("Log level: none (silent mode)")
	}

	if config.CLIConfig.Web.Enabled {
		if err := web.InitAssetLoader(config.CLIConfig.Web.CustomAssetsPath); err != nil {
			logger.Fatal("Failed to initialize custom assets: %v", err)
		}
	}

	geoManager := xray.NewGeoFileManager("")
	if err := geoManager.EnsureGeoFiles(); err != nil {
		// Geo databases are optional for the generated config: routing
		// rules are tag-based and never reference geoip:/geosite:
		// categories, so Xray runs fine without them. A failed
		// download (unreachable github.com, read-only ./geo bind
		// mount) must not crash-loop the instance.
		logger.Warn("Geo files unavailable (continuing without them): %v", err)
	}

	// subURLStore holds every subscription URL the checker fetches from: the
	// static ones from --subscription-url/env, plus any added at runtime via
	// the Telegram bot's /addsub (persisted to disk so they survive a
	// restart). It's consulted instead of config.CLIConfig.Subscription.URLs
	// everywhere subscriptions are (re)loaded.
	subURLStore, err := subscription.NewURLStore(config.CLIConfig.Subscription.URLs, config.CLIConfig.Subscription.StorePath)
	if err != nil {
		logger.Fatal("Error loading subscription store: %v", err)
	}

	var nodeRegistry *nodes.Registry
	var nodeSubsStore *nodes.NodeSubsStore
	if len(config.CLIConfig.Nodes.List) > 0 {
		nodeCfgs, err := nodes.ParseNodes(config.CLIConfig.Nodes.List)
		if err != nil {
			logger.Fatal("Invalid NODES entry: %v", err)
		}
		nodeSubsStore, err = nodes.NewNodeSubsStore(config.CLIConfig.Nodes.StorePath)
		if err != nil {
			logger.Fatal("Error loading node subscriptions store: %v", err)
		}
		asnLookup := asn.NopLookup
		asnPath := "geo/asn.mmdb"
		if err := asn.EnsureDB(asnPath, config.CLIConfig.ASN.DBURL); err != nil {
			logger.Warn("ASN database unavailable (node ASN will be empty): %v", err)
		} else if db, oerr := asn.Open(asnPath); oerr != nil {
			logger.Warn("ASN database failed to open (node ASN will be empty): %v", oerr)
		} else {
			defer db.Close()
			asnLookup = db.Lookup
		}
		nodeRegistry = nodes.NewRegistry(nodeCfgs, nodeSubsStore, asnLookup)
		logger.Info("Remote nodes configured: %d", len(nodeCfgs))
	}

	configFile := "xray_config.json"
	proxyConfigs, err := subscription.InitializeConfiguration(configFile, version, subURLStore.All())
	if err != nil {
		logger.Fatal("Error initializing configuration: %v", err)
	}

	logger.Info("Loaded %d proxy configurations", len(*proxyConfigs))
	if dynamicURLs := subURLStore.Dynamic(); len(dynamicURLs) > 0 {
		logger.Info("%d subscription(s) previously added via the Telegram bot", len(dynamicURLs))
	}

	if config.CLIConfig.Web.Public {
		if name := subscription.GetSubscriptionName(); name != "" {
			logger.Info("Subscription name for public status page: %s", name)
		}
	} else {
		subNames := web.CollectSubscriptionNames(*proxyConfigs)
		if len(subNames) > 0 {
			logger.Info("Subscriptions: %s", strings.Join(subNames, ", "))
		}
	}

	if logLevel == logger.LevelDebug {
		logger.Debug("=== Parsed Proxy Configurations ===")
		for _, pc := range *proxyConfigs {
			logger.Debug("%s", pc.DebugString())
		}
	}

	xrayRunner := xray.NewRunner(configFile)
	if err := xrayRunner.Start(); err != nil {
		logger.Fatal("Error starting Xray: %v", err)
	}

	defer func() {
		if err := xrayRunner.Stop(); err != nil {
			logger.Error("Error stopping Xray: %v", err)
		}
	}()

	proxyChecker := checker.NewProxyChecker(
		*proxyConfigs,
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
	proxyChecker.SetTargetManager(targetMgr)

	// The collector renders metrics from the checker's current proxy snapshot on
	// each scrape, so custom metricsLabels (#124) can change across subscription
	// updates without resetting other series.
	registry := prometheus.NewRegistry()
	registry.MustRegister(metrics.NewCollector(config.CLIConfig.Metrics.Instance, proxyChecker))

	// tgBot, runCheckIteration and reloadSubscriptions are declared/defined
	// before the Telegram bot is created below, since the bot's /addsub and
	// /delsub handlers (wired in via telegramSubscriptionManager) need
	// reloadSubscriptions, which itself calls runCheckIteration.
	var tgBot *telegram.Bot
	var checkScheduler *gocron.Scheduler
	var checkSchedulerMu sync.Mutex

	var reporter *nodes.Reporter
	var reporterRunning atomic.Bool
	if config.CLIConfig.Report.URL != "" {
		reporter = nodes.NewReporter(config.CLIConfig.Report.URL, config.CLIConfig.Report.Token)
	}

	// reconcileDesired applies the master's desired managed-subscription list
	// on the node side; assigned after reloadSubscriptions is defined below.
	var reconcileDesired func(desired []string) error

	var runCheckIteration func()

	rescheduleChecks := func(seconds int) {
		checkSchedulerMu.Lock()
		defer checkSchedulerMu.Unlock()

		if seconds < 10 {
			seconds = 10
		}
		config.CLIConfig.Proxy.CheckInterval = seconds

		if checkScheduler != nil {
			checkScheduler.Clear()
			checkScheduler.Every(seconds).Seconds().SingletonMode().Do(runCheckIteration)
			logger.Info("Check interval updated to %ds", seconds)
		}
	}

	// checkRunnerMu synchronizes proxy check iterations with runner restarts.
	// Check cycles hold an RLock while making connections through Xray; configuration
	// updates hold a Lock while stopping and restarting the runner to prevent connection
	// resets and false down alerts during subscription reloads.
	var checkRunnerMu sync.RWMutex

	// emitSnapshot pushes the combined local + remote snapshot into the bot,
	// if one is running. Remote metrics are namespaced with the name of the
	// reporting node. ProcessSnapshot drops state for absent IDs, so local and
	// remote snapshots must never be fed separately.
	emitSnapshot := func() {
		if tgBot == nil {
			return
		}
		snap := proxyChecker.MetricsSnapshot()
		if nodeRegistry != nil {
			snap = nodeRegistry.MergedSnapshot(snap)
		}
		tgBot.ProcessSnapshot(snap)
	}

	if nodeRegistry != nil {
		nodeRegistry.SetOnUpdate(func() {
			emitSnapshot()
			if tgBot != nil {
				tgBot.ProcessNodesHealth(toNodeInfos(nodeRegistry.HealthSnapshot()))
			}
		})
		nodeRegistry.SetConfigSource(func() *nodes.NodeConfigSync {
			if tgBot != nil {
				cfg := tgBot.GetConfig()
				return &nodes.NodeConfigSync{
					SyncEnabled:            cfg.NodeSyncEnabled,
					DisabledProxies:        cfg.DisabledProxies,
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
			return &nodes.NodeConfigSync{
				SyncEnabled:         true,
				CheckIntervalSec:    config.CLIConfig.Proxy.CheckInterval,
				TargetURLs:          config.CLIConfig.Telegram.TargetURLs,
				QuietHoursEnabled:   config.CLIConfig.Telegram.QuietHoursEnabled,
				AlertMode:           config.CLIConfig.Telegram.AlertMode,
				NodeAlertsEnabled:   true,
				NodeProxyAlertsChat: true,
				NodeStaleTimeoutSec: 300,
			}
		})
	}

	runCheckIteration = func() {
		checkRunnerMu.RLock()
		defer checkRunnerMu.RUnlock()

		logger.Info("Starting proxy check iteration")
		start := time.Now()
		proxyChecker.CheckAllProxies()
		elapsed := time.Since(start)

		emitSnapshot()

		var interval int
		checkSchedulerMu.Lock()
		interval = config.CLIConfig.Proxy.CheckInterval
		checkSchedulerMu.Unlock()
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
				if err := metrics.PushMetrics(pushConfig, registry); err != nil {
					logger.Error("Error pushing metrics: %v", err)
				}
			}
		}

		if reporter != nil {
			if reporterRunning.CompareAndSwap(false, true) {
				go func() {
					defer reporterRunning.Store(false)
					hostIP, err := proxyChecker.GetCurrentIP()
					if err != nil {
						hostIP = ""
					}
					checkSchedulerMu.Lock()
					interval := config.CLIConfig.Proxy.CheckInterval
					checkSchedulerMu.Unlock()
					payload := nodes.BuildReport(
						proxyChecker.MetricsSnapshot(), version, interval,
						config.CLIConfig.Proxy.CheckMethod, hostIP,
					)
					desired, err := reporter.Send(payload)
					if err != nil {
						logger.Warn("Report to master failed (next cycle will retry): %v", err)
						return
					}
					if desired != nil {
						if reconcileDesired != nil && len(desired.ManagedSubs) > 0 {
							if err := reconcileDesired(desired.ManagedSubs); err != nil {
								logger.Error("Reconciling managed subscriptions failed: %v", err)
							}
						}
						if desired.ConfigSync != nil && desired.ConfigSync.SyncEnabled {
							if desired.ConfigSync.CheckIntervalSec > 0 {
								checkSchedulerMu.Lock()
								curInterval := config.CLIConfig.Proxy.CheckInterval
								checkSchedulerMu.Unlock()
								if curInterval != desired.ConfigSync.CheckIntervalSec {
									logger.Info("Agent check interval updated by master: %ds", desired.ConfigSync.CheckIntervalSec)
									rescheduleChecks(desired.ConfigSync.CheckIntervalSec)
								}
							}
							if tm := proxyChecker.GetTargetManager(); tm != nil && len(desired.ConfigSync.TargetURLs) > 0 {
								tm.SetTargets(desired.ConfigSync.TargetURLs)
							}
							if len(desired.ConfigSync.DisabledHosts) > 0 || len(desired.ConfigSync.DisabledProxies) > 0 {
								disHosts := desired.ConfigSync.DisabledHosts
								disProxies := desired.ConfigSync.DisabledProxies
								proxyChecker.SetDisabledFilter(func(server, stableID string) bool {
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
				}()
			} else {
				logger.Debug("Previous report to master still in flight, skipping overlapping push")
			}
		}
	}

	// reloadMu serializes every subscription reload: the periodic updater
	// below and any /addsub or /delsub from the Telegram bot must never
	// rebuild the Xray config and restart the runner at the same time.
	var reloadMu sync.Mutex

	// reloadSubscriptions re-fetches every subscription in subURLStore and,
	// if the resulting proxy set differs from the current one, applies it
	// (rebuilds the Xray config, restarts Xray, and updates the checker).
	// changed reports whether anything was applied; proxyCount is always the
	// resulting total regardless. It's used both by the periodic subscription
	// updater and by the Telegram bot's /addsub and /delsub.
	reloadSubscriptions := func() (changed bool, proxyCount int, err error) {
		reloadMu.Lock()
		defer reloadMu.Unlock()

		newConfigs, subCounts, err := subscription.ReadFromMultipleSourcesDetailed(subURLStore.All())
		if err != nil {
			return false, len(*proxyConfigs), err
		}
		now := time.Now()
		for u, cnt := range subCounts {
			subURLStore.RecordUpdate(u, cnt, now)
		}
		if tgBot != nil {
			for _, u := range subURLStore.All() {
				if meta, ok := subURLStore.GetMeta(u); ok {
					tgBot.SetSubFreshness(u, meta.Count, meta.PrevCount, meta.Added, meta.Removed, meta.LastUpdate)
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

		if xray.IsConfigsEqual(*proxyConfigs, newConfigs) {
			return false, len(*proxyConfigs), nil
		}

		updateErr := func() error {
			checkRunnerMu.Lock()
			defer checkRunnerMu.Unlock()
			return updateConfiguration(newConfigs, proxyConfigs, xrayRunner, proxyChecker)
		}()
		if updateErr != nil {
			return false, len(*proxyConfigs), updateErr
		}

		// Immediately re-check the new proxy set so /metrics is repopulated
		// right away instead of staying empty until the next scheduled check
		// (up to PROXY_CHECK_INTERVAL), then drop series for removed proxies.
		runCheckIteration()
		proxyChecker.PruneStaleResults()
		return true, len(*proxyConfigs), nil
	}

	reconcileDesired = func(desired []string) error {
		return subscription.ReconcileManaged(subURLStore, desired, reloadSubscriptions, subscription.DefaultSubscriptionValidator)
	}

	// The Telegram bot is only started for a long-running instance: --run-once
	// exits right after one check, so there's no continuous state to notify
	// about and no point long-polling for commands.
	if config.CLIConfig.Telegram.BotToken != "" {
		if config.CLIConfig.RunOnce {
			logger.Info("Telegram bot is not started in --run-once mode")
		} else {
			var subManager telegram.SubscriptionManager
			if config.CLIConfig.Telegram.ManageSubscriptions {
				subManager = &telegramSubscriptionManager{store: subURLStore, reload: reloadSubscriptions}
			}

			// Reuse TargetManager
			targetMgr := proxyChecker.GetTargetManager()

			defaultBotCfg := telegram.BotConfig{
				QuietHoursEnabled:      config.CLIConfig.Telegram.QuietHoursEnabled,
				QuietHoursStart:        config.CLIConfig.Telegram.QuietHoursStart,
				QuietHoursEnd:          config.CLIConfig.Telegram.QuietHoursEnd,
				DayDigestEnabled:       config.CLIConfig.Telegram.DayDigestEnabled,
				DayDigestIntervalHours: config.CLIConfig.Telegram.DayDigestIntervalHours,
				AlertMode:              config.CLIConfig.Telegram.AlertMode,
				TargetURLs:             targetMgr.GetTargets(),
				CheckIntervalSec:       config.CLIConfig.Proxy.CheckInterval,
				RichMode:               config.CLIConfig.Telegram.RichMode,
				CheckHostBgEnabled:     config.CLIConfig.Telegram.CheckHostBgEnabled,
				CheckHostIntervalHours: config.CLIConfig.Telegram.CheckHostIntervalHours,
				CheckHostAlertEnabled:  config.CLIConfig.Telegram.CheckHostAlertEnabled,
				NodeSyncEnabled:        true,
				NodeAlertsEnabled:      true,
				NodeProxyAlertsChat:    true,
				NodeStaleTimeoutSec:    90,
			}

			botCfgMgr, err := telegram.NewConfigManager(config.CLIConfig.Telegram.BotConfigStorePath, defaultBotCfg)
			if err != nil {
				logger.Warn("Failed to initialize bot config manager: %v", err)
			} else {
				savedCfg := botCfgMgr.Get()
				if len(savedCfg.TargetURLs) > 0 {
					for _, t := range savedCfg.TargetURLs {
						_ = targetMgr.AddTarget(t)
					}
				}
				if savedCfg.CheckIntervalSec > 0 {
					config.CLIConfig.Proxy.CheckInterval = savedCfg.CheckIntervalSec
				}
			}

			if botCfgMgr != nil {
				proxyChecker.SetDisabledFilter(func(server, stableID string) bool {
					return botCfgMgr.Get().IsDisabled(server, stableID)
				})
			}

			statsStore, err := telegram.NewStatsStore(config.CLIConfig.Telegram.StatsStorePath)
			if err != nil {
				logger.Warn("Failed to initialize stats store: %v", err)
			}

			var alertTracker *telegram.AlertTracker
			if config.CLIConfig.Telegram.AlertStorePath != "" {
				alertTracker = telegram.NewAlertTracker(config.CLIConfig.Telegram.AlertStorePath)
			}

			chatTargets, err := telegram.ParseChatTargets(config.CLIConfig.Telegram.ChatTargets)
			if err != nil {
				logger.Fatal("Invalid TELEGRAM_CHAT_IDS entry: %v", err)
			}

			if bot, err := telegram.New(
				config.CLIConfig.Telegram.BotToken,
				chatTargets,
				proxyChecker,
				config.CLIConfig.Telegram.NotifyOnRecovery,
				config.CLIConfig.Telegram.Commands,
				subManager,
			); err != nil {
				logger.Error("Telegram bot disabled: %v", err)
			} else {
				if botCfgMgr != nil {
					bot.SetConfigManager(botCfgMgr)
				}
				if statsStore != nil {
					bot.SetStatsStore(statsStore)
				}
				if alertTracker != nil {
					bot.SetAlertTracker(alertTracker)
				}
				if nodeRegistry != nil {
					bot.SetNodeManager(&nodeManagerAdapter{reg: nodeRegistry, subs: nodeSubsStore})
				}
				bot.SetDiagnosticsSource(proxyChecker)
				bot.SetIntervalHandler(rescheduleChecks)
				if config.CLIConfig.Telegram.RichMode {
					bot.SetRichMode(true)
				} else if botCfgMgr == nil {
					bot.SetRichMode(false)
				}

				tgBot = bot
				for _, u := range subURLStore.All() {
					if meta, ok := subURLStore.GetMeta(u); ok {
						tgBot.SetSubFreshness(u, meta.Count, meta.PrevCount, meta.Added, meta.Removed, meta.LastUpdate)
					}
				}
				tgBot.StartCommands()
				defer tgBot.Stop()
			}
		}
	}

	if config.CLIConfig.RunOnce {
		runCheckIteration()
		logger.Info("Check completed")
		return
	}

	checkSchedulerMu.Lock()
	checkScheduler = gocron.NewScheduler(time.UTC)
	// SingletonMode: if a check cycle overruns the interval, the next tick is skipped
	// instead of starting a second concurrent cycle. Without a concurrency limit a
	// cycle is bounded by PROXY_TIMEOUT so this rarely triggers, but with
	// PROXY_CHECK_CONCURRENCY a slow cycle degrades to "runs less often" rather than
	// piling up overlapping runs.
	checkScheduler.Every(config.CLIConfig.Proxy.CheckInterval).Seconds().SingletonMode().Do(func() {
		runCheckIteration()
	})
	checkScheduler.StartAsync()
	checkSchedulerMu.Unlock()

	if config.CLIConfig.Subscription.Update {
		updateScheduler := gocron.NewScheduler(time.UTC)
		updateScheduler.Every(config.CLIConfig.Subscription.UpdateInterval).Seconds().WaitForSchedule().Do(func() {
			logger.Info("Checking subscriptions for updates...")
			changed, count, err := reloadSubscriptions()
			if err != nil {
				logger.Error("Error fetching subscriptions: %v", err)
			} else if changed {
				logger.Info("Configuration updated: %d proxies", count)
			} else {
				logger.Info("Subscriptions checked, no changes")
			}
		})
		updateScheduler.StartAsync()
	}

	if nodeRegistry != nil {
		sweepScheduler := gocron.NewScheduler(time.UTC)
		sweepScheduler.Every(30).Seconds().SingletonMode().Do(func() {
			names := nodeRegistry.SweepStale(time.Now())
			for _, name := range names {
				logger.Warn("Node %s: no reports within deadline, marking down", name)
			}
			if len(names) > 0 {
				emitSnapshot()
				if tgBot != nil {
					tgBot.ProcessNodesHealth(toNodeInfos(nodeRegistry.HealthSnapshot()))
				}
			}
		})
		sweepScheduler.StartAsync()
	}

	mux, err := web.NewPrefixServeMux(config.CLIConfig.Metrics.BasePath)
	if err != nil {
		logger.Fatal("Error creating web server: %v", err)
	}
	mux.Handle("/health", web.HealthHandler())
	if nodeRegistry != nil {
		// Bearer-token auth of its own — deliberately not behind the metrics
		// basic auth, which nodes must not need to know.
		mux.Handle("/api/v1/nodes/report", nodeRegistry.HandleReport())
	}

	protectedHandler := http.NewServeMux()
	protectedHandler.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	if config.CLIConfig.Web.Enabled {
		// The status page's assets and public API stay unauthenticated only in
		// --web-public mode. Otherwise they mount on the protected mux below:
		// /api/v1/public/proxies is an exact-match route that would otherwise
		// bypass the basic-auth wrapper mounted at "/" and leak the proxy list
		// of a private deployment.
		if config.CLIConfig.Web.Public {
			mux.Handle("/static/", web.StaticHandler())
			mux.Handle("/api/v1/public/proxies", web.APIPublicProxiesHandler(proxyChecker))
		} else {
			protectedHandler.Handle("/static/", web.StaticHandler())
			protectedHandler.Handle("/api/v1/public/proxies", web.APIPublicProxiesHandler(proxyChecker))
		}

		web.RegisterConfigEndpoints(*proxyConfigs, proxyChecker, config.CLIConfig.Xray.StartPort)
		if nodeRegistry != nil {
			protectedHandler.Handle("/api/v1/nodes", web.APINodesHandler(nodeRegistry))
		}

		protectedHandler.Handle("/config/", web.ConfigStatusHandler(proxyChecker))
		protectedHandler.Handle("/api/v1/proxies/", web.APIProxyHandler(proxyChecker, config.CLIConfig.Xray.StartPort))
		protectedHandler.Handle("/api/v1/proxies", web.APIProxiesHandler(proxyChecker, config.CLIConfig.Xray.StartPort))
		protectedHandler.Handle("/api/v1/config", web.APIConfigHandler(proxyChecker))
		protectedHandler.Handle("/api/v1/status", web.APIStatusHandler(proxyChecker))
		protectedHandler.Handle("/api/v1/system/info", web.APISystemInfoHandler(version, startTime))
		protectedHandler.Handle("/api/v1/system/ip", web.APISystemIPHandler(proxyChecker))
		protectedHandler.Handle("/api/v1/docs", web.APIDocsHandler())
		protectedHandler.Handle("/api/v1/openapi.yaml", web.APIOpenAPIHandler())

		if config.CLIConfig.Web.Public {
			mux.Handle("/", web.IndexHandler(version, proxyChecker))
			mux.Handle("/config/", web.ConfigStatusHandler(proxyChecker))
			middlewareHandler := web.BasicAuthMiddleware(
				config.CLIConfig.Metrics.Username,
				config.CLIConfig.Metrics.Password,
			)(protectedHandler)
			mux.Handle("/metrics", middlewareHandler)
			mux.Handle("/api/", middlewareHandler)
		} else if config.CLIConfig.Metrics.Protected {
			protectedHandler.Handle("/", web.IndexHandler(version, proxyChecker))
			middlewareHandler := web.BasicAuthMiddleware(
				config.CLIConfig.Metrics.Username,
				config.CLIConfig.Metrics.Password,
			)(protectedHandler)
			mux.Handle("/", middlewareHandler)
		} else {
			protectedHandler.Handle("/", web.IndexHandler(version, proxyChecker))
			mux.Handle("/", protectedHandler)
		}
	} else {
		logger.Info("Web dashboard panel is disabled (--web-enabled=false)")
		if config.CLIConfig.Metrics.Protected {
			middlewareHandler := web.BasicAuthMiddleware(
				config.CLIConfig.Metrics.Username,
				config.CLIConfig.Metrics.Password,
			)(protectedHandler)
			mux.Handle("/metrics", middlewareHandler)
		} else {
			mux.Handle("/metrics", protectedHandler)
		}
	}

	if !config.CLIConfig.RunOnce {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

		if config.CLIConfig.Metrics.Port == "" || config.CLIConfig.Metrics.Port == "0" {
			logger.Info("HTTP server disabled. Running headless (Telegram bot / scheduler only)")
			sig := <-sigChan
			logger.Info("Received signal %v, shutting down...", sig)
		} else {
			addr := config.CLIConfig.Metrics.Host + ":" + config.CLIConfig.Metrics.Port
			srv := &http.Server{
				Addr:              addr,
				Handler:           mux,
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       60 * time.Second,
			}

			serverErr := make(chan error, 1)
			go func() {
				logger.Info("Server listening on %s%s", addr, config.CLIConfig.Metrics.BasePath)
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					serverErr <- err
				}
			}()

			select {
			case sig := <-sigChan:
				logger.Info("Received signal %v, shutting down gracefully...", sig)
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := srv.Shutdown(shutdownCtx); err != nil {
					logger.Error("Error shutting down HTTP server: %v", err)
				}
			case err := <-serverErr:
				logger.Fatal("Error starting server: %v", err)
			}
		}
	}
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

	if err := xrayRunner.Start(); err != nil {
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
