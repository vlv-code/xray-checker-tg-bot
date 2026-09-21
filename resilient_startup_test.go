package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"xray-checker/models"
	"xray-checker/subscription"
	"xray-checker/telegram"
)

func TestResolveInitialConfigs_Success(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "xray.json")
	cachePath := filepath.Join(tempDir, "cache.json")

	called := 0
	sampleProxies := []*models.ProxyConfig{
		{Protocol: "vless", Server: "1.2.3.4", Port: 443, Name: "node1", UUID: "11111111-1111-1111-1111-111111111111", Index: 0},
	}

	fetcher := func() (*[]*models.ProxyConfig, error) {
		called++
		return &sampleProxies, nil
	}

	configs, isDegraded, err := resolveInitialConfigs(configFile, cachePath, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDegraded {
		t.Errorf("expected isDegraded = false, got true")
	}
	if len(*configs) != 1 {
		t.Errorf("expected 1 proxy, got %d", len(*configs))
	}
	if called != 1 {
		t.Errorf("expected 1 fetch call, got %d", called)
	}

	// Verify cache was saved
	cached, _, err := subscription.LoadProxyCache(cachePath)
	if err != nil {
		t.Fatalf("expected cache to be written, got: %v", err)
	}
	if len(cached) != 1 {
		t.Errorf("expected 1 cached proxy, got %d", len(cached))
	}
}

func TestResolveInitialConfigs_FallbackToCacheOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "xray.json")
	cachePath := filepath.Join(tempDir, "cache.json")

	// Pre-populate cache
	cachedProxies := []*models.ProxyConfig{
		{Protocol: "vless", Server: "cached.example.com", Port: 443, Name: "cached-node", UUID: "22222222-2222-2222-2222-222222222222", Index: 0},
	}
	if err := subscription.SaveProxyCache(cachePath, cachedProxies, "CachedSub"); err != nil {
		t.Fatal(err)
	}

	fetcher := func() (*[]*models.ProxyConfig, error) {
		return nil, errors.New("net/http: TLS handshake timeout")
	}

	configs, isDegraded, err := resolveInitialConfigs(configFile, cachePath, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDegraded {
		t.Errorf("expected isDegraded = true when using cache fallback")
	}
	if len(*configs) != 1 || (*configs)[0].Server != "cached.example.com" {
		t.Errorf("expected 1 cached proxy with server 'cached.example.com', got %+v", configs)
	}
}

func TestResolveInitialConfigs_DegradedZeroProxiesWhenNoCache(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "xray.json")
	cachePath := filepath.Join(tempDir, "non_existent_cache.json")

	fetcher := func() (*[]*models.ProxyConfig, error) {
		return nil, errors.New("connection refused")
	}

	configs, isDegraded, err := resolveInitialConfigs(configFile, cachePath, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDegraded {
		t.Errorf("expected isDegraded = true for 0 proxies degraded mode")
	}
	if configs == nil || len(*configs) != 0 {
		t.Errorf("expected 0 proxies in degraded mode, got %+v", configs)
	}

	// Verify valid Xray config was still generated on disk
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		t.Fatalf("expected fallback xray config file to be generated at %s", configFile)
	}
}

func TestStartTelegramBotWithRetry_ImmediateSuccess(t *testing.T) {
	var tgBot atomic.Pointer[telegram.Bot]
	dummyBot := &telegram.Bot{}
	onSuccessCalled := false

	initFunc := func() (*telegram.Bot, error) {
		return dummyBot, nil
	}

	startTelegramBotWithRetry(
		context.Background(),
		initFunc,
		&tgBot,
		func(b *telegram.Bot) {
			onSuccessCalled = true
		},
		10*time.Millisecond,
		false,
	)

	if tgBot.Load() != dummyBot {
		t.Errorf("expected tgBot to be set to dummyBot, got %v", tgBot.Load())
	}
	if !onSuccessCalled {
		t.Errorf("expected onSuccess to be called")
	}
}

func TestStartTelegramBotWithRetry_BackgroundRetrySuccess(t *testing.T) {
	var tgBot atomic.Pointer[telegram.Bot]
	dummyBot := &telegram.Bot{}
	var onSuccessCalled atomic.Bool

	var attempts atomic.Int32
	initFunc := func() (*telegram.Bot, error) {
		att := attempts.Add(1)
		if att < 3 {
			return nil, errors.New("connection reset by peer")
		}
		return dummyBot, nil
	}

	startTelegramBotWithRetry(
		context.Background(),
		initFunc,
		&tgBot,
		func(b *telegram.Bot) {
			onSuccessCalled.Store(true)
		},
		10*time.Millisecond,
		false,
	)

	// Wait up to 2 seconds for background retry to succeed and onSuccess callback to be executed
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tgBot.Load() != nil && onSuccessCalled.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if tgBot.Load() != dummyBot {
		t.Fatalf("expected tgBot to be set after background retry, got %v (attempts: %d)", tgBot.Load(), attempts.Load())
	}
	if !onSuccessCalled.Load() {
		t.Errorf("expected onSuccess to be called upon recovery")
	}
	if attempts.Load() < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts.Load())
	}
}

func TestStartTelegramBotWithRetry_ContextCancelled(t *testing.T) {
	var tgBot atomic.Pointer[telegram.Bot]
	ctx, cancel := context.WithCancel(context.Background())

	var attempts atomic.Int32
	initFunc := func() (*telegram.Bot, error) {
		attempts.Add(1)
		return nil, errors.New("temporary error")
	}

	startTelegramBotWithRetry(
		ctx,
		initFunc,
		&tgBot,
		nil,
		10*time.Millisecond,
		false,
	)

	// Cancel context quickly
	time.Sleep(25 * time.Millisecond)
	cancel()

	time.Sleep(40 * time.Millisecond)
	capturedAttempts := attempts.Load()

	time.Sleep(40 * time.Millisecond)
	if attempts.Load() > capturedAttempts+1 {
		t.Errorf("expected goroutine to stop after context cancellation, but attempts increased from %d to %d", capturedAttempts, attempts.Load())
	}
	if tgBot.Load() != nil {
		t.Errorf("expected tgBot to remain nil, got %v", tgBot.Load())
	}
}

func TestStartTelegramBotWithRetry_RunOnceDoesNotRetry(t *testing.T) {
	var tgBot atomic.Pointer[telegram.Bot]
	var attempts atomic.Int32
	initFunc := func() (*telegram.Bot, error) {
		attempts.Add(1)
		return nil, errors.New("telegram error")
	}

	startTelegramBotWithRetry(
		context.Background(),
		initFunc,
		&tgBot,
		nil,
		10*time.Millisecond,
		true, // RunOnce = true
	)

	time.Sleep(40 * time.Millisecond)
	if attempts.Load() != 1 {
		t.Errorf("expected exactly 1 attempt with RunOnce, got %d", attempts.Load())
	}
	if tgBot.Load() != nil {
		t.Errorf("expected tgBot to remain nil, got %v", tgBot.Load())
	}
}

