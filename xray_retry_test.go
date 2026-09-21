package main

import (
	"errors"
	"testing"
	"time"
)

func TestStartWithRetry_SuccessFirstAttempt(t *testing.T) {
	attempts := 0
	start := func() error {
		attempts++
		return nil
	}

	backoffs := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	err := startWithRetry(start, backoffs)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", attempts)
	}
}

func TestStartWithRetry_SuccessAfterRetries(t *testing.T) {
	attempts := 0
	start := func() error {
		attempts++
		if attempts < 3 {
			return errors.New("port in use")
		}
		return nil
	}

	backoffs := []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	err := startWithRetry(start, backoffs)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestStartWithRetry_ExhaustRetries(t *testing.T) {
	attempts := 0
	start := func() error {
		attempts++
		return errors.New("fatal bind failure")
	}

	backoffs := []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
	err := startWithRetry(start, backoffs)
	if err == nil {
		t.Fatal("expected error after retries exhausted, got nil")
	}
	if attempts != 3 { // 1 initial + 2 retries
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}
