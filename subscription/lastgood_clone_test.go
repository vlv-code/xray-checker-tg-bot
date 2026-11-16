package subscription

import (
	"testing"

	"xray-checker/models"
)

// Regression (Guardian P1, 2026-11-16): the last-known-good cache shared
// *ProxyConfig pointers with the live set; downstream PrepareProxyConfigs /
// AssignStableIDs write Index/StableID on those structs unconditionally, so a
// cached set handed out again on a partial-failure merge aliased mutable live
// state (and carried stale positional mutations across cycles).
func TestLastGoodCacheStoreDoesNotAliasOriginalConfigs(t *testing.T) {
	cfg := &models.ProxyConfig{
		Protocol: "vless", Server: "a.example", Port: 443, Name: "a", UUID: "u",
		ALPN:          []string{"h2"},
		Settings:      map[string]string{"k": "v"},
		MetricsLabels: map[string]string{"m": "1"},
		WGAddresses:   []string{"10.0.0.1/32"},
		WGAllowedIPs:  []string{"0.0.0.0/0"},
		WGDNS:         []string{"1.1.1.1"},
	}
	rememberLastGoodLocked("src", []*models.ProxyConfig{cfg})

	// Downstream mutation of the ORIGINAL object must not reach the cache.
	cfg.Index = 42
	cfg.StableID = "hacked"
	cfg.SubName = "hacked"
	cfg.ALPN[0] = "mutated"
	cfg.Settings["k"] = "mutated"
	cfg.MetricsLabels["m"] = "mutated"
	cfg.WGAddresses[0] = "mutated"
	cfg.WGAllowedIPs[0] = "mutated"
	cfg.WGDNS[0] = "mutated"

	cached, ok := lastGoodConfigs["src"]
	if !ok || len(cached) != 1 {
		t.Fatalf("expected 1 cached config, got %d (ok=%v)", len(cached), ok)
	}
	if cached[0].Index == 42 || cached[0].StableID == "hacked" || cached[0].SubName == "hacked" {
		t.Fatalf("cached config aliases original struct: %+v", cached[0])
	}
	for name, got := range map[string]string{
		"ALPN[0]":          cached[0].ALPN[0],
		"Settings[k]":      cached[0].Settings["k"],
		"MetricsLabels[m]": cached[0].MetricsLabels["m"],
		"WGAddresses[0]":   cached[0].WGAddresses[0],
		"WGAllowedIPs[0]":  cached[0].WGAllowedIPs[0],
		"WGDNS[0]":         cached[0].WGDNS[0],
	} {
		if got == "mutated" {
			t.Fatalf("cached config aliases original %s", name)
		}
	}
}

// The merge branch must hand out copies independent of the cache: mutating
// the merged set (as PrepareProxyConfigs does) must leave the cache pristine
// for the next cycle.
func TestCloneProxyConfigsReturnsIndependentCopies(t *testing.T) {
	cfg := &models.ProxyConfig{
		Protocol: "trojan", Server: "b.example", Port: 443, Name: "b", Password: "p",
		ALPN:     []string{"h2", "http/1.1"},
		Settings: map[string]string{"k": "v"},
	}
	rememberLastGoodLocked("src", []*models.ProxyConfig{cfg})

	merged := cloneProxyConfigs(lastGoodConfigs["src"])
	merged[0].Index = 7
	merged[0].StableID = "reassigned"
	merged[0].ALPN[0] = "mutated"
	merged[0].Settings["k"] = "mutated"

	cached := lastGoodConfigs["src"]
	if cached[0].Index == 7 || cached[0].StableID == "reassigned" {
		t.Fatalf("merge aliased cached structs: %+v", cached[0])
	}
	if cached[0].ALPN[0] == "mutated" || cached[0].Settings["k"] == "mutated" {
		t.Fatalf("merge aliased cached reference fields: %+v", cached[0])
	}
}
