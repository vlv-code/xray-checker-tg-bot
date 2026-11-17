package nodes

import (
	"path/filepath"
	"testing"
	"time"

	"xray-checker/metrics"
)

// G5b RED-first: a disabled node must be excluded from MergedSnapshot (no
// metrics → no proxy alerts) while staying visible in HealthSnapshot with
// Enabled=false; re-enabling restores it without a restart.
func TestRegistry_SetNodeEnabled_ExcludesFromMergedSnapshot(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()
	postReport(t, h, "t1", ReportPayload{Version: "1", CheckIntervalSec: 60,
		Proxies: []ReportProxy{{StableID: "a", Online: true}}})
	postReport(t, h, "t2", ReportPayload{Version: "1", CheckIntervalSec: 60,
		Proxies: []ReportProxy{{StableID: "b", Online: true}}})

	local := []metrics.ProxyMetric{{StableID: "local-a"}}
	if got := len(reg.MergedSnapshot(local)); got != 3 {
		t.Fatalf("precondition: local+2 nodes, got %d", got)
	}

	if err := reg.SetNodeEnabled("n1", false); err != nil {
		t.Fatalf("SetNodeEnabled: %v", err)
	}

	// Disabled: n1's proxies vanish from the merged snapshot...
	merged := reg.MergedSnapshot(local)
	if len(merged) != 2 {
		t.Fatalf("disabled node must contribute nothing to MergedSnapshot, got %d: %v", len(merged), merged)
	}
	for _, m := range merged {
		if m.NodeName == "n1" {
			t.Fatalf("disabled node n1 leaked into MergedSnapshot: %+v", m)
		}
	}

	// ...but stays visible in HealthSnapshot with Enabled=false.
	hs := reg.HealthSnapshot()
	var n1, n2 *NodeHealth
	for i := range hs {
		switch hs[i].Name {
		case "n1":
			n1 = &hs[i]
		case "n2":
			n2 = &hs[i]
		}
	}
	if n1 == nil {
		t.Fatal("disabled node must remain in HealthSnapshot")
	}
	if n1.Enabled {
		t.Fatal("n1 must expose Enabled=false in HealthSnapshot")
	}
	if n2 == nil || !n2.Enabled {
		t.Fatal("n2 must expose Enabled=true in HealthSnapshot")
	}

	// Re-enable: back in the merged snapshot without a restart.
	if err := reg.SetNodeEnabled("n1", true); err != nil {
		t.Fatalf("SetNodeEnabled(true): %v", err)
	}
	if got := reg.MergedSnapshot(local); len(got) != 3 {
		t.Fatalf("re-enabled node must contribute again, got %d: %v", len(got), got)
	}
	if err := reg.SetNodeEnabled("nope", false); err == nil {
		t.Fatal("unknown node must be rejected")
	}
}

// G5b: static NODES nodes toggle runtime-only — the toggle works, but no
// store record may materialize for a config-owned node.
func TestRegistry_SetNodeEnabled_StaticRuntimeOnly(t *testing.T) {
	nodesStore, err := NewNodesStore(filepath.Join(t.TempDir(), "nodes.json"))
	if err != nil {
		t.Fatalf("NewNodesStore: %v", err)
	}
	reg := NewRegistry([]NodeConfig{{Name: "n1", Token: "t1"}}, nil, nil)
	reg.SetNodesStore(nodesStore)

	if err := reg.SetNodeEnabled("n1", false); err != nil {
		t.Fatalf("static node toggle must work runtime-only, got: %v", err)
	}
	if len(nodesStore.All()) != 0 {
		t.Fatalf("static node toggle must not persist a store record, got %+v", nodesStore.All())
	}
	if got := reg.MergedSnapshot(nil); len(got) != 0 {
		t.Fatalf("disabled static node must be excluded, got %v", got)
	}
	for _, h := range reg.HealthSnapshot() {
		if h.Name == "n1" && h.Enabled {
			t.Fatal("static node must expose Enabled=false after runtime toggle")
		}
	}
	if err := reg.SetNodeEnabled("n1", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if got := reg.MergedSnapshot(nil); len(got) != 0 {
		_ = got // no report yet — merged stays empty; HealthSnapshot is the assertion
	}
	var enabled bool
	for _, h := range reg.HealthSnapshot() {
		if h.Name == "n1" {
			enabled = h.Enabled
		}
	}
	if !enabled {
		t.Fatal("static node must be enabled again after re-enable")
	}
}

// G5b: for dynamic nodes the enabled state persists — a rebuilt registry must
// load the node already disabled (no metrics until re-enabled).
func TestRegistry_SetNodeEnabled_PersistsAcrossRebuild(t *testing.T) {
	nodesStore, err := NewNodesStore(filepath.Join(t.TempDir(), "nodes.json"))
	if err != nil {
		t.Fatalf("NewNodesStore: %v", err)
	}
	reg := NewRegistry(nil, nil, nil)
	reg.SetNodesStore(nodesStore)
	if err := reg.RegisterNode(NodeConfig{Name: "dyn-1", Token: "tok-1"}); err != nil {
		t.Fatalf("RegisterNode: %v", err)
	}
	if err := reg.SetNodeEnabled("dyn-1", false); err != nil {
		t.Fatalf("SetNodeEnabled: %v", err)
	}

	reg2 := NewRegistry(nil, nil, nil)
	reg2.SetNodesStore(nodesStore)
	hs := reg2.HealthSnapshot()
	if len(hs) != 1 || hs[0].Name != "dyn-1" || hs[0].Enabled {
		t.Fatalf("rebuilt registry must load the node disabled, got %+v", hs)
	}
	if err := reg2.SetNodeEnabled("dyn-1", true); err != nil {
		t.Fatalf("re-enable on rebuilt registry: %v", err)
	}
	all := nodesStore.All()
	if len(all) != 1 || all[0].Enabled == nil || !*all[0].Enabled {
		t.Fatalf("re-enable must persist, got %+v", all)
	}
}

// G5b: SweepStale is unaffected by the enabled flag — a disabled node that
// stops reporting still transitions to down (its absence from MergedSnapshot
// is what suppresses alerts, not the status machine).
func TestRegistry_SetNodeEnabled_SweepStaleUnaffected(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	postReport(t, reg.HandleReport(), "t1", ReportPayload{Version: "1", CheckIntervalSec: 60})
	if err := reg.SetNodeEnabled("n1", false); err != nil {
		t.Fatalf("SetNodeEnabled: %v", err)
	}
	if trans := reg.SweepStale(time.Now().Add(5 * time.Minute)); len(trans) != 1 || trans[0] != "n1" {
		t.Fatalf("disabled node must still sweep to down, got %v", trans)
	}
}
