package nodes

import (
	"os"
	"path/filepath"
	"testing"
)

// G5a RED-first: NodeSubsStore.Rename migrates a node's desired-subscription
// key, persists, keeps other nodes untouched, treats an unknown source as a
// no-op, and rejects occupied targets.
func TestNodeSubsStore_Rename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_subs.json")
	s, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("a", "https://sub.example/one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("a", "https://sub.example/two"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("b", "https://sub.example/other"); err != nil {
		t.Fatal(err)
	}

	if err := s.Rename("a", "c"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got := s.ManagedSubsFor("c"); len(got) != 2 || got[0] != "https://sub.example/one" || got[1] != "https://sub.example/two" {
		t.Fatalf("subscriptions must follow the rename, got %v", got)
	}
	if got := s.ManagedSubsFor("a"); len(got) != 0 {
		t.Fatalf("old key must be gone, got %v", got)
	}
	if got := s.ManagedSubsFor("b"); len(got) != 1 {
		t.Fatalf("other nodes must stay untouched, got %v", got)
	}

	// Persistence across reopen.
	s2, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.ManagedSubsFor("c"); len(got) != 2 {
		t.Fatalf("rename must persist, got %v", got)
	}

	// Unknown source: no-op success.
	if err := s2.Rename("unknown", "x"); err != nil {
		t.Fatalf("unknown source must be a no-op, got: %v", err)
	}

	// Occupied target: rejected, source intact.
	if err := s2.Rename("b", "c"); err == nil {
		t.Fatal("rename onto an occupied target must be rejected")
	}
	if got := s2.ManagedSubsFor("b"); len(got) != 1 {
		t.Fatalf("source must stay intact after rejection, got %v", got)
	}
}

// G5a: a failed persist must roll the in-memory key back — the node keeps its
// subscriptions after a full-disk rename attempt.
func TestNodeSubsStore_RenameRollsBackOnPersistFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_subs.json")
	s, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("a", "https://sub.example/one"); err != nil {
		t.Fatal(err)
	}
	// Occupy the atomic-write tmp path so persistLocked fails deterministically.
	if err := os.Mkdir(path+".tmp", 0755); err != nil {
		t.Fatal(err)
	}

	if err := s.Rename("a", "b"); err == nil {
		t.Fatal("persist failure must surface as an error")
	}
	if got := s.ManagedSubsFor("a"); len(got) != 1 || got[0] != "https://sub.example/one" {
		t.Fatalf("rollback must restore the old key, got %v", got)
	}
	if got := s.ManagedSubsFor("b"); len(got) != 0 {
		t.Fatalf("rollback must clear the new key, got %v", got)
	}
}
