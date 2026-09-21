package subscription

import (
	"os"
	"path/filepath"
	"testing"

	"xray-checker/models"
)

func TestBuildValidatedConfigurationEmpty(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "empty_xray.json")

	emptyProxies := []*models.ProxyConfig{}
	configs, err := BuildValidatedConfiguration(configFile, emptyProxies)
	if err != nil {
		t.Fatalf("BuildValidatedConfiguration failed on empty proxies: %v", err)
	}

	if configs == nil || len(*configs) != 0 {
		t.Fatalf("expected empty configs, got: %v", configs)
	}

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		t.Fatalf("expected xray config file to be generated at %s", configFile)
	}
}

func TestBuildValidatedConfigurationWithProxies(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "valid_xray.json")

	proxies := []*models.ProxyConfig{
		{
			Protocol: "vless",
			Server:   "valid.example.com",
			Port:     443,
			Name:     "Valid Node",
			UUID:     "22222222-3333-4444-5555-666666666666",
			Index:    0,
		},
	}

	configs, err := BuildValidatedConfiguration(configFile, proxies)
	if err != nil {
		t.Fatalf("BuildValidatedConfiguration failed: %v", err)
	}

	if configs == nil || len(*configs) != 1 {
		t.Fatalf("expected 1 config, got: %v", configs)
	}

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		t.Fatalf("expected xray config file to be generated at %s", configFile)
	}
}
