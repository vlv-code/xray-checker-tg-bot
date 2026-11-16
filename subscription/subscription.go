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

var (
	subscriptionName string
	subNameMu        sync.RWMutex
)

// lastGoodConfigs caches the parsed proxy configs of the last SUCCESSFUL
// fetch per subscription source, so a single hiccuping source can be merged
// back into the result instead of silently shrinking the proxy set (which
// would take every proxy of the failing panel offline and fire false outage
// alerts until the next cycle). A removed source loses its entry.
var (
	lastGoodMu      sync.Mutex
	lastGoodConfigs = make(map[string][]*models.ProxyConfig)
)

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

// rememberLastGoodLocked stores a deep copy of the configs (cloneProxyConfig)
// so the cache never aliases structs that downstream code mutates.
// Caller must hold lastGoodMu.
func rememberLastGoodLocked(url string, configs []*models.ProxyConfig) {
	lastGoodConfigs[url] = cloneProxyConfigs(configs)
}

// evictStaleLastGoodLocked drops cache entries whose source is no longer in
// the requested set, so a removed subscription cannot resurrect its proxies
// from the cache forever. Caller must hold lastGoodMu.
func evictStaleLastGoodLocked(urls []string) {
	requested := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		requested[u] = struct{}{}
	}
	for u := range lastGoodConfigs {
		if _, ok := requested[u]; !ok {
			delete(lastGoodConfigs, u)
		}
	}
}

func GetSubscriptionName() string {
	subNameMu.RLock()
	defer subNameMu.RUnlock()
	return subscriptionName
}

func SetSubscriptionName(name string) {
	subNameMu.Lock()
	defer subNameMu.Unlock()
	subscriptionName = name
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
	configs, _, err := ReadFromMultipleSourcesDetailed(urls)
	return configs, err
}

func ReadFromMultipleSourcesDetailed(urls []string) ([]*models.ProxyConfig, map[string]int, error) {
	if len(urls) == 0 {
		return []*models.ProxyConfig{}, make(map[string]int), nil
	}

	counts := make(map[string]int)
	if len(urls) == 1 {
		configs, name, err := ReadFromSource(urls[0])
		if err != nil {
			return nil, nil, err
		}
		for _, cfg := range configs {
			cfg.SubName = name
		}
		if name != "" {
			SetSubscriptionName(name)
		}
		counts[urls[0]] = len(configs)
		lastGoodMu.Lock()
		rememberLastGoodLocked(urls[0], configs)
		evictStaleLastGoodLocked(urls)
		lastGoodMu.Unlock()
		return configs, counts, nil
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
	lastGoodMu.Lock()
	defer lastGoodMu.Unlock()

	for _, url := range urls {
		result := resultMap[url]
		if result.Error != nil {
			logger.Warn("Failed to fetch subscription %s: %v", RedactURL(result.URL), result.Error)
			errors = append(errors, fmt.Errorf("%s: %v", RedactURL(result.URL), result.Error))
			// Keep the last successfully fetched set for this source: a
			// single hiccuping panel must not lose all of its proxies
			// (false outage alerts) until it recovers.
			if cached, ok := lastGoodConfigs[url]; ok && len(cached) > 0 {
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
		rememberLastGoodLocked(url, result.Configs)
		counts[url] = len(result.Configs)
		allConfigs = append(allConfigs, result.Configs...)
		if firstName == "" && result.Name != "" {
			firstName = result.Name
		}
		successCount++
	}

	// Sources no longer requested lose their cache entry (done once per run).
	evictStaleLastGoodLocked(urls)

	if successCount == 0 {
		if len(allConfigs) == 0 {
			return nil, nil, fmt.Errorf("failed to fetch any subscription: %v", errors)
		}
		// Total failure: callers keyed on the error keep the currently
		// applied set (main.go returns early on err), but the merged
		// last-known-good configs are still returned instead of nil.
		for i := range allConfigs {
			allConfigs[i].Index = i
		}
		return allConfigs, counts, fmt.Errorf("failed to fetch any subscription: %v", errors)
	}

	if firstName != "" {
		SetSubscriptionName(firstName)
	}

	for i := range allConfigs {
		allConfigs[i].Index = i
	}

	logger.Debug("Total: %d proxies from %d/%d subscriptions", len(allConfigs), successCount, len(urls))
	return allConfigs, counts, nil
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
