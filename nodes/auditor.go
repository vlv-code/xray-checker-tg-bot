package nodes

import (
	"context"
	"sync"
	"time"

	"xray-checker/checker"
)

// AuditTarget is one deduplicated host to audit via Check-Host.
type AuditTarget struct {
	Address string // host:port — TCP check target and result key
	Host    string // bare host — UDP ping target
	IsUDP   bool
	Name    string // display proxy name
}

// auditorRateLimit mirrors the master's pacing between Check-Host API calls.
const auditorRateLimit = 3 * time.Second

// Auditor periodically audits a node's unique hosts through Check-Host and
// keeps the latest summary per target for the next report. It replaces the
// master's audit for this node's hosts (audit ownership split), so the same
// host is never audited twice.
type Auditor struct {
	mu       sync.Mutex
	client   *checker.CheckHostClient
	targets  func() []AuditTarget
	enabled  bool
	interval time.Duration
	lastRun  time.Time
	results  map[string]checker.CheckHostSummary
	stop     chan struct{}
	stopped  bool
}

// NewAuditor builds an auditor using the given Check-Host client (nil = a
// default client against check-host.net) and a targets provider evaluated
// before every audit pass. The loop goroutine starts immediately but audits
// nothing until SetSchedule enables it.
func NewAuditor(client *checker.CheckHostClient, targets func() []AuditTarget) *Auditor {
	if client == nil {
		client = checker.NewCheckHostClient("", 1500*time.Millisecond)
	}
	a := &Auditor{
		client:   client,
		targets:  targets,
		interval: time.Hour,
		results:  make(map[string]checker.CheckHostSummary),
		stop:     make(chan struct{}),
	}
	go a.loop()
	return a
}

// SetSchedule enables/disables auditing and updates the interval (hours <= 0
// falls back to 1h). Enabling schedules the first audit ~2 minutes ahead.
func (a *Auditor) SetSchedule(enabled bool, intervalHours int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	wasEnabled := a.enabled
	a.enabled = enabled
	if intervalHours > 0 {
		a.interval = time.Duration(intervalHours) * time.Hour
	}
	if enabled && !wasEnabled {
		a.lastRun = time.Now().Add(-a.interval + 2*time.Minute)
	}
}

// Results returns a copy of the latest audit summaries keyed by target
// address (bare host for UDP targets).
func (a *Auditor) Results() map[string]checker.CheckHostSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]checker.CheckHostSummary, len(a.results))
	for k, v := range a.results {
		out[k] = v
	}
	return out
}

// Stop terminates the loop goroutine.
func (a *Auditor) Stop() {
	a.mu.Lock()
	if !a.stopped {
		a.stopped = true
		close(a.stop)
	}
	a.mu.Unlock()
}

func (a *Auditor) loop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
			if a.dueLocked() {
				a.mu.Lock()
				a.lastRun = time.Now()
				a.mu.Unlock()
				a.runOnce()
			}
		}
	}
}

// dueLocked reports whether an audit pass is due. Caller must not hold mu.
func (a *Auditor) dueLocked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enabled && time.Since(a.lastRun) >= a.interval
}

// runOnceForTest runs one audit pass synchronously; tests only.
func (a *Auditor) runOnceForTest() { a.runOnce() }

// dueForTest exposes the due check for tests.
func (a *Auditor) dueForTest() bool { return a.dueLocked() }

// runOnce audits all current targets sequentially with rate limiting;
// failed checks keep the previous summary for that target.
func (a *Auditor) runOnce() {
	targets := a.targets()
	for i, t := range targets {
		if i > 0 {
			time.Sleep(auditorRateLimit)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		var summary *checker.CheckHostSummary
		var err error
		if t.IsUDP {
			summary, err = a.client.CheckPing(ctx, t.Host, nil)
		} else {
			summary, err = a.client.CheckTCP(ctx, t.Address, nil)
		}
		cancel()
		if err != nil || summary == nil {
			continue
		}
		key := t.Address
		if t.IsUDP {
			key = t.Host
		}
		a.mu.Lock()
		a.results[key] = *summary
		a.mu.Unlock()
	}
}
