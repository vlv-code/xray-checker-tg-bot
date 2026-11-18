package telegram

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Single source of truth for bot UI icons: the SF-taxonomy legend and the
// custom-emoji dual layer (Bot API 9.4 <tg-emoji> tags).
//
// Custom emoji IDs come from the official «Telegram iOS Icons» pack
// (https://t.me/addemoji/tgiosicons); IDs were taken from the pack catalog
// https://emoji.wivvi.net/emoji.json, fetched 2026-11-18. Roles without a
// suitable pack icon carry CustomID "" and always render the plain fallback.
//
// Render output is raw HTML and MUST be injected into message text AFTER
// escaping — never pass a tg-emoji tag through escapeHTML again.

// Icon pairs a plain fallback emoji with an optional custom emoji ID from the
// «Telegram iOS Icons» pack. Legend roles are exported vars because Go has no
// struct constants; treat them as read-only.
type Icon struct {
	Fallback string // plain emoji shown when custom emoji is off or the pack misses the role
	CustomID string // digit-only custom emoji ID; "" = no pack icon for this role
}

// Render returns the tg-emoji wrapper `<tg-emoji emoji-id="ID">FALLBACK</tg-emoji>`
// when custom icons are enabled and the pack provides one for this role;
// otherwise the plain fallback emoji.
func (i Icon) Render(enabled bool) string {
	if enabled && i.CustomID != "" {
		return `<tg-emoji emoji-id="` + i.CustomID + `">` + i.Fallback + `</tg-emoji>`
	}
	return i.Fallback
}

var (
	// State circles: the pack has no green/red circle (a neutral ⚪ "Круг"
	// cannot encode ok-vs-down) — plain emoji only, per legend.
	IconStateOK     = Icon{Fallback: "🟢"}
	IconStateDown   = Icon{Fallback: "🔴"}
	IconStepSuccess = Icon{Fallback: "✅", CustomID: "5774022692642492953"}  // «Галочка»
	IconDisabled    = Icon{Fallback: "⏸️"}                                  // no pause icon in pack
	IconPartialFail = Icon{Fallback: "⚠️", CustomID: "6030563507299160824"} // «Внимание»
	IconHardError   = Icon{Fallback: "❌", CustomID: "5774077015388852135"}  // «Крестик»
	// Check-Host: no literal antenna; «Трансляция» (broadcast waves, 🛜) is the
	// closest match to antenna.radiowaves (speaker «Рупор» was the declared
	// fallback anchor).
	IconCheckHost = Icon{Fallback: "📡", CustomID: "6048723247501938454"}
	// RF restriction: no shield.slash in pack; the three shields («питание»,
	// «внимание», «галочка») carry glyphs colliding with other roles, so the
	// prohibition glyph «Ладонь запрет» (🚫) matches the ⛔ fallback semantics.
	IconRFBlock = Icon{Fallback: "⛔", CustomID: "5938215362473496448"}
	// Cheburcheck: no clean shield in pack — the only shield-named icons all
	// carry glyphs colliding with existing legend roles («Щит питание»
	// 6030537007350944596 = ⚡ vs IconLatency «Молния»; «Щит внимание»
	// 6032636795387121097 = ⚠️ vs IconPartialFail «Внимание»; «Щит галочка»
	// 6030445631921721471 = ✅ vs IconStepSuccess «Галочка»), so per the legend
	// rule «если ни один не чист, CustomID=""» the role has no pack icon and
	// renders the plain 🛡️ (catalog fetched 2026-11-18).
	IconChebur  = Icon{Fallback: "🛡️", CustomID: ""}
	IconLatency = Icon{Fallback: "⚡", CustomID: "5884428842780594914"}  // «Молния»
	IconTime    = Icon{Fallback: "⏱️", CustomID: "5983150113483134607"} // «Часы»
	IconMaster  = Icon{Fallback: "🏠", CustomID: "6042137469204303531"}  // «Дом» — master checker only
	IconBack    = Icon{Fallback: "↩️", CustomID: "5960671702059848143"} // «Назад»
	IconWeb     = Icon{Fallback: "🌐", CustomID: "5776233299424843260"}  // «Глобус»
	// Node: no server.rack in pack (the only 🖥 entry, «Finder», is the macOS
	// Finder face, not a server) — plain emoji only, per legend.
	IconNode        = Icon{Fallback: "🖥"}
	IconSummary     = Icon{Fallback: "📊", CustomID: "5936143551854285132"}  // «Статистика график»
	IconStats       = Icon{Fallback: "📈", CustomID: "5938539885907415367"}  // «Рост график» (chart.line.uptrend)
	IconDiagnostics = Icon{Fallback: "🔍", CustomID: "6032850693348399258"}  // «Поиск»
	IconCopy        = Icon{Fallback: "📋", CustomID: "6034969813032374911"}  // «Копировать»
	IconSettings    = Icon{Fallback: "⚙️", CustomID: "6032742198179532882"} // «Настройки»
)

// fmtLatency renders a latency in the legend format: «⚡122 мс» below 1000 ms,
// else «⚡1.5 с» (one decimal). It always uses the plain fallback icon so the
// formatter stays testable without bot state; Phase-2 call sites that respect
// the CustomEmoji setting must prefix IconLatency.Render(b.customEmojiEnabled())
// themselves or route through the dual-layer wrapper.
func fmtLatency(ms int64) string {
	if ms < 1000 {
		return IconLatency.Render(false) + fmt.Sprintf("%d мс", ms)
	}
	return IconLatency.Render(false) + fmt.Sprintf("%.1f с", float64(ms)/1000)
}

// fmtAgeIcon renders a duration in the legend format «⏱️ 2 мин» with human RU
// units (сек/мин/ч/дн). Like fmtLatency it uses the plain fallback so it stays
// testable without bot state.
func fmtAgeIcon(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	var unit string
	switch {
	case d < time.Minute:
		unit = fmt.Sprintf("%d сек", int64(d.Seconds()))
	case d < time.Hour:
		unit = fmt.Sprintf("%d мин", int64(d.Minutes()))
	case d < 24*time.Hour:
		unit = fmt.Sprintf("%d ч", int64(d.Hours()))
	default:
		unit = fmt.Sprintf("%d дн", int64(d.Hours()/24))
	}
	return IconTime.Render(false) + " " + unit
}

// --- Wave K-B1 additive dual-layer wiring (appended; existing foundation
// symbols above are untouched so parallel workers stay compile-safe). ---

// customEmojiEnabled is the package-level dual-layer switch mirrored from the
// bot-runtime CustomEmoji setting. The Bot lives in bot.go (another wave-K
// owner), so the hand-off is this atomic flag: the config wiring calls
// SetCustomEmojiEnabled at startup/toggle, and the formatters below render
// tg-emoji wrappers only while it is set. Zero value (false) = plain emoji —
// identical to the pre-wave behavior.
var customEmojiEnabled atomic.Bool

// SetCustomEmojiEnabled wires the bot-runtime «✨ Иконки (анимация)» setting
// into the package-level formatters. Safe to call from any goroutine.
func SetCustomEmojiEnabled(enabled bool) {
	customEmojiEnabled.Store(enabled)
}

// renderIcon renders an icon through the package-level dual-layer switch:
// the tg-emoji wrapper when custom icons are enabled (and the pack provides
// one), otherwise the plain fallback. Output is raw HTML — inject AFTER
// escapeHTML, never re-escape.
func renderIcon(i Icon) string {
	return i.Render(customEmojiEnabled.Load())
}

// fmtLatencyUnit formats a latency value WITHOUT any icon: «122 мс» below
// 1000, else «1.5 с» (one decimal). Plain-text contexts (rich blocks, <pre>
// stage lines) use it directly; HTML contexts prefer fmtLatencyIcon.
func fmtLatencyUnit(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d мс", ms)
	}
	return fmt.Sprintf("%.1f с", float64(ms)/1000)
}

// fmtLatencyIcon renders a latency in the legend format through the
// dual-layer switch: «⚡122 мс» / «⚡1.5 с», with the latency icon upgraded to
// its tg-emoji wrapper while custom emoji are enabled.
func fmtLatencyIcon(ms int64) string {
	return renderIcon(IconLatency) + fmtLatencyUnit(ms)
}

// fmtAgeIconDyn renders fmtAgeIcon through the dual-layer switch («⏱️ 2 мин»,
// icon upgraded to tg-emoji while custom emoji are enabled).
func fmtAgeIconDyn(d time.Duration) string {
	rest := strings.TrimPrefix(fmtAgeIcon(d), IconTime.Render(false))
	return renderIcon(IconTime) + rest
}
