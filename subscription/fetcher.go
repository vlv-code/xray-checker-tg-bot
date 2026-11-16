package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"xray-checker/config"
	"xray-checker/logger"
)

const (
	// subscriptionHWID is a fixed device id sent with subscription requests. It is
	// intentionally constant (not per-request) so panels that enforce HWID/device
	// limits register a single device for the checker.
	subscriptionHWID = "0JLQq9Ca0JvQrtCn0Jgg0JHQm9Cp0KLQrCBIV0lE"
	// subscriptionJSONUserAgent impersonates an app whose responses are full JSON
	// configs (used when --subscription-json-format is enabled).
	subscriptionJSONUserAgent = "Happ/1.0"
)

type fetchResult struct {
	Content []byte
	Name    string
}

// subscriptionClient is shared by every subscription fetch: building a fresh
// transport per request stranded idle connections with no IdleConnTimeout until
// GC finalizers reaped them, and the periodic re-fetch cadence made that leak
// grow steadily.
//
// CheckRedirect re-validates every hop: Go's default redirect handling would
// otherwise follow up to 10 redirects with no SSRF re-validation and forward
// all original headers (including panel-auth X-headers) to any host.
// Residual risk: when an outbound proxy is configured (HTTP_PROXY or
// --subscription.bootstrap-proxy), the dial-time IP guard only ever sees the
// proxy address; the redirect-level URL + DNS validation below is the
// mitigation for that gap, but DNS resolution performed by the proxy itself
// (proxy-side DNS rebinding) cannot be detected from this process.
var subscriptionClient = &http.Client{
	Timeout:       30 * time.Second,
	Transport:     newSafeTransport(),
	CheckRedirect: subscriptionRedirectPolicy,
}

// subscriptionRedirectPolicy applies the same SSRF rules as the initial
// target to every redirect hop and strips identifying headers on cross-host
// hops so subscription credentials never leak to a redirected-to host.
func subscriptionRedirectPolicy(req *http.Request, via []*http.Request) error {
	// Setting CheckRedirect replaces Go's default, so re-implement its
	// "stopped after 10 redirects" limit here.
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if err := validateSubscriptionTarget(req.URL.String()); err != nil {
		return fmt.Errorf("SSRF: redirect to %s blocked: %w", RedactURL(req.URL.String()), err)
	}
	// Cross-host hop: keep only non-identifying headers. The header copier in
	// net/http re-applies the original headers on every hop, but it runs
	// BEFORE this policy (see net/http.Client.do), so stripping here is
	// effective for the outgoing request at each hop.
	for _, prev := range via {
		if req.URL.Host != prev.URL.Host {
			stripCrossHostHeaders(req.Header)
			break
		}
	}
	return nil
}

// stripCrossHostHeaders reduces h to the headers that are safe to send to a
// host other than the one the subscription was configured for.
func stripCrossHostHeaders(h http.Header) {
	for key := range h {
		switch http.CanonicalHeaderKey(key) {
		case "User-Agent", "Accept", "Accept-Encoding":
		default:
			delete(h, key)
		}
	}
}

func (p *Parser) fetchURLContent(source string) (*fetchResult, error) {
	cleanURL, fragmentName := p.extractURLFragment(source)
	if err := validateSubscriptionTarget(cleanURL); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cleanURL, nil)
	if err != nil {
		return nil, err
	}

	sub := config.CLIConfig.Subscription
	req.Header.Set("Accept", "*/*")
	switch {
	case sub.UserAgent != "":
		// Explicit override: the user controls exactly which client to impersonate.
		req.Header.Set("User-Agent", sub.UserAgent)
	case sub.JSONFormat:
		// App-like headers so panels (e.g. Remnawave) return full JSON configs with
		// individual grouped/balancer nodes instead of collapsed base64 share links.
		req.Header.Set("User-Agent", subscriptionJSONUserAgent)
		req.Header.Set("X-Hwid", subscriptionHWID)
	default:
		req.Header.Set("User-Agent", "Xray-Checker")
		req.Header.Set("X-Device-OS", "CheckerOS")
		req.Header.Set("X-Ver-OS", config.Version)
		req.Header.Set("X-Device-Model", "Xray-Checker Pro Max")
		req.Header.Set("X-Hwid", subscriptionHWID)
	}

	// User-supplied headers are applied last so they can override any of the above.
	for _, h := range sub.Headers {
		key, value, ok := strings.Cut(h, ":")
		if !ok {
			logger.Warn("Ignoring malformed subscription header (want 'Key: Value'): %s", h)
			continue
		}
		req.Header.Set(strings.TrimSpace(key), strings.TrimSpace(value))
	}

	client := subscriptionClient
	resp, err := doSubscriptionRequest(client, req)
	if err != nil {
		// Guard MUST stay between the request and the Body-close defer:
		// on transport failure resp is nil and the deferred Close would
		// panic the process (Guardian P0-1, 2026-11-16).
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	content, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}

	name := fragmentName
	if name == "" {
		name = p.extractNameFromHeader(resp.Header.Get("profile-title"))
	}

	return &fetchResult{
		Content: content,
		Name:    name,
	}, nil
}

// doSubscriptionRequest issues req on the shared subscription client. A raw
// *url.Error embeds the full request URL — including ?token=... query strings
// and redirect Location targets that never passed pre-flight validation — so
// transport failures are redacted before escaping this package.
func doSubscriptionRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, redactURLError(err)
	}
	return resp, nil
}

func (p *Parser) extractURLFragment(source string) (cleanURL string, name string) {
	if idx := strings.LastIndex(source, "#"); idx != -1 {
		name = strings.TrimSpace(source[idx+1:])
		cleanURL = source[:idx]
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		return cleanURL, name
	}
	return source, ""
}

func (p *Parser) extractNameFromHeader(headerValue string) string {
	if headerValue == "" {
		return ""
	}

	headerValue = strings.TrimSpace(headerValue)

	if strings.HasPrefix(headerValue, "base64:") {
		encoded := strings.TrimPrefix(headerValue, "base64:")
		if decoded, err := p.decodeBase64(encoded); err == nil {
			return strings.TrimSpace(string(decoded))
		}
		return ""
	}

	if decoded, err := p.decodeBase64(headerValue); err == nil {
		decodedStr := string(decoded)
		if p.isPrintableString(decodedStr) {
			return strings.TrimSpace(decodedStr)
		}
	}

	return headerValue
}

func (p *Parser) isPrintableString(s string) bool {
	for _, r := range s {
		if r < 32 && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Check IPv4-mapped IPv6 (e.g. ::ffff:127.0.0.1)
	if ip4 := ip.To4(); ip4 != nil {
		if ip4.IsLoopback() || ip4.IsPrivate() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() || ip4.IsMulticast() {
			return true
		}
		// 0.0.0.0/8
		if ip4[0] == 0 {
			return true
		}
		// 169.254.0.0/16
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		// 100.64.0.0/10 (CGNAT / Shared Address Space)
		if ip4[0] == 100 && (ip4[1]&0xc0) == 64 {
			return true
		}
		// 198.18.0.0/15 (benchmarking, RFC 2544)
		if ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
			return true
		}
		// 240.0.0.0/4 (reserved; includes broadcast 255.255.255.255)
		if ip4[0] >= 240 {
			return true
		}
		return false
	}
	// IPv6
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	// IPv6 transition mechanisms embed an IPv4 target inside an IPv6 address:
	// e.g. 2002:7f00:1:: is 6to4 for 127.0.0.1. Legitimate subscription hosts
	// never use these deprecated/unroutable forms, but they bypass the plain
	// loopback/private checks.
	for _, r := range blockedV6TunnelRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// blockedV6TunnelRanges covers IPv6 transition mechanisms whose addresses wrap
// an IPv4 host: NAT64 well-known prefix (RFC 6052), 6to4 (RFC 7526, deprecated)
// and Teredo.
var blockedV6TunnelRanges = []*net.IPNet{
	mustCIDR("64:ff9b::/96"),
	mustCIDR("2002::/16"),
	mustCIDR("2001::/32"),
}

func mustCIDR(cidr string) *net.IPNet {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in CIDR %s: %v", cidr, err))
	}
	return n
}

func isEnvironmentProxy(host, port string) bool {
	for _, envKey := range []string{
		"HTTP_PROXY", "http_proxy",
		"HTTPS_PROXY", "https_proxy",
		"ALL_PROXY", "all_proxy",
		"BOOTSTRAP_PROXY", "bootstrap_proxy",
		"GEO_PROXY", "geo_proxy",
	} {
		val := os.Getenv(envKey)
		if val != "" {
			if u, err := url.Parse(val); err == nil {
				proxyHost := u.Hostname()
				proxyPort := u.Port()
				if proxyPort == "" {
					if u.Scheme == "https" {
						proxyPort = "443"
					} else {
						proxyPort = "80"
					}
				}
				if strings.EqualFold(proxyHost, host) && proxyPort == port {
					return true
				}
			}
		}
	}
	return false
}

func validateSubscriptionTarget(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		// Parse errors embed the raw input URL verbatim; redact it too.
		return fmt.Errorf("invalid subscription URL: %w", redactURLError(err))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid subscription scheme %q, only http/https allowed", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host in subscription URL")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("SSRF: access to localhost is blocked")
	}

	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("SSRF: access to private/reserved IP %s is blocked", ip)
		}
		return nil
	}

	// Bound the pre-flight resolution: an unbounded lookup here stalls both the
	// periodic updater and Telegram-triggered reloads on a hung resolver.
	resolveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(resolveCtx, "ip", host)
	if err != nil {
		return fmt.Errorf("failed to resolve host %s: %w", host, err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("SSRF: host %s resolved to blocked IP %s", host, ip)
		}
	}
	return nil
}

func newSafeTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	proxyFunc := http.ProxyFromEnvironment
	if bp := config.GetBootstrapProxy(); bp != "" {
		if u, err := url.Parse(bp); err == nil {
			proxyFunc = http.ProxyURL(u)
		}
	}
	return &http.Transport{
		Proxy: proxyFunc,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}

			// Allow connecting to an infrastructure proxy configured via environment
			if isEnvironmentProxy(host, port) {
				return dialer.DialContext(ctx, network, addr)
			}

			if ip := net.ParseIP(host); ip != nil {
				if isBlockedIP(ip) {
					return nil, fmt.Errorf("SSRF: access to private/reserved IP %s is blocked", ip)
				}
				return dialer.DialContext(ctx, network, addr)
			}

			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no IP addresses found for host %s", host)
			}

			for _, ip := range ips {
				if isBlockedIP(ip) {
					return nil, fmt.Errorf("SSRF: host %s resolved to blocked IP %s", host, ip)
				}
			}

			targetAddr := net.JoinHostPort(ips[0].String(), port)
			return dialer.DialContext(ctx, network, targetAddr)
		},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
}

// redactURLError rewrites a *url.Error so that its embedded URL carries no
// secrets (query parameters such as ?token=..., userinfo credentials) while
// the operation name and the wrapped cause stay intact — the inner error
// remains reachable via errors.Is/errors.As.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &url.Error{
			Op:  urlErr.Op,
			URL: RedactURL(urlErr.URL),
			Err: urlErr.Err,
		}
	}
	return err
}

// RedactURL returns a copy of rawURL with sensitive parts (query parameters
// e.g. ?token=..., userinfo credentials, fragments) redacted, suitable for
// logging and error reporting without credential leakage.
func RedactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		// Best-effort redaction for URLs that do not even parse.
		s := rawURL
		if idx := strings.IndexByte(s, '?'); idx >= 0 {
			s = s[:idx] + "?<redacted>"
		}
		if schemeEnd := strings.Index(s, "://"); schemeEnd >= 0 {
			if at := strings.IndexByte(s[schemeEnd+3:], '@'); at >= 0 {
				s = s[:schemeEnd+3] + s[schemeEnd+3+at+1:]
			}
		}
		return s
	}
	u.User = nil
	if u.RawQuery != "" {
		u.RawQuery = "<redacted>"
	}
	u.Fragment = ""
	return u.String()
}

// RedactedList returns a slice with each URL redacted.
func RedactedList(urls []string) []string {
	out := make([]string, len(urls))
	for i, u := range urls {
		out[i] = RedactURL(u)
	}
	return out
}
