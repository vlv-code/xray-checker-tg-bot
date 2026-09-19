package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"xray-checker/metrics"
	"xray-checker/nodes"
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
