package subscription

import (
	"os"
	"path/filepath"
	"testing"
)

func TestURLStore_AddAndAll(t *testing.T) {
	store, err := NewURLStore([]string{"https://static.example/sub"}, "")
	if err != nil {
		t.Fatalf("NewURLStore: %v", err)
	}

	added, err := store.Add("https://dynamic.example/sub")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !added {
		t.Fatalf("expected Add to report added=true for a new URL")
	}

	all := store.All()
	want := []string{"https://static.example/sub", "https://dynamic.example/sub"}
	if len(all) != len(want) || all[0] != want[0] || all[1] != want[1] {
		t.Fatalf("All() = %v, want %v", all, want)
	}
}

func TestURLStore_AddRejectsInvalidAndDuplicate(t *testing.T) {
	store, err := NewURLStore([]string{"https://static.example/sub"}, "")
	if err != nil {
		t.Fatalf("NewURLStore: %v", err)
	}

	if _, err := store.Add("not-a-url"); err == nil {
		t.Fatalf("expected Add to reject a URL without http(s):// scheme")
	}

	added, err := store.Add("https://static.example/sub")
	if err != nil {
		t.Fatalf("Add duplicate of a static URL: %v", err)
	}
	if added {
		t.Fatalf("expected Add to report added=false for a URL already in the static set")
	}

	if _, err := store.Add("https://new.example/sub"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	added, err = store.Add("https://new.example/sub")
	if err != nil {
		t.Fatalf("Add duplicate of a dynamic URL: %v", err)
	}
	if added {
		t.Fatalf("expected Add to report added=false for a URL already in the dynamic set")
	}
}

func TestURLStore_Remove(t *testing.T) {
	store, err := NewURLStore([]string{"https://static.example/sub"}, "")
	if err != nil {
		t.Fatalf("NewURLStore: %v", err)
	}
	if _, err := store.Add("https://dynamic.example/sub"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Static URLs can't be removed through the store.
	removed, err := store.Remove("https://static.example/sub")
	if err != nil {
		t.Fatalf("Remove(static): %v", err)
	}
	if removed {
		t.Fatalf("expected Remove to refuse a static URL")
	}
	if len(store.All()) != 2 {
		t.Fatalf("static URL should still be present after a refused Remove")
	}

	removed, err = store.Remove("https://dynamic.example/sub")
	if err != nil {
		t.Fatalf("Remove(dynamic): %v", err)
	}
	if !removed {
		t.Fatalf("expected Remove to report removed=true for a dynamic URL")
	}
	if len(store.Dynamic()) != 0 {
		t.Fatalf("expected the dynamic set to be empty after removal, got %v", store.Dynamic())
	}

	removed, err = store.Remove("https://not-tracked.example/sub")
	if err != nil {
		t.Fatalf("Remove(unknown): %v", err)
	}
	if removed {
		t.Fatalf("expected Remove to report removed=false for an untracked URL")
	}
}

func TestURLStore_PersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.json")

	store, err := NewURLStore(nil, path)
	if err != nil {
		t.Fatalf("NewURLStore: %v", err)
	}
	if _, err := store.Add("https://one.example/sub"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.Add("https://two.example/sub"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected persistence file to exist: %v", err)
	}

	reloaded, err := NewURLStore(nil, path)
	if err != nil {
		t.Fatalf("NewURLStore (reload): %v", err)
	}
	dyn := reloaded.Dynamic()
	if len(dyn) != 2 || dyn[0] != "https://one.example/sub" || dyn[1] != "https://two.example/sub" {
		t.Fatalf("Dynamic() after reload = %v", dyn)
	}

	if _, err := reloaded.Remove("https://one.example/sub"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	reloadedAgain, err := NewURLStore(nil, path)
	if err != nil {
		t.Fatalf("NewURLStore (second reload): %v", err)
	}
	dyn = reloadedAgain.Dynamic()
	if len(dyn) != 1 || dyn[0] != "https://two.example/sub" {
		t.Fatalf("Dynamic() after remove+reload = %v", dyn)
	}
}

func TestURLStore_NewURLStoreMissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	store, err := NewURLStore([]string{"https://static.example/sub"}, path)
	if err != nil {
		t.Fatalf("NewURLStore with a missing store file should succeed, got: %v", err)
	}
	if len(store.Dynamic()) != 0 {
		t.Fatalf("expected no dynamic URLs, got %v", store.Dynamic())
	}
}
