package telegram

import (
	"strings"
	"testing"
	"time"

	"xray-checker/metrics"
)

func TestNodeLineFor(t *testing.T) {
	if got := nodeLineFor(metrics.ProxyMetric{Name: "p"}); got != "" {
		t.Errorf("local proxy must have no node line, got %q", got)
	}
	got := nodeLineFor(metrics.ProxyMetric{NodeName: "n1"})
	if !strings.Contains(got, "n1") {
		t.Errorf("node line missing node: %q", got)
	}
	got = nodeLineFor(metrics.ProxyMetric{NodeName: "n1", NodeASN: "AS9009 M247"})
	if !strings.Contains(got, "AS9009 M247") {
		t.Errorf("node line missing ASN: %q", got)
	}
}

func TestCommandArgs(t *testing.T) {
	if got := commandArgs("/nodeaddsub n1 https://x/y"); len(got) != 2 || got[0] != "n1" || got[1] != "https://x/y" {
		t.Errorf("commandArgs: %v", got)
	}
	if got := commandArgs("/nodes"); len(got) != 0 {
		t.Errorf("commandArgs no-arg: %v", got)
	}
}

func TestFormatAge(t *testing.T) {
	cases := map[string]struct {
		d    time.Duration
		want string
	}{
		"12s":   {12 * time.Second, "12s"},
		"3m12s": {3*time.Minute + 12*time.Second, "3m12s"},
		"2h05m": {2*time.Hour + 5*time.Minute, "2h05m"},
	}
	for name, c := range cases {
		if got := formatAge(c.d); got != c.want {
			t.Errorf("%s: formatAge = %q, want %q", name, got, c.want)
		}
	}
}
