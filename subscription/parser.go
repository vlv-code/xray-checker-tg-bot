package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xray-checker/logger"
	"xray-checker/models"

	libXray "github.com/xtls/libxray"
)

type Parser struct{}

type ParseResult struct {
	Configs []*models.ProxyConfig
	Name    string
}

func NewParser() *Parser {
	return &Parser{}
}

func (p *Parser) Parse(subscriptionData string) (*ParseResult, error) {
	sourceType := p.detectSourceType(subscriptionData)
	logger.Debug("Detected source type: %s", sourceType)

	var rawData []byte
	var subName string
	var err error

	switch sourceType {
	case "url":
		result, fetchErr := p.fetchURLContent(subscriptionData)
		if fetchErr != nil {
			return nil, fmt.Errorf("failed to fetch URL content: %v", fetchErr)
		}
		rawData = result.Content
		subName = result.Name
	case "folder":
		folderPath := strings.TrimPrefix(subscriptionData, "folder://")
		configs, folderErr := p.parseFolder(folderPath)
		if folderErr != nil {
			return nil, folderErr
		}
		return &ParseResult{Configs: configs, Name: ""}, nil
	case "file":
		filePath := strings.TrimPrefix(subscriptionData, "file://")
		rawData, err = os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read file: %v", err)
		}
	case "base64":
		rawData = []byte(strings.TrimPrefix(subscriptionData, "base64://"))
		rawData = []byte(strings.TrimPrefix(string(rawData), "data:text/plain;base64,"))
	default:
		rawData = []byte(subscriptionData)
	}

	trimmedData := strings.TrimSpace(string(rawData))

	// A common panel format is a base64-encoded JSON array. The JSON prefix
	// only becomes visible after decoding, so re-check for it once
	// cleanEmptyLines has run the base64 pass (below, on the share-link path).
	// Keep the original bytes for share-link parsing so a body that is BOTH
	// valid base64 and valid share-links (e.g. the literal "vmess://" lines)
	// still takes the link path.
	if !strings.HasPrefix(trimmedData, "[") && !strings.HasPrefix(trimmedData, "{") {
		if decoded := p.tryDecodeBase64(rawData); len(decoded) > 0 && string(decoded) != string(rawData) {
			decodedTrimmed := strings.TrimSpace(string(decoded))
			if strings.HasPrefix(decodedTrimmed, "[") || strings.HasPrefix(decodedTrimmed, "{") {
				logger.Debug("Detected base64-encoded JSON subscription")
				rawData = decoded
				trimmedData = decodedTrimmed
			}
		}
	}

	if strings.HasPrefix(trimmedData, "[") {
		logger.Debug("Detected JSON array format")
		configs, jsonErr := p.parseJSONConfigs(rawData)
		if jsonErr != nil {
			return nil, jsonErr
		}
		return &ParseResult{Configs: configs, Name: subName}, nil
	}

	if strings.HasPrefix(trimmedData, "{") {
		logger.Debug("Detected single JSON object format")
		configs, jsonErr := p.parseSingleJSONConfig(rawData)
		if jsonErr != nil {
			return nil, jsonErr
		}
		return &ParseResult{Configs: configs, Name: subName}, nil
	}

	originalData := p.parseOriginalLinks(rawData)

	cleanedData := p.cleanEmptyLines(rawData)

	// Pull out socks/http/https forward-proxy URIs first: libXray cannot parse
	// them, so we handle them directly and pass the rest to the normal path.
	directConfigs, remaining := p.extractDirectProxyLines(cleanedData)
	if len(directConfigs) > 0 {
		logger.Info("Parsed %d direct proxy line(s) (socks/http/wireguard)", len(directConfigs))
	}

	var proxyConfigs []*models.ProxyConfig
	if len(strings.TrimSpace(string(remaining))) > 0 {
		// Try batch parsing first
		proxyConfigs = p.parseViaLibXray(remaining, originalData)

		// If batch parsing failed, fall back to line-by-line parsing
		if len(proxyConfigs) == 0 {
			logger.Warn("Batch parsing failed or returned no configs, trying line-by-line parsing")
			proxyConfigs = p.parseLineByLine(remaining, originalData)
		}
	}

	proxyConfigs = append(proxyConfigs, directConfigs...)

	if len(proxyConfigs) == 0 {
		return nil, fmt.Errorf("no valid proxy configurations found")
	}

	// Re-index after merging direct + libXray-parsed configs.
	for i, cfg := range proxyConfigs {
		cfg.Index = i
	}

	return &ParseResult{Configs: proxyConfigs, Name: subName}, nil
}

// extractDirectProxyLines pulls socks/http/https forward-proxy URIs out of the
// raw subscription content (which libXray cannot parse) and returns the parsed
// configs together with the remaining lines for the normal libXray path.
func (p *Parser) extractDirectProxyLines(cleanedData []byte) ([]*models.ProxyConfig, []byte) {
	lines := strings.Split(string(cleanedData), "\n")
	var configs []*models.ProxyConfig
	remaining := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if pc := parseWireGuardURI(trimmed); pc != nil {
				configs = append(configs, pc)
				continue
			}
			if pc := parseProxyURI(trimmed); pc != nil {
				configs = append(configs, pc)
				continue
			}
		}
		remaining = append(remaining, line)
	}

	return configs, []byte(strings.Join(remaining, "\n"))
}

// parseViaLibXray attempts to parse all configs at once via libXray.
// Returns parsed configs or nil if parsing fails.
func (p *Parser) parseViaLibXray(cleanedData []byte, originalData map[string]*originalLinkData) []*models.ProxyConfig {
	base64Data := base64.StdEncoding.EncodeToString(cleanedData)

	resultBase64 := libXray.ConvertShareLinksToXrayJson(base64Data)

	resultBytes, err := base64.StdEncoding.DecodeString(resultBase64)
	if err != nil {
		logger.Debug("Failed to decode libXray response: %v", err)
		return nil
	}

	var response libXrayResponse
	if err := json.Unmarshal(resultBytes, &response); err != nil {
		logger.Debug("Failed to parse libXray response: %v", err)
		return nil
	}

	if !response.Success {
		logger.Debug("libXray batch parsing returned success=false")
		return nil
	}

	return p.extractOutbounds(response.Data, originalData)
}

// parseLineByLine parses each config line individually, skipping broken ones.
func (p *Parser) parseLineByLine(cleanedData []byte, originalData map[string]*originalLinkData) []*models.ProxyConfig {
	lines := strings.Split(string(cleanedData), "\n")
	var allConfigs []*models.ProxyConfig
	skippedCount := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		lineBase64 := base64.StdEncoding.EncodeToString([]byte(line))
		resultBase64 := libXray.ConvertShareLinksToXrayJson(lineBase64)

		resultBytes, err := base64.StdEncoding.DecodeString(resultBase64)
		if err != nil {
			logger.Warn("Skipping invalid config line (decode error): %.50s...", line)
			skippedCount++
			continue
		}

		var response libXrayResponse
		if err := json.Unmarshal(resultBytes, &response); err != nil {
			logger.Warn("Skipping invalid config line (parse error): %.50s...", line)
			skippedCount++
			continue
		}

		if !response.Success {
			logger.Warn("Skipping invalid config line (libXray error): %.50s...", line)
			skippedCount++
			continue
		}

		configs := p.extractOutbounds(response.Data, originalData)
		allConfigs = append(allConfigs, configs...)
	}

	if skippedCount > 0 {
		logger.Warn("Skipped %d invalid config line(s) during parsing", skippedCount)
	}

	// Re-index configs
	for i, cfg := range allConfigs {
		cfg.Index = i
	}

	return allConfigs
}

func (p *Parser) parseJSONConfigs(data []byte) ([]*models.ProxyConfig, error) {
	var configs []struct {
		Remarks   string            `json:"remarks"`
		Outbounds []json.RawMessage `json:"outbounds"`
	}

	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, fmt.Errorf("failed to parse JSON configs: %v", err)
	}

	logger.Debug("Parsed %d JSON configs", len(configs))

	var proxyConfigs []*models.ProxyConfig
	configIndex := 0

	for _, config := range configs {
		var group []*models.ProxyConfig
		for _, outboundRaw := range config.Outbounds {
			proxyConfig, err := p.convertOutbound(outboundRaw, configIndex, nil)
			if err != nil || proxyConfig == nil {
				continue
			}
			group = append(group, proxyConfig)
			configIndex++
		}
		nameGroupedProxies(config.Remarks, group)
		proxyConfigs = append(proxyConfigs, group...)
	}

	if len(proxyConfigs) == 0 {
		return nil, fmt.Errorf("no valid proxy configurations found in JSON")
	}

	return proxyConfigs, nil
}

func (p *Parser) parseSingleJSONConfig(data []byte) ([]*models.ProxyConfig, error) {
	var config struct {
		Remarks   string            `json:"remarks"`
		Outbounds []json.RawMessage `json:"outbounds"`
	}

	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse single JSON config: %v", err)
	}

	logger.Debug("Parsed single JSON config with %d outbounds", len(config.Outbounds))

	var proxyConfigs []*models.ProxyConfig
	configIndex := 0

	for _, outboundRaw := range config.Outbounds {
		proxyConfig, err := p.convertOutbound(outboundRaw, configIndex, nil)
		if err != nil || proxyConfig == nil {
			continue
		}
		proxyConfigs = append(proxyConfigs, proxyConfig)
		configIndex++
	}

	nameGroupedProxies(config.Remarks, proxyConfigs)

	if len(proxyConfigs) == 0 {
		return nil, fmt.Errorf("no valid proxy configurations found in single JSON config")
	}

	return proxyConfigs, nil
}

func (p *Parser) cleanEmptyLines(data []byte) []byte {
	decoded := p.tryDecodeBase64(data)

	lines := strings.Split(string(decoded), "\n")
	var cleanLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			cleanLines = append(cleanLines, line)
		}
	}

	return []byte(strings.Join(cleanLines, "\n"))
}

func (p *Parser) detectSourceType(source string) string {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return "url"
	}
	if strings.HasPrefix(source, "folder://") {
		return "folder"
	}
	if strings.HasPrefix(source, "file://") {
		return "file"
	}
	if strings.HasPrefix(source, "base64://") || strings.HasPrefix(source, "data:text/plain;base64,") {
		return "base64"
	}
	return "raw"
}

func (p *Parser) tryDecodeBase64(data []byte) []byte {
	text := strings.TrimSpace(string(data))

	if strings.HasPrefix(text, "vless://") || strings.HasPrefix(text, "vmess://") ||
		strings.HasPrefix(text, "trojan://") || strings.HasPrefix(text, "ss://") ||
		strings.HasPrefix(text, "hysteria2://") || strings.HasPrefix(text, "hy2://") ||
		strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return data
	}

	decoded, err := p.decodeBase64(text)
	if err != nil {
		return data
	}

	return decoded
}

func (p *Parser) decodeBase64(text string) ([]byte, error) {
	text = strings.ReplaceAll(text, "-", "+")
	text = strings.ReplaceAll(text, "_", "/")

	if m := len(text) % 4; m != 0 {
		text += strings.Repeat("=", 4-m)
	}

	return base64.StdEncoding.DecodeString(text)
}

func (p *Parser) parseFolder(folderPath string) ([]*models.ProxyConfig, error) {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read folder: %v", err)
	}

	var allConfigs []*models.ProxyConfig
	configIndex := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		fileName := entry.Name()
		ext := strings.ToLower(filepath.Ext(fileName))
		if ext != ".json" {
			continue
		}

		filePath := filepath.Join(folderPath, fileName)
		data, err := os.ReadFile(filePath)
		if err != nil {
			logger.Warn("Failed to read file %s: %v", fileName, err)
			continue
		}

		configs, err := p.parseSingleConfigFile(data, configIndex)
		if err != nil {
			logger.Warn("Failed to parse file %s: %v", fileName, err)
			continue
		}

		for _, cfg := range configs {
			cfg.Index = configIndex
			allConfigs = append(allConfigs, cfg)
			configIndex++
		}

		logger.Debug("Parsed %d configs from %s", len(configs), fileName)
	}

	if len(allConfigs) == 0 {
		return nil, fmt.Errorf("no valid proxy configurations found in folder")
	}

	logger.Debug("Total configs from folder: %d", len(allConfigs))
	return allConfigs, nil
}

func (p *Parser) parseSingleConfigFile(data []byte, startIndex int) ([]*models.ProxyConfig, error) {
	trimmedData := strings.TrimSpace(string(data))

	if strings.HasPrefix(trimmedData, "[") {
		return p.parseJSONConfigs(data)
	}

	if strings.HasPrefix(trimmedData, "{") {
		var config struct {
			Remarks   string            `json:"remarks"`
			Outbounds []json.RawMessage `json:"outbounds"`
		}

		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("failed to parse JSON config: %v", err)
		}

		var proxyConfigs []*models.ProxyConfig
		for _, outboundRaw := range config.Outbounds {
			proxyConfig, err := p.convertOutbound(outboundRaw, startIndex, nil)
			if err != nil || proxyConfig == nil {
				continue
			}
			proxyConfigs = append(proxyConfigs, proxyConfig)
		}

		nameGroupedProxies(config.Remarks, proxyConfigs)

		if len(proxyConfigs) == 0 {
			return nil, fmt.Errorf("no valid proxy configurations found")
		}

		return proxyConfigs, nil
	}

	return nil, fmt.Errorf("unsupported config format")
}
