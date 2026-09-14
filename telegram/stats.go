package telegram

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Incident records a downtime event for a proxy.
type Incident struct {
	ProxyName   string `json:"proxy_name"`
	StableID    string `json:"stable_id"`
	DownAt      int64  `json:"down_at"`      // Unix timestamp
	UpAt        int64  `json:"up_at"`        // Unix timestamp, 0 if still down
	DurationSec int64  `json:"duration_sec"` // Total seconds in downtime
	Reason      string `json:"reason"`
}

// ProxyStats holds aggregated check numbers for one proxy.
type ProxyStats struct {
	ProxyName        string `json:"proxy_name"`
	TotalChecks      int64  `json:"total_checks"`
	SuccessfulChecks int64  `json:"successful_checks"`
	TotalDowntimeSec int64  `json:"total_downtime_sec"`
	DropCount        int64  `json:"drop_count"`
	LastCheckAt      int64  `json:"last_check_at"`
	CurrentlyDown    bool   `json:"currently_down"`
	CurrentDownAt    int64  `json:"current_down_at"`
}

// StatsStore manages historical statistics and incident logs.
type StatsStore struct {
	mu           sync.RWMutex
	path         string
	Incidents    []Incident                   `json:"incidents"` // Most recent first
	Stats        map[string]*ProxyStats       `json:"stats"`     // stableID -> stats
	Transitions  map[string][]TransitionEvent `json:"transitions,omitempty"`
	rollingStats *RollingStats
	latencyMap   map[string]*LatencySamples
	latMu        sync.RWMutex
}

// NewStatsStore loads or creates a new StatsStore.
func NewStatsStore(path string) (*StatsStore, error) {
	ss := &StatsStore{
		path:         path,
		Incidents:    make([]Incident, 0),
		Stats:        make(map[string]*ProxyStats),
		Transitions:  make(map[string][]TransitionEvent),
		rollingStats: NewRollingStats(1000),
		latencyMap:   make(map[string]*LatencySamples),
	}

	if err := ss.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to load stats: %w", err)
	}

	return ss, nil
}

func (ss *StatsStore) load() error {
	if ss.path == "" {
		return nil
	}
	data, err := os.ReadFile(ss.path)
	if err != nil {
		return err
	}

	var dataStore struct {
		Incidents   []Incident                   `json:"incidents"`
		Stats       map[string]*ProxyStats       `json:"stats"`
		Transitions map[string][]TransitionEvent `json:"transitions,omitempty"`
	}

	if err := json.Unmarshal(data, &dataStore); err != nil {
		return err
	}

	if dataStore.Incidents != nil {
		ss.Incidents = dataStore.Incidents
	}
	if dataStore.Stats != nil {
		ss.Stats = dataStore.Stats
	}
	if dataStore.Transitions != nil {
		ss.Transitions = dataStore.Transitions
		for id, evs := range dataStore.Transitions {
			for _, ev := range evs {
				ss.rollingStats.Record(id, ev.Online, ev.Timestamp)
			}
		}
	}
	return nil
}

// RecordCheck updates continuous check metrics for a proxy.
func (ss *StatsStore) RecordCheck(stableID, name string, online bool, latencyMs float64) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ps, exists := ss.Stats[stableID]
	if !exists {
		ps = &ProxyStats{
			ProxyName: name,
		}
		ss.Stats[stableID] = ps
	}
	ps.ProxyName = name
	ps.TotalChecks++
	if online {
		ps.SuccessfulChecks++
	}
	ps.LastCheckAt = time.Now().Unix()
	if latencyMs > 0 {
		ss.GetLatencySamples(stableID).Add(latencyMs)
	}
}

// RecordInitialDown marks a proxy as down if it is already failing on the initial check iteration.
func (ss *StatsStore) RecordInitialDown(stableID, name string, timestamp time.Time) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ps, exists := ss.Stats[stableID]
	if !exists {
		ps = &ProxyStats{
			ProxyName: name,
		}
		ss.Stats[stableID] = ps
	}
	ps.ProxyName = name

	if !ps.CurrentlyDown {
		ps.CurrentlyDown = true
		ps.CurrentDownAt = timestamp.Unix()
		ps.DropCount++

		if ss.rollingStats != nil {
			ss.rollingStats.Record(stableID, false, timestamp.Unix())
		}

		incident := Incident{
			ProxyName: name,
			StableID:  stableID,
			DownAt:    timestamp.Unix(),
			UpAt:      0,
			Reason:    "Offline at startup",
		}
		ss.Incidents = append([]Incident{incident}, ss.Incidents...)
		if len(ss.Incidents) > 100 {
			ss.Incidents = ss.Incidents[:100]
		}
	}
}

// RecordTransition logs a state transition (down or up).
func (ss *StatsStore) RecordTransition(stableID, name string, online bool, reason string, timestamp time.Time) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ps, exists := ss.Stats[stableID]
	if !exists {
		ps = &ProxyStats{
			ProxyName: name,
		}
		ss.Stats[stableID] = ps
	}
	ps.ProxyName = name

	ts := timestamp.Unix()
	if ss.rollingStats != nil {
		ss.rollingStats.Record(stableID, online, ts)
	}

	if !online {
		// Went down
		ps.DropCount++
		ps.CurrentlyDown = true
		ps.CurrentDownAt = ts

		incident := Incident{
			ProxyName: name,
			StableID:  stableID,
			DownAt:    ts,
			UpAt:      0,
			Reason:    reason,
		}
		// Prepend so newest is first
		ss.Incidents = append([]Incident{incident}, ss.Incidents...)
		if len(ss.Incidents) > 100 {
			ss.Incidents = ss.Incidents[:100]
		}
	} else {
		// Recovered
		if ps.CurrentlyDown {
			duration := ts - ps.CurrentDownAt
			if duration < 0 {
				duration = 0
			}
			ps.TotalDowntimeSec += duration
			ps.CurrentlyDown = false
			ps.CurrentDownAt = 0

			// Close open incident in incidents list
			for i := range ss.Incidents {
				if ss.Incidents[i].StableID == stableID && ss.Incidents[i].UpAt == 0 {
					ss.Incidents[i].UpAt = ts
					ss.Incidents[i].DurationSec = ts - ss.Incidents[i].DownAt
					break
				}
			}
		}
	}
}

// GetUptimePercent returns the uptime percentage (0-100) for a proxy.
func (ss *StatsStore) GetUptimePercent(stableID string) float64 {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	ps, ok := ss.Stats[stableID]
	if !ok || ps.TotalChecks == 0 {
		return 100.0
	}

	return (float64(ps.SuccessfulChecks) / float64(ps.TotalChecks)) * 100.0
}

// GetRecentIncidents returns the most recent N incidents.
func (ss *StatsStore) GetRecentIncidents(limit int) []Incident {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	if limit <= 0 || limit > len(ss.Incidents) {
		limit = len(ss.Incidents)
	}

	res := make([]Incident, limit)
	copy(res, ss.Incidents[:limit])
	return res
}

// RecordLatency appends a latency sample for a given stableID.
func (ss *StatsStore) RecordLatency(stableID string, latencyMs float64) {
	if ss == nil {
		return
	}
	ss.GetLatencySamples(stableID).Add(latencyMs)
}

// GetFlapCount24h returns the number of online/offline transitions in the last 24 hours.
func (ss *StatsStore) GetFlapCount24h(stableID string, now time.Time) int {
	if ss == nil || ss.rollingStats == nil {
		return 0
	}
	return ss.rollingStats.FlapCount24h(stableID, now)
}

// TopProblematic holds drop statistics for a proxy.
type TopProblematic struct {
	ProxyName   string
	StableID    string
	DropCount   int64
	DowntimeSec int64
	UptimePct   float64
}

// GetTopProblematic returns proxies sorted by drop count descending.
func (ss *StatsStore) GetTopProblematic(limit int) []TopProblematic {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	list := make([]TopProblematic, 0, len(ss.Stats))
	for id, ps := range ss.Stats {
		var uptime float64 = 100.0
		if ps.TotalChecks > 0 {
			uptime = (float64(ps.SuccessfulChecks) / float64(ps.TotalChecks)) * 100.0
		}
		list = append(list, TopProblematic{
			ProxyName:   ps.ProxyName,
			StableID:    id,
			DropCount:   ps.DropCount,
			DowntimeSec: ps.TotalDowntimeSec,
			UptimePct:   uptime,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].DropCount != list[j].DropCount {
			return list[i].DropCount > list[j].DropCount
		}
		return list[i].DowntimeSec > list[j].DowntimeSec
	})

	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list
}

// Save flushes state to disk.
func (ss *StatsStore) Save() error {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	if ss.path == "" {
		return nil
	}

	dir := filepath.Dir(ss.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	var transitions map[string][]TransitionEvent
	if ss.rollingStats != nil {
		transitions = ss.rollingStats.Snapshot()
	}

	dataStore := struct {
		Incidents   []Incident                   `json:"incidents"`
		Stats       map[string]*ProxyStats       `json:"stats"`
		Transitions map[string][]TransitionEvent `json:"transitions,omitempty"`
	}{
		Incidents:   ss.Incidents,
		Stats:       ss.Stats,
		Transitions: transitions,
	}

	data, err := json.MarshalIndent(dataStore, "", "  ")
	if err != nil {
		return err
	}

	tmp := ss.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, ss.path)
}

// GetLatencySamples returns the latency ring buffer for a given proxy.
func (ss *StatsStore) GetLatencySamples(stableID string) *LatencySamples {
	ss.latMu.Lock()
	defer ss.latMu.Unlock()
	if ss.latencyMap == nil {
		ss.latencyMap = make(map[string]*LatencySamples)
	}
	ls, ok := ss.latencyMap[stableID]
	if !ok {
		ls = NewLatencySamples(50)
		ss.latencyMap[stableID] = ls
	}
	return ls
}

// GetRollingStats returns the internal RollingStats instance.
func (ss *StatsStore) GetRollingStats() *RollingStats {
	return ss.rollingStats
}

// TransitionEvent records a state transition (online/offline) with timestamp.
type TransitionEvent struct {
	Timestamp int64 `json:"ts"`
	Online    bool  `json:"online"`
}

// RollingStats maintains transition events in a ring buffer to calculate
// sliding window metrics (24h/7d uptime, flapping, MTBF/MTTR).
type RollingStats struct {
	mu      sync.RWMutex
	events  map[string][]TransitionEvent
	maxSize int
}

// NewRollingStats creates a RollingStats instance.
func NewRollingStats(maxSize int) *RollingStats {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &RollingStats{
		events:  make(map[string][]TransitionEvent),
		maxSize: maxSize,
	}
}

// Record appends a new state event for a proxy.
func (r *RollingStats) Record(stableID string, online bool, ts int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	evs := r.events[stableID]
	if len(evs) > 0 && evs[len(evs)-1].Online == online {
		return
	}

	evs = append(evs, TransitionEvent{Timestamp: ts, Online: online})
	if len(evs) > r.maxSize {
		evs = evs[len(evs)-r.maxSize:]
	}
	r.events[stableID] = evs
}

// Snapshot returns a copy of current transition events.
func (r *RollingStats) Snapshot() map[string][]TransitionEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string][]TransitionEvent, len(r.events))
	for id, evs := range r.events {
		cp := make([]TransitionEvent, len(evs))
		copy(cp, evs)
		out[id] = cp
	}
	return out
}

// UptimePercent computes uptime % over a rolling time window ending at `now`.
func (r *RollingStats) UptimePercent(stableID string, window time.Duration, now time.Time) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	evs, ok := r.events[stableID]
	if !ok || len(evs) == 0 {
		return 100.0
	}

	nowSec := now.Unix()
	windowSec := int64(window.Seconds())
	if windowSec <= 0 {
		windowSec = 86400
	}
	windowStart := nowSec - windowSec

	firstEv := evs[0]
	actualStart := windowStart
	if firstEv.Timestamp > actualStart {
		actualStart = firstEv.Timestamp
	}
	totalSpan := nowSec - actualStart
	if totalSpan <= 0 {
		if evs[len(evs)-1].Online {
			return 100.0
		}
		return 0.0
	}

	var downtimeSec int64

	// Find starting state at actualStart
	stateAtStart := true
	for _, ev := range evs {
		if ev.Timestamp <= actualStart {
			stateAtStart = ev.Online
		} else {
			break
		}
	}

	curState := stateAtStart
	lastTs := actualStart

	for _, ev := range evs {
		if ev.Timestamp <= actualStart {
			continue
		}
		if ev.Timestamp > nowSec {
			break
		}

		if !curState {
			downtimeSec += (ev.Timestamp - lastTs)
		}
		curState = ev.Online
		lastTs = ev.Timestamp
	}

	if !curState && nowSec > lastTs {
		downtimeSec += (nowSec - lastTs)
	}

	if downtimeSec > totalSpan {
		downtimeSec = totalSpan
	}
	uptimeSec := totalSpan - downtimeSec
	return (float64(uptimeSec) / float64(totalSpan)) * 100.0
}

func (r *RollingStats) UptimePercent24h(stableID string, now time.Time) float64 {
	return r.UptimePercent(stableID, 24*time.Hour, now)
}

func (r *RollingStats) UptimePercent7d(stableID string, now time.Time) float64 {
	return r.UptimePercent(stableID, 7*24*time.Hour, now)
}

func (r *RollingStats) UptimePercent24hAll(now time.Time, activeIDs []string) float64 {
	if len(activeIDs) == 0 {
		return 100.0
	}
	var total float64
	for _, id := range activeIDs {
		total += r.UptimePercent24h(id, now)
	}
	return total / float64(len(activeIDs))
}

func (r *RollingStats) FlapCount24h(stableID string, now time.Time) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	evs, ok := r.events[stableID]
	if !ok {
		return 0
	}
	windowStart := now.Unix() - 86400
	flaps := 0
	for _, ev := range evs {
		if ev.Timestamp >= windowStart && ev.Timestamp <= now.Unix() {
			flaps++
		}
	}
	return flaps
}

// LatencySamples maintains a rolling buffer of N recent check latencies.
type LatencySamples struct {
	mu      sync.Mutex
	samples []float64
	idx     int
	count   int
	maxSize int
}

// NewLatencySamples creates a LatencySamples ring buffer.
func NewLatencySamples(maxSize int) *LatencySamples {
	if maxSize <= 0 {
		maxSize = 50
	}
	return &LatencySamples{
		samples: make([]float64, maxSize),
		maxSize: maxSize,
	}
}

// Add appends a new latency measurement.
func (l *LatencySamples) Add(latencyMs float64) {
	if latencyMs <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.samples[l.idx] = latencyMs
	l.idx = (l.idx + 1) % l.maxSize
	if l.count < l.maxSize {
		l.count++
	}
}

// Count returns the number of collected samples.
func (l *LatencySamples) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

// Percentile calculates the p-th percentile (0.0 - 1.0) of stored samples.
func (l *LatencySamples) Percentile(p float64) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.count == 0 {
		return 0
	}
	if p <= 0 {
		p = 0
	}
	if p >= 1 {
		p = 1
	}

	vals := make([]float64, l.count)
	copy(vals, l.samples[:l.count])
	sort.Float64s(vals)

	idx := int(float64(l.count-1) * p)
	return vals[idx]
}

// StdDev calculates the standard deviation (jitter) of stored samples.
func (l *LatencySamples) StdDev() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.count < 2 {
		return 0
	}

	var sum float64
	for i := 0; i < l.count; i++ {
		sum += l.samples[i]
	}
	mean := sum / float64(l.count)

	var varianceSum float64
	for i := 0; i < l.count; i++ {
		diff := l.samples[i] - mean
		varianceSum += diff * diff
	}
	return math.Sqrt(varianceSum / float64(l.count))
}

// IncidentStats contains MTBF/MTTR and incident count over a sliding window.
type IncidentStats struct {
	MTBF      time.Duration
	MTTR      time.Duration
	Incidents int
}

// IncidentStats calculates MTBF, MTTR, and the number of downtime incidents
// for a proxy over the given sliding window ending at now.
func (r *RollingStats) IncidentStats(stableID string, window time.Duration, now time.Time) IncidentStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	evs, ok := r.events[stableID]
	if !ok || len(evs) == 0 {
		return IncidentStats{}
	}

	nowSec := now.Unix()
	windowSec := int64(window.Seconds())
	if windowSec <= 0 {
		windowSec = 86400
	}
	windowStart := nowSec - windowSec

	type interval struct {
		start int64
		end   int64
	}
	var intervals []interval
	var curDownStart int64 = -1

	for _, ev := range evs {
		if !ev.Online && curDownStart == -1 {
			curDownStart = ev.Timestamp
		} else if ev.Online && curDownStart != -1 {
			intervals = append(intervals, interval{start: curDownStart, end: ev.Timestamp})
			curDownStart = -1
		}
	}
	if curDownStart != -1 {
		intervals = append(intervals, interval{start: curDownStart, end: nowSec})
	}

	var totalDowntimeSec int64
	var incidents int

	for _, iv := range intervals {
		if iv.end <= windowStart || iv.start >= nowSec {
			continue
		}
		effStart := iv.start
		if effStart < windowStart {
			effStart = windowStart
		}
		effEnd := iv.end
		if effEnd > nowSec {
			effEnd = nowSec
		}
		if effEnd > effStart {
			totalDowntimeSec += (effEnd - effStart)
			incidents++
		}
	}

	if incidents == 0 {
		return IncidentStats{
			MTBF:      window,
			MTTR:      0,
			Incidents: 0,
		}
	}

	mttr := (time.Duration(totalDowntimeSec) * time.Second) / time.Duration(incidents)
	uptimeSec := windowSec - totalDowntimeSec
	if uptimeSec < 0 {
		uptimeSec = 0
	}
	mtbf := (time.Duration(uptimeSec) * time.Second) / time.Duration(incidents)

	return IncidentStats{
		MTBF:      mtbf,
		MTTR:      mttr,
		Incidents: incidents,
	}
}

// Heatmap7d computes a 24x7 matrix of outages by hour of day (0..23) and day of week (Mon=0..Sun=6),
// along with the peak hour and its total outage count.
func (r *RollingStats) Heatmap7d(now time.Time, loc *time.Location) (matrix [24][7]int, peakHourStart int, peakHourCount int) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if loc == nil {
		loc = time.UTC
	}

	windowStart := now.Add(-7 * 24 * time.Hour).Unix()
	nowSec := now.Unix()

	for _, evs := range r.events {
		for _, ev := range evs {
			if !ev.Online && ev.Timestamp >= windowStart && ev.Timestamp <= nowSec {
				t := time.Unix(ev.Timestamp, 0).In(loc)
				hour := t.Hour()
				// Monday is column 0, Sunday is column 6
				dayIdx := (int(t.Weekday()) + 6) % 7
				matrix[hour][dayIdx]++
			}
		}
	}

	for h := 0; h < 24; h++ {
		sum := 0
		for d := 0; d < 7; d++ {
			sum += matrix[h][d]
		}
		if sum > peakHourCount {
			peakHourCount = sum
			peakHourStart = h
		}
	}

	return matrix, peakHourStart, peakHourCount
}

// GetIncidentStats returns MTBF/MTTR metrics for a given proxy.
func (ss *StatsStore) GetIncidentStats(stableID string, window time.Duration, now time.Time) IncidentStats {
	if ss == nil || ss.rollingStats == nil {
		return IncidentStats{}
	}
	return ss.rollingStats.IncidentStats(stableID, window, now)
}

// GetHeatmap7d returns the 24x7 outage heatmap for the last 7 days.
func (ss *StatsStore) GetHeatmap7d(now time.Time, loc *time.Location) ([24][7]int, int, int) {
	if ss == nil || ss.rollingStats == nil {
		return [24][7]int{}, 0, 0
	}
	return ss.rollingStats.Heatmap7d(now, loc)
}
