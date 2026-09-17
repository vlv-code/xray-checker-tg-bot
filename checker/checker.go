package checker

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"xray-checker/logger"
	"xray-checker/metrics"
	"xray-checker/models"
)

type ProxyChecker struct {
	proxies          []*models.ProxyConfig
	startPort        int
	ipCheck          string
	currentIP        string
	httpClient       *http.Client
	results          sync.Map // proxyMetricLabels -> proxyResult
	ipInitialized    bool
	ipCheckedAt      time.Time
	ipCheckTimeout   int
	genMethodURL     string
	downloadURL      string
	downloadTimeout  int
	downloadMinSize  int64
	checkMethod      string
	checkConcurrency int // max proxies checked in parallel per cycle; 0 = unlimited
	targetManager    *TargetManager
	disabledFilter   func(server, stableID string) bool
	mu               sync.RWMutex
	// ipFetchMu serializes IP-echo fetches so concurrent cache misses collapse
	// into one request. It is never held while pc.mu is held: the fetch runs
	// outside pc.mu so a slow IP service can't stall snapshot readers.
	ipFetchMu sync.Mutex
}

// hostIPCacheTTL bounds how long the checker's own public IP is trusted. The
// IP still serves as a fallback after expiry when re-fetching fails.
const hostIPCacheTTL = time.Hour

// proxyResult is the latest check outcome for one proxy. Metrics are rendered from
// these at scrape time (a pull model), so there is no separate metric state to keep
// in sync and no series to delete — the metrics collector simply reflects whatever
// results exist for the current proxy set.
type proxyResult struct {
	status             bool
	latency            time.Duration
	lastCheck          time.Time
	disabled           bool
	canConnect         bool
	canTransfer        bool
	tlsHandshakeMs     int64
	ttfbMs             int64
	lastErrorCategory  ErrorCategory
	lastErrorMsg       string
	directProbeSuccess bool
	directProbeRTTMs   int64
	directProbeErr     string
}

type checkOutcome struct {
	success        bool
	canConnect     bool
	canTransfer    bool
	logMessage     string
	latency        time.Duration
	tlsHandshakeMs int64
	ttfbMs         int64
	httpStatus     int
	err            error
}

// SetDisabledFilter sets a predicate to check if a proxy or host is disabled from checking.
func (pc *ProxyChecker) SetDisabledFilter(f func(server, stableID string) bool) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.disabledFilter = f
}

// IsProxyDisabled returns true if the proxy is configured to be skipped from checks.
func (pc *ProxyChecker) IsProxyDisabled(proxy *models.ProxyConfig) bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	if pc.disabledFilter != nil {
		return pc.disabledFilter(proxy.Server, proxy.StableID)
	}
	return false
}

// GetUniqueHosts returns a sorted list of unique server hosts from all configured proxies.
func (pc *ProxyChecker) GetUniqueHosts() []string {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	seen := make(map[string]bool)
	var hosts []string
	for _, p := range pc.proxies {
		if p.Server != "" && !seen[p.Server] {
			seen[p.Server] = true
			hosts = append(hosts, p.Server)
		}
	}
	return hosts
}

func NewProxyChecker(proxies []*models.ProxyConfig, startPort int, ipCheckURL string, ipCheckTimeout int, genMethodURL string, downloadURL string, downloadTimeout int, downloadMinSize int64, checkMethod string, checkConcurrency int) *ProxyChecker {
	if checkMethod != "ip" && checkMethod != "status" && checkMethod != "download" {
		logger.Warn("Invalid check method %q specified, falling back to 'ip'", checkMethod)
		checkMethod = "ip"
	}
	// StableIDs are assigned here and in UpdateProxies — the only two places
	// pc.proxies is written — so readers never need to lazily fill the field
	// (a data race when done under a mere RLock or concurrently). Re-running
	// AssignStableIDs over an already-assigned set is a no-op: it recomputes
	// the same deterministic IDs.
	models.AssignStableIDs(proxies)
	return &ProxyChecker{
		proxies:   proxies,
		startPort: startPort,
		ipCheck:   ipCheckURL,
		httpClient: &http.Client{
			Timeout: time.Second * time.Duration(ipCheckTimeout),
		},
		ipCheckTimeout:   ipCheckTimeout,
		genMethodURL:     genMethodURL,
		downloadURL:      downloadURL,
		downloadTimeout:  downloadTimeout,
		downloadMinSize:  downloadMinSize,
		checkMethod:      checkMethod,
		checkConcurrency: checkConcurrency,
	}
}

func (pc *ProxyChecker) GetCurrentIP() (string, error) {
	pc.mu.RLock()
	// Re-resolve after a TTL: a long-running host's public IP can change
	// (DHCP/ISP), and a cached-forever IP turns into false "transparent
	// proxy" verdicts and wrong error text.
	if pc.ipInitialized && pc.currentIP != "" && time.Since(pc.ipCheckedAt) < hostIPCacheTTL {
		ip := pc.currentIP
		pc.mu.RUnlock()
		return ip, nil
	}
	pc.mu.RUnlock()

	// The HTTP round trip runs WITHOUT pc.mu: it can take up to ipCheckTimeout,
	// and holding the write lock across it blocked every RLock holder (metrics
	// scrapes, bot snapshots) for the duration. ipFetchMu still collapses
	// concurrent misses into a single fetch.
	pc.ipFetchMu.Lock()
	defer pc.ipFetchMu.Unlock()

	pc.mu.RLock()
	if pc.ipInitialized && pc.currentIP != "" {
		ip := pc.currentIP
		pc.mu.RUnlock()
		return ip, nil
	}
	pc.mu.RUnlock()

	resp, err := pc.httpClient.Get(pc.ipCheck)
	if err != nil {
		return "", fmt.Errorf("error getting current IP: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("error getting current IP: unexpected HTTP status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	ipStr := strings.TrimSpace(string(body))
	if parsed := net.ParseIP(ipStr); parsed == nil {
		return "", fmt.Errorf("error getting current IP: invalid IP %q returned", ipStr)
	}

	pc.mu.Lock()
	pc.currentIP = ipStr
	pc.ipInitialized = true
	pc.ipCheckedAt = time.Now()
	pc.mu.Unlock()

	return ipStr, nil
}

func (pc *ProxyChecker) CheckProxy(proxy *models.ProxyConfig) {
	pc.checkProxyInternal(proxy)
}

// proxyMetricLabels is the full Prometheus label set for a proxy. It doubles as the
// in-memory map key, so series can be deleted exactly from their labels without
// parsing a packed string (proxy names may legitimately contain any character).
type proxyMetricLabels struct {
	protocol  string
	address   string
	name      string
	subName   string
	stableID  string
	groupName string
}

func proxyMetricKey(proxy *models.ProxyConfig) proxyMetricLabels {
	return proxyMetricLabels{
		protocol:  proxy.Protocol,
		address:   fmt.Sprintf("%s:%d", proxy.Server, proxy.Port),
		name:      proxy.Name,
		subName:   proxy.SubName,
		stableID:  proxy.StableID,
		groupName: proxy.GroupName,
	}
}

func (pc *ProxyChecker) checkProxyInternal(proxy *models.ProxyConfig) {
	metricKey := proxyMetricKey(proxy)

	if pc.IsProxyDisabled(proxy) {
		logger.Debug("%s (%s) is disabled from checks, skipping", proxy.Name, proxy.Server)
		pc.results.Store(metricKey, proxyResult{
			status:    false,
			latency:   0,
			lastCheck: time.Now(),
			disabled:  true,
		})
		return
	}

	storeResult := func(status bool, latency time.Duration) {
		pc.results.Store(metricKey, proxyResult{
			status:    status,
			latency:   latency,
			lastCheck: time.Now(),
		})
	}

	setFailed := func() {
		storeResult(false, 0)
	}

	proxyURL := fmt.Sprintf("socks5://127.0.0.1:%d", pc.startPort+proxy.Index)
	proxyURLParsed, err := url.Parse(proxyURL)
	if err != nil {
		logger.Error("Error parsing proxy URL %s: %v", proxyURL, err)
		setFailed()

		return
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURLParsed),
			DisableKeepAlives: true,
		},
		Timeout: time.Second * time.Duration(pc.ipCheckTimeout),
	}

	var outcome checkOutcome
	if pc.checkMethod == "ip" {
		outcome = pc.checkByIP(client)
	} else if pc.checkMethod == "status" {
		outcome = pc.checkByGen(client)
	} else if pc.checkMethod == "download" {
		outcome = pc.checkByDownload(client)
	} else {
		logger.Error("Invalid check method: %s", pc.checkMethod)
		return
	}

	if outcome.err != nil || !outcome.success {
		if outcome.err != nil {
			logger.Error("%s | %v", proxy.Name, outcome.err)
		} else {
			logger.Error("%s | Failed | %s | Latency: %s", proxy.Name, outcome.logMessage, outcome.latency)
		}

		var errCat ErrorCategory
		var errMsg string
		if outcome.err != nil {
			errCat = ClassifyError(outcome.err, outcome.httpStatus)
			errMsg = outcome.err.Error()
		} else {
			errCat = CatUnknown
			errMsg = outcome.logMessage
		}

		// Forced direct node probe to distinguish host network vs proxy service failure
		health := ProbeNodeHealth(proxy.Server, proxy.Port, proxy.Protocol, proxy.Security, proxy.SNI, proxy.AllowInsecure)
		var directSuccess bool
		var directRTT int64
		var directErr string
		if IsUDPProto(proxy.Protocol) {
			if health.UDPErr == "" && health.UDPPing > 0 {
				directSuccess = true
				directRTT = health.UDPPing.Milliseconds()
			} else {
				directErr = health.UDPErr
			}
		} else {
			if health.TCPErr == "" && health.TCPPing > 0 {
				directSuccess = true
				directRTT = health.TCPPing.Milliseconds()
			} else {
				directErr = health.TCPErr
			}
		}

		pc.results.Store(metricKey, proxyResult{
			status:             false,
			latency:            outcome.latency,
			lastCheck:          time.Now(),
			canConnect:         outcome.canConnect,
			canTransfer:        outcome.canTransfer,
			tlsHandshakeMs:     outcome.tlsHandshakeMs,
			ttfbMs:             outcome.ttfbMs,
			lastErrorCategory:  errCat,
			lastErrorMsg:       errMsg,
			directProbeSuccess: directSuccess,
			directProbeRTTMs:   directRTT,
			directProbeErr:     directErr,
		})
	} else {
		logger.Result("%s | Success | %s | Latency: %s", proxy.Name, outcome.logMessage, outcome.latency)
		pc.results.Store(metricKey, proxyResult{
			status:            true,
			latency:           outcome.latency,
			lastCheck:         time.Now(),
			canConnect:        true,
			canTransfer:       true,
			tlsHandshakeMs:    outcome.tlsHandshakeMs,
			ttfbMs:            outcome.ttfbMs,
			lastErrorCategory: CatNone,
		})
	}
}

func (pc *ProxyChecker) checkByIP(client *http.Client) checkOutcome {
	var tlsStart, tlsDone time.Time
	var gotFirstByte time.Time
	start := time.Now()
	trace := &httptrace.ClientTrace{
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone:  func(_ tls.ConnectionState, _ error) { tlsDone = time.Now() },
		GotFirstResponseByte: func() {
			gotFirstByte = time.Now()
		},
	}
	ctx := httptrace.WithClientTrace(context.Background(), trace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pc.ipCheck, nil)
	if err != nil {
		return checkOutcome{err: err}
	}

	resp, err := client.Do(req)
	var tlsHandshakeMs, ttfbMs int64
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		tlsHandshakeMs = tlsDone.Sub(tlsStart).Milliseconds()
	}
	if !gotFirstByte.IsZero() {
		ttfbMs = gotFirstByte.Sub(start).Milliseconds()
	}

	if err != nil {
		canConnect := !tlsDone.IsZero() || !gotFirstByte.IsZero()
		return checkOutcome{
			canConnect:     canConnect,
			canTransfer:    false,
			tlsHandshakeMs: tlsHandshakeMs,
			ttfbMs:         ttfbMs,
			err:            err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return checkOutcome{
			canConnect:     true,
			canTransfer:    false,
			latency:        time.Duration(ttfbMs) * time.Millisecond,
			tlsHandshakeMs: tlsHandshakeMs,
			ttfbMs:         ttfbMs,
			httpStatus:     resp.StatusCode,
			err:            fmt.Errorf("HTTP %d from %s", resp.StatusCode, pc.ipCheck),
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return checkOutcome{
			canConnect:     true,
			canTransfer:    false,
			latency:        time.Duration(ttfbMs) * time.Millisecond,
			tlsHandshakeMs: tlsHandshakeMs,
			ttfbMs:         ttfbMs,
			httpStatus:     resp.StatusCode,
			err:            err,
		}
	}

	proxyIP := strings.TrimSpace(string(body))
	if net.ParseIP(proxyIP) == nil {
		return checkOutcome{
			canConnect:     true,
			canTransfer:    false,
			latency:        time.Duration(ttfbMs) * time.Millisecond,
			tlsHandshakeMs: tlsHandshakeMs,
			ttfbMs:         ttfbMs,
			httpStatus:     resp.StatusCode,
			err:            fmt.Errorf("invalid IP %q returned via proxy", proxyIP),
		}
	}

	pc.mu.RLock()
	currentHostIP := pc.currentIP
	pc.mu.RUnlock()

	logMessage := fmt.Sprintf("Source IP: %s | Proxy IP: %s", currentHostIP, proxyIP)
	success := currentHostIP != "" && proxyIP != currentHostIP
	var checkErr error
	if !success {
		if currentHostIP == "" {
			// Without a known host IP the comparison is meaningless; don't
			// misdiagnose it as a transparent-proxy leak.
			checkErr = fmt.Errorf("host IP unknown (IP check endpoint unreachable), cannot verify source IP %s", proxyIP)
		} else {
			checkErr = fmt.Errorf("proxy returned host IP %s (transparent or routing leak)", proxyIP)
		}
	}

	latency := time.Duration(ttfbMs) * time.Millisecond
	if latency == 0 {
		latency = time.Since(start)
	}
	return checkOutcome{
		success:        success,
		canConnect:     true,
		canTransfer:    success,
		logMessage:     logMessage,
		latency:        latency,
		tlsHandshakeMs: tlsHandshakeMs,
		ttfbMs:         ttfbMs,
		httpStatus:     resp.StatusCode,
		err:            checkErr,
	}
}

func (pc *ProxyChecker) SetTargetManager(tm *TargetManager) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.targetManager = tm
}

func (pc *ProxyChecker) GetTargetManager() *TargetManager {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.targetManager
}

func (pc *ProxyChecker) checkByGen(client *http.Client) checkOutcome {
	targets := []string{pc.genMethodURL}
	if tm := pc.GetTargetManager(); tm != nil {
		configured := tm.GetTargets()
		if len(configured) > 0 {
			targets = configured
		}
	}

	var lastOutcome checkOutcome
	for _, targetURL := range targets {
		var tlsStart, tlsDone time.Time
		var gotFirstByte time.Time
		start := time.Now()
		trace := &httptrace.ClientTrace{
			TLSHandshakeStart: func() { tlsStart = time.Now() },
			TLSHandshakeDone:  func(_ tls.ConnectionState, _ error) { tlsDone = time.Now() },
			GotFirstResponseByte: func() {
				gotFirstByte = time.Now()
			},
		}
		ctx := httptrace.WithClientTrace(context.Background(), trace)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			lastOutcome = checkOutcome{err: err}
			continue
		}

		resp, err := client.Do(req)
		var tlsHandshakeMs, ttfbMs int64
		if !tlsStart.IsZero() && !tlsDone.IsZero() {
			tlsHandshakeMs = tlsDone.Sub(tlsStart).Milliseconds()
		}
		if !gotFirstByte.IsZero() {
			ttfbMs = gotFirstByte.Sub(start).Milliseconds()
		}

		if err != nil {
			canConnect := !tlsDone.IsZero() || !gotFirstByte.IsZero()
			lastOutcome = checkOutcome{
				canConnect:     canConnect,
				canTransfer:    false,
				tlsHandshakeMs: tlsHandshakeMs,
				ttfbMs:         ttfbMs,
				err:            err,
			}
			continue
		}

		status := resp.StatusCode
		resp.Body.Close()

		latency := time.Duration(ttfbMs) * time.Millisecond
		if latency == 0 {
			latency = time.Since(start)
		}

		if status >= 200 && status < 400 {
			logMessage := fmt.Sprintf("Status: %d via %s", status, targetURL)
			return checkOutcome{
				success:        true,
				canConnect:     true,
				canTransfer:    true,
				logMessage:     logMessage,
				latency:        latency,
				tlsHandshakeMs: tlsHandshakeMs,
				ttfbMs:         ttfbMs,
				httpStatus:     status,
			}
		}
		lastOutcome = checkOutcome{
			success:        false,
			canConnect:     true,
			canTransfer:    false,
			logMessage:     fmt.Sprintf("HTTP %d from %s", status, targetURL),
			latency:        latency,
			tlsHandshakeMs: tlsHandshakeMs,
			ttfbMs:         ttfbMs,
			httpStatus:     status,
			err:            fmt.Errorf("HTTP %d from %s", status, targetURL),
		}
	}

	return lastOutcome
}

func (pc *ProxyChecker) checkByDownload(client *http.Client) checkOutcome {
	if pc.downloadURL == "" {
		return checkOutcome{
			logMessage: "Download URL not configured",
			err:        fmt.Errorf("download URL not configured"),
		}
	}

	var tlsStart, tlsDone time.Time
	var gotFirstByte time.Time
	start := time.Now()
	trace := &httptrace.ClientTrace{
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone:  func(_ tls.ConnectionState, _ error) { tlsDone = time.Now() },
		GotFirstResponseByte: func() {
			gotFirstByte = time.Now()
		},
	}
	ctx := httptrace.WithClientTrace(context.Background(), trace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pc.downloadURL, nil)
	if err != nil {
		return checkOutcome{err: err}
	}

	downloadClient := &http.Client{
		Transport: client.Transport,
		Timeout:   time.Second * time.Duration(pc.downloadTimeout),
	}

	resp, err := downloadClient.Do(req)
	var tlsHandshakeMs, ttfbMs int64
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		tlsHandshakeMs = tlsDone.Sub(tlsStart).Milliseconds()
	}
	if !gotFirstByte.IsZero() {
		ttfbMs = gotFirstByte.Sub(start).Milliseconds()
	}

	if err != nil {
		canConnect := !tlsDone.IsZero() || !gotFirstByte.IsZero()
		return checkOutcome{
			canConnect:     canConnect,
			canTransfer:    false,
			tlsHandshakeMs: tlsHandshakeMs,
			ttfbMs:         ttfbMs,
			err:            err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return checkOutcome{
			canConnect:     true,
			canTransfer:    false,
			logMessage:     fmt.Sprintf("HTTP status: %d", resp.StatusCode),
			latency:        time.Duration(ttfbMs) * time.Millisecond,
			tlsHandshakeMs: tlsHandshakeMs,
			ttfbMs:         ttfbMs,
			httpStatus:     resp.StatusCode,
			err:            fmt.Errorf("HTTP status: %d", resp.StatusCode),
		}
	}

	totalBytes := int64(0)
	buffer := make([]byte, 8192)

	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			totalBytes += int64(n)
		}

		if totalBytes >= pc.downloadMinSize {
			break
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return checkOutcome{
				canConnect:     true,
				canTransfer:    false,
				logMessage:     fmt.Sprintf("Download error after %d bytes: %v", totalBytes, err),
				latency:        time.Duration(ttfbMs) * time.Millisecond,
				tlsHandshakeMs: tlsHandshakeMs,
				ttfbMs:         ttfbMs,
				err:            err,
			}
		}
	}

	success := totalBytes >= pc.downloadMinSize
	logMessage := fmt.Sprintf("Downloaded: %d bytes (min: %d)", totalBytes, pc.downloadMinSize)
	latency := time.Duration(ttfbMs) * time.Millisecond
	if latency == 0 {
		latency = time.Since(start)
	}

	return checkOutcome{
		success:        success,
		canConnect:     true,
		canTransfer:    success,
		logMessage:     logMessage,
		latency:        latency,
		tlsHandshakeMs: tlsHandshakeMs,
		ttfbMs:         ttfbMs,
	}
}

// UpdateProxies swaps in a new proxy set. Metrics are rendered from the current
// set at scrape time, so surviving proxies keep their last result (no blink to 0,
// the #148 regression) and removed proxies simply stop being emitted on the next
// scrape — no metric deletion needed. The caller should run an immediate check to
// populate the new proxies and may call PruneStaleResults to drop cached results
// for removed proxies.
func (pc *ProxyChecker) UpdateProxies(newProxies []*models.ProxyConfig) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	// Same invariant as NewProxyChecker: assign StableIDs at the only write
	// point of pc.proxies so read paths never mutate shared configs.
	models.AssignStableIDs(newProxies)
	pc.proxies = newProxies
}

// PruneStaleResults drops cached results for proxies no longer in the current set.
// This is memory hygiene only: stale results are never emitted (the snapshot
// iterates the current set), but pruning keeps the results map from growing across
// many subscription changes.
func (pc *ProxyChecker) PruneStaleResults() {
	pc.mu.RLock()
	currentKeys := make(map[proxyMetricLabels]struct{}, len(pc.proxies))
	for _, proxy := range pc.proxies {
		currentKeys[proxyMetricKey(proxy)] = struct{}{}
	}
	pc.mu.RUnlock()

	pc.results.Range(func(key, _ interface{}) bool {
		if _, ok := currentKeys[key.(proxyMetricLabels)]; !ok {
			pc.results.Delete(key)
		}
		return true
	})
}

// MetricsSnapshot returns one ProxyMetric per current proxy that has a check
// result, attaching its custom metricsLabels. The metrics collector renders these
// at scrape time, so the exported series always match the current proxy set.
func (pc *ProxyChecker) MetricsSnapshot() []metrics.ProxyMetric {
	pc.mu.RLock()
	proxies := make([]*models.ProxyConfig, len(pc.proxies))
	copy(proxies, pc.proxies)
	// Snapshot the filter under the same lock: reading pc.disabledFilter here
	// without it races with SetDisabledFilter's write.
	disabledFilter := pc.disabledFilter
	pc.mu.RUnlock()

	out := make([]metrics.ProxyMetric, 0, len(proxies))
	for _, proxy := range proxies {
		key := proxyMetricKey(proxy)
		v, ok := pc.results.Load(key)
		if !ok {
			// Not checked yet: no series until the first result, matching prior behavior.
			continue
		}
		r := v.(proxyResult)
		disabled := r.disabled
		if disabledFilter != nil {
			disabled = disabledFilter(proxy.Server, key.stableID)
		}
		var lastCheckSec int64
		if !r.lastCheck.IsZero() {
			lastCheckSec = r.lastCheck.Unix()
		}
		out = append(out, metrics.ProxyMetric{
			Protocol:           key.protocol,
			Address:            key.address,
			Name:               key.name,
			SubName:            key.subName,
			StableID:           key.stableID,
			GroupName:          key.groupName,
			Transport:          proxy.GetTransportType(),
			Security:           proxy.GetSecurityType(),
			CustomLabels:       proxy.MetricsLabels,
			Online:             r.status,
			CanConnect:         r.canConnect,
			CanTransfer:        r.canTransfer,
			LatencyMs:          float64(r.latency.Milliseconds()),
			Disabled:           disabled,
			LastErrorCategory:  int(r.lastErrorCategory),
			LastErrorMsg:       r.lastErrorMsg,
			TLSHandshakeMs:     r.tlsHandshakeMs,
			TTFBMs:             r.ttfbMs,
			DirectProbeSuccess: r.directProbeSuccess,
			DirectProbeRTTMs:   r.directProbeRTTMs,
			DirectProbeErr:     r.directProbeErr,
			LastCheckSec:       lastCheckSec,
		})
	}
	return out
}

func (pc *ProxyChecker) CheckAllProxies() {
	if _, err := pc.GetCurrentIP(); err != nil {
		logger.Warn("Error getting current IP: %v", err)
		if pc.checkMethod == "ip" {
			pc.mu.RLock()
			hasIP := pc.currentIP != ""
			pc.mu.RUnlock()
			if !hasIP {
				return
			}
		}
	}

	pc.mu.RLock()
	proxiesToCheck := make([]*models.ProxyConfig, len(pc.proxies))
	copy(proxiesToCheck, pc.proxies)
	pc.mu.RUnlock()

	runBoundedChecks(proxiesToCheck, pc.checkConcurrency, pc.checkProxyInternal)
}

// runBoundedChecks runs check(p) for every proxy concurrently. concurrency == 0
// keeps the original behavior (all at once); a positive value bounds how many run
// simultaneously via a semaphore, so large subscriptions don't open thousands of
// connections in one burst. Local socks ports are unchanged — each check still
// dials its own fixed port; this only throttles how many run at a time.
func runBoundedChecks(proxies []*models.ProxyConfig, concurrency int, check func(*models.ProxyConfig)) {
	var sem chan struct{}
	if concurrency > 0 {
		sem = make(chan struct{}, concurrency)
	}

	var wg sync.WaitGroup
	for _, proxy := range proxies {
		wg.Add(1)
		go func(p *models.ProxyConfig) {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}
			check(p)
		}(proxy)
	}
	wg.Wait()
}

// GetProxyResultByStableID returns the latest check outcome for the proxy with the
// given stable_id: online status, latency, last-check time as a Unix timestamp in
// seconds (0 if never checked), and whether a result was found.
//
// Lookup is by the unique stable_id (and the full metric key it maps to), the same
// key /metrics uses — so proxies that share a display name are never confused. A
// previous name-based lookup returned the first same-named proxy's result, making
// /config/{id}, the dashboard and the JSON API disagree with /metrics (issue #172).
func (pc *ProxyChecker) GetProxyResultByStableID(stableID string) (bool, time.Duration, int64, bool) {
	pc.mu.RLock()
	var metricKey proxyMetricLabels
	found := false
	for _, proxy := range pc.proxies {
		if proxy.StableID == stableID {
			metricKey = proxyMetricKey(proxy)
			found = true
			break
		}
	}
	pc.mu.RUnlock()

	if !found {
		return false, 0, 0, false
	}

	v, ok := pc.results.Load(metricKey)
	if !ok {
		return false, 0, 0, false
	}

	r := v.(proxyResult)
	var lastCheck int64
	if !r.lastCheck.IsZero() {
		lastCheck = r.lastCheck.Unix()
	}
	return r.status, r.latency, lastCheck, true
}

func (pc *ProxyChecker) GetProxyByStableID(stableID string) (*models.ProxyConfig, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	for _, proxy := range pc.proxies {
		if proxy.StableID == stableID {
			return proxy, true
		}
	}
	return nil, false
}

func (pc *ProxyChecker) GetProxies() []*models.ProxyConfig {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result := make([]*models.ProxyConfig, len(pc.proxies))
	copy(result, pc.proxies)
	return result
}
