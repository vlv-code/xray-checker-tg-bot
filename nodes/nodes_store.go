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

// NodeSettings holds per-node overrides of the master's check settings.
// A nil field means "inherit from the master"; a set field overrides it for
// this node only.
type NodeSettings struct {
	CheckIntervalSec      *int     `json:"checkIntervalSec,omitempty"`
	CheckMethod           *string  `json:"checkMethod,omitempty"`
	IpCheckURL            *string  `json:"ipCheckUrl,omitempty"`
	StatusCheckURL        *string  `json:"statusCheckUrl,omitempty"`
	DownloadURL           *string  `json:"downloadUrl,omitempty"`
	ProxyTimeoutSec       *int     `json:"proxyTimeoutSec,omitempty"`
	DownloadTimeoutSec    *int     `json:"downloadTimeoutSec,omitempty"`
	DownloadMinSize       *int64   `json:"downloadMinSize,omitempty"`
	CheckConcurrency      *int     `json:"checkConcurrency,omitempty"`
	SubsUpdateIntervalSec *int     `json:"subsUpdateIntervalSec,omitempty"`
	TargetURLs            []string `json:"targetUrls,omitempty"`
}

// clone returns a deep copy so callers can't mutate stored state through
// shared slices.
func (n *NodeSettings) clone() *NodeSettings {
	if n == nil {
		return nil
	}
	out := *n
	if n.TargetURLs != nil {
		out.TargetURLs = append([]string(nil), n.TargetURLs...)
	}
	return &out
}

// nodeRecord is the persisted per-node entry: hashed token plus optional
// settings overrides.
type nodeRecord struct {
	TokenHash string        `json:"tokenHash"`
	Settings  *NodeSettings `json:"settings,omitempty"`
}

// NodesStore persists dynamic node registrations with tokens hashed at-rest
// and optional per-node settings overrides.
type NodesStore struct {
	mu   sync.RWMutex
	path string
	data map[string]*nodeRecord
}

// NewNodesStore initializes a NodesStore loading existing registrations if path exists.
// Transparently migrates legacy formats: a flat name→token map (plaintext or
// pre-hashed) is converted to the nodeRecord schema; plaintext tokens are
// hashed at rest.
func NewNodesStore(path string) (*NodesStore, error) {
	s := &NodesStore{
		path: path,
		data: make(map[string]*nodeRecord),
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
		// Legacy format: a flat map of name → token (hashed or plaintext).
		var legacy map[string]string
		if lerr := json.Unmarshal(bytes, &legacy); lerr != nil {
			return nil, fmt.Errorf("parsing nodes store %s: %w", path, err)
		}
		for name, val := range legacy {
			s.data[name] = &nodeRecord{TokenHash: val}
		}
	}

	// Migration: any token not prefixed with "sha256:" is legacy plaintext.
	// Hash it and re-persist to disk.
	migrated := false
	for _, rec := range s.data {
		if !strings.HasPrefix(rec.TokenHash, tokenHashPrefix) {
			rec.TokenHash = tokenHashPrefix + HashToken(rec.TokenHash)
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
	for name, rec := range s.data {
		out = append(out, NodeConfig{Name: name, Token: strings.TrimPrefix(rec.TokenHash, tokenHashPrefix)})
	}
	return out
}

// Settings returns a copy of the node's settings overrides; ok is false for
// unknown nodes or nodes without overrides.
func (s *NodesStore) Settings(name string) (*NodeSettings, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.data[name]
	if !ok {
		return nil, false
	}
	return rec.Settings.clone(), rec.Settings != nil
}

// SetSettings replaces (or clears, when ns is nil) the node's settings
// overrides and persists. Unknown nodes are rejected.
func (s *NodesStore) SetSettings(name string, ns *NodeSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[name]
	if !ok {
		return fmt.Errorf("unknown node %q", name)
	}
	old := rec.Settings
	rec.Settings = ns.clone()
	if err := s.persistLocked(); err != nil {
		rec.Settings = old
		return err
	}
	return nil
}

// Save stores or updates a node with hashed token and persists to disk.
// Existing settings overrides are preserved.
func (s *NodesStore) Save(name, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := tokenHashPrefix + HashToken(token)
	rec, hadOld := s.data[name]
	var oldHash string
	if hadOld {
		oldHash = rec.TokenHash
		rec.TokenHash = hash
	} else {
		rec = &nodeRecord{TokenHash: hash}
		s.data[name] = rec
	}
	if err := s.persistLocked(); err != nil {
		if hadOld {
			rec.TokenHash = oldHash
		} else {
			delete(s.data, name)
		}
		return err
	}
	return nil
}

// Delete removes a node (and its settings) and persists to disk.
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
