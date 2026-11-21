package config

import (
	"sync"
	"sync/atomic"
)

// EffectiveRuntime is an immutable snapshot of the CLIConfig fields that are
// mutated while the instance is running. CLIConfig itself is parsed once at
// startup, but two fields change at runtime (scheduler rescheduling,
// master-driven config sync, persisted bot config load):
//
//   - Proxy.CheckInterval         (main.go: rescheduleChecks, saved bot config load)
//   - Subscription.UpdateInterval (main.go: rescheduleSubscriptionUpdates)
//
// Wave N-3a replaced the RuntimeMu lock band-aid (config/runtime_lock.go)
// with this snapshot: writers store a fresh immutable copy on currentRuntime
// (read-modify-write under runtimeWriteMu so concurrent writers of different
// fields never clobber each other), readers load the pointer without any
// lock. Every reader therefore observes a fully consistent snapshot: a whole
// snapshot atomically replaces the previous one, so torn reads are
// impossible and the "RuntimeMu inside schedulerMu" lock-order convention
// disappears entirely.
type EffectiveRuntime struct {
	CheckInterval  int
	UpdateInterval int
}

var (
	// currentRuntime holds the effective runtime snapshot. It is nil only
	// before InitEffectiveRuntime: main() calls InitEffectiveRuntime right
	// after config parse/validation and BEFORE any scheduler, bot, web or
	// node wiring starts, so every production reader observes the
	// happens-before edge of the atomic Store and never sees nil.
	currentRuntime atomic.Pointer[EffectiveRuntime]

	// runtimeWriteMu serializes the read-modify-write loop below so two
	// concurrent writers of DIFFERENT fields (check interval vs subscription
	// update interval) cannot overwrite each other's value with a stale
	// whole-struct copy. Readers never take it.
	runtimeWriteMu sync.Mutex
)

// InitEffectiveRuntime publishes the startup snapshot of the runtime-mutated
// fields. Call once in main() after config.Parse/Validate and before any
// scheduler, bot, web or node wiring; later changes go through
// UpdateCheckInterval / UpdateSubscriptionUpdateInterval, never by mutating
// CLIConfig directly.
func InitEffectiveRuntime(checkInterval, updateInterval int) {
	currentRuntime.Store(&EffectiveRuntime{
		CheckInterval:  checkInterval,
		UpdateInterval: updateInterval,
	})
}

// LoadEffectiveRuntime returns the current immutable snapshot. The returned
// pointer is never mutated after publication, so callers may read its fields
// (and retain the pointer) without any lock. Nil is returned only before
// InitEffectiveRuntime — unreachable in production because main() initializes
// the snapshot before any reader is wired (see currentRuntime).
func LoadEffectiveRuntime() *EffectiveRuntime {
	return currentRuntime.Load()
}

// mutateRuntime applies f to a copy of the current snapshot under
// runtimeWriteMu and stores the result as the new snapshot — the
// read-modify-write loop that keeps concurrent writers of different fields
// from clobbering each other. A call before InitEffectiveRuntime degrades to
// publishing a partial snapshot instead of panicking (graceful degradation
// over crash-loops); production calls InitEffectiveRuntime first, so this
// path is defensive only.
func mutateRuntime(f func(*EffectiveRuntime)) {
	runtimeWriteMu.Lock()
	defer runtimeWriteMu.Unlock()
	next := EffectiveRuntime{}
	if cur := currentRuntime.Load(); cur != nil {
		next = *cur
	}
	f(&next)
	currentRuntime.Store(&next)
}

// UpdateCheckInterval atomically replaces the effective check interval.
// Safe for concurrent use with UpdateSubscriptionUpdateInterval and with any
// lock-free LoadEffectiveRuntime reader.
func UpdateCheckInterval(sec int) {
	mutateRuntime(func(er *EffectiveRuntime) { er.CheckInterval = sec })
}

// UpdateSubscriptionUpdateInterval atomically replaces the effective
// subscription update interval. Safe for concurrent use with
// UpdateCheckInterval and with any lock-free LoadEffectiveRuntime reader.
func UpdateSubscriptionUpdateInterval(sec int) {
	mutateRuntime(func(er *EffectiveRuntime) { er.UpdateInterval = sec })
}
