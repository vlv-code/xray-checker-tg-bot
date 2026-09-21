package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"xray-checker/models"
	"xray-checker/subscription"
)

func TestResolveInitialConfigs_Success(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "xray.json")
	cachePath := filepath.Join(tempDir, "cache.json")

	called := 0
	sampleProxies := []*models.ProxyConfig{
		{Protocol: "vless", Server: "1.2.3.4", Port: 443, Name: "node1", UUID: "11111111-1111-1111-1111-111111111111", Index: 0},
	}

	fetcher := func() (*[]*models.ProxyConfig, error) {
		called++
		return &sampleProxies, nil
	}

	configs, isDegraded, err := resolveInitialConfigs(configFile, cachePath, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDegraded {
		t.Errorf("expected isDegraded = false, got true")
	}
	if len(*configs) != 1 {
		t.Errorf("expected 1 proxy, got %d", len(*configs))
	}
	if called != 1 {
		t.Errorf("expected 1 fetch call, got %d", called)
	}

	// Verify cache was saved
	cached, _, err := subscription.LoadProxyCache(cachePath)
	if err != nil {
		t.Fatalf("expected cache to be written, got: %v", err)
	}
	if len(cached) != 1 {
		t.Errorf("expected 1 cached proxy, got %d", len(cached))
	}
}

func TestResolveInitialConfigs_FallbackToCacheOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "xray.json")
	cachePath := filepath.Join(tempDir, "cache.json")

	// Pre-populate cache
	cachedProxies := []*models.ProxyConfig{
		{Protocol: "vless", Server: "cached.example.com", Port: 443, Name: "cached-node", UUID: "22222222-2222-2222-2222-222222222222", Index: 0},
	}
	if err := subscription.SaveProxyCache(cachePath, cachedProxies, "CachedSub"); err != nil {
		t.Fatal(err)
	}

	fetcher := func() (*[]*models.ProxyConfig, error) {
		return nil, errors.New("net/http: TLS handshake timeout")
	}

	configs, isDegraded, err := resolveInitialConfigs(configFile, cachePath, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDegraded {
		t.Errorf("expected isDegraded = true when using cache fallback")
	}
	if len(*configs) != 1 || (*configs)[0].Server != "cached.example.com" {
		t.Errorf("expected 1 cached proxy with server 'cached.example.com', got %+v", configs)
	}
}

func TestResolveInitialConfigs_DegradedZeroProxiesWhenNoCache(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "xray.json")
	cachePath := filepath.Join(tempDir, "non_existent_cache.json")

	fetcher := func() (*[]*models.ProxyConfig, error) {
		return nil, errors.New("connection refused")
	}

	configs, isDegraded, err := resolveInitialConfigs(configFile, cachePath, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDegraded {
		t.Errorf("expected isDegraded = true for 0 proxies degraded mode")
	}
	if configs == nil || len(*configs) != 0 {
		t.Errorf("expected 0 proxies in degraded mode, got %+v", configs)
	}

	// Verify valid Xray config was still generated on disk
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		t.Fatalf("expected fallback xray config file to be generated at %s", configFile)
	}
}
