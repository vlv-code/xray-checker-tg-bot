package app

import (
	"context"
	"sync/atomic"
	"time"

	"xray-checker/config"
	"xray-checker/logger"
	"xray-checker/metrics"
	"xray-checker/telegram"
)

// initTelegram wires and starts the Telegram bot. The former main() also
// registered the bot's stop defer right after startTelegramBotWithRetry;
// Run registers it (as a no-op-when-nil defer) in the same position of the
// LIFO chain.
func (a *App) initTelegram() {
	// The Telegram bot is only started for a long-running instance: --run-once
	// exits right after one check, so there's no continuous state to notify
	// about and no point long-polling for commands.
	if config.CLIConfig.Telegram.BotToken != "" {
		if config.CLIConfig.RunOnce {
			logger.Info("Telegram bot is not started in --run-once mode")
		} else {
			var subManager telegram.SubscriptionManager
			if config.CLIConfig.Telegram.ManageSubscriptions {
				// The telegramSubscriptionManager type lives in package main
				// (pinned by telegram_subscriptions_test.go); the factory
				// binds it to this App's store and reload path.
				subManager = a.adapters.NewSubscriptionManager(a)
			}

			// Reuse TargetManager
			targetMgr := a.proxyChecker.GetTargetManager()

			// Startup read from the immutable runtime snapshot
			// (config/snapshot.go): InitEffectiveRuntime ran at the top of
			// Build, before any wiring. No writer can be running this early
			// (schedulers and the bot itself start later), and the persisted
			// bot config below applies its override through the same atomic
			// snapshot.
			startupCheckInterval := CurrentCheckInterval()

			defaultBotCfg := telegram.BotConfig{
				QuietHoursEnabled:         config.CLIConfig.Telegram.QuietHoursEnabled,
				QuietHoursStart:           config.CLIConfig.Telegram.QuietHoursStart,
				QuietHoursEnd:             config.CLIConfig.Telegram.QuietHoursEnd,
				DayDigestEnabled:          config.CLIConfig.Telegram.DayDigestEnabled,
				DayDigestIntervalHours:    config.CLIConfig.Telegram.DayDigestIntervalHours,
				AlertMode:                 config.CLIConfig.Telegram.AlertMode,
				TargetURLs:                targetMgr.GetTargets(),
				CheckIntervalSec:          startupCheckInterval,
				RichMode:                  config.CLIConfig.Telegram.RichMode,
				CheckHostBgEnabled:        config.CLIConfig.Telegram.CheckHostBgEnabled,
				CheckHostIntervalHours:    config.CLIConfig.Telegram.CheckHostIntervalHours,
				CheckHostAlertEnabled:     config.CLIConfig.Telegram.CheckHostAlertEnabled,
				NodeSyncEnabled:           true,
				NodeAlertsEnabled:         true,
				NodeProxyAlertsChat:       true,
				NodeStaleTimeoutSec:       90,
				MasterPublicURL:           config.CLIConfig.Nodes.MasterPublicURL,
				ReleaseAlertsEnabled:      config.CLIConfig.Telegram.ReleaseAlertsEnabled,
				ReleaseCheckIntervalHours: config.CLIConfig.Telegram.ReleaseCheckIntervalHours,
			}

			botCfgMgr, err := telegram.NewConfigManager(config.CLIConfig.Telegram.BotConfigStorePath, defaultBotCfg)
			if err != nil {
				logger.Warn("Failed to initialize bot config manager: %v", err)
			} else {
				savedCfg := botCfgMgr.Get()
				if len(savedCfg.TargetURLs) > 0 {
					for _, t := range savedCfg.TargetURLs {
						if err := targetMgr.AddTarget(t); err != nil {
							logger.Warn("Failed to restore persisted target %s: %v", t, err)
						}
					}
				}
				if savedCfg.CheckIntervalSec > 0 {
					// Snapshot write (config/snapshot.go): the persisted bot
					// config overrides the parsed PROXY_CHECK_INTERVAL before
					// any scheduler starts.
					config.UpdateCheckInterval(savedCfg.CheckIntervalSec)
				}
			}

			if botCfgMgr != nil {
				a.proxyChecker.SetDisabledFilter(func(server, stableID string) bool {
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

			var botMetricsSource metrics.MetricsSource = a.proxyChecker
			if a.NodeRegistry != nil {
				botMetricsSource = a.adapters.NewMergedMetricsSource(a, a.proxyChecker)
			}

			initTelegramBot := func() (*telegram.Bot, error) {
				bot, err := telegram.New(
					config.CLIConfig.Telegram.BotToken,
					chatTargets,
					botMetricsSource,
					config.CLIConfig.Telegram.NotifyOnRecovery,
					config.CLIConfig.Telegram.Commands,
					subManager,
				)
				if err != nil {
					return nil, err
				}
				if botCfgMgr != nil {
					bot.SetConfigManager(botCfgMgr)
				}
				if statsStore != nil {
					bot.SetStatsStore(statsStore)
				}
				if alertTracker != nil {
					bot.SetAlertTracker(alertTracker)
				}
				if a.NodeRegistry != nil {
					// The nodeManagerAdapter type lives in package main
					// (pinned by node_manager_test.go); the factory binds it
					// to this App's registry, stores and bot-config reader.
					bot.SetNodeManager(a.adapters.NewNodeManager(a))
				}
				bot.SetASNLookup(a.asnLookup)
				bot.SetDiagnosticsSource(a.proxyChecker)
				bot.SetCheburcheckClient(a.cheburClient)
				bot.SetIntervalHandler(a.rescheduleChecks)
				bot.SetVersion(a.version)
				bot.SetAdminUserIDs(config.CLIConfig.Telegram.AdminUserIDs)
				if config.CLIConfig.Telegram.RichMode {
					bot.SetRichMode(true)
				} else if botCfgMgr == nil {
					bot.SetRichMode(false)
				}

				for _, u := range a.SubURLStore.All() {
					if meta, ok := a.SubURLStore.GetMeta(u); ok {
						bot.SetSubFreshness(u, meta.Count, meta.PrevCount, meta.Added, meta.Removed, meta.LastUpdate)
					}
				}
				bot.StartCommands()
				return bot, nil
			}

			StartTelegramBotWithRetry(
				a.rootCtx,
				initTelegramBot,
				&a.tgBot,
				func(b *telegram.Bot) {
					a.emitSnapshot()
					if a.NodeRegistry != nil {
						b.ProcessNodesHealth(ToNodeInfos(a.NodeRegistry.HealthSnapshot()))
					}
				},
				20*time.Second,
				config.CLIConfig.RunOnce,
			)
		}
	}
}

// BotConfig returns the running bot's current config, or the zero value
// while no bot is running. Exported for the main-package nodeManagerAdapter
// factory (its botCfg field is bound to this method — the former inline
// closure in main() read the very same tgBot pointer).
func (a *App) BotConfig() telegram.BotConfig {
	if b := a.tgBot.Load(); b != nil {
		return b.GetConfig()
	}
	return telegram.BotConfig{}
}

// StartTelegramBotWithRetry attempts to initialize the Telegram bot.
// If initialization fails and runOnce is false, it launches a background
// retry goroutine that periodically attempts to reconnect until successful
// or until ctx is cancelled.
func StartTelegramBotWithRetry(
	ctx context.Context,
	initBot func() (*telegram.Bot, error),
	tgBot *atomic.Pointer[telegram.Bot],
	onSuccess func(b *telegram.Bot),
	retryInterval time.Duration,
	runOnce bool,
) {
	bot, err := initBot()
	if err == nil {
		tgBot.Store(bot)
		if onSuccess != nil {
			onSuccess(bot)
		}
		return
	}

	if runOnce {
		logger.Error("Telegram bot disabled: %v", err)
		return
	}

	logger.Warn("Failed to initialize Telegram bot: %v. Retrying in background every %v...", err, retryInterval)
	SafeGo("telegram bot retry", func() {
		ticker := time.NewTicker(retryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				logger.Debug("Retrying Telegram bot initialization in background...")
				b, err := initBot()
				if err == nil {
					tgBot.Store(b)
					logger.Info("Telegram bot successfully connected and initialized in background")
					if onSuccess != nil {
						onSuccess(b)
					}
					return
				}
				logger.Warn("Background Telegram bot retry failed: %v. Next attempt in %v...", err, retryInterval)
			}
		}
	})
}
