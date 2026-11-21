package subscription

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xray-checker/models"
)

func TestBuildValidatedConfigurationEmpty(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "empty_xray.json")

	emptyProxies := []*models.ProxyConfig{}
	configs, err := BuildValidatedConfiguration(configFile, emptyProxies)
	if err != nil {
		t.Fatalf("BuildValidatedConfiguration failed on empty proxies: %v", err)
	}

	if configs == nil || len(*configs) != 0 {
		t.Fatalf("expected empty configs, got: %v", configs)
	}

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		t.Fatalf("expected xray config file to be generated at %s", configFile)
	}
}

func TestBuildValidatedConfigurationWithProxies(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "valid_xray.json")

	proxies := []*models.ProxyConfig{
		{
			Protocol: "vless",
			Server:   "valid.example.com",
			Port:     443,
			Name:     "Valid Node",
			UUID:     "22222222-3333-4444-5555-666666666666",
			Index:    0,
		},
	}

	configs, err := BuildValidatedConfiguration(configFile, proxies)
	if err != nil {
		t.Fatalf("BuildValidatedConfiguration failed: %v", err)
	}

	if configs == nil || len(*configs) != 1 {
		t.Fatalf("expected 1 config, got: %v", configs)
	}

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		t.Fatalf("expected xray config file to be generated at %s", configFile)
	}
}

// ---- last-known-good cache: a partial source failure must not shrink the
// ---- proxy set (regression: the failed source's proxies vanished from
// ---- allConfigs, so the reconcile took every proxy of the hiccuping panel
// ---- offline until the next cycle).

// socksSubscriptionPayload builds a single-socks-node JSON subscription body
// that parses fully offline (no libXray, no network).
func socksSubscriptionPayload(server string, port int) string {
	return fmt.Sprintf(`[{"remarks":"grp","outbounds":[{"protocol":"socks","tag":"n","settings":{"address":"%s","port":%d}}]}]`, server, port)
}

// emptySubscriptionPayload is valid JSON with zero convertible outbounds: the
// parser rejects it with "no valid proxy configurations found", giving a
// deterministic offline fetch/parse failure.
const emptySubscriptionPayload = `[{"remarks":"grp","outbounds":[]}]`

// writeSubscriptionFile stores payload in a temp file and returns its source
// string. httptest HTTP sources cannot be used here: the SSRF guard
// (validateSubscriptionTarget) blocks loopback targets by design, so a
// failing fetch is simulated by flipping the file content under a stable
// file:// source key — the same "same source, content goes bad" semantics.
func writeSubscriptionFile(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub.txt")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write subscription file: %v", err)
	}
	return "file://" + path
}

func flipSubscriptionFile(t *testing.T, source, payload string) {
	t.Helper()
	if err := os.WriteFile(strings.TrimPrefix(source, "file://"), []byte(payload), 0o600); err != nil {
		t.Fatalf("flip subscription file %s: %v", source, err)
	}
}

func resetLastGoodCache(t *testing.T) {
	t.Helper()
	defaultSourceState.resetLastGood()
}

func lastGoodHas(t *testing.T, source string) bool {
	t.Helper()
	_, ok := defaultSourceState.get(source)
	return ok
}

// proxyServers maps server -> port for quick set comparisons.
func proxyServers(configs []*models.ProxyConfig) map[string]int {
	servers := make(map[string]int, len(configs))
	for _, cfg := range configs {
		servers[cfg.Server] = cfg.Port
	}
	return servers
}

func TestReadFromMultipleSources_KeepsLastKnownGoodOnSourceFailure(t *testing.T) {
	resetLastGoodCache(t)

	srcA := writeSubscriptionFile(t, socksSubscriptionPayload("1.2.3.4", 1080))
	srcB := writeSubscriptionFile(t, socksSubscriptionPayload("5.6.7.8", 1081))

	configs, counts, _, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcB})
	if err != nil {
		t.Fatalf("cycle 1 (both sources OK) failed: %v", err)
	}
	if got := proxyServers(configs); len(got) != 2 {
		t.Fatalf("cycle 1: expected 2 configs, got %d (%v)", len(got), got)
	}

	// Source B starts failing; source A is untouched.
	flipSubscriptionFile(t, srcB, emptySubscriptionPayload)

	configs, counts, _, err = ReadFromMultipleSourcesDetailed([]string{srcA, srcB})
	if err != nil {
		t.Fatalf("cycle 2 (partial failure) must not be a total failure: %v", err)
	}
	got := proxyServers(configs)
	if len(got) != 2 {
		t.Fatalf("cycle 2: failing source B must keep its 1 last-known-good proxy, got %d configs (%v)", len(got), got)
	}
	if got["5.6.7.8"] != 1081 {
		t.Errorf("cycle 2: source B proxy missing from result: %v", got)
	}
	if got["1.2.3.4"] != 1080 {
		t.Errorf("cycle 2: source A proxy missing from result: %v", got)
	}
	if counts[srcB] != 1 {
		t.Errorf("cycle 2: counts must reflect the merged set for B, got %d", counts[srcB])
	}

	// The plain wrapper must benefit from the same cache.
	configs, err = ReadFromMultipleSources([]string{srcA, srcB})
	if err != nil {
		t.Fatalf("cycle 3 via ReadFromMultipleSources failed: %v", err)
	}
	if got := proxyServers(configs); len(got) != 2 {
		t.Errorf("cycle 3: ReadFromMultipleSources must keep source B's proxies too, got %v", got)
	}
}

func TestReadFromMultipleSources_EvictsRemovedSources(t *testing.T) {
	resetLastGoodCache(t)

	srcA := writeSubscriptionFile(t, socksSubscriptionPayload("1.2.3.4", 1080))
	srcB := writeSubscriptionFile(t, socksSubscriptionPayload("5.6.7.8", 1081))

	if _, _, _, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcB}); err != nil {
		t.Fatalf("cycle 1 failed: %v", err)
	}
	if !lastGoodHas(t, srcB) {
		t.Fatalf("precondition: source B should be cached after a successful fetch")
	}

	// Source B is removed from the requested set.
	configs, _, _, err := ReadFromMultipleSourcesDetailed([]string{srcA})
	if err != nil {
		t.Fatalf("cycle 2 failed: %v", err)
	}
	if got := proxyServers(configs); len(got) != 1 || got["1.2.3.4"] != 1080 {
		t.Fatalf("only source A's proxies must remain, got %v", got)
	}
	if lastGoodHas(t, srcB) {
		t.Errorf("removed source B must be evicted from the last-known-good cache")
	}
	if !lastGoodHas(t, srcA) {
		t.Errorf("source A must stay cached")
	}
}

func TestReadFromMultipleSources_NoCacheFailureStaysAbsent(t *testing.T) {
	resetLastGoodCache(t)

	srcA := writeSubscriptionFile(t, socksSubscriptionPayload("1.2.3.4", 1080))
	srcC := writeSubscriptionFile(t, emptySubscriptionPayload)

	configs, counts, _, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcC})
	if err != nil {
		t.Fatalf("partial failure must not be a total failure: %v", err)
	}
	if got := proxyServers(configs); len(got) != 1 || got["1.2.3.4"] != 1080 {
		t.Fatalf("only source A's proxies must be present, got %v", got)
	}
	if _, ok := counts[srcC]; ok {
		t.Errorf("failed source C without cache must not appear in counts")
	}
	if lastGoodHas(t, srcC) {
		t.Errorf("a failed source must not gain a cache entry")
	}
}

func TestReadFromMultipleSources_TotalFailureReturnsErrorAndCache(t *testing.T) {
	resetLastGoodCache(t)

	srcA := writeSubscriptionFile(t, socksSubscriptionPayload("1.2.3.4", 1080))
	srcB := writeSubscriptionFile(t, socksSubscriptionPayload("5.6.7.8", 1081))

	if _, _, _, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcB}); err != nil {
		t.Fatalf("cycle 1 failed: %v", err)
	}

	flipSubscriptionFile(t, srcA, emptySubscriptionPayload)
	flipSubscriptionFile(t, srcB, emptySubscriptionPayload)

	configs, _, _, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcB})
	if err == nil {
		t.Fatal("total failure must still return an error")
	}
	if !strings.Contains(err.Error(), "failed to fetch any subscription") {
		t.Errorf("error must aggregate both failures, got: %v", err)
	}
	if got := proxyServers(configs); len(got) != 2 || got["1.2.3.4"] != 1080 || got["5.6.7.8"] != 1081 {
		t.Fatalf("cached configs must still be merged on total failure, got %v", got)
	}
}

func TestReadFromMultipleSources_FreshFetchOverwritesCache(t *testing.T) {
	resetLastGoodCache(t)

	srcA := writeSubscriptionFile(t, socksSubscriptionPayload("1.2.3.4", 1080))
	srcB := writeSubscriptionFile(t, socksSubscriptionPayload("5.6.7.8", 1081))

	if _, _, _, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcB}); err != nil {
		t.Fatalf("cycle 1 failed: %v", err)
	}
	flipSubscriptionFile(t, srcB, emptySubscriptionPayload)
	if _, _, _, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcB}); err != nil {
		t.Fatalf("cycle 2 (B failing, served from cache) failed: %v", err)
	}
	// Source B recovers with a NEW proxy list.
	flipSubscriptionFile(t, srcB, socksSubscriptionPayload("9.9.9.9", 1082))

	configs, _, _, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcB})
	if err != nil {
		t.Fatalf("cycle 3 failed: %v", err)
	}
	got := proxyServers(configs)
	if len(got) != 2 {
		t.Fatalf("expected exactly A + fresh B, got %v", got)
	}
	if got["9.9.9.9"] != 1082 {
		t.Errorf("fresh B proxy missing, got %v", got)
	}
	if _, stale := got["5.6.7.8"]; stale {
		t.Errorf("stale cached B proxy must not be merged after a fresh fetch, got %v", got)
	}
	// The cache itself must hold the fresh list, not the stale one.
	cachedB, _ := defaultSourceState.get(srcB)
	if len(cachedB) != 1 || cachedB[0].Server != "9.9.9.9" {
		t.Errorf("cache must hold the fresh B list, got %+v", cachedB)
	}
}

// Wave N-G FIX 2: ReadFromMultipleSourcesDetailed must expose WHICH sources
// failed this cycle, so callers (main.go) can keep freshness telemetry honest:
// a source that failed but served last-known-good cached configs must not be
// stamped with URLStore.LastUpdate=now — that presents stale proxies as
// freshly fetched in the /subs view. counts still reports the cached count
// (the merged set is real), failed marks the sources to skip.
func TestReadFromMultipleSourcesDetailed_ReportsFailedSources(t *testing.T) {
	resetLastGoodCache(t)

	srcA := writeSubscriptionFile(t, socksSubscriptionPayload("1.2.3.4", 1080))
	srcB := writeSubscriptionFile(t, socksSubscriptionPayload("5.6.7.8", 1081))

	// Cycle 1: both sources OK — failed must be empty.
	_, _, failed, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcB})
	if err != nil {
		t.Fatalf("cycle 1 failed: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("cycle 1: no source failed, got failed=%v", failed)
	}

	// Cycle 2: source B fails and serves its cached proxy — B must be
	// reported as failed, A must not.
	flipSubscriptionFile(t, srcB, emptySubscriptionPayload)
	configs, counts, failed, err := ReadFromMultipleSourcesDetailed([]string{srcA, srcB})
	if err != nil {
		t.Fatalf("cycle 2 (partial failure) must not be a total failure: %v", err)
	}
	if !failed[srcB] {
		t.Errorf("cycle 2: failing source B must be reported in the failed map, got %v", failed)
	}
	if failed[srcA] {
		t.Errorf("cycle 2: healthy source A must not be reported failed, got %v", failed)
	}
	if counts[srcB] != 1 {
		t.Errorf("cycle 2: cached count for B must stay visible in counts (merged set is real), got %d", counts[srcB])
	}
	if got := proxyServers(configs); len(got) != 2 || got["1.2.3.4"] != 1080 || got["5.6.7.8"] != 1081 {
		t.Errorf("cycle 2: merged set must still hold both proxies, got %v", got)
	}

	// Cycle 3: total failure — both sources reported failed, cached configs
	// still merged, error returned (main.go ignores the maps on err, but
	// they must stay honest).
	flipSubscriptionFile(t, srcA, emptySubscriptionPayload)
	configs, _, failed, err = ReadFromMultipleSourcesDetailed([]string{srcA, srcB})
	if err == nil {
		t.Fatal("cycle 3: total failure must return an error")
	}
	if !failed[srcA] || !failed[srcB] {
		t.Errorf("cycle 3: both failing sources must be reported, got %v", failed)
	}
	if got := proxyServers(configs); len(got) != 2 {
		t.Errorf("cycle 3: cached configs must still be merged, got %v", got)
	}
}

// Wave N-H: the internal constructor must build ISOLATED instances — state
// stored in one sourceState (cache entries and subscription name) must be
// invisible to another. The old bare package-level vars made per-test
// isolation impossible without a global reset.
func TestNewSourceStateInstancesAreIsolated(t *testing.T) {
	a := newSourceState()
	b := newSourceState()

	cfg := &models.ProxyConfig{
		Protocol: "socks",
		Server:   "1.2.3.4",
		Port:     1080,
		Name:     "n",
	}
	a.store("src-a", []*models.ProxyConfig{cfg})

	if cached, ok := a.get("src-a"); !ok || len(cached) != 1 || cached[0].Server != "1.2.3.4" {
		t.Fatalf("precondition: instance a must hold its stored entry, got %v (ok=%v)", cached, ok)
	}
	if cached, ok := b.get("src-a"); ok || cached != nil {
		t.Errorf("instance b must not see instance a's cache entries, got %v (ok=%v)", cached, ok)
	}

	b.setSubscriptionName("beta")
	if got := a.subscriptionName(); got != "" {
		t.Errorf("instance a must not see instance b's name, got %q", got)
	}
	if got := b.subscriptionName(); got != "beta" {
		t.Errorf("instance b must hold its own name, got %q", got)
	}
}
