package metrics

import (
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

type fakeSource struct{ pms []ProxyMetric }

func (f fakeSource) MetricsSnapshot() []ProxyMetric { return f.pms }

// gatherSeries registers the collector and returns, per metric family name, one
// sorted "name=value,name=value" string per series.
func gatherSeries(t *testing.T, c prometheus.Collector) map[string][]string {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := make(map[string][]string)
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			var parts []string
			for _, l := range m.GetLabel() {
				parts = append(parts, l.GetName()+"="+l.GetValue())
			}
			sort.Strings(parts)
			out[mf.GetName()] = append(out[mf.GetName()], strings.Join(parts, ","))
		}
	}
	return out
}

func TestCollectorCustomLabels(t *testing.T) {
	src := fakeSource{pms: []ProxyMetric{
		{
			Protocol: "trojan", Address: "1.1.1.1:443", Name: "A", SubName: "s", StableID: "id1",
			CustomLabels: map[string]string{"location": "NL", "hoster": "FreeVDS"},
			Online:       true, LatencyMs: 227,
		},
		{
			Protocol: "vless", Address: "2.2.2.2:443", Name: "B", SubName: "s", StableID: "id2", GroupName: "g",
			Online: false, LatencyMs: 0,
		},
	}}
	c := NewCollector("", src)

	got := gatherSeries(t, c)
	if len(got["xray_proxy_status"]) != 2 || len(got["xray_proxy_latency_ms"]) != 2 {
		t.Fatalf("expected 2 series per family, got %v", got)
	}

	joined := strings.Join(got["xray_proxy_status"], "\n")
	if !strings.Contains(joined, "hoster=FreeVDS") || !strings.Contains(joined, "location=NL") {
		t.Errorf("custom labels missing from series:\n%s", joined)
	}
	// A series with custom labels (id1) and one without (id2) coexist in one family.
	if !strings.Contains(joined, "stable_id=id1") || !strings.Contains(joined, "stable_id=id2") {
		t.Errorf("expected both proxies present:\n%s", joined)
	}
}

func TestCollectorInstanceAndSanitizeAndReserved(t *testing.T) {
	src := fakeSource{pms: []ProxyMetric{
		{
			Protocol: "trojan", Address: "1.1.1.1:443", Name: "A", SubName: "s", StableID: "id1",
			CustomLabels: map[string]string{
				"data center": "dc1",   // space -> underscore
				"1region":     "eu",    // leading digit -> _1region
				"protocol":    "x",     // reserved -> skipped
				"instance":    "spoof", // reserved -> skipped
			},
			Online: true, LatencyMs: 1,
		},
	}}
	c := NewCollector("node-1", src)

	got := gatherSeries(t, c)
	if len(got["xray_proxy_status"]) != 1 {
		t.Fatalf("expected 1 series, got %v", got)
	}
	s := got["xray_proxy_status"][0]

	if !strings.Contains(s, "instance=node-1") {
		t.Errorf("instance label missing: %s", s)
	}
	if !strings.Contains(s, "data_center=dc1") {
		t.Errorf("space key should be sanitized to data_center: %s", s)
	}
	if !strings.Contains(s, "_1region=eu") {
		t.Errorf("leading-digit key should become _1region: %s", s)
	}
	// reserved keys keep their real values, the custom override is dropped
	if !strings.Contains(s, "protocol=trojan") || strings.Contains(s, "protocol=x") {
		t.Errorf("reserved protocol must not be overridden: %s", s)
	}
	if strings.Contains(s, "instance=spoof") {
		t.Errorf("reserved instance must not be overridden: %s", s)
	}
}

func TestCollectorSkipsReservedPrefix(t *testing.T) {
	src := fakeSource{pms: []ProxyMetric{{
		Protocol: "trojan", Address: "1.1.1.1:443", Name: "A", StableID: "id1",
		CustomLabels: map[string]string{
			"__region": "eu",   // Prometheus-reserved prefix -> must be skipped (no panic)
			"  weird":  "ok",   // sanitizes to "__weird" -> also reserved -> skipped
			"good":     "keep", // normal -> kept
		},
		Online: true, LatencyMs: 1,
	}}}
	c := NewCollector("", src)

	// Must not panic at scrape time.
	got := gatherSeries(t, c)
	if len(got["xray_proxy_status"]) != 1 {
		t.Fatalf("expected 1 series, got %v", got)
	}
	s := got["xray_proxy_status"][0]
	if strings.Contains(s, "__region") || strings.Contains(s, "__weird") {
		t.Errorf("reserved-prefix labels must be dropped: %s", s)
	}
	if !strings.Contains(s, "good=keep") {
		t.Errorf("normal custom label should be kept: %s", s)
	}
}

func TestSanitizeLabelName(t *testing.T) {
	cases := map[string]string{
		"location":    "location",
		"data center": "data_center",
		"1region":     "_1region",
		"a-b.c":       "a_b_c",
		"":            "",
		"123":         "_123",
	}
	for in, want := range cases {
		if got := sanitizeLabelName(in); got != want {
			t.Errorf("sanitizeLabelName(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestCollectorDisabledProxy(t *testing.T) {
	src := fakeSource{pms: []ProxyMetric{
		{
			Protocol: "vless", Address: "1.1.1.1:443", Name: "Proxy1", StableID: "id1",
			Online: true, LatencyMs: 150, Disabled: true,
		},
		{
			Protocol: "vless", Address: "2.2.2.2:443", Name: "Proxy2", StableID: "id2",
			Online: true, LatencyMs: 120, Disabled: false,
		},
	}}
	c := NewCollector("", src)
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	for _, mf := range mfs {
		if mf.GetName() == "xray_proxy_status" {
			for _, m := range mf.GetMetric() {
				var stableID string
				for _, l := range m.GetLabel() {
					if l.GetName() == "stable_id" {
						stableID = l.GetValue()
					}
				}
				val := m.GetGauge().GetValue()
				if stableID == "id1" && val != 0 {
					t.Errorf("expected status 0 for disabled proxy id1, got %f", val)
				}
				if stableID == "id2" && val != 1 {
					t.Errorf("expected status 1 for enabled online proxy id2, got %f", val)
				}
			}
		}
		if mf.GetName() == "xray_proxy_latency_ms" {
			for _, m := range mf.GetMetric() {
				var stableID string
				for _, l := range m.GetLabel() {
					if l.GetName() == "stable_id" {
						stableID = l.GetValue()
					}
				}
				val := m.GetGauge().GetValue()
				if stableID == "id1" && val != 0 {
					t.Errorf("expected latency 0 for disabled proxy id1, got %f", val)
				}
				if stableID == "id2" && val != 120 {
					t.Errorf("expected latency 120 for enabled online proxy id2, got %f", val)
				}
			}
		}
	}
}
