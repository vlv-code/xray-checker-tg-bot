package nodes

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"xray-checker/checker"
	"xray-checker/logger"
	"xray-checker/metrics"
)

// Stale configuration: a node is down after this much silence.
const (
	StaleMultiplier = 2                // × reported CheckIntervalSec
	StaleGrace      = 60 * time.Second // fixed grace on top
	SweepInterval   = 30 * time.Second // master sweeper period
	MaxReportBytes  = 5 << 20          // ingest body limit
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

// nodeState is the registry's per-node runtime state. Invariant: cfg.Token
// always holds the canonical sha256 hex hash of the node's bearer token —
// never plaintext — regardless of node origin (static NODES config,
// store-loaded, dynamically registered). byToken keys and cfg.Token must
// stay in the same representation: RemoveNode revokes a token by deleting
// the byToken key stored in cfg.Token.
type nodeState struct {
	cfg          NodeConfig
	status       nodeStatus
	lastReport   time.Time
	intervalSec  int
	version      string
	hostIP       string
	asn          string
	snapshot     []metrics.ProxyMetric
	diagReports  []checker.ProxyDiagReport
	gracePending bool // emit last snapshot with Disabled=true exactly once
}

// NodeHealth is the master's point-in-time view of one node. Enabled is
// false when the node was toggled off (Wave G5b): it stays listed but is
// excluded from MergedSnapshot, so no metrics or alerts flow for it.
type NodeHealth struct {
	Name             string    `json:"name"`
	Up               bool      `json:"up"`
	Enabled          bool      `json:"enabled"`
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
	mu         sync.RWMutex
	nodes      map[string]*nodeState // by name
	byToken    map[string]string     // token -> name
	static     map[string]bool       // NODES-env names: config-owned (rename rejected, toggle runtime-only)
	subs       NodeSubsSource
	asn        LookupFunc
	onUpdate   func(nodeName string)
	cfgSource  func(nodeName string) *NodeConfigSync
	nodesStore *NodesStore
	staleCap   time.Duration // 0 = no cap; use node's reported interval
	// audits holds the latest background Check-Host audit results per node,
	// keyed by target address (bare host for UDP targets).
	audits map[string]map[string]checker.CheckHostSummary
}

// NewRegistry builds a registry for the given node configs. subs may be nil
// (empty managed lists); asn may be nil (no ASN enrichment).
func NewRegistry(cfgs []NodeConfig, subs NodeSubsSource, asn LookupFunc) *Registry {
	r := &Registry{
		nodes:   make(map[string]*nodeState, len(cfgs)),
		byToken: make(map[string]string, len(cfgs)),
		static:  make(map[string]bool, len(cfgs)),
		subs:    subs,
		asn:     asn,
		audits:  make(map[string]map[string]checker.CheckHostSummary),
	}
	for _, cfg := range cfgs {
		// Canonical representation: store the token hashed so cfg.Token and
		// byToken keys match (cfg is a range copy — caller's slice untouched).
		cfg.Token = HashToken(cfg.Token)
		r.nodes[cfg.Name] = &nodeState{cfg: cfg, status: statusPending}
		r.byToken[cfg.Token] = cfg.Name
		r.static[cfg.Name] = true
	}
	return r
}

// SetNodesStore sets the persistence store for dynamic node registrations and loads any stored nodes.
func (r *Registry) SetNodesStore(store *NodesStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodesStore = store
	if store != nil {
		for _, cfg := range store.All() {
			if _, exists := r.nodes[cfg.Name]; !exists {
				// Never silently remap an existing node's token identity to a
				// different name: skip the colliding store record instead.
				if owner, collides := r.byToken[cfg.Token]; collides && owner != cfg.Name {
					logger.Warn("Nodes store record %q skipped: its token is already owned by node %q", cfg.Name, owner)
					continue
				}
				r.nodes[cfg.Name] = &nodeState{cfg: cfg, status: statusPending}
				r.byToken[cfg.Token] = cfg.Name
			}
		}
	}
}

// RegisterNode dynamically registers a new node with name and token, persisting it if a store is configured.
func (r *Registry) RegisterNode(cfg NodeConfig) error {
	name := strings.TrimSpace(cfg.Name)
	token := strings.TrimSpace(cfg.Token)
	if name == "" || token == "" {
		return fmt.Errorf("имя и токен ноды не могут быть пустыми")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	tokenHash := HashToken(token)
	if _, exists := r.nodes[name]; exists {
		return fmt.Errorf("нода с именем %q уже существует", name)
	}
	if _, exists := r.byToken[tokenHash]; exists {
		return fmt.Errorf("нода с таким токеном уже существует")
	}

	r.nodes[name] = &nodeState{cfg: NodeConfig{Name: name, Token: tokenHash}, status: statusPending}
	r.byToken[tokenHash] = name

	if r.nodesStore != nil {
		if err := r.nodesStore.Save(name, token); err != nil {
			return fmt.Errorf("saving node to store: %w", err)
		}
	}

	if r.onUpdate != nil {
		go r.onUpdate("")
	}
	return nil
}

// RemoveNode unregisters a node dynamically and deletes it from store.
func (r *Registry) RemoveNode(name string) error {
	name = strings.TrimSpace(name)
	r.mu.Lock()
	defer r.mu.Unlock()

	st, exists := r.nodes[name]
	if !exists {
		return fmt.Errorf("нода %q не найдена", name)
	}

	delete(r.byToken, st.cfg.Token)
	delete(r.nodes, name)

	if r.nodesStore != nil {
		if err := r.nodesStore.Delete(name); err != nil {
			return fmt.Errorf("deleting node from store: %w", err)
		}
	}

	if r.onUpdate != nil {
		go r.onUpdate("")
	}
	return nil
}

// RenameNode migrates a dynamic node to a new name: the in-memory identity
// (nodes map + byToken with the SAME token — auth keeps working), the
// persisted store record (token hash, settings overrides, enabled state),
// and the managed-subscription keys. Static NODES-env nodes are config-owned
// and rejected. Wave G5a.
func (r *Registry) RenameNode(oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)

	r.mu.Lock()
	defer r.mu.Unlock()

	if oldName == "" || newName == "" {
		return fmt.Errorf("имя ноды не может быть пустым")
	}
	st, exists := r.nodes[oldName]
	if !exists {
		return fmt.Errorf("нода %q не найдена", oldName)
	}
	if r.static[oldName] {
		return fmt.Errorf("нода %q — статическая (NODES): переименование выполняется в конфиге, а не через бота", oldName)
	}
	if newName == oldName {
		return fmt.Errorf("новое имя совпадает с текущим именем ноды")
	}
	if reservedNodeNames[newName] {
		return fmt.Errorf("имя ноды %q зарезервировано — выберите другое", newName)
	}
	if _, exists := r.nodes[newName]; exists {
		return fmt.Errorf("нода с именем %q уже существует", newName)
	}

	// 1. In-memory migration (same token → byToken re-points, auth intact).
	token := st.cfg.Token
	st.cfg.Name = newName
	delete(r.nodes, oldName)
	r.nodes[newName] = st
	r.byToken[token] = newName

	rollbackMemory := func() {
		delete(r.nodes, newName)
		st.cfg.Name = oldName
		r.nodes[oldName] = st
		r.byToken[token] = oldName
	}

	// 2. Managed-subscription keys (NodeSubsStore.Rename rolls itself back
	// on persist failure; nil subs or a custom source skips this step).
	if s, ok := r.subs.(*NodeSubsStore); ok {
		if err := s.Rename(oldName, newName); err != nil {
			rollbackMemory()
			return fmt.Errorf("renaming node subscriptions: %w", err)
		}
	}

	// 3. Persisted record (dynamic nodes only — static nodes were rejected
	// above). On failure: roll back the subs rename (best effort) and memory.
	if r.nodesStore != nil {
		if err := r.nodesStore.Rename(oldName, newName); err != nil {
			if s, ok := r.subs.(*NodeSubsStore); ok {
				if rbErr := s.Rename(newName, oldName); rbErr != nil {
					logger.Warn("Registry: failed to roll back subscription keys after failed node rename %q→%q: %v", oldName, newName, rbErr)
				}
			}
			rollbackMemory()
			return fmt.Errorf("renaming node in store: %w", err)
		}
	}

	logger.Info("Node renamed: %s -> %s (token, settings and subscriptions migrated)", oldName, newName)
	if r.onUpdate != nil {
		go r.onUpdate("")
	}
	return nil
}

// SetNodeEnabled toggles a node's participation in merged metrics and node
// alerts: a disabled node is excluded from MergedSnapshot (no proxy metrics,
// no alerts) but stays visible in HealthSnapshot. Works for static NODES
// nodes too (runtime-only, nothing is persisted for config-owned nodes);
// dynamic nodes persist the flag in the nodes store. Wave G5b.
func (r *Registry) SetNodeEnabled(name string, enabled bool) error {
	name = strings.TrimSpace(name)

	r.mu.Lock()
	defer r.mu.Unlock()

	st, exists := r.nodes[name]
	if !exists {
		return fmt.Errorf("нода %q не найдена", name)
	}
	old := st.cfg.Enabled
	v := enabled
	st.cfg.Enabled = &v
	if !r.static[name] && r.nodesStore != nil {
		if err := r.nodesStore.SetEnabled(name, enabled); err != nil {
			st.cfg.Enabled = old // roll back runtime on persist failure
			return fmt.Errorf("saving node enabled state: %w", err)
		}
	}
	if enabled {
		logger.Info("Node %s enabled (back in metrics and alerts)", name)
	} else {
		logger.Info("Node %s disabled (excluded from metrics and alerts)", name)
	}
	if r.onUpdate != nil {
		go r.onUpdate("")
	}
	return nil
}

// SetOnUpdate registers a callback fired after every accepted report and
// every health transition — the master re-feeds ProcessSnapshot then. The
// callback receives the reporting node's name ("" for structural changes).
func (r *Registry) SetOnUpdate(f func(nodeName string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onUpdate = f
}

// SetConfigSource registers a provider for configuration synchronized to nodes.
func (r *Registry) SetConfigSource(f func(nodeName string) *NodeConfigSync) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfgSource = f
}

// NodeExists reports whether name is a configured node.
func (r *Registry) NodeExists(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.nodes[name]
	return ok
}

// HandleReport returns the HTTP handler for POST /api/v1/nodes/report.
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
		diag := ProxyDiagReportsFromReport(payload)

		r.mu.Lock()
		st, ok := r.nodes[name]
		if !ok {
			// Token resolved to a name that no longer exists (ghost byToken
			// entry, e.g. after a concurrent RemoveNode): fail closed.
			r.mu.Unlock()
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		st.status = statusUp
		st.lastReport = time.Now()
		st.intervalSec = payload.CheckIntervalSec
		st.version = payload.Version
		st.hostIP = payload.HostIP
		st.asn = asn
		st.snapshot = snap
		st.diagReports = diag
		st.gracePending = false
		cb := r.onUpdate
		if payload.CheckHostAudits != nil {
			r.audits[name] = payload.CheckHostAudits
		} else {
			delete(r.audits, name)
		}
		r.mu.Unlock()

		logger.Info("Node %s: report accepted (%d proxies, %s)", name, len(payload.Proxies), payload.Version)
		if cb != nil {
			go cb(name)
		}

		w.Header().Set("Content-Type", "application/json")
		var managed []string
		if r.subs != nil {
			managed = r.subs.ManagedSubsFor(name)
		}
		var cfgSync *NodeConfigSync
		r.mu.RLock()
		cf := r.cfgSource
		r.mu.RUnlock()
		if cf != nil {
			cfgSync = cf(name)
		}
		json.NewEncoder(w).Encode(IngestResponse{
			ManagedSubs: managed,
			ConfigSync:  cfgSync,
		})
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
	receivedHash := HashToken(token)
	name, ok := "", false
	r.mu.RLock()
	for storedHash, n := range r.byToken {
		if subtle.ConstantTimeCompare([]byte(storedHash), []byte(receivedHash)) == 1 {
			name, ok = n, true
		}
	}
	r.mu.RUnlock()
	return name, ok
}

// MergedSnapshot returns local plus every up node's snapshot. A node that
// just went down contributes its last snapshot once with Disabled=true (the
// alert pipeline then cleans its active alerts), then disappears.
// Uses a write lock for the entire operation to guarantee atomic reading and
// clearing of gracePending, preventing race conditions with incoming HandleReport calls.
func (r *Registry) MergedSnapshot(local []metrics.ProxyMetric) []metrics.ProxyMetric {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]metrics.ProxyMetric, 0, len(local)+16)
	out = append(out, local...)
	for _, name := range r.sortedNamesLocked() {
		st := r.nodes[name]
		// Wave G5b: a disabled node contributes nothing to the merged
		// snapshot — no metrics, hence no proxy alerts from the pipeline.
		if !nodeEnabled(st.cfg) {
			continue
		}
		switch st.status {
		case statusUp:
			out = append(out, st.snapshot...)
		case statusDown:
			if st.gracePending {
				for _, pm := range st.snapshot {
					pm.Disabled = true
					out = append(out, pm)
				}
				st.gracePending = false
			}
		}
	}
	return out
}

// NodeSnapshot returns a copy of the latest proxy snapshot reported by the specified node.
// Returns nil if the node does not exist or has never reported a snapshot.
func (r *Registry) NodeSnapshot(name string) []metrics.ProxyMetric {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.nodes[name]
	if !ok || len(st.snapshot) == 0 {
		return nil
	}
	out := make([]metrics.ProxyMetric, len(st.snapshot))
	copy(out, st.snapshot)
	return out
}

// cloneCheckHostSummary duplicates a summary with a private Results slice so
// callers can never reach stored audit/diag state through a shared backing
// array.
func cloneCheckHostSummary(s checker.CheckHostSummary) checker.CheckHostSummary {
	if len(s.Results) > 0 {
		s.Results = append([]checker.CheckHostNodeResult(nil), s.Results...)
	}
	return s
}

// cloneDiagReport deep-copies one report's mutable nested state: the Targets
// slice and the pointed CheckHost summary (value copy plus private Results).
// nil Targets/Results stay nil so wire and view rendering is unchanged.
func cloneDiagReport(rep checker.ProxyDiagReport) checker.ProxyDiagReport {
	if len(rep.Targets) > 0 {
		rep.Targets = append([]checker.TargetDiagResult(nil), rep.Targets...)
	}
	if rep.CheckHost != nil {
		ch := cloneCheckHostSummary(*rep.CheckHost)
		rep.CheckHost = &ch
	}
	return rep
}

// NodeDiagReports returns a copy of the latest diagnostic reports reported by the specified node.
// Returns nil if the node does not exist or has never reported any diagnostic reports.
func (r *Registry) NodeDiagReports(name string) []checker.ProxyDiagReport {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.nodes[name]
	if !ok || len(st.diagReports) == 0 {
		return nil
	}
	out := make([]checker.ProxyDiagReport, len(st.diagReports))
	for i, rep := range st.diagReports {
		out[i] = cloneDiagReport(rep)
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
			Enabled:          nodeEnabled(st.cfg),
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

// SetStaleCap sets the master-side maximum silence interval. 0 means
// "use whatever the node reports in CheckIntervalSec" (backward-compatible).
func (r *Registry) SetStaleCap(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.staleCap = d
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
		interval := st.intervalSec
		if r.staleCap > 0 {
			capSec := int(r.staleCap / time.Second)
			if interval > capSec {
				interval = capSec
			}
		}
		deadline := time.Duration(StaleMultiplier*interval)*time.Second + StaleGrace
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

// NodeAudits returns a copy of the node's latest Check-Host audit results;
// an empty map when the node never delivered audits. Each summary carries a
// private Results slice — callers cannot corrupt stored audit state.
func (r *Registry) NodeAudits(node string) map[string]checker.CheckHostSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]checker.CheckHostSummary, len(r.audits[node]))
	for k, v := range r.audits[node] {
		out[k] = cloneCheckHostSummary(v)
	}
	return out
}
