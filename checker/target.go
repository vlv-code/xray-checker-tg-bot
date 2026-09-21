package checker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"xray-checker/models"
)

// TargetDiagResult holds the check outcome for one target endpoint.
type TargetDiagResult struct {
	URL        string        `json:"url"`
	Success    bool          `json:"success"`
	StatusCode int           `json:"status_code"`
	Latency    time.Duration `json:"latency"`
	Error      string        `json:"error,omitempty"`
}

// NodeHealth holds reachability results for a proxy server node itself.
type NodeHealth struct {
	ResolvedIP string        `json:"resolved_ip,omitempty"`
	DNSErr     string        `json:"dns_err,omitempty"`
	DNSLatency time.Duration `json:"dns_latency,omitempty"`
	TCPPing    time.Duration `json:"tcp_ping,omitempty"`
	TCPErr     string        `json:"tcp_err,omitempty"`
	UDPPing    time.Duration `json:"udp_ping,omitempty"`
	UDPErr     string        `json:"udp_err,omitempty"`
	TLSErr     string        `json:"tls_err,omitempty"`
	TLSLatency time.Duration `json:"tls_latency,omitempty"`
}

// ProxyDiagReport holds diagnostic results across all target endpoints for one proxy.
type ProxyDiagReport struct {
	ProxyName  string             `json:"proxy_name"`
	Protocol   string             `json:"protocol"`
	Server     string             `json:"server"`
	Port       int                `json:"port"`
	StableID   string             `json:"stable_id"`
	NodeHealth NodeHealth         `json:"node_health"`
	CheckHost  *CheckHostSummary  `json:"check_host,omitempty"`
	Targets    []TargetDiagResult `json:"targets"`
	Status     string             `json:"status"` // "online", "degraded", "offline", "disabled"
	Verdict    string             `json:"verdict"`
	Disabled   bool               `json:"disabled,omitempty"`
}

// TargetManager manages the list of target endpoints to test proxies against.
type TargetManager struct {
	mu      sync.RWMutex
	targets []string
}

// NewTargetManager creates a new TargetManager with initial targets.
func NewTargetManager(initialTargets []string) *TargetManager {
	tm := &TargetManager{
		targets: make([]string, 0),
	}
	for _, t := range initialTargets {
		_ = tm.AddTarget(t)
	}
	if len(tm.targets) == 0 {
		tm.targets = []string{
			"https://cp.cloudflare.com/generate_204",
			"https://www.gstatic.com/generate_204",
		}
	}
	return tm
}

// GetTargets returns a copy of current target URLs.
func (tm *TargetManager) GetTargets() []string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	out := make([]string, len(tm.targets))
	copy(out, tm.targets)
	return out
}

func isBlockedTargetIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4.IsLoopback() || ip4.IsPrivate() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() || ip4.IsMulticast() {
			return true
		}
		if ip4[0] == 0 || (ip4[0] == 169 && ip4[1] == 254) {
			return true
		}
		if ip4[0] == 100 && (ip4[1]&0xc0) == 64 {
			return true
		}
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func validateTargetURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("target URL cannot be empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid URL: must be http or https with valid host")
	}

	hostname := u.Hostname()
	if hostname == "" {
		return nil, fmt.Errorf("missing host in target URL")
	}

	if strings.EqualFold(hostname, "localhost") || strings.HasSuffix(strings.ToLower(hostname), ".localhost") || strings.HasSuffix(strings.ToLower(hostname), ".local") {
		return nil, fmt.Errorf("SSRF: local host %q is blocked", hostname)
	}

	if ip := net.ParseIP(hostname); ip != nil {
		if isBlockedTargetIP(ip) {
			return nil, fmt.Errorf("SSRF: access to private/reserved IP %s is blocked", ip)
		}
	}

	return u, nil
}

// AddTarget adds a validated target URL to the list.
func (tm *TargetManager) AddTarget(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if _, err := validateTargetURL(rawURL); err != nil {
		return err
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	for _, existing := range tm.targets {
		if strings.EqualFold(existing, rawURL) {
			return fmt.Errorf("target URL already exists")
		}
	}

	tm.targets = append(tm.targets, rawURL)
	return nil
}

// RemoveTarget removes a target URL.
func (tm *TargetManager) RemoveTarget(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	tm.mu.Lock()
	defer tm.mu.Unlock()

	idx := -1
	for i, existing := range tm.targets {
		if strings.EqualFold(existing, rawURL) {
			idx = i
			break
		}
	}

	if idx == -1 {
		return fmt.Errorf("target URL not found")
	}

	tm.targets = append(tm.targets[:idx], tm.targets[idx+1:]...)
	return nil
}

// SetTargets atomically replaces the targets list with validated targets.
func (tm *TargetManager) SetTargets(newTargets []string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	valid := make([]string, 0, len(newTargets))
	for _, t := range newTargets {
		t = strings.TrimSpace(t)
		if _, err := validateTargetURL(t); err == nil {
			valid = append(valid, t)
		}
	}
	if len(valid) > 0 {
		tm.targets = valid
	}
}

// CheckSingleTarget tests an endpoint via the provided http.Client.
func CheckSingleTarget(client *http.Client, targetURL string) TargetDiagResult {
	var ttfb time.Duration
	start := time.Now()
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() {
			ttfb = time.Since(start)
		},
	}
	ctx := httptrace.WithClientTrace(context.Background(), trace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return TargetDiagResult{
			URL:     targetURL,
			Success: false,
			Error:   err.Error(),
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return TargetDiagResult{
			URL:     targetURL,
			Success: false,
			Latency: time.Since(start),
			Error:   simplifyError(err),
		}
	}
	defer resp.Body.Close()

	if ttfb == 0 {
		ttfb = time.Since(start)
	}

	success := resp.StatusCode >= 200 && resp.StatusCode < 400
	var errStr string
	if !success {
		errStr = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	return TargetDiagResult{
		URL:        targetURL,
		Success:    success,
		StatusCode: resp.StatusCode,
		Latency:    ttfb,
		Error:      errStr,
	}
}

func simplifyError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "context deadline exceeded") || strings.Contains(s, "Client.Timeout") || strings.Contains(s, "i/o timeout"):
		return "Timeout"
	case strings.Contains(s, "EOF"):
		return "EOF / Connection reset"
	case strings.Contains(s, "connection refused"):
		return "Connection refused"
	case strings.Contains(s, "no such host"):
		return "DNS resolution error"
	case strings.Contains(s, "certificate has expired"):
		return "Certificate expired"
	case strings.Contains(s, "certificate is not trusted") || strings.Contains(s, "unknown authority"):
		return "Untrusted certificate"
	default:
		return s
	}
}

// IsUDPProto returns true if the proxy protocol operates over UDP/QUIC rather than TCP.
func IsUDPProto(proto string) bool {
	p := strings.ToLower(strings.TrimSpace(proto))
	return p == "hysteria" || p == "hysteria2" || p == "tuic" || p == "wireguard"
}

// ProbeNodeHealth runs direct low-level reachability tests against the proxy node server.
func ProbeNodeHealth(server string, port int, protocol string, security string, sni string, allowInsecure bool) NodeHealth {
	var health NodeHealth

	// 1. DNS Resolution
	targetIP := server
	if net.ParseIP(server) != nil {
		health.ResolvedIP = server
	} else {
		dnsStart := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
		defer cancel()

		var r net.Resolver
		ips, err := r.LookupIP(ctx, "ip", server)
		health.DNSLatency = time.Since(dnsStart)
		if err != nil {
			health.DNSErr = simplifyError(err)
			return health
		}
		if len(ips) == 0 {
			health.DNSErr = "No IP found"
			return health
		}
		health.ResolvedIP = ips[0].String()
		targetIP = health.ResolvedIP
	}

	// For UDP-based protocols (Hysteria, Hysteria2, TUIC, Wireguard), test UDP reachability
	// directly since the node listens on UDP/QUIC rather than TCP.
	if IsUDPProto(protocol) {
		udpAddr := net.JoinHostPort(targetIP, fmt.Sprintf("%d", port))
		udpStart := time.Now()
		conn, err := net.DialTimeout("udp", udpAddr, 2500*time.Millisecond)
		if err != nil {
			health.UDPErr = simplifyError(err)
			return health
		}
		defer conn.Close()

		_ = conn.SetDeadline(time.Now().Add(1000 * time.Millisecond))
		_, err = conn.Write([]byte{0x00})
		if err != nil {
			health.UDPErr = simplifyError(err)
			return health
		}

		// Check if remote actively rejects via ICMP port unreachable or responds
		buf := make([]byte, 512)
		n, rErr := conn.Read(buf)
		if rErr != nil {
			errMsg := strings.ToLower(rErr.Error())
			if errors.Is(rErr, syscall.ECONNREFUSED) || strings.Contains(errMsg, "refused") {
				health.UDPErr = "connection refused (ICMP unreachable)"
				return health
			}
			var netErr net.Error
			if errors.As(rErr, &netErr) && netErr.Timeout() {
				// Silence after a garbage datagram is EXPECTED from QUIC
				// (Hysteria2/TUIC) and WireGuard servers — they drop invalid
				// packets without answering. A timeout proves nothing either
				// way, so report it as inconclusive (no error, no ping)
				// instead of condemning healthy nodes.
				return health
			}
			health.UDPErr = simplifyError(rErr)
			health.UDPPing = 0
			return health
		}
		if n > 0 {
			health.UDPPing = time.Since(udpStart)
			if health.UDPPing == 0 {
				health.UDPPing = time.Microsecond
			}
		}
		return health
	}

	// 2. TCP Ping
	tcpAddr := net.JoinHostPort(targetIP, fmt.Sprintf("%d", port))
	tcpStart := time.Now()
	conn, err := net.DialTimeout("tcp", tcpAddr, 2500*time.Millisecond)
	if err != nil {
		// Record a ping only on a successful dial: a timed-out dial would
		// otherwise expose a bogus ~2500ms "ping" in diagnostics.
		health.TCPErr = simplifyError(err)
		return health
	}
	health.TCPPing = time.Since(tcpStart)
	if health.TCPPing == 0 {
		health.TCPPing = time.Microsecond
	}
	defer conn.Close()

	// 3. TLS Probe (if security == "tls")
	if strings.EqualFold(security, "tls") {
		serverName := sni
		if serverName == "" {
			serverName = server
		}
		tlsConf := &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: allowInsecure,
		}
		tlsConn := tls.Client(conn, tlsConf)
		_ = tlsConn.SetDeadline(time.Now().Add(2500 * time.Millisecond))
		tlsStart := time.Now()
		if err := tlsConn.Handshake(); err != nil {
			health.TLSErr = simplifyError(err)
		} else {
			health.TLSLatency = time.Since(tlsStart)
		}
	}

	return health
}

// DetermineVerdict produces a health status and concise root-cause diagnosis.
// End-to-end target connectivity through the proxy tunnel is the primary source of truth.
func DetermineVerdict(proto string, health NodeHealth, targets []TargetDiagResult) (status string, verdict string) {
	successCount := 0
	eofCount := 0
	forbiddenCount := 0
	timeoutCount := 0

	for _, t := range targets {
		if t.Success {
			successCount++
		} else {
			errLow := strings.ToLower(t.Error)
			if strings.Contains(errLow, "eof") || strings.Contains(errLow, "reset") {
				eofCount++
			} else if strings.Contains(errLow, "403") {
				forbiddenCount++
			} else if strings.Contains(errLow, "timeout") {
				timeoutCount++
			}
		}
	}

	// 1. If all targets succeeded, the proxy is 100% online!
	if len(targets) > 0 && successCount == len(targets) {
		return "online", "Полностью исправен"
	}

	// 2. If some targets succeeded, it is degraded
	if successCount > 0 {
		return "degraded", fmt.Sprintf("Частичная доступность (%d/%d сайтов доступны)", successCount, len(targets))
	}

	// 3. If no targets succeeded, investigate the root cause using low-level probes:
	if health.DNSErr != "" {
		return "offline", fmt.Sprintf("Сбой DNS домена ноды (%s)", health.DNSErr)
	}

	if IsUDPProto(proto) && health.UDPErr != "" {
		if strings.Contains(strings.ToLower(health.UDPErr), "refused") || strings.Contains(strings.ToLower(health.UDPErr), "unreachable") {
			return "offline", "UDP-порт недоступен (ICMP Port Unreachable / сервис остановлен)"
		}
		return "offline", fmt.Sprintf("Сбой UDP подключения (%s)", health.UDPErr)
	}

	if !IsUDPProto(proto) && health.TCPErr != "" {
		if strings.Contains(health.TCPErr, "Timeout") {
			return "offline", "Нода не отвечает на TCP (таймаут: хост недоступен с узла проверки)"
		}
		if strings.Contains(strings.ToLower(health.TCPErr), "refused") {
			return "offline", "TCP-соединение сброшено (порт закрыт / сервис на ноде остановлен)"
		}
		return "offline", fmt.Sprintf("Сбой TCP подключения (%s)", health.TCPErr)
	}

	if !IsUDPProto(proto) && health.TLSErr != "" {
		return "offline", fmt.Sprintf("Сбой TLS рукопожатия (%s)", health.TLSErr)
	}

	if eofCount > 0 {
		return "offline", "Сервер сбросил сессию (ошибка авторизации/UUID или закрыто сервером)"
	}
	if forbiddenCount > 0 {
		return "offline", "Ограничение доступа со стороны целевых сервисов (HTTP 403 / Cloudflare Challenge)"
	}
	if timeoutCount > 0 {
		return "offline", "Таймаут проксирования через туннель"
	}

	return "offline", "Проксирование через туннель завершилось ошибкой"
}

// EnrichVerdictWithCheckHost enriches a diagnostic verdict with objective Check-Host findings
func EnrichVerdictWithCheckHost(verdict string, ch *CheckHostSummary, dnsErr ...string) string {
	if ch == nil {
		return verdict
	}
	hasDNSErr := len(dnsErr) > 0 && dnsErr[0] != ""
	switch {
	case !ch.RUAvailable && ch.WorldAvailable:
		return fmt.Sprintf("%s | Check-Host: хост недоступен из узлов РФ, но отвечает из зарубежных сетей", verdict)
	case !ch.RUAvailable && !ch.WorldAvailable:
		if hasDNSErr {
			return fmt.Sprintf("%s | Check-Host: домен не резолвится также и на внешних узлах (РФ и Мир)", verdict)
		}
		return fmt.Sprintf("%s | Check-Host: хост недоступен как из РФ, так и из других стран", verdict)
	case ch.RUAvailable && ch.WorldAvailable:
		if hasDNSErr {
			return fmt.Sprintf("%s | Check-Host: внешние узлы смогли отрезолвить домен (возможен локальный сбой DNS на узле проверки)", verdict)
		}
		return fmt.Sprintf("%s | Check-Host: хост отвечает из РФ и других стран (вероятна фильтрация/DPI на узле проверки)", verdict)
	default:
		return fmt.Sprintf("%s | Check-Host: хост доступен из РФ, но недоступен из части внешних сетей", verdict)
	}
}

// alignTargetErrorsWithNodeHealth adjusts target error messages when the proxy tunnel
// could not be established at all (all targets failed due to low-level node DNS/TCP/UDP failure).
// If at least one target succeeded, the tunnel IS up and functioning, so target errors are preserved!
func alignTargetErrorsWithNodeHealth(health NodeHealth, proto string, targets []TargetDiagResult) {
	successCount := 0
	for _, t := range targets {
		if t.Success {
			successCount++
		}
	}
	if successCount > 0 {
		return // Tunnel is up and working, preserve real target-level error messages
	}

	if health.DNSErr != "" {
		for tIdx := range targets {
			if !targets[tIdx].Success {
				targets[tIdx].Error = fmt.Sprintf("Туннель не поднят (сбой DNS ноды: %s)", health.DNSErr)
			}
		}
	} else if health.TCPErr != "" {
		for tIdx := range targets {
			if !targets[tIdx].Success {
				targets[tIdx].Error = fmt.Sprintf("Туннель не поднят (сбой TCP ноды: %s)", health.TCPErr)
			}
		}
	} else if IsUDPProto(proto) && health.UDPErr != "" {
		for tIdx := range targets {
			if !targets[tIdx].Success {
				targets[tIdx].Error = fmt.Sprintf("Туннель не поднят (сбой UDP ноды: %s)", health.UDPErr)
			}
		}
	}
}

// RunDiagnostics executes concurrent tests against all given targets for each proxy.
func (pc *ProxyChecker) RunDiagnostics(targets []string) []ProxyDiagReport {
	pc.mu.RLock()
	proxies := make([]*models.ProxyConfig, len(pc.proxies))
	copy(proxies, pc.proxies)
	pc.mu.RUnlock()

	if len(targets) == 0 {
		targets = []string{
			"https://cp.cloudflare.com/generate_204",
			"https://www.gstatic.com/generate_204",
		}
	}

	results := make([]ProxyDiagReport, len(proxies))
	var wg sync.WaitGroup

	// Bound the fan-out the same way CheckAllProxies does: every goroutine
	// opens its own SOCKS connections, spawns per-target probes, and offline
	// proxies add a synchronous Check-Host poll — a large subscription must
	// not launch all of that in a single burst.
	var sem chan struct{}
	if pc.checkConcurrency > 0 {
		sem = make(chan struct{}, pc.checkConcurrency)
	}

	for i, p := range proxies {
		wg.Add(1)
		go func(idx int, proxy *models.ProxyConfig) {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}

			if pc.IsProxyDisabled(proxy) {
				results[idx] = ProxyDiagReport{
					ProxyName: proxy.Name,
					Protocol:  proxy.Protocol,
					Server:    proxy.Server,
					Port:      proxy.Port,
					StableID:  proxy.StableID,
					Status:    "disabled",
					Verdict:   "Проверка отключена в настройках бота",
					Disabled:  true,
				}
				return
			}

			proxyURL := fmt.Sprintf("socks5://127.0.0.1:%d", pc.startPort+proxy.Index)
			proxyURLParsed, err := url.Parse(proxyURL)
			if err != nil {
				results[idx] = ProxyDiagReport{
					ProxyName: proxy.Name,
					Protocol:  proxy.Protocol,
					Server:    proxy.Server,
					Port:      proxy.Port,
					StableID:  proxy.StableID,
					Targets: []TargetDiagResult{{
						URL:     "local socks5",
						Success: false,
						Error:   err.Error(),
					}},
					Status:  "offline",
					Verdict: "Ошибка конфигурации локального SOCKS5 порта",
				}
				return
			}

			diagTimeout := time.Duration(pc.ipCheckTimeout) * time.Second
			if diagTimeout <= 0 {
				diagTimeout = 10 * time.Second
			}
			transport := &http.Transport{
				Proxy:             http.ProxyURL(proxyURLParsed),
				DisableKeepAlives: true,
			}
			defer transport.CloseIdleConnections()
			client := &http.Client{
				Transport: transport,
				Timeout:   diagTimeout,
			}

			// Run Node health probe in parallel with target tests
			var health NodeHealth
			var nodeWg sync.WaitGroup
			nodeWg.Add(1)
			go func() {
				defer nodeWg.Done()
				health = ProbeNodeHealth(proxy.Server, proxy.Port, proxy.Protocol, proxy.Security, proxy.SNI, proxy.AllowInsecure)
			}()

			targetResults := make([]TargetDiagResult, len(targets))
			var innerWg sync.WaitGroup
			for tIdx, targetURL := range targets {
				innerWg.Add(1)
				go func(resIdx int, tURL string) {
					defer innerWg.Done()
					targetResults[resIdx] = CheckSingleTarget(client, tURL)
				}(tIdx, targetURL)
			}
			innerWg.Wait()
			nodeWg.Wait()

			// Align target errors with low-level node health only if ALL targets failed
			// (if at least one target succeeded, the tunnel is functioning and target errors are preserved)
			alignTargetErrorsWithNodeHealth(health, proxy.Protocol, targetResults)

			status, verdict := DetermineVerdict(proxy.Protocol, health, targetResults)

			var checkHostSummary *CheckHostSummary
			// Run Check-Host if the node has connectivity issues
			if status == "offline" && proxy.Server != "" {
				chClient := NewCheckHostClient("", 1500*time.Millisecond)
				chCtx, chCancel := context.WithTimeout(context.Background(), 7*time.Second)
				// Explicit copy: appending to the package-level defaults would
				// share (and on growth, corrupt) their backing array across
				// concurrent goroutines.
				fastNodes := make([]string, 0, len(DefaultFastRUNodes)+len(DefaultFastWorldNodes))
				fastNodes = append(fastNodes, DefaultFastRUNodes...)
				fastNodes = append(fastNodes, DefaultFastWorldNodes...)
				var chSummary *CheckHostSummary
				var chErr error
				if IsUDPProto(proxy.Protocol) {
					chSummary, chErr = chClient.CheckPing(chCtx, proxy.Server, fastNodes)
				} else if proxy.Port > 0 {
					targetHost := fmt.Sprintf("%s:%d", proxy.Server, proxy.Port)
					chSummary, chErr = chClient.CheckTCP(chCtx, targetHost, fastNodes)
				}
				chCancel()
				if chErr == nil && chSummary != nil {
					checkHostSummary = chSummary
					verdict = EnrichVerdictWithCheckHost(verdict, chSummary, health.DNSErr)
				}
			}

			results[idx] = ProxyDiagReport{
				ProxyName:  proxy.Name,
				Protocol:   proxy.Protocol,
				Server:     proxy.Server,
				Port:       proxy.Port,
				StableID:   proxy.StableID,
				NodeHealth: health,
				CheckHost:  checkHostSummary,
				Targets:    targetResults,
				Status:     status,
				Verdict:    verdict,
			}
		}(i, p)
	}

	wg.Wait()
	return results
}
