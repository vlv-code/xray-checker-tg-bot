package telegram

import (
	"strings"
	"testing"
	"time"
)

// TestIconLegendTable pins the FROZEN LEGEND (wave K): every role maps to its
// exact fallback emoji and custom emoji ID from the official «Telegram iOS
// Icons» pack (catalog https://emoji.wivvi.net/emoji.json, fetched 2026-11-18).
// Roles without a pack icon carry CustomID "" (plain emoji only) — the misses
// are listed explicitly in TestIconLegendMissingRoles.
func TestIconLegendTable(t *testing.T) {
	cases := []struct {
		icon     Icon
		name     string
		fallback string
		customID string
	}{
		{IconStateOK, "IconStateOK", "🟢", ""},
		{IconStateDown, "IconStateDown", "🔴", ""},
		{IconStepSuccess, "IconStepSuccess", "✅", "5774022692642492953"},
		{IconDisabled, "IconDisabled", "⏸️", ""},
		{IconPartialFail, "IconPartialFail", "⚠️", "6030563507299160824"},
		{IconHardError, "IconHardError", "❌", "5774077015388852135"},
		{IconCheckHost, "IconCheckHost", "📡", "6048723247501938454"},
		{IconRFBlock, "IconRFBlock", "⛔", "5938215362473496448"},
		{IconChebur, "IconChebur", "🛡️", ""},
		{IconLatency, "IconLatency", "⚡", "5884428842780594914"},
		{IconTime, "IconTime", "⏱️", "5983150113483134607"},
		{IconMaster, "IconMaster", "🏠", "6042137469204303531"},
		{IconBack, "IconBack", "↩️", "5960671702059848143"},
		{IconWeb, "IconWeb", "🌐", "5776233299424843260"},
		{IconNode, "IconNode", "🖥", ""},
		{IconSummary, "IconSummary", "📊", "5936143551854285132"},
		{IconStats, "IconStats", "📈", "5938539885907415367"},
		{IconDiagnostics, "IconDiagnostics", "🔍", "6032850693348399258"},
		{IconCopy, "IconCopy", "📋", "6034969813032374911"},
		{IconSettings, "IconSettings", "⚙️", "6032742198179532882"},
	}
	for _, tc := range cases {
		if tc.icon.Fallback != tc.fallback {
			t.Errorf("%s: Fallback = %q, want %q", tc.name, tc.icon.Fallback, tc.fallback)
		}
		if tc.icon.CustomID != tc.customID {
			t.Errorf("%s: CustomID = %q, want %q", tc.name, tc.icon.CustomID, tc.customID)
		}
	}
}

// TestIconLegendMissingRoles documents, explicitly, the roles for which the
// pack catalog (fetched 2026-11-18) has NO suitable icon: green circle, red
// circle, pause, server.rack, and the shield (the pack's only shield-named
// icons — «Щит питание» 6030537007350944596, «Щит внимание»
// 6032636795387121097, «Щит галочка» 6030445631921721471 — all carry glyphs
// colliding with existing legend roles ⚡/⚠️/✅, so none is clean; collision
// verdict 2026-11-18). These must stay CustomID "" so users always see a
// semantically correct plain emoji — do NOT "complete" IconChebur with a
// colliding pack id.
func TestIconLegendMissingRoles(t *testing.T) {
	for _, tc := range []struct {
		icon Icon
		name string
	}{
		{IconStateOK, "IconStateOK"},
		{IconStateDown, "IconStateDown"},
		{IconDisabled, "IconDisabled"},
		{IconNode, "IconNode"},
		{IconChebur, "IconChebur"},
	} {
		if tc.icon.CustomID != "" {
			t.Errorf("%s: CustomID = %q, want empty (no pack icon for this role)", tc.name, tc.icon.CustomID)
		}
	}
}

// TestIconCustomIDsDigitOnly guards the Bot API contract: tg-emoji emoji-id is
// a digit-only custom emoji ID string.
func TestIconCustomIDsDigitOnly(t *testing.T) {
	for _, icon := range []Icon{
		IconStepSuccess, IconPartialFail, IconHardError, IconCheckHost,
		IconRFBlock, IconLatency, IconTime, IconMaster, IconBack, IconWeb,
		IconSummary, IconStats, IconDiagnostics, IconCopy, IconSettings,
	} {
		if icon.CustomID == "" {
			t.Errorf("icon %q: expected non-empty CustomID in this list", icon.Fallback)
			continue
		}
		if strings.ContainsFunc(icon.CustomID, func(r rune) bool { return r < '0' || r > '9' }) {
			t.Errorf("icon %q: CustomID %q must be digit-only", icon.Fallback, icon.CustomID)
		}
	}
}

// TestIconRender pins the dual-layer contract: enabled + pack ID → exact
// Bot API 9.4 tg-emoji HTML (raw, never escaped); otherwise the plain fallback.
func TestIconRender(t *testing.T) {
	cases := []struct {
		name    string
		icon    Icon
		enabled bool
		want    string
	}{
		{"enabled with ID renders tg-emoji wrapper", IconStepSuccess, true, `<tg-emoji emoji-id="5774022692642492953">✅</tg-emoji>`},
		{"disabled renders plain fallback", IconStepSuccess, false, "✅"},
		{"enabled without pack ID renders plain fallback", IconDisabled, true, "⏸️"},
		{"enabled state circle (pack miss) renders plain fallback", IconStateOK, true, "🟢"},
		{"zero Icon enabled renders empty", Icon{}, true, ""},
		{"zero Icon disabled renders empty", Icon{}, false, ""},
	}
	for _, tc := range cases {
		if got := tc.icon.Render(tc.enabled); got != tc.want {
			t.Errorf("%s: Render(%v) = %q, want %q", tc.name, tc.enabled, got, tc.want)
		}
	}
}

// TestFmtLatency pins the latency formatter: <1000 → «N мс», else «X.Y с»
// (one decimal), always prefixed with the latency icon fallback.
func TestFmtLatency(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "⚡0 мс"},
		{999, "⚡999 мс"},
		{1000, "⚡1.0 с"},
		{1570, "⚡1.6 с"},
	}
	for _, tc := range cases {
		if got := fmtLatency(tc.ms); got != tc.want {
			t.Errorf("fmtLatency(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

// TestFmtAgeIcon pins the age formatter: «⏱️ N сек/мин/ч/дн» human units.
func TestFmtAgeIcon(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "⏱️ 0 сек"},
		{30 * time.Second, "⏱️ 30 сек"},
		{59 * time.Second, "⏱️ 59 сек"},
		{2 * time.Minute, "⏱️ 2 мин"},
		{59 * time.Minute, "⏱️ 59 мин"},
		{time.Hour, "⏱️ 1 ч"},
		{23 * time.Hour, "⏱️ 23 ч"},
		{24 * time.Hour, "⏱️ 1 дн"},
		{72 * time.Hour, "⏱️ 3 дн"},
		{-time.Minute, "⏱️ 0 сек"},
	}
	for _, tc := range cases {
		if got := fmtAgeIcon(tc.d); got != tc.want {
			t.Errorf("fmtAgeIcon(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
