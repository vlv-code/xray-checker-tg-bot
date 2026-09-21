package config

import (
	"os"
	"strings"
	"testing"
)

func TestSanitizeNoProxy(t *testing.T) {
	tests := []struct {
		name      string
		existing  string
		reportURL string
		mustHave  []string
	}{
		{
			name:      "empty existing",
			existing:  "",
			reportURL: "",
			mustHave:  []string{"localhost", "127.0.0.1", "10.0.0.0/8", "192.168.0.0/16"},
		},
		{
			name:      "with reportURL ip and port",
			existing:  "",
			reportURL: "http://198.51.100.42:2112/api/v1/nodes/report",
			mustHave:  []string{"localhost", "127.0.0.1", "198.51.100.42"},
		},
		{
			name:      "with reportURL hostname",
			existing:  "corp.net, .example.com",
			reportURL: "https://master.internal.domain:8443/report",
			mustHave:  []string{"corp.net", ".example.com", "master.internal.domain", "127.0.0.1"},
		},
		{
			name:      "already has localhost and 127.0.0.1",
			existing:  "localhost, 127.0.0.1, custom.host",
			reportURL: "http://custom.host:2112",
			mustHave:  []string{"localhost", "127.0.0.1", "custom.host"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := SanitizeNoProxy(tt.existing, tt.reportURL)
			parts := strings.Split(res, ",")
			partSet := make(map[string]bool)
			for _, p := range parts {
				partSet[strings.TrimSpace(p)] = true
			}
			for _, m := range tt.mustHave {
				if !partSet[m] {
					t.Errorf("SanitizeNoProxy(%q, %q) missing %q, got: %s", tt.existing, tt.reportURL, m, res)
				}
			}
		})
	}
}

func TestGetBootstrapProxy(t *testing.T) {
	orig := os.Getenv("BOOTSTRAP_PROXY")
	defer os.Setenv("BOOTSTRAP_PROXY", orig)

	os.Setenv("BOOTSTRAP_PROXY", "http://10.0.0.1:8080")
	if p := GetBootstrapProxy(); p != "http://10.0.0.1:8080" {
		t.Errorf("expected http://10.0.0.1:8080, got %s", p)
	}

	os.Unsetenv("BOOTSTRAP_PROXY")
	origGeo := os.Getenv("GEO_PROXY")
	defer os.Setenv("GEO_PROXY", origGeo)
	os.Setenv("GEO_PROXY", "socks5://10.0.0.1:1080")
	if p := GetBootstrapProxy(); p != "socks5://10.0.0.1:1080" {
		t.Errorf("expected socks5://10.0.0.1:1080, got %s", p)
	}
}

func TestSetupNetworkEnvironment(t *testing.T) {
	origNP := os.Getenv("NO_PROXY")
	origNp := os.Getenv("no_proxy")
	defer func() {
		os.Setenv("NO_PROXY", origNP)
		os.Setenv("no_proxy", origNp)
	}()

	os.Setenv("NO_PROXY", "test.local")
	SetupNetworkEnvironment("http://198.51.100.42:2112")

	newNP := os.Getenv("NO_PROXY")
	newNp := os.Getenv("no_proxy")
	if !strings.Contains(newNP, "198.51.100.42") || !strings.Contains(newNP, "test.local") {
		t.Errorf("NO_PROXY not properly set: %s", newNP)
	}
	if newNP != newNp {
		t.Errorf("NO_PROXY (%s) != no_proxy (%s)", newNP, newNp)
	}
}
