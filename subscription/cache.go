package subscription

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"xray-checker/models"
)

type ProxyCacheData struct {
	Timestamp time.Time             `json:"timestamp"`
	SubName   string                `json:"sub_name,omitempty"`
	Proxies   []*models.ProxyConfig `json:"proxies"`
}

// SaveProxyCache writes the parsed proxy configurations and subName to path atomically.
func SaveProxyCache(path string, proxies []*models.ProxyConfig, subName string) error {
	if path == "" || len(proxies) == 0 {
		return nil
	}

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory for proxy cache: %w", err)
		}
	}

	data := ProxyCacheData{
		Timestamp: time.Now(),
		SubName:   subName,
		Proxies:   proxies,
	}

	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling proxy cache: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0600); err != nil {
		return fmt.Errorf("writing proxy cache temp file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming proxy cache: %w", err)
	}

	return nil
}

// LoadProxyCache loads cached proxy configurations and subscription name from path.
func LoadProxyCache(path string) ([]*models.ProxyConfig, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("cache path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	var cache ProxyCacheData
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, "", fmt.Errorf("unmarshaling proxy cache: %w", err)
	}

	if len(cache.Proxies) == 0 {
		return nil, "", fmt.Errorf("proxy cache is empty")
	}

	return cache.Proxies, cache.SubName, nil
}
