package telegram

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		current  string
		latest   string
		expected bool
	}{
		{"v2.3.5", "v2.4.0", true},
		{"2.3.5", "v2.4.0", true},
		{"v2.3.5", "2.4.0", true},
		{"v2.4.0", "v2.4.0", false},
		{"v2.4.1", "v2.4.0", false},
		{"v3.0.0", "v2.4.0", false},
		{"v2.3.5", "v2.3.6", true},
		{"v2.3.5", "v2.3.5.1", true},
		{"v2.3.5-beta", "v2.3.5", true},
		{"v1.0.0", "v2.0.0", true},
		{"unknown", "v2.4.0", false},
		{"dev", "v2.4.0", false},
		{"", "v2.4.0", false},
		{"v2.4.0", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.current+"_vs_"+tt.latest, func(t *testing.T) {
			got := isNewerVersion(tt.current, tt.latest)
			if got != tt.expected {
				t.Errorf("isNewerVersion(%q, %q) = %v; want %v", tt.current, tt.latest, got, tt.expected)
			}
		})
	}
}

func TestShouldAlertNewRelease(t *testing.T) {
	tests := []struct {
		name         string
		current      string
		latest       string
		lastNotified string
		expected     bool
	}{
		{
			name:         "newer version not notified yet",
			current:      "v2.3.5",
			latest:       "v2.4.0",
			lastNotified: "",
			expected:     true,
		},
		{
			name:         "already notified this tag",
			current:      "v2.3.5",
			latest:       "v2.4.0",
			lastNotified: "v2.4.0",
			expected:     false,
		},
		{
			name:         "already on latest version",
			current:      "v2.4.0",
			latest:       "v2.4.0",
			lastNotified: "",
			expected:     false,
		},
		{
			name:         "current is newer than release (e.g. pre-release or ahead)",
			current:      "v2.5.0",
			latest:       "v2.4.0",
			lastNotified: "",
			expected:     false,
		},
		{
			name:         "dev or unknown current version does not trigger background alert",
			current:      "dev",
			latest:       "v2.4.0",
			lastNotified: "",
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldAlertNewRelease(tt.current, tt.latest, tt.lastNotified)
			if got != tt.expected {
				t.Errorf("shouldAlertNewRelease(%q, %q, %q) = %v; want %v",
					tt.current, tt.latest, tt.lastNotified, got, tt.expected)
			}
		})
	}
}

func TestFetchLatestRelease(t *testing.T) {
	mockJSON := `{
		"tag_name": "v2.4.0",
		"name": "v2.4.0: Node Optimization",
		"html_url": "https://github.com/vlv-code/xray-checker-tg-bot/releases/tag/v2.4.0",
		"body": "## Changes\n- Fixed 5-minute lag on node start\n- Added BOOTSTRAP_PROXY",
		"published_at": "2026-09-21T12:00:00Z"
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" && r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected Accept header: %s", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockJSON))
	}))
	defer srv.Close()

	client := srv.Client()
	rel, err := fetchLatestReleaseFromURL(client, srv.URL)
	if err != nil {
		t.Fatalf("fetchLatestReleaseFromURL failed: %v", err)
	}

	if rel.TagName != "v2.4.0" {
		t.Errorf("expected tag v2.4.0, got %s", rel.TagName)
	}
	if rel.Name != "v2.4.0: Node Optimization" {
		t.Errorf("expected name 'v2.4.0: Node Optimization', got %s", rel.Name)
	}
	if !strings.Contains(rel.Body, "BOOTSTRAP_PROXY") {
		t.Errorf("expected body to contain BOOTSTRAP_PROXY, got %s", rel.Body)
	}
}

func TestFormatReleaseNotification(t *testing.T) {
	rel := &ReleaseInfo{
		TagName: "v2.4.0",
		Name:    "v2.4.0: Enterprise Features",
		HTMLURL: "https://github.com/vlv-code/xray-checker-tg-bot/releases/tag/v2.4.0",
		Body:    "- Added smart NO_PROXY\n- Pre-bundled geo files",
	}

	text := formatReleaseNotification("v2.3.5", rel)
	if !strings.Contains(text, "Доступно обновление Xray Checker") {
		t.Errorf("expected header, got: %s", text)
	}
	if !strings.Contains(text, "v2.3.5") || !strings.Contains(text, "v2.4.0") {
		t.Errorf("expected versions in message, got: %s", text)
	}
	if !strings.Contains(text, "https://github.com/vlv-code/xray-checker-tg-bot/releases/tag/v2.4.0") {
		t.Errorf("expected URL in message, got: %s", text)
	}
}

func TestReleaseNotificationDeduplication(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := cfgDir + "/bot_config.json"

	cfgMgr, err := NewConfigManager(cfgPath, BotConfig{
		ReleaseAlertsEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	bot := &Bot{
		configMgr: cfgMgr,
		version:   "v2.3.5",
	}

	rel := &ReleaseInfo{
		TagName: "v2.4.0",
		Name:    "v2.4.0",
		HTMLURL: "https://example.com/rel",
	}

	// 1. First check: should alert
	cfg := bot.GetConfig()
	if !shouldAlertNewRelease(bot.version, rel.TagName, cfg.LastNotifiedReleaseTag) {
		t.Errorf("expected alert on first check")
	}

	// Record notification as sent
	_ = bot.updateConfig(func(c *BotConfig) {
		c.LastNotifiedReleaseTag = rel.TagName
	})

	// 2. Second check: should NOT alert again
	cfg = bot.GetConfig()
	if shouldAlertNewRelease(bot.version, rel.TagName, cfg.LastNotifiedReleaseTag) {
		t.Errorf("expected no alert on second check for same tag")
	}
}
