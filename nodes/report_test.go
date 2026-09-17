package nodes

import (
	"testing"

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
