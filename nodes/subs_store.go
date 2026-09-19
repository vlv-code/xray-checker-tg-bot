package nodes

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// NodeSubsSource supplies the desired managed-subscription list for a node.
// Implemented by NodeSubsStore; consumed by the ingest handler.
type NodeSubsSource interface {
	ManagedSubsFor(node string) []string
}

// NodeSubsStore persists the desired managed subscriptions per node
// ({"node-a": ["url"]}), edited via the master bot's /nodeaddsub commands.
type NodeSubsStore struct {
	mu   sync.RWMutex
	path string
	data map[string][]string
}

// NewNodeSubsStore loads the persisted map from path; a missing file starts
// empty.
func NewNodeSubsStore(path string) (*NodeSubsStore, error) {
	s := &NodeSubsStore{path: path, data: make(map[string][]string)}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("reading node subscriptions store %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		return nil, fmt.Errorf("parsing node subscriptions store %s: %w", path, err)
	}
	return s, nil
}

// Get returns a copy of node's desired URLs, in insertion order.
func (s *NodeSubsStore) Get(node string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.data[node]...)
}

// ManagedSubsFor implements NodeSubsSource.
func (s *NodeSubsStore) ManagedSubsFor(node string) []string {
	return s.Get(node)
}

// Add appends url to node's desired list (http/https only) and persists.
// Returns false if it was already present.
func (s *NodeSubsStore) Add(node, raw string) (bool, error) {
	u := strings.TrimSpace(raw)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return false, fmt.Errorf("URL must start with http:// or https://")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.data[node] {
		if existing == u {
			return false, nil
		}
	}
	backup := append([]string(nil), s.data[node]...)
	s.data[node] = append(s.data[node], u)
	if err := s.persistLocked(); err != nil {
		s.data[node] = backup
		return false, err
	}
	return true, nil
}

// Remove drops url from node's desired list and persists. Returns false if
// the node or URL is unknown.
func (s *NodeSubsStore) Remove(node, raw string) (bool, error) {
	u := strings.TrimSpace(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.data[node]
	for i, existing := range list {
		if existing == u {
			backup := append([]string(nil), list...)
			s.data[node] = append(list[:i:i], list[i+1:]...)
			if err := s.persistLocked(); err != nil {
				s.data[node] = backup
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// persistLocked atomically writes the map. Callers hold s.mu.
func (s *NodeSubsStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding node subscriptions store: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing node subscriptions store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("saving node subscriptions store: %w", err)
	}
	return nil
}
