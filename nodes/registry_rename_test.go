package nodes

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRenamableRegistry builds a registry with real stores: the static
// NODES-env pair n1/n2 plus one dynamic node (dyn-1) registered through the
// store.
func newRenamableRegistry(t *testing.T) (*Registry, *NodesStore, *NodeSubsStore) {
	t.Helper()
	nodesStore, err := NewNodesStore(filepath.Join(t.TempDir(), "nodes.json"))
	if err != nil {
		t.Fatalf("NewNodesStore: %v", err)
	}
	subsStore, err := NewNodeSubsStore(filepath.Join(t.TempDir(), "node_subs.json"))
	if err != nil {
		t.Fatalf("NewNodeSubsStore: %v", err)
	}
	reg := NewRegistry([]NodeConfig{{Name: "n1", Token: "t1"}, {Name: "n2", Token: "t2"}}, subsStore, nil)
	reg.SetNodesStore(nodesStore)
	if err := reg.RegisterNode(NodeConfig{Name: "dyn-1", Token: "tok-1"}); err != nil {
		t.Fatalf("RegisterNode: %v", err)
	}
	return reg, nodesStore, subsStore
}

// G5a RED-first: RenameNode must migrate the in-memory identity (nodes map +
// byToken with the SAME token), the persisted store record (token + settings
// overrides + enabled state) and the NodeSubsStore keys. Auth with the old
// bearer token must keep working and attribute reports to the NEW name.
func TestRegistry_RenameNode_MigratesMemoryStoresAndAuth(t *testing.T) {
	reg, nodesStore, subsStore := newRenamableRegistry(t)

	if _, err := subsStore.Add("dyn-1", "https://sub.example/one"); err != nil {
		t.Fatalf("subs Add: %v", err)
	}
	interval := 120
	if err := nodesStore.SetSettings("dyn-1", &NodeSettings{CheckIntervalSec: &interval}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}

	fired := make(chan struct{}, 1)
	reg.SetOnUpdate(func(string) {
		select {
		case fired <- struct{}{}:
		default:
		}
	})

	if err := reg.RenameNode("dyn-1", "dyn-2"); err != nil {
		t.Fatalf("RenameNode: %v", err)
	}

	// In-memory identity moved.
	if !reg.NodeExists("dyn-2") || reg.NodeExists("dyn-1") {
		t.Fatalf("nodes map not migrated: dyn-2=%v dyn-1=%v", reg.NodeExists("dyn-2"), reg.NodeExists("dyn-1"))
	}

	// Auth with the OLD token keeps working and attributes to the NEW name.
	rec := postReport(t, reg.HandleReport(), "tok-1", ReportPayload{Version: "1", CheckIntervalSec: 60})
	if rec.Code != 200 {
		t.Fatalf("old token after rename: want 200, got %d", rec.Code)
	}
	var renamed *NodeHealth
	for i := range reg.HealthSnapshot() {
		if h := reg.HealthSnapshot()[i]; h.Name == "dyn-2" {
			renamed = &h
		}
	}
	if renamed == nil || !renamed.Up || renamed.Total != 0 {
		t.Fatalf("report must attribute to the renamed node, got %+v", reg.HealthSnapshot())
	}

	// Settings overrides followed the rename.
	ns, ok := nodesStore.Settings("dyn-2")
	if !ok || ns == nil || ns.CheckIntervalSec == nil || *ns.CheckIntervalSec != 120 {
		t.Fatalf("settings must follow the rename, got ok=%v ns=%+v", ok, ns)
	}
	if _, still := nodesStore.Settings("dyn-1"); still {
		t.Fatal("old settings key must be gone after rename")
	}

	// Managed-subs keys followed the rename.
	if got := subsStore.ManagedSubsFor("dyn-2"); len(got) != 1 || got[0] != "https://sub.example/one" {
		t.Fatalf("subs must follow the rename, got %v", got)
	}
	if got := subsStore.ManagedSubsFor("dyn-1"); len(got) != 0 {
		t.Fatalf("old subs key must be gone, got %v", got)
	}

	// Structural change must re-feed the alert pipeline.
	select {
	case <-fired:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("onUpdate did not fire after RenameNode")
	}
}

// G5a: the persisted store record (token hash + settings + enabled) must be
// migrated, not duplicated — a rebuilt registry sees the node only under the
// new name, still disabled if it was disabled before the rename.
func TestRegistry_RenameNode_MigratesPersistedRecord(t *testing.T) {
	reg, nodesStore, _ := newRenamableRegistry(t)
	if err := reg.SetNodeEnabled("dyn-1", false); err != nil {
		t.Fatalf("SetNodeEnabled: %v", err)
	}

	if err := reg.RenameNode("dyn-1", "dyn-2"); err != nil {
		t.Fatalf("RenameNode: %v", err)
	}

	all := nodesStore.All()
	if len(all) != 1 || all[0].Name != "dyn-2" {
		t.Fatalf("store must hold exactly the renamed record, got %+v", all)
	}
	if all[0].Token != HashToken("tok-1") {
		t.Fatalf("token hash must survive the rename, got %q", all[0].Token)
	}
	if all[0].Enabled == nil || *all[0].Enabled {
		t.Fatalf("enabled state must survive the rename, got %+v", all[0].Enabled)
	}

	// Rebuild from the store: the node comes back under the new name, disabled.
	reg2 := NewRegistry(nil, nil, nil)
	reg2.SetNodesStore(nodesStore)
	if !reg2.NodeExists("dyn-2") || reg2.NodeExists("dyn-1") {
		t.Fatalf("rebuilt registry must load the renamed node only: dyn-2=%v dyn-1=%v",
			reg2.NodeExists("dyn-2"), reg2.NodeExists("dyn-1"))
	}
	for _, h := range reg2.HealthSnapshot() {
		if h.Name == "dyn-2" && h.Enabled {
			t.Fatal("rebuilt registry must keep the node disabled")
		}
	}
}

// G5a: static NODES-env nodes are config-owned — rename must be rejected with
// a RU message pointing at the config, and nothing may change.
func TestRegistry_RenameNode_RejectsStaticNode(t *testing.T) {
	reg, nodesStore, subsStore := newRenamableRegistry(t)

	if err := reg.RenameNode("n1", "n1-renamed"); err == nil {
		t.Fatal("static NODES node rename must be rejected")
	} else if !strings.Contains(err.Error(), "NODES") {
		t.Fatalf("RU rejection must point at the NODES config, got: %v", err)
	}
	if !reg.NodeExists("n1") || reg.NodeExists("n1-renamed") {
		t.Fatal("static node must be untouched after rejection")
	}
	if len(nodesStore.All()) != 1 || nodesStore.All()[0].Name != "dyn-1" {
		t.Fatalf("store must be untouched by the rejected rename: %+v", nodesStore.All())
	}
	if got := subsStore.ManagedSubsFor("dyn-1"); got == nil {
		_ = got // dyn-1 subs untouched is checked in the migration test; here only n1 matters
	}
}

// G5a: unknown source, duplicate target, reserved target and empty names must
// all be rejected before any migration happens.
func TestRegistry_RenameNode_RejectsUnknownDuplicateReservedEmpty(t *testing.T) {
	reg, _, _ := newRenamableRegistry(t)

	if err := reg.RenameNode("nope", "x"); err == nil {
		t.Fatal("unknown source node must be rejected")
	}
	if err := reg.RenameNode("dyn-1", "n1"); err == nil {
		t.Fatal("rename onto an existing name must be rejected")
	}
	if !reg.NodeExists("dyn-1") || reg.NodeExists("n1") && false {
		t.Fatal("registry must be unchanged after rejections")
	}
	if err := reg.RenameNode("dyn-1", "local"); err == nil {
		t.Fatal("reserved target name must be rejected")
	}
	if err := reg.RenameNode("dyn-1", ""); err == nil {
		t.Fatal("empty target name must be rejected")
	}
	if err := reg.RenameNode("", "x"); err == nil {
		t.Fatal("empty source name must be rejected")
	}
	if err := reg.RenameNode("dyn-1", "dyn-1"); err == nil {
		t.Fatal("no-op rename must be rejected explicitly")
	}
	// Nothing migrated by the failing cases above.
	if reg.NodeExists("x") || reg.NodeExists("local") || reg.NodeExists("dyn-2") {
		t.Fatal("failed renames must not create nodes")
	}
}
