package subscription

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"xray-checker/config"
	"xray-checker/logger"
	"xray-checker/models"
	"xray-checker/xray"
)

// All package-level mutable state of this reader (last-known-good cache,
// subscription name) lives in the sourceState type — see source_state.go.
// The package-level funcs below delegate to defaultSourceState.

// cloneProxyConfig returns a deep copy of cfg (slice and map fields
// duplicated), so cached configs never alias the live set: downstream
// PrepareProxyConfigs/AssignStableIDs write Index/StableID on the pointed-to
// structs unconditionally (Guardian P1, 2026-11-16).
func cloneProxyConfig(cfg *models.ProxyConfig) *models.ProxyConfig {
	c := *cfg
	if cfg.ALPN != nil {
		c.ALPN = append([]string(nil), cfg.ALPN...)
	}
	if cfg.WGAddresses != nil {
		c.WGAddresses = append([]string(nil), cfg.WGAddresses...)
	}
	if cfg.WGAllowedIPs != nil {
		c.WGAllowedIPs = append([]string(nil), cfg.WGAllowedIPs...)
	}
	if cfg.WGDNS != nil {
		c.WGDNS = append([]string(nil), cfg.WGDNS...)
	}
	if cfg.Settings != nil {
		c.Settings = make(map[string]string, len(cfg.Settings))
		for k, v := range cfg.Settings {
			c.Settings[k] = v
		}
	}
	if cfg.MetricsLabels != nil {
		c.MetricsLabels = make(map[string]string, len(cfg.MetricsLabels))
		for k, v := range cfg.MetricsLabels {
			c.MetricsLabels[k] = v
		}
	}
	return &c
}

// cloneProxyConfigs deep-copies every element (see cloneProxyConfig).
func cloneProxyConfigs(cs []*models.ProxyConfig) []*models.ProxyConfig {
	out := make([]*models.ProxyConfig, len(cs))
	for i, c := range cs {
		out[i] = cloneProxyConfig(c)
	}
	return out
}

func GetSubscriptionName() string {
	return defaultSourceState.subscriptionName()
}

func SetSubscriptionName(name string) {
	defaultSourceState.setSubscriptionName(name)
}

type subscriptionResult struct {
	URL     string
	Name    string
	Configs []*models.ProxyConfig
	Error   error
}

func InitializeConfiguration(configFile string, version string, urls []string) (*[]*models.ProxyConfig, error) {
	configs, err := ReadFromMultipleSources(urls)
	if err != nil {
		return nil, err
	}
	return BuildValidatedConfiguration(configFile, configs)
}

// BuildValidatedConfiguration prepares, validates, and generates xray configuration for the given proxy list.
func BuildValidatedConfiguration(configFile string, configs []*models.ProxyConfig) (*[]*models.ProxyConfig, error) {
	proxyConfigs := configs
	var err error

	if config.CLIConfig.Proxy.ResolveDomains {
		proxyConfigs, err = ResolveDomainsForConfigs(configs)
		if err != nil {
			return nil, err
		}
	}

	xray.PrepareProxyConfigs(proxyConfigs)

	configGenerator := xray.NewConfigGenerator()
	validProxies, err := configGenerator.GenerateValidatedConfig(
		proxyConfigs,
		config.CLIConfig.Xray.StartPort,
		configFile,
		config.CLIConfig.Xray.LogLevel,
	)
	if err != nil {
		return nil, err
	}
	proxyConfigs = validProxies

	return &proxyConfigs, nil
}

func ReadFromMultipleSources(urls []string) ([]*models.ProxyConfig, error) {
	configs, _, _, err := ReadFromMultipleSourcesDetailed(urls)
	return configs, err
}

// ReadFromMultipleSourcesDetailed fetches every source and merges the results.
// counts maps each source that contributed proxies to the merged set to its
// proxy count — successfully refreshed sources and failed sources rescued
// from the last-known-good cache alike (the merged proxies are real).
// failed marks the sources whose fetch errored THIS cycle (cached fallbacks
// included), so callers can keep freshness telemetry honest: a cached
// fallback is not a fresh update (Wave N-G FIX 2). failed is non-nil and
// empty when every source refreshed successfully.
func ReadFromMultipleSourcesDetailed(urls []string) ([]*models.ProxyConfig, map[string]int, map[string]bool, error) {
	if len(urls) == 0 {
		return []*models.ProxyConfig{}, make(map[string]int), make(map[string]bool), nil
	}

	counts := make(map[string]int)
	failed := make(map[string]bool)
	if len(urls) == 1 {
		configs, name, err := ReadFromSource(urls[0])
		if err != nil {
			return nil, nil, nil, err
		}
		for _, cfg := range configs {
			cfg.SubName = name
		}
		if name != "" {
			SetSubscriptionName(name)
		}
		counts[urls[0]] = len(configs)
		defaultSourceState.storeAndEvictStale(urls[0], configs, urls)
		return configs, counts, failed, nil
	}

	logger.Debug("Fetching %d subscriptions in parallel", len(urls))

	resultMap := make(map[string]subscriptionResult)
	var resultMu sync.Mutex

	var wg sync.WaitGroup
	for _, url := range urls {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			configs, name, err := ReadFromSource(u)
			for _, cfg := range configs {
				cfg.SubName = name
			}
			resultMu.Lock()
			resultMap[u] = subscriptionResult{
				URL:     u,
				Name:    name,
				Configs: configs,
				Error:   err,
			}
			resultMu.Unlock()
		}(url)
	}

	wg.Wait()

	var allConfigs []*models.ProxyConfig
	var errors []error
	var firstName string
	successCount := 0

	// Guard the whole cache read-modify-write (merge + eviction) so
	// concurrent reloads cannot interleave updates.
	defaultSourceState.lock()
	defer defaultSourceState.unlock()

	for _, url := range urls {
		result := resultMap[url]
		if result.Error != nil {
			logger.Warn("Failed to fetch subscription %s: %v", RedactURL(result.URL), result.Error)
			errors = append(errors, fmt.Errorf("%s: %v", RedactURL(result.URL), result.Error))
			failed[url] = true
			// Keep the last successfully fetched set for this source: a
			// single hiccuping panel must not lose all of its proxies
			// (false outage alerts) until it recovers.
			if cached, ok := defaultSourceState.getLocked(url); ok && len(cached) > 0 {
				logger.Warn("Subscription %s failed, keeping %d last-known-good proxies: %v", RedactURL(url), len(cached), result.Error)
				counts[url] = len(cached)
				// Deep-copy on merge too: the merged set flows into the live
				// checker where PrepareProxyConfigs mutates Index/StableID —
				// the cache must stay pristine for the next cycle
				// (Guardian P1, 2026-11-16).
				allConfigs = append(allConfigs, cloneProxyConfigs(cached)...)
			}
			continue
		}
		logger.Debug("Fetched %d proxies from %s (name: %s)", len(result.Configs), RedactURL(result.URL), result.Name)
		defaultSourceState.storeLocked(url, result.Configs)
		counts[url] = len(result.Configs)
		allConfigs = append(allConfigs, result.Configs...)
		if firstName == "" && result.Name != "" {
			firstName = result.Name
		}
		successCount++
	}

	// Sources no longer requested lose their cache entry (done once per run).
	defaultSourceState.evictStaleLocked(urls)

	if successCount == 0 {
		if len(allConfigs) == 0 {
			return nil, nil, nil, fmt.Errorf("failed to fetch any subscription: %v", errors)
		}
		// Total failure: callers keyed on the error keep the currently
		// applied set (main.go returns early on err), but the merged
		// last-known-good configs are still returned instead of nil.
		for i := range allConfigs {
			allConfigs[i].Index = i
		}
		return allConfigs, counts, failed, fmt.Errorf("failed to fetch any subscription: %v", errors)
	}

	if firstName != "" {
		SetSubscriptionName(firstName)
	}

	for i := range allConfigs {
		allConfigs[i].Index = i
	}

	logger.Debug("Total: %d proxies from %d/%d subscriptions", len(allConfigs), successCount, len(urls))
	return allConfigs, counts, failed, nil
}

func ReadFromSource(source string) ([]*models.ProxyConfig, string, error) {
	parser := NewParser()
	result, err := parser.Parse(source)
	if err != nil {
		return nil, "", err
	}
	return result.Configs, result.Name, nil
}

func ResolveDomainsForConfigs(configs []*models.ProxyConfig) ([]*models.ProxyConfig, error) {
	var out []*models.ProxyConfig
	for _, cfg := range configs {
		if ip := net.ParseIP(cfg.Server); ip != nil {
			out = append(out, cfg)
			continue
		}

		ips, err := net.LookupIP(cfg.Server)
		if err != nil || len(ips) == 0 {
			logger.Warn("Failed to resolve domain %s: %v", cfg.Server, err)
			out = append(out, cfg)
			continue
		}

		type resolvedConfig struct {
			config   *models.ProxyConfig
			stableID string
		}
		resolved := make([]resolvedConfig, 0, len(ips))

		for _, ip := range ips {
			clone := *cfg
			clone.Server = ip.String()
			clone.StableID = clone.GenerateStableID()
			resolved = append(resolved, resolvedConfig{
				config:   &clone,
				stableID: clone.StableID,
			})
		}

		sort.Slice(resolved, func(i, j int) bool {
			return resolved[i].stableID < resolved[j].stableID
		})

		for i, item := range resolved {
			if len(ips) > 1 {
				item.config.Name = fmt.Sprintf("%s #%d", cfg.Name, i+1)
			}
			out = append(out, item.config)
		}
	}
	return out, nil
}
