package nodes

import (
	"os"
	"path/filepath"
	"testing"
)

func newRenameStore(t *testing.T) (*NodesStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nodes.json")
	store, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("NewNodesStore: %v", err)
	}
	return store, path
}

// G5a RED-first: the store-level rename migrates the whole record — token
// hash, settings overrides and enabled state — and leaves no old key behind.
// Unknown source is a no-op success (nothing to migrate); an occupied target
// is rejected.
func TestNodesStore_RenameMigratesRecord(t *testing.T) {
	store, path := newRenameStore(t)
	if err := store.Save("a", "tok-a"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	interval := 90
	if err := store.SetSettings("a", &NodeSettings{CheckIntervalSec: &interval}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	if err := store.SetEnabled("a", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	if err := store.Rename("a", "b"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	all := store.All()
	if len(all) != 1 || all[0].Name != "b" || all[0].Token != HashToken("tok-a") {
		t.Fatalf("record must migrate intact: %+v", all)
	}
	if all[0].Enabled == nil || *all[0].Enabled {
		t.Fatalf("enabled state must migrate, got %+v", all[0].Enabled)
	}
	if ns, ok := store.Settings("b"); !ok || ns == nil || ns.CheckIntervalSec == nil || *ns.CheckIntervalSec != 90 {
		t.Fatalf("settings must migrate: ok=%v ns=%+v", ok, ns)
	}
	if _, ok := store.Settings("a"); ok {
		t.Fatal("old record key must be gone")
	}

	// Persistence: a fresh store over the same file sees the migrated record.
	store2, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if all := store2.All(); len(all) != 1 || all[0].Name != "b" {
		t.Fatalf("rename must persist, got %+v", all)
	}

	// Unknown source: nothing to migrate, no error.
	if err := store.Rename("unknown", "x"); err != nil {
		t.Fatalf("unknown source must be a no-op, got: %v", err)
	}

	// Occupied target: rejected.
	if err := store.Save("c", "tok-c"); err != nil {
		t.Fatalf("Save c: %v", err)
	}
	if err := store.Rename("c", "b"); err == nil {
		t.Fatal("rename onto an existing record must be rejected")
	}
	if all := store.All(); len(all) != 2 {
		t.Fatalf("rejected rename must not delete anything: %+v", all)
	}
}

// G5a: a failed persist must leave the old record fully intact (in-memory
// rollback), so the node keeps authenticating after a full-disk rename.
func TestNodesStore_RenameRollsBackOnPersistFailure(t *testing.T) {
	store, path := newRenameStore(t)
	if err := store.Save("a", "tok-a"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	interval := 77
	if err := store.SetSettings("a", &NodeSettings{CheckIntervalSec: &interval}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	// Occupy the atomic-write tmp path with a directory: persistLocked fails
	// deterministically on every platform.
	if err := os.Mkdir(path+".tmp", 0755); err != nil {
		t.Fatal(err)
	}

	if err := store.Rename("a", "b"); err == nil {
		t.Fatal("persist failure must surface as an error")
	}
	all := store.All()
	if len(all) != 1 || all[0].Name != "a" || all[0].Token != HashToken("tok-a") {
		t.Fatalf("rollback must restore the old record: %+v", all)
	}
	if ns, _ := store.Settings("a"); ns == nil || ns.CheckIntervalSec == nil || *ns.CheckIntervalSec != 77 {
		t.Fatalf("rollback must restore settings: %+v", ns)
	}
}

// G5b: SetEnabled persists the toggle; unknown nodes are rejected.
func TestNodesStore_SetEnabled(t *testing.T) {
	store, path := newRenameStore(t)
	if err := store.Save("a", "tok-a"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.SetEnabled("a", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	all := store.All()
	if len(all) != 1 || all[0].Enabled == nil || *all[0].Enabled {
		t.Fatalf("disabled state must persist, got %+v", all)
	}
	store2, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if all := store2.All(); len(all) != 1 || all[0].Enabled == nil || *all[0].Enabled {
		t.Fatalf("enabled state must survive reload, got %+v", all)
	}
	if err := store2.SetEnabled("unknown", true); err == nil {
		t.Fatal("unknown node must be rejected")
	}
	if err := store2.SetEnabled("a", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
}

// G5b migration discipline: legacy records (both the flat name→token schema
// and the pre-enabled nodeRecord schema) must load as ENABLED (nil pointer) —
// a missing enabled field never disables an existing node.
func TestNodesStore_LegacyRecordsDefaultEnabled(t *testing.T) {
	dir := t.TempDir()

	// Legacy flat schema (plaintext token).
	flat := filepath.Join(dir, "flat.json")
	if err := os.WriteFile(flat, []byte(`{"legacy": "plain-tok"}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewNodesStore(flat)
	if err != nil {
		t.Fatalf("legacy flat store: %v", err)
	}
	all := store.All()
	if len(all) != 1 || all[0].Enabled != nil {
		t.Fatalf("legacy flat record must default to enabled (nil), got %+v", all)
	}

	// Pre-enabled nodeRecord schema (no enabled key).
	record := filepath.Join(dir, "record.json")
	if err := os.WriteFile(record, []byte(`{"old":{"tokenHash":"sha256:deadbeef"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store2, err := NewNodesStore(record)
	if err != nil {
		t.Fatalf("legacy record store: %v", err)
	}
	all2 := store2.All()
	if len(all2) != 1 || all2[0].Enabled != nil {
		t.Fatalf("legacy nodeRecord must default to enabled (nil), got %+v", all2)
	}
}
