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

func TestNodesStore_SettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	store, err := NewNodesStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("node-1", "tok"); err != nil {
		t.Fatal(err)
	}

	interval := 120
	conc := 4
	ns := &NodeSettings{
		CheckIntervalSec: &interval,
		CheckConcurrency: &conc,
		TargetURLs:       []string{"https://t1.example/204"},
	}
	if err := store.SetSettings("node-1", ns); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	got, ok := store.Settings("node-1")
	if !ok || got.CheckIntervalSec == nil || *got.CheckIntervalSec != 120 ||
		got.CheckConcurrency == nil || *got.CheckConcurrency != 4 ||
		len(got.TargetURLs) != 1 {
		t.Fatalf("settings lost: %+v", got)
	}

	// Persistence across reload.
	store2, err := NewNodesStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got2, ok := store2.Settings("node-1")
	if !ok || got2.CheckIntervalSec == nil || *got2.CheckIntervalSec != 120 {
		t.Fatalf("settings lost after reload: %+v", got2)
	}

	// Saving the same node again must keep its settings.
	if err := store2.Save("node-1", "new-token"); err != nil {
		t.Fatal(err)
	}
	if got3, _ := store2.Settings("node-1"); got3 == nil || got3.CheckIntervalSec == nil {
		t.Fatal("Save must preserve existing settings")
	}

	// Clearing settings.
	if err := store2.SetSettings("node-1", nil); err != nil {
		t.Fatal(err)
	}
	if got4, _ := store2.Settings("node-1"); got4 != nil {
		t.Fatalf("expected cleared settings, got %+v", got4)
	}

	// Unknown node.
	if err := store2.SetSettings("nope", ns); err == nil {
		t.Fatal("SetSettings on unknown node must error")
	}
	if _, ok := store2.Settings("nope"); ok {
		t.Fatal("Settings on unknown node must report false")
	}

	// Deleting the node removes its settings.
	if err := store2.Save("node-2", "t2"); err != nil {
		t.Fatal(err)
	}
	if err := store2.SetSettings("node-2", ns); err != nil {
		t.Fatal(err)
	}
	if err := store2.Delete("node-2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store2.Settings("node-2"); ok {
		t.Fatal("Delete must remove settings too")
	}
}

func TestNodesStore_LegacyHashedFormatLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	legacy := `{"node-a": "sha256:deadbeef"}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("legacy hashed format must load: %v", err)
	}
	all := store.All()
	if len(all) != 1 || all[0].Name != "node-a" || all[0].Token != "deadbeef" {
		t.Fatalf("unexpected nodes: %+v", all)
	}
	if got, _ := store.Settings("node-a"); got != nil {
		t.Fatalf("legacy entries must have no settings: %+v", got)
	}
}
