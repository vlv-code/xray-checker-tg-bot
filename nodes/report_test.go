package nodes

import (
	"encoding/json"
	"testing"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

func sampleMetric() metrics.ProxyMetric {
	return metrics.ProxyMetric{
		Protocol: "vless", Address: "host.example:443", Name: "de-01",
		SubName: "sub1", GroupName: "g1", StableID: "a1b2c3",
		Online: true, LatencyMs: 123, LastErrorCategory: 0,
	}
}

func TestBuildReportAndMapBack(t *testing.T) {
	pm := sampleMetric()
	payload := BuildReport([]metrics.ProxyMetric{pm}, "1.2.3", 300, "ip", "203.0.113.7")
	if payload.Version != "1.2.3" || payload.CheckIntervalSec != 300 ||
		payload.CheckMethod != "ip" || payload.HostIP != "203.0.113.7" {
		t.Fatalf("bad payload meta: %+v", payload)
	}
	if len(payload.Proxies) != 1 {
		t.Fatalf("want 1 proxy, got %d", len(payload.Proxies))
	}
	rp := payload.Proxies[0]
	if rp.StableID != "a1b2c3" || rp.Name != "de-01" || !rp.Online || rp.LatencyMs != 123 {
		t.Errorf("bad report proxy: %+v", rp)
	}

	out := ProxyMetricsFromReport("node-1", "AS9009 M247", payload)
	if len(out) != 1 {
		t.Fatalf("want 1 metric, got %d", len(out))
	}
	got := out[0]
	if got.StableID != "node-1/a1b2c3" {
		t.Errorf("stable id not namespaced: %q", got.StableID)
	}
	if got.NodeName != "node-1" || got.NodeASN != "AS9009 M247" {
		t.Errorf("node fields not set: %+v", got)
	}
	if got.Name != "de-01" || got.Address != "host.example:443" || !got.Online {
		t.Errorf("proxy fields lost: %+v", got)
	}
}

func TestMapReportDisabledPassthrough(t *testing.T) {
	payload := ReportPayload{Version: "v", CheckIntervalSec: 60, Proxies: []ReportProxy{{
		StableID: "x", Name: "n", Online: false, Disabled: true,
	}}}
	out := ProxyMetricsFromReport("n1", "", payload)
	if !out[0].Disabled {
		t.Error("Disabled must pass through for the grace/alert-cleanup path")
	}
}

func TestBuildReportFromDiag(t *testing.T) {
	reports := []checker.ProxyDiagReport{
		{
			ProxyName: "de-01",
			Protocol:  "vless",
			Server:    "host.example",
			Port:      443,
			StableID:  "a1b2c3",
			Status:    "online",
			Verdict:   "Полностью исправен",
			NodeHealth: checker.NodeHealth{
				ResolvedIP: "1.2.3.4",
				DNSLatency: 15 * time.Millisecond,
				TCPPing:    45 * time.Millisecond,
				TLSLatency: 60 * time.Millisecond,
			},
			Targets: []checker.TargetDiagResult{
				{URL: "https://cp.cloudflare.com/generate_204", Success: true, StatusCode: 204, Latency: 110 * time.Millisecond},
				{URL: "https://www.gstatic.com/generate_204", Success: true, StatusCode: 204, Latency: 125 * time.Millisecond},
			},
		},
	}

	payload := BuildReportFromDiag(reports, "1.2.3", 300, "multi", "203.0.113.7")
	if len(payload.Proxies) != 1 {
		t.Fatalf("want 1 proxy, got %d", len(payload.Proxies))
	}
	rp := payload.Proxies[0]
	if rp.Name != "de-01" || rp.StableID != "a1b2c3" || !rp.Online || rp.LatencyMs != 110 {
		t.Errorf("bad proxy summary: %+v", rp)
	}
	if rp.NodeHealth == nil || rp.NodeHealth.ResolvedIP != "1.2.3.4" || rp.NodeHealth.TCPPing != 45*time.Millisecond {
		t.Errorf("bad node health: %+v", rp.NodeHealth)
	}
	if len(rp.Targets) != 2 || rp.Targets[0].StatusCode != 204 {
		t.Errorf("bad targets: %+v", rp.Targets)
	}
	if rp.Verdict != "Полностью исправен" {
		t.Errorf("bad verdict: %s", rp.Verdict)
	}

	// Test JSON serialization roundtrip
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded ReportPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if decoded.Proxies[0].NodeHealth == nil || decoded.Proxies[0].NodeHealth.ResolvedIP != "1.2.3.4" {
		t.Errorf("decoded NodeHealth corrupted: %+v", decoded.Proxies[0].NodeHealth)
	}
	if len(decoded.Proxies[0].Targets) != 2 {
		t.Errorf("decoded Targets corrupted: %+v", decoded.Proxies[0].Targets)
	}
}

func TestBuildReportFromDiagWithMetrics(t *testing.T) {
	reports := []checker.ProxyDiagReport{
		{
			ProxyName: "proxy1",
			StableID:  "sid-1",
			Protocol:  "vless",
			Server:    "example.com",
			Port:      443,
			Status:    "online",
			Verdict:   "OK",
		},
	}
	snap := []metrics.ProxyMetric{
		{
			StableID:          "sid-1",
			SubName:           "Premium Sub",
			GroupName:         "Europe",
			LastErrorCategory: 1,
			LastCheckSec:      1700000000,
		},
	}

	payload := BuildReportFromDiagWithMetrics(reports, snap, "1.0", 60, "connect", "1.1.1.1")
	if len(payload.Proxies) != 1 {
		t.Fatalf("expected 1 proxy, got %d", len(payload.Proxies))
	}
	rp := payload.Proxies[0]
	if rp.SubName != "Premium Sub" {
		t.Errorf("expected SubName 'Premium Sub', got %q", rp.SubName)
	}
	if rp.GroupName != "Europe" {
		t.Errorf("expected GroupName 'Europe', got %q", rp.GroupName)
	}
	if rp.LastErrorCategory != 1 {
		t.Errorf("expected LastErrorCategory 1, got %d", rp.LastErrorCategory)
	}
	if rp.LastCheck != 1700000000 {
		t.Errorf("expected LastCheck 1700000000, got %d", rp.LastCheck)
	}
}

func TestResolveNodeSync(t *testing.T) {
	base := NodeConfigSync{
		SyncEnabled:           true,
		CheckIntervalSec:      300,
		TargetURLs:            []string{"https://master.example/204"},
		CheckMethod:           "ip",
		IpCheckURL:            "https://api.ipify.org",
		StatusCheckURL:        "http://cp.cloudflare.com/generate_204",
		DownloadURL:           "https://proof.ovh.net/files/1Mb.dat",
		ProxyTimeoutSec:       30,
		DownloadTimeoutSec:    60,
		DownloadMinSize:       51200,
		CheckConcurrency:      0,
		SubsUpdateIntervalSec: 300,
	}

	// nil overrides: base passes through untouched.
	got := ResolveNodeSync(base, nil)
	if got.CheckIntervalSec != 300 || got.CheckMethod != "ip" || got.CheckConcurrency != 0 {
		t.Fatalf("nil overrides changed base: %+v", got)
	}

	interval := 120
	method := "status"
	timeout := 15
	conc := 4
	var minSize int64 = 1024
	subsInt := 600
	url := "https://node.example/204"
	ns := &NodeSettings{
		CheckIntervalSec:      &interval,
		CheckMethod:           &method,
		ProxyTimeoutSec:       &timeout,
		CheckConcurrency:      &conc,
		DownloadMinSize:       &minSize,
		SubsUpdateIntervalSec: &subsInt,
		TargetURLs:            []string{url},
	}
	got = ResolveNodeSync(base, ns)
	if got.CheckIntervalSec != 120 || got.CheckMethod != "status" ||
		got.ProxyTimeoutSec != 15 || got.CheckConcurrency != 4 ||
		got.DownloadMinSize != 1024 || got.SubsUpdateIntervalSec != 600 ||
		len(got.TargetURLs) != 1 || got.TargetURLs[0] != url {
		t.Fatalf("overrides not applied: %+v", got)
	}
	// Untouched base fields survive.
	if got.IpCheckURL != base.IpCheckURL || got.DownloadURL != base.DownloadURL ||
		got.DownloadTimeoutSec != 60 {
		t.Fatalf("base fields lost: %+v", got)
	}

	// Empty (non-nil) TargetURLs override resets node to built-in defaults.
	ns2 := &NodeSettings{TargetURLs: []string{}}
	got = ResolveNodeSync(base, ns2)
	if len(got.TargetURLs) != 0 {
		t.Fatalf("empty override must clear targets, got %v", got.TargetURLs)
	}
}

func TestSyncToRuntimeSettings(t *testing.T) {
	sync := NodeConfigSync{
		CheckMethod:           "download",
		IpCheckURL:            "https://ip.example/",
		ProxyTimeoutSec:       20,
		DownloadMinSize:       4096,
		CheckConcurrency:      0,
		SubsUpdateIntervalSec: 600,
	}
	rs := SyncToRuntimeSettings(sync)
	if rs.CheckMethod == nil || *rs.CheckMethod != "download" {
		t.Fatal("method not mapped")
	}
	if rs.CheckConcurrency == nil || *rs.CheckConcurrency != 0 {
		t.Fatal("explicit zero concurrency must map to non-nil pointer")
	}
	if rs.IpCheckURL == nil || rs.ProxyTimeoutSec == nil || rs.DownloadMinSize == nil {
		t.Fatal("fields not mapped")
	}
}
