// Package nodes implements remote checker nodes: a node-side reporter that
// pushes check snapshots to a master, and the master-side registry that
// ingests them (identity, health, merged metrics) and hands out the desired
// managed-subscription list.
package nodes

import (
	"fmt"
	"strings"
)

// NodeConfig is one entry of the master's NODES list: a reporting node's
// name and its bearer token.
type NodeConfig struct {
	Name  string
	Token string
}

// ParseNodes parses "name|token" entries. It rejects empty names/tokens,
// extra pipe-separated parts, and duplicate names.
func ParseNodes(raw []string) ([]NodeConfig, error) {
	seen := make(map[string]bool, len(raw))
	out := make([]NodeConfig, 0, len(raw))
	for i, entry := range raw {
		parts := strings.Split(strings.TrimSpace(entry), "|")
		if len(parts) != 2 {
			return nil, fmt.Errorf("NODES entry %d (%q): want exactly 'name|token'", i+1, entry)
		}
		name := strings.TrimSpace(parts[0])
		token := strings.TrimSpace(parts[1])
		if name == "" || token == "" {
			return nil, fmt.Errorf("NODES entry %d (%q): name and token must be non-empty", i+1, entry)
		}
		if seen[name] {
			return nil, fmt.Errorf("NODES entry %d: duplicate node name %q", i+1, name)
		}
		seen[name] = true
		out = append(out, NodeConfig{Name: name, Token: token})
	}
	return out, nil
}
