package telegram

import (
	"encoding/json"
	"fmt"
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
	mu        sync.RWMutex
	path      string
	Incidents []Incident             `json:"incidents"` // Most recent first
	Stats     map[string]*ProxyStats `json:"stats"`     // stableID -> stats
}

// NewStatsStore loads or creates a new StatsStore.
func NewStatsStore(path string) (*StatsStore, error) {
	ss := &StatsStore{
		path:      path,
		Incidents: make([]Incident, 0),
		Stats:     make(map[string]*ProxyStats),
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
		Incidents []Incident             `json:"incidents"`
		Stats     map[string]*ProxyStats `json:"stats"`
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

	dataStore := struct {
		Incidents []Incident             `json:"incidents"`
		Stats     map[string]*ProxyStats `json:"stats"`
	}{
		Incidents: ss.Incidents,
		Stats:     ss.Stats,
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
