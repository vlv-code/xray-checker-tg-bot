package app

import (
	"net/http"

	"xray-checker/config"
	"xray-checker/logger"
	"xray-checker/web"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// buildServeMux creates the prefix mux and mounts every public/protected
// route (health, node report ingest, status page, API, metrics). The Fatal
// on mux creation stays at the Run() call site, exactly where the former
// main() had it.
func (a *App) buildServeMux() (*web.PrefixServeMux, error) {
	mux, err := web.NewPrefixServeMux(config.CLIConfig.Metrics.BasePath)
	if err != nil {
		return nil, err
	}
	mux.Handle("/health", web.HealthHandler())
	if a.NodeRegistry != nil {
		// Bearer-token auth of its own — deliberately not behind the metrics
		// basic auth, which nodes must not need to know.
		mux.Handle("/api/v1/nodes/report", a.NodeRegistry.HandleReport())
	}

	protectedHandler := http.NewServeMux()
	protectedHandler.Handle("/metrics", promhttp.HandlerFor(a.registry, promhttp.HandlerOpts{}))

	if config.CLIConfig.Web.Enabled {
		// The status page's assets and public API stay unauthenticated only in
		// --web-public mode. Otherwise they mount on the protected mux below:
		// /api/v1/public/proxies is an exact-match route that would otherwise
		// bypass the basic-auth wrapper mounted at "/" and leak the proxy list
		// of a private deployment.
		if config.CLIConfig.Web.Public {
			mux.Handle("/static/", web.StaticHandler())
			mux.Handle("/api/v1/public/proxies", web.APIPublicProxiesHandler(a.proxyChecker))
		} else {
			protectedHandler.Handle("/static/", web.StaticHandler())
			protectedHandler.Handle("/api/v1/public/proxies", web.APIPublicProxiesHandler(a.proxyChecker))
		}

		web.RegisterConfigEndpoints(*a.proxyConfigs, a.proxyChecker, config.CLIConfig.Xray.StartPort)
		if a.NodeRegistry != nil {
			protectedHandler.Handle("/api/v1/nodes", web.APINodesHandler(a.NodeRegistry))
		}

		protectedHandler.Handle("/config/", web.ConfigStatusHandler(a.proxyChecker))
		protectedHandler.Handle("/api/v1/proxies/", web.APIProxyHandler(a.proxyChecker, config.CLIConfig.Xray.StartPort))
		protectedHandler.Handle("/api/v1/proxies", web.APIProxiesHandler(a.proxyChecker, config.CLIConfig.Xray.StartPort))
		protectedHandler.Handle("/api/v1/config", web.APIConfigHandler(a.proxyChecker))
		protectedHandler.Handle("/api/v1/status", web.APIStatusHandler(a.proxyChecker))
		protectedHandler.Handle("/api/v1/system/info", web.APISystemInfoHandler(a.version, a.startTime))
		protectedHandler.Handle("/api/v1/system/ip", web.APISystemIPHandler(a.proxyChecker))
		protectedHandler.Handle("/api/v1/docs", web.APIDocsHandler())
		protectedHandler.Handle("/api/v1/openapi.yaml", web.APIOpenAPIHandler())

		if config.CLIConfig.Web.Public {
			mux.Handle("/", web.IndexHandler(a.version, a.proxyChecker))
			mux.Handle("/config/", web.ConfigStatusHandler(a.proxyChecker))
			middlewareHandler := web.BasicAuthMiddleware(
				config.CLIConfig.Metrics.Username,
				config.CLIConfig.Metrics.Password,
			)(protectedHandler)
			mux.Handle("/metrics", middlewareHandler)
			mux.Handle("/api/", middlewareHandler)
		} else if config.CLIConfig.Metrics.Protected {
			protectedHandler.Handle("/", web.IndexHandler(a.version, a.proxyChecker))
			middlewareHandler := web.BasicAuthMiddleware(
				config.CLIConfig.Metrics.Username,
				config.CLIConfig.Metrics.Password,
			)(protectedHandler)
			mux.Handle("/", middlewareHandler)
		} else {
			protectedHandler.Handle("/", web.IndexHandler(a.version, a.proxyChecker))
			mux.Handle("/", protectedHandler)
		}
	} else {
		logger.Info("Web dashboard panel is disabled (--web-enabled=false)")
		if config.CLIConfig.Metrics.Protected {
			middlewareHandler := web.BasicAuthMiddleware(
				config.CLIConfig.Metrics.Username,
				config.CLIConfig.Metrics.Password,
			)(protectedHandler)
			mux.Handle("/metrics", middlewareHandler)
		} else {
			mux.Handle("/metrics", protectedHandler)
		}
	}

	return mux, nil
}
