package web

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"xray-checker/checker"
	"xray-checker/config"
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

func BasicAuthMiddleware(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(username)) == 1
			passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1
			if !ok || !userMatch || !passMatch {
				w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
				http.Error(w, "Unauthorized.", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

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
			time.Sleep(time.Duration(latency))
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
