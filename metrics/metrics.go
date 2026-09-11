package metrics

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"xray-checker/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

type RemoteWriteConfig struct {
	URL      string
	Username string
	Password string
	Timeout  time.Duration
}

// baseLabels are the fixed Prometheus labels every proxy series carries. Custom
// metricsLabels (#124) are appended after these and may not override them.
var baseLabels = []string{"protocol", "address", "name", "sub_name", "stable_id", "group_name"}

// ProxyMetric is a point-in-time view of one proxy, rendered into metrics at
// scrape time by Collector. Building metrics from current state (a pull model)
// instead of pushing into a fixed-label vector lets custom label keys appear and
// disappear across subscription updates without resetting any other series to 0.
type ProxyMetric struct {
	Protocol     string
	Address      string
	Name         string
	SubName      string
	StableID     string
	GroupName    string
	CustomLabels map[string]string
	Online       bool
	LatencyMs    float64
	Disabled     bool
}

// MetricsSource supplies the current proxy snapshot to the Collector. The checker
// implements it; the interface keeps the metrics package free of a checker import.
type MetricsSource interface {
	MetricsSnapshot() []ProxyMetric
}

// Collector renders xray_proxy_status / xray_proxy_latency_ms from the current
// proxy snapshot on every scrape. It is an *unchecked* collector (Describe is a
// no-op) so the registry permits series whose label-name sets differ across the
// same metric family — required because each proxy may carry different custom
// metricsLabels.
type Collector struct {
	instance    string
	hasInstance bool
	source      MetricsSource
}

func NewCollector(instance string, source MetricsSource) *Collector {
	return &Collector{
		instance:    instance,
		hasInstance: instance != "",
		source:      source,
	}
}

func (c *Collector) Describe(chan<- *prometheus.Desc) {}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	if c.source == nil {
		return
	}
	for _, pm := range c.source.MetricsSnapshot() {
		names, values := c.labelsFor(pm)
		statusDesc := prometheus.NewDesc("xray_proxy_status",
			"Status of proxy connection (1: success, 0: failure)", names, nil)
		latencyDesc := prometheus.NewDesc("xray_proxy_latency_ms",
			"Latency of proxy connection in milliseconds, 0 if failed", names, nil)

		status := 0.0
		if pm.Online {
			status = 1.0
		}
		ch <- prometheus.MustNewConstMetric(statusDesc, prometheus.GaugeValue, status, values...)
		ch <- prometheus.MustNewConstMetric(latencyDesc, prometheus.GaugeValue, pm.LatencyMs, values...)
	}
}

// labelsFor builds aligned label-name/value slices: the fixed base labels, the
// optional instance label, then sanitized custom labels in deterministic (sorted)
// order. Custom keys are skipped when they are empty, use the Prometheus-reserved
// "__" prefix, or collide with an already-used name (a built-in label or another
// custom key) — otherwise NewDesc would build an invalid descriptor and
// MustNewConstMetric would panic on the next scrape.
func (c *Collector) labelsFor(pm ProxyMetric) ([]string, []string) {
	names := make([]string, len(baseLabels), len(baseLabels)+1+len(pm.CustomLabels))
	copy(names, baseLabels)
	values := []string{pm.Protocol, pm.Address, pm.Name, pm.SubName, pm.StableID, pm.GroupName}
	if c.hasInstance {
		names = append(names, "instance")
		values = append(values, c.instance)
	}

	if len(pm.CustomLabels) > 0 {
		used := make(map[string]bool, len(names))
		for _, n := range names {
			used[n] = true
		}
		keys := make([]string, 0, len(pm.CustomLabels))
		for k := range pm.CustomLabels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name := sanitizeLabelName(k)
			// "__"-prefixed names are reserved by Prometheus and would make the Desc
			// invalid (panicking MustNewConstMetric), so drop them along with empty
			// and already-used names.
			if name == "" || isReservedLabelName(name) || used[name] {
				continue
			}
			used[name] = true
			names = append(names, name)
			values = append(values, pm.CustomLabels[k])
		}
	}
	return names, values
}

// isReservedLabelName reports whether a label name uses the Prometheus-reserved
// "__" prefix, which is not allowed for user-defined labels.
func isReservedLabelName(name string) bool {
	return len(name) >= 2 && name[0] == '_' && name[1] == '_'
}

// sanitizeLabelName coerces a custom key to a valid Prometheus label name
// ([a-zA-Z_][a-zA-Z0-9_]*): invalid characters become "_" and a leading digit is
// prefixed with "_". Returns "" if nothing usable remains.
func sanitizeLabelName(k string) string {
	if k == "" {
		return ""
	}
	b := make([]byte, 0, len(k))
	for i := 0; i < len(k); i++ {
		ch := k[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch == '_':
			b = append(b, ch)
		case ch >= '0' && ch <= '9':
			if len(b) == 0 {
				b = append(b, '_')
			}
			b = append(b, ch)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}

func ParseURL(remoteWriteURL string) (*RemoteWriteConfig, error) {
	if remoteWriteURL == "" {
		return nil, nil
	}

	u, err := url.Parse(remoteWriteURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %v", err)
	}

	config := &RemoteWriteConfig{
		Timeout: 10 * time.Second,
	}

	if u.User != nil {
		config.Username = u.User.Username()
		if password, ok := u.User.Password(); ok {
			config.Password = password
		}
		u.User = nil
	}

	config.URL = u.String()
	return config, nil
}

func PushMetrics(config *RemoteWriteConfig, registry *prometheus.Registry) error {
	if config == nil {
		return fmt.Errorf("config is nil")
	}

	metricFamilies, err := registry.Gather()
	if err != nil {
		return fmt.Errorf("failed to gather metrics: %v", err)
	}

	var buf bytes.Buffer
	encoder := expfmt.NewEncoder(&buf, expfmt.FmtText)

	for _, mf := range metricFamilies {
		if err := encoder.Encode(mf); err != nil {
			return fmt.Errorf("failed to encode metrics: %v", err)
		}
	}

	client := &http.Client{
		Timeout: config.Timeout,
	}

	req, err := http.NewRequest("POST", config.URL, &buf)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	if config.Username != "" && config.Password != "" {
		req.SetBasicAuth(config.Username, config.Password)
	}

	req.Header.Set("Content-Type", "text/plain; version=0.0.4")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}
	logger.Debug("Metrics pushed to %s", config.URL)

	return nil
}

func GetPushURL(url string) string {
	if url == "" {
		return ""
	}

	cfg, err := ParseURL(url)
	if err != nil || cfg == nil {
		return ""
	}

	return cfg.URL
}
