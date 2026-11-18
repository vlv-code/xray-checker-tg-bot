package telegram

// Wave M-C (UX polish) tests: M2 honest uptime in /digest and the M9 polish
// cluster (Σ glyph unification, RU «через» node line, diagnostics cache
// timestamp accessor, /status offline-row soft reason).

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// TestNodeLineFor_RussianVia pins the RU connector on remote-node alert
// lines: «📍 через <node>», never the English "via".
func TestNodeLineFor_RussianVia(t *testing.T) {
	pm := metrics.ProxyMetric{NodeName: "m31a", NodeASN: "AS9001"}
	s := nodeLineFor(pm)
	if !strings.Contains(s, "📍 через m31a · AS9001") {
		t.Errorf("expected «📍 через m31a · AS9001», got: %q", s)
	}
	if strings.Contains(s, "via") {
		t.Errorf("node line must not contain English \"via\": %q", s)
	}
	if nodeLineFor(metrics.ProxyMetric{}) != "" {
		t.Errorf("local proxy must render no node line")
	}
}

// TestDigest_Uses24hUptimeNotAllTime is the M2 (U-7) pin: the /digest line
// «Средний аптайм (24ч)» must carry the rolling 24h figure (UptimePercent24hAll
// over ACTIVE proxies), not the all-time average. Fixtures:
//   - p1: all-time 50% (3/6 RecordCheck), no rolling events → 24h 100%;
//   - p2: rolling window shows a drop 2h ago → 24h ≈ 91.7%;
//   - p3: DISABLED, all-time 0% and fully down in 24h — must be excluded.
//
// Expected digest value: (100 + 91.7) / 2 = 95.8%. The old bug printed the
// all-time 50.0% under the 24ч label; including p3 would give 63.9%.
func TestDigest_Uses24hUptimeNotAllTime(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	if err != nil {
		t.Fatalf("NewStatsStore failed: %v", err)
	}

	now := time.Unix(1700000000, 0)
	h := time.Hour
	// All-time counters (diverge from the rolling window).
	for i := 0; i < 3; i++ {
		store.RecordCheck("p1", "P1", true, 50)
		store.RecordCheck("p1", "P1", false, 0)
	}
	store.RecordCheck("p3", "P3", false, 0)
	// Rolling 24h events.
	rs := store.GetRollingStats()
	rs.Record("p2", true, now.Add(-25*h).Unix())
	rs.Record("p2", false, now.Add(-2*h).Unix())
	rs.Record("p3", false, now.Add(-25*h).Unix())

	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "P1", Online: true},
			{StableID: "p2", Name: "P2", Online: false},
			{StableID: "p3", Name: "P3", Online: false, Disabled: true},
		},
	}
	b := &Bot{source: src, statsStore: store, nowFunc: func() time.Time { return now }}

	text := b.digestText()
	if !strings.Contains(text, "• Средний аптайм (24ч): <b>95.8%</b>") {
		t.Errorf("digest must show the 24h rolling figure 95.8%% for active proxies, got:\n%s", text)
	}
	if strings.Contains(text, "50.0%") {
		t.Errorf("digest must not show the all-time figure 50.0%% under the 24ч label, got:\n%s", text)
	}
	if strings.Contains(text, "63.9%") {
		t.Errorf("disabled p3 must be excluded from the 24h average, got:\n%s", text)
	}
}

// TestDigest_SkipsUptimeLineWithoutStatsStore guards the no-store path: no
// uptime line, no panic.
func TestDigest_SkipsUptimeLineWithoutStatsStore(t *testing.T) {
	src := &mockSource{metrics: []metrics.ProxyMetric{{StableID: "p1", Name: "P1", Online: true}}}
	b := &Bot{source: src}
	text := b.digestText()
	if strings.Contains(text, "аптайм") {
		t.Errorf("no stats store — digest must not print an uptime line, got:\n%s", text)
	}
	if !strings.Contains(text, "📊 Сводка Xray Checker") {
		t.Errorf("digest header missing, got:\n%s", text)
	}
}

// TestMenuUptimeLabel_AllTime pins the /menu honesty requirement (M2): the
// all-time figure must be explicitly labelled «за всё время».
func TestMenuUptimeLabel_AllTime(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	store.RecordCheck("p1", "P1", true, 50)
	src := &mockSource{metrics: []metrics.ProxyMetric{{StableID: "p1", Name: "P1", Online: true}}}
	b := &Bot{source: src, statsStore: store}

	text := b.getMenuText()
	if !strings.Contains(text, "Средний аптайм (за всё время): <b>100.0%</b>") {
		t.Errorf("menu must label the all-time uptime explicitly, got:\n%s", text)
	}
	if strings.Contains(text, "Средний аптайм (24ч)") {
		t.Errorf("menu must not imply a 24h window for the all-time figure, got:\n%s", text)
	}
}

// TestFormatDeepDiagnostics_SigmaGlyph pins Σ= on the deep-diagnostics
// latency-statistics block (n>=5 branch, n<50).
func TestFormatDeepDiagnostics_SigmaGlyph(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStatsStore(filepath.Join(tmpDir, "stats.json"))
	for i := 0; i < 8; i++ {
		store.RecordLatency("pl-sigma", 40.0+float64(i))
	}
	b := &Bot{statsStore: store}

	metric := metrics.ProxyMetric{
		Name: "PL-sigma", Protocol: "vless", Address: "1.2.3.4:443",
		StableID: "pl-sigma", Online: true, CanConnect: true, CanTransfer: true, LatencyMs: 42,
	}
	text := b.formatDeepDiagnostics(metric, checker.NodeHealth{}, nil)
	if !strings.Contains(text, "Σ=") {
		t.Errorf("deep diag must render jitter as Σ=, got:\n%s", text)
	}
	if strings.Contains(text, "σ=") {
		t.Errorf("legacy σ= glyph found in deep diag:\n%s", text)
	}
}

// TestDiagCacheTimestamp covers the M9.3 accessor contract: ok=false before
// the cache is filled; ok=true with a ~now timestamp after RunDiagnostics
// fills the cache.
func TestDiagCacheTimestamp(t *testing.T) {
	b := &Bot{diagSource: &mockDiagSource{
		reports: []checker.ProxyDiagReport{
			{ProxyName: "P1", Protocol: "vless", Status: "online", Targets: []checker.TargetDiagResult{{Success: true, Latency: 30 * time.Millisecond}}},
		},
	}}

	if _, ok := b.DiagCacheTimestamp(); ok {
		t.Errorf("fresh bot must report ok=false (no cached diagnostics)")
	}

	b.getDiagnosticsReports(false)
	ts, ok := b.DiagCacheTimestamp()
	if !ok {
		t.Fatalf("after getDiagnosticsReports the accessor must report ok=true")
	}
	if diff := time.Since(ts); diff < -5*time.Second || diff > time.Minute {
		t.Errorf("cache timestamp must be ~now, off by %v", diff)
	}
}

// TestFormatSoftHint_Categories pins the short RU phrases per checker error
// category and the escaped-truncated fallback for unknown ones.
func TestFormatSoftHint_Categories(t *testing.T) {
	cases := []struct {
		name     string
		category int
		lastMsg  string
		want     string
	}{
		{"timeout", int(checker.CatTimeout), "context deadline exceeded", "таймаут соединения"},
		{"dns", int(checker.CatDNSError), "no such host", "ошибка DNS"},
		{"tls", int(checker.CatTLSError), "tls handshake failure", "ошибка TLS"},
		{"refused", int(checker.CatConnRefused), "connection refused", "сеть недоступна"},
		{"reset", int(checker.CatConnReset), "reset by peer", "сеть недоступна"},
		{"unknown falls back to escaped msg", int(checker.CatUnknown), "weird failure", "weird failure"},
		{"none falls back", int(checker.CatNone), "mystery", "mystery"},
		{"http5xx falls back", int(checker.CatHTTP5xx), "http 502", "http 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatSoftHint(tc.category, tc.lastMsg)
			if got != tc.want {
				t.Errorf("FormatSoftHint(%d, %q) = %q, want %q", tc.category, tc.lastMsg, got, tc.want)
			}
		})
	}

	t.Run("hostile msg escaped once", func(t *testing.T) {
		got := FormatSoftHint(int(checker.CatUnknown), `<b>x&`)
		if got != `&lt;b&gt;x&amp;` {
			t.Errorf("expected pre-escaped output &lt;b&gt;x&amp;, got %q", got)
		}
		// Entity syntax (&amp;) legitimately contains '&'; raw markup must not survive.
		if strings.ContainsAny(got, "<>") {
			t.Errorf("helper output must contain no raw markup characters, got %q", got)
		}
	})

	t.Run("long msg truncated to ~60 runes", func(t *testing.T) {
		msg := strings.Repeat("ж", 100)
		got := FormatSoftHint(int(checker.CatUnknown), msg)
		if runes := len([]rune(got)); runes > 60 {
			t.Errorf("expected truncation to ≤60 runes, got %d", runes)
		}
	})

	t.Run("empty fallback msg yields empty hint", func(t *testing.T) {
		if got := FormatSoftHint(int(checker.CatUnknown), ""); got != "" {
			t.Errorf("expected empty hint, got %q", got)
		}
	})
}

// TestStatusDownRow_ShowsSoftReason pins the /status offline-row reason
// segment: one short line segment «причина: <hint>», escaped-once, online and
// disabled rows unaffected.
func TestStatusDownRow_ShowsSoftReason(t *testing.T) {
	timeout := metrics.ProxyMetric{
		StableID: "p2", Name: "Proxy2", Online: false,
		LastErrorCategory: int(checker.CatTimeout), LastErrorMsg: "context deadline exceeded",
	}
	row := statusDownRow(timeout)
	if !strings.Contains(row, "🔴 <b>Proxy2</b> — недоступен · <i>причина: таймаут соединения</i>") {
		t.Errorf("expected reason segment on offline row, got: %q", row)
	}
	if strings.Contains(strings.TrimRight(row, "\n"), "\n") {
		t.Errorf("reason must stay on one line, got: %q", row)
	}

	hostile := metrics.ProxyMetric{
		StableID: "p3", Name: "Proxy3", Online: false,
		LastErrorCategory: int(checker.CatUnknown), LastErrorMsg: `<b>x&`,
	}
	row = statusDownRow(hostile)
	if strings.Contains(row, "<b>x&") {
		t.Errorf("hostile LastErrorMsg must be escaped, got: %q", row)
	}
	if !strings.Contains(row, "&lt;b&gt;x&amp;") {
		t.Errorf("expected escaped hint in row, got: %q", row)
	}

	quiet := metrics.ProxyMetric{StableID: "p4", Name: "Proxy4", Online: false}
	row = statusDownRow(quiet)
	if !strings.Contains(row, "🔴 <b>Proxy4</b> — недоступен\n") {
		t.Errorf("row without hint data must keep the bare format, got: %q", row)
	}
	if strings.Contains(row, "причина") {
		t.Errorf("no reason segment expected without category/msg, got: %q", row)
	}
}

// TestStatusText_OfflineRowsHaveReasonOnlyForOffline guards the full /status
// surface: offline rows carry «причина:», online rows do not.
func TestStatusText_OfflineRowsHaveReasonOnlyForOffline(t *testing.T) {
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: "Proxy1", Online: true, LatencyMs: 45},
			{StableID: "p2", Name: "Proxy2", Online: false, LastErrorCategory: int(checker.CatTimeout)},
			{StableID: "p3", Name: "Proxy3", Online: false, Disabled: true},
		},
	}
	b := &Bot{source: src}
	text := b.getStatusText()
	if !strings.Contains(text, "причина: таймаут соединения") {
		t.Errorf("offline row must show the soft reason, got:\n%s", text)
	}
	if strings.Contains(text, "🟢 <b>Proxy1</b> — ⚡45 мс") && strings.Contains(text, "причина:") {
		// The only «причина:» occurrence must belong to Proxy2's row.
		idx := strings.Index(text, "причина:")
		rowStart := strings.LastIndex(text[:idx], "\n")
		if rowStart == -1 {
			rowStart = 0
		}
		if !strings.Contains(text[rowStart:idx], "Proxy2") {
			t.Errorf("«причина:» must only appear on offline rows, got:\n%s", text)
		}
	}
	// The disabled row itself must not gain a reason segment (a «причина:» on
	// some OTHER offline row is expected and covered above).
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "⏸️ <b>Proxy3</b>") && strings.Contains(line, "причина") {
			t.Errorf("disabled rows must not gain a reason segment, got line: %q", line)
		}
	}
}
