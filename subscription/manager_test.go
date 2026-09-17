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

func TestURLStoreManagedMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	s, err := NewURLStore([]string{"https://static.example/s"}, path)
	if err != nil {
		t.Fatal(err)
	}
	// A bot-style dynamic entry stays unmanaged.
	if _, err := s.Add("https://bot.example/b"); err != nil {
		t.Fatal(err)
	}
	added, err := s.AddManaged("https://master.example/m")
	if err != nil || !added {
		t.Fatalf("AddManaged: %v %v", added, err)
	}

	managed := s.Managed()
	if len(managed) != 1 || managed[0] != "https://master.example/m" {
		t.Fatalf("Managed() = %v", managed)
	}

	// Managing an existing dynamic entry flips its flag instead of duplicating.
	if ok, _ := s.AddManaged("https://bot.example/b"); !ok {
		t.Error("adopting an existing dynamic entry must report true")
	}
	if got := s.Managed(); len(got) != 2 {
		t.Errorf("after adoption Managed() = %v", got)
	}
	if got := s.All(); len(got) != 3 {
		t.Errorf("All() must not duplicate: %v", got)
	}

	// RemoveManaged only touches managed entries.
	if ok, _ := s.RemoveManaged("https://bot.example/b"); !ok {
		t.Error("adopted entry must be removable as managed")
	}
	if ok, _ := s.RemoveManaged("https://static.example/s"); ok {
		t.Error("static entry can never be removed")
	}
	if got := s.All(); len(got) != 2 {
		t.Errorf("All() after removals = %v", got)
	}

	// State survives reopen.
	s2, err := NewURLStore([]string{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Managed(); len(got) != 1 || got[0] != "https://master.example/m" {
		t.Errorf("managed flag lost on reopen: %v", got)
	}
	if ok, _ := s2.Remove("https://master.example/m"); !ok {
		t.Error("plain Remove still drops a managed dynamic entry")
	}
}

func TestURLStoreLegacyFormatMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	legacy := `["https://old.example/one","https://old.example/two"]`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewURLStore(nil, path)
	if err != nil {
		t.Fatalf("legacy file must load: %v", err)
	}
	if got := s.Dynamic(); len(got) != 2 {
		t.Fatalf("legacy dynamic lost: %v", got)
	}
	if got := s.Managed(); len(got) != 0 {
		t.Errorf("legacy entries must be unmanaged: %v", got)
	}
	// Next persist writes the new format and keeps loading.
	if _, err := s.AddManaged("https://new.example/three"); err != nil {
		t.Fatal(err)
	}
	s2, err := NewURLStore(nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Dynamic(); len(got) != 3 {
		t.Errorf("dynamic after reformat: %v", got)
	}
	if got := s2.Managed(); len(got) != 1 || got[0] != "https://new.example/three" {
		t.Errorf("managed after reformat: %v", got)
	}
}
