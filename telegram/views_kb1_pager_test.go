package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// Wave K-B1 RED-2 pins: the new pagination views and the custom-emoji
// dual-layer wiring. These tests introduce NEW symbols (compile-RED was
// acceptable per the wave plan): viewPagerRow / withPagerRow /
// getStatusPageText / StatusPageMarkup / renderStatusPage /
// getIncidentsTextPage / incidentsPagerPrefix / renderIncidentsPage /
// proxyNotFoundText / degradedLabel / getHelpText /
// SetCustomEmojiEnabled / renderIcon / fmtLatencyUnit / fmtLatencyIcon /
// fmtAgeIconDyn.

// withCustomEmoji toggles the package-level dual-layer switch for the test
// duration and restores it afterwards.
func withCustomEmoji(t *testing.T, enabled bool) {
	t.Helper()
	prev := customEmojiEnabled.Load()
	SetCustomEmojiEnabled(enabled)
	t.Cleanup(func() {
		SetCustomEmojiEnabled(prev)
	})
}

// TestRenderIcon_PackageToggle pins the atomic dual-layer switch: formatters
// upgrade to tg-emoji wrappers only while the package flag is set.
func TestRenderIcon_PackageToggle(t *testing.T) {
	withCustomEmoji(t, false)
	if got := renderIcon(IconLatency); got != "⚡" {
		t.Errorf("renderIcon with flag off = %q, want plain fallback", got)
	}
	withCustomEmoji(t, true)
	if got := renderIcon(IconLatency); got != `<tg-emoji emoji-id="5884428842780594914">⚡</tg-emoji>` {
		t.Errorf("renderIcon with flag on = %q, want tg-emoji wrapper", got)
	}
	// Roles without a pack icon stay plain even when enabled.
	if got := renderIcon(IconStateOK); got != "🟢" {
		t.Errorf("state circle must stay plain, got %q", got)
	}
}

// TestFmtLatencyIcon_DualLayer pins the legend latency format through the
// dual layer.
func TestFmtLatencyIcon_DualLayer(t *testing.T) {
	withCustomEmoji(t, false)
	if got := fmtLatencyIcon(122); got != "⚡122 мс" {
		t.Errorf("fmtLatencyIcon(122) = %q, want «⚡122 мс»", got)
	}
	withCustomEmoji(t, true)
	if got := fmtLatencyIcon(122); got != `<tg-emoji emoji-id="5884428842780594914">⚡</tg-emoji>122 мс` {
		t.Errorf("fmtLatencyIcon(122) with flag on = %q, want tg-emoji latency", got)
	}
}

// TestFmtLatencyUnit pins the icon-free latency value used by plain-text
// contexts (rich blocks, <pre> stage lines).
func TestFmtLatencyUnit(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0 мс"},
		{42, "42 мс"},
		{999, "999 мс"},
		{1000, "1.0 с"},
		{1570, "1.6 с"},
	}
	for _, tc := range cases {
		if got := fmtLatencyUnit(tc.ms); got != tc.want {
			t.Errorf("fmtLatencyUnit(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

// TestFmtAgeIconDyn pins the dual-layer age formatter.
func TestFmtAgeIconDyn(t *testing.T) {
	withCustomEmoji(t, false)
	if got := fmtAgeIconDyn(5 * time.Minute); got != "⏱️ 5 мин" {
		t.Errorf("fmtAgeIconDyn off = %q, want «⏱️ 5 мин»", got)
	}
	withCustomEmoji(t, true)
	if got := fmtAgeIconDyn(5 * time.Minute); got != `<tg-emoji emoji-id="5983150113483134607">⏱️</tg-emoji> 5 мин` {
		t.Errorf("fmtAgeIconDyn on = %q, want tg-emoji age", got)
	}
}

// TestEscapeOnce_TgEmojiAndEscapedHostCoexist is the wave hard-rule pin: with
// custom emoji enabled, the raw <tg-emoji> tag and the HTML-escaped proxy
// name must coexist on ONE line — icons are injected AFTER escaping and are
// never re-escaped.
func TestEscapeOnce_TgEmojiAndEscapedHostCoexist(t *testing.T) {
	withCustomEmoji(t, true)
	src := &mockSource{
		metrics: []metrics.ProxyMetric{
			{StableID: "p1", Name: `<b>host&</b>`, Online: true, LatencyMs: 42},
		},
	}
	b := &Bot{source: src}

	text := b.getStatusText()
	rawTag := `<tg-emoji emoji-id="5884428842780594914">⚡</tg-emoji>`
	escapedName := `&lt;b&gt;host&amp;&lt;/b&gt;`
	if !strings.Contains(text, rawTag) {
		t.Errorf("expected RAW tg-emoji tag in output, got:\n%s", text)
	}
	if !strings.Contains(text, escapedName) {
		t.Errorf("expected escaped hostile proxy name in the same output, got:\n%s", text)
	}
	if strings.Contains(text, "&lt;tg-emoji") {
		t.Errorf("tg-emoji tag must never be escaped, got:\n%s", text)
	}
	if !strings.Contains(text, "🟢 <b>"+escapedName+"</b> — "+rawTag+"42 мс") {
		t.Errorf("expected icon and escaped host on one line, got:\n%s", text)
	}
}

// TestViewPagerRow_RouteShapeAndBounds pins the local pager pattern: flip
// buttons carry routePrefix+page, unavailable directions use the wired
// menu:noop placeholder, and the composed callback stays within the Telegram
// 64-byte limit (degrading to menu:noop instead of poisoning the keyboard).
func TestViewPagerRow_RouteShapeAndBounds(t *testing.T) {
	row := viewPagerRow("menu:statuspg:", 1, 3)
	if len(row) != 3 {
		t.Fatalf("expected 3 pager buttons, got %d", len(row))
	}
	if row[0].Text != "·" || row[0].CallbackData != "menu:noop" {
		t.Errorf("prev on first page must be the noop placeholder, got %+v", row[0])
	}
	if row[2].CallbackData != "menu:statuspg:2" {
		t.Errorf("next must emit menu:statuspg:2, got %q", row[2].CallbackData)
	}
	if row[1].Text != "· Стр. 1/3 ·" {
		t.Errorf("unexpected page label %q", row[1].Text)
	}

	rowLast := viewPagerRow("menu:statuspg:", 3, 3)
	if rowLast[0].CallbackData != "menu:statuspg:2" || rowLast[2].CallbackData != "menu:noop" {
		t.Errorf("last page: prev must flip back, next must be noop, got %+v", rowLast)
	}

	hugePrefix := "menu:stats:incpg:" + strings.Repeat("n", 60)
	huge := viewPagerRow(hugePrefix, 2, 3)
	for i, btn := range huge {
		if len(btn.CallbackData) > 64 {
			t.Errorf("pager button %d exceeds 64-byte callback limit: %q", i, btn.CallbackData)
		}
	}
	if huge[0].CallbackData != pagerRouteNoop || huge[2].CallbackData != pagerRouteNoop {
		t.Errorf("oversized flip callbacks must degrade to the noop route, got %+v", huge)
	}
}

// TestStatusPage_PaginationViews pins the paginated /status view: 16 proxies
// over pageSize 15 → two pages, page 2 carries the tail and the disabled
// block, and the markup exposes only wired routes plus the new pager prefix.
func TestStatusPage_PaginationViews(t *testing.T) {
	var ms []metrics.ProxyMetric
	for i := 1; i <= 16; i++ {
		ms = append(ms, metrics.ProxyMetric{
			StableID:  mockStableID(i),
			Name:      fmt.Sprintf("Proxy-%02d", i),
			Address:   "1.2.3.4:443",
			Online:    true,
			LatencyMs: float64(i),
		})
	}
	ms = append(ms, metrics.ProxyMetric{StableID: "p-off", Name: "Zzz-Disabled", Address: "1.2.3.4:443", Disabled: true})
	b := &Bot{source: &mockSource{metrics: ms}}

	text1, pages := b.getStatusPageText(1)
	if pages != 2 {
		t.Fatalf("expected 2 pages for 16 active proxies, got %d", pages)
	}
	if !strings.Contains(text1, "стр. 1 из 2") {
		t.Errorf("expected page note in header, got:\n%s", text1)
	}
	if strings.Contains(text1, "Proxy-16") {
		t.Errorf("page 1 must not contain the 16th proxy, got:\n%s", text1)
	}
	if !strings.Contains(text1, "Proxy-15") {
		t.Errorf("page 1 must contain the 15th proxy, got:\n%s", text1)
	}

	text2, _ := b.getStatusPageText(2)
	if !strings.Contains(text2, "Proxy-16") {
		t.Errorf("page 2 must contain the 16th proxy, got:\n%s", text2)
	}
	if !strings.Contains(text2, "Zzz-Disabled") {
		t.Errorf("disabled block must ride the last page, got:\n%s", text2)
	}

	markup := StatusPageMarkup(1, 2)
	if len(markup.InlineKeyboard) != 3 {
		t.Fatalf("expected 3 rows (pager + actions + back), got %d", len(markup.InlineKeyboard))
	}
	if markup.InlineKeyboard[0][2].CallbackData != "menu:statuspg:2" {
		t.Errorf("expected pager next callback menu:statuspg:2, got %q", markup.InlineKeyboard[0][2].CallbackData)
	}
	if markup.InlineKeyboard[1][0].CallbackData != "menu:status" || markup.InlineKeyboard[1][1].CallbackData != "menu:diag" {
		t.Errorf("action rows must keep the wired routes, got %+v", markup.InlineKeyboard[1])
	}
	if markup.InlineKeyboard[2][0].CallbackData != "menu:main" || !strings.HasPrefix(markup.InlineKeyboard[2][0].Text, "↩️") {
		t.Errorf("back row must be «↩️ Главное меню» on menu:main, got %+v", markup.InlineKeyboard[2][0])
	}

	// Single-page screens must not render a pager row.
	markup1 := StatusPageMarkup(1, 1)
	if len(markup1.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows without pager for a single page, got %d", len(markup1.InlineKeyboard))
	}
}

// TestIncidentsPage_PaginationViews pins the paginated incident journal:
// 17 incidents over pageSize 15 → two pages, «⏱️» durations, and the pager
// appended after the filter-tab base keyboard.
func TestIncidentsPage_PaginationViews(t *testing.T) {
	store := &StatsStore{}
	now := time.Now()
	for i := 1; i <= 17; i++ {
		store.Incidents = append(store.Incidents, Incident{
			ProxyName:   mockIncidentName(i),
			StableID:    mockIncidentID(i),
			DownAt:      now.Add(-time.Duration(i) * time.Hour).Unix(),
			UpAt:        now.Add(-time.Duration(i) * time.Hour).Add(5 * time.Minute).Unix(),
			DurationSec: 300,
			Reason:      "timeout",
		})
	}
	b := &Bot{statsStore: store, nowFunc: func() time.Time { return now }}

	text1, pages := b.getIncidentsTextPage("all", 1)
	if pages != 2 {
		t.Fatalf("expected 2 pages for 17 incidents, got %d", pages)
	}
	if !strings.Contains(text1, "стр. 1 из 2") {
		t.Errorf("expected page note, got:\n%s", text1)
	}
	if strings.Count(text1, "Причина:") != incidentsPageSize {
		t.Errorf("page 1 must carry exactly %d rows, got:\n%s", incidentsPageSize, text1)
	}
	if !strings.Contains(text1, "простой: ⏱️ 5 мин") {
		t.Errorf("durations must render via fmtAgeIcon, got:\n%s", text1)
	}

	text2, _ := b.getIncidentsTextPage("all", 2)
	if strings.Count(text2, "Причина:") != 2 {
		t.Errorf("page 2 must carry the remaining 2 rows, got:\n%s", text2)
	}

	base := &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{
		{pagerButton("Все", "menu:stats:incidents:all")},
	}}
	combined := withPagerRow(base, incidentsPagerPrefix("all"), 1, 2)
	if len(combined.InlineKeyboard) != len(base.InlineKeyboard)+1 {
		t.Fatalf("pager row must be appended to the base keyboard")
	}
	last := combined.InlineKeyboard[len(combined.InlineKeyboard)-1]
	if last[2].CallbackData != "menu:stats:incpg:all:2" {
		t.Errorf("expected incidents pager callback menu:stats:incpg:all:2, got %q", last[2].CallbackData)
	}

	// One-page journals must keep the base keyboard untouched.
	if withPagerRow(base, incidentsPagerPrefix("all"), 1, 1) != base {
		t.Errorf("single-page pager must return the base keyboard unchanged")
	}
}

// TestProxyNotFoundText pins the hard-error wording: ❌ (never ⚠️) plus the
// refresh action hint.
func TestProxyNotFoundText(t *testing.T) {
	text := proxyNotFoundText()
	if !strings.HasPrefix(text, "❌") {
		t.Errorf("hard error must start with ❌, got %q", text)
	}
	if strings.Contains(text, "⚠️") {
		t.Errorf("⚠️ is partial-failure only, got %q", text)
	}
	if !strings.Contains(text, "список изменился") || !strings.Contains(text, "🔄 Обновить") {
		t.Errorf("expected the refresh action hint, got %q", text)
	}
}

// TestDegradedLabel pins the single degraded wording shared by all diag
// surfaces.
func TestDegradedLabel(t *testing.T) {
	if degradedLabel() != "туннель работает, но цель недоступна" {
		t.Errorf("degradedLabel() = %q", degradedLabel())
	}
	b := &Bot{}
	reports := []checker.ProxyDiagReport{
		{ProxyName: "PL-d", Protocol: "vless", Status: "degraded"},
	}
	detail := &strings.Builder{}
	b.formatSingleProxyDiagWithStats(detail, reports[0])
	if !strings.Contains(detail.String(), degradedLabel()) {
		t.Errorf("per-proxy detail line must use degradedLabel, got:\n%s", detail.String())
	}
}

// TestGetHelpText_ExpandableBlockquote pins the /help card: the command wall
// collapses into one expandable blockquote while the header and the alert
// footer stay visible.
func TestGetHelpText_ExpandableBlockquote(t *testing.T) {
	b := &Bot{subs: &mockSubFreshness{}}
	text := b.getHelpText()
	if !strings.Contains(text, "<blockquote expandable>") || !strings.Contains(text, "</blockquote>") {
		t.Errorf("help command list must be an expandable blockquote, got:\n%s", text)
	}
	if strings.Count(text, "<blockquote") != 1 {
		t.Errorf("expected exactly one blockquote, got:\n%s", text)
	}
	if !strings.HasPrefix(text, "🤖 <b>Xray Checker Bot — справка</b>") {
		t.Errorf("header must stay outside the blockquote, got:\n%s", text)
	}
	if !strings.Contains(text, "/stats — статистика аптайма и инцидентов") || !strings.Contains(text, "/delsub &lt;URL&gt;") {
		t.Errorf("command lines must survive inside the blockquote, got:\n%s", text)
	}
	if !strings.HasSuffix(text, "🔔 Уведомления о сбоях отправляются автоматически.") {
		t.Errorf("footer must stay outside the blockquote, got:\n%s", text)
	}
}

// mockStableID / mockProxyName / mockIncidentID / mockIncidentName build
// deterministic fixture names for the pagination pins.
func mockStableID(i int) string { return "p" + strconv.Itoa(i) }
func mockProxyName(i int) string {
	return "Proxy-" + strconv.Itoa(i)
}
func mockIncidentID(i int) string { return "p" + strconv.Itoa(i) }
func mockIncidentName(i int) string {
	return "Incident-" + strconv.Itoa(i)
}
