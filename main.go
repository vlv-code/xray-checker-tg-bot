package main

import (
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"xray-checker/checker"
	"xray-checker/config"
	"xray-checker/logger"
	"xray-checker/metrics"
	"xray-checker/models"
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
		logger.Fatal("Failed to ensure geo files: %v", err)
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

	var runCheckIteration func()

	rescheduleChecks := func(seconds int) {
		checkSchedulerMu.Lock()
		defer checkSchedulerMu.Unlock()

		if seconds < 10 {
			seconds = 10
		}
		config.CLIConfig.Proxy.CheckInterval = seconds
		logger.Info("Rescheduling proxy checks with interval %ds", seconds)
		if checkScheduler != nil {
			checkScheduler.Clear()
			checkScheduler.Every(seconds).Seconds().SingletonMode().Do(func() {
				runCheckIteration()
			})
		}
	}

	runCheckIteration = func() {
		logger.Info("Starting proxy check iteration")
		start := time.Now()
		proxyChecker.CheckAllProxies()
		elapsed := time.Since(start)

		if tgBot != nil {
			tgBot.ProcessSnapshot(proxyChecker.MetricsSnapshot())
		}

		// Warn if a cycle overruns the interval: with PROXY_CHECK_CONCURRENCY set,
		// a large/slow proxy set can take longer than PROXY_CHECK_INTERVAL, so checks
		// (and metrics) effectively run less often than configured.
		if interval := config.CLIConfig.Proxy.CheckInterval; interval > 0 && elapsed > time.Duration(interval)*time.Second {
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

		newConfigs, err := subscription.ReadFromMultipleSources(subURLStore.All())
		if err != nil {
			return false, len(*proxyConfigs), err
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

		if err := updateConfiguration(newConfigs, proxyConfigs, xrayRunner, proxyChecker); err != nil {
			return false, len(*proxyConfigs), err
		}

		// Immediately re-check the new proxy set so /metrics is repopulated
		// right away instead of staying empty until the next scheduled check
		// (up to PROXY_CHECK_INTERVAL), then drop series for removed proxies.
		runCheckIteration()
		proxyChecker.PruneStaleResults()
		return true, len(*proxyConfigs), nil
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

			// Initialize TargetManager
			targetMgr := checker.NewTargetManager(config.CLIConfig.Telegram.TargetURLs)
			proxyChecker.SetTargetManager(targetMgr)

			defaultBotCfg := telegram.BotConfig{
				QuietHoursEnabled:      config.CLIConfig.Telegram.QuietHoursEnabled,
				QuietHoursStart:        config.CLIConfig.Telegram.QuietHoursStart,
				QuietHoursEnd:          config.CLIConfig.Telegram.QuietHoursEnd,
				DayDigestEnabled:       config.CLIConfig.Telegram.DayDigestEnabled,
				DayDigestIntervalHours: config.CLIConfig.Telegram.DayDigestIntervalHours,
				AlertMode:              config.CLIConfig.Telegram.AlertMode,
				TargetURLs:             targetMgr.GetTargets(),
				CheckIntervalSec:       config.CLIConfig.Proxy.CheckInterval,
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

			statsStore, err := telegram.NewStatsStore(config.CLIConfig.Telegram.StatsStorePath)
			if err != nil {
				logger.Warn("Failed to initialize stats store: %v", err)
			}

			if bot, err := telegram.New(
				config.CLIConfig.Telegram.BotToken,
				config.CLIConfig.Telegram.ChatIDs,
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
				bot.SetDiagnosticsSource(proxyChecker)
				bot.SetIntervalHandler(rescheduleChecks)

				tgBot = bot
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

	mux, err := web.NewPrefixServeMux(config.CLIConfig.Metrics.BasePath)
	if err != nil {
		logger.Fatal("Error creating web server: %v", err)
	}
	mux.Handle("/health", web.HealthHandler())

	protectedHandler := http.NewServeMux()
	protectedHandler.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	if config.CLIConfig.Web.Enabled {
		mux.Handle("/static/", web.StaticHandler())
		mux.Handle("/api/v1/public/proxies", web.APIPublicProxiesHandler(proxyChecker))

		web.RegisterConfigEndpoints(*proxyConfigs, proxyChecker, config.CLIConfig.Xray.StartPort)

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
		if config.CLIConfig.Metrics.Port == "" || config.CLIConfig.Metrics.Port == "0" {
			logger.Info("HTTP server disabled. Running headless (Telegram bot / scheduler only)")
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
			<-sigChan
			logger.Info("Shutting down...")
		} else {
			logger.Info("Server listening on %s:%s%s",
				config.CLIConfig.Metrics.Host,
				config.CLIConfig.Metrics.Port,
				config.CLIConfig.Metrics.BasePath,
			)
			if err := http.ListenAndServe(config.CLIConfig.Metrics.Host+":"+config.CLIConfig.Metrics.Port, mux); err != nil {
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
