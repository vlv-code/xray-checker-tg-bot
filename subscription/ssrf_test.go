package subscription

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},
		{"10.0.0.1", true},
		{"10.255.255.254", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"169.254.1.1", true},
		{"0.0.0.0", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:169.254.169.254", true},
		{"1.1.1.1", false},
		{"8.8.8.8", false},
		{"93.184.216.34", false},
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		if ip == nil {
			t.Fatalf("failed to parse IP %s", tt.ip)
		}
		got := isBlockedIP(ip)
		if got != tt.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", tt.ip, got, tt.blocked)
		}
	}
}

func TestSafeTransportBlocksLocalhost(t *testing.T) {
	transport := newSafeTransport()
	ctx := context.Background()

	_, err := transport.DialContext(ctx, "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("expected error connecting to 127.0.0.1, got nil")
	}
	if !strings.Contains(err.Error(), "SSRF") && !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF error message, got: %v", err)
	}
}

func TestValidateSubscriptionTarget_BlocksPrivateAndLocalhost(t *testing.T) {
	blockedURLs := []string{
		"http://127.0.0.1:2112/metrics",
		"http://169.254.169.254/latest/meta-data",
		"http://localhost:8080/secret",
		"http://192.168.1.1/config",
		"http://10.0.0.1/admin",
		"file:///etc/passwd",
	}

	for _, u := range blockedURLs {
		err := validateSubscriptionTarget(u)
		if err == nil {
			t.Errorf("expected URL %s to be blocked, got nil", u)
		}
	}
}

func TestIsEnvironmentProxy(t *testing.T) {
	t.Setenv("ALL_PROXY", "socks5://xray-client:1080")
	if !isEnvironmentProxy("xray-client", "1080") {
		t.Errorf("expected xray-client:1080 to be recognized as environment proxy")
	}
	if isEnvironmentProxy("xray-client", "80") {
		t.Errorf("xray-client on wrong port should not match")
	}
	if isEnvironmentProxy("attacker.com", "1080") {
		t.Errorf("attacker.com should not be recognized as environment proxy")
	}
}

