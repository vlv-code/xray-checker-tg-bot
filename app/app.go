// Package app wires the xray-checker subsystems into a runnable application
// (wave N-3b: main.go → app/ wiring split). Build() performs the entire
// startup wiring in the exact order of the former main() body — semantics
// are byte-identical; Run() owns the run phase and the LIFO shutdown defer
// chain (sweep → geo → update → check → bot → auditor → xray → asn →
// rootCancel — the committed deadlock-fix contract). Package main keeps only
// flag parsing, the ldflags-injected version/startTime, and the
// telegram-facing adapter types pinned by root tests (see root shims.go).
package app

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
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
)

// App holds every subsystem and every piece of runtime wiring state that used
// to be a main() local (wave N-3b rule 4: closures became methods,
// closure-held mutable state became fields; no new package-level mutable vars
// in this package except CheckSchedulerMu, which was already package-level in
// main.go). Fields needed by the main-package adapter factories are exported
// (SubURLStore, NodeRegistry, NodeSubsStore, NodesStore, ReloadSubscriptions,
// BotConfig); everything else stays unexported.
type App struct {
	version   string
	startTime time.Time

	adapters Adapters

	// Runtime (app/runtime.go): logger level, root context (cancelled by the
	// last shutdown defer in Run), geo files, subscription URL store, xray runner.
	logLevel    logger.Level
	rootCtx     context.Context
	rootCancel  context.CancelFunc
	geoManager  *xray.GeoFileManager
	SubURLStore *subscription.URLStore
	xrayRunner  *xray.Runner

	// Master topology (app/nodes.go): nil on a reporting node. asnLookup is
	// asn.NopLookup until the master's ASN database opens; asnDB keeps the
	// handle so Run can register its Close in the original defer position.
	NodeRegistry  *nodes.Registry
	NodeSubsStore *nodes.NodeSubsStore
	NodesStore    *nodes.NodesStore
	asnLookup     func(string) string
	asnDB         *asn.DB

	// Initial configuration (app/checker.go): proxyConfigs is the mutable
	// pointer-to-slice that updateConfiguration replaces on reloads.
	configFile   string
	cachePath    string
	proxyConfigs *[]*models.ProxyConfig
	isDegraded   bool

	// Checker (app/checker.go): proxy health checker, shared Cheburcheck
	// client, prometheus registry serving /metrics and push gateway.
	proxyChecker *checker.ProxyChecker
	cheburClient *checker.CheburcheckClient
	registry     *prometheus.Registry

	// Node reporting (app/nodes.go): master reporter (nil in master mode),
	// in-flight report flag, node's own Check-Host auditor (nil in master mode).
	reporter        *nodes.Reporter
	reporterRunning atomic.Bool
	nodeAuditor     *nodes.Auditor

	// Desired-state appliers, assigned during Build. Kept as nil-able func
	// fields (not methods) to preserve the original nil-guards verbatim in
	// the report-delivery path — they can only be invoked after Build has
	// assigned them, exactly like the former closure variables.
	reconcileDesired              func(desired []string) error
	rescheduleSubscriptionUpdates func(seconds int)

	// Telegram (app/telegram.go): the running bot, if any.
	tgBot atomic.Pointer[telegram.Bot]

	// Schedulers (app/schedulers.go + Run): check/update scheduler handles
	// and the mutexes guarding them. checkSchedulerMu is package-level
	// (CheckSchedulerMu) because the shutdown deadlock contract requires the
	// lifecycle tests to lock the very same mutex the app code locks.
	checkScheduler        *gocron.Scheduler
	updateScheduler       *gocron.Scheduler
	updateSchedulerMu     sync.Mutex
	checkRunnerMu         sync.RWMutex
	reloadMu              sync.Mutex
	checkIterationRunning atomic.Bool
}

// Adapters carries the constructor callbacks for the telegram-facing adapter
// types that remain in package main (telegramSubscriptionManager,
// mergedMetricsSource, nodeManagerAdapter). Those types cannot move into
// app/: the root-level pins (telegram_subscriptions_test.go,
// node_manager_test.go) construct them via composite literals with
// unexported fields, which cannot cross a package boundary. Build stores the
// callbacks and initTelegram invokes them at the original wiring points;
// factories must only read App wiring fields.
type Adapters struct {
	NewSubscriptionManager func(a *App) telegram.SubscriptionManager
	NewMergedMetricsSource func(a *App, local metrics.MetricsSource) metrics.MetricsSource
	NewNodeManager         func(a *App) telegram.NodeManager
}

// Build performs the startup wiring: InitEffectiveRuntime seeding, network
// environment, logger, geo/ASN init, node topology, initial configuration
// resolution, xray runner start, checker/target/chebur/metrics setup,
// reporter/auditor setup, node callbacks, desired-state closures, and the
// Telegram bot start (or its background retry). Step order mirrors the
// former main() body statement-for-statement; actions (stores opened,
// schedulers configured, bot started) happen in exactly the original
// sequence. Cleanups that were defers in main() are registered by Run in the
// same relative order — Build only stores the handles.
func Build(version string, startTime time.Time, adapters Adapters) *App {
	a := &App{
		version:   version,
		startTime: startTime,
		adapters:  adapters,
		asnLookup: asn.NopLookup,
	}
	a.initRuntime()            // InitEffectiveRuntime, network env, logger, ctx, assets, geo, subURLStore
	a.initMasterTopology()     // master-mode registry/subs/nodes stores + ASN DB
	a.initInitialConfigs()     // resolveInitialConfigs + startup logs
	a.initXrayRunner()         // xray runner + startWithRetry
	a.initChecker()            // proxyChecker, targetManager, chebur, prometheus registry
	a.initReporterAndAuditor() // node reporter + node's own Check-Host auditor
	a.initNodeCallbacks()      // registry SetOnUpdate + SetConfigSource
	a.initReschedulers()       // rescheduleSubscriptionUpdates closure (nil-guarded field)
	a.initReconciler()         // reconcileDesired closure (nil-guarded field)
	a.initTelegram()           // bot wiring + startTelegramBotWithRetry
	return a
}

// Run executes the run phase: the RunOnce fast path, the check/update/geo/
// sweep schedulers with their co-located stop-defers, the degraded-start
// retry goroutine, the web/metrics server and the signal wait.
//
// Shutdown ordering (the committed LIFO contract, preserved 1:1 from main()):
// the defers registered here unwind in exactly the original sequence —
// sweep → geo → update → check → bot → auditor → xray → asnDB.Close →
// rootCancel. The build-phase cleanups (asnDB, xray, auditor, bot) are
// registered first from App fields in the same relative order the original
// defers had in main(); every scheduler's stop-defer stays co-located with
// its scheduler's creation block (process-boundary transaction).
func (a *App) Run() {
	defer a.rootCancel()

	if a.asnDB != nil {
		defer a.asnDB.Close()
	}

	defer func() {
		if err := a.xrayRunner.Stop(); err != nil {
			logger.Error("Error stopping Xray: %v", err)
		}
	}()

	if a.nodeAuditor != nil {
		defer a.nodeAuditor.Stop()
	}

	// Registered unconditionally: in the original main() this defer existed
	// only when the bot block ran, but its body is a no-op while no bot was
	// stored (tgBot.Load() == nil), so the observable behavior is identical.
	defer func() {
		if b := a.tgBot.Load(); b != nil {
			b.Stop()
		}
	}()

	if config.CLIConfig.RunOnce {
		a.runCheckIteration()
		logger.Info("Check completed")
		return
	}

	CheckSchedulerMu.Lock()
	a.checkScheduler = gocron.NewScheduler(time.UTC)
	// Startup read from the immutable runtime snapshot (config/snapshot.go):
	// InitEffectiveRuntime ran at the top of Build and the persisted
	// bot-config override was applied through the same atomic snapshot, so
	// this observes the final effective interval with no lock.
	startupCheckInterval := CurrentCheckInterval()
	// SingletonMode: if a check cycle overruns the interval, the next tick is skipped
	// instead of starting a second concurrent cycle. Without a concurrency limit a
	// cycle is bounded by PROXY_TIMEOUT so this rarely triggers, but with
	// PROXY_CHECK_CONCURRENCY a slow cycle degrades to "runs less often" rather than
	// piling up overlapping runs.
	a.checkScheduler.Every(startupCheckInterval).Seconds().SingletonMode().Do(func() {
		a.runCheckIteration()
	})
	a.checkScheduler.StartAsync()
	CheckSchedulerMu.Unlock()

	// Shutdown ordering: this defer is registered after the xray runner's
	// Stop defer (top of Run), so LIFO unwinding stops this scheduler
	// BEFORE the xray runner closes — no check job can start a new iteration
	// against a closed instance. gocron v1.37.0 has no Shutdown(); Stop() is
	// its shutdown call.
	defer func() {
		CheckSchedulerMu.Lock()
		defer CheckSchedulerMu.Unlock()
		if a.checkScheduler != nil {
			a.checkScheduler.Stop()
		}
	}()

	// Initial check on startup: runs immediately without waiting for the first scheduled interval.
	SafeGo("initial check iteration", a.runCheckIteration)

	if config.CLIConfig.Subscription.Update {
		a.updateSchedulerMu.Lock()
		a.updateScheduler = gocron.NewScheduler(time.UTC)
		// Startup read from the immutable runtime snapshot
		// (config/snapshot.go) — lock-free, see the check-scheduler site above.
		startupUpdateInterval := CurrentSubscriptionUpdateInterval()
		a.updateScheduler.Every(startupUpdateInterval).Seconds().WaitForSchedule().Do(func() {
			logger.Info("Checking subscriptions for updates...")
			changed, count, err := a.ReloadSubscriptions()
			if err != nil {
				logger.Error("Error fetching subscriptions: %v", err)
			} else if changed {
				logger.Info("Configuration updated: %d proxies", count)
			} else {
				logger.Info("Subscriptions checked, no changes")
			}
		})
		a.updateScheduler.StartAsync()
		a.updateSchedulerMu.Unlock()

		// Registered after the check-scheduler defer: LIFO stops the update
		// scheduler before the check scheduler, and both before xray.
		defer func() {
			a.updateSchedulerMu.Lock()
			defer a.updateSchedulerMu.Unlock()
			if a.updateScheduler != nil {
				a.updateScheduler.Stop()
			}
		}()

		geoScheduler := gocron.NewScheduler(time.UTC)
		geoScheduler.Every(24).Hours().Do(func() {
			logger.Info("Checking geo databases for updates...")
			a.geoManager.UpdateGeoFiles()
		})
		geoScheduler.StartAsync()
		defer func() {
			if geoScheduler != nil {
				geoScheduler.Stop()
			}
		}()
	}

	if a.isDegraded && len(a.SubURLStore.All()) > 0 && !config.CLIConfig.RunOnce {
		SafeGo("background subscription retry", func() {
			logger.Info("Background subscription retry active: retrying every 20s until reachable...")
			ticker := time.NewTicker(20 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-a.rootCtx.Done():
					return
				case <-ticker.C:
					logger.Debug("Retrying initial subscription fetch in background...")
					changed, count, err := a.ReloadSubscriptions()
					if err == nil {
						logger.Info("Initial subscriptions recovered in background: loaded %d proxies (changed: %v)", count, changed)
						return
					}
					logger.Warn("Background subscription retry failed: %v. Next attempt in 20s...", err)
				}
			}
		})
	}

	if a.NodeRegistry != nil {
		sweepScheduler := gocron.NewScheduler(time.UTC)
		sweepScheduler.Every(30).Seconds().SingletonMode().Do(func() {
			names := a.NodeRegistry.SweepStale(time.Now())
			for _, name := range names {
				logger.Warn("Node %s: no reports within deadline, marking down", name)
			}
			if len(names) > 0 {
				a.emitSnapshot()
				if b := a.tgBot.Load(); b != nil {
					b.ProcessNodesHealth(ToNodeInfos(a.NodeRegistry.HealthSnapshot()))
				}
			}
		})
		sweepScheduler.StartAsync()
		// Registered last: LIFO stops the sweep scheduler first at shutdown,
		// before the other schedulers and before the xray runner closes.
		defer func() {
			if sweepScheduler != nil {
				sweepScheduler.Stop()
			}
		}()
	}

	mux, err := a.buildServeMux()
	if err != nil {
		logger.Fatal("Error creating web server: %v", err)
	}

	if !config.CLIConfig.RunOnce {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

		if config.CLIConfig.Metrics.Port == "" || config.CLIConfig.Metrics.Port == "0" {
			logger.Info("HTTP server disabled. Running headless (Telegram bot / scheduler only)")
			sig := <-sigChan
			a.rootCancel()
			logger.Info("Received signal %v, shutting down...", sig)
		} else {
			addr := config.CLIConfig.Metrics.Host + ":" + config.CLIConfig.Metrics.Port
			srv := &http.Server{
				Addr:              addr,
				Handler:           web.SecurityHeadersMiddleware(mux),
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       60 * time.Second,
			}

			serverErr := make(chan error, 1)
			SafeGo("http server", func() {
				logger.Info("Server listening on %s%s", addr, config.CLIConfig.Metrics.BasePath)
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					serverErr <- err
				}
			})

			select {
			case sig := <-sigChan:
				a.rootCancel()
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

// SafeGoBody is the synchronous core of SafeGo: it runs fn and recovers any
// panic so a failing worker cannot take the whole process down. Kept
// separate from SafeGo so tests can exercise the recover path synchronously.
func SafeGoBody(label string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("panic in %s: %v\n%s", label, r, debug.Stack())
		}
	}()
	fn()
}

// SafeGo launches fn in a panic-isolated goroutine: a panic in fn is logged
// with a stack trace instead of crashing the instance (graceful degradation
// over crash-loops). All five goroutine sites (initial check iteration, node
// report push, background subscription retry, http server, telegram bot
// retry) go through it, exactly as in the former main().
func SafeGo(label string, fn func()) {
	go SafeGoBody(label, fn)
}
