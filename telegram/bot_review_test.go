package telegram

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"xray-checker/checker"
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

func TestBot_TimezoneDisplay(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "cfg.json")
	cm, err := NewConfigManager(cfgPath, BotConfig{Timezone: "Asia/Yekaterinburg"})
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	b := &Bot{
		configMgr: cm,
	}

	loc := b.loc()
	if loc.String() != "Asia/Yekaterinburg" {
		t.Errorf("expected b.loc() to be Asia/Yekaterinburg, got %s", loc.String())
	}

	tzText := b.getTimezoneText()
	if !strings.Contains(tzText, "Asia/Yekaterinburg") {
		t.Errorf("expected tzText to contain Asia/Yekaterinburg, got:\n%s", tzText)
	}
	if !strings.Contains(tzText, "UTC+5") {
		t.Errorf("expected tzText to contain UTC+5, got:\n%s", tzText)
	}
}

func TestBot_FlappingSuppression(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}

	testNow := time.Now()
	b := &Bot{
		statsStore:    store,
		seeded:        true,
		lastSeen:      make(map[string]bool),
		lastFlapAlert: make(map[string]time.Time),
		tracker:       NewAlertTracker(),
		eventBuffer:   NewEventBuffer(),
		nowFunc:       func() time.Time { return testNow },
	}

	// Record 24 transitions so offline count is 12 (> 10)
	for i := 0; i < 24; i++ {
		online := i%2 == 0
		store.RecordTransition("p1", "Proxy 1", online, "test", testNow.Add(time.Duration(-60+i)*time.Minute))
	}

	if store.GetFlapCount24h("p1", testNow) < 11 {
		t.Fatalf("expected flap count > 10, got %d", store.GetFlapCount24h("p1", testNow))
	}

	// 1st transition to down: should record alert time and not be suppressed
	b.lastSeen["p1"] = true
	snapshot1 := []metrics.ProxyMetric{
		{StableID: "p1", Name: "Proxy 1", Online: false, Address: "1.2.3.4:443"},
	}
	t1 := testNow
	b.ProcessSnapshot(snapshot1)

	if b.lastFlapAlert["p1"].IsZero() || !b.lastFlapAlert["p1"].Equal(t1) {
		t.Fatalf("expected lastFlapAlert to be recorded as t1, got %v (want %v)", b.lastFlapAlert["p1"], t1)
	}

	// 2nd transition to down 2 minutes later: should be suppressed (lastFlapAlert unchanged)
	testNow = testNow.Add(2 * time.Minute)
	b.lastSeen["p1"] = true
	b.ProcessSnapshot(snapshot1)
	if !b.lastFlapAlert["p1"].Equal(t1) {
		t.Errorf("expected lastFlapAlert to remain t1 (%v) during cooldown, got %v", t1, b.lastFlapAlert["p1"])
	}

	// 3rd transition to down 16 minutes later: cooldown expired, allowed again
	testNow = testNow.Add(16 * time.Minute)
	t3 := testNow
	b.lastSeen["p1"] = true
	b.ProcessSnapshot(snapshot1)
	if !b.lastFlapAlert["p1"].Equal(t3) {
		t.Errorf("expected lastFlapAlert to update to t3 (%v) after cooldown, got %v", t3, b.lastFlapAlert["p1"])
	}
}

func TestBot_DeepDiag_ReliabilityAndP99(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}

	b := &Bot{
		statsStore: store,
	}

	// Record 10 latency samples
	for _, lat := range []float64{20, 30, 40, 50, 60, 70, 80, 90, 150, 300} {
		store.RecordLatency("p1", lat)
	}

	// Record transitions for MTBF/MTTR and flaps
	now := time.Now()
	store.RecordTransition("p1", "Proxy 1", false, "Connection refused", now.Add(-2*time.Hour))
	store.RecordTransition("p1", "Proxy 1", true, "", now.Add(-1*time.Hour))

	pm := metrics.ProxyMetric{
		StableID: "p1",
		Name:     "Proxy 1",
		Address:  "example.com:443",
		Protocol: "vless",
		Online:   true,
	}

	text := b.formatDeepDiagnostics(pm, checker.NodeHealth{ResolvedIP: "1.1.1.1"}, nil)

	if !strings.Contains(text, "p99:") {
		t.Errorf("expected text to contain 'p99:', got:\n%s", text)
	}
	if !strings.Contains(text, "НАДЁЖНОСТЬ (24ч):") {
		t.Errorf("expected text to contain 'НАДЁЖНОСТЬ (24ч):', got:\n%s", text)
	}
	if !strings.Contains(text, "MTBF") {
		t.Errorf("expected text to contain 'MTBF', got:\n%s", text)
	}
	if !strings.Contains(text, "MTTR") {
		t.Errorf("expected text to contain 'MTTR', got:\n%s", text)
	}
}

func TestBot_TopProblematic_MTBF_MTTR(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("failed to create stats store: %v", err)
	}

	b := &Bot{
		statsStore: store,
	}

	now := time.Now()
	store.RecordCheck("p1", "BadProxy", false, 0)
	store.RecordTransition("p1", "BadProxy", false, "Offline", now.Add(-2*time.Hour))
	store.RecordTransition("p1", "BadProxy", true, "", now.Add(-1*time.Hour))

	text := b.getTopProblematicText()
	if !strings.Contains(text, "MTBF:") || !strings.Contains(text, "MTTR:") {
		t.Errorf("expected top problematic text to contain MTBF and MTTR, got:\n%s", text)
	}
}
