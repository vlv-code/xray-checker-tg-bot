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

