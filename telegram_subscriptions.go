package main

import (
	"fmt"

	"xray-checker/subscription"
)

// telegramSubscriptionManager adapts a subscription.URLStore plus the
// reload-and-apply closure built in main() into telegram.SubscriptionManager,
// so /addsub and /delsub go through the exact same path (and the same
// reloadMu) as the periodic subscription updater — there is only ever one
// place that rebuilds the Xray config and restarts the runner.
type telegramSubscriptionManager struct {
	store  *subscription.URLStore
	reload func() (changed bool, proxyCount int, err error)
}

func (m *telegramSubscriptionManager) Static() []string  { return m.store.Static() }
func (m *telegramSubscriptionManager) Dynamic() []string { return m.store.Dynamic() }

// AddSubscription validates that url actually serves a working subscription
// before persisting it. ReadFromMultipleSources tolerates individual source
// failures once there is more than one URL configured, so without this
// up-front check a broken new URL would silently vanish into the mix on
// reload instead of being reported back to whoever tried to add it.
func (m *telegramSubscriptionManager) AddSubscription(url string) (int, error) {
	configs, _, err := subscription.ReadFromSource(url)
	if err != nil {
		return 0, fmt.Errorf("не удалось загрузить подписку: %w", err)
	}
	if len(configs) == 0 {
		return 0, fmt.Errorf("подписка не содержит ни одного прокси")
	}

	added, err := m.store.Add(url)
	if err != nil {
		return 0, fmt.Errorf("не удалось сохранить подписку: %w", err)
	}
	if !added {
		return 0, fmt.Errorf("эта подписка уже добавлена")
	}

	_, count, err := m.reload()
	if err != nil {
		// Don't leave a subscription persisted that we couldn't actually
		// apply — otherwise it would keep failing on every future reload
		// with no way to remove it short of editing the store file by hand.
		_, _ = m.store.Remove(url)
		return 0, fmt.Errorf("подписка сохранена, но не удалось применить: %w", err)
	}
	return count, nil
}

func (m *telegramSubscriptionManager) RemoveSubscription(url string) (bool, int, error) {
	removed, err := m.store.Remove(url)
	if err != nil {
		return false, 0, fmt.Errorf("не удалось удалить подписку: %w", err)
	}
	if !removed {
		return false, 0, nil
	}

	_, count, err := m.reload()
	if err != nil {
		return true, 0, fmt.Errorf("подписка удалена, но не удалось применить: %w", err)
	}
	return true, count, nil
}
