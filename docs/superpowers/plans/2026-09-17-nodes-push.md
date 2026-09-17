# Remote Checker Nodes (push) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Master instance receives push reports from headless checker nodes, alerts on their proxies per-(node, proxy), tracks node health, and manages node subscriptions via Telegram.

**Architecture:** Nodes (same image, no bot token) POST check snapshots to the master's ingest endpoint after each check cycle; the master namesp­aces identities (`node/stableID`), merges snapshots into the existing `ProcessSnapshot` alert pipeline, and answers each report with the node's desired managed-subscription list which the node reconciles locally. Node ASN comes from a local mmdb database keyed by the reported host IP.

**Tech Stack:** Go 1.26 (module `xray-checker`), kong config, gocron, telego, `github.com/oschwald/maxminddb-golang` (new, only new dependency).

**Spec:** `docs/superpowers/specs/2026-09-17-nodes-push-design.md` — the plan argues from the spec; read both.

## Global Constraints

- Feature fully OFF when `NODES` and `REPORT_URL` are empty: existing behavior and `/metrics` output byte-identical (spec §11, §13).
- New dependency allowed: only `github.com/oschwald/maxminddb-golang`. No DB, no CGO.
- Config follows kong style in `config/config.go`: `name`/`env`/`default` tags, validation in `Validate()`.
- Exported identifiers get English doc comments (repo style). User-facing Telegram text is Russian, matching existing bot copy.
- Namespaced remote identity everywhere: `"<nodeName>/<stableID>"` (spec §5.1).
- Stale deadline: `2 × reported checkInterval + 60s` grace; sweeper every 30s (spec §5.1).
- Ingest body limit 5 MiB; Bearer auth constant-time; unknown token → 401 without details (spec §10).
- Every task: `gofmt -w` changed files, `go vet ./...`, `go test ./...` green before commit. New `nodes`/`asn` package tests must pass under `-race`.
- Commits: conventional style (`feat:`, `fix:`, `docs:`, `test:`), on branch `feat/nodes-push`.
- Node-down grace: proxies of a downed node appear exactly once in the merged snapshot with `Disabled=true`, then disappear (spec §5.2).
- No node-down alerts before the first successful report (`pending` state is silent) (spec §5.1).

---

### Task 1: `nodes` package skeleton — NodeConfig and ParseNodes

**Files:**
- Create: `nodes/nodes.go`
- Test: `nodes/nodes_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `nodes.NodeConfig{Name, Token string}`, `nodes.ParseNodes(raw []string) ([]NodeConfig, error)`.

- [ ] **Step 1: Write the failing test**

```go
package nodes

import "testing"

func TestParseNodes(t *testing.T) {
	got, err := ParseNodes([]string{"node-1|secrettoken1", "node-2|secrettoken2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(got))
	}
	if got[0].Name != "node-1" || got[0].Token != "secrettoken1" {
		t.Errorf("bad first entry: %+v", got[0])
	}
}

func TestParseNodesErrors(t *testing.T) {
	cases := [][]string{
		{"noname"},                      // missing token
		{"|token"},                      // missing name
		{"a|t1", "a|t2"},                // duplicate name
		{"a|t1|extra"},                  // too many parts
		{""},                            // empty entry
	}
	for _, c := range cases {
		if _, err := ParseNodes(c); err == nil {
			t.Errorf("ParseNodes(%q) should fail", c)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./nodes/ -run TestParseNodes -v`
Expected: FAIL — package has no ParseNodes.

- [ ] **Step 3: Implement**

```go
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
```

- [ ] **Step 4: Run tests, vet, format**

Run: `go test ./nodes/ -v && gofmt -l nodes/ && go vet ./nodes/`
Expected: PASS, empty gofmt output, no vet findings.

- [ ] **Step 5: Commit**

```bash
git add nodes/
git commit -m "feat(nodes): add NodeConfig and NODES list parser"
```

---

### Task 2: Config surface + validation + .env.example

**Files:**
- Modify: `config/config.go` (add struct groups after `Telegram`, extend `Validate()`)
- Modify: `.env.example`
- Test: `config/config_test.go`

**Interfaces:**
- Consumes: `nodes.ParseNodes` (Task 1).
- Produces: `config.CLIConfig.Nodes.List []string` (env `NODES`), `config.CLIConfig.Nodes.StorePath string` (env `NODES_STORE_PATH`, default `node_subs.json`), `config.CLIConfig.Report.URL` (env `REPORT_URL`), `config.CLIConfig.Report.Token` (env `REPORT_TOKEN`), `config.CLIConfig.ASN.DBURL` (env `ASN_DB_URL`).

- [ ] **Step 1: Write the failing test**

Append to `config/config_test.go` (match existing test function style):

```go
func TestValidateNodesAndReport(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *CLI)
		wantErr string
	}{
		{"valid nodes", func(c *CLI) {
			c.Nodes.List = []string{"n1|tok"}
			c.Metrics.Port = "2112"
		}, ""},
		{"bad node entry", func(c *CLI) {
			c.Nodes.List = []string{"n1"}
			c.Metrics.Port = "2112"
		}, "want exactly 'name|token'"},
		{"nodes need http port", func(c *CLI) {
			c.Nodes.List = []string{"n1|tok"}
			c.Metrics.Port = "0"
		}, "requires a listening HTTP port"},
		{"report url without token", func(c *CLI) {
			c.Report.URL = "http://master:2112/api/v1/nodes/report"
		}, "REPORT_TOKEN"},
		{"report token without url", func(c *CLI) {
			c.Report.Token = "tok"
		}, "REPORT_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := defaultTestCLI(t) // see step 3: helper building a valid CLI
			tt.mutate(c)
			err := c.Validate()
			if tt.wantErr == "" {
				if err != nil { t.Fatalf("unexpected error: %v", err) }
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestValidateNodesAndReport -v`
Expected: FAIL — `c.Nodes` undefined / `defaultTestCLI` undefined.

- [ ] **Step 3: Implement**

In `config/config.go`, add struct groups inside `CLI` (after the `Telegram` group):

```go
	Nodes struct {
		List      []string `name:"node" help:"Remote checker node as 'name|token' (repeatable; env: comma-separated). Empty disables the node feature" env:"NODES"`
		StorePath string   `name:"nodes-store-path" help:"File with the desired managed subscriptions per node, edited via the bot" default:"node_subs.json" env:"NODES_STORE_PATH"`
	} `embed:"" prefix:""`

	Report struct {
		URL   string `name:"report-url" help:"Master ingest URL to push check snapshots to (enables node reporting when set)" default:"" env:"REPORT_URL"`
		Token string `name:"report-token" help:"Bearer token matching this node's entry in the master's NODES list" default:"" env:"REPORT_TOKEN"`
	} `embed:"" prefix:""`

	ASN struct {
		DBURL string `name:"asn-db-url" help:"URL of the gzipped ASN mmdb database (db-ip asn-lite format)" default:"https://download.db-ip.com/free/dbip-asn-lite-2026-08.mmdb.gz" env:"ASN_DB_URL"`
	} `embed:"" prefix:""`
```

Add import `"xray-checker/nodes"` and extend `Validate()` (after the existing checks):

```go
	if len(c.Nodes.List) > 0 {
		if _, err := nodes.ParseNodes(c.Nodes.List); err != nil {
			return err
		}
		if c.Metrics.Port == "" || c.Metrics.Port == "0" {
			return fmt.Errorf("NODES requires a listening HTTP port for the ingest endpoint (METRICS_PORT is empty or 0)")
		}
	}
	if (c.Report.URL == "") != (c.Report.Token == "") {
		return fmt.Errorf("REPORT_URL and REPORT_TOKEN must be set together")
	}
```

In `config/config_test.go`, add the helper (reuse whatever valid-CLI construction existing tests use; if one already exists, adapt instead of duplicating):

```go
// defaultTestCLI returns a CLI value that passes Validate().
func defaultTestCLI(t *testing.T) *CLI {
	t.Helper()
	c := &CLI{}
	c.Subscription.URLs = []string{"https://example.com/sub"}
	c.Proxy.CheckMethod = "ip"
	return c
}
```

If existing `config_test.go` already constructs a valid `CLI` differently (check before writing), follow that pattern instead.

In `.env.example`, append (match the file's existing comment style — English, one block per feature):

```env
# ── Remote checker nodes (master side) ────────────────────────────────────────
# Remote checker instances that push reports to this master, as 'name|token'
# (comma-separated). Empty disables the feature. Ingest endpoint:
# POST /api/v1/nodes/report. Requires a listening METRICS_PORT.
NODES=
# File storing the desired managed subscriptions per node (edited via bot).
NODES_STORE_PATH=node_subs.json

# ── Remote checker nodes (node side) ──────────────────────────────────────────
# Master ingest URL to push check snapshots to, e.g.
# https://master.example:2112/api/v1/nodes/report. Set together with
# REPORT_TOKEN; empty disables reporting.
REPORT_URL=
REPORT_TOKEN=

# ── Node ASN lookup (master side) ─────────────────────────────────────────────
# Gzipped ASN mmdb database (db-ip asn-lite). Downloaded once at startup;
# failures degrade to empty ASN everywhere, never affecting alerts.
ASN_DB_URL=https://download.db-ip.com/free/dbip-asn-lite-2026-08.mmdb.gz
```

- [ ] **Step 4: Run tests**

Run: `go test ./config/ -v && go vet ./config/`
Expected: PASS (existing config tests included).

- [ ] **Step 5: Commit**

```bash
git add config/config.go config/config_test.go .env.example
git commit -m "feat(config): NODES, REPORT_URL/TOKEN and ASN_DB_URL surface with validation"
```

---

### Task 3: ProxyMetric node fields + report DTO + mapping

**Files:**
- Modify: `metrics/metrics.go` (add two fields to `ProxyMetric`)
- Create: `nodes/report.go`
- Test: `nodes/report_test.go`

**Interfaces:**
- Consumes: `metrics.ProxyMetric` (existing).
- Produces: `metrics.ProxyMetric.NodeName string`, `metrics.ProxyMetric.NodeASN string`; `nodes.ReportProxy`, `nodes.ReportPayload`, `nodes.IngestResponse{ManagedSubs []string}`; `nodes.BuildReport(pm []metrics.ProxyMetric, version string, intervalSec int, checkMethod, hostIP string) ReportPayload`; `nodes.ProxyMetricsFromReport(nodeName, nodeASN string, p ReportPayload) []metrics.ProxyMetric`.

- [ ] **Step 1: Write the failing test**

```go
package nodes

import (
	"testing"

	"xray-checker/metrics"
)

func sampleMetric() metrics.ProxyMetric {
	return metrics.ProxyMetric{
		Protocol: "vless", Address: "host.example:443", Name: "de-01",
		SubName: "sub1", GroupName: "g1", StableID: "a1b2c3",
		Online: true, LatencyMs: 123, LastErrorCategory: 0,
	}
}

func TestBuildReportAndMapBack(t *testing.T) {
	pm := sampleMetric()
	payload := BuildReport([]metrics.ProxyMetric{pm}, "1.2.3", 300, "ip", "203.0.113.7")
	if payload.Version != "1.2.3" || payload.CheckIntervalSec != 300 ||
		payload.CheckMethod != "ip" || payload.HostIP != "203.0.113.7" {
		t.Fatalf("bad payload meta: %+v", payload)
	}
	if len(payload.Proxies) != 1 {
		t.Fatalf("want 1 proxy, got %d", len(payload.Proxies))
	}
	rp := payload.Proxies[0]
	if rp.StableID != "a1b2c3" || rp.Name != "de-01" || !rp.Online || rp.LatencyMs != 123 {
		t.Errorf("bad report proxy: %+v", rp)
	}

	out := ProxyMetricsFromReport("node-1", "AS9009 M247", payload)
	if len(out) != 1 {
		t.Fatalf("want 1 metric, got %d", len(out))
	}
	got := out[0]
	if got.StableID != "node-1/a1b2c3" {
		t.Errorf("stable id not namespaced: %q", got.StableID)
	}
	if got.NodeName != "node-1" || got.NodeASN != "AS9009 M247" {
		t.Errorf("node fields not set: %+v", got)
	}
	if got.Name != "de-01" || got.Address != "host.example:443" || !got.Online {
		t.Errorf("proxy fields lost: %+v", got)
	}
}

func TestMapReportDisabledPassthrough(t *testing.T) {
	payload := ReportPayload{Version: "v", CheckIntervalSec: 60, Proxies: []ReportProxy{{
		StableID: "x", Name: "n", Online: false, Disabled: true,
	}}}
	out := ProxyMetricsFromReport("n1", "", payload)
	if !out[0].Disabled {
		t.Error("Disabled must pass through for the grace/alert-cleanup path")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./nodes/ -run TestBuildReport -v`
Expected: FAIL — undefined BuildReport/ReportPayload.

- [ ] **Step 3: Implement**

In `metrics/metrics.go`, add to `ProxyMetric` (after `DirectProbeErr`):

```go
	// NodeName is set only for proxies reported by remote checker nodes;
	// empty for locally checked proxies.
	NodeName string
	// NodeASN is the reporting node's "AS<number> <org>" string, empty when
	// the ASN database is unavailable.
	NodeASN string
```

Create `nodes/report.go`:

```go
package nodes

import (
	"time"

	"xray-checker/metrics"
)

// ReportProxy is one proxy result inside a node report. Field set mirrors
// what the master needs for alerts; custom labels are not transported (v1).
type ReportProxy struct {
	StableID          string  `json:"stableId"`
	Name              string  `json:"name"`
	SubName           string  `json:"subName"`
	GroupName         string  `json:"groupName"`
	Protocol          string  `json:"protocol"`
	Address           string  `json:"address"`
	Online            bool    `json:"online"`
	Disabled          bool    `json:"disabled"`
	LatencyMs         float64 `json:"latencyMs"`
	LastCheck         int64   `json:"lastCheck"`
	LastErrorCategory int     `json:"lastErrorCategory"`
	LastErrorMsg      string  `json:"lastErrorMsg"`
}

// ReportPayload is the body a node POSTs to the master after each check cycle.
type ReportPayload struct {
	Version          string        `json:"version"`
	CheckIntervalSec int           `json:"checkIntervalSec"`
	CheckMethod      string        `json:"checkMethod"`
	HostIP           string        `json:"hostIP"`
	Proxies          []ReportProxy `json:"proxies"`
}

// IngestResponse is the master's reply: the full desired list of managed
// subscription URLs for the reporting node.
type IngestResponse struct {
	ManagedSubs []string `json:"managedSubs"`
}

// BuildReport converts a local check snapshot into the wire payload.
// Node-scoped fields are dropped: they only exist on the master after ingest.
func BuildReport(pm []metrics.ProxyMetric, version string, intervalSec int, checkMethod, hostIP string) ReportPayload {
	p := ReportPayload{
		Version:          version,
		CheckIntervalSec: intervalSec,
		CheckMethod:      checkMethod,
		HostIP:           hostIP,
		Proxies:          make([]ReportProxy, 0, len(pm)),
	}
	for _, m := range pm {
		var lastCheck int64
		if m.LastCheckSec > 0 {
			lastCheck = m.LastCheckSec
		}
		p.Proxies = append(p.Proxies, ReportProxy{
			StableID:          m.StableID,
			Name:              m.Name,
			SubName:           m.SubName,
			GroupName:         m.GroupName,
			Protocol:          m.Protocol,
			Address:           m.Address,
			Online:            m.Online,
			Disabled:          m.Disabled,
			LatencyMs:         m.LatencyMs,
			LastCheck:         lastCheck,
			LastErrorCategory: m.LastErrorCategory,
			LastErrorMsg:      m.LastErrorMsg,
		})
	}
	return p
}

// ProxyMetricsFromReport maps an accepted report into master-side metrics,
// namespacing every identity under the reporting node so remote proxies never
// collide with local ones or across nodes.
func ProxyMetricsFromReport(nodeName, nodeASN string, p ReportPayload) []metrics.ProxyMetric {
	out := make([]metrics.ProxyMetric, 0, len(p.Proxies))
	for _, rp := range p.Proxies {
		out = append(out, metrics.ProxyMetric{
			Protocol:         rp.Protocol,
			Address:          rp.Address,
			Name:             rp.Name,
			SubName:          rp.SubName,
			StableID:         nodeName + "/" + rp.StableID,
			GroupName:        rp.GroupName,
			Online:           rp.Online,
			Disabled:         rp.Disabled,
			LatencyMs:        rp.LatencyMs,
			LastErrorCategory: rp.LastErrorCategory,
			LastErrorMsg:     rp.LastErrorMsg,
			NodeName:         nodeName,
			NodeASN:          nodeASN,
		})
	}
	return out
}
```

Note: `ProxyMetric` currently has no `LastCheckSec` field — add it in this task too (plain field, no behavior), because `BuildReport` transports last-check time and `web` already recomputes it elsewhere from the checker:

In `metrics/metrics.go`, alongside the node fields:

```go
	// LastCheckSec is the last check time as a Unix timestamp in seconds;
	// 0 when never checked. Populated by the checker snapshot path.
	LastCheckSec int64
```

And populate it in `checker/checker.go` → `MetricsSnapshot()` when building each `metrics.ProxyMetric` (it already computes `lastCheck` in `GetProxyResultByStableID`; in `MetricsSnapshot` derive from `r.lastCheck`):

```go
		var lastCheckSec int64
		if !r.lastCheck.IsZero() {
			lastCheckSec = r.lastCheck.Unix()
		}
```

adding `LastCheckSec: lastCheckSec,` to the appended `metrics.ProxyMetric` literal.

- [ ] **Step 4: Run tests**

Run: `go test ./nodes/ ./metrics/ ./checker/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add nodes/report.go nodes/report_test.go metrics/metrics.go checker/checker.go
git commit -m "feat(nodes): report DTO, namespaced mapping and ProxyMetric node fields"
```

---

### Task 4: NodeSubsStore — desired managed subscriptions per node

**Files:**
- Create: `nodes/subs_store.go`
- Test: `nodes/subs_store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `nodes.NodeSubsSource` (interface `{ ManagedSubsFor(node string) []string }`), `nodes.NewNodeSubsStore(path string) (*NodeSubsStore, error)`, methods `Get(node) []string`, `Add(node, url) (bool, error)`, `Remove(node, url) (bool, error)`.

- [ ] **Step 1: Write the failing test**

```go
package nodes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodeSubsStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_subs.json")
	s, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.Add("node-1", "https://sub.example/one")
	if err != nil || !added {
		t.Fatalf("Add: added=%v err=%v", added, err)
	}
	if again, _ := s.Add("node-1", "https://sub.example/one"); again {
		t.Error("duplicate Add must return false")
	}
	if _, err := s.Add("node-1", "ftp://bad"); err == nil {
		t.Error("non-http URL must be rejected")
	}
	s.Add("node-1", "https://sub.example/two")
	s.Add("node-2", "https://sub.example/other")

	got := s.ManagedSubsFor("node-1")
	if len(got) != 2 || got[0] != "https://sub.example/one" || got[1] != "https://sub.example/two" {
		t.Errorf("unexpected order/content: %v", got)
	}

	removed, _ := s.Remove("node-1", "https://sub.example/one")
	if !removed || len(s.Get("node-1")) != 1 {
		t.Errorf("Remove failed: removed=%v get=%v", removed, s.Get("node-1"))
	}
	if r, _ := s.Remove("node-1", "https://not-there"); r {
		t.Error("removing unknown URL must return false")
	}
	if r, _ := s.Remove("unknown-node", "https://sub.example/one"); r {
		t.Error("removing on unknown node must return false")
	}

	// Persistence: a fresh store over the same file sees the same state.
	s2, err := NewNodeSubsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if g := s2.ManagedSubsFor("node-2"); len(g) != 1 || g[0] != "https://sub.example/other" {
		t.Errorf("state lost after reopen: %v", g)
	}
}

func TestNodeSubsStoreMissingFile(t *testing.T) {
	if _, err := NewNodeSubsStore(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("missing file must not be an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "x", "y.json")); err == nil {
		t.Error("sanity")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./nodes/ -run TestNodeSubsStore -v`
Expected: FAIL — undefined NewNodeSubsStore.

- [ ] **Step 3: Implement**

```go
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
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing node subscriptions store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("saving node subscriptions store: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./nodes/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add nodes/subs_store.go nodes/subs_store_test.go
git commit -m "feat(nodes): persisted desired-subscription store per node"
```

---

### Task 5: URLStore managed marker (with legacy format migration)

**Files:**
- Modify: `subscription/manager.go`
- Test: `subscription/manager_test.go` (append cases)

**Interfaces:**
- Consumes: existing `URLStore`.
- Produces: `URLStore.AddManaged(raw) (bool, error)`, `URLStore.Managed() []string`, `URLStore.RemoveManaged(raw) (bool, error)`. Persisted file becomes `{"dynamic": [...], "managed": {"url": true}}` with transparent migration from the legacy `["url"]` array.

- [ ] **Step 1: Write the failing test**

Append to `subscription/manager_test.go`:

```go
func TestURLStoreManagedMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	s, err := NewURLStore([]string{"https://static.example/s"}, path)
	if err != nil {
		t.Fatal(err)
	}
	// A bot-style dynamic entry stays unmanaged.
	if _, err := s.Add("https://bot.example/b"); err != nil {
		t.Fatal(err)
	}
	added, err := s.AddManaged("https://master.example/m")
	if err != nil || !added {
		t.Fatalf("AddManaged: %v %v", added, err)
	}

	managed := s.Managed()
	if len(managed) != 1 || managed[0] != "https://master.example/m" {
		t.Fatalf("Managed() = %v", managed)
	}

	// Managing an existing dynamic entry flips its flag instead of duplicating.
	if ok, _ := s.AddManaged("https://bot.example/b"); !ok {
		t.Error("adopting an existing dynamic entry must report true")
	}
	if got := s.Managed(); len(got) != 2 {
		t.Errorf("after adoption Managed() = %v", got)
	}
	if got := s.All(); len(got) != 3 {
		t.Errorf("All() must not duplicate: %v", got)
	}

	// RemoveManaged only touches managed entries.
	if ok, _ := s.RemoveManaged("https://bot.example/b"); !ok {
		t.Error("adopted entry must be removable as managed")
	}
	if ok, _ := s.RemoveManaged("https://static.example/s"); ok {
		t.Error("static entry can never be removed")
	}
	if got := s.All(); len(got) != 2 {
		t.Errorf("All() after removals = %v", got)
	}

	// State survives reopen.
	s2, err := NewURLStore([]string{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Managed(); len(got) != 1 || got[0] != "https://master.example/m" {
		t.Errorf("managed flag lost on reopen: %v", got)
	}
	if ok, _ := s2.Remove("https://master.example/m"); !ok {
		t.Error("plain Remove still drops a managed dynamic entry")
	}
}

func TestURLStoreLegacyFormatMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	legacy := `["https://old.example/one","https://old.example/two"]`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewURLStore(nil, path)
	if err != nil {
		t.Fatalf("legacy file must load: %v", err)
	}
	if got := s.Dynamic(); len(got) != 2 {
		t.Fatalf("legacy dynamic lost: %v", got)
	}
	if got := s.Managed(); len(got) != 0 {
		t.Errorf("legacy entries must be unmanaged: %v", got)
	}
	// Next persist writes the new format and keeps loading.
	if _, err := s.AddManaged("https://new.example/three"); err != nil {
		t.Fatal(err)
	}
	s2, err := NewURLStore(nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Dynamic(); len(got) != 3 {
		t.Errorf("dynamic after reformat: %v", got)
	}
	if got := s2.Managed(); len(got) != 1 || got[0] != "https://new.example/three" {
		t.Errorf("managed after reformat: %v", got)
	}
}
```

Add `path/filepath` / `os` imports if the test file lacks them.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./subscription/ -run TestURLStoreManaged -v`
Expected: FAIL — undefined AddManaged.

- [ ] **Step 3: Implement**

In `subscription/manager.go`:

Add field to `URLStore`: `managed map[string]bool`.

Add store-file type and change loading in `NewURLStore`:

```go
type storeFile struct {
	Dynamic []string        `json:"dynamic"`
	Managed map[string]bool `json:"managed,omitempty"`
}
```

Replace the body after `data, err := os.ReadFile(path)` success:

```go
	var sf storeFile
	if err := json.Unmarshal(data, &sf); err == nil && sf.Dynamic != nil {
		s.dynamic = sf.Dynamic
		s.managed = sf.Managed
		if s.managed == nil {
			s.managed = make(map[string]bool)
		}
		return s, nil
	}
	// Legacy format: a bare JSON array of dynamic URLs, all unmanaged.
	var dynamic []string
	if err := json.Unmarshal(data, &dynamic); err != nil {
		return nil, fmt.Errorf("parsing subscription store %s: %w", path, err)
	}
	s.dynamic = dynamic
	s.managed = make(map[string]bool)
	return s, nil
```

Also initialize `managed: make(map[string]bool)` in the non-persistent early return.

Change `persistLocked` to write the new format:

```go
	data, err := json.MarshalIndent(storeFile{Dynamic: s.dynamic, Managed: s.managed}, "", "  ")
```

Add methods:

```go
// AddManaged adds url to the dynamic set flagged as managed by a remote
// master. An existing dynamic entry is adopted (flag flipped) instead of
// duplicated. Static entries are left alone (returns false).
func (s *URLStore) AddManaged(raw string) (bool, error) {
	u, err := normalizeSubscriptionURL(raw)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.managed == nil {
		s.managed = make(map[string]bool)
	}
	for _, existing := range s.static {
		if existing == u {
			return false, nil // static already satisfies the desired state
		}
	}
	managedBackup := make(map[string]bool, len(s.managed))
	for k, v := range s.managed {
		managedBackup[k] = v
	}
	dynamicBackup := append([]string(nil), s.dynamic...)
	if !s.containsLocked(u) {
		s.dynamic = append(s.dynamic, u)
	}
	s.managed[u] = true
	if err := s.persistLocked(); err != nil {
		s.dynamic = dynamicBackup
		s.managed = managedBackup
		return false, err
	}
	return true, nil
}

// Managed returns the dynamic URLs flagged as managed, in insertion order.
func (s *URLStore) Managed() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, u := range s.dynamic {
		if s.managed[u] {
			out = append(out, u)
		}
	}
	return out
}

// RemoveManaged removes url only if it is a managed dynamic entry.
func (s *URLStore) RemoveManaged(raw string) (bool, error) {
	u := strings.TrimSpace(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.managed[u] {
		return false, nil
	}
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
	dynamicBackup := append([]string(nil), s.dynamic...)
	managedBackup := make(map[string]bool, len(s.managed))
	for k, v := range s.managed {
		managedBackup[k] = v
	}
	s.dynamic = append(s.dynamic[:idx:idx], s.dynamic[idx+1:]...)
	delete(s.managed, u)
	if err := s.persistLocked(); err != nil {
		s.dynamic = dynamicBackup
		s.managed = managedBackup
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 4: Run tests (whole package — regression on existing store tests)**

Run: `go test ./subscription/ -race -v`
Expected: PASS including pre-existing tests (manager_test, ssrf_test).

- [ ] **Step 5: Commit**

```bash
git add subscription/manager.go subscription/manager_test.go
git commit -m "feat(subscription): managed-marker for dynamic URLs with legacy store migration"
```

---

### Task 6: ReconcileManaged — desired-state convergence on the node

**Files:**
- Create: `subscription/reconcile.go`
- Test: `subscription/reconcile_test.go`

**Interfaces:**
- Consumes: `URLStore.AddManaged/Managed/RemoveManaged/All` (Task 5), `ReadFromSource`, `logger`.
- Produces: `subscription.ReconcileManaged(store *URLStore, desired []string, reload func() (changed bool, proxyCount int, err error)) error`.

- [ ] **Step 1: Write the failing test**

```go
package subscription

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func subServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/plain")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// validSubBody is a base64url vless share link the existing parser accepts.
const validSubBody = "vless://389b6d3a-5b6c-4a8d-9f0e-1a2b3c4d5e6f@node.example:443?security=reality&type=tcp#node-1"

func TestReconcileManaged(t *testing.T) {
	good1 := subServer(t, validSubBody).URL
	good2 := subServer(t, validSubBody+"x").URL
	bad := subServer(t, "not a subscription at all").URL

	store, err := NewURLStore(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	reloadCalls := 0
	reload := func() (bool, int, error) { reloadCalls++; return true, 1, nil }

	// Empty desired on an empty store: no-op, no reload.
	if err := ReconcileManaged(store, nil, reload); err != nil {
		t.Fatal(err)
	}
	if reloadCalls != 0 {
		t.Fatalf("no-op must not reload, got %d calls", reloadCalls)
	}

	// Desired adds two valid, one invalid — invalid skipped with no error.
	if err := ReconcileManaged(store, []string{good1, good2, bad}, reload); err != nil {
		t.Fatal(err)
	}
	if got := store.Managed(); len(got) != 2 {
		t.Fatalf("want 2 managed, got %v", got)
	}
	if reloadCalls != 1 {
		t.Fatalf("exactly one reload after adds, got %d", reloadCalls)
	}

	// Shrink desired to one — the other is removed.
	if err := ReconcileManaged(store, []string{good1}, reload); err != nil {
		t.Fatal(err)
	}
	if got := store.Managed(); len(got) != 1 || got[0] != good1 {
		t.Fatalf("after shrink: %v", got)
	}

	// Same desired again: no reload.
	before := reloadCalls
	if err := ReconcileManaged(store, []string{good1}, reload); err != nil {
		t.Fatal(err)
	}
	if reloadCalls != before {
		t.Error("steady state must not reload")
	}
}

func TestReconcileManagedReloadFailureReverts(t *testing.T) {
	good := subServer(t, validSubBody).URL
	store, _ := NewURLStore(nil, "")
	fail := func() (bool, int, error) { return false, 0, errors.New("boom") }

	if err := ReconcileManaged(store, []string{good}, fail); err == nil {
		t.Fatal("reload error must propagate")
	}
	if got := store.Managed(); len(got) != 0 {
		t.Fatalf("store must be reverted, got %v", got)
	}
}
```

If the existing parser rejects `validSubBody` in this repo's test environment, take a working subscription fixture from `subscription/parser_test.go` and use it instead — the property under test is reconciliation, not parsing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./subscription/ -run TestReconcileManaged -v`
Expected: FAIL — undefined ReconcileManaged.

- [ ] **Step 3: Implement**

```go
package subscription

import (
	"fmt"
	"sort"

	"xray-checker/logger"
)

// ReconcileManaged aligns the store's managed dynamic subscriptions with the
// desired list received from the master. New URLs are validated before the
// store is touched; a failing reload reverts the store. Invalid desired URLs
// are skipped with a warning (the master's desired state stays authoritative).
func ReconcileManaged(store *URLStore, desired []string, reload func() (changed bool, proxyCount int, err error)) error {
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
		configs, _, err := ReadFromSource(u)
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./subscription/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add subscription/reconcile.go subscription/reconcile_test.go
git commit -m "feat(subscription): converge managed subs against master's desired list"
```

---

### Task 7: Reporter — node-side push client

**Files:**
- Create: `nodes/reporter.go`
- Test: `nodes/reporter_test.go`

**Interfaces:**
- Consumes: `ReportPayload`, `IngestResponse` (Task 3).
- Produces: `nodes.NewReporter(url, token string) *Reporter`, `(r *Reporter) Send(p ReportPayload) (managed []string, err error)`.

- [ ] **Step 1: Write the failing test**

```go
package nodes

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReporterSend(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Write([]byte(`{"managedSubs":["https://sub.example/one"]}`))
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL+"/api/v1/nodes/report", "secrettoken")
	managed, err := rep.Send(ReportPayload{Version: "v", CheckIntervalSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/nodes/report" {
		t.Errorf("path: %s", gotPath)
	}
	if gotAuth != "Bearer secrettoken" {
		t.Errorf("auth header: %s", gotAuth)
	}
	if len(managed) != 1 || managed[0] != "https://sub.example/one" {
		t.Errorf("managed list: %v", managed)
	}
}

func TestReporterSendErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL, "tok")
	if _, err := rep.Send(ReportPayload{}); err == nil {
		t.Error("401 must be an error")
	}
	if _, err := NewReporter("http://127.0.0.1:1/", "tok").Send(ReportPayload{}); err == nil {
		t.Error("connection failure must be an error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./nodes/ -run TestReporter -v`
Expected: FAIL — undefined NewReporter.

- [ ] **Step 3: Implement**

```go
package nodes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// reportTimeout bounds a single push attempt; the next check cycle retries.
const reportTimeout = 15 * time.Second

// Reporter pushes check snapshots to the master's ingest endpoint and returns
// the desired managed-subscription list from the response.
type Reporter struct {
	url    string
	token  string
	client *http.Client
}

// NewReporter builds a reporter for the given ingest URL and bearer token.
func NewReporter(url, token string) *Reporter {
	return &Reporter{url: url, token: token, client: &http.Client{Timeout: reportTimeout}}
}

// Send posts the payload and returns the managedSubs list. Errors are logged
// by the caller; a failed report is retried by the next check cycle.
func (r *Reporter) Send(p ReportPayload) ([]string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encoding report: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building report request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("posting report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return nil, fmt.Errorf("master answered HTTP %d", resp.StatusCode)
	}

	var out IngestResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding ingest response: %w", err)
	}
	return out.ManagedSubs, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./nodes/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add nodes/reporter.go nodes/reporter_test.go
git commit -m "feat(nodes): node-side reporter pushing snapshots to master"
```

---

### Task 8: Registry — ingest, health, grace, merged snapshot, sweeper

**Files:**
- Create: `nodes/registry.go`
- Test: `nodes/registry_test.go`

**Interfaces:**
- Consumes: `NodeConfig`, `ReportPayload`, `ProxyMetricsFromReport` (Tasks 1, 3), `NodeSubsSource` (Task 4), `metrics.ProxyMetric`.
- Produces:
  - `nodes.LookupFunc func(ip string) string`
  - `nodes.NodeHealth{Name string; Up, EverReported bool; Version, HostIP, ASN string; Online, Total int; LastReport time.Time; CheckIntervalSec int}` (json-tagged)
  - `nodes.NewRegistry(cfgs []NodeConfig, subs NodeSubsSource, asn LookupFunc) *Registry`
  - `(r) SetOnUpdate(f func())`, `(r) HandleReport() http.HandlerFunc`, `(r) MergedSnapshot(local []metrics.ProxyMetric) []metrics.ProxyMetric`, `(r) HealthSnapshot() []NodeHealth`, `(r) SweepStale(now time.Time) []string`, `(r) SubCounts(node string) map[string]int`, `(r) NodeExists(name string) bool`
  - consts `StaleMultiplier = 2`, `StaleGrace = 60 * time.Second`, `SweepInterval = 30 * time.Second`, `MaxReportBytes = 5 << 20`

- [ ] **Step 1: Write the failing test**

```go
package nodes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"xray-checker/metrics"
)

type fakeSubs struct{ data map[string][]string }

func (f *fakeSubs) ManagedSubsFor(node string) []string { return f.data[node] }

func newTestRegistry(asn LookupFunc) (*Registry, *fakeSubs) {
	subs := &fakeSubs{data: map[string][]string{"n1": {"https://sub.example/one"}}}
	return NewRegistry([]NodeConfig{{Name: "n1", Token: "t1"}, {Name: "n2", Token: "t2"}}, subs, asn), subs
}

func postReport(t *testing.T, h http.HandlerFunc, token string, p ReportPayload) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(p)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/report", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestHandleReportAuthAndHappyPath(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()

	for _, token := range []string{"", "wrong", "t2-or-nothing"} {
		if rec := postReport(t, h, token, ReportPayload{}); rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: want 401, got %d", token, rec.Code)
		}
	}
	if rec := postReport(t, h, "", ReportPayload{}); rec.Code != http.StatusUnauthorized {
		t.Error("missing header must 401")
	}

	rec := postReport(t, h, "t1", ReportPayload{
		Version: "1.0", CheckIntervalSec: 300, HostIP: "203.0.113.7",
		Proxies: []ReportProxy{{StableID: "a", Name: "p1", Online: true, SubName: "s1"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp IngestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.ManagedSubs) != 1 || resp.ManagedSubs[0] != "https://sub.example/one" {
		t.Errorf("managedSubs: %v", resp.ManagedSubs)
	}

	hs := reg.HealthSnapshot()
	if len(hs) != 2 || !hs[0].Up || hs[0].EverReported != true || hs[0].Online != 1 || hs[0].Total != 1 {
		t.Fatalf("health: %+v", hs)
	}
	if hs[1].Up || hs[1].EverReported {
		t.Errorf("n2 must stay pending: %+v", hs[1])
	}
}

func TestHandleReportBodyLimitAndBadJSON(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()

	big := make([]byte, MaxReportBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/report", bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer t1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: want 400, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/nodes/report", bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer t1")
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: want 400, got %d", rec.Code)
	}
}

func TestMergedSnapshotNamespacingAndGrace(t *testing.T) {
	reg, _ := newTestRegistry(func(ip string) string { return "AS9009 M247" })

	postReport(t, reg.HandleReport(), "t1", ReportPayload{
		Version: "1.0", CheckIntervalSec: 100, HostIP: "203.0.113.7",
		Proxies: []ReportProxy{{StableID: "a", Name: "p1", Online: true}},
	})
	postReport(t, reg.HandleReport(), "t2", ReportPayload{
		Version: "1.0", CheckIntervalSec: 100, HostIP: "203.0.113.8",
		Proxies: []ReportProxy{{StableID: "a", Name: "p1", Online: true}},
	})

	local := []metrics.ProxyMetric{{StableID: "a", Name: "p1"}}
	merged := reg.MergedSnapshot(local)
	if len(merged) != 3 {
		t.Fatalf("want local+2 nodes, got %d", len(merged))
	}
	ids := map[string]bool{}
	for _, m := range merged {
		ids[m.StableID] = true
	}
	if !ids["a"] || !ids["n1/a"] || !ids["n2/a"] {
		t.Errorf("namespacing broken: %v", ids)
	}

	// Deadline: 2*100s + 60s = 260s. At +261s both nodes go down; n1's grace
	// emits its last snapshot once with Disabled=true.
	if trans := reg.SweepStale(time.Now().Add(261 * time.Second)); len(trans) != 2 {
		t.Fatalf("both nodes stale, transitions = %v", trans)
	}
	grace := reg.MergedSnapshot(local)
	sawDisabled := 0
	for _, m := range grace {
		if m.NodeName == "n1" && m.Disabled {
			sawDisabled++
		}
	}
	if sawDisabled != 1 {
		t.Fatalf("grace snapshot must mark n1 proxies Disabled once, got %d", sawDisabled)
	}
	after := reg.MergedSnapshot(local)
	for _, m := range after {
		if m.NodeName == "n1" {
			t.Error("n1 must disappear from merged after grace")
		}
	}
}

func TestSweepStalePendingNeverTransitions(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	if trans := reg.SweepStale(time.Now().Add(24 * time.Hour)); len(trans) != 0 {
		t.Errorf("pending nodes must never transition: %v", trans)
	}
}

func TestNodeRecoversOnReport(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()
	postReport(t, h, "t1", ReportPayload{Version: "1", CheckIntervalSec: 10, Proxies: []ReportProxy{{StableID: "x", Online: true}}})
	reg.SweepStale(time.Now().Add(5 * time.Minute)) // down
	postReport(t, h, "t1", ReportPayload{Version: "1", CheckIntervalSec: 10, Proxies: []ReportProxy{{StableID: "x", Online: true}}})
	hs := reg.HealthSnapshot()
	if !hs[0].Up {
		t.Error("node must be up after a fresh report")
	}
}

func TestSubCounts(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	postReport(t, reg.HandleReport(), "t1", ReportPayload{
		Version: "1", CheckIntervalSec: 60,
		Proxies: []ReportProxy{
			{StableID: "a", SubName: "s1", Online: true},
			{StableID: "b", SubName: "s1", Online: false},
			{StableID: "c", SubName: "s2", Online: true},
		},
	})
	sc := reg.SubCounts("n1")
	if sc["s1"] != 2 || sc["s2"] != 1 {
		t.Errorf("SubCounts: %v", sc)
	}
}

func TestOnUpdateFires(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	var mu sync.Mutex
	calls := 0
	reg.SetOnUpdate(func() { mu.Lock(); calls++; mu.Unlock() })
	postReport(t, reg.HandleReport(), "t1", ReportPayload{Version: "1", CheckIntervalSec: 60})
	if calls != 1 {
		t.Errorf("onUpdate calls = %d", calls)
	}
}

func TestConcurrentUse(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	h := reg.HandleReport()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			postReport(t, h, "t1", ReportPayload{Version: "1", CheckIntervalSec: 60,
				Proxies: []ReportProxy{{StableID: "a", Online: true}}})
			reg.MergedSnapshot(nil)
			reg.HealthSnapshot()
			reg.SubCounts("n1")
		}(i)
	}
	wg.Wait()
}
```

Note: `postReport` inside goroutines uses `t.Helper` safely; if `go vet` complains about `t` use in goroutine, move error assertions out (record errors in a channel) — keep the race coverage.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./nodes/ -run 'TestHandleReport|TestMerged|TestSweep|TestNodeRecovers|TestSubCounts|TestOnUpdate|TestConcurrent' -v`
Expected: FAIL — undefined NewRegistry.

- [ ] **Step 3: Implement**

```go
package nodes

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"xray-checker/logger"
	"xray-checker/metrics"
)

// Stale configuration: a node is down after this much silence.
const (
	StaleMultiplier = 2                 // × reported CheckIntervalSec
	StaleGrace      = 60 * time.Second  // fixed grace on top
	SweepInterval   = 30 * time.Second  // master sweeper period
	MaxReportBytes  = 5 << 20           // ingest body limit
)

type nodeStatus int

const (
	statusPending nodeStatus = iota // no report seen yet: silent, no alerts
	statusUp
	statusDown
)

// LookupFunc resolves an IP to an ASN display string ("AS9009 M247"),
// returning "" when unavailable.
type LookupFunc func(ip string) string

type nodeState struct {
	cfg          NodeConfig
	status       nodeStatus
	lastReport   time.Time
	intervalSec  int
	version      string
	hostIP       string
	asn          string
	snapshot     []metrics.ProxyMetric
	gracePending bool // emit last snapshot with Disabled=true exactly once
}

// NodeHealth is the master's point-in-time view of one node.
type NodeHealth struct {
	Name             string    `json:"name"`
	Up               bool      `json:"up"`
	EverReported     bool      `json:"everReported"`
	Version          string    `json:"version"`
	HostIP           string    `json:"hostIP"`
	ASN              string    `json:"asn"`
	Online           int       `json:"online"`
	Total            int       `json:"total"`
	LastReport       time.Time `json:"lastReport"`
	CheckIntervalSec int       `json:"checkIntervalSec"`
}

// Registry holds the state of every reporting node: last snapshots, health,
// and identity by token. It is the ingest endpoint and the merged-snapshot
// source for the alert pipeline.
type Registry struct {
	mu       sync.RWMutex
	nodes    map[string]*nodeState // by name
	byToken  map[string]string     // token -> name
	subs     NodeSubsSource
	asn      LookupFunc
	onUpdate func()
}

// NewRegistry builds a registry for the given node configs. subs may be nil
// (empty managed lists); asn may be nil (no ASN enrichment).
func NewRegistry(cfgs []NodeConfig, subs NodeSubsSource, asn LookupFunc) *Registry {
	r := &Registry{
		nodes:   make(map[string]*nodeState, len(cfgs)),
		byToken: make(map[string]string, len(cfgs)),
		subs:    subs,
		asn:     asn,
	}
	for _, cfg := range cfgs {
		r.nodes[cfg.Name] = &nodeState{cfg: cfg, status: statusPending}
		r.byToken[cfg.Token] = cfg.Name
	}
	return r
}

// SetOnUpdate registers a callback fired after every accepted report and
// every health transition — the master re-feeds ProcessSnapshot then.
func (r *Registry) SetOnUpdate(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onUpdate = f
}

// NodeExists reports whether name is a configured node.
func (r *Registry) NodeExists(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.nodes[name]
	return ok
}

// HandleReport is the ingest endpoint: bearer auth by token, 5 MiB limit,
// state update, onUpdate callback, managedSubs response.
func (r *Registry) HandleReport() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name, ok := r.authenticate(req.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var payload ReportPayload
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, MaxReportBytes))
		if err := dec.Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if payload.CheckIntervalSec <= 0 {
			payload.CheckIntervalSec = 300
		}

		asn := ""
		if r.asn != nil && payload.HostIP != "" {
			asn = r.asn(payload.HostIP)
		}
		snap := ProxyMetricsFromReport(name, asn, payload)

		r.mu.Lock()
		st := r.nodes[name]
		st.status = statusUp
		st.lastReport = time.Now()
		st.intervalSec = payload.CheckIntervalSec
		st.version = payload.Version
		st.hostIP = payload.HostIP
		st.asn = asn
		st.snapshot = snap
		st.gracePending = false
		cb := r.onUpdate
		r.mu.Unlock()

		logger.Info("Node %s: report accepted (%d proxies, %s)", name, len(payload.Proxies), payload.Version)
		if cb != nil {
			cb()
		}

		w.Header().Set("Content-Type", "application/json")
		var managed []string
		if r.subs != nil {
			managed = r.subs.ManagedSubsFor(name)
		}
		json.NewEncoder(w).Encode(IngestResponse{ManagedSubs: managed})
	}
}

// authenticate resolves the bearer token to a node name with constant-time
// comparison per configured token.
func (r *Registry) authenticate(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return "", false
	}
	token := header[len(prefix):]
	name, ok := "", false
	r.mu.RLock()
	for t, n := range r.byToken {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			name, ok = n, true
		}
	}
	r.mu.RUnlock()
	return name, ok
}

// MergedSnapshot returns local plus every up node's snapshot. A node that
// just went down contributes its last snapshot once with Disabled=true (the
// alert pipeline then cleans its active alerts), then disappears.
func (r *Registry) MergedSnapshot(local []metrics.ProxyMetric) []metrics.ProxyMetric {
	r.mu.RLock()
	out := make([]metrics.ProxyMetric, 0, len(local)+16)
	out = append(out, local...)
	var graced []string
	for _, name := range r.sortedNamesLocked() {
		st := r.nodes[name]
		switch st.status {
		case statusUp:
			out = append(out, st.snapshot...)
		case statusDown:
			if st.gracePending {
				for _, pm := range st.snapshot {
					pm.Disabled = true
					out = append(out, pm)
				}
				graced = append(graced, name)
			}
		}
	}
	r.mu.RUnlock()

	if len(graced) > 0 {
		r.mu.Lock()
		for _, name := range graced {
			if st, ok := r.nodes[name]; ok {
				st.gracePending = false
			}
		}
		r.mu.Unlock()
	}
	return out
}

func (r *Registry) sortedNamesLocked() []string {
	names := make([]string, 0, len(r.nodes))
	for name := range r.nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HealthSnapshot returns one NodeHealth per configured node, sorted by name.
func (r *Registry) HealthSnapshot() []NodeHealth {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NodeHealth, 0, len(r.nodes))
	for _, name := range r.sortedNamesLocked() {
		st := r.nodes[name]
		h := NodeHealth{
			Name:             name,
			Up:               st.status == statusUp,
			EverReported:     !st.lastReport.IsZero(),
			Version:          st.version,
			HostIP:           st.hostIP,
			ASN:              st.asn,
			LastReport:       st.lastReport,
			CheckIntervalSec: st.intervalSec,
			Total:            len(st.snapshot),
		}
		for _, pm := range st.snapshot {
			if pm.Online {
				h.Online++
			}
		}
		out = append(out, h)
	}
	return out
}

// SweepStale marks nodes down whose last report is older than
// StaleMultiplier×interval + StaleGrace. Pending nodes never transition.
// Returns the names that transitioned (caller logs and re-feeds snapshots).
func (r *Registry) SweepStale(now time.Time) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var transitioned []string
	for name, st := range r.nodes {
		if st.status != statusUp {
			continue
		}
		deadline := time.Duration(StaleMultiplier*st.intervalSec)*time.Second + StaleGrace
		if now.Sub(st.lastReport) > deadline {
			st.status = statusDown
			st.gracePending = true
			transitioned = append(transitioned, name)
		}
	}
	sort.Strings(transitioned)
	return transitioned
}

// SubCounts returns proxy counts per subscription name from the node's last
// report; empty map when the node has no data yet.
func (r *Registry) SubCounts(node string) map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]int)
	st, ok := r.nodes[node]
	if !ok {
		return out
	}
	for _, pm := range st.snapshot {
		if pm.SubName != "" {
			out[pm.SubName]++
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./nodes/ -race -v`
Expected: PASS, no race reports.

- [ ] **Step 5: Commit**

```bash
git add nodes/registry.go nodes/registry_test.go
git commit -m "feat(nodes): master registry — ingest, health, grace cleanup, stale sweeper"
```

---

### Task 9: ASN package (db-ip asn-lite mmdb, gzipped)

**Files:**
- Create: `asn/asn.go`
- Test: `asn/asn_test.go`
- Modify: `go.mod` / `go.sum` (new dependency)

**Interfaces:**
- Consumes: nothing (self-contained; `logger`).
- Produces: `asn.DefaultDBURL`, `asn.EnsureDB(path, url string) error`, `asn.Open(path string) (*DB, error)`, `(d *DB) Lookup(ip string) string`, `(d *DB) Close() error`, `asn.NopLookup`.

- [ ] **Step 1: Add dependency**

Run: `go get github.com/oschwald/maxminddb-golang@latest && go mod tidy`
Expected: go.mod gains the single new require entry.

- [ ] **Step 2: Write the failing test**

```go
package asn

import (
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func gzServe(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		gz := gzip.NewWriter(w)
		gz.Write(payload)
		gz.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnsureDBDownloadsAndGunzips(t *testing.T) {
	payload := []byte("pretend this is an mmdb blob")
	srv := gzServe(t, payload)
	path := filepath.Join(t.TempDir(), "asn.mmdb")

	if err := EnsureDB(path, srv.URL); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Error("gunzipped content mismatch")
	}

	// Existing file: no second download.
	calls := 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("should not be fetched"))
	}))
	defer srv2.Close()
	if err := EnsureDB(path, srv2.URL); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Error("existing DB must not be re-downloaded")
	}
}

func TestEnsureDBBadURL(t *testing.T) {
	if err := EnsureDB(filepath.Join(t.TempDir(), "x.mmdb"), "http://127.0.0.1:1/nope"); err == nil {
		t.Error("unreachable URL must error")
	}
}

func TestFormatASN(t *testing.T) {
	if got := formatASN(9009, "M247 Europe SRL"); got != "AS9009 M247 Europe SRL" {
		t.Errorf("formatASN: %q", got)
	}
	if got := formatASN(0, ""); got != "" {
		t.Errorf("empty ASN must format to empty string, got %q", got)
	}
}

func TestLookupInvalidIP(t *testing.T) {
	db := &DB{}
	if got := db.Lookup("not-an-ip"); got != "" {
		t.Errorf("invalid IP must return empty, got %q", got)
	}
	if got := db.Lookup("192.168.1.1"); got != "" {
		t.Errorf("private IP must return empty, got %q", got)
	}
}

// Optional: against a real database when provided.
func TestLookupRealDB(t *testing.T) {
	path := os.Getenv("ASN_TEST_DB")
	if path == "" {
		t.Skip("set ASN_TEST_DB to a real dbip-asn-lite mmdb to run")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Lookup("8.8.8.8"); got == "" {
		t.Log("8.8.8.8 resolved to empty; check the database covers it")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./asn/ -v`
Expected: FAIL — package undefined.

- [ ] **Step 4: Implement**

```go
// Package asn resolves node host IPs to their autonomous system (network
// operator) using a local db-ip asn-lite mmdb database. Every failure mode
// degrades to an empty string — ASN never blocks or breaks alerting.
package asn

import (
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"xray-checker/logger"
)

// DefaultDBURL points at db-ip's free asn-lite database (CC-BY 4.0). The URL
// embeds a month; override with ASN_DB_URL after db-ip publishes a newer one.
const DefaultDBURL = "https://download.db-ip.com/free/dbip-asn-lite-2026-08.mmdb.gz"

// downloadTimeout bounds the one-shot DB download at startup.
const downloadTimeout = 90 * time.Second

// EnsureDB downloads the gzipped mmdb from url to path unless a file already
// exists. The download is atomic (tmp + rename) so an interrupted fetch
// never leaves a truncated database behind.
func EnsureDB(path, url string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("asn db is not valid gzip: %w", err)
	}
	defer gz.Close()

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	if _, err := io.Copy(f, gz); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing asn db: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("closing asn db: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("moving asn db into place: %w", err)
	}
	logger.Info("ASN database downloaded to %s", path)
	return nil
}

// DB wraps the mmdb reader. Safe for concurrent use.
type DB struct {
	reader *maxminddb.Reader
}

type asnRecord struct {
	AutonomousSystemNumber         uint   `maxminddb:"autonomous_system_number"`
	AutonomousSystemOrganization   string `maxminddb:"autonomous_system_organization"`
}

// Open memory-maps the mmdb file at path.
func Open(path string) (*DB, error) {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening asn db %s: %w", path, err)
	}
	return &DB{reader: reader}, nil
}

// Close releases the underlying reader.
func (d *DB) Close() error {
	if d == nil || d.reader == nil {
		return nil
	}
	return d.reader.Close()
}

// NopLookup is the LookupFunc used when no database is available.
func NopLookup(string) string { return "" }

// Lookup returns "AS<number> <organization>" for a public IP, "" otherwise.
func (d *DB) Lookup(ipStr string) string {
	if d == nil || d.reader == nil {
		return ""
	}
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return ""
	}
	var rec asnRecord
	if err := d.reader.Lookup(ip, &rec); err != nil {
		return ""
	}
	return formatASN(rec.AutonomousSystemNumber, rec.AutonomousSystemOrganization)
}

// formatASN renders the display form; zero ASN renders empty.
func formatASN(num uint, org string) string {
	if num == 0 {
		return ""
	}
	if org == "" {
		return fmt.Sprintf("AS%d", num)
	}
	return fmt.Sprintf("AS%d %s", num, org)
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./asn/ -v && go vet ./asn/`
Expected: PASS (real-db test skips unless `ASN_TEST_DB` set).

- [ ] **Step 6: Commit**

```bash
git add asn/ go.mod go.sum
git commit -m "feat(asn): local ASN lookup from db-ip asn-lite mmdb with atomic gz download"
```

---

### Task 10: telegram — NodeManager interface and node health alerts

**Files:**
- Create: `telegram/nodes.go`
- Modify: `telegram/bot.go` (add fields)
- Test: `telegram/nodes_test.go`

**Interfaces:**
- Consumes: `metrics.ProxyMetric` indirectly; existing `Bot` plumbing (`sendAndReturn`, `b.now()`, `escapeHTML`, `FormatDowntime`).
- Produces: `telegram.NodeInfo`, `telegram.ManagedSubInfo{URL string; ProxyCount int}` (`-1` = no data), `telegram.NodeManager` interface, `(b *Bot) SetNodeManager(m NodeManager)`, `(b *Bot) ProcessNodesHealth(states []NodeInfo)`.

- [ ] **Step 1: Write the failing test**

The state machine is split from the network side so it is testable with a
struct-literal `Bot` (the pattern `telegram/bot_test.go` already uses):

```go
package telegram

import (
	"strings"
	"testing"
	"time"
)

func TestUpdateNodeHealthTransitions(t *testing.T) {
	b := &Bot{lastNodeHealth: make(map[string]nodeHealthState)}
	base := time.Now()

	up := []NodeInfo{{Name: "n1", Up: true, ASN: "AS9009 M247"}}
	down := []NodeInfo{{Name: "n1", Up: false, ASN: "AS9009 M247"}}

	// Seed: no alerts.
	alerts := b.updateNodeHealthLocked(up, base)
	if len(alerts) != 0 {
		t.Fatalf("seed must not alert: %v", alerts)
	}

	// Down: one alert mentioning node and ASN.
	alerts = b.updateNodeHealthLocked(down, base.Add(time.Minute))
	if len(alerts) != 1 {
		t.Fatalf("want 1 down alert, got %v", alerts)
	}
	if !strings.Contains(alerts[0], "n1") || !strings.Contains(alerts[0], "AS9009 M247") {
		t.Errorf("down alert content: %q", alerts[0])
	}

	// Up again: recovery alert with downtime.
	alerts = b.updateNodeHealthLocked(up, base.Add(3*time.Minute))
	if len(alerts) != 1 || !strings.Contains(alerts[0], "n1") {
		t.Fatalf("want recovery alert, got %v", alerts)
	}

	// Steady state: silence.
	alerts = b.updateNodeHealthLocked(up, base.Add(4*time.Minute))
	if len(alerts) != 0 {
		t.Errorf("steady state must not alert: %v", alerts)
	}

	// Vanished nodes drop out of state; re-appearing seeds silently again.
	b.updateNodeHealthLocked(nil, base.Add(5*time.Minute))
	if len(b.lastNodeHealth) != 0 {
		t.Errorf("state must be pruned, got %v", b.lastNodeHealth)
	}
	if alerts := b.updateNodeHealthLocked(up, base.Add(6*time.Minute)); len(alerts) != 0 {
		t.Errorf("re-appeared node must seed silently, got %v", alerts)
	}
}

func TestUpdateNodeHealthSilentDownRecovery(t *testing.T) {
	b := &Bot{lastNodeHealth: make(map[string]nodeHealthState)}
	base := time.Now()

	// A node seeded while down (never alerted, e.g. first sight after restart)
	// must not send a recovery alert when it comes up.
	if alerts := b.updateNodeHealthLocked([]NodeInfo{{Name: "n2", Up: false}}, base); len(alerts) != 0 {
		t.Fatalf("down seed must be silent: %v", alerts)
	}
	if alerts := b.updateNodeHealthLocked([]NodeInfo{{Name: "n2", Up: true}}, base.Add(time.Minute)); len(alerts) != 0 {
		t.Fatalf("silent-down recovery must not alert: %v", alerts)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./telegram/ -run TestUpdateNodeHealth -v`
Expected: FAIL — undefined updateNodeHealthLocked/NodeInfo.

- [ ] **Step 3: Implement**

`telegram/nodes.go`:

```go
package telegram

import (
	"fmt"
	"time"
)

// NodeInfo is the bot's view of one remote checker node. It mirrors
// nodes.NodeHealth but keeps the telegram package decoupled from the nodes
// package (mirrors the SubscriptionManager pattern).
type NodeInfo struct {
	Name         string
	Up           bool
	EverReported bool
	Version      string
	HostIP       string
	ASN          string
	Online       int
	Total        int
	LastReport   time.Time
	IntervalSec  int
}

// ManagedSubInfo is one desired subscription on a node. ProxyCount is taken
// from the node's last report; -1 means no data yet.
type ManagedSubInfo struct {
	URL        string
	ProxyCount int
}

// NodeManager lets the bot inspect remote nodes and edit their desired
// managed subscriptions. Implemented in main over nodes.Registry and
// nodes.NodeSubsStore.
type NodeManager interface {
	// Nodes returns every configured node, name-sorted.
	Nodes() []NodeInfo
	// ManagedSubs returns node's desired subscriptions with last-report counts.
	ManagedSubs(node string) ([]ManagedSubInfo, error)
	// AddSub appends url to node's desired list; applied on next report.
	AddSub(node, url string) error
	// RemoveSub drops url from node's desired list.
	RemoveSub(node, url string) error
}

// nodeHealthState tracks what the alert loop knows about a node. alerted
// distinguishes "we told the user it's down" from a silent seed (first sight
// or pending node), so a silent-down node never sends a bogus recovery.
type nodeHealthState struct {
	up      bool
	since   time.Time
	alerted bool
}

// SetNodeManager wires the node manager; nil disables node commands.
func (b *Bot) SetNodeManager(m NodeManager) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nodeMgr = m
}

// updateNodeHealthLocked folds a states slice into lastNodeHealth and returns
// the alert texts for down/recovery transitions. Caller holds nodeHealthMu;
// now is pre-localized by the caller. Lazily initializes the map so a
// struct-literal Bot (tests) works.
func (b *Bot) updateNodeHealthLocked(states []NodeInfo, now time.Time) []string {
	if b.lastNodeHealth == nil {
		b.lastNodeHealth = make(map[string]nodeHealthState)
	}
	var alerts []string
	seen := make(map[string]bool, len(states))
	for _, st := range states {
		seen[st.Name] = true
		prev, known := b.lastNodeHealth[st.Name]
		if !known {
			b.lastNodeHealth[st.Name] = nodeHealthState{up: st.Up, since: now}
			continue
		}
		asnSuffix := ""
		if st.ASN != "" {
			asnSuffix = " (" + escapeHTML(st.ASN) + ")"
		}
		switch {
		case prev.up && !st.Up:
			b.lastNodeHealth[st.Name] = nodeHealthState{up: false, since: now, alerted: true}
			timeStr := now.Format("15:04")
			alerts = append(alerts, fmt.Sprintf("🔴 <b>Нода %s</b> недоступна%s\n⏱ %s",
				escapeHTML(st.Name), asnSuffix, timeStr))
		case !prev.up && st.Up:
			b.lastNodeHealth[st.Name] = nodeHealthState{up: true, since: now}
			if prev.alerted {
				alerts = append(alerts, fmt.Sprintf("✅ <b>Нода %s</b> вернулась%s\n⏱ простой: %s",
					escapeHTML(st.Name), asnSuffix, FormatDowntime(now.Sub(prev.since))))
			}
		}
	}
	for name := range b.lastNodeHealth {
		if !seen[name] {
			delete(b.lastNodeHealth, name)
		}
	}
	return alerts
}

// ProcessNodesHealth alerts on node up/down transitions. Node alerts are
// infrastructure-level and bypass quiet hours; send failures are logged by
// sendAndReturn and skipped so one bad chat can't stall the loop.
func (b *Bot) ProcessNodesHealth(states []NodeInfo) {
	if len(states) == 0 {
		return
	}
	b.nodeHealthMu.Lock()
	alerts := b.updateNodeHealthLocked(states, b.now())
	b.nodeHealthMu.Unlock()

	for _, text := range alerts {
		for _, t := range b.targets {
			_, _ = b.sendAndReturn(t, text)
		}
	}
}
```

In `telegram/bot.go`, add fields to `Bot` (next to `subs`):

```go
	nodeMgr NodeManager

	nodeHealthMu    sync.Mutex
	lastNodeHealth  map[string]nodeHealthState
```

and initialize `lastNodeHealth: make(map[string]nodeHealthState),` inside `New`'s returned literal (next to `lastSeen`).

Check `b.loc()` and `b.now()` exist (they do — used in alerts.go); reuse them.

- [ ] **Step 4: Run tests**

Run: `go test ./telegram/ -v`
Expected: PASS including existing suite.

- [ ] **Step 5: Commit**

```bash
git add telegram/nodes.go telegram/nodes_test.go telegram/bot.go
git commit -m "feat(telegram): node health transition alerts and NodeManager interface"
```

---

### Task 11: telegram — node commands and alert formatters

**Files:**
- Create: `telegram/nodes_commands.go`
- Modify: `telegram/commands.go` (routing + `replyHelp`), `telegram/alerts.go` (node line in outage/recovery)
- Test: `telegram/nodes_commands_test.go`

**Interfaces:**
- Consumes: `NodeManager` (Task 10), `commandArg`, `escapeHTML`, `sendOrUpdateMenu`, `BackToSettingsMarkup`, `replyCommand`.
- Produces: `(b *Bot) replyNodes(t ChatTarget)`, `(b *Bot) replyNodeSubs(msg)`, `(b *Bot) handleNodeAddSub(msg)`, `(b *Bot) handleNodeDelSub(msg)`, `commandArgs(text) []string`, `nodeLineFor(pm metrics.ProxyMetric) string`.

- [ ] **Step 1: Write the failing test**

```go
package telegram

import (
	"strings"
	"testing"

	"xray-checker/metrics"
)

func TestNodeLineFor(t *testing.T) {
	if got := nodeLineFor(metrics.ProxyMetric{Name: "p"}); got != "" {
		t.Errorf("local proxy must have no node line, got %q", got)
	}
	got := nodeLineFor(metrics.ProxyMetric{NodeName: "n1"})
	if !strings.Contains(got, "n1") {
		t.Errorf("node line missing node: %q", got)
	}
	got = nodeLineFor(metrics.ProxyMetric{NodeName: "n1", NodeASN: "AS9009 M247"})
	if !strings.Contains(got, "AS9009 M247") {
		t.Errorf("node line missing ASN: %q", got)
	}
}

func TestCommandArgs(t *testing.T) {
	if got := commandArgs("/nodeaddsub n1 https://x/y"); len(got) != 2 || got[0] != "n1" || got[1] != "https://x/y" {
		t.Errorf("commandArgs: %v", got)
	}
	if got := commandArgs("/nodes"); len(got) != 0 {
		t.Errorf("commandArgs no-arg: %v", got)
	}
}

func TestFormatAge(t *testing.T) {
	cases := map[string]struct {
		d    time.Duration
		want string
	}{"12s": {12 * time.Second, "12s"}, "3m12s": {3*time.Minute + 12*time.Second, "3m12s"}, "2h05m": {2*time.Hour + 5*time.Minute, "2h05m"}}
	for name, c := range cases {
		if got := formatAge(c.d); got != c.want {
			t.Errorf("%s: formatAge = %q, want %q", name, got, c.want)
		}
	}
}
```

Add `time` import to the test file. Extend `TestFormatAge`'s map values inline if compile order complains.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./telegram/ -run 'TestNodeLineFor|TestCommandArgs|TestFormatAge' -v`
Expected: FAIL — undefined identifiers.

- [ ] **Step 3: Implement**

`telegram/nodes_commands.go`:

```go
package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"
)

// commandArgs returns every whitespace-separated argument after the command
// (commandArg returns only the first).
func commandArgs(text string) []string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return nil
	}
	return fields[1:]
}

// formatAge renders a duration as a compact human age: 12s, 3m12s, 2h05m.
func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func (b *Bot) replyNodes(t ChatTarget) {
	if b.nodeMgr == nil {
		b.sendOrUpdateMenu(t, "Ноды не настроены (список NODES на мастере пуст).", BackToSettingsMarkup())
		return
	}
	list := b.nodeMgr.Nodes()
	var sb strings.Builder
	fmt.Fprintf(&sb, "🖥 <b>Ноды (%d)</b>\n", len(list))
	now := b.now()
	for _, n := range list {
		icon := "🔴"
		if n.Up {
			icon = "🟢"
		}
		fmt.Fprintf(&sb, "%s <b>%s</b>", icon, escapeHTML(n.Name))
		if n.ASN != "" {
			sb.WriteString(" · " + escapeHTML(n.ASN))
		}
		if !n.EverReported {
			sb.WriteString(" · отчётов ещё не было")
		} else {
			fmt.Fprintf(&sb, " · %d/%d онлайн · отчёт %s назад",
				n.Online, n.Total, formatAge(now.Sub(n.LastReport)))
			if n.Version != "" {
				sb.WriteString(" · v" + escapeHTML(n.Version))
			}
		}
		sb.WriteString("\n")
	}
	b.sendOrUpdateMenu(t, sb.String(), BackToSettingsMarkup())
}

func (b *Bot) replyNodeSubs(msg *telego.Message) {
	args := commandArgs(msg.Text)
	if len(args) != 1 {
		b.replyCommand(msg, "Использование: /nodesubs &lt;имя ноды&gt;")
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "Ноды не настроены.")
		return
	}
	subs, err := b.nodeMgr.ManagedSubs(args[0])
	if err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📡 <b>Подписки ноды %s (%d)</b>\n", escapeHTML(args[0]), len(subs))
	for _, s := range subs {
		if s.ProxyCount >= 0 {
			fmt.Fprintf(&sb, "• %s — %d прокси\n", escapeHTML(s.URL), s.ProxyCount)
		} else {
			fmt.Fprintf(&sb, "• %s — нет данных (нода ещё не применяла)\n", escapeHTML(s.URL))
		}
	}
	b.replyCommand(msg, sb.String())
}

func (b *Bot) handleNodeAddSub(msg *telego.Message) {
	args := commandArgs(msg.Text)
	if len(args) != 2 {
		b.replyCommand(msg, "Использование: /nodeaddsub &lt;имя ноды&gt; &lt;URL подписки&gt;")
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "Ноды не настроены.")
		return
	}
	if err := b.nodeMgr.AddSub(args[0], args[1]); err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Подписка добавлена для ноды <b>%s</b>.\nНода применит её при следующем отчёте.", escapeHTML(args[0])))
}

func (b *Bot) handleNodeDelSub(msg *telego.Message) {
	args := commandArgs(msg.Text)
	if len(args) != 2 {
		b.replyCommand(msg, "Использование: /nodedelsub &lt;имя ноды&gt; &lt;URL подписки&gt;")
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "Ноды не настроены.")
		return
	}
	if err := b.nodeMgr.RemoveSub(args[0], args[1]); err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Подписка удалена для ноды <b>%s</b>.\nНода уберёт её при следующем отчёте.", escapeHTML(args[0])))
}
```

In `telegram/commands.go` → `handleMessage`, add before the `/subs` case (order matters: longest prefixes first):

```go
	case strings.HasPrefix(msg.Text, "/nodesubs"):
		b.replyNodeSubs(msg)
	case strings.HasPrefix(msg.Text, "/nodeaddsub"):
		go b.handleNodeAddSub(msg)
	case strings.HasPrefix(msg.Text, "/nodedelsub"):
		go b.handleNodeDelSub(msg)
	case strings.HasPrefix(msg.Text, "/nodes"):
		b.replyNodes(t)
```

In `telegram/alerts.go`, add the helper and wire it into both texts:

```go
// nodeLineFor renders the "via node" line for remote-proxy alerts; empty
// for locally checked proxies.
func nodeLineFor(pm metrics.ProxyMetric) string {
	if pm.NodeName == "" {
		return ""
	}
	s := "\n📍 via " + escapeHTML(pm.NodeName)
	if pm.NodeASN != "" {
		s += " · " + escapeHTML(pm.NodeASN)
	}
	return s
}
```

Change the outage format string (currently `alerts.go:198`) from:

```go
		outageText := fmt.Sprintf("🔴 <b>%s</b> — не отвечает%s\n⏱ %s · %d-й сбой\n%s%s", escapeHTML(pm.Name), softHint, timeStr, dropCount, escapeHTML(pm.Address), flapNote)
```

to:

```go
		outageText := fmt.Sprintf("🔴 <b>%s</b> — не отвечает%s%s\n⏱ %s · %d-й сбой\n%s%s", escapeHTML(pm.Name), softHint, nodeLineFor(pm), timeStr, dropCount, escapeHTML(pm.Address), flapNote)
```

Change the recovery text (currently `alerts.go:231`) from:

```go
				recoveryText := fmt.Sprintf("✅ <b>%s</b> восстановлен — %.0f ms", escapeHTML(rec.pm.Name), rec.pm.LatencyMs)
```

to:

```go
				recoveryText := fmt.Sprintf("✅ <b>%s</b> восстановлен — %.0f ms%s", escapeHTML(rec.pm.Name), rec.pm.LatencyMs, nodeLineFor(rec.pm))
```

In `telegram/commands.go` → `replyHelp` (line ~386), find the `/addsub`/`/delsub` help entries and add after them (match the surrounding list format used there):

```go
/nodes — ноды-инстансы чекера: статус, ASN, сводка
/nodesubs <имя> — подписки ноды, назначенные с мастера
/nodeaddsub <имя> <URL> — назначить подписку ноде
/nodedelsub <имя> <URL> — снять подписку с ноды
```

and reword the existing `/togglenode` entry to clarify it toggles a proxy host, e.g. `«/togglenode — включить/выключить проверку хоста (не ноды-инстанса)»` keeping the file's language conventions.

- [ ] **Step 4: Run tests**

Run: `go test ./telegram/ -v && go vet ./telegram/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add telegram/nodes_commands.go telegram/nodes_commands_test.go telegram/commands.go telegram/alerts.go
git commit -m "feat(telegram): /nodes commands and node context in proxy alerts"
```

---

### Task 12: web — GET /api/v1/nodes on the master

**Files:**
- Modify: `web/api.go`, `web/openapi.yaml`
- Test: `web/api_test.go` (append)

**Interfaces:**
- Consumes: `nodes.Registry.HealthSnapshot()` (Task 8).
- Produces: `web.APINodesHandler(reg *nodes.Registry) http.HandlerFunc` → `APIResponse{Data: []nodes.NodeHealth}`.

- [ ] **Step 1: Write the failing test**

Append to `web/api_test.go` (mirror its existing handler test style):

```go
func TestAPINodesHandler(t *testing.T) {
	reg := nodes.NewRegistry([]nodes.NodeConfig{{Name: "n1", Token: "t1"}}, nil, nil)
	rec := httptest.NewRecorder()
	APINodesHandler(reg)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp struct {
		Success bool              `json:"success"`
		Data    []nodes.NodeHealth `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Success || len(resp.Data) != 1 || resp.Data[0].Name != "n1" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.Data[0].EverReported || resp.Data[0].Up {
		t.Errorf("pending node must be down/unreported: %+v", resp.Data[0])
	}
}
```

Add imports `xray-checker/nodes`, `encoding/json`, `net/http/httptest` if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./web/ -run TestAPINodesHandler -v`
Expected: FAIL — undefined APINodesHandler.

- [ ] **Step 3: Implement**

In `web/api.go`, add import `"xray-checker/nodes"` and:

```go
// APINodesHandler returns the health of remote nodes reporting to this master
// @Summary Remote nodes health
// @Description Health snapshot of every configured remote checker node
// @Tags nodes
// @Produce json
// @Success 200 {array} nodes.NodeHealth
// @Router /api/v1/nodes [get]
func APINodesHandler(reg *nodes.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, reg.HealthSnapshot())
	}
}
```

In `web/openapi.yaml`: inside `paths:`, after the `/api/v1/system/info` block, add:

```yaml
  /api/v1/nodes:
    get:
      tags:
        - nodes
      summary: Remote nodes health
      description: Health snapshot of every configured remote checker node
      operationId: getNodes
      responses:
        '200':
          description: Node health list
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: '#/components/schemas/NodeHealth'
```

And inside `components.schemas:`, after `SystemIPResponse`, add:

```yaml
    NodeHealth:
      type: object
      properties:
        name:
          type: string
          example: node-1
        up:
          type: boolean
        everReported:
          type: boolean
        version:
          type: string
          example: "1.0.0"
        hostIP:
          type: string
          example: "203.0.113.7"
        asn:
          type: string
          example: "AS9009 M247 Europe SRL"
        online:
          type: integer
        total:
          type: integer
        lastReport:
          type: string
          format: date-time
        checkIntervalSec:
          type: integer
```

- [ ] **Step 4: Run tests**

Run: `go test ./web/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/api.go web/api_test.go web/openapi.yaml
git commit -m "feat(web): /api/v1/nodes health endpoint and OpenAPI entry"
```

---

### Task 13: main wiring, NodeManager adapter, READMEs, full regression

**Files:**
- Create: `node_manager.go` (package main — adapter + helpers)
- Modify: `main.go`
- Modify: `README.md`, `README_RU.md`
- Test: compile + full suite (`node_manager.go` logic is thin mapping; its correctness is covered by nodes/telegram tests; a small unit test for `toNodeInfos` is included where practical — if root-package tests don't exist yet, this task adds the first one: `node_manager_test.go`)

**Interfaces:**
- Consumes: everything produced by Tasks 1–12.
- Produces: running wiring — ingest mount, sweeper scheduler, `emitSnapshot`, reporter hook, `nodeManagerAdapter` implementing `telegram.NodeManager`.

- [ ] **Step 1: Create the adapter**

`node_manager.go`:

```go
package main

import (
	"fmt"

	"xray-checker/nodes"
	"xray-checker/telegram"
)

// nodeManagerAdapter implements telegram.NodeManager over the master's
// nodes.Registry (live state) and nodes.NodeSubsStore (desired state).
type nodeManagerAdapter struct {
	reg  *nodes.Registry
	subs *nodes.NodeSubsStore
}

func toNodeInfos(hs []nodes.NodeHealth) []telegram.NodeInfo {
	out := make([]telegram.NodeInfo, 0, len(hs))
	for _, h := range hs {
		out = append(out, telegram.NodeInfo{
			Name: h.Name, Up: h.Up, EverReported: h.EverReported,
			Version: h.Version, HostIP: h.HostIP, ASN: h.ASN,
			Online: h.Online, Total: h.Total, LastReport: h.LastReport,
			IntervalSec: h.CheckIntervalSec,
		})
	}
	return out
}

func (a *nodeManagerAdapter) Nodes() []telegram.NodeInfo {
	return toNodeInfos(a.reg.HealthSnapshot())
}

func (a *nodeManagerAdapter) ManagedSubs(node string) ([]telegram.ManagedSubInfo, error) {
	if !a.reg.NodeExists(node) {
		return nil, fmt.Errorf("неизвестная нода: %s", node)
	}
	counts := a.reg.SubCounts(node)
	// SubCounts is keyed by subscription display name; the bot shows counts
	// per URL only when the node reported a matching subName, so -1 default.
	out := make([]telegram.ManagedSubInfo, 0)
	for _, u := range a.subs.Get(node) {
		out = append(out, telegram.ManagedSubInfo{URL: u, ProxyCount: -1})
	}
	_ = counts // counts surfaced via /nodes summary; per-URL matching is v2
	return out, nil
}

func (a *nodeManagerAdapter) AddSub(node, url string) error {
	if !a.reg.NodeExists(node) {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	added, err := a.subs.Add(node, url)
	if err != nil {
		return err
	}
	if !added {
		return fmt.Errorf("эта подписка уже назначена ноде")
	}
	return nil
}

func (a *nodeManagerAdapter) RemoveSub(node, url string) error {
	if !a.reg.NodeExists(node) {
		return fmt.Errorf("неизвестная нода: %s", node)
	}
	removed, err := a.subs.Remove(node, url)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("подписка не найдена среди назначенных ноде")
	}
	return nil
}
```

`node_manager_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"

	"xray-checker/nodes"
)

func TestNodeManagerAdapter(t *testing.T) {
	reg := nodes.NewRegistry([]nodes.NodeConfig{{Name: "n1", Token: "t"}}, nil, nil)
	subs, err := nodes.NewNodeSubsStore(filepath.Join(t.TempDir(), "subs.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &nodeManagerAdapter{reg: reg, subs: subs}

	if err := a.AddSub("nope", "https://x/y"); err == nil {
		t.Error("unknown node must error")
	}
	if err := a.AddSub("n1", "https://x/y"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddSub("n1", "https://x/y"); err == nil {
		t.Error("duplicate must error")
	}
	got, err := a.ManagedSubs("n1")
	if err != nil || len(got) != 1 || got[0].ProxyCount != -1 {
		t.Fatalf("ManagedSubs: %v %v", got, err)
	}
	if err := a.RemoveSub("n1", "https://x/y"); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveSub("n1", "https://x/y"); err == nil {
		t.Error("removing twice must error")
	}
}
```

- [ ] **Step 2: Run adapter tests**

Run: `go test . -v`
Expected: PASS.

- [ ] **Step 3: Wire main.go**

In `main.go`:

1. Add imports: `"xray-checker/asn"`, `"xray-checker/nodes"`.

2. After the `subURLStore` creation (~line 64) and before `InitializeConfiguration`, add:

```go
	var nodeRegistry *nodes.Registry
	var nodeSubsStore *nodes.NodeSubsStore
	if len(config.CLIConfig.Nodes.List) > 0 {
		nodeCfgs, err := nodes.ParseNodes(config.CLIConfig.Nodes.List)
		if err != nil {
			logger.Fatal("Invalid NODES entry: %v", err)
		}
		nodeSubsStore, err = nodes.NewNodeSubsStore(config.CLIConfig.NodesStorePath)
		if err != nil {
			logger.Fatal("Error loading node subscriptions store: %v", err)
		}
		asnLookup := asn.NopLookup
		asnPath := "geo/asn.mmdb"
		if err := asn.EnsureDB(asnPath, config.CLIConfig.ASN.DBURL); err != nil {
			logger.Warn("ASN database unavailable (node ASN will be empty): %v", err)
		} else if db, oerr := asn.Open(asnPath); oerr != nil {
			logger.Warn("ASN database failed to open (node ASN will be empty): %v", oerr)
		} else {
			defer db.Close()
			asnLookup = db.Lookup
		}
		nodeRegistry = nodes.NewRegistry(nodeCfgs, nodeSubsStore, asnLookup)
		logger.Info("Remote nodes configured: %d", len(nodeCfgs))
	}
```

3. Next to the `var tgBot *telegram.Bot` block (~line 132), add:

```go
	var reporter *nodes.Reporter
	var reporterMu sync.Mutex
	if config.CLIConfig.Report.URL != "" {
		reporter = nodes.NewReporter(config.CLIConfig.Report.URL, config.CLIConfig.Report.Token)
	}
```

4. Replace the body of the snapshot hand-off inside `runCheckIteration` — currently:

```go
		if tgBot != nil {
			tgBot.ProcessSnapshot(proxyChecker.MetricsSnapshot())
		}
```

with:

```go
		emitSnapshot()
```

and define `emitSnapshot` next to `runCheckIteration` (before it, following the same declare-early pattern the file already uses):

```go
	// emitSnapshot feeds the bot ONE merged snapshot: local proxies plus every
	// reporting node. ProcessSnapshot drops state for absent IDs, so local and
	// remote snapshots must never be fed separately.
	emitSnapshot := func() {
		if tgBot == nil {
			return
		}
		snap := proxyChecker.MetricsSnapshot()
		if nodeRegistry != nil {
			snap = nodeRegistry.MergedSnapshot(snap)
		}
		tgBot.ProcessSnapshot(snap)
	}

	if nodeRegistry != nil {
		nodeRegistry.SetOnUpdate(func() {
			emitSnapshot()
			if tgBot != nil {
				tgBot.ProcessNodesHealth(toNodeInfos(nodeRegistry.HealthSnapshot()))
			}
		})
	}
```

At the end of `runCheckIteration` (after the metrics push block), add the report hook:

```go
		if reporter != nil {
			go func() {
				reporterMu.Lock()
				defer reporterMu.Unlock()
				hostIP, err := proxyChecker.GetCurrentIP()
				if err != nil {
					hostIP = ""
				}
				checkSchedulerMu.Lock()
				interval := config.CLIConfig.Proxy.CheckInterval
				checkSchedulerMu.Unlock()
				payload := nodes.BuildReport(
					proxyChecker.MetricsSnapshot(), version, interval,
					config.CLIConfig.Proxy.CheckMethod, hostIP,
				)
				desired, err := reporter.Send(payload)
				if err != nil {
					logger.Warn("Report to master failed (next cycle will retry): %v", err)
					return
				}
				if reconcileDesired != nil {
					if err := reconcileDesired(desired); err != nil {
						logger.Error("Reconciling managed subscriptions failed: %v", err)
					}
				}
			}()
		}
```

Note on declaration order: `runCheckIteration` is defined in `main` **before** `reloadSubscriptions`, so the goroutine cannot reference it directly (it wouldn't compile). Declare the indirection next to `var runCheckIteration func()` (~line 136):

```go
	// reconcileDesired applies the master's desired managed-subscription list
	// on the node side; assigned after reloadSubscriptions is defined below.
	var reconcileDesired func(desired []string) error
```

and immediately after the `reloadSubscriptions := func() ...` definition (~line 263), assign it:

```go
	reconcileDesired = func(desired []string) error {
		return subscription.ReconcileManaged(subURLStore, desired, reloadSubscriptions)
	}
```

5. Inside the bot setup block, after `bot.SetDiagnosticsSource(proxyChecker)` (or beside the other `bot.Set*` calls, before `tgBot = bot`):

```go
					if nodeRegistry != nil {
						bot.SetNodeManager(&nodeManagerAdapter{reg: nodeRegistry, subs: nodeSubsStore})
					}
```

(matching that block's indentation).

6. After the subscription update scheduler (~line 404), add the sweeper:

```go
	if nodeRegistry != nil {
		sweepScheduler := gocron.NewScheduler(time.UTC)
		sweepScheduler.Every(30).Seconds().SingletonMode().Do(func() {
			names := nodeRegistry.SweepStale(time.Now())
			for _, name := range names {
				logger.Warn("Node %s: no reports within deadline, marking down", name)
			}
			if len(names) > 0 {
				emitSnapshot()
				if tgBot != nil {
					tgBot.ProcessNodesHealth(toNodeInfos(nodeRegistry.HealthSnapshot()))
				}
			}
		})
		sweepScheduler.StartAsync()
	}
```

7. Mount the endpoints. Right after `mux.Handle("/health", ...)`:

```go
	if nodeRegistry != nil {
		// Bearer-token auth of its own — deliberately not behind the metrics
		// basic auth, which nodes must not need to know.
		mux.Handle("/api/v1/nodes/report", nodeRegistry.HandleReport())
	}
```

And inside the `if config.CLIConfig.Web.Enabled {` block, after `web.RegisterConfigEndpoints(...)`:

```go
		if nodeRegistry != nil {
			protectedHandler.Handle("/api/v1/nodes", web.APINodesHandler(nodeRegistry))
		}
```

- [ ] **Step 4: Build and full regression**

Run: `go build ./... && go vet ./... && gofmt -l . | grep -v docs/ ; go test ./... -race`
Expected: build OK, vet clean, no gofmt findings outside `docs/`, all tests PASS.

- [ ] **Step 5: README sections**

In `README.md`, add a `## Remote nodes` section (after the Telegram bot section, matching its heading level and tone):

```markdown
## Remote nodes

Run additional headless instances of this image (no `TELEGRAM_BOT_TOKEN`) and
have them push check results to the master instance that owns the bot:

- On each node: set `REPORT_URL=https://master:2112/api/v1/nodes/report` and
  `REPORT_TOKEN` (no inbound ports required; works behind NAT).
- On the master: list nodes in `NODES=name|token,other|token`. The master
  alerts on every node's proxies (`[node] name` identity), alerts when a node
  stops reporting, and shows each node's ASN (`ASN_DB_URL`, db-ip asn-lite).
- Bot commands: `/nodes`, `/nodesubs <name>`, `/nodeaddsub <name> <url>`,
  `/nodedelsub <name> <url>` — subscriptions are delivered to the node in the
  response to its next report and applied locally (validated, reload-rolled
  back on failure).

The ingest endpoint uses bearer-token auth; run it behind HTTPS (reverse
proxy) when nodes report over the public internet. See
`docs/superpowers/specs/2026-09-17-nodes-push-design.md` for the full design.
```

Add the equivalent section in Russian to `README_RU.md` (same content, translated in the file's existing voice).

- [ ] **Step 6: Commit**

```bash
git add main.go node_manager.go node_manager_test.go README.md README_RU.md
git commit -m "feat(nodes): wire master ingest/sweeper/reporter and document remote nodes"
```

---

## Final verification (after all tasks)

- [ ] `go build ./... && go vet ./... && go test ./... -race` — green.
- [ ] Smoke (optional, manual): run master with `NODES=test|devtoken` + `METRICS_PORT=2112`; `curl -X POST localhost:2112/api/v1/nodes/report -H "Authorization: Bearer devtoken" -d '{"version":"t","checkIntervalSec":60,"proxies":[{"stableId":"a","name":"p","online":true}]}'` → `{"managedSubs":[]}`; `curl localhost:2112/api/v1/nodes` shows the node up.
- [ ] With empty `NODES`/`REPORT_URL`: startup logs and `/metrics` identical to `main` branch behavior.
