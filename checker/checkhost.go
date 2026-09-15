package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"
)

// CheckHostNodeResult holds individual node check results
type CheckHostNodeResult struct {
	Node    string        `json:"node"`
	Country string        `json:"country"`
	City    string        `json:"city"`
	IP      string        `json:"ip"`
	Success bool          `json:"success"`
	Latency time.Duration `json:"latency"`
	Error   string        `json:"error"`
}

// CheckHostSummary contains the aggregate check result for a host
type CheckHostSummary struct {
	Host           string                `json:"host"`
	RequestID      string                `json:"request_id"`
	PermanentLink  string                `json:"permanent_link"`
	Results        []CheckHostNodeResult `json:"results"`
	RUAvailable    bool                  `json:"ru_available"`
	WorldAvailable bool                  `json:"world_available"`
	Verdict        string                `json:"verdict"`
}

// RUStats returns the total, successful, and average latency for RU nodes.
func (s *CheckHostSummary) RUStats() (total int, success int, avgLatency time.Duration) {
	if s == nil {
		return 0, 0, 0
	}
	var latSum time.Duration
	for _, r := range s.Results {
		if strings.EqualFold(r.Country, "ru") {
			total++
			if r.Success {
				success++
				latSum += r.Latency
			}
		}
	}
	if success > 0 {
		avgLatency = latSum / time.Duration(success)
	}
	return
}

// WorldStats returns the total, successful, and average latency for non-RU nodes.
func (s *CheckHostSummary) WorldStats() (total int, success int, avgLatency time.Duration) {
	if s == nil {
		return 0, 0, 0
	}
	var latSum time.Duration
	for _, r := range s.Results {
		if !strings.EqualFold(r.Country, "ru") {
			total++
			if r.Success {
				success++
				latSum += r.Latency
			}
		}
	}
	if success > 0 {
		avgLatency = latSum / time.Duration(success)
	}
	return
}

var (
	// DefaultFastRUNodes are the Russian nodes used for quick diagnostics
	DefaultFastRUNodes = []string{"ru2.node.check-host.net", "ru3.node.check-host.net"}

	// DefaultFastWorldNodes are the representative global nodes
	DefaultFastWorldNodes = []string{
		"de1.node.check-host.net",
		"nl1.node.check-host.net",
		"fi1.node.check-host.net",
		"pl2.node.check-host.net",
		"us1.node.check-host.net",
	}

	// DefaultWorldwideNodes are selected for full multi-region audits
	DefaultWorldwideNodes = []string{
		// Russia
		"ru2.node.check-host.net", "ru3.node.check-host.net",
		// Europe
		"de1.node.check-host.net", "nl1.node.check-host.net", "fi1.node.check-host.net",
		"pl2.node.check-host.net", "uk1.node.check-host.net", "fr2.node.check-host.net",
		"ch1.node.check-host.net", "es1.node.check-host.net",
		// Americas
		"us1.node.check-host.net", "us5.node.check-host.net", "ca1.node.check-host.net", "br1.node.check-host.net",
		// Asia & Middle East
		"jp1.node.check-host.net", "sg1.node.check-host.net", "hk1.node.check-host.net",
		"kz1.node.check-host.net", "tr1.node.check-host.net",
	}
)

// CheckHostClient handles communication with check-host.net API
type CheckHostClient struct {
	BaseURL      string
	HTTPClient   *http.Client
	PollInterval time.Duration
}

// NewCheckHostClient creates a new CheckHostClient
func NewCheckHostClient(baseURL string, pollInterval time.Duration) *CheckHostClient {
	if baseURL == "" {
		baseURL = "https://check-host.net"
	}
	if pollInterval <= 0 {
		pollInterval = 1500 * time.Millisecond
	}
	jar, _ := cookiejar.New(nil)
	return &CheckHostClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
			Jar:     jar,
		},
		PollInterval: pollInterval,
	}
}

type checkInitResponse struct {
	Ok            int                    `json:"ok"`
	RequestID     string                 `json:"request_id"`
	PermanentLink string                 `json:"permanent_link"`
	Nodes         map[string]interface{} `json:"nodes"`
	Error         string                 `json:"error"`
}

// CheckTCP runs a TCP connection check on host (host:port) using specified nodes
func (c *CheckHostClient) CheckTCP(ctx context.Context, host string, nodes []string) (*CheckHostSummary, error) {
	return c.checkInternal(ctx, "check-tcp", host, nodes)
}

// CheckPing runs an ICMP ping check on host (domain or IP) using specified nodes
func (c *CheckHostClient) CheckPing(ctx context.Context, host string, nodes []string) (*CheckHostSummary, error) {
	return c.checkInternal(ctx, "check-ping", host, nodes)
}

func (c *CheckHostClient) checkInternal(ctx context.Context, checkType, host string, nodes []string) (*CheckHostSummary, error) {
	if len(nodes) == 0 {
		nodes = append(DefaultFastRUNodes, DefaultFastWorldNodes...)
	}

	// 1. Send check request
	q := url.Values{}
	q.Set("host", host)
	for _, n := range nodes {
		q.Add("node", n)
	}

	reqURL := fmt.Sprintf("%s/%s?%s", c.BaseURL, checkType, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send check request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("check-host init returned status %d", resp.StatusCode)
	}

	var initResp checkInitResponse
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil {
		return nil, fmt.Errorf("failed to parse init response: %w", err)
	}
	if initResp.Ok != 1 || initResp.RequestID == "" {
		return nil, fmt.Errorf("check-host failed: %s", initResp.Error)
	}

	// Node metadata from initResp.Nodes: map[nodeID] -> [country, countryName, city, ip, asn]
	nodeMeta := make(map[string]CheckHostNodeResult)
	for nID, raw := range initResp.Nodes {
		var res CheckHostNodeResult
		res.Node = nID
		if arr, ok := raw.([]interface{}); ok {
			if len(arr) > 0 {
				if c, ok := arr[0].(string); ok {
					res.Country = strings.ToLower(c)
				}
			}
			if len(arr) > 2 {
				if city, ok := arr[2].(string); ok {
					res.City = city
				}
			}
			if len(arr) > 3 {
				if ip, ok := arr[3].(string); ok {
					res.IP = ip
				}
			}
		}
		nodeMeta[nID] = res
	}

	// 2. Poll for results
	resultURL := fmt.Sprintf("%s/check-result/%s", c.BaseURL, initResp.RequestID)
	ticker := time.NewTicker(c.PollInterval)
	defer ticker.Stop()

	var finalResults map[string]interface{}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			resReq, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL, nil)
			if err != nil {
				return nil, err
			}
			resReq.Header.Set("Accept", "application/json")
			resReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

			resResp, err := c.HTTPClient.Do(resReq)
			if err != nil {
				continue
			}

			var resultMap map[string]interface{}
			err = json.NewDecoder(resResp.Body).Decode(&resultMap)
			resResp.Body.Close()
			if err != nil {
				continue
			}

			// Check if all requested nodes have finished (non-nil results)
			allFinished := true
			for _, nID := range nodes {
				val, exists := resultMap[nID]
				if !exists || val == nil {
					allFinished = false
					break
				}
			}

			finalResults = resultMap
			if allFinished {
				goto DonePolling
			}
		}
	}

DonePolling:
	resultsList := make([]CheckHostNodeResult, 0, len(nodeMeta))
	ruCount, ruSuccess := 0, 0
	worldCount, worldSuccess := 0, 0

	for nID, meta := range nodeMeta {
		rawVal := finalResults[nID]
		if rawVal != nil {
			if arr, ok := rawVal.([]interface{}); ok && len(arr) > 0 {
				// 1. TCP result format: [{"time": float, "address": "..."}] or [{"error": "..."}]
				if item, ok := arr[0].(map[string]interface{}); ok {
					if t, ok := item["time"].(float64); ok {
						meta.Success = true
						meta.Latency = time.Duration(t * float64(time.Second))
					} else if errMsg, ok := item["error"].(string); ok {
						meta.Success = false
						meta.Error = errMsg
					}
				} else if attempts, ok := arr[0].([]interface{}); ok {
					// 2. Ping result format: [[["OK", float, "ip"], ["OK", float], ...]]
					for _, attRaw := range attempts {
						if att, ok := attRaw.([]interface{}); ok && len(att) >= 2 {
							if status, ok := att[0].(string); ok && status == "OK" {
								meta.Success = true
								if t, ok := att[1].(float64); ok {
									meta.Latency = time.Duration(t * float64(time.Second))
								}
								break
							}
						}
					}
				}
			}
		} else {
			meta.Success = false
			meta.Error = "No response / pending"
		}

		if meta.Country == "ru" {
			ruCount++
			if meta.Success {
				ruSuccess++
			}
		} else {
			worldCount++
			if meta.Success {
				worldSuccess++
			}
		}

		resultsList = append(resultsList, meta)
	}

	// Sort results: RU first, then alphabetically by country and city
	sort.Slice(resultsList, func(i, j int) bool {
		if resultsList[i].Country == "ru" && resultsList[j].Country != "ru" {
			return true
		}
		if resultsList[i].Country != "ru" && resultsList[j].Country == "ru" {
			return false
		}
		if resultsList[i].Country != resultsList[j].Country {
			return resultsList[i].Country < resultsList[j].Country
		}
		return resultsList[i].City < resultsList[j].City
	})

	ruAvailable := ruSuccess > 0
	worldAvailable := worldSuccess > 0

	summary := &CheckHostSummary{
		Host:           host,
		RequestID:      initResp.RequestID,
		PermanentLink:  initResp.PermanentLink,
		Results:        resultsList,
		RUAvailable:    ruAvailable,
		WorldAvailable: worldAvailable,
		Verdict:        EvaluateCheckHostVerdict(ruAvailable, worldAvailable),
	}

	return summary, nil
}

// EvaluateCheckHostVerdict returns neutral, objective availability verdict
func EvaluateCheckHostVerdict(ruAvailable, worldAvailable bool) string {
	switch {
	case !ruAvailable && worldAvailable:
		return "Хост недоступен из узлов РФ, но отвечает из зарубежных сетей (Европа/США)"
	case !ruAvailable && !worldAvailable:
		return "Хост недоступен как из РФ, так и из других стран"
	case ruAvailable && worldAvailable:
		return "Хост доступен из всех проверяемых сетей"
	default:
		return "Хост доступен из РФ, но недоступен из части внешних сетей"
	}
}

// FormatCheckHostReport formats full Check-Host results into clean Telegram markdown
func FormatCheckHostReport(summary *CheckHostSummary) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🌐 <b>Результаты Check-Host для</b> <code>%s</code> <b>(TCP):</b>\n\n", html.EscapeString(summary.Host)))

	var ruLines, euLines, amLines, asLines []string

	for _, res := range summary.Results {
		icon := "✅"
		statusText := fmt.Sprintf("%.0f ms", float64(res.Latency.Milliseconds()))
		if !res.Success {
			icon = "❌"
			statusText = res.Error
			if statusText == "" {
				statusText = "Timeout"
			}
		}

		line := fmt.Sprintf("  • %s (%s): %s %s", res.City, strings.ToUpper(res.Country), icon, statusText)

		switch res.Country {
		case "ru":
			ruLines = append(ruLines, line)
		case "de", "nl", "fi", "pl", "uk", "fr", "ch", "es", "at", "bg", "it", "ro", "se":
			euLines = append(euLines, line)
		case "us", "ca", "br":
			amLines = append(amLines, line)
		default:
			asLines = append(asLines, line)
		}
	}

	if len(ruLines) > 0 {
		sb.WriteString("🇷🇺 <b>Россия:</b>\n")
		sb.WriteString(strings.Join(ruLines, "\n"))
		sb.WriteString("\n\n")
	}

	if len(euLines) > 0 {
		sb.WriteString("🇪🇺 <b>Европа:</b>\n")
		sb.WriteString(strings.Join(euLines, "\n"))
		sb.WriteString("\n\n")
	}

	if len(amLines) > 0 {
		sb.WriteString("🇺🇸 <b>Америка:</b>\n")
		sb.WriteString(strings.Join(amLines, "\n"))
		sb.WriteString("\n\n")
	}

	if len(asLines) > 0 {
		sb.WriteString("🌏 <b>Азия / Другие:</b>\n")
		sb.WriteString(strings.Join(asLines, "\n"))
		sb.WriteString("\n\n")
	}

	if summary.Verdict != "" {
		sb.WriteString(fmt.Sprintf("💡 <b>Вывод:</b> <i>%s</i>\n", html.EscapeString(summary.Verdict)))
	}

	if summary.PermanentLink != "" {
		sb.WriteString(fmt.Sprintf("🔗 <a href=\"%s\">Постоянная ссылка на отчёт</a>\n", summary.PermanentLink))
	}

	return sb.String()
}
