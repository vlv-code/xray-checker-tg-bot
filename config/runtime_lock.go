package config

import "sync"

// RuntimeMu guards runtime-mutated CLIConfig fields against concurrent
// readers. CLIConfig is parsed once at startup, but two fields are later
// mutated while the instance is running (scheduler rescheduling and
// master-driven config sync):
//
//   - Proxy.CheckInterval        (main.go: rescheduleChecks, persisted bot config load)
//   - Subscription.UpdateInterval (main.go: rescheduleSubscriptionUpdates)
//
// Writers must hold RuntimeMu.Lock() (in addition to any scheduler-level
// mutex); readers of these two fields must hold RuntimeMu.RLock(). RuntimeMu
// is always acquired after (and never before) scheduler-level mutexes, so
// there is no lock-order inversion.
var RuntimeMu sync.RWMutex
