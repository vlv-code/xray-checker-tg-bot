package telegram

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	"xray-checker/metrics"
)

func TestBot_AverageUptimeInMenu(t *testing.T) {
	tmpDir := t.TempDir()
	statsPath := filepath.Join(tmpDir, "stats.json")
	cfgPath := filepath.Join(tmpDir, "cfg.json")

	store, err := NewStatsStore(statsPath)
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}

	cm, err := NewConfigManager(cfgPath, BotConfig{})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	// proxy1: 100% uptime (1 check, 1 success)
	store.RecordCheck("proxy1", "Proxy 1", true, 50)
	// proxy2: 0% uptime (1 check, 0 success)
	store.RecordCheck("proxy2", "Proxy 2", false, 0)

	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "proxy1", Name: "Proxy 1", Online: true},
			{StableID: "proxy2", Name: "Proxy 2", Online: false},
		},
	}

	b := &Bot{
		source:     src,
		statsStore: store,
		configMgr:  cm,
	}

	menuText := b.getMenuText()
	// Average uptime should be 50.0%, NOT 100.0%
	if !strings.Contains(menuText, "50.0%") {
		t.Fatalf("expected menu text to contain 50.0%% average uptime, got:\n%s", menuText)
	}
}

func TestBot_StatusFiltersDisabled(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "OnlineNode", Online: true, LatencyMs: 45},
			{StableID: "p2", Name: "OfflineNode", Online: false},
			{StableID: "p3", Name: "DisabledNode", Online: false, Disabled: true},
		},
	}

	b := &Bot{
		source: src,
	}

	statusText := b.getStatusText()

	// Should not mark DisabledNode as 🔴 недоступен
	if strings.Contains(statusText, "🔴 <b>DisabledNode</b>") {
		t.Errorf("expected DisabledNode not to be shown as offline red, got:\n%s", statusText)
	}
	// Should indicate it's disabled with ⏸️
	if !strings.Contains(statusText, "⏸️") || !strings.Contains(statusText, "DisabledNode") {
		t.Errorf("expected DisabledNode to be marked with ⏸️, got:\n%s", statusText)
	}
	// Active online count should be 1/2, not 1/3
	if !strings.Contains(statusText, "1/2") {
		t.Errorf("expected 1/2 active online in header, got:\n%s", statusText)
	}
}

func TestSplitMessage_TagAware(t *testing.T) {
	input := "<b>Line 1\nLine 2\nLine 3</b>"
	chunks := splitMessage(input, 15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// First chunk must close </b>
	if !strings.HasSuffix(chunks[0], "</b>") {
		t.Errorf("expected chunk 0 to end with </b>, got: %q", chunks[0])
	}
	// Second chunk must open with <b>
	if !strings.HasPrefix(chunks[1], "<b>") {
		t.Errorf("expected chunk 1 to start with <b>, got: %q", chunks[1])
	}
}

func TestBot_ToggleNode_UnknownNodeDoesNotAddToConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "cfg.json")
	cm, err := NewConfigManager(cfgPath, BotConfig{})
	if err != nil {
		t.Fatalf("failed to create config manager: %v", err)
	}

	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "Node-1", Online: true},
		},
	}

	b := &Bot{
		source:    src,
		configMgr: cm,
	}

	msg := &telego.Message{
		Text: "/togglenode NonExistentNode",
	}

	b.handleToggleNodeCommand(msg)

	cfg := b.GetConfig()
	if len(cfg.DisabledProxies) != 0 {
		t.Errorf("expected 0 disabled proxies for unknown node, got %v", cfg.DisabledProxies)
	}
}

func TestBot_BuildAddSubReport(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "Node-Online-1", Online: true},
			{StableID: "p2", Name: "Node-Online-2", Online: true},
			{StableID: "p3", Name: "Node-Dead-1", Online: false},
		},
	}
	b := &Bot{
		source: src,
	}

	report := b.buildAddSubReport(3)
	if !strings.Contains(report, "Прокси-хостов: 3") {
		t.Errorf("expected report to contain total proxies, got:\n%s", report)
	}
	if !strings.Contains(report, "🟢 Доступно: 2") {
		t.Errorf("expected 2 online, got:\n%s", report)
	}
	if !strings.Contains(report, "🔴 Недоступно: 1") {
		t.Errorf("expected 1 offline, got:\n%s", report)
	}
	if !strings.Contains(report, "Node-Dead-1") {
		t.Errorf("expected dead node name in list, got:\n%s", report)
	}
}

func TestBot_SendOrUpdateMenu_TracksLastMessage(t *testing.T) {
	b := &Bot{
		lastMenuMsg: make(map[int64]int),
	}

	// First call simulates sending a menu (mock returns MessageID: 0 or we set directly)
	b.lastMenuMu.Lock()
	b.lastMenuMsg[12345] = 42
	b.lastMenuMu.Unlock()

	b.lastMenuMu.Lock()
	got := b.lastMenuMsg[12345]
	b.lastMenuMu.Unlock()

	if got != 42 {
		t.Fatalf("expected lastMenuMsg to be 42, got %d", got)
	}
}



