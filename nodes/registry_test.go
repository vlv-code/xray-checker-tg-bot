package nodes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"xray-checker/metrics"
)

type fakeSubs struct{ data map[string][]string }

func (f *fakeSubs) ManagedSubsFor(node string) []string { return f.data[node] }

func newTestRegistry(asn LookupFunc) (*Registry, *fakeSubs) {
	subs := &fakeSubs{data: map[string][]string{"n1": {"https://sub.example/one"}}}
	return NewRegistry([]NodeConfig{{Name: "n1", Token: "t1"}, {Name: "n2", Token: "t2"}}, subs, asn), subs
}

func postReport(t *testing.T, h http.HandlerFunc, token string, p ReportPayload) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(p)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/report", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestHandleReportAuthAndHappyPath(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()

	for _, token := range []string{"wrong", "t2-or-nothing"} {
		if rec := postReport(t, h, token, ReportPayload{}); rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: want 401, got %d", token, rec.Code)
		}
	}
	if rec := postReport(t, h, "", ReportPayload{}); rec.Code != http.StatusUnauthorized {
		t.Error("missing header must 401")
	}

	rec := postReport(t, h, "t1", ReportPayload{
		Version: "1.0", CheckIntervalSec: 300, HostIP: "203.0.113.7",
		Proxies: []ReportProxy{{StableID: "a", Name: "p1", Online: true, SubName: "s1"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp IngestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.ManagedSubs) != 1 || resp.ManagedSubs[0] != "https://sub.example/one" {
		t.Errorf("managedSubs: %v", resp.ManagedSubs)
	}

	hs := reg.HealthSnapshot()
	if len(hs) != 2 || !hs[0].Up || !hs[0].EverReported || hs[0].Online != 1 || hs[0].Total != 1 {
		t.Fatalf("health: %+v", hs)
	}
	if hs[1].Up || hs[1].EverReported {
		t.Errorf("n2 must stay pending: %+v", hs[1])
	}
}

func TestHandleReportBodyLimitAndBadJSON(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()

	big := make([]byte, MaxReportBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/report", bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer t1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: want 400, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/nodes/report", bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer t1")
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: want 400, got %d", rec.Code)
	}
}

func TestHandleReportMethodNotAllowed(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/report", nil)
	rec := httptest.NewRecorder()
	reg.HandleReport()(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: want 405, got %d", rec.Code)
	}
}

func TestMergedSnapshotNamespacingAndGrace(t *testing.T) {
	reg, _ := newTestRegistry(func(ip string) string { return "AS9009 M247" })
	h := reg.HandleReport()

	postReport(t, h, "t1", ReportPayload{
		Version: "1.0", CheckIntervalSec: 100, HostIP: "203.0.113.7",
		Proxies: []ReportProxy{{StableID: "a", Name: "p1", Online: true}},
	})
	postReport(t, h, "t2", ReportPayload{
		Version: "1.0", CheckIntervalSec: 100, HostIP: "203.0.113.8",
		Proxies: []ReportProxy{{StableID: "a", Name: "p1", Online: true}},
	})

	local := []metrics.ProxyMetric{{StableID: "a", Name: "p1"}}
	merged := reg.MergedSnapshot(local)
	if len(merged) != 3 {
		t.Fatalf("want local+2 nodes, got %d", len(merged))
	}
	ids := map[string]bool{}
	asnSeen := false
	for _, m := range merged {
		ids[m.StableID] = true
		if m.NodeName == "n1" && m.NodeASN == "AS9009 M247" {
			asnSeen = true
		}
	}
	if !ids["a"] || !ids["n1/a"] || !ids["n2/a"] {
		t.Errorf("namespacing broken: %v", ids)
	}
	if !asnSeen {
		t.Error("ASN from LookupFunc must be attached to n1 metrics")
	}

	// Deadline: 2*100s + 60s = 260s. At +261s both nodes go down; the grace
	// snapshot emits their last state once with Disabled=true.
	if trans := reg.SweepStale(time.Now().Add(261 * time.Second)); len(trans) != 2 {
		t.Fatalf("both nodes stale, transitions = %v", trans)
	}
	grace := reg.MergedSnapshot(local)
	sawDisabled := 0
	for _, m := range grace {
		if m.NodeName == "n1" && m.Disabled {
			sawDisabled++
		}
	}
	if sawDisabled != 1 {
		t.Fatalf("grace snapshot must mark n1 proxies Disabled once, got %d", sawDisabled)
	}
	after := reg.MergedSnapshot(local)
	for _, m := range after {
		if m.NodeName == "n1" {
			t.Error("n1 must disappear from merged after grace")
		}
	}
}

func TestSweepStalePendingNeverTransitions(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	if trans := reg.SweepStale(time.Now().Add(24 * time.Hour)); len(trans) != 0 {
		t.Errorf("pending nodes must never transition: %v", trans)
	}
}

func TestNodeRecoversOnReport(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()
	postReport(t, h, "t1", ReportPayload{Version: "1", CheckIntervalSec: 10, Proxies: []ReportProxy{{StableID: "x", Online: true}}})
	reg.SweepStale(time.Now().Add(5 * time.Minute)) // down
	if hs := reg.HealthSnapshot(); hs[0].Up {
		t.Fatal("node must be down after sweep")
	}
	postReport(t, h, "t1", ReportPayload{Version: "1", CheckIntervalSec: 10, Proxies: []ReportProxy{{StableID: "x", Online: true}}})
	hs := reg.HealthSnapshot()
	if !hs[0].Up {
		t.Error("node must be up after a fresh report")
	}
}

func TestSubCounts(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	postReport(t, reg.HandleReport(), "t1", ReportPayload{
		Version: "1", CheckIntervalSec: 60,
		Proxies: []ReportProxy{
			{StableID: "a", SubName: "s1", Online: true},
			{StableID: "b", SubName: "s1", Online: false},
			{StableID: "c", SubName: "s2", Online: true},
		},
	})
	sc := reg.SubCounts("n1")
	if sc["s1"] != 2 || sc["s2"] != 1 {
		t.Errorf("SubCounts: %v", sc)
	}
	if sc := reg.SubCounts("unknown"); len(sc) != 0 {
		t.Errorf("unknown node: %v", sc)
	}
}

func TestOnUpdateFires(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	var mu sync.Mutex
	calls := 0
	reg.SetOnUpdate(func() { mu.Lock(); calls++; mu.Unlock() })
	postReport(t, reg.HandleReport(), "t1", ReportPayload{Version: "1", CheckIntervalSec: 60})
	if calls != 1 {
		t.Errorf("onUpdate calls = %d", calls)
	}
}

func TestNodeExists(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	if !reg.NodeExists("n1") || reg.NodeExists("nope") {
		t.Error("NodeExists broken")
	}
}

func TestConcurrentUse(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(ReportPayload{Version: "1", CheckIntervalSec: 60,
				Proxies: []ReportProxy{{StableID: "a", Online: true}}})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/report", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer t1")
			rec := httptest.NewRecorder()
			h(rec, req)
			reg.MergedSnapshot(nil)
			reg.HealthSnapshot()
			reg.SubCounts("n1")
		}(i)
	}
	wg.Wait()
}
