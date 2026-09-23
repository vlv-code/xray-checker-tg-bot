package config

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"xray-checker/logger"
)

// DefaultNoProxyEntries lists loopback, link-local, and private subnets (RFC 1918)
// that should never be proxied through an outbound proxy.
var DefaultNoProxyEntries = []string{
	"localhost",
	"127.0.0.1",
	"::1",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
}

// SanitizeNoProxy constructs a robust NO_PROXY string combining existing entries,
// standard private network exclusions, and the host of reportURL (if provided).
func SanitizeNoProxy(existingNoProxy, reportURL string) string {
	seen := make(map[string]bool)
	var result []string

	add := func(entry string) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return
		}
		lower := strings.ToLower(entry)
		if !seen[lower] {
			seen[lower] = true
			result = append(result, entry)
		}
	}

	// 1. Add existing entries
	for _, part := range strings.Split(existingNoProxy, ",") {
		add(part)
	}

	// 2. Add standard private/local exclusions
	for _, d := range DefaultNoProxyEntries {
		add(d)
	}

	// 3. Add reportURL host if specified
	if reportURL != "" {
		if u, err := url.Parse(reportURL); err == nil && u.Hostname() != "" {
			add(u.Hostname())
		} else {
			// In case reportURL was raw host or host:port
			host, _, err := net.SplitHostPort(reportURL)
			if err == nil && host != "" {
				add(host)
			} else if reportURL != "" {
				add(reportURL)
			}
		}
	}

	return strings.Join(result, ",")
}

// SetupNetworkEnvironment normalizes NO_PROXY and no_proxy environment variables
// so local traffic and master reporting are never accidentally captured by corporate
// HTTP_PROXY/HTTPS_PROXY settings.
func SetupNetworkEnvironment(reportURL string) {
	existing := os.Getenv("NO_PROXY")
	if existing == "" {
		existing = os.Getenv("no_proxy")
	}

	sanitized := SanitizeNoProxy(existing, reportURL)
	_ = os.Setenv("NO_PROXY", sanitized)
	_ = os.Setenv("no_proxy", sanitized)

	httpProxy := os.Getenv("HTTP_PROXY")
	if httpProxy == "" {
		httpProxy = os.Getenv("http_proxy")
	}
	httpsProxy := os.Getenv("HTTPS_PROXY")
	if httpsProxy == "" {
		httpsProxy = os.Getenv("https_proxy")
	}
	bootstrap := GetBootstrapProxy()

	if httpProxy != "" || httpsProxy != "" || bootstrap != "" {
		logger.Info("Network environment initialized: HTTP_PROXY=%q HTTPS_PROXY=%q BOOTSTRAP_PROXY=%q NO_PROXY=%q",
			httpProxy, httpsProxy, bootstrap, sanitized)
	}
}

// GetBootstrapProxy returns the configured bootstrap/geo proxy URL, checking
// BOOTSTRAP_PROXY, bootstrap_proxy, GEO_PROXY, and geo_proxy.
func GetBootstrapProxy() string {
	for _, k := range []string{"BOOTSTRAP_PROXY", "bootstrap_proxy", "GEO_PROXY", "geo_proxy"} {
		if val := os.Getenv(k); val != "" {
			return val
		}
	}
	return ""
}

// GetBootstrapTransport returns an http.Transport configured with the bootstrap proxy
// if set, or falling back to http.ProxyFromEnvironment.
func GetBootstrapTransport() *http.Transport {
	proxyStr := GetBootstrapProxy()
	if proxyStr != "" {
		if u, err := url.Parse(proxyStr); err == nil {
			return &http.Transport{
				Proxy:                 http.ProxyURL(u),
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       90 * time.Second,
			}
		}
		logger.Warn("Failed to parse BOOTSTRAP_PROXY %q, falling back to environment proxy", proxyStr)
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
}
