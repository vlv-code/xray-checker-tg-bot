package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"xray-checker/config"
	"xray-checker/metrics"
	"xray-checker/nodes"
	"xray-checker/telegram"
)

type mockLocalSource struct {
	data []metrics.ProxyMetric
}

func (m *mockLocalSource) MetricsSnapshot() []metrics.ProxyMetric {
	return m.data
}

func TestNodeManagerAdapter(t *testing.T) {
	reg := nodes.NewRegistry([]nodes.NodeConfig{{Name: "n1", Token: "t"}}, nil, nil)
	subs, err := nodes.NewNodeSubsStore(filepath.Join(t.TempDir(), "subs.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &nodeManagerAdapter{reg: reg, subs: subs}

	if err := a.AddSub("nope", "https://x/y"); err == nil {
		t.Error("unknown node must error")
	}
	if err := a.AddSub("n1", "https://x/y"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddSub("n1", "https://x/y"); err == nil {
		t.Error("duplicate must error")
	}
	got, err := a.ManagedSubs("n1")
	if err != nil || len(got) != 1 || got[0].ProxyCount != -1 {
		t.Fatalf("ManagedSubs: %v %v", got, err)
	}
	if err := a.RemoveSub("n1", "https://x/y"); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveSub("n1", "https://x/y"); err == nil {
		t.Error("removing twice must error")
	}
	if snap := a.NodeSnapshot("n1"); snap != nil {
		t.Fatalf("expected nil snapshot initially, got %v", snap)
	}
	if diag := a.NodeDiagReports("n1"); diag != nil {
		t.Fatalf("expected nil diag reports initially, got %v", diag)
	}

	// When node reports diagnostics, a.NodeDiagReports returns them
	h := reg.HandleReport()
	body := []byte(`{"version":"1.0","checkIntervalSec":60,"proxies":[{"stableID":"rem1","name":"NodeProxy","online":true,"verdict":"OK","targets":[{"url":"https://cp.cloudflare.com/generate_204","online":true,"latencyMs":45}]}]}`)
	req, _ := http.NewRequest("POST", "/api/v1/nodes/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()
	h(w, req)

	diags := a.NodeDiagReports("n1")
	if len(diags) != 1 {
		t.Fatalf("expected 1 diag report, got %d", len(diags))
	}
	if diags[0].ProxyName != "NodeProxy" || diags[0].Verdict != "OK" || len(diags[0].Targets) != 1 {
		t.Fatalf("unexpected diag report: %+v", diags[0])
	}

	// After report, single managed subscription should reflect the reported proxy count
	if err := a.AddSub("n1", "https://x/y"); err != nil {
		t.Fatal(err)
	}
	gotAfterReport, err := a.ManagedSubs("n1")
	if err != nil || len(gotAfterReport) != 1 {
		t.Fatalf("ManagedSubs after report error: %v, len: %d", err, len(gotAfterReport))
	}
	if gotAfterReport[0].ProxyCount != 1 {
		t.Errorf("expected ProxyCount = 1 after node reported 1 proxy, got %d", gotAfterReport[0].ProxyCount)
	}
}

func TestMergedMetricsSource(t *testing.T) {
	localSrc := &mockLocalSource{
		data: []metrics.ProxyMetric{
			{StableID: "local1", Name: "MasterProxy", Online: true},
		},
	}
	reg := nodes.NewRegistry([]nodes.NodeConfig{{Name: "n1", Token: "t1"}}, nil, nil)
	merged := &mergedMetricsSource{local: localSrc, reg: reg}

	snap := merged.MetricsSnapshot()
	if len(snap) != 1 || snap[0].StableID != "local1" {
		t.Fatalf("expected 1 local metric, got %v", snap)
	}

	// When node reports, merged snapshot includes it
	h := reg.HandleReport()
	body := []byte(`{"version":"1.0","checkIntervalSec":60,"proxies":[{"stableID":"rem1","name":"NodeProxy","online":true}]}`)
	req, _ := http.NewRequest("POST", "/api/v1/nodes/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer t1")
	w := httptest.NewRecorder()
	h(w, req)

	snap = merged.MetricsSnapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 merged metrics (local + node), got %d", len(snap))
	}
}

func TestNodeManagerAdapter_Settings(t *testing.T) {
	reg := nodes.NewRegistry([]nodes.NodeConfig{{Name: "n1", Token: "t"}}, nil, nil)
	subsStore, err := nodes.NewNodeSubsStore(filepath.Join(t.TempDir(), "subs.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := nodes.NewNodesStore(filepath.Join(t.TempDir(), "nodes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("n1", "t"); err != nil {
		t.Fatal(err)
	}
	a := &nodeManagerAdapter{reg: reg, subs: subsStore, store: store}

	// Seed master base values (kong provides these defaults in production).
	config.CLIConfig.Proxy.CheckMethod = "ip"
	config.CLIConfig.Proxy.CheckInterval = 300
	config.CLIConfig.Subscription.UpdateInterval = 300

	// View on unknown node errors.
	if _, err := a.NodeSettingsView("nope"); err == nil {
		t.Fatal("unknown node must error")
	}

	// Set invalid key / value.
	if err := a.SetNodeSetting("n1", "bogus", "1"); err == nil {
		t.Fatal("unknown key must error")
	}
	if err := a.SetNodeSetting("n1", "check_method", "bogus"); err == nil {
		t.Fatal("invalid method must error")
	}
	if err := a.SetNodeSetting("n1", "check_interval", "-5"); err == nil {
		t.Fatal("non-positive interval must error")
	}

	// Valid set overrides the base value.
	if err := a.SetNodeSetting("n1", "check_interval", "120"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetNodeSetting("n1", "check_concurrency", "0"); err != nil {
		t.Fatal("explicit zero concurrency must be accepted:", err)
	}
	entries, err := a.NodeSettingsView("n1")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]telegram.NodeSettingEntry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	if !byKey["check_interval"].Overridden || byKey["check_interval"].Value != "120" {
		t.Fatalf("check_interval override not shown: %+v", byKey["check_interval"])
	}
	if !byKey["check_concurrency"].Overridden || byKey["check_concurrency"].Value != "0" {
		t.Fatalf("zero concurrency override not shown: %+v", byKey["check_concurrency"])
	}
	if byKey["check_method"].Overridden {
		t.Fatalf("unmodified key must not be marked: %+v", byKey["check_method"])
	}
	if byKey["check_method"].Value == "" {
		t.Fatal("base value must be present")
	}

	// Reset one key.
	if err := a.ResetNodeSetting("n1", "check_interval"); err != nil {
		t.Fatal(err)
	}
	entries, _ = a.NodeSettingsView("n1")
	for _, e := range entries {
		if e.Key == "check_interval" && e.Overridden {
			t.Fatal("check_interval must be reset")
		}
	}
	if err := a.ResetNodeSetting("n1", "bogus"); err == nil {
		t.Fatal("unknown reset key must error")
	}

	// Reset all.
	if err := a.ResetNodeSetting("n1", ""); err != nil {
		t.Fatal(err)
	}
	entries, _ = a.NodeSettingsView("n1")
	for _, e := range entries {
		if e.Overridden {
			t.Fatalf("all overrides must be cleared, %s still set", e.Key)
		}
	}
}
