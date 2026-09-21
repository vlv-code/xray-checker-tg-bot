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
	if changed, err := ReconcileManaged(store, nil, reload, fakeValidator); err != nil || changed {
		t.Fatalf("expected not changed and no error, got changed=%v, err=%v", changed, err)
	}
	if reloadCalls != 0 {
		t.Fatalf("no-op must not reload, got %d calls", reloadCalls)
	}

	// Desired adds two valid, one invalid and one malformed — both skipped
	// with no error.
	if changed, err := ReconcileManaged(store, []string{good1, good2, bad, malformed}, reload, fakeValidator); err != nil || !changed {
		t.Fatalf("expected changed=true, got changed=%v, err=%v", changed, err)
	}
	if got := store.Managed(); len(got) != 2 {
		t.Fatalf("want 2 managed, got %v", got)
	}
	if reloadCalls != 1 {
		t.Fatalf("exactly one reload after adds, got %d", reloadCalls)
	}

	// Shrink desired to one — the other is removed.
	if changed, err := ReconcileManaged(store, []string{good1}, reload, fakeValidator); err != nil || !changed {
		t.Fatalf("after shrink: expected changed=true, got changed=%v, err=%v", changed, err)
	}
	if got := store.Managed(); len(got) != 1 || got[0] != good1 {
		t.Fatalf("after shrink: %v", got)
	}

	// Same desired again: no reload, changed=false.
	before := reloadCalls
	if changed, err := ReconcileManaged(store, []string{good1}, reload, fakeValidator); err != nil || changed {
		t.Fatalf("steady state must not change, got changed=%v, err=%v", changed, err)
	}
	if reloadCalls != before {
		t.Error("steady state must not reload")
	}
}

func TestReconcileManagedReloadFailureReverts(t *testing.T) {
	good := "https://good.example/sub"
	store, _ := NewURLStore(nil, "")
	fail := func() (bool, int, error) { return false, 0, errors.New("boom") }

	if _, err := ReconcileManaged(store, []string{good}, fail, fakeValidator); err == nil {
		t.Fatal("reload error must propagate")
	}
	if got := store.Managed(); len(got) != 0 {
		t.Fatalf("store must be reverted, got %v", got)
	}
}

func TestReconcileManagedReloadFailurePreservesUnmanagedDynamic(t *testing.T) {
	userSub := "https://user.example/sub"
	managedSub := "https://good-managed.example/sub"
	store, _ := NewURLStore(nil, "")
	if _, err := store.Add(userSub); err != nil {
		t.Fatal(err)
	}

	fail := func() (bool, int, error) { return false, 0, errors.New("reload failed") }

	_, err := ReconcileManaged(store, []string{managedSub}, fail, fakeValidator)
	if err == nil {
		t.Fatal("expected reload error")
	}

	if got := store.Dynamic(); len(got) != 1 || got[0] != userSub {
		t.Fatalf("expected userSub to survive reload failure, got %v", got)
	}
	if got := store.Managed(); len(got) != 0 {
		t.Fatalf("expected 0 managed subs, got %v", got)
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
	if changed, err := ReconcileManaged(store, []string{good}, reload, fakeValidator); err != nil || changed {
		t.Fatalf("expected changed=false and err=nil, got changed=%v, err=%v", changed, err)
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

func TestReadFromMultipleSourcesDetailed_Empty(t *testing.T) {
	cfgs, counts, err := ReadFromMultipleSourcesDetailed(nil)
	if err != nil {
		t.Fatalf("unexpected error for nil urls: %v", err)
	}
	if len(cfgs) != 0 || len(counts) != 0 {
		t.Fatalf("expected empty configs and counts, got cfgs=%d, counts=%d", len(cfgs), len(counts))
	}

	cfgs, counts, err = ReadFromMultipleSourcesDetailed([]string{})
	if err != nil {
		t.Fatalf("unexpected error for empty urls: %v", err)
	}
	if len(cfgs) != 0 || len(counts) != 0 {
		t.Fatalf("expected empty configs and counts, got cfgs=%d, counts=%d", len(cfgs), len(counts))
	}
}
