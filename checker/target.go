package checker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"xray-checker/models"
)

// TargetDiagResult holds the check outcome for one target endpoint.
type TargetDiagResult struct {
	URL        string        `json:"url"`
	Success    bool          `json:"success"`
	StatusCode int           `json:"status_code"`
	Latency    time.Duration `json:"latency"`
	Error      string        `json:"error,omitempty"`
}

// ProxyDiagReport holds diagnostic results across all target endpoints for one proxy.
type ProxyDiagReport struct {
	ProxyName string             `json:"proxy_name"`
	StableID  string             `json:"stable_id"`
	Targets   []TargetDiagResult `json:"targets"`
}

// TargetManager manages the list of target endpoints to test proxies against.
type TargetManager struct {
	mu      sync.RWMutex
	targets []string
}

// NewTargetManager creates a new TargetManager with initial targets.
func NewTargetManager(initialTargets []string) *TargetManager {
	tm := &TargetManager{
		targets: make([]string, 0),
	}
	for _, t := range initialTargets {
		_ = tm.AddTarget(t)
	}
	if len(tm.targets) == 0 {
		tm.targets = []string{
			"https://cp.cloudflare.com/generate_204",
			"https://www.gstatic.com/generate_204",
		}
	}
	return tm
}

// GetTargets returns a copy of current target URLs.
func (tm *TargetManager) GetTargets() []string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	out := make([]string, len(tm.targets))
	copy(out, tm.targets)
	return out
}

// AddTarget adds a validated target URL to the list.
func (tm *TargetManager) AddTarget(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("target URL cannot be empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid URL: must be http or https with valid host")
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	for _, existing := range tm.targets {
		if strings.EqualFold(existing, rawURL) {
			return fmt.Errorf("target URL already exists")
		}
	}

	tm.targets = append(tm.targets, rawURL)
	return nil
}

// RemoveTarget removes a target URL.
func (tm *TargetManager) RemoveTarget(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	tm.mu.Lock()
	defer tm.mu.Unlock()

	idx := -1
	for i, existing := range tm.targets {
		if strings.EqualFold(existing, rawURL) {
			idx = i
			break
		}
	}

	if idx == -1 {
		return fmt.Errorf("target URL not found")
	}

	tm.targets = append(tm.targets[:idx], tm.targets[idx+1:]...)
	return nil
}

// CheckSingleTarget tests an endpoint via the provided http.Client.
func CheckSingleTarget(client *http.Client, targetURL string) TargetDiagResult {
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return TargetDiagResult{
			URL:     targetURL,
			Success: false,
			Error:   err.Error(),
		}
	}

	var ttfb time.Duration
	start := time.Now()
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() {
			ttfb = time.Since(start)
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(context.Background(), trace))

	resp, err := client.Do(req)
	if err != nil {
		return TargetDiagResult{
			URL:     targetURL,
			Success: false,
			Latency: time.Since(start),
			Error:   simplifyError(err),
		}
	}
	defer resp.Body.Close()

	if ttfb == 0 {
		ttfb = time.Since(start)
	}

	success := resp.StatusCode >= 200 && resp.StatusCode < 400
	var errStr string
	if !success {
		errStr = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	return TargetDiagResult{
		URL:        targetURL,
		Success:    success,
		StatusCode: resp.StatusCode,
		Latency:    ttfb,
		Error:      errStr,
	}
}

func simplifyError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "context deadline exceeded") || strings.Contains(s, "Client.Timeout"):
		return "Timeout"
	case strings.Contains(s, "EOF"):
		return "EOF / Connection reset"
	case strings.Contains(s, "connection refused"):
		return "Connection refused"
	case strings.Contains(s, "no such host"):
		return "DNS resolution error"
	default:
		return s
	}
}

// RunDiagnostics executes concurrent tests against all given targets for each proxy.
func (pc *ProxyChecker) RunDiagnostics(targets []string) []ProxyDiagReport {
	pc.mu.RLock()
	proxies := make([]*models.ProxyConfig, len(pc.proxies))
	copy(proxies, pc.proxies)
	pc.mu.RUnlock()

	if len(targets) == 0 {
		targets = []string{
			"https://cp.cloudflare.com/generate_204",
			"https://www.gstatic.com/generate_204",
		}
	}

	results := make([]ProxyDiagReport, len(proxies))
	var wg sync.WaitGroup

	for i, p := range proxies {
		wg.Add(1)
		go func(idx int, proxy *models.ProxyConfig) {
			defer wg.Done()

			proxyURL := fmt.Sprintf("socks5://127.0.0.1:%d", pc.startPort+proxy.Index)
			proxyURLParsed, err := url.Parse(proxyURL)
			if err != nil {
				results[idx] = ProxyDiagReport{
					ProxyName: proxy.Name,
					StableID:  proxy.StableID,
					Targets: []TargetDiagResult{{
						URL:     "local socks5",
						Success: false,
						Error:   err.Error(),
					}},
				}
				return
			}

			client := &http.Client{
				Transport: &http.Transport{
					Proxy:             http.ProxyURL(proxyURLParsed),
					DisableKeepAlives: true,
				},
				Timeout: time.Second * 10,
			}

			targetResults := make([]TargetDiagResult, len(targets))
			var innerWg sync.WaitGroup
			for tIdx, targetURL := range targets {
				innerWg.Add(1)
				go func(resIdx int, tURL string) {
					defer innerWg.Done()
					targetResults[resIdx] = CheckSingleTarget(client, tURL)
				}(tIdx, targetURL)
			}
			innerWg.Wait()

			results[idx] = ProxyDiagReport{
				ProxyName: proxy.Name,
				StableID:  proxy.StableID,
				Targets:   targetResults,
			}
		}(i, p)
	}

	wg.Wait()
	return results
}
