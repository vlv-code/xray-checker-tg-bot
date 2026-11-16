package xray

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// NOTE: local verification runs without -race (the host has no gcc); CI runs
// `go test -race ./...` and is the authoritative gate for this hammer test.
// Start/Stop used to touch Runner.instance unsynchronized while main defers a
// Stop() that races scheduler-driven updateConfiguration restarts.
func TestRunnerStartStopConcurrentHammer(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrent hammer skipped in -short mode")
	}
	cfgPath := filepath.Join(t.TempDir(), "xray_config.json")
	// Outbound-only config: no listeners, so instances never bind ports and
	// the hammer stays free of address-in-use noise.
	cfg := `{"log":{"loglevel":"none"},"inbounds":[],"outbounds":[{"protocol":"freedom"}]}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(cfgPath)
	var startOK atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 6; j++ {
				if err := r.Start(); err != nil {
					t.Logf("start attempt failed (legitimate when racing a concurrent Stop): %v", err)
				} else {
					startOK.Add(1)
				}
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 6; j++ {
				if err := r.Stop(); err != nil {
					t.Errorf("Stop returned error: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	if startOK.Load() == 0 {
		t.Fatalf("expected at least one successful Start during the hammer, got 0")
	}
	// The runner must remain usable after the hammer: a serial Start/Stop
	// cycle leaves consistent state.
	if err := r.Start(); err != nil {
		t.Fatalf("serial Start after hammer failed: %v", err)
	}
	if r.instance == nil {
		t.Fatal("expected instance to be set after successful Start")
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("serial Stop after hammer failed: %v", err)
	}
	if r.instance != nil {
		t.Fatal("expected instance to be cleared after Stop")
	}
}

// TestRunnerStopWithoutStartIsNoop: Stop on a runner that never started must
// be a no-op returning nil.
func TestRunnerStopWithoutStartIsNoop(t *testing.T) {
	r := NewRunner(filepath.Join(t.TempDir(), "missing.json"))
	if err := r.Stop(); err != nil {
		t.Fatalf("Stop without Start returned error: %v", err)
	}
	if r.instance != nil {
		t.Fatal("expected instance to stay nil after Stop without Start")
	}
}

// TestRunnerStartMissingConfigFailsCleanly: Start against a missing config
// file must return an error and leave no half-started instance behind.
func TestRunnerStartMissingConfigFailsCleanly(t *testing.T) {
	r := NewRunner(filepath.Join(t.TempDir(), "missing.json"))
	if err := r.Start(); err == nil {
		t.Fatal("expected Start to fail for a missing config file")
	}
	if r.instance != nil {
		t.Fatal("expected instance to stay nil after failed Start")
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("Stop after failed Start returned error: %v", err)
	}
}
