package main

// Root-level seam shims (wave N-3b). All wiring logic moved to the app/
// package; the root behavioral pins reference the former main-package seams
// byte-for-byte, so each seam stays as a thin delegating shim here and the
// real implementation lives in app/.

import (
	"context"
	"sync/atomic"
	"time"

	"xray-checker/app"
	"xray-checker/models"
	"xray-checker/nodes"
	"xray-checker/subscription"
	"xray-checker/telegram"
)

// checkSchedulerMu aliases the check-scheduler reschedule mutex now owned by
// app (app.CheckSchedulerMu — the full deadlock-contract comment lives
// there). TestCurrentCheckInterval_IsLockFreeSnapshot locks THIS mutex and
// asserts the interval read completes concurrently: it must observe the very
// same mutex the app code locks, hence the pointer alias rather than a copy.
var checkSchedulerMu = &app.CheckSchedulerMu

// currentCheckInterval delegating shim. The lock-free-snapshot contract
// comment lives on app.CurrentCheckInterval.
func currentCheckInterval() int { return app.CurrentCheckInterval() }

// currentSubscriptionUpdateInterval delegating shim (update-interval twin;
// still called by node_manager.go, which stays in package main).
func currentSubscriptionUpdateInterval() int { return app.CurrentSubscriptionUpdateInterval() }

// safeGo / safeGoBody delegating shims (panic-isolation contract comment
// lives on app.SafeGo / app.SafeGoBody).
func safeGo(label string, fn func()) { app.SafeGo(label, fn) }

func safeGoBody(label string, fn func()) { app.SafeGoBody(label, fn) }

// flushReporterOnce delegating shim (--run-once sync-delivery contract
// comment lives on app.FlushReporterOnce).
func flushReporterOnce(reporter *nodes.Reporter, payload nodes.ReportPayload) (*nodes.IngestResponse, error) {
	return app.FlushReporterOnce(reporter, payload)
}

// masterReportSecurityNote delegating shim.
func masterReportSecurityNote(masterPublicURL string) string {
	return app.MasterReportSecurityNote(masterPublicURL)
}

// recordSubFreshness delegating shim (freshness-honesty contract comment
// lives on app.RecordSubFreshness).
func recordSubFreshness(store *subscription.URLStore, counts map[string]int, failed map[string]bool, now time.Time) {
	app.RecordSubFreshness(store, counts, failed, now)
}

// resolveInitialConfigs delegating shim (degraded-start contract comment
// lives on app.ResolveInitialConfigs).
func resolveInitialConfigs(configFile, cachePath string, initFetcher func() (*[]*models.ProxyConfig, error)) (*[]*models.ProxyConfig, bool, error) {
	return app.ResolveInitialConfigs(configFile, cachePath, initFetcher)
}

// startWithRetry delegating shim (retry/backoff contract lives on
// app.StartWithRetry).
func startWithRetry(start func() error, backoffs []time.Duration) error {
	return app.StartWithRetry(start, backoffs)
}

// startTelegramBotWithRetry delegating shim (background-retry contract
// comment lives on app.StartTelegramBotWithRetry).
func startTelegramBotWithRetry(
	ctx context.Context,
	initBot func() (*telegram.Bot, error),
	tgBot *atomic.Pointer[telegram.Bot],
	onSuccess func(b *telegram.Bot),
	retryInterval time.Duration,
	runOnce bool,
) {
	app.StartTelegramBotWithRetry(ctx, initBot, tgBot, onSuccess, retryInterval, runOnce)
}

// toNodeInfos delegating shim: node_manager.go (stays in package main,
// pinned by node_manager_test.go) and the app wiring share one mapping; the
// implementation lives in app.ToNodeInfos.
func toNodeInfos(hs []nodes.NodeHealth) []telegram.NodeInfo {
	return app.ToNodeInfos(hs)
}
