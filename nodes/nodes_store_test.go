package nodes

import (
	"path/filepath"
	"testing"
)

func TestNodesStore_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nodes.json")

	store, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	if len(store.All()) != 0 {
		t.Fatalf("expected empty store initially, got %d", len(store.All()))
	}

	// Add nodes
	if err := store.Save("node-1", "token-1"); err != nil {
		t.Fatalf("failed to save node-1: %v", err)
	}
	if err := store.Save("node-2", "token-2"); err != nil {
		t.Fatalf("failed to save node-2: %v", err)
	}

	all := store.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(all))
	}

	// Reload from disk to verify persistence
	store2, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("failed to reload store: %v", err)
	}
	all2 := store2.All()
	if len(all2) != 2 {
		t.Fatalf("expected 2 nodes after reload, got %d", len(all2))
	}

	// Delete
	if err := store2.Delete("node-1"); err != nil {
		t.Fatalf("failed to delete node-1: %v", err)
	}
	if len(store2.All()) != 1 {
		t.Fatalf("expected 1 node after delete, got %d", len(store2.All()))
	}
}
