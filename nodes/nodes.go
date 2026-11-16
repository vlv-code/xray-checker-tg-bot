// Package nodes implements remote checker nodes: a node-side reporter that
// pushes check snapshots to a master, and the master-side registry that
// ingests them (identity, health, merged metrics) and hands out the desired
// managed-subscription list.
package nodes

import (
	"fmt"
	"strings"

	"xray-checker/logger"
)

// reservedNodeNames mirrors telegram's reserved-name list: these names collide
// with static Telegram diagnostic routes (menu:diag:details:<name>,
// menu:diag:refresh:<name>, the target == "local" token in callbacks.go).
// Duplicated deliberately — importing the telegram package from nodes would
// create an import cycle. Wave-F B6.
var reservedNodeNames = map[string]bool{
	"local":   true,
	"rich":    true,
	"summary": true,
	"details": true,
}

// NodeConfig is one entry of the master's NODES list: a reporting node's
// name and its bearer token.
type NodeConfig struct {
	Name  string
	Token string
}

// ParseNodes parses "name|token" entries. It rejects empty names/tokens,
// extra pipe-separated parts, and duplicate names. Reserved names (static
// Telegram diag route collisions, see reservedNodeNames) are skipped with a
// warning instead of failing the whole master startup: NODES is static env
// configuration, so one bad entry must not take the instance down (Wave-F B6).
func ParseNodes(raw []string) ([]NodeConfig, error) {
	seenName := make(map[string]bool, len(raw))
	seenToken := make(map[string]bool, len(raw))
	out := make([]NodeConfig, 0, len(raw))
	for i, entry := range raw {
		parts := strings.Split(strings.TrimSpace(entry), "|")
		// Error text must never echo the full entry: everything after the
		// first '|' is potentially a bearer token, so only the name portion
		// (parts[0]) and the entry index are ever included.
		if len(parts) != 2 {
			return nil, fmt.Errorf("NODES entry %d (%q): want exactly 'name|token'", i+1, parts[0])
		}
		name := strings.TrimSpace(parts[0])
		token := strings.TrimSpace(parts[1])
		if name == "" || token == "" {
			return nil, fmt.Errorf("NODES entry %d (%q): name and token must be non-empty", i+1, parts[0])
		}
		if reservedNodeNames[name] {
			logger.Warn("NODES entry %d: node name %q is reserved (telegram diag route collision); skipping it", i+1, name)
			continue
		}
		if seenName[name] {
			return nil, fmt.Errorf("NODES entry %d: duplicate node name %q", i+1, name)
		}
		if seenToken[token] {
			return nil, fmt.Errorf("NODES entry %d: duplicate token for node %q", i+1, name)
		}
		seenName[name] = true
		seenToken[token] = true
		out = append(out, NodeConfig{Name: name, Token: token})
	}
	return out, nil
}
