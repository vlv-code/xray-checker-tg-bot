package nodes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"xray-checker/checker"
)

func TestAuditor_RunOnceCollectsResults(t *testing.T) {
	// Fake Check-Host API: mirrors checker/checkhost_test.go fixtures, but
	// answers for every node the client requested (the client waits until
	// all requested nodes have non-nil results).
	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/check-tcp") {
			requested = r.URL.Query()["node"]
			nodes := map[string]interface{}{}
			for _, n := range requested {
				if strings.HasPrefix(n, "ru") {
					nodes[n] = []interface{}{"ru", "Russia", "Moscow", "194.26.229.20", "AS210644"}
				} else {
					nodes[n] = []interface{}{"de", "Germany", "Nuremberg", "142.132.174.167", "AS24940"}
				}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":             1,
				"request_id":     "test1234",
				"permanent_link": "https://check-host.net/check-report/test1234",
				"nodes":          nodes,
			})
			return
		}
		// check-result: every requested node answers successfully.
		res := map[string]interface{}{}
		for _, n := range requested {
			res[n] = []interface{}{map[string]interface{}{"time": 0.05, "address": "1.2.3.4"}}
		}
		json.NewEncoder(w).Encode(res)
	}))
	defer srv.Close()

	client := checker.NewCheckHostClient(srv.URL, 5*time.Millisecond)
	a := NewAuditor(client, func() []AuditTarget {
		return []AuditTarget{{Address: "h1.example:443", Host: "h1.example", Name: "p1"}}
	})
	defer a.Stop()

	a.runOnceForTest()

	res := a.Results()
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %+v", res)
	}
	if res["h1.example:443"].RequestID != "test1234" {
		t.Fatalf("summary not parsed: %+v", res["h1.example:443"])
	}
	if !res["h1.example:443"].RUAvailable {
		t.Fatal("all nodes answered successfully, RU must be available")
	}
}

func TestAuditor_SetScheduleGating(t *testing.T) {
	a := NewAuditor(nil, func() []AuditTarget { return nil })
	defer a.Stop()

	// Disabled by default: due() must be false.
	if a.dueForTest() {
		t.Fatal("auditor must not be due while disabled")
	}
	a.SetSchedule(true, 0) // hours<=0 falls back to 1h
	if a.dueForTest() {
		t.Fatal("freshly enabled auditor must wait ~2 minutes for the first run")
	}
	a.mu.Lock()
	a.lastRun = time.Now().Add(-2 * time.Hour)
	a.mu.Unlock()
	if !a.dueForTest() {
		t.Fatal("auditor must be due after the interval elapsed")
	}
}
