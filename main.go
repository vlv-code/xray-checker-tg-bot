// Package main is the composition root of xray-checker. Since wave N-3b all
// startup wiring lives in the app/ package: main() only parses flags, builds
// the App and runs it. The adapter types in this package (node_manager.go,
// telegram_subscriptions.go) and the delegating shims in shims.go exist
// because the root-level behavioral pins construct them directly with
// unexported fields (node_manager_test.go, telegram_subscriptions_test.go),
// which cannot cross a package boundary.
package main

import (
	"time"

	"xray-checker/app"
	"xray-checker/config"
	"xray-checker/metrics"
	"xray-checker/telegram"
)

var (
	version   = "unknown" // overridden at build time via -ldflags "-X main.version=..."
	startTime = time.Now()
)

func main() {
	config.Parse(version)

	application := app.Build(version, startTime, telegramAdapters())
	application.Run()
}

// telegramAdapters supplies the main-package adapter types to the app
// wiring. The factories receive the built *App so they can bind the adapters
// to the wiring state exactly like the former inline constructions in main():
//   - telegramSubscriptionManager  (was &telegramSubscriptionManager{store, reload})
//   - mergedMetricsSource          (was &mergedMetricsSource{local: proxyChecker, reg})
//   - nodeManagerAdapter           (was &nodeManagerAdapter{reg, subs, store, botCfg})
func telegramAdapters() app.Adapters {
	return app.Adapters{
		NewSubscriptionManager: func(a *app.App) telegram.SubscriptionManager {
			return &telegramSubscriptionManager{
				store:  a.SubURLStore,
				reload: a.ReloadSubscriptions,
			}
		},
		NewMergedMetricsSource: func(a *app.App, local metrics.MetricsSource) metrics.MetricsSource {
			return &mergedMetricsSource{local: local, reg: a.NodeRegistry}
		},
		NewNodeManager: func(a *app.App) telegram.NodeManager {
			return &nodeManagerAdapter{
				reg:    a.NodeRegistry,
				subs:   a.NodeSubsStore,
				store:  a.NodesStore,
				botCfg: a.BotConfig,
			}
		},
	}
}
