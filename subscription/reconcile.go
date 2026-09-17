package subscription

import (
	"fmt"
	"sort"

	"xray-checker/logger"
	"xray-checker/models"
)

// SubscriptionValidator checks that a URL serves a working subscription,
// returning its parsed configs and display name. It is the /addsub
// validation path; injectable so tests can run without network.
type SubscriptionValidator func(raw string) ([]*models.ProxyConfig, string, error)

// DefaultSubscriptionValidator validates through the same parser/fetcher
// path /addsub uses (including its SSRF protection).
func DefaultSubscriptionValidator(raw string) ([]*models.ProxyConfig, string, error) {
	return ReadFromSource(raw)
}

// ReconcileManaged aligns the store's managed dynamic subscriptions with the
// desired list received from the master. New URLs are validated before the
// store is touched; a failing reload reverts the store. Invalid desired URLs
// are skipped with a warning (the master's desired state stays authoritative).
func ReconcileManaged(store *URLStore, desired []string, reload func() (changed bool, proxyCount int, err error), validate SubscriptionValidator) error {
	desiredSet := make(map[string]bool, len(desired))
	for _, raw := range desired {
		u, err := normalizeSubscriptionURL(raw)
		if err != nil {
			logger.Warn("Reconcile: skipping invalid desired URL %q: %v", raw, err)
			continue
		}
		desiredSet[u] = true
	}

	existing := make(map[string]bool)
	for _, u := range store.All() {
		existing[u] = true
	}
	var toAdd []string
	for u := range desiredSet {
		if !existing[u] {
			toAdd = append(toAdd, u)
		}
	}
	sort.Strings(toAdd)

	var toRemove []string
	for _, u := range store.Managed() {
		if !desiredSet[u] {
			toRemove = append(toRemove, u)
		}
	}

	var added, removed []string
	for _, u := range toAdd {
		configs, _, err := validate(u)
		if err != nil || len(configs) == 0 {
			logger.Warn("Reconcile: desired subscription %s failed validation, skipping: %v", u, err)
			continue
		}
		ok, err := store.AddManaged(u)
		if err != nil {
			return fmt.Errorf("adding managed subscription %s: %w", u, err)
		}
		if ok {
			added = append(added, u)
		}
	}
	for _, u := range toRemove {
		ok, err := store.RemoveManaged(u)
		if err != nil {
			return fmt.Errorf("removing managed subscription %s: %w", u, err)
		}
		if ok {
			removed = append(removed, u)
		}
	}

	if len(added) == 0 && len(removed) == 0 {
		return nil
	}

	if _, _, err := reload(); err != nil {
		for _, u := range added {
			_, _ = store.RemoveManaged(u)
		}
		for _, u := range removed {
			_, _ = store.AddManaged(u)
		}
		return fmt.Errorf("managed subscriptions changed but reload failed (reverted): %w", err)
	}
	logger.Info("Managed subscriptions reconciled: +%d -%d", len(added), len(removed))
	return nil
}
