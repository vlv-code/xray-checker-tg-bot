package app

import (
	"sync"

	"xray-checker/config"
	"xray-checker/logger"
)

// CheckSchedulerMu guards the check scheduler's reschedule path and is held by
// the shutdown stop-defer while gocron Stop() drains the running singleton
// job. Package-level (not an App field) so lifecycle tests can pin the
// deadlock contract: code running INSIDE the check job must never acquire it.
// (Wave N-3b: moved verbatim from package main together with its users;
// the root shims.go aliases it so the main-package pin locks the same mutex.)
var CheckSchedulerMu sync.Mutex

// CurrentCheckInterval reads the effective check interval from the immutable
// runtime snapshot (config/snapshot.go, wave N-3a). It must never acquire
// CheckSchedulerMu: it runs inside the check job, while the shutdown
// stop-defer holds CheckSchedulerMu and gocron Stop() waits for this very
// job — taking it here is a circular wait that hangs the whole LIFO shutdown
// chain (Guardian P0-2, 2026-11-16). Wave N-3a removed the last lock from
// this path: the snapshot is an atomic.Pointer swapped by writers, so no
// caller-held lock is needed at all. Returns 0 only before
// InitEffectiveRuntime; startup order in Build guarantees Init runs before
// any reader is wired, so production never sees 0 here.
func CurrentCheckInterval() int {
	if snap := config.LoadEffectiveRuntime(); snap != nil {
		return snap.CheckInterval
	}
	return 0
}

// CurrentSubscriptionUpdateInterval is the update-interval twin of
// CurrentCheckInterval: lock-free read of the immutable runtime snapshot
// (config/snapshot.go). Same nil-before-Init caveat, same 0 fallback.
func CurrentSubscriptionUpdateInterval() int {
	if snap := config.LoadEffectiveRuntime(); snap != nil {
		return snap.UpdateInterval
	}
	return 0
}

// rescheduleChecks re-applies the desired check interval: it updates the
// runtime snapshot and clears/re-arms the check scheduler job. Wired into the
// bot's interval handler and invoked by the master's config sync on nodes.
func (a *App) rescheduleChecks(seconds int) {
	CheckSchedulerMu.Lock()
	defer CheckSchedulerMu.Unlock()

	if seconds < 10 {
		seconds = 10
	}
	// Snapshot write (config/snapshot.go): CheckInterval is read
	// concurrently by config-sync/web/report readers via the lock-free
	// atomic snapshot — no RuntimeMu needed. CheckSchedulerMu stays: it
	// guards the gocron scheduler objects below, not config.
	config.UpdateCheckInterval(seconds)

	if a.checkScheduler != nil {
		a.checkScheduler.Clear()
		a.checkScheduler.Every(seconds).Seconds().SingletonMode().Do(a.runCheckIteration)
		logger.Info("Check interval updated to %ds", seconds)
	}
}

// initReschedulers assigns the node-side subscription-update rescheduler
// (former main() closure, assigned at the same wiring point). It stays a
// nil-able func field because the report-delivery path nil-checks it — the
// assignment happens during Build, before anything can invoke it, exactly
// like the former closure variable.
func (a *App) initReschedulers() {
	// rescheduleSubscriptionUpdates applies the master's desired subscription
	// update interval on a node (config sync). A no-op when the instance runs
	// with subscription updates disabled.
	a.rescheduleSubscriptionUpdates = func(seconds int) {
		if seconds < 60 {
			seconds = 60
		}
		a.updateSchedulerMu.Lock()
		defer a.updateSchedulerMu.Unlock()
		if a.updateScheduler == nil {
			return
		}
		// Snapshot write (config/snapshot.go): UpdateInterval is read
		// concurrently by config-sync/web readers via the lock-free atomic
		// snapshot. updateSchedulerMu stays: it guards the gocron scheduler
		// objects below, not config.
		config.UpdateSubscriptionUpdateInterval(seconds)
		logger.Info("Rescheduling subscription updates with interval %ds (set by master)", seconds)
		a.updateScheduler.Clear()
		a.updateScheduler.Every(seconds).Seconds().WaitForSchedule().Do(func() {
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
	}
}
