package subscription

import (
	"sync"

	"xray-checker/models"
)

// sourceState concentrates ALL package-level mutable state of the
// subscription reader (Wave N-H, 2026-11-19 — previously four bare
// package-level vars: lastGoodMu/lastGoodConfigs and
// subscriptionName/subNameMu):
//
//   - lastGood caches the parsed proxy configs of the last SUCCESSFUL fetch
//     per subscription source, so a single hiccuping source can be merged
//     back into the result instead of silently shrinking the proxy set (which
//     would take every proxy of the failing panel offline and fire false
//     outage alerts until the next cycle). A removed source loses its entry.
//   - name holds the display name of the most recently fetched subscription.
//
// The package-level funcs (ReadFromMultipleSources*, Get/SetSubscriptionName)
// delegate to defaultSourceState; tests build isolated instances with
// newSourceState.
type sourceState struct {
	// mu guards lastGood. Its coverage spans whole read-modify-write cycles
	// (merge + eviction), so concurrent reloads cannot interleave cache
	// updates — use lock/unlock + the *Locked methods for compound
	// sequences, the single-op wrappers (store/get/resetLastGood) otherwise.
	mu       sync.Mutex
	lastGood map[string][]*models.ProxyConfig

	// nameMu guards name.
	nameMu sync.RWMutex
	name   string
}

// newSourceState returns an isolated, ready-to-use instance (internal
// constructor — tests use it to avoid sharing state with the package
// default).
func newSourceState() *sourceState {
	return &sourceState{lastGood: make(map[string][]*models.ProxyConfig)}
}

// defaultSourceState backs the package-level API. The binding itself is
// never reassigned; all mutation happens behind the struct's mutexes.
var defaultSourceState = newSourceState()

// lock/unlock guard compound lastGood sequences that must stay atomic
// against concurrent reloads (see mu).
func (s *sourceState) lock()   { s.mu.Lock() }
func (s *sourceState) unlock() { s.mu.Unlock() }

// storeLocked stores a deep copy of the configs (cloneProxyConfigs) so the
// cache never aliases structs that downstream code mutates.
// Caller must hold the lock.
func (s *sourceState) storeLocked(url string, configs []*models.ProxyConfig) {
	s.lastGood[url] = cloneProxyConfigs(configs)
}

// getLocked returns the cached slice for url. The returned slice is the
// cache's backing storage: callers may read it and deep-copy it (as the
// failure-merge branch does), never mutate it.
// Caller must hold the lock.
func (s *sourceState) getLocked(url string) ([]*models.ProxyConfig, bool) {
	cached, ok := s.lastGood[url]
	return cached, ok
}

// evictStaleLocked drops cache entries whose source is no longer in the
// requested set, so a removed subscription cannot resurrect its proxies from
// the cache forever. Caller must hold the lock.
func (s *sourceState) evictStaleLocked(urls []string) {
	requested := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		requested[u] = struct{}{}
	}
	for u := range s.lastGood {
		if _, ok := requested[u]; !ok {
			delete(s.lastGood, u)
		}
	}
}

// storeAndEvictStale atomically stores a fresh result for url and evicts
// sources outside the requested set — one critical section, so a concurrent
// reload cannot interleave between the store and the eviction.
func (s *sourceState) storeAndEvictStale(url string, configs []*models.ProxyConfig, requested []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeLocked(url, configs)
	s.evictStaleLocked(requested)
}

// store is the single-op variant of storeLocked (locks internally).
func (s *sourceState) store(url string, configs []*models.ProxyConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeLocked(url, configs)
}

// get is the single-op variant of getLocked (locks internally).
func (s *sourceState) get(url string) ([]*models.ProxyConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(url)
}

// resetLastGood drops every cache entry (test isolation helper; the semantics
// match the former "lastGoodConfigs = make(...)" reset).
func (s *sourceState) resetLastGood() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastGood = make(map[string][]*models.ProxyConfig)
}

// subscriptionName returns the current subscription display name.
func (s *sourceState) subscriptionName() string {
	s.nameMu.RLock()
	defer s.nameMu.RUnlock()
	return s.name
}

// setSubscriptionName updates the subscription display name.
func (s *sourceState) setSubscriptionName(name string) {
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	s.name = name
}
