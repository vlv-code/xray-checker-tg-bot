package main

import (
	"fmt"
	"path/filepath"
	"testing"

	"xray-checker/models"
	"xray-checker/subscription"
)

func TestTelegramSubscriptionManager(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "subscriptions.json")

	validURL := "https://valid.example.com/sub"
	store, err := subscription.NewURLStore([]string{"https://static.example.com/sub"}, storePath)
	if err != nil {
		t.Fatalf("NewURLStore failed: %v", err)
	}

	reloadCount := 0
	reloadFunc := func() (bool, int, error) {
		reloadCount++
		return true, 10, nil
	}

	mockValidator := func(u string) ([]*models.ProxyConfig, string, error) {
		if u == validURL {
			return []*models.ProxyConfig{{Name: "test-proxy"}}, "sub", nil
		}
		if u == "https://empty.example.com/sub" {
			return []*models.ProxyConfig{}, "sub", nil
		}
		return nil, "", fmt.Errorf("network error")
	}

	mgr := &telegramSubscriptionManager{
		store:    store,
		reload:   reloadFunc,
		validate: mockValidator,
	}

	if static := mgr.Static(); len(static) != 1 || static[0] != "https://static.example.com/sub" {
		t.Errorf("expected static subscription, got %v", static)
	}
	if dynamic := mgr.Dynamic(); len(dynamic) != 0 {
		t.Errorf("expected empty dynamic subscriptions, got %v", dynamic)
	}

	// 1. Add invalid URL returning error
	if _, err := mgr.AddSubscription("https://invalid.example.com/sub"); err == nil {
		t.Errorf("expected error adding invalid URL")
	}

	// 2. Add URL returning 0 proxies
	if _, err := mgr.AddSubscription("https://empty.example.com/sub"); err == nil {
		t.Errorf("expected error adding empty subscription")
	}

	// 3. Add valid subscription
	count, err := mgr.AddSubscription(validURL)
	if err != nil {
		t.Fatalf("AddSubscription failed: %v", err)
	}
	if count != 10 {
		t.Errorf("expected count 10, got %d", count)
	}
	if reloadCount != 1 {
		t.Errorf("expected reloadCount=1, got %d", reloadCount)
	}
	if len(mgr.Dynamic()) != 1 {
		t.Errorf("expected 1 dynamic subscription, got %v", mgr.Dynamic())
	}

	// 4. Add duplicate
	if _, err := mgr.AddSubscription(validURL); err == nil {
		t.Errorf("expected error adding duplicate subscription")
	}

	// 5. Remove non-existent
	found, _, err := mgr.RemoveSubscription("https://nonexistent.example.com/sub")
	if err != nil {
		t.Fatalf("RemoveSubscription unexpected error: %v", err)
	}
	if found {
		t.Errorf("expected found=false for non-existent subscription")
	}

	// 6. Remove added subscription
	found, count, err = mgr.RemoveSubscription(validURL)
	if err != nil {
		t.Fatalf("RemoveSubscription failed: %v", err)
	}
	if !found {
		t.Errorf("expected found=true")
	}
	if count != 10 {
		t.Errorf("expected count=10 from reload, got %d", count)
	}
	if len(mgr.Dynamic()) != 0 {
		t.Errorf("expected 0 dynamic subscriptions after removal, got %v", mgr.Dynamic())
	}

	// 7. Test reload failure rollback
	failingReload := func() (bool, int, error) {
		return false, 0, fmt.Errorf("reload failed")
	}
	mgrFail := &telegramSubscriptionManager{
		store:    store,
		reload:   failingReload,
		validate: mockValidator,
	}
	if _, err := mgrFail.AddSubscription(validURL); err == nil {
		t.Errorf("expected error when reload fails")
	}
	// Verify it was rolled back from store
	if len(mgrFail.Dynamic()) != 0 {
		t.Errorf("expected dynamic subscriptions to be rolled back, got %v", mgrFail.Dynamic())
	}
}
