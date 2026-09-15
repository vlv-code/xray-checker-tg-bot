package telegram

import (
	"strings"
	"testing"
	"time"

	"xray-checker/metrics"
)

func TestRollingStats_IncidentStats(t *testing.T) {
	rs := NewRollingStats(100)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	// No incidents -> 0 incidents
	stats0 := rs.IncidentStats("p1", 24*time.Hour, now)
	if stats0.Incidents != 0 {
		t.Errorf("expected 0 incidents, got %d", stats0.Incidents)
	}

	// 1 incident: down at 10:00, up at 10:30 (30m downtime)
	tDown := now.Add(-2 * time.Hour)
	tUp := now.Add(-90 * time.Minute)
	rs.Record("p1", false, tDown.Unix())
	rs.Record("p1", true, tUp.Unix())

	stats1 := rs.IncidentStats("p1", 24*time.Hour, now)
	if stats1.Incidents != 1 {
		t.Errorf("expected 1 incident, got %d", stats1.Incidents)
	}
	if stats1.MTTR != 30*time.Minute {
		t.Errorf("expected MTTR 30m, got %v", stats1.MTTR)
	}
	expectedMTBF := (24*time.Hour - 30*time.Minute)
	if stats1.MTBF != expectedMTBF {
		t.Errorf("expected MTBF %v, got %v", expectedMTBF, stats1.MTBF)
	}
}

func TestRollingStats_Heatmap7d(t *testing.T) {
	rs := NewRollingStats(100)
	loc := time.UTC
	now := time.Date(2026, 9, 14, 20, 0, 0, 0, loc) // Monday 20:00

	// Record 3 drops at Monday 19:00
	tDrop := now.Add(-1 * time.Hour)
	rs.Record("p1", false, tDrop.Unix())
	rs.Record("p2", false, tDrop.Unix())
	rs.Record("p3", false, tDrop.Unix())

	matrix, peakHour, maxDrops := rs.Heatmap7d(now, loc)
	if maxDrops != 3 {
		t.Errorf("expected 3 max drops, got %d", maxDrops)
	}
	if peakHour != 19 {
		t.Errorf("expected peak hour 19, got %d", peakHour)
	}
	// Monday is column 0
	if matrix[19][0] != 3 {
		t.Errorf("expected matrix[19][0] == 3, got %d", matrix[19][0])
	}
}

func TestBot_ProtocolsStats(t *testing.T) {
	mockSrc := &mockSource{
		metrics: []metrics.ProxyMetric{
			{Name: "vless-1", Protocol: "vless", Address: "1.1.1.1:443", StableID: "v1", Online: true, LatencyMs: 40},
			{Name: "vless-2", Protocol: "vless", Address: "1.1.1.2:443", StableID: "v2", Online: true, LatencyMs: 45},
			{Name: "vless-3", Protocol: "vless", Address: "1.1.1.3:443", StableID: "v3", Online: true, LatencyMs: 50},
			{Name: "trojan-1", Protocol: "trojan", Address: "2.2.2.1:443", StableID: "t1", Online: true, LatencyMs: 60},
		},
	}

	b := &Bot{
		source: mockSrc,
	}

	text := b.getProtocolsStatsText()
	if !strings.Contains(text, "VLESS") {
		t.Errorf("expected VLESS group in output:\n%s", text)
	}
	if !strings.Contains(text, "n=3") {
		t.Errorf("expected n=3 for VLESS:\n%s", text)
	}
	if !strings.Contains(text, "Слишком мало данных (n<3)") {
		t.Errorf("expected small sample warning for Trojan (n=1):\n%s", text)
	}
}

func TestBot_HeatmapText(t *testing.T) {
	ss, err := NewStatsStore("")
	if err != nil {
		t.Fatalf("NewStatsStore failed: %v", err)
	}
	now := time.Date(2026, 9, 14, 19, 30, 0, 0, time.UTC)
	// Add drop at 19:00
	ss.RecordTransition("p1", "Proxy-1", false, "timeout", now.Add(-30*time.Minute))

	cm := &ConfigManager{}
	_ = cm.Update(func(c *BotConfig) {
		c.Timezone = "UTC"
	})

	b := &Bot{
		statsStore: ss,
		configMgr:  cm,
		nowFunc:    func() time.Time { return now },
	}

	text := b.getHeatmapText()
	if !strings.Contains(text, "Карта падений за 7 дней") {
		t.Errorf("expected title in heatmap text:\n%s", text)
	}
	if !strings.Contains(text, "← пик") {
		t.Errorf("expected peak indicator in heatmap text:\n%s", text)
	}
	if !strings.Contains(text, "Пик падений: 19ч") {
		t.Errorf("expected peak hour 19ч in text:\n%s", text)
	}
}

func TestBot_StatsOverviewText(t *testing.T) {
	ss, err := NewStatsStore("")
	if err != nil {
		t.Fatalf("NewStatsStore failed: %v", err)
	}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	// Record checks and flapping for p1 (24 transitions = 12 drops > 10)
	for i := 0; i < 24; i++ {
		ss.RecordTransition("p1", "PL-flapping", i%2 == 0, "test", now.Add(-time.Duration(24-i)*time.Minute))
	}
	for i := 0; i < 10; i++ {
		ss.RecordCheck("p1", "PL-flapping", true, float64(40+i))
	}

	mockSrc := &mockSource{
		metrics: []metrics.ProxyMetric{
			{Name: "PL-flapping", StableID: "p1", Online: true, LatencyMs: 45},
		},
	}

	b := &Bot{
		source:     mockSrc,
		statsStore: ss,
		nowFunc:    func() time.Time { return now },
	}

	text := b.getStatsOverviewText()
	if !strings.Contains(text, "Статистика аптайма") {
		t.Errorf("expected title in stats overview:\n%s", text)
	}
	if !strings.Contains(text, "Топ нестабильных") {
		t.Errorf("expected top unstable section in stats overview:\n%s", text)
	}
	if !strings.Contains(text, "PL-flapping") {
		t.Errorf("expected PL-flapping in text:\n%s", text)
	}
	if !strings.Contains(text, "P50") || !strings.Contains(text, "P95") || !strings.Contains(text, "ПАД.") {
		t.Errorf("expected table header in text:\n%s", text)
	}
	if !strings.Contains(text, "⚠️") {
		t.Errorf("expected flap warning icon ⚠️ in table row:\n%s", text)
	}
}

type mockSubFreshness struct {
	static  []string
	dynamic []string
}

func (m *mockSubFreshness) Static() []string                                 { return m.static }
func (m *mockSubFreshness) Dynamic() []string                                { return m.dynamic }
func (m *mockSubFreshness) AddSubscription(url string) (int, error)          { return 0, nil }
func (m *mockSubFreshness) RemoveSubscription(url string) (bool, int, error) { return false, 0, nil }

func TestBot_SubsFreshness(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	b := &Bot{
		subs: &mockSubFreshness{
			static:  []string{"https://example.com/sub1"},
			dynamic: []string{"https://example.com/sub2"},
		},
		nowFunc: func() time.Time { return now },
	}

	b.SetSubFreshness("https://example.com/sub1", 47, 45, 2, 0, now.Add(-2*time.Hour))
	b.SetSubFreshness("https://example.com/sub2", 12, 12, 0, 0, now.Add(-5*time.Minute))

	text := b.getSubsText()
	if !strings.Contains(text, "2ч назад · 47 прокси (было 45, +2, -0)") {
		t.Errorf("expected diff format for sub1:\n%s", text)
	}
	if !strings.Contains(text, "5м назад · 12 прокси (без изменений)") {
		t.Errorf("expected unchanged format for sub2:\n%s", text)
	}
}
