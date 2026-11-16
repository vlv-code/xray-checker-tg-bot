package nodes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodeSubsStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_subs.json")
	s, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.Add("node-1", "https://sub.example/one")
	if err != nil || !added {
		t.Fatalf("Add: added=%v err=%v", added, err)
	}
	if again, _ := s.Add("node-1", "https://sub.example/one"); again {
		t.Error("duplicate Add must return false")
	}
	if _, err := s.Add("node-1", "ftp://bad"); err == nil {
		t.Error("non-http URL must be rejected")
	}
	s.Add("node-1", "https://sub.example/two")
	s.Add("node-2", "https://sub.example/other")

	got := s.ManagedSubsFor("node-1")
	if len(got) != 2 || got[0] != "https://sub.example/one" || got[1] != "https://sub.example/two" {
		t.Errorf("unexpected order/content: %v", got)
	}

	removed, _ := s.Remove("node-1", "https://sub.example/one")
	if !removed || len(s.Get("node-1")) != 1 {
		t.Errorf("Remove failed: removed=%v get=%v", removed, s.Get("node-1"))
	}
	if r, _ := s.Remove("node-1", "https://not-there"); r {
		t.Error("removing unknown URL must return false")
	}
	if r, _ := s.Remove("unknown-node", "https://sub.example/one"); r {
		t.Error("removing on unknown node must return false")
	}

	// Persistence: a fresh store over the same file sees the same state.
	s2, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if g := s2.ManagedSubsFor("node-2"); len(g) != 1 || g[0] != "https://sub.example/other" {
		t.Errorf("state lost after reopen: %v", g)
	}
}

func TestNodeSubsStoreMissingFile(t *testing.T) {
	if _, err := NewNodeSubsStore(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("missing file must not be an error: %v", err)
	}
}

// The first save must succeed even when the store's parent directory does
// not exist yet (fresh volume, moved data dir) — persistLocked creates it.
func TestNodeSubsStoreSaveCreatesMissingDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "node_subs.json")
	s, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("node-1", "https://sub.example/one"); err != nil {
		t.Fatalf("first save into a missing directory must succeed: %v", err)
	}
	if got := s.ManagedSubsFor("node-1"); len(got) != 1 || got[0] != "https://sub.example/one" {
		t.Fatalf("state lost after save into fresh dir: %v", got)
	}
}

// Repeated saves must keep working and leave no leftover tmp file behind
// (tmp is renamed onto the store path on every successful persist).
func TestNodeSubsStoreRepeatedSavesNoTmpLeftover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node_subs.json")
	s, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("node-1", "https://sub.example/one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("node-1", "https://sub.example/two"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file must be renamed away after saves, stat err: %v", err)
	}
	s2, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get("node-1"); len(got) != 2 {
		t.Errorf("repeated saves must persist both URLs: %v", got)
	}
}
