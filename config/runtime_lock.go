package config

import "sync"

// DEPRECATED (wave N-3a, 2026-11-21): RuntimeMu is a zero-user legacy relic.
// The runtime-mutated CLIConfig fields (Proxy.CheckInterval,
// Subscription.UpdateInterval) are now served through the immutable
// EffectiveRuntime snapshot on atomic.Pointer — see config/snapshot.go:
//
//   - writers: UpdateCheckInterval / UpdateSubscriptionUpdateInterval
//     (read-modify-write under a private write mutex, atomic Store);
//   - readers: LoadEffectiveRuntime (lock-free, always observes a
//     consistent snapshot);
//   - startup: InitEffectiveRuntime in main() before any wiring.
//
// The "RuntimeMu inside schedulerMu" lock-order convention this mutex
// imposed is gone. Do NOT adopt RuntimeMu for new code; removal of this
// var (and file) is a separate cleanup wave. Kept as a deprecation marker
// so any reintroduction is a visible diff instead of a silent regression.
var RuntimeMu sync.RWMutex
