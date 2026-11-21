package config

// Tests for the immutable runtime snapshot (wave N-3a) that replaced the
// RuntimeMu lock band-aid (config/runtime_lock.go). The contract:
//
//   - readers never take a lock and always observe a consistent snapshot;
//   - concurrent writers of the SAME field serialize (last value wins);
//   - concurrent writers of DIFFERENT fields never clobber each other —
//     this is exactly what a naive whole-struct overwrite would break;
//   - CI `go test -race` additionally enforces the atomic-pointer contract
//     on every commit.

import (
	"sync"
	"testing"
	"time"
)

func TestInitEffectiveRuntime_SnapshotVisibleWithoutLock(t *testing.T) {
	InitEffectiveRuntime(42, 300)
	snap := LoadEffectiveRuntime()
	if snap == nil {
		t.Fatal("LoadEffectiveRuntime returned nil after InitEffectiveRuntime")
	}
	if snap.CheckInterval != 42 {
		t.Fatalf("CheckInterval = %d, want 42", snap.CheckInterval)
	}
	if snap.UpdateInterval != 300 {
		t.Fatalf("UpdateInterval = %d, want 300", snap.UpdateInterval)
	}

	// Update paths must be visible to lock-free readers too.
	UpdateCheckInterval(60)
	UpdateSubscriptionUpdateInterval(900)
	snap = LoadEffectiveRuntime()
	if snap.CheckInterval != 60 || snap.UpdateInterval != 900 {
		t.Fatalf("snapshot after updates = %+v, want CheckInterval=60 UpdateInterval=900", snap)
	}
}

// Same-field hammer: one goroutine updates CheckInterval 200 times while
// another loads the snapshot 200 times. Neither side takes a caller-held
// lock, so both must complete (done-channel + timeout, same shape as the
// old RuntimeMu deadlock pin) and the reader must always observe a valid
// snapshot (positive interval after Init).
func TestUpdateCheckInterval_ConcurrentWithLoad(t *testing.T) {
	InitEffectiveRuntime(300, 300)

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < 200; i++ {
			UpdateCheckInterval(300 + i%2)
		}
	}()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for i := 0; i < 200; i++ {
			snap := LoadEffectiveRuntime()
			if snap == nil {
				t.Error("reader observed nil snapshot after Init")
				return
			}
			if snap.CheckInterval <= 0 {
				t.Errorf("reader observed invalid snapshot: CheckInterval=%d", snap.CheckInterval)
				return
			}
		}
	}()

	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("lock-free reader did not finish — snapshot load blocked")
	}
	select {
	case <-writerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot writer did not finish — update blocked")
	}
}

// Adversarial (wave N-3a item 6): concurrent writers of DIFFERENT fields.
// A hammers UpdateCheckInterval while B hammers
// UpdateSubscriptionUpdateInterval; the final snapshot must contain BOTH
// last values. A naive implementation that stores a whole struct per update
// (without read-modify-write under the write mutex) loses one of the two
// values here — that regression class is exactly what this test pins shut.
func TestConcurrentWriters_DifferentFieldsPreserveBoth(t *testing.T) {
	const (
		iters      = 200
		wantCheck  = 777
		wantUpdate = 888
	)
	InitEffectiveRuntime(1, 2)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			UpdateCheckInterval(wantCheck)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			UpdateSubscriptionUpdateInterval(wantUpdate)
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent writers did not finish")
	}

	snap := LoadEffectiveRuntime()
	if snap == nil {
		t.Fatal("snapshot nil after Init and concurrent updates")
	}
	if snap.CheckInterval != wantCheck || snap.UpdateInterval != wantUpdate {
		t.Fatalf("concurrent different-field writers lost values: snapshot=%+v, want CheckInterval=%d UpdateInterval=%d (whole-struct overwrite regression)",
			snap, wantCheck, wantUpdate)
	}
}
