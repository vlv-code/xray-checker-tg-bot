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

	proxyConfigs := configs

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
		return nil, nil, fmt.Errorf("no subscription URLs provided")
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

	for _, url := range urls {
		result := resultMap[url]
		if result.Error != nil {
			logger.Warn("Failed to fetch subscription %s: %v", result.URL, result.Error)
			errors = append(errors, fmt.Errorf("%s: %v", result.URL, result.Error))
			continue
		}
		logger.Debug("Fetched %d proxies from %s (name: %s)", len(result.Configs), result.URL, result.Name)
		counts[url] = len(result.Configs)
		allConfigs = append(allConfigs, result.Configs...)
		if firstName == "" && result.Name != "" {
			firstName = result.Name
		}
		successCount++
	}

	if successCount == 0 {
		return nil, nil, fmt.Errorf("failed to fetch any subscription: %v", errors)
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
