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

func TestNodesStore_RejectsEmptyTokenHashRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	// Corrupted record: empty tokenHash must be rejected, not migrated into
	// a fabricated credential for the well-known empty-string hash.
	if err := os.WriteFile(path, []byte(`{"n1": {}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("corrupted record must not fail store init: %v", err)
	}
	if all := store.All(); len(all) != 0 {
		t.Fatalf("node with empty tokenHash must be rejected, got: %+v", all)
	}
	// The empty-string credential must never become usable via reload either.
	store2, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("reload after rejection must succeed: %v", err)
	}
	for _, n := range store2.All() {
		if n.Name == "n1" || n.Token == HashToken("") {
			t.Fatalf("rejected node reappeared or empty-string hash fabricated: %+v", n)
		}
	}
}

func TestNodesStore_MigrationPersistFailureIsNonFatal(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nodes.json")
	if err := os.WriteFile(path, []byte(`{"legacy-node": "my-plain-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Occupy the atomic-write tmp path with a directory so persistLocked
	// fails deterministically on every platform.
	if err := os.Mkdir(path+".tmp", 0755); err != nil {
		t.Fatal(err)
	}
	store, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("migration persist failure must not fail store init: %v", err)
	}
	all := store.All()
	if len(all) != 1 || all[0].Name != "legacy-node" || all[0].Token != HashToken("my-plain-token") {
		t.Fatalf("migration must apply in-memory even when persist fails, got: %+v", all)
	}
	// Persist failed, so the legacy plaintext remains on disk; the store must
	// not pretend the migration was saved (loud warning is expected).
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "my-plain-token") {
		t.Fatalf("expected unchanged legacy file after failed persist, got: %s", content)
	}
}

// Behavior pins for corrupted store files (test-suite hole). Unparseable
// content must fail closed: NewNodesStore returns an error, and main.go then
// disables the store with a loud warning (graceful degradation, no partial
// adoption). An empty file is tolerated as a fresh empty store.
func TestNodesStore_CorruptedFilesFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr bool
	}{
		{"empty file starts fresh", "", false},
		{"truncated json fails closed", `{"n1":`, true},
		{"garbage bytes fail closed", "\x00\x01\x02not-json", true},
		{"top-level array fails closed", `["array"]`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nodes.json")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := NewNodesStore(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("corrupted store content %q must fail closed (main.go then disables the store with a warning), got nil error", tc.content)
				}
				return
			}
			if err != nil {
				t.Fatalf("empty file must start a fresh store, got: %v", err)
			}
			if all := store.All(); len(all) != 0 {
				t.Fatalf("fresh store over an empty file must be empty, got: %+v", all)
			}
		})
	}
}

// Pin: an object whose values are plain strings is NOT wrong-type garbage —
// it is exactly the legacy name→plaintext-token schema, so the store adopts
// it through the legacy migration path with the token hashed at rest. A
// stricter "error on any non-record value" rule would break plaintext legacy
// migration (see TestNodesStore_MigrateLegacyPlaintext).
func TestNodesStore_ObjectWithStringValuesLoadsAsLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	if err := os.WriteFile(path, []byte(`{"a":"string"}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewNodesStore(path)
	if err != nil {
		t.Fatalf("legacy name→token schema must load: %v", err)
	}
	all := store.All()
	if len(all) != 1 || all[0].Name != "a" {
		t.Fatalf("legacy record must be adopted, got: %+v", all)
	}
	if all[0].Token != HashToken("string") {
		t.Fatalf("legacy plaintext token must be hashed on load, got: %q", all[0].Token)
	}
}
