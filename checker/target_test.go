package checker

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTargetManager_AddRemove(t *testing.T) {
	tm := NewTargetManager([]string{
		"https://cp.cloudflare.com/generate_204",
		"https://www.gstatic.com/generate_204",
	})

	targets := tm.GetTargets()
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}

	err := tm.AddTarget("https://example.com/check")
	if err != nil {
		t.Fatalf("AddTarget failed: %v", err)
	}
	if len(tm.GetTargets()) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(tm.GetTargets()))
	}

	// Duplicate add
	err = tm.AddTarget("https://example.com/check")
	if err == nil {
		t.Errorf("expected error on duplicate target, got nil")
	}

	// Remove target
	err = tm.RemoveTarget("https://example.com/check")
	if err != nil {
		t.Fatalf("RemoveTarget failed: %v", err)
	}
	if len(tm.GetTargets()) != 2 {
		t.Fatalf("expected 2 targets after removal, got %d", len(tm.GetTargets()))
	}
}

func TestCheckSingleTarget(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	client := ts.Client()
	client.Timeout = 2 * time.Second

	res := CheckSingleTarget(client, ts.URL)
	if !res.Success {
		t.Errorf("expected success, got error: %s", res.Error)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", res.StatusCode)
	}
	if res.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", res.Latency)
	}
}

func TestProbeNodeHealth_TCPAndDNS(t *testing.T) {
	// 1. Valid local TCP listener
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port

	health := ProbeNodeHealth("127.0.0.1", port, "vless", "none", "", false)
	if health.ResolvedIP != "127.0.0.1" {
		t.Errorf("expected resolved IP 127.0.0.1, got %s", health.ResolvedIP)
	}
	if health.TCPErr != "" {
		t.Errorf("expected no TCP error on open port, got %s", health.TCPErr)
	}
	if health.TCPPing <= 0 {
		t.Errorf("expected positive TCP ping, got %v", health.TCPPing)
	}

	// 2. Closed port on TCP protocol
	l.Close()
	closedHealth := ProbeNodeHealth("127.0.0.1", port, "vless", "none", "", false)
	if closedHealth.TCPErr == "" {
		t.Errorf("expected TCP error on closed port, got nil")
	}

	// 3. UDP protocol (Hysteria) skips TCP ping
	udpHealth := ProbeNodeHealth("127.0.0.1", port, "hysteria", "none", "", false)
	if udpHealth.TCPErr != "" {
		t.Errorf("expected no TCP error for UDP protocol, got %s", udpHealth.TCPErr)
	}

	// 4. Invalid DNS host
	dnsHealth := ProbeNodeHealth("invalid-test-domain-not-found.invalid", 443, "vless", "none", "", false)
	if dnsHealth.DNSErr == "" {
		t.Errorf("expected DNS error on invalid domain, got nil")
	}
}

func TestDetermineVerdict(t *testing.T) {
	// 1. All ok
	status, verdict := DetermineVerdict("vless", NodeHealth{ResolvedIP: "1.1.1.1"}, []TargetDiagResult{
		{Success: true},
		{Success: true},
	})
	if status != "online" || verdict != "Полностью исправен" {
		t.Errorf("expected online / Полностью исправен, got %s / %s", status, verdict)
	}

	// 2. Target success takes priority over any TCP error (especially for UDP/Hysteria)
	udpSuccessStatus, udpSuccessVerdict := DetermineVerdict("hysteria", NodeHealth{
		ResolvedIP: "1.1.1.1",
		TCPErr:     "Connection refused",
	}, []TargetDiagResult{
		{Success: true},
		{Success: true},
	})
	if udpSuccessStatus != "online" || udpSuccessVerdict != "Полностью исправен" {
		t.Errorf("expected target success to override TCP error, got %s / %s", udpSuccessStatus, udpSuccessVerdict)
	}

	// 3. DNS error
	status, _ = DetermineVerdict("vless", NodeHealth{DNSErr: "no such host"}, nil)
	if status != "offline" {
		t.Errorf("expected offline on DNS error, got %s", status)
	}

	// 4. TCP Timeout (TCP protocol)
	status, verdict = DetermineVerdict("vless", NodeHealth{TCPErr: "Timeout"}, nil)
	if status != "offline" || !strings.Contains(verdict, "таймаут") {
		t.Errorf("expected offline / таймаут, got %s / %s", status, verdict)
	}

	// 5. TCP Refused (TCP protocol)
	status, verdict = DetermineVerdict("vless", NodeHealth{TCPErr: "Connection refused"}, nil)
	if status != "offline" || !strings.Contains(verdict, "порт закрыт") {
		t.Errorf("expected offline / порт закрыт, got %s / %s", status, verdict)
	}

	// 6. TLS error
	status, verdict = DetermineVerdict("vless", NodeHealth{TLSErr: "Certificate expired"}, nil)
	if status != "offline" || !strings.Contains(verdict, "Сбой TLS") {
		t.Errorf("expected offline / Сбой TLS, got %s / %s", status, verdict)
	}

	// 7. Partial targets (Degraded)
	status, _ = DetermineVerdict("vless", NodeHealth{ResolvedIP: "1.1.1.1"}, []TargetDiagResult{
		{Success: true},
		{Success: false, Error: "Timeout"},
	})
	if status != "degraded" {
		t.Errorf("expected degraded, got %s", status)
	}

	// 8. EOF from session
	status, verdict = DetermineVerdict("vless", NodeHealth{ResolvedIP: "1.1.1.1"}, []TargetDiagResult{
		{Success: false, Error: "EOF / Connection reset"},
		{Success: false, Error: "EOF / Connection reset"},
	})
	if status != "offline" || !strings.Contains(verdict, "сбросил сессию") {
		t.Errorf("expected offline / session EOF, got %s / %s", status, verdict)
	}
}

func TestEnrichVerdictWithCheckHost(t *testing.T) {
	baseVerdict := "Нода не отвечает на TCP (таймаут)"

	// RU offline, World online
	ch1 := &CheckHostSummary{RUAvailable: false, WorldAvailable: true}
	v1 := EnrichVerdictWithCheckHost(baseVerdict, ch1)
	if !strings.Contains(v1, "недоступен из узлов РФ, но отвечает из зарубежных сетей") {
		t.Errorf("expected RU unavailable verdict, got: %s", v1)
	}

	// Both offline
	ch2 := &CheckHostSummary{RUAvailable: false, WorldAvailable: false}
	v2 := EnrichVerdictWithCheckHost(baseVerdict, ch2)
	if !strings.Contains(v2, "недоступен как из РФ, так и из других стран") {
		t.Errorf("expected both offline verdict, got: %s", v2)
	}

	// Both online
	ch3 := &CheckHostSummary{RUAvailable: true, WorldAvailable: true}
	v3 := EnrichVerdictWithCheckHost(baseVerdict, ch3)
	if !strings.Contains(v3, "отвечает из РФ") {
		t.Errorf("expected RU online verdict, got: %s", v3)
	}

	// Nil CheckHost returns original verdict
	v4 := EnrichVerdictWithCheckHost(baseVerdict, nil)
	if v4 != baseVerdict {
		t.Errorf("expected unchanged verdict on nil, got: %s", v4)
	}
}

func TestProbeNodeHealth_UDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen packet: %v", err)
	}
	defer pc.Close()

	addr := pc.LocalAddr().(*net.UDPAddr)

	// Echo listener
	go func() {
		buf := make([]byte, 1024)
		for {
			n, clientAddr, rErr := pc.ReadFrom(buf)
			if rErr != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], clientAddr)
		}
	}()

	health := ProbeNodeHealth("127.0.0.1", addr.Port, "hysteria2", "", "", false)
	if health.UDPErr != "" {
		t.Errorf("expected no UDP error for active UDP listener, got %s", health.UDPErr)
	}
	if health.UDPPing <= 0 {
		t.Errorf("expected positive UDPPing, got %v", health.UDPPing)
	}
}

func TestTargetManager_SSRF_Protection(t *testing.T) {
	tm := NewTargetManager(nil)

	blockedTargets := []string{
		"http://127.0.0.1/test",
		"http://127.0.0.2:8080/metrics",
		"http://localhost/check",
		"https://my.localhost/check",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.1/admin",
		"http://172.16.0.1/api",
		"http://192.168.1.1/setup",
		"http://0.0.0.0/test",
		"http://[::1]/status",
	}

	for _, target := range blockedTargets {
		err := tm.AddTarget(target)
		if err == nil {
			t.Errorf("expected target %q to be blocked by SSRF check, got nil", target)
		} else if !strings.Contains(err.Error(), "SSRF") {
			t.Errorf("expected error for %q to contain 'SSRF', got %v", target, err)
		}
	}

	allowedTargets := []string{
		"https://example.com/generate_204",
		"https://1.1.1.1/generate_204",
		"https://8.8.8.8/test",
		"https://google.com/health",
	}

	for _, target := range allowedTargets {
		err := tm.AddTarget(target)
		if err != nil {
			t.Errorf("expected allowed target %q to succeed, got %v", target, err)
		}
	}
}
