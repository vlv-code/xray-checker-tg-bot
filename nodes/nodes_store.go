package nodes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// NodesStore persists dynamic node registrations (name + token).
type NodesStore struct {
	mu   sync.RWMutex
	path string
	data map[string]string // name -> token
}

// NewNodesStore initializes a NodesStore loading existing registrations if path exists.
func NewNodesStore(path string) (*NodesStore, error) {
	s := &NodesStore{
		path: path,
		data: make(map[string]string),
	}
	if path == "" {
		return s, nil
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("reading nodes store %s: %w", path, err)
	}
	if len(bytes) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(bytes, &s.data); err != nil {
		return nil, fmt.Errorf("parsing nodes store %s: %w", path, err)
	}
	return s, nil
}

// All returns a copy of all configured nodes.
func (s *NodesStore) All() []NodeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NodeConfig, 0, len(s.data))
	for name, token := range s.data {
		out = append(out, NodeConfig{Name: name, Token: token})
	}
	return out
}

// Save stores or updates a node and persists to disk.
func (s *NodesStore) Save(name, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[name] = token
	return s.persistLocked()
}

// Delete removes a node and persists to disk.
func (s *NodesStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, name)
	return s.persistLocked()
}

func (s *NodesStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	bytes, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
