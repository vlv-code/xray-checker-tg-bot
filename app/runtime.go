package app

import (
	"context"
	"fmt"
	"time"

	"xray-checker/config"
	"xray-checker/logger"
	"xray-checker/subscription"
	"xray-checker/web"
	"xray-checker/xray"
)

// initRuntime performs the first startup segment of the former main() body:
// InitEffectiveRuntime seeding, network environment setup, logger level,
// root context, startup logs, custom web assets, geo file manager and the
// subscription URL store.
func (a *App) initRuntime() {
	// Publish the startup snapshot of the runtime-mutated config fields
	// (config/snapshot.go, wave N-3a). Parse includes kong parsing plus
	// CLI.Validate; from here on Proxy.CheckInterval and
	// Subscription.UpdateInterval are read and written ONLY through the
	// immutable snapshot (config.LoadEffectiveRuntime / UpdateCheckInterval /
	// UpdateSubscriptionUpdateInterval) — never by mutating CLIConfig.
	// InitEffectiveRuntime must precede every scheduler, bot, web and node
	// wiring step below: all of them read the snapshot lock-free and rely on
	// the startup happens-before edge (nil snapshot is unreachable after
	// this point).
	config.InitEffectiveRuntime(
		config.CLIConfig.Proxy.CheckInterval,
		config.CLIConfig.Subscription.UpdateInterval,
	)

	config.SetupNetworkEnvironment(config.CLIConfig.Report.URL)

	logLevel := logger.ParseLevel(config.CLIConfig.LogLevel)
	logger.SetLevel(logLevel)
	a.logLevel = logLevel

	a.rootCtx, a.rootCancel = context.WithCancel(context.Background())

	logger.Startup("Xray Checker %s", a.version)
	if logLevel == logger.LevelNone {
		logger.Startup("Log level: none (silent mode)")
	}

	if config.CLIConfig.Web.Enabled {
		if err := web.InitAssetLoader(config.CLIConfig.Web.CustomAssetsPath); err != nil {
			logger.Fatal("Failed to initialize custom assets: %v", err)
		}
	}

	a.geoManager = xray.NewGeoFileManager("")
	if err := a.geoManager.EnsureGeoFiles(); err != nil {
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
	a.SubURLStore = subURLStore
}

// initXrayRunner creates the xray runner and starts it with the standard
// backoff retry. The former main() registered the runner's Stop defer right
// here; Run now registers it in the same relative position of the LIFO chain.
func (a *App) initXrayRunner() {
	a.xrayRunner = xray.NewRunner(a.configFile)
	if err := startXrayWithRetry(a.xrayRunner); err != nil {
		logger.Fatal("Error starting Xray: %v", err)
	}
}

var defaultXrayStartBackoffs = []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second, 3 * time.Second}

func StartWithRetry(start func() error, backoffs []time.Duration) error {
	var startErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		startErr = start()
		if startErr == nil {
			return nil
		}
		if attempt < len(backoffs) {
			logger.Warn("Failed to start service (attempt %d/%d): %v. Retrying in %v...", attempt+1, len(backoffs)+1, startErr, backoffs[attempt])
			time.Sleep(backoffs[attempt])
		}
	}
	return fmt.Errorf("failed after %d attempts: %w", len(backoffs)+1, startErr)
}

func startXrayWithRetry(runner interface{ Start() error }) error {
	return StartWithRetry(runner.Start, defaultXrayStartBackoffs)
}
