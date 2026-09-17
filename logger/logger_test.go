package logger

import (
	"sync"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"none", LevelNone},
		{"off", LevelNone},
		{"silent", LevelNone},
		{"error", LevelError},
		{"err", LevelError},
		{"warn", LevelWarn},
		{"warning", LevelWarn},
		{"info", LevelInfo},
		{"debug", LevelDebug},
		{"unknown", LevelInfo},
		{"", LevelInfo},
	}

	for _, tt := range tests {
		got := ParseLevel(tt.input)
		if got != tt.want {
			t.Errorf("ParseLevel(%q) = %v; want %v", tt.input, got, tt.want)
		}
	}
}

func TestLevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{LevelNone, "none"},
		{LevelError, "error"},
		{LevelWarn, "warn"},
		{LevelInfo, "info"},
		{LevelDebug, "debug"},
		{Level(999), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("Level(%d).String() = %q; want %q", tt.level, got, tt.want)
		}
	}
}

func TestSetLevelAndConcurrency(t *testing.T) {
	orig := CurrentLevel()
	defer SetLevel(orig)

	SetLevel(LevelDebug)
	if got := CurrentLevel(); got != LevelDebug {
		t.Fatalf("expected LevelDebug, got %v", got)
	}

	// Concurrent read and write to verify race-safety
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				SetLevel(LevelDebug)
			} else {
				SetLevel(LevelNone)
			}
		}(i)
		go func() {
			defer wg.Done()
			Debug("test debug msg %d", 123)
			Info("test info msg")
			Warn("test warn msg")
			Error("test error msg")
			Result("test result msg")
		}()
	}
	wg.Wait()
}
