package nodes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"xray-checker/checker"
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
	fired := make(chan struct{}, 1)
	reg.SetOnUpdate(func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	postReport(t, reg.HandleReport(), "t1", ReportPayload{Version: "1", CheckIntervalSec: 60})
	select {
	case <-fired:
	case <-time.After(500 * time.Millisecond):
		t.Error("onUpdate did not fire")
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

func TestRegistry_RegisterAndRemoveNode(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := tmpDir + "/nodes.json"
	nodesStore, err := NewNodesStore(storePath)
	if err != nil {
		t.Fatalf("failed to create nodes store: %v", err)
	}

	reg := NewRegistry(nil, nil, nil)
	reg.SetNodesStore(nodesStore)

	// Register a new node dynamically
	if err := reg.RegisterNode(NodeConfig{Name: "dyn-1", Token: "tok-1"}); err != nil {
		t.Fatalf("failed to register node: %v", err)
	}

	if !reg.NodeExists("dyn-1") {
		t.Errorf("dyn-1 must exist after RegisterNode")
	}

	// Should reject duplicate name or token
	if err := reg.RegisterNode(NodeConfig{Name: "dyn-1", Token: "tok-diff"}); err == nil {
		t.Errorf("expected error registering duplicate node name")
	}
	if err := reg.RegisterNode(NodeConfig{Name: "dyn-2", Token: "tok-1"}); err == nil {
		t.Errorf("expected error registering duplicate token")
	}

	// Verify persistence
	reg2 := NewRegistry(nil, nil, nil)
	reg2.SetNodesStore(nodesStore)
	if !reg2.NodeExists("dyn-1") {
		t.Errorf("dyn-1 must be loaded by SetNodesStore")
	}

	// Remove node
	if err := reg.RemoveNode("dyn-1"); err != nil {
		t.Fatalf("failed to remove node: %v", err)
	}
	if reg.NodeExists("dyn-1") {
		t.Errorf("dyn-1 must not exist after RemoveNode")
	}
}

func TestRegistry_NodeSnapshot(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	if snap := reg.NodeSnapshot("n1"); snap != nil {
		t.Fatalf("expected nil snapshot before any report, got %v", snap)
	}
	if snap := reg.NodeSnapshot("nonexistent"); snap != nil {
		t.Fatalf("expected nil snapshot for nonexistent node, got %v", snap)
	}

	h := reg.HandleReport()
	postReport(t, h, "t1", ReportPayload{
		Version: "1.0", CheckIntervalSec: 60, HostIP: "1.2.3.4",
		Proxies: []ReportProxy{{StableID: "p1", Name: "Proxy1", Address: "1.1.1.1:443", Protocol: "vless", Online: true, LatencyMs: 50}},
	})

	snap := reg.NodeSnapshot("n1")
	if len(snap) != 1 {
		t.Fatalf("expected 1 metric in node snapshot, got %d", len(snap))
	}
	if snap[0].Name != "Proxy1" || snap[0].NodeName != "n1" || !snap[0].Online {
		t.Errorf("unexpected metric in snapshot: %+v", snap[0])
	}

	// Verify mutating returned slice does not mutate internal snapshot
	snap[0].Name = "Mutated"
	snap2 := reg.NodeSnapshot("n1")
	if snap2[0].Name != "Proxy1" {
		t.Errorf("NodeSnapshot did not return an isolated copy")
	}
}

func TestRegistry_NodeDiagReports(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	if reports := reg.NodeDiagReports("n1"); reports != nil {
		t.Fatalf("expected nil diag reports before report, got %v", reports)
	}

	h := reg.HandleReport()
	postReport(t, h, "t1", ReportPayload{
		Version: "1.0", CheckIntervalSec: 60, HostIP: "1.2.3.4",
		Proxies: []ReportProxy{
			{
				StableID:  "p1",
				Name:      "Proxy1",
				Address:   "1.1.1.1:443",
				Protocol:  "vless",
				Online:    true,
				LatencyMs: 50,
				Verdict:   "Полностью исправен",
				NodeHealth: &checker.NodeHealth{
					ResolvedIP: "1.1.1.1",
					TCPPing:    25 * time.Millisecond,
				},
				Targets: []checker.TargetDiagResult{
					{URL: "https://cp.cloudflare.com/generate_204", Success: true, StatusCode: 204, Latency: 50 * time.Millisecond},
				},
			},
		},
	})

	reports := reg.NodeDiagReports("n1")
	if len(reports) != 1 {
		t.Fatalf("expected 1 diag report, got %d", len(reports))
	}
	rep := reports[0]
	if rep.ProxyName != "Proxy1" || rep.Verdict != "Полностью исправен" {
		t.Errorf("bad diag report: %+v", rep)
	}
	if rep.NodeHealth.ResolvedIP != "1.1.1.1" || rep.NodeHealth.TCPPing != 25*time.Millisecond {
		t.Errorf("bad node health: %+v", rep.NodeHealth)
	}
	if len(rep.Targets) != 1 || rep.Targets[0].StatusCode != 204 {
		t.Errorf("bad targets: %+v", rep.Targets)
	}

	// Verify mutating returned slice does not mutate internal slice
	reports[0].ProxyName = "Mutated"
	reports2 := reg.NodeDiagReports("n1")
	if reports2[0].ProxyName != "Proxy1" {
		t.Errorf("NodeDiagReports did not return an isolated copy")
	}
}

func TestHandleReport_AsyncOnUpdate(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	updateStarted := make(chan struct{})
	finishUpdate := make(chan struct{})

	reg.SetOnUpdate(func() {
		close(updateStarted)
		<-finishUpdate
	})

	h := reg.HandleReport()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- postReport(t, h, "t1", ReportPayload{
			Version:          "1.0",
			CheckIntervalSec: 60,
		})
	}()

	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", rec.Code)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("HandleReport blocked on synchronous onUpdate callback")
	}

	<-updateStarted
	close(finishUpdate)
}

func TestHandleReport_ConfigSourceReceivesNodeName(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	var gotNodeName string
	reg.SetConfigSource(func(name string) *NodeConfigSync {
		gotNodeName = name
		return &NodeConfigSync{
			SyncEnabled:     true,
			DisabledProxies: []string{"test-proxy"},
		}
	})

	h := reg.HandleReport()
	rec := postReport(t, h, "t1", ReportPayload{
		Version:          "1.0",
		CheckIntervalSec: 60,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if gotNodeName != "n1" {
		t.Fatalf("want nodeName 'n1', got %q", gotNodeName)
	}

	var resp IngestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ConfigSync == nil || len(resp.ConfigSync.DisabledProxies) != 1 || resp.ConfigSync.DisabledProxies[0] != "test-proxy" {
		t.Fatalf("unexpected ConfigSync response: %+v", resp.ConfigSync)
	}
}

func TestHandleReport_DisabledProxiesStrippedPerNode(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	allDisabled := []string{"n1/proxy-a", "n2/proxy-b", "legacy-proxy"}

	reg.SetConfigSource(func(nodeName string) *NodeConfigSync {
		prefix := nodeName + "/"
		var disabled []string
		for _, id := range allDisabled {
			if strings.HasPrefix(id, prefix) {
				disabled = append(disabled, strings.TrimPrefix(id, prefix))
			} else if !strings.Contains(id, "/") {
				disabled = append(disabled, id)
			}
		}
		return &NodeConfigSync{
			SyncEnabled:     true,
			DisabledProxies: disabled,
		}
	})

	h := reg.HandleReport()
	rec1 := postReport(t, h, "t1", ReportPayload{CheckIntervalSec: 60})
	var resp1 IngestResponse
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)

	if len(resp1.ConfigSync.DisabledProxies) != 2 ||
		resp1.ConfigSync.DisabledProxies[0] != "proxy-a" ||
		resp1.ConfigSync.DisabledProxies[1] != "legacy-proxy" {
		t.Fatalf("unexpected n1 disabled proxies: %v", resp1.ConfigSync.DisabledProxies)
	}

	rec2 := postReport(t, h, "t2", ReportPayload{CheckIntervalSec: 60})
	var resp2 IngestResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)

	if len(resp2.ConfigSync.DisabledProxies) != 2 ||
		resp2.ConfigSync.DisabledProxies[0] != "proxy-b" ||
		resp2.ConfigSync.DisabledProxies[1] != "legacy-proxy" {
		t.Fatalf("unexpected n2 disabled proxies: %v", resp2.ConfigSync.DisabledProxies)
	}
}

func TestMergedSnapshotConcurrentWithHandleReport(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	reg.mu.Lock()
	st := reg.nodes["n1"]
	st.status = statusDown
	st.gracePending = true
	st.snapshot = []metrics.ProxyMetric{{StableID: "n1/p1", Online: false}}
	reg.mu.Unlock()

	h := reg.HandleReport()
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = postReport(t, h, "t1", ReportPayload{
					Version:          "1.0",
					CheckIntervalSec: 60,
					Proxies:          []ReportProxy{{StableID: "p1", Online: true}},
				})
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = reg.MergedSnapshot(nil)
		}
		close(stop)
	}()

	wg.Wait()
}

func TestSweepStale_RespectsMasterStaleCap(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	reg.SetStaleCap(60 * time.Second)

	reg.mu.Lock()
	st := reg.nodes["n1"]
	st.status = statusUp
	st.intervalSec = 3600
	st.lastReport = time.Now().Add(-5 * time.Minute)
	reg.mu.Unlock()

	transitioned := reg.SweepStale(time.Now())
	if len(transitioned) != 1 || transitioned[0] != "n1" {
		t.Fatalf("expected n1 to transition to down with stale cap, got %v", transitioned)
	}

	snap := reg.HealthSnapshot()
	var n1Up bool
	for _, nh := range snap {
		if nh.Name == "n1" {
			n1Up = nh.Up
		}
	}
	if n1Up {
		t.Fatalf("expected n1 to be down in HealthSnapshot")
	}
}




