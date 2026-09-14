package subscription

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// URLMeta tracks freshness statistics for a subscription URL.
type URLMeta struct {
	LastUpdate time.Time `json:"last_update"`
	Count      int       `json:"count"`
	PrevCount  int       `json:"prev_count"`
	Added      int       `json:"added"`
	Removed    int       `json:"removed"`
}

// URLStore tracks the full set of subscription URLs the checker fetches
// from: a fixed "static" set configured via --subscription-url / the
// SUBSCRIPTION_URL env var, plus a "dynamic" set added at runtime (currently
// only through the Telegram bot's /addsub command). The dynamic set is
// persisted to disk so it survives a restart.
type URLStore struct {
	mu   sync.RWMutex
	path string

	static  []string
	dynamic []string
	meta    map[string]URLMeta
}

// NewURLStore creates a store seeded with staticURLs and loads any
// previously persisted dynamic URLs from path. An empty path disables
// persistence: Add and Remove still work, but added URLs are lost on
// restart.
func NewURLStore(staticURLs []string, path string) (*URLStore, error) {
	s := &URLStore{
		path:   path,
		static: append([]string(nil), staticURLs...),
		meta:   make(map[string]URLMeta),
	}

	if path == "" {
		return s, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("reading subscription store %s: %w", path, err)
	}

	var dynamic []string
	if err := json.Unmarshal(data, &dynamic); err != nil {
		return nil, fmt.Errorf("parsing subscription store %s: %w", path, err)
	}
	s.dynamic = dynamic
	return s, nil
}

// All returns every subscription URL currently in effect: static URLs first,
// then dynamic ones, in the order they were added.
func (s *URLStore) All() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.static)+len(s.dynamic))
	out = append(out, s.static...)
	out = append(out, s.dynamic...)
	return out
}

// Static returns the URLs configured via CLI flags/env. These cannot be
// removed through Remove.
func (s *URLStore) Static() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.static...)
}

// Dynamic returns the URLs added at runtime (e.g. via /addsub), in the order
// they were added.
func (s *URLStore) Dynamic() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.dynamic...)
}

// RecordUpdate updates the proxy count and delta for a URL.
func (s *URLStore) RecordUpdate(u string, count int, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta == nil {
		s.meta = make(map[string]URLMeta)
	}
	prev, exists := s.meta[u]
	added := 0
	removed := 0
	prevCount := prev.Count
	if exists {
		if count > prev.Count {
			added = count - prev.Count
		} else if count < prev.Count {
			removed = prev.Count - count
		}
	} else {
		prevCount = count
	}
	s.meta[u] = URLMeta{
		LastUpdate: now,
		Count:      count,
		PrevCount:  prevCount,
		Added:      added,
		Removed:    removed,
	}
}

// GetMeta returns freshness info for a URL.
func (s *URLStore) GetMeta(u string) (URLMeta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.meta == nil {
		return URLMeta{}, false
	}
	m, ok := s.meta[u]
	return m, ok
}

// Add validates and appends url to the dynamic set and persists the result.
// It returns added=false (with no error) if the URL is already tracked,
// whether as a static or a dynamic entry.
func (s *URLStore) Add(raw string) (added bool, err error) {
	u, err := normalizeSubscriptionURL(raw)
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.containsLocked(u) {
		return false, nil
	}

	backup := append([]string(nil), s.dynamic...)
	s.dynamic = append(s.dynamic, u)
	if err := s.persistLocked(); err != nil {
		s.dynamic = backup
		return false, err
	}
	return true, nil
}

// Remove drops url from the dynamic set and persists the result. Static
// (CLI/env-configured) URLs cannot be removed this way. It returns
// removed=false (with no error) if url isn't currently a dynamic entry.
func (s *URLStore) Remove(raw string) (removed bool, err error) {
	u := strings.TrimSpace(raw)

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, existing := range s.dynamic {
		if existing == u {
			idx = i
			break
		}
	}
	if idx == -1 {
		return false, nil
	}

	backup := append([]string(nil), s.dynamic...)
	s.dynamic = append(s.dynamic[:idx:idx], s.dynamic[idx+1:]...)
	if err := s.persistLocked(); err != nil {
		s.dynamic = backup
		return false, err
	}
	return true, nil
}

func (s *URLStore) containsLocked(u string) bool {
	for _, existing := range s.static {
		if existing == u {
			return true
		}
	}
	for _, existing := range s.dynamic {
		if existing == u {
			return true
		}
	}
	return false
}

// persistLocked writes the dynamic set to s.path, if persistence is enabled.
// It writes to a temp file and renames it into place so a crash or a
// concurrent read never observes a half-written file. Callers must hold
// s.mu.
func (s *URLStore) persistLocked() error {
	if s.path == "" {
		return nil
	}

	data, err := json.MarshalIndent(s.dynamic, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding subscription store: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing subscription store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("saving subscription store: %w", err)
	}
	return nil
}

func normalizeSubscriptionURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", fmt.Errorf("empty URL")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "", fmt.Errorf("URL must start with http:// or https://")
	}
	return u, nil
}
