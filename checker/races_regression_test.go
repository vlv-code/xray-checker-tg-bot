package checker

// Regression tests for checker-package data races and panic isolation.
//
// NOTE ON -race ENFORCEMENT: this dev host has no gcc, so the Go race detector
// cannot run locally. CI (`.github/workflows/test.yml`, `go test -race ./...`)
// is the enforcement gate that turns the pre-fix interleaving exercised below
// into a RED report; locally these tests must at least pass without -race.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"xray-checker/models"
)

// TestSetRuntimeCheckSettings_ConcurrentWithReaders reproduces the two data
// races fixed in checker.go:
//   - CheckAllProxies read pc.checkMethod bare while SetRuntimeCheckSettings
//     wrote it under pc.mu (string header race, checker.go:836),
//   - GetCurrentIP dereferenced pc.httpClient bare while the writer swapped the
//     pointer (client rebuilt on ProxyTimeoutSec change, checker.go:232 vs :135).
//
// The writer alternates both fields 200 times while CheckAllProxies and
// GetCurrentIP loop concurrently (bounded iterations). Under CI -race the
// pre-fix code reports a data race here; the fixed code must stay clean.
func TestSetRuntimeCheckSettings_ConcurrentWithReaders(t *testing.T) {
	// Always-failing IP endpoint: GetCurrentIP errors on every call, which is
	// what drives CheckAllProxies into the branch that reads checkMethod, and
	// defeats success caching so every GetCurrentIP call re-dereferences the
	// httpClient for a fresh fetch.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	pc := NewProxyChecker(nil, 10000, ts.URL, 5,
		"http://cp.cloudflare.com/generate_204", "https://proof.ovh.net/files/1Mb.dat",
		60, 51200, "ip", 4)

	const iterations = 200
	methods := [2]string{"ip", "status"}
	timeouts := [2]int{5, 7}

	var wg sync.WaitGroup

	// Writer: swaps checkMethod and rebuilds httpClient on every iteration
	// (mirrors node-reporter/web/bot goroutines applying runtime settings).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			m := methods[i%2]
			to := timeouts[i%2]
			pc.SetRuntimeCheckSettings(RuntimeCheckSettings{
				CheckMethod:     &m,
				ProxyTimeoutSec: &to,
			})
		}
	}()

	// Reader 1: CheckAllProxies reads checkMethod on its scheduler goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			pc.CheckAllProxies()
		}
	}()

	// Reader 2: GetCurrentIP dereferences httpClient for the IP-echo fetch.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = pc.GetCurrentIP()
		}
	}()

	wg.Wait()
}

// TestRunBoundedChecks_PanicIsolation: a panicking check must not kill the
// process and must not prevent sibling proxies from being checked.
// RED evidence (pre-fix): the panic crashes the whole test binary.
func TestRunBoundedChecks_PanicIsolation(t *testing.T) {
	proxies := make([]*models.ProxyConfig, 5)
	for i := range proxies {
		proxies[i] = &models.ProxyConfig{Name: fmt.Sprintf("p%d", i), Index: i, StableID: fmt.Sprintf("id%d", i)}
	}

	var checked atomic.Int32
	runBoundedChecks(proxies, 2, func(p *models.ProxyConfig) {
		if p.Index == 0 {
			panic("boom in proxy check")
		}
		checked.Add(1)
	})

	if got := checked.Load(); got != 4 {
		t.Errorf("expected 4 sibling checks to complete despite panicking proxy, got %d", got)
	}
}

// TestRunBoundedChecks_PanicMarksFailedResult verifies the production wiring
// used by CheckAllProxies: the panicking proxy must get a generic failed result
// (CatUnknown, failure-path shape of checkProxyInternal) recorded in results,
// while the sibling proxy is still checked.
func TestRunBoundedChecks_PanicMarksFailedResult(t *testing.T) {
	pc := NewProxyChecker(nil, 10000, "https://api.ipify.org", 5,
		"http://cp.cloudflare.com/generate_204", "https://proof.ovh.net/files/1Mb.dat",
		60, 51200, "ip", 0)

	proxies := []*models.ProxyConfig{
		{Name: "ok", Index: 0, StableID: "ok"},
		{Name: "panicky", Index: 1, StableID: "panicky"},
	}

	var checked atomic.Int32
	runBoundedChecks(proxies, 2, func(p *models.ProxyConfig) {
		if p.StableID == "panicky" {
			panic("boom in proxy check")
		}
		checked.Add(1)
	}, pc.markPanicResult)

	if got := checked.Load(); got != 1 {
		t.Errorf("expected the sibling check to complete, got %d checked", got)
	}

	v, ok := pc.results.Load(proxyMetricKey(proxies[1]))
	if !ok {
		t.Fatalf("expected a failure result recorded for the panicking proxy")
	}
	r := v.(proxyResult)
	if r.status {
		t.Errorf("panicking proxy must be recorded as failed")
	}
	if r.lastErrorCategory != CatUnknown {
		t.Errorf("expected CatUnknown for panicking check, got %v", r.lastErrorCategory)
	}
	if !strings.Contains(r.lastErrorMsg, "panic") {
		t.Errorf("expected panic mention in error message, got %q", r.lastErrorMsg)
	}
	if r.lastCheck.IsZero() {
		t.Errorf("expected lastCheck to be set on the panic failure result")
	}
}
