package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckHostClient_CheckTCP(t *testing.T) {
	// Mock Check-Host server
	pollCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			resp := map[string]interface{}{
				"ok":             1,
				"request_id":     "test1234",
				"permanent_link": "https://check-host.net/check-report/test1234",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "194.26.229.20", "AS210644"},
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Nuremberg", "142.132.174.167", "AS24940"},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/check-result/test1234") {
			pollCount++
			if pollCount == 1 {
				// first poll: still in progress
				resp := map[string]interface{}{
					"ru2.node.check-host.net": nil,
					"de1.node.check-host.net": nil,
				}
				json.NewEncoder(w).Encode(resp)
				return
			}
			// second poll: completed
			resp := map[string]interface{}{
				"ru2.node.check-host.net": []interface{}{
					map[string]interface{}{"error": "Connection timed out"},
				},
				"de1.node.check-host.net": []interface{}{
					map[string]interface{}{"time": 0.025, "address": "1.1.1.1"},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewCheckHostClient(ts.URL, 100*time.Millisecond)
	client.HTTPClient = ts.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	summary, err := client.CheckTCP(ctx, "1.1.1.1:443", []string{"ru2.node.check-host.net", "de1.node.check-host.net"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if summary.RequestID != "test1234" {
		t.Errorf("expected request_id test1234, got %s", summary.RequestID)
	}
	if summary.PermanentLink != "https://check-host.net/check-report/test1234" {
		t.Errorf("expected permalink, got %s", summary.PermanentLink)
	}
	if summary.RUAvailable {
		t.Errorf("expected RUAvailable=false due to timeout, got true")
	}
	if !summary.WorldAvailable {
		t.Errorf("expected WorldAvailable=true due to de1 success, got false")
	}
	if !strings.Contains(summary.Verdict, "недоступен из узлов РФ") {
		t.Errorf("expected verdict about RU unavailability, got: %s", summary.Verdict)
	}
}

func TestCheckHost_EvaluateVerdict(t *testing.T) {
	cases := []struct {
		ru    bool
		world bool
		want  string
	}{
		{false, true, "Хост недоступен из узлов РФ, но отвечает из зарубежных сетей"},
		{false, false, "Хост недоступен как из РФ, так и из других стран"},
		{true, true, "Хост доступен из всех проверяемых сетей"},
		{true, false, "Хост доступен из РФ, но недоступен из части внешних сетей"},
	}

	for _, c := range cases {
		got := EvaluateCheckHostVerdict(c.ru, c.world)
		if !strings.Contains(got, c.want) {
			t.Errorf("for ru=%v, world=%v expected %q in %q", c.ru, c.world, c.want, got)
		}
	}
}

func TestFormatCheckHostReport(t *testing.T) {
	summary := &CheckHostSummary{
		Host:          "1.1.1.1:443",
		PermanentLink: "https://check-host.net/check-report/test1234",
		Results: []CheckHostNodeResult{
			{Node: "ru2.node.check-host.net", Country: "ru", City: "Moscow", Success: false, Error: "Connection timed out"},
			{Node: "de1.node.check-host.net", Country: "de", City: "Frankfurt", Success: true, Latency: 25 * time.Millisecond},
			{Node: "us1.node.check-host.net", Country: "us", City: "Los Angeles", Success: true, Latency: 120 * time.Millisecond},
		},
		RUAvailable:    false,
		WorldAvailable: true,
		Verdict:        EvaluateCheckHostVerdict(false, true),
	}

	report := FormatCheckHostReport(summary)
	if !strings.Contains(report, "1.1.1.1:443") {
		t.Errorf("expected host in report, got: %s", report)
	}
	if !strings.Contains(report, "Россия") {
		t.Errorf("expected Russia section in report, got: %s", report)
	}
	if !strings.Contains(report, "Европа") {
		t.Errorf("expected Europe section in report, got: %s", report)
	}
	if !strings.Contains(report, "https://check-host.net/check-report/test1234") {
		t.Errorf("expected permalink in report, got: %s", report)
	}
}

func TestCheckHostClient_CheckPing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-ping") {
			resp := map[string]interface{}{
				"ok":             1,
				"request_id":     "ping1234",
				"permanent_link": "https://check-host.net/check-report/ping1234",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "194.26.229.20", "AS210644"},
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Nuremberg", "142.132.174.167", "AS24940"},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/check-result/ping1234") {
			resp := map[string]interface{}{
				"ru2.node.check-host.net": []interface{}{
					[]interface{}{
						[]interface{}{"TIMEOUT"},
						[]interface{}{"TIMEOUT"},
					},
				},
				"de1.node.check-host.net": []interface{}{
					[]interface{}{
						[]interface{}{"OK", 0.015, "1.1.1.1"},
						[]interface{}{"OK", 0.016, "1.1.1.1"},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewCheckHostClient(ts.URL, 50*time.Millisecond)
	client.HTTPClient = ts.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	summary, err := client.CheckPing(ctx, "1.1.1.1", []string{"ru2.node.check-host.net", "de1.node.check-host.net"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if summary.RUAvailable {
		t.Errorf("expected RUAvailable=false for ping timeout, got true")
	}
	if !summary.WorldAvailable {
		t.Errorf("expected WorldAvailable=true for de1 ping OK, got false")
	}
}

func TestCheckHostClient_WaitsForAllNodes(t *testing.T) {
	pollCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			resp := map[string]interface{}{
				"ok":             1,
				"request_id":     "wait1234",
				"permanent_link": "https://check-host.net/check-report/wait1234",
				"nodes": map[string]interface{}{
					"ru2.node.check-host.net": []interface{}{"ru", "Russia", "Moscow", "194.26.229.20", "AS210644"},
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Nuremberg", "142.132.174.167", "AS24940"},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/check-result/wait1234") {
			pollCount++
			if pollCount == 1 {
				// Node de1 finished, but ru2 is still null (pending)
				resp := map[string]interface{}{
					"ru2.node.check-host.net": nil,
					"de1.node.check-host.net": []interface{}{
						map[string]interface{}{"time": 0.020, "address": "1.1.1.1"},
					},
				}
				json.NewEncoder(w).Encode(resp)
				return
			}
			// Second poll: ru2 also finished successfully
			resp := map[string]interface{}{
				"ru2.node.check-host.net": []interface{}{
					map[string]interface{}{"time": 0.045, "address": "1.1.1.1"},
				},
				"de1.node.check-host.net": []interface{}{
					map[string]interface{}{"time": 0.020, "address": "1.1.1.1"},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewCheckHostClient(ts.URL, 20*time.Millisecond)
	client.HTTPClient = ts.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	summary, err := client.CheckTCP(ctx, "1.1.1.1:443", []string{"ru2.node.check-host.net", "de1.node.check-host.net"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pollCount != 2 {
		t.Errorf("expected 2 polls to wait for ru2, got %d", pollCount)
	}
	if !summary.RUAvailable {
		t.Errorf("expected RUAvailable=true after waiting for poll 2, got false")
	}
	if !summary.WorldAvailable {
		t.Errorf("expected WorldAvailable=true, got false")
	}
}

func TestCheckHostClient_RateLimiter(t *testing.T) {
	limiter := newRateLimiter(5.0, 1) // 5 per sec = 1 token per 200ms, burst 1
	ctx := context.Background()

	// 1st request should be immediate (burst = 1)
	start := time.Now()
	if err := limiter.Wait(ctx); err != nil {
		t.Fatalf("first wait failed: %v", err)
	}

	// 2nd request should wait ~200ms
	if err := limiter.Wait(ctx); err != nil {
		t.Fatalf("second wait failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 150*time.Millisecond {
		t.Errorf("expected at least 150ms elapsed for rate limited call, got %v", elapsed)
	}

	// Canceled context should fail
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.Wait(cancelCtx); err == nil {
		t.Errorf("expected error on canceled context, got nil")
	}
}

// TestCheckHostClient_DroppedRequestedNodeCompletesFromAccepted pins that the
// poll-completion loop tracks ACCEPTED nodes (initResp.Nodes), not the
// requested list: check-host.net silently drops unknown/retired requested
// nodes, and a dropped node's key never appears in check-result, which must
// not stall polling until the context deadline.
func TestCheckHostClient_DroppedRequestedNodeCompletesFromAccepted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			// Request asked for de1 + zz9, but only de1 was accepted.
			resp := map[string]interface{}{
				"ok":             1,
				"request_id":     "drop1234",
				"permanent_link": "https://check-host.net/check-report/drop1234",
				"nodes": map[string]interface{}{
					"de1.node.check-host.net": []interface{}{"de", "Germany", "Nuremberg", "142.132.174.167", "AS24940"},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/check-result/drop1234") {
			// zz9 never appears here; de1 is already finished on first poll.
			resp := map[string]interface{}{
				"de1.node.check-host.net": []interface{}{
					map[string]interface{}{"time": 0.03, "address": "1.1.1.1"},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewCheckHostClient(ts.URL, 20*time.Millisecond)
	client.HTTPClient = ts.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	summary, err := client.CheckTCP(ctx, "1.1.1.1:443", []string{"de1.node.check-host.net", "zz9.node.check-host.net"})
	if err != nil {
		t.Fatalf("expected completion from accepted nodes despite dropped zz9, got error: %v", err)
	}
	if summary == nil {
		t.Fatal("expected non-nil summary, got nil")
	}
	if len(summary.Results) != 1 {
		t.Fatalf("expected only the accepted node de1 in results, got %d: %+v", len(summary.Results), summary.Results)
	}
	if !summary.WorldAvailable {
		t.Errorf("expected WorldAvailable=true from accepted de1 result, got false")
	}
}

// TestFormatCheckHostReport_GBUnderEurope pins that the check-host country
// code "gb" (e.g. uk1.node.check-host.net reports gb) is grouped under the
// Europe section instead of falling into the "Азия / Другие" default bucket.
func TestFormatCheckHostReport_GBUnderEurope(t *testing.T) {
	summary := &CheckHostSummary{
		Host: "example.com:443",
		Results: []CheckHostNodeResult{
			{Node: "uk1.node.check-host.net", Country: "gb", City: "London", Success: true, Latency: 30 * time.Millisecond},
		},
		WorldAvailable: true,
	}

	report := FormatCheckHostReport(summary)
	if !strings.Contains(report, "Европа") {
		t.Errorf("expected gb (London) grouped under Европа, got: %s", report)
	}
	if strings.Contains(report, "Азия / Другие") {
		t.Errorf("did not expect gb to fall into Азия / Другие bucket, got: %s", report)
	}
}

// TestFormatCheckHostReport_EscapesNodeData pins that external API data
// (City, error statusText) is html-escaped before being interpolated into the
// HTML-parse-mode Telegram message.
func TestFormatCheckHostReport_EscapesNodeData(t *testing.T) {
	summary := &CheckHostSummary{
		Host: "example.com:443",
		Results: []CheckHostNodeResult{
			{Node: "de1.node.check-host.net", Country: "de", City: "A<b>&c", Success: false, Error: "err<&>"},
		},
		Verdict: "Безопасный текст вывода",
	}

	report := FormatCheckHostReport(summary)
	if !strings.Contains(report, "A&lt;b&gt;&amp;c") {
		t.Errorf("expected escaped city A&lt;b&gt;&amp;c, got: %s", report)
	}
	if !strings.Contains(report, "err&lt;&amp;&gt;") {
		t.Errorf("expected escaped status text err&lt;&amp;&gt;, got: %s", report)
	}
	if strings.Contains(report, "<b>&c") {
		t.Errorf("did not expect raw unescaped API data in HTML report, got: %s", report)
	}
}

// TestFormatCheckHostReport_PingLabel pins that ping-kind summaries
// (CheckKind == "ping") render a "(PING)" header instead of the hardcoded
// "(TCP)" label.
func TestFormatCheckHostReport_PingLabel(t *testing.T) {
	summary := &CheckHostSummary{
		Host:      "1.1.1.1",
		CheckKind: "ping",
		Results: []CheckHostNodeResult{
			{Node: "de1.node.check-host.net", Country: "de", City: "Frankfurt", Success: true, Latency: 25 * time.Millisecond},
		},
		WorldAvailable: true,
	}

	report := FormatCheckHostReport(summary)
	if !strings.Contains(report, "(PING)") {
		t.Errorf("expected (PING) header label for ping-kind summary, got: %s", report)
	}
	if strings.Contains(report, "(TCP)") {
		t.Errorf("did not expect (TCP) label for ping-kind summary, got: %s", report)
	}
}
