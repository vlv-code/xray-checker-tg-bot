package web

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"xray-checker/checker"
	"xray-checker/config"
	"xray-checker/logger"
	"xray-checker/metrics"
	"xray-checker/models"
	"xray-checker/subscription"
)

var (
	registeredEndpoints []EndpointInfo
	endpointsMu         sync.RWMutex
	lastEndpointsUpdate time.Time
	endpointsCacheTTL   = 3 * time.Second
)

type EndpointInfo struct {
	Name       string
	Protocol   string
	ServerInfo string
	URL        string
	ProxyPort  int
	Index      int
	Status     bool
	Latency    time.Duration
	StableID   string
	GroupName  string
}

func IndexHandler(version string, proxyChecker *checker.ProxyChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		RegisterConfigEndpoints(proxyChecker.GetProxies(), proxyChecker, config.CLIConfig.Xray.StartPort)

		endpointsMu.RLock()
		allEndpoints := make([]EndpointInfo, len(registeredEndpoints))
		copy(allEndpoints, registeredEndpoints)
		endpointsMu.RUnlock()

		isPublic := config.CLIConfig.Web.Public
		showServerDetails := shouldShowServerDetails()

		endpoints := allEndpoints
		if isPublic {
			// In public mode the per-proxy config URL (copy link) is never
			// exposed. Server address/port are exposed only when details are
			// allowed, e.g. via WEB_TRUSTED_EXTERNAL_AUTH behind an external
			// auth proxy.
			endpoints = make([]EndpointInfo, len(allEndpoints))
			for i, ep := range allEndpoints {
				e := EndpointInfo{
					Name:      ep.Name,
					Index:     ep.Index,
					Status:    ep.Status,
					Latency:   ep.Latency,
					StableID:  ep.StableID,
					GroupName: ep.GroupName,
				}
				if config.CLIConfig.Web.PublicShowProtocol {
					e.Protocol = ep.Protocol
				}
				if showServerDetails {
					e.ServerInfo = ep.ServerInfo
					e.ProxyPort = ep.ProxyPort
				}
				endpoints[i] = e
			}
		}

		data := PageData{
			Version:                    version,
			Host:                       config.CLIConfig.Metrics.Host,
			Port:                       config.CLIConfig.Metrics.Port,
			CheckInterval:              config.CLIConfig.Proxy.CheckInterval,
			IPCheckUrl:                 config.CLIConfig.Proxy.IpCheckUrl,
			CheckMethod:                config.CLIConfig.Proxy.CheckMethod,
			StatusCheckUrl:             config.CLIConfig.Proxy.StatusCheckUrl,
			DownloadUrl:                config.CLIConfig.Proxy.DownloadUrl,
			SimulateLatency:            config.CLIConfig.Proxy.SimulateLatency,
			Timeout:                    config.CLIConfig.Proxy.Timeout,
			SubscriptionUpdate:         config.CLIConfig.Subscription.Update,
			SubscriptionUpdateInterval: config.CLIConfig.Subscription.UpdateInterval,
			StartPort:                  config.CLIConfig.Xray.StartPort,
			Instance:                   config.CLIConfig.Metrics.Instance,
			PushUrl:                    metrics.GetPushURL(config.CLIConfig.Metrics.PushURL),
			Endpoints:                  endpoints,
			ShowServerDetails:          showServerDetails,
			IsPublic:                   isPublic,
			SubscriptionName:           subscription.GetSubscriptionName(),
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		if err := RenderIndex(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

func HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}
}

// SecurityHeadersMiddleware adds defensive HTTP security headers to all responses.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

type AuthLimiter struct {
	mu           sync.Mutex
	maxFailures  int
	lockDuration time.Duration
	window       time.Duration
	failures     map[string]*failureRecord
}

type failureRecord struct {
	count       int
	firstFail   time.Time
	lockedUntil time.Time
}

func NewAuthLimiter(maxFailures int, lockDuration time.Duration, window time.Duration) *AuthLimiter {
	return &AuthLimiter{
		maxFailures:  maxFailures,
		lockDuration: lockDuration,
		window:       window,
		failures:     make(map[string]*failureRecord),
	}
}

var (
	authLimiterMu     sync.RWMutex
	globalAuthLimiter = NewAuthLimiter(5, 30*time.Second, 1*time.Minute)
)

func SetAuthLimiter(l *AuthLimiter) {
	authLimiterMu.Lock()
	globalAuthLimiter = l
	authLimiterMu.Unlock()
}

func getAuthLimiter() *AuthLimiter {
	authLimiterMu.RLock()
	defer authLimiterMu.RUnlock()
	return globalAuthLimiter
}

func (l *AuthLimiter) isLocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.failures[ip]
	if !ok {
		return false
	}
	return time.Now().Before(rec.lockedUntil)
}

func (l *AuthLimiter) recordFailure(ip string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()

	if len(l.failures) > 1000 {
		for k, v := range l.failures {
			if now.Sub(v.firstFail) > 10*time.Minute && now.After(v.lockedUntil) {
				delete(l.failures, k)
			}
		}
	}

	rec, ok := l.failures[ip]
	if !ok || now.Sub(rec.firstFail) > l.window {
		rec = &failureRecord{
			count:     1,
			firstFail: now,
		}
		l.failures[ip] = rec
	} else {
		rec.count++
	}

	if rec.count >= l.maxFailures {
		rec.lockedUntil = now.Add(l.lockDuration)
		return true, rec.count
	}
	return false, rec.count
}

func (l *AuthLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	delete(l.failures, ip)
	l.mu.Unlock()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func BasicAuthMiddleware(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limiter := getAuthLimiter()
			ip := clientIP(r)

			if limiter.isLocked(ip) {
				w.Header().Set("Retry-After", "30")
				http.Error(w, "Too many failed authentication attempts. Please try again later.", http.StatusTooManyRequests)
				return
			}

			user, pass, ok := r.BasicAuth()
			userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(username)) == 1
			passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1
			if !ok || !userMatch || !passMatch {
				locked, attempts := limiter.recordFailure(ip)
				logUser := user
				if logUser == "" {
					logUser = "<anonymous>"
				}
				if locked {
					logger.Warn("Basic Auth: IP %s locked out for %v after %d failed attempts (last user: %q)", ip, limiter.lockDuration, attempts, logUser)
				} else {
					logger.Warn("Basic Auth failed for user %q from %s (attempt %d/%d)", logUser, ip, attempts, limiter.maxFailures)
				}
				w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
				http.Error(w, "Unauthorized.", http.StatusUnauthorized)
				return
			}

			limiter.recordSuccess(ip)
			next.ServeHTTP(w, r)
		})
	}
}

// maxSimulatedLatency caps the artificial delay added by SIMULATE_LATENCY so
// badge requests can't hold a goroutine for a full check timeout.
const maxSimulatedLatency = 2 * time.Second

func ConfigStatusHandler(proxyChecker *checker.ProxyChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[len("/config/"):]
		if path == "" {
			http.Error(w, "Config path is required", http.StatusBadRequest)
			return
		}

		if _, exists := proxyChecker.GetProxyByStableID(path); !exists {
			http.Error(w, "Config not found", http.StatusNotFound)
			return
		}

		status, latency, _, ok := proxyChecker.GetProxyResultByStableID(path)
		if !ok {
			http.Error(w, "Status not available", http.StatusNotFound)
			return
		}

		if config.CLIConfig.Proxy.SimulateLatency {
			// Cap the simulated delay: real latency can approach the full
			// check timeout (up to a minute with download checks), and this
			// endpoint is unauthenticated in public mode — unbounded sleeps
			// let concurrent requests pin goroutines and memory.
			sleep := latency
			if sleep > maxSimulatedLatency || sleep < 0 {
				sleep = maxSimulatedLatency
			}
			time.Sleep(sleep)
		}

		if status {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Failed"))
		}
	}
}

func RegisterConfigEndpoints(proxies []*models.ProxyConfig, proxyChecker *checker.ProxyChecker, startPort int) {
	endpointsMu.RLock()
	if time.Since(lastEndpointsUpdate) < endpointsCacheTTL && len(registeredEndpoints) > 0 {
		endpointsMu.RUnlock()
		return
	}
	endpointsMu.RUnlock()

	endpoints := make([]EndpointInfo, 0, len(proxies))

	// StableID is assumed assigned: the checker fills it in NewProxyChecker /
	// UpdateProxies (the only writers of the proxy set), so mutating shared
	// configs from this request path would be a data race.
	for _, proxy := range proxies {
		endpoint := fmt.Sprintf("./config/%s", proxy.StableID)

		status, latency, _, _ := proxyChecker.GetProxyResultByStableID(proxy.StableID)

		endpoints = append(endpoints, EndpointInfo{
			Name:       proxy.Name,
			Protocol:   proxy.Protocol,
			ServerInfo: fmt.Sprintf("%s:%d", proxy.Server, proxy.Port),
			URL:        endpoint,
			ProxyPort:  startPort + proxy.Index,
			Index:      proxy.Index,
			Status:     status,
			Latency:    latency,
			StableID:   proxy.StableID,
			GroupName:  proxy.GroupName,
		})
	}

	endpointsMu.Lock()
	registeredEndpoints = endpoints
	lastEndpointsUpdate = time.Now()
	endpointsMu.Unlock()
}

type PrefixServeMux struct {
	prefix string
	mux    *http.ServeMux
}

func NewPrefixServeMux(prefix string) (*PrefixServeMux, error) {
	if strings.HasSuffix(prefix, "/") {
		return nil, fmt.Errorf("served url path prefix '%s' should not ends with a '/'", prefix)
	}
	return &PrefixServeMux{
		prefix: prefix,
		mux:    http.NewServeMux(),
	}, nil
}

func (pm *PrefixServeMux) Handle(pattern string, handler http.Handler) {
	pm.mux.Handle(pm.prefix+pattern, http.StripPrefix(pm.prefix, handler))
}

func (pm *PrefixServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == pm.prefix || strings.HasPrefix(r.URL.Path, pm.prefix+"/") {
		pm.mux.ServeHTTP(w, r)
	} else {
		http.NotFound(w, r)
	}
}
