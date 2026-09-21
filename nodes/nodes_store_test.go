package nodes

import (
	"os"
	"path/filepath"
	"strings"
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

func TestNodesStore_SaveHashesTokenAtRest(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nodes.json")

	store, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	rawToken := "super-secret-token"
	if err := store.Save("node-secure", rawToken); err != nil {
		t.Fatalf("failed to save node: %v", err)
	}

	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(bytes)
	if strings.Contains(content, rawToken) {
		t.Fatalf("raw token leaked into nodes.json file on disk! content: %s", content)
	}
	if !strings.Contains(content, "sha256:") {
		t.Fatalf("expected sha256: prefix in nodes.json, got %s", content)
	}
}

func TestNodesStore_MigrateLegacyPlaintext(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nodes.json")

	// Write legacy plaintext nodes.json
	if err := os.WriteFile(path, []byte(`{"legacy-node": "my-plain-token"}`), 0600); err != nil {
		t.Fatal(err)
	}

	store, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("failed to open legacy store: %v", err)
	}

	all := store.All()
	if len(all) != 1 || all[0].Name != "legacy-node" {
		t.Fatalf("unexpected nodes: %+v", all)
	}

	expectedHash := HashToken("my-plain-token")
	if all[0].Token != expectedHash {
		t.Fatalf("expected hash %q, got %q", expectedHash, all[0].Token)
	}

	// Verify file was migrated on disk
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(bytes)
	if strings.Contains(content, "my-plain-token") {
		t.Fatalf("plaintext token still present on disk after migration: %s", content)
	}
}

