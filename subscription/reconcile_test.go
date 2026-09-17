package subscription

import (
	"errors"
	"strings"
	"testing"

	"xray-checker/models"
)

// fakeValidator accepts any URL containing "good", rejects the rest —
// no network involved (the fetcher's SSRF filter blocks loopback, so real
// fetching can't be exercised against httptest servers).
func fakeValidator(raw string) ([]*models.ProxyConfig, string, error) {
	if strings.Contains(raw, "good") {
		return []*models.ProxyConfig{{Name: "x"}}, "sub", nil
	}
	return nil, "", errors.New("no valid proxies")
}

func TestReconcileManaged(t *testing.T) {
	good1 := "https://good1.example/sub"
	good2 := "https://good2.example/sub"
	bad := "https://bad.example/sub"
	malformed := "ftp://not-http.example/sub"

	store, err := NewURLStore(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	reloadCalls := 0
	reload := func() (bool, int, error) { reloadCalls++; return true, 1, nil }

	// Empty desired on an empty store: no-op, no reload.
	if err := ReconcileManaged(store, nil, reload, fakeValidator); err != nil {
		t.Fatal(err)
	}
	if reloadCalls != 0 {
		t.Fatalf("no-op must not reload, got %d calls", reloadCalls)
	}

	// Desired adds two valid, one invalid and one malformed — both skipped
	// with no error.
	if err := ReconcileManaged(store, []string{good1, good2, bad, malformed}, reload, fakeValidator); err != nil {
		t.Fatal(err)
	}
	if got := store.Managed(); len(got) != 2 {
		t.Fatalf("want 2 managed, got %v", got)
	}
	if reloadCalls != 1 {
		t.Fatalf("exactly one reload after adds, got %d", reloadCalls)
	}

	// Shrink desired to one — the other is removed.
	if err := ReconcileManaged(store, []string{good1}, reload, fakeValidator); err != nil {
		t.Fatal(err)
	}
	if got := store.Managed(); len(got) != 1 || got[0] != good1 {
		t.Fatalf("after shrink: %v", got)
	}

	// Same desired again: no reload.
	before := reloadCalls
	if err := ReconcileManaged(store, []string{good1}, reload, fakeValidator); err != nil {
		t.Fatal(err)
	}
	if reloadCalls != before {
		t.Error("steady state must not reload")
	}
}

func TestReconcileManagedReloadFailureReverts(t *testing.T) {
	good := "https://good.example/sub"
	store, _ := NewURLStore(nil, "")
	fail := func() (bool, int, error) { return false, 0, errors.New("boom") }

	if err := ReconcileManaged(store, []string{good}, fail, fakeValidator); err == nil {
		t.Fatal("reload error must propagate")
	}
	if got := store.Managed(); len(got) != 0 {
		t.Fatalf("store must be reverted, got %v", got)
	}
}

func TestReconcileManagedLeavesUnmanagedDynamicAlone(t *testing.T) {
	good := "https://good.example/sub"
	store, _ := NewURLStore(nil, "")
	if _, err := store.Add(good); err != nil {
		t.Fatal(err)
	}
	reloadCalls := 0
	reload := func() (bool, int, error) { reloadCalls++; return true, 1, nil }

	// The URL is already dynamic (bot-added) and satisfies the master's
	// desired state functionally; reconciliation must NOT adopt or remove
	// it (spec §6: bot-added subscriptions stay out of the master's reach).
	if err := ReconcileManaged(store, []string{good}, reload, fakeValidator); err != nil {
		t.Fatal(err)
	}
	if got := store.Managed(); len(got) != 0 {
		t.Fatalf("bot-added entry must stay unmanaged, got %v", got)
	}
	if got := store.All(); len(got) != 1 || got[0] != good {
		t.Fatalf("bot-added entry must survive reconciliation, got %v", got)
	}
	if reloadCalls != 0 {
		t.Errorf("no-op reconciliation must not reload, got %d", reloadCalls)
	}
}
