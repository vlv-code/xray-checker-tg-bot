package subscription

import (
	"os"
	"path/filepath"
	"testing"

	"xray-checker/models"
)

func TestSaveAndLoadProxyCache(t *testing.T) {
	tempDir := t.TempDir()
	cachePath := filepath.Join(tempDir, "proxy_cache.json")

	// 1. Loading non-existent cache should fail
	_, _, err := LoadProxyCache(cachePath)
	if err == nil {
		t.Fatal("expected error when loading non-existent cache, got nil")
	}

	// 2. Save valid proxies
	proxies := []*models.ProxyConfig{
		{
			Protocol: "vless",
			Server:   "example.com",
			Port:     443,
			Name:     "Test Node",
			UUID:     "11111111-2222-3333-4444-555555555555",
			SubName:  "MySub",
			Index:    0,
		},
		{
			Protocol: "trojan",
			Server:   "trojan.example.com",
			Port:     8443,
			Name:     "Trojan Node",
			Password: "secretpassword",
			SubName:  "MySub",
			Index:    1,
		},
	}

	err = SaveProxyCache(cachePath, proxies, "MySub")
	if err != nil {
		t.Fatalf("SaveProxyCache failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		t.Fatalf("cache file was not created at %s", cachePath)
	}

	// 3. Load valid cache
	loaded, subName, err := LoadProxyCache(cachePath)
	if err != nil {
		t.Fatalf("LoadProxyCache failed: %v", err)
	}

	if subName != "MySub" {
		t.Errorf("expected subName 'MySub', got %q", subName)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 proxies, got %d", len(loaded))
	}
	if loaded[0].Server != "example.com" || loaded[0].Protocol != "vless" {
		t.Errorf("first proxy mismatch: %+v", loaded[0])
	}
	if loaded[1].Server != "trojan.example.com" || loaded[1].Password != "secretpassword" {
		t.Errorf("second proxy mismatch: %+v", loaded[1])
	}
}

func TestLoadEmptyOrCorruptProxyCache(t *testing.T) {
	tempDir := t.TempDir()
	cachePath := filepath.Join(tempDir, "empty_cache.json")

	// Empty file
	if err := os.WriteFile(cachePath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadProxyCache(cachePath)
	if err == nil {
		t.Fatal("expected error loading cache with 0 proxies, got nil")
	}

	// Corrupt JSON
	corruptPath := filepath.Join(tempDir, "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{broken json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = LoadProxyCache(corruptPath)
	if err == nil {
		t.Fatal("expected error loading corrupt cache, got nil")
	}
}

func TestSaveEmptyProxyCacheIsNoOp(t *testing.T) {
	tempDir := t.TempDir()
	cachePath := filepath.Join(tempDir, "noop_cache.json")

	err := SaveProxyCache(cachePath, nil, "sub")
	if err != nil {
		t.Fatalf("expected nil error for nil proxies, got: %v", err)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatal("expected file to not be created for empty proxies")
	}
}
