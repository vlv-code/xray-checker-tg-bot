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
	mu         sync.RWMutex
	nodes      map[string]*nodeState // by name
	byToken    map[string]string     // token -> name
	subs       NodeSubsSource
	asn        LookupFunc
	onUpdate   func()
	cfgSource  func(nodeName string) *NodeConfigSync
	nodesStore *NodesStore
	staleCap   time.Duration // 0 = no cap; use node's reported interval
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
		r.byToken[HashToken(cfg.Token)] = cfg.Name
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
		go r.onUpdate()
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
		go r.onUpdate()
	}
	return nil
}

// SetOnUpdate registers a callback fired after every accepted report and
// every health transition — the master re-feeds ProcessSnapshot then.
func (r *Registry) SetOnUpdate(f func()) {
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
		st := r.nodes[name]
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
		r.mu.Unlock()

		logger.Info("Node %s: report accepted (%d proxies, %s)", name, len(payload.Proxies), payload.Version)
		if cb != nil {
			go cb()
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
	copy(out, st.diagReports)
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
