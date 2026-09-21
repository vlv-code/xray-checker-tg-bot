package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const tokenHashPrefix = "sha256:"

// HashToken computes sha256 hex string for a raw bearer token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NodesStore persists dynamic node registrations with tokens hashed at-rest.
type NodesStore struct {
	mu   sync.RWMutex
	path string
	data map[string]string // name -> hash (stored as "sha256:<hex>")
}

// NewNodesStore initializes a NodesStore loading existing registrations if path exists.
// Transparently migrates any legacy plaintext tokens to "sha256:<hex>".
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

	// Migration: any token not prefixed with "sha256:" is legacy plaintext.
	// Hash it and re-persist to disk.
	migrated := false
	for name, val := range s.data {
		if !strings.HasPrefix(val, tokenHashPrefix) {
			s.data[name] = tokenHashPrefix + HashToken(val)
			migrated = true
		}
	}
	if migrated {
		_ = s.persistLocked() // best-effort migration save
	}

	return s, nil
}

// All returns a copy of all configured nodes with normalised token hashes.
func (s *NodesStore) All() []NodeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NodeConfig, 0, len(s.data))
	for name, val := range s.data {
		hash := strings.TrimPrefix(val, tokenHashPrefix)
		out = append(out, NodeConfig{Name: name, Token: hash})
	}
	return out
}

// Save stores or updates a node with hashed token and persists to disk.
func (s *NodesStore) Save(name, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, hadOld := s.data[name]
	s.data[name] = tokenHashPrefix + HashToken(token)
	if err := s.persistLocked(); err != nil {
		if hadOld {
			s.data[name] = old
		} else {
			delete(s.data, name)
		}
		return err
	}
	return nil
}

// Delete removes a node and persists to disk.
func (s *NodesStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, hadOld := s.data[name]
	if !hadOld {
		return nil
	}
	delete(s.data, name)
	if err := s.persistLocked(); err != nil {
		s.data[name] = old
		return err
	}
	return nil
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
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(bytes); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
