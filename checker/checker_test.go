package checker

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xray-checker/metrics"
	"xray-checker/models"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// peakTracker records the maximum number of concurrent calls seen.
type peakTracker struct {
	cur, peak int32
	mu        sync.Mutex
}

func (p *peakTracker) enter() {
	n := atomic.AddInt32(&p.cur, 1)
	p.mu.Lock()
	if n > p.peak {
		p.peak = n
	}
	p.mu.Unlock()
}
func (p *peakTracker) leave() { atomic.AddInt32(&p.cur, -1) }

func TestRunBoundedChecks_Concurrency(t *testing.T) {
	proxies := make([]*models.ProxyConfig, 30)
	for i := range proxies {
		proxies[i] = &models.ProxyConfig{Name: "p", Index: i}
	}

	// Bounded to 4: peak concurrency must never exceed 4, and all run.
	var tr peakTracker
	var count int32
	runBoundedChecks(proxies, 4, func(*models.ProxyConfig) {
		tr.enter()
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&count, 1)
		tr.leave()
	})
	if tr.peak > 4 {
		t.Errorf("peak concurrency %d exceeded limit 4", tr.peak)
	}
	if count != 30 {
		t.Errorf("expected all 30 checked, got %d", count)
	}

	// Unlimited (0): checks are not bounded to a small number. Assert the peak far
	// exceeds the limited case's cap of 4 rather than exact equality to 30, which
	// would be timing-sensitive on a loaded/single-CPU runner.
	var tr2 peakTracker
	runBoundedChecks(proxies, 0, func(*models.ProxyConfig) {
		tr2.enter()
		time.Sleep(10 * time.Millisecond)
		tr2.leave()
	})
	if tr2.peak < 10 {
		t.Errorf("unlimited: expected substantial parallelism (peak >= 10), got %d", tr2.peak)
	}
}

func mkProxy(server, name, stableID string) *models.ProxyConfig {
	return &models.ProxyConfig{
		Protocol: "vless",
		Server:   server,
		Port:     443,
		Name:     name,
		SubName:  "sub",
		StableID: stableID,
	}
}

// recordUp mimics what checkProxyInternal stores on a successful check.
func recordUp(pc *ProxyChecker, p *models.ProxyConfig, lat time.Duration) {
	pc.results.Store(proxyMetricKey(p), proxyResult{
		status:    true,
		latency:   lat,
		lastCheck: time.Now(),
	})
}

// statusValue gathers the collector and returns the xray_proxy_status value for a
// given stable_id.
func statusValue(t *testing.T, c prometheus.Collector, stableID string) (float64, bool) {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "xray_proxy_status" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "stable_id" && l.GetValue() == stableID {
					return m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

// Reproduces the #148 scenario under the pull-collector model: a subscription
// update must not blank out a surviving proxy's metric (no blink to 0), removed
// proxies drop out immediately, and newly added proxies appear once checked.
func TestUpdateProxiesNoBlinkAndReflectsCurrentSet(t *testing.T) {
	a := mkProxy("1.1.1.1", "A", "ida")
	b := mkProxy("2.2.2.2", "B", "idb")
	pc := NewProxyChecker([]*models.ProxyConfig{a, b}, 10000, "", 5, "", "", 5, 1, "ip", 0)
	collector := metrics.NewCollector("", pc)

	// Initial check populated A and B.
	recordUp(pc, a, 100*time.Millisecond)
	recordUp(pc, b, 200*time.Millisecond)
	if got := testutil.CollectAndCount(collector, "xray_proxy_status"); got != 2 {
		t.Fatalf("expected 2 status series after initial check, got %d", got)
	}

	// Subscription update: B removed, C added (not yet checked).
	c := mkProxy("3.3.3.3", "C", "idc")
	pc.UpdateProxies([]*models.ProxyConfig{a, c})

	// Surviving A must keep status 1 across the update — never blink to 0.
	if v, ok := statusValue(t, collector, "ida"); !ok || v != 1 {
		t.Fatalf("surviving proxy A must keep status 1 across update, got v=%v ok=%v", v, ok)
	}
	// Only A is emitted now: B is gone (not in current set), C has no result yet.
	if got := testutil.CollectAndCount(collector, "xray_proxy_status"); got != 1 {
		t.Fatalf("after update expected 1 series (A), got %d", got)
	}

	// Immediate post-update check populates C.
	recordUp(pc, c, 300*time.Millisecond)
	if got := testutil.CollectAndCount(collector, "xray_proxy_status"); got != 2 {
		t.Fatalf("after post-update check expected 2 series (A,C), got %d", got)
	}

	// B's stale result lingers in the map but is never emitted; PruneStaleResults
	// removes it for memory hygiene.
	pc.PruneStaleResults()
	if _, ok := pc.results.Load(proxyMetricKey(b)); ok {
		t.Errorf("removed proxy B should be pruned from results")
	}
	for _, p := range []*models.ProxyConfig{a, c} {
		if _, ok := pc.results.Load(proxyMetricKey(p)); !ok {
			t.Errorf("proxy %s should remain in results", p.Name)
		}
	}
}

func TestGetProxyResultLastCheck(t *testing.T) {
	a := mkProxy("1.1.1.1", "A", "ida")
	pc := NewProxyChecker([]*models.ProxyConfig{a}, 10000, "", 5, "", "", 5, 1, "ip", 0)

	// No result yet: not found, lastCheck 0.
	if _, _, lc, found := pc.GetProxyResultByStableID("ida"); found || lc != 0 {
		t.Fatalf("expected no result before check, got found=%v lastCheck=%d", found, lc)
	}

	before := time.Now().Unix()
	recordUp(pc, a, 150*time.Millisecond)
	online, latency, lc, found := pc.GetProxyResultByStableID("ida")
	if !found || !online || latency != 150*time.Millisecond {
		t.Fatalf("unexpected result: online=%v latency=%v found=%v", online, latency, found)
	}
	if lc < before {
		t.Fatalf("lastCheck %d should be >= %d", lc, before)
	}
}

// Regression for #172: two proxies with the SAME name but different stable_id must
// resolve to their own result by stable_id, not to the first same-named proxy's.
func TestGetProxyResultByStableID_DuplicateNames(t *testing.T) {
	up := &models.ProxyConfig{Protocol: "socks", Server: "1.1.1.1", Port: 1080, Name: "Dup", StableID: "id-up"}
	down := &models.ProxyConfig{Protocol: "socks", Server: "2.2.2.2", Port: 1080, Name: "Dup", StableID: "id-down"}
	pc := NewProxyChecker([]*models.ProxyConfig{up, down}, 10000, "", 5, "", "", 5, 1, "status", 0)

	// up is healthy, down failed — same name, different stable_id.
	pc.results.Store(proxyMetricKey(up), proxyResult{status: true, latency: 100 * time.Millisecond, lastCheck: time.Now()})
	pc.results.Store(proxyMetricKey(down), proxyResult{status: false, latency: 0, lastCheck: time.Now()})

	if online, _, _, found := pc.GetProxyResultByStableID("id-up"); !found || !online {
		t.Errorf("id-up should be online, got found=%v online=%v", found, online)
	}
	// The bug: name lookup returned the first ("up") result for "down". By stable_id
	// it must be offline.
	if online, _, _, found := pc.GetProxyResultByStableID("id-down"); !found || online {
		t.Errorf("id-down should be offline, got found=%v online=%v", found, online)
	}
}

func TestMetricsSnapshot_DisabledDynamic(t *testing.T) {
	p1 := mkProxy("1.1.1.1", "Proxy1", "id1")
	p2 := mkProxy("2.2.2.2", "Proxy2", "id2")
	pc := NewProxyChecker([]*models.ProxyConfig{p1, p2}, 10000, "", 5, "", "", 5, 1, "status", 0)

	recordUp(pc, p1, 100*time.Millisecond)
	recordUp(pc, p2, 100*time.Millisecond)

	// Initially neither is disabled
	snap := pc.MetricsSnapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snap))
	}
	for _, m := range snap {
		if m.Disabled {
			t.Errorf("expected proxy %s not disabled initially", m.Name)
		}
	}

	// Disable p1 dynamically via filter
	disabledIDs := map[string]bool{"id1": true}
	pc.SetDisabledFilter(func(server, stableID string) bool {
		return disabledIDs[stableID]
	})

	// Snapshot should immediately reflect p1 disabled without new check
	snap = pc.MetricsSnapshot()
	for _, m := range snap {
		if m.StableID == "id1" && !m.Disabled {
			t.Errorf("expected id1 to be disabled dynamically in snapshot")
		}
		if m.StableID == "id2" && m.Disabled {
			t.Errorf("expected id2 to NOT be disabled in snapshot")
		}
	}

	// Re-enable p1
	delete(disabledIDs, "id1")
	snap = pc.MetricsSnapshot()
	for _, m := range snap {
		if m.Disabled {
			t.Errorf("expected all proxies re-enabled in snapshot")
		}
	}
}

func TestProxyChecker_DiagnosticsSnapshot(t *testing.T) {
	p1 := mkProxy("1.1.1.1", "Proxy1", "id1")
	pc := NewProxyChecker([]*models.ProxyConfig{p1}, 10000, "", 5, "", "", 5, 1, "status", 0)

	pc.results.Store(proxyMetricKey(p1), proxyResult{
		status:             false,
		latency:            150 * time.Millisecond,
		lastCheck:          time.Now(),
		canConnect:         true,
		canTransfer:        false,
		tlsHandshakeMs:     45,
		ttfbMs:             120,
		lastErrorCategory:  CatTimeout,
		lastErrorMsg:       "context deadline exceeded",
		directProbeSuccess: true,
		directProbeRTTMs:   25,
	})

	snap := pc.MetricsSnapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(snap))
	}
	m := snap[0]
	if m.Online {
		t.Errorf("expected Online=false")
	}
	if !m.CanConnect {
		t.Errorf("expected CanConnect=true")
	}
	if m.CanTransfer {
		t.Errorf("expected CanTransfer=false")
	}
	if m.LastErrorCategory != int(CatTimeout) {
		t.Errorf("expected LastErrorCategory=%d, got %d", CatTimeout, m.LastErrorCategory)
	}
	if m.TLSHandshakeMs != 45 {
		t.Errorf("expected TLSHandshakeMs=45, got %d", m.TLSHandshakeMs)
	}
	if m.TTFBMs != 120 {
		t.Errorf("expected TTFBMs=120, got %d", m.TTFBMs)
	}
	if !m.DirectProbeSuccess || m.DirectProbeRTTMs != 25 {
		t.Errorf("expected DirectProbeSuccess=true, RTT=25, got success=%v, rtt=%d", m.DirectProbeSuccess, m.DirectProbeRTTMs)
	}
}

func TestDetermineVerdict_UDP(t *testing.T) {
	health := NodeHealth{
		UDPErr: "connection refused (ICMP unreachable)",
	}
	status, verdict := DetermineVerdict("hysteria2", health, nil)
	if status != "offline" {
		t.Errorf("expected offline for UDP refused, got %s", status)
	}
	if !strings.Contains(verdict, "UDP-порт недоступен") {
		t.Errorf("expected UDP error verdict, got %s", verdict)
	}
}

func TestGetCurrentIP_ConcurrentRace(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.Write([]byte("203.0.113.199\n"))
	}))
	defer ts.Close()

	pc := &ProxyChecker{
		httpClient: ts.Client(),
		ipCheck:    ts.URL,
	}

	var wg sync.WaitGroup
	const goroutines = 20
	errChan := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ip, err := pc.GetCurrentIP()
			if err != nil {
				errChan <- err
				return
			}
			if ip != "203.0.113.199" {
				errChan <- fmt.Errorf("unexpected IP %q", ip)
			}
		}()
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Errorf("concurrent GetCurrentIP failed: %v", err)
	}
}

func TestGetCurrentIP_Rejects502HTML(t *testing.T) {
	status := http.StatusBadGateway
	body := "<html><head><title>502 Bad Gateway</title></head><body>Cloudflare 502</body></html>"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer ts.Close()

	pc := &ProxyChecker{
		httpClient: ts.Client(),
		ipCheck:    ts.URL,
	}

	// First attempt: 502 HTML must fail and not be cached
	ip, err := pc.GetCurrentIP()
	if err == nil {
		t.Fatalf("expected error on 502 HTML, got ip %q", ip)
	}
	if pc.ipInitialized {
		t.Errorf("expected ipInitialized to remain false after error")
	}

	// Server recovers: returns 200 OK with valid IP
	status = http.StatusOK
	body = "198.51.100.42\n"

	ip, err = pc.GetCurrentIP()
	if err != nil {
		t.Fatalf("expected success after server recovery, got error: %v", err)
	}
	if ip != "198.51.100.42" {
		t.Errorf("expected trimmed IP 198.51.100.42, got %q", ip)
	}
	if !pc.ipInitialized {
		t.Errorf("expected ipInitialized to be true after success")
	}
}

func TestCheckByIP_TransparentAndValidation(t *testing.T) {
	pc := &ProxyChecker{
		currentIP: "198.51.100.1",
		ipCheck:   "http://mock-ip",
	}

	// 1. Same IP returned (transparent proxy / direct routing leak)
	mockSameIP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("198.51.100.1\n"))
	}))
	defer mockSameIP.Close()
	pc.ipCheck = mockSameIP.URL

	outcome := pc.checkByIP(mockSameIP.Client(), pc.ipCheck)
	if outcome.success {
		t.Errorf("expected checkByIP to fail when proxy returns same host IP")
	}
	if outcome.err == nil || !strings.Contains(outcome.err.Error(), "proxy returned host IP") {
		t.Errorf("expected descriptive routing leak error, got %v", outcome.err)
	}

	// 2. Different valid IP returned (successful proxy)
	mockDiffIP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("203.0.113.50\n"))
	}))
	defer mockDiffIP.Close()
	pc.ipCheck = mockDiffIP.URL

	outcome = pc.checkByIP(mockDiffIP.Client(), pc.ipCheck)
	if !outcome.success {
		t.Errorf("expected checkByIP to succeed for different IP, got: %v", outcome.err)
	}

	// 3. HTML error returned through proxy
	mockHTML := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	defer mockHTML.Close()
	pc.ipCheck = mockHTML.URL

	outcome = pc.checkByIP(mockHTML.Client(), pc.ipCheck)
	if outcome.success {
		t.Errorf("expected checkByIP to fail on 502 HTML")
	}
	if outcome.httpStatus != 502 {
		t.Errorf("expected httpStatus 502, got %d", outcome.httpStatus)
	}
}

func TestGetCurrentIP_TTLExpirationRefetches(t *testing.T) {
	currentServerIP := "198.51.100.2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(currentServerIP))
	}))
	defer server.Close()

	pc := &ProxyChecker{
		currentIP:     "198.51.100.1",
		ipInitialized: true,
		ipCheckedAt:   time.Now().Add(-2 * hostIPCacheTTL), // expired
		ipCheck:       server.URL,
		httpClient:    server.Client(),
	}

	ip, err := pc.GetCurrentIP()
	if err != nil {
		t.Fatalf("unexpected error from GetCurrentIP: %v", err)
	}

	if ip != "198.51.100.2" {
		t.Errorf("expected GetCurrentIP to refetch expired IP and get 198.51.100.2, got %q", ip)
	}
}

func TestSetRuntimeCheckSettings(t *testing.T) {
	pc := NewProxyChecker(nil, 10000, "https://api.ipify.org", 30,
		"http://cp.cloudflare.com/generate_204", "https://proof.ovh.net/files/1Mb.dat",
		60, 51200, "ip", 0)

	intPtr := func(i int) *int { return &i }
	strPtr := func(s string) *string { return &s }
	i64Ptr := func(i int64) *int64 { return &i }

	// Apply a full set.
	pc.SetRuntimeCheckSettings(RuntimeCheckSettings{
		CheckMethod:        strPtr("status"),
		IpCheckURL:         strPtr("https://ip.example/"),
		StatusCheckURL:     strPtr("http://status.example/204"),
		DownloadURL:        strPtr("https://dl.example/1Mb.dat"),
		ProxyTimeoutSec:    intPtr(15),
		DownloadTimeoutSec: intPtr(45),
		DownloadMinSize:    i64Ptr(2048),
		CheckConcurrency:   intPtr(4),
	})
	pc.mu.RLock()
	method, ipURL, statusURL, dlURL := pc.checkMethod, pc.ipCheck, pc.genMethodURL, pc.downloadURL
	proxyTo, dlTo, dlMin, conc := pc.ipCheckTimeout, pc.downloadTimeout, pc.downloadMinSize, pc.checkConcurrency
	hcTimeout := pc.httpClient.Timeout
	pc.mu.RUnlock()
	if method != "status" || ipURL != "https://ip.example/" || statusURL != "http://status.example/204" ||
		dlURL != "https://dl.example/1Mb.dat" || proxyTo != 15 || dlTo != 45 || dlMin != 2048 || conc != 4 {
		t.Fatalf("settings not applied: method=%s ip=%s status=%s dl=%s to=%d dlTo=%d min=%d conc=%d",
			method, ipURL, statusURL, dlURL, proxyTo, dlTo, dlMin, conc)
	}
	if hcTimeout != 15*time.Second {
		t.Fatalf("httpClient timeout not rebuilt: %s", hcTimeout)
	}

	// Zero-value (unlimited) concurrency must be settable explicitly.
	conc5 := 5
	pc.SetRuntimeCheckSettings(RuntimeCheckSettings{CheckConcurrency: intPtr(5)})
	pc.SetRuntimeCheckSettings(RuntimeCheckSettings{CheckConcurrency: intPtr(0)})
	pc.mu.RLock()
	conc = pc.checkConcurrency
	pc.mu.RUnlock()
	if conc != 0 {
		t.Fatalf("explicit zero concurrency must apply, got %d", conc)
	}
	_ = conc5

	// Nil fields and invalid values are skipped.
	pc.SetRuntimeCheckSettings(RuntimeCheckSettings{
		CheckMethod:     strPtr("bogus"),
		ProxyTimeoutSec: intPtr(-5),
	})
	pc.mu.RLock()
	method = pc.checkMethod
	pc.mu.RUnlock()
	if method != "status" {
		t.Fatalf("invalid method must be skipped, got %q", method)
	}

	// Empty runtime struct changes nothing.
	pc.SetRuntimeCheckSettings(RuntimeCheckSettings{})
}
