package main

import (
	"fmt"
	"strconv"
	"strings"

	"xray-checker/checker"
	"xray-checker/config"
	"xray-checker/metrics"
	"xray-checker/nodes"
	"xray-checker/telegram"
)

// nodeManagerAdapter implements telegram.NodeManager over the master's
// nodes.Registry (live state), nodes.NodeSubsStore (desired state) and
// nodes.NodesStore (per-node settings overrides).
type nodeManagerAdapter struct {
	reg    *nodes.Registry
	subs   *nodes.NodeSubsStore
	store  *nodes.NodesStore
	botCfg func() telegram.BotConfig // runtime bot config; may be nil
}

func toNodeInfos(hs []nodes.NodeHealth) []telegram.NodeInfo {
	out := make([]telegram.NodeInfo, 0, len(hs))
	for _, h := range hs {
		out = append(out, telegram.NodeInfo{
			Name: h.Name, Up: h.Up, EverReported: h.EverReported,
			Version: h.Version, HostIP: h.HostIP, ASN: h.ASN,
			Online: h.Online, Total: h.Total, LastReport: h.LastReport,
			IntervalSec: h.CheckIntervalSec,
		})
	}
	return out
}

func (a *nodeManagerAdapter) Nodes() []telegram.NodeInfo {
	return toNodeInfos(a.reg.HealthSnapshot())
}

func (a *nodeManagerAdapter) ManagedSubs(node string) ([]telegram.ManagedSubInfo, error) {
	if !a.reg.NodeExists(node) {
		return nil, fmt.Errorf("неизвестная нода: %s", node)
	}
	counts := a.reg.SubCounts(node)
	urls := a.subs.Get(node)
	out := make([]telegram.ManagedSubInfo, 0, len(urls))

	totalFromCounts := 0
	for _, c := range counts {
		totalFromCounts += c
	}

	everReported := false
	totalProxies := totalFromCounts
	for _, h := range a.reg.HealthSnapshot() {
		if h.Name == node {
			everReported = h.EverReported
			if totalProxies == 0 && everReported {
				totalProxies = h.Total
			}
			break
		}
	}

	for _, u := range urls {
		count := -1
		frag := ""
		if idx := strings.LastIndex(u, "#"); idx != -1 {
			frag = u[idx+1:]
		}
		if c, ok := counts[u]; ok {
			count = c
		} else if frag != "" && counts[frag] > 0 {
			count = counts[frag]
		} else if len(urls) == 1 && everReported {
			count = totalProxies
		}
		out = append(out, telegram.ManagedSubInfo{URL: u, ProxyCount: count})
	}
	return out, nil
}

func (a *nodeManagerAdapter) AddSub(node, url string) error {
	if !a.reg.NodeExists(node) {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	added, err := a.subs.Add(node, url)
	if err != nil {
		return err
	}
	if !added {
		return fmt.Errorf("эта подписка уже назначена ноде")
	}
	return nil
}

func (a *nodeManagerAdapter) RemoveSub(node, url string) error {
	if !a.reg.NodeExists(node) {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	removed, err := a.subs.Remove(node, url)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("подписка не найдена среди назначенных ноде")
	}
	return nil
}

func (a *nodeManagerAdapter) AddNode(name, token string) error {
	return a.reg.RegisterNode(nodes.NodeConfig{Name: name, Token: token})
}

func (a *nodeManagerAdapter) RemoveNode(name string) error {
	return a.reg.RemoveNode(name)
}

func (a *nodeManagerAdapter) NodeSnapshot(node string) []metrics.ProxyMetric {
	return a.reg.NodeSnapshot(node)
}

func (a *nodeManagerAdapter) NodeDiagReports(node string) []checker.ProxyDiagReport {
	return a.reg.NodeDiagReports(node)
}

// mergedMetricsSource combines local master metrics with metrics reported by remote nodes.
type mergedMetricsSource struct {
	local metrics.MetricsSource
	reg   *nodes.Registry
}

func (m *mergedMetricsSource) MetricsSnapshot() []metrics.ProxyMetric {
	var local []metrics.ProxyMetric
	if m.local != nil {
		local = m.local.MetricsSnapshot()
	}
	if m.reg != nil {
		return m.reg.MergedSnapshot(local)
	}
	return local
}

// nodeSettingKey is one overridable check setting.
type nodeSettingKey struct {
	key      string
	override func(ns *nodes.NodeSettings) any // non-nil pointer getter for override detection
	apply    func(ns *nodes.NodeSettings, value string) error
}

var nodeSettingKeys = []nodeSettingKey{
	{key: "check_interval", override: func(ns *nodes.NodeSettings) any { return ns.CheckIntervalSec },
		apply: func(ns *nodes.NodeSettings, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("check_interval: нужно целое число секунд > 0")
			}
			ns.CheckIntervalSec = &n
			return nil
		}},
	{key: "check_method", override: func(ns *nodes.NodeSettings) any { return ns.CheckMethod },
		apply: func(ns *nodes.NodeSettings, v string) error {
			if v != "ip" && v != "status" && v != "download" {
				return fmt.Errorf("check_method: допустимо ip, status или download")
			}
			ns.CheckMethod = &v
			return nil
		}},
	{key: "ip_check_url", override: func(ns *nodes.NodeSettings) any { return ns.IpCheckURL },
		apply: func(ns *nodes.NodeSettings, v string) error {
			u, err := validHTTPURL(v)
			if err != nil {
				return err
			}
			ns.IpCheckURL = &u
			return nil
		}},
	{key: "status_check_url", override: func(ns *nodes.NodeSettings) any { return ns.StatusCheckURL },
		apply: func(ns *nodes.NodeSettings, v string) error {
			u, err := validHTTPURL(v)
			if err != nil {
				return err
			}
			ns.StatusCheckURL = &u
			return nil
		}},
	{key: "download_url", override: func(ns *nodes.NodeSettings) any { return ns.DownloadURL },
		apply: func(ns *nodes.NodeSettings, v string) error {
			u, err := validHTTPURL(v)
			if err != nil {
				return err
			}
			ns.DownloadURL = &u
			return nil
		}},
	{key: "proxy_timeout", override: func(ns *nodes.NodeSettings) any { return ns.ProxyTimeoutSec },
		apply: func(ns *nodes.NodeSettings, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("proxy_timeout: нужно целое число секунд > 0")
			}
			ns.ProxyTimeoutSec = &n
			return nil
		}},
	{key: "download_timeout", override: func(ns *nodes.NodeSettings) any { return ns.DownloadTimeoutSec },
		apply: func(ns *nodes.NodeSettings, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("download_timeout: нужно целое число секунд > 0")
			}
			ns.DownloadTimeoutSec = &n
			return nil
		}},
	{key: "download_min_size", override: func(ns *nodes.NodeSettings) any { return ns.DownloadMinSize },
		apply: func(ns *nodes.NodeSettings, v string) error {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return fmt.Errorf("download_min_size: нужно целое число байт ≥ 0")
			}
			ns.DownloadMinSize = &n
			return nil
		}},
	{key: "check_concurrency", override: func(ns *nodes.NodeSettings) any { return ns.CheckConcurrency },
		apply: func(ns *nodes.NodeSettings, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("check_concurrency: нужно целое число ≥ 0 (0 = без лимита)")
			}
			ns.CheckConcurrency = &n
			return nil
		}},
	{key: "subs_update_interval", override: func(ns *nodes.NodeSettings) any { return ns.SubsUpdateIntervalSec },
		apply: func(ns *nodes.NodeSettings, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("subs_update_interval: нужно целое число секунд > 0")
			}
			ns.SubsUpdateIntervalSec = &n
			return nil
		}},
	{key: "target_urls", override: func(ns *nodes.NodeSettings) any { return ns.TargetURLs },
		apply: func(ns *nodes.NodeSettings, v string) error {
			parts := strings.Split(v, ",")
			urls := make([]string, 0, len(parts))
			for _, p := range parts {
				u, err := validHTTPURL(p)
				if err != nil {
					return fmt.Errorf("target_urls: %v", err)
				}
				urls = append(urls, u)
			}
			ns.TargetURLs = urls
			return nil
		}},
}

func validHTTPURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" || (!strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://")) {
		return "", fmt.Errorf("URL должен начинаться с http:// или https://")
	}
	return u, nil
}

func findNodeSettingKey(key string) *nodeSettingKey {
	for i := range nodeSettingKeys {
		if nodeSettingKeys[i].key == key {
			return &nodeSettingKeys[i]
		}
	}
	return nil
}

// settingOverrideValue renders the override value of one key, if set. The
// getters return typed nils inside an any, so each case checks its pointer.
func settingOverrideValue(ns *nodes.NodeSettings, sk *nodeSettingKey) (string, bool) {
	v := sk.override(ns)
	switch typed := v.(type) {
	case *int:
		if typed == nil {
			return "", false
		}
		return strconv.Itoa(*typed), true
	case *string:
		if typed == nil {
			return "", false
		}
		return *typed, true
	case *int64:
		if typed == nil {
			return "", false
		}
		return strconv.FormatInt(*typed, 10), true
	case []string:
		if typed == nil {
			return "", false
		}
		return strings.Join(typed, ","), true
	}
	return "", false
}

// baseSettingValues returns the master's effective base values (the
// inheritance base for every node), mirroring the SetConfigSource logic.
func (a *nodeManagerAdapter) baseSettingValues() map[string]string {
	interval := config.CLIConfig.Proxy.CheckInterval
	targets := config.CLIConfig.Telegram.TargetURLs
	if a.botCfg != nil {
		if cfg := a.botCfg(); cfg.CheckIntervalSec > 0 {
			interval = cfg.CheckIntervalSec
		}
		if cfg := a.botCfg(); len(cfg.TargetURLs) > 0 {
			targets = cfg.TargetURLs
		}
	}
	return map[string]string{
		"check_interval":       strconv.Itoa(interval),
		"check_method":         config.CLIConfig.Proxy.CheckMethod,
		"ip_check_url":         config.CLIConfig.Proxy.IpCheckUrl,
		"status_check_url":     config.CLIConfig.Proxy.StatusCheckUrl,
		"download_url":         config.CLIConfig.Proxy.DownloadUrl,
		"proxy_timeout":        strconv.Itoa(config.CLIConfig.Proxy.Timeout),
		"download_timeout":     strconv.Itoa(config.CLIConfig.Proxy.DownloadTimeout),
		"download_min_size":    strconv.FormatInt(config.CLIConfig.Proxy.DownloadMinSize, 10),
		"check_concurrency":    strconv.Itoa(config.CLIConfig.Proxy.CheckConcurrency),
		"subs_update_interval": strconv.Itoa(config.CLIConfig.Subscription.UpdateInterval),
		"target_urls":          strings.Join(targets, ","),
	}
}

// NodeSettingsView implements telegram.NodeManager.
func (a *nodeManagerAdapter) NodeSettingsView(node string) ([]telegram.NodeSettingEntry, error) {
	if !a.reg.NodeExists(node) {
		return nil, fmt.Errorf("неизвестная нода: %s", node)
	}
	var ns *nodes.NodeSettings
	if a.store != nil {
		ns, _ = a.store.Settings(node)
	}
	base := a.baseSettingValues()
	out := make([]telegram.NodeSettingEntry, 0, len(nodeSettingKeys))
	for i := range nodeSettingKeys {
		sk := &nodeSettingKeys[i]
		entry := telegram.NodeSettingEntry{Key: sk.key, Value: base[sk.key]}
		if ns != nil {
			if v, ok := settingOverrideValue(ns, sk); ok {
				entry.Value = v
				entry.Overridden = true
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// SetNodeSetting implements telegram.NodeManager.
func (a *nodeManagerAdapter) SetNodeSetting(node, key, value string) error {
	if !a.reg.NodeExists(node) {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	if a.store == nil {
		return fmt.Errorf("хранилище настроек нод недоступно")
	}
	sk := findNodeSettingKey(key)
	if sk == nil {
		return fmt.Errorf("неизвестный ключ %q", key)
	}
	ns, _ := a.store.Settings(node)
	if ns == nil {
		ns = &nodes.NodeSettings{}
	}
	if err := sk.apply(ns, value); err != nil {
		return err
	}
	return a.store.SetSettings(node, ns)
}

// ResetNodeSetting implements telegram.NodeManager. An empty key clears all
// overrides; a named key clears exactly that field.
func (a *nodeManagerAdapter) ResetNodeSetting(node, key string) error {
	if !a.reg.NodeExists(node) {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	if a.store == nil {
		return fmt.Errorf("хранилище настроек нод недоступно")
	}
	if key == "" {
		return a.store.SetSettings(node, nil)
	}
	sk := findNodeSettingKey(key)
	if sk == nil {
		return fmt.Errorf("неизвестный ключ %q", key)
	}
	ns, _ := a.store.Settings(node)
	if ns == nil {
		return nil // nothing to reset
	}
	switch sk.key {
	case "check_interval":
		ns.CheckIntervalSec = nil
	case "check_method":
		ns.CheckMethod = nil
	case "ip_check_url":
		ns.IpCheckURL = nil
	case "status_check_url":
		ns.StatusCheckURL = nil
	case "download_url":
		ns.DownloadURL = nil
	case "proxy_timeout":
		ns.ProxyTimeoutSec = nil
	case "download_timeout":
		ns.DownloadTimeoutSec = nil
	case "download_min_size":
		ns.DownloadMinSize = nil
	case "check_concurrency":
		ns.CheckConcurrency = nil
	case "subs_update_interval":
		ns.SubsUpdateIntervalSec = nil
	case "target_urls":
		ns.TargetURLs = nil
	}
	return a.store.SetSettings(node, ns)
}
