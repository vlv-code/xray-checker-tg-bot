package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/checker"
)

func btn(text, data string) telego.InlineKeyboardButton {
	return tu.InlineKeyboardButton(text).WithCallbackData(data)
}

func btnURL(text, urlStr string) telego.InlineKeyboardButton {
	return tu.InlineKeyboardButton(text).WithURL(urlStr)
}

// btnIcon builds a callback button with the dual-layer legend icon (wave K-B3;
// the package-level layer is B1's icons.go flag, synced from
// BotConfig.CustomEmoji). Button text has NO parse_mode — it renders verbatim,
// so the label must carry the PLAIN fallback emoji only; the custom icon is
// drawn by Telegram from IconCustomEmojiID placed BEFORE the text (telego
// v1.13 contract, types.go:3845-3849). Raw tg-emoji tags belong to message
// TEXT exclusively (Guardian P1 fix, K+L re-gate 2026-11-18).
func btnIcon(icon Icon, label, data string) telego.InlineKeyboardButton {
	b := btn(icon.Fallback+label, data)
	if customEmojiEnabled.Load() && icon.CustomID != "" {
		b = b.WithIconCustomEmojiID(icon.CustomID)
	}
	return b
}

// mainMenuBtnText is the unified main-menu navigation label (wave K-B3): ☰
// replaces 🏠 on the home shortcut of mixed back rows — 🏠 stays reserved for
// the master checker tab.
const mainMenuBtnText = "☰ Главное меню"

// backRow renders the unified navigation row (wave K-B3): the parent screen
// behind the frozen arrow.uturn.backward icon ↩️, plus the ☰ main-menu
// shortcut. When the parent IS the main menu a single «↩️ Главное меню» row is
// returned (two buttons with the same target would be a dead duplicate).
func backRow(parent, callback string) []telego.InlineKeyboardButton {
	if callback == "menu:main" {
		return []telego.InlineKeyboardButton{
			btnIcon(IconBack, " Главное меню", "menu:main"),
		}
	}
	return []telego.InlineKeyboardButton{
		btnIcon(IconBack, " "+parent, callback),
		btn(mainMenuBtnText, "menu:main"),
	}
}

// truncateButtonText truncates text to maxRunes, adding an ellipsis if exceeded.
func truncateButtonText(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

// HostCallbackKey returns a callback-safe key for a host, hashing if longer than 36 bytes.
func HostCallbackKey(host string) string {
	if len(host) <= 36 {
		return host
	}
	h := sha256.Sum256([]byte(strings.ToLower(host)))
	return "h:" + hex.EncodeToString(h[:6])
}

// MainMenuMarkup returns the inline keyboard for the top-level bot menu.
// Wave K-B3: the «📈 Статистика» row is promoted into the main menu (between
// Ноды and Настройки — removed from the settings screen), the Check-Host row
// carries the 📡 antenna icon (🌐 is freed for web/dashboard mentions), and
// the legend roles with pack icons render through the custom-emoji dual layer.
// Wave M-D (M8): the Cheburcheck row carries the plain 🛡️ shield via
// btnIcon(IconChebur, ...) — IconChebur.CustomID is "" (no clean pack shield),
// so the button never gains an IconCustomEmojiID.
func MainMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🔄 Обновить", "menu:main"),
		),
		tu.InlineKeyboardRow(
			btnIcon(IconDiagnostics, " Подробнее", "menu:diag"),
		),
		tu.InlineKeyboardRow(
			btnIcon(IconCheckHost, " Проверка Check-Host.net", "menu:checkhost"),
		),
		tu.InlineKeyboardRow(
			btnIcon(IconChebur, " Cheburcheck", "menu:chebur"),
		),
		tu.InlineKeyboardRow(
			// Button text stays plain fallback-only even when IconNode gains a
			// pack id — raw tg-emoji output is banned in button text.
			btn(IconNode.Fallback+" Ноды", "menu:nodes"),
		),
		tu.InlineKeyboardRow(
			btnIcon(IconStats, " Статистика", "menu:stats"),
		),
		tu.InlineKeyboardRow(
			btnIcon(IconSettings, " Настройки", "menu:settings"),
		),
	)
}

// NodesMainMenuMarkup returns inline keyboard for the main Nodes section.
func NodesMainMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("➕ Подключить ноду", "menu:nodes:add"),
		),
		tu.InlineKeyboardRow(
			btn("📥 Инструкция по установке", "menu:nodes:install"),
		),
		tu.InlineKeyboardRow(
			btn("🩺 Проверка связи и статуса", "menu:nodes:health"),
		),
		tu.InlineKeyboardRow(
			btn("⚙️ Глобальные настройки нод", "menu:nodes:settings"),
		),
		tu.InlineKeyboardRow(
			btn("⚙️ Настройки нод", "menu:nodes:cfg"),
		),
		tu.InlineKeyboardRow(
			btn("🔄 Обновить", "menu:nodes:refresh"),
			btn(mainMenuBtnText, "menu:main"),
		),
	)
}

// NodesInstallMarkup returns navigation keyboard for the installation guide.
func NodesInstallMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		backRow("К нодам", "menu:nodes"),
	)
}

// NodesHealthMarkup returns keyboard for node health/connectivity check.
// Wave K-B2 register: the health recheck button is the plain «🔄 Обновить».
func NodesHealthMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🔄 Обновить", "menu:nodes:health"),
			btnIcon(IconBack, " К нодам", "menu:nodes"),
		),
		tu.InlineKeyboardRow(
			btn(mainMenuBtnText, "menu:main"),
		),
	)
}

// NodesSettingsMarkup returns keyboard for configuring node sync and alerts.
func NodesSettingsMarkup(cfg BotConfig) *telego.InlineKeyboardMarkup {
	syncStatus := "🔄 Синхронизация с нодами: вкл"
	if !cfg.NodeSyncEnabled {
		syncStatus = "🔄 Синхронизация с нодами: выкл"
	}

	alertsStatus := "🔔 Алерты нод: вкл"
	if !cfg.NodeAlertsEnabled {
		alertsStatus = "🔕 Алерты нод: выкл"
	}

	proxyAlertsStatus := "📢 Алерты прокси нод: в чат"
	if !cfg.NodeProxyAlertsChat {
		proxyAlertsStatus = "📝 Алерты прокси нод: журнал"
	}

	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn(syncStatus, "menu:nodes:toggle_sync"),
		),
		tu.InlineKeyboardRow(
			btn(alertsStatus, "menu:nodes:toggle_alerts"),
		),
		tu.InlineKeyboardRow(
			btn(proxyAlertsStatus, "menu:nodes:toggle_proxy_alerts"),
		),
		// Wave H2 (decision D4): the «📋 Подписки нод» entry to the global
		// subs screen (menu:nodes:subs) is removed — node subscriptions are
		// managed ONLY via the per-node cfg screen (menu:nodes:cfg:<n> →
		// «📋 Подписки ноды»).
		backRow("К нодам", "menu:nodes"),
	)
}

// NodeSubsManageMarkup returns keyboard to manage subscriptions for a specific node.
// Wave-F B2: delsub/addsub callbacks are composed via cbNodeCfg; over-limit
// buttons are omitted individually and one routable hint row (menu:noop)
// points to the /nodesubs command path.
func NodeSubsManageMarkup(node string, subs []ManagedSubInfo) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	omitted := 0
	for i := range subs {
		cb, ok := cbNodeCfg("menu:nodes:delsub:", node, ":"+strconv.Itoa(i))
		if !ok {
			omitted++
			continue
		}
		rows = append(rows, tu.InlineKeyboardRow(
			btn(fmt.Sprintf("🗑 Удалить: #%d", i+1), cb),
		))
	}
	if cb, ok := cbNodeCfg("menu:nodes:addsub:", node); ok {
		rows = append(rows,
			tu.InlineKeyboardRow(
				btn("➕ Назначить подписку", cb),
			),
		)
	} else {
		omitted++
	}
	if omitted > 0 {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("ℹ️ длинное имя ноды — используйте /nodesubs", "menu:noop"),
		))
	}
	// Wave H2 (decision D4): the back row returns to the per-node cfg screen
	// (menu:nodes:cfg:<node>) instead of the removed global subs screen; the
	// back button is omitted via cbNodeCfg when the name would overflow the
	// 64-byte limit (the /nodesubs hint row above stays the command path).
	// Wave K-B3: the back button carries the unified ↩️ arrow.
	var navRow []telego.InlineKeyboardButton
	if cb, ok := cbNodeCfg("menu:nodes:cfg:", node); ok {
		navRow = append(navRow, btnIcon(IconBack, " К настройкам ноды", cb))
	}
	navRow = append(navRow, btn("⚙️ Глобальные настройки нод", "menu:nodes:settings"))
	rows = append(rows, tu.InlineKeyboardRow(navRow...))
	return tu.InlineKeyboard(rows...)
}

// SettingsMenuMarkup returns the settings keyboard. Wave K-B3 accepts an
// optional runtime config (BotConfig) and/or the subscriptions visibility flag
// so the callback render path passes live toggle state; the legacy call forms
// (no arguments / a bare bool) keep compiling for the /settings command path
// (they render the default-state toggle labels).
// NOTE (K-B3 reconciliation for the coordinator): the command-path caller
// replySettings (commands.go, B1's file) still uses the legacy form — switch
// it to SettingsMenuMarkup(b.GetConfig(), b.subs != nil) so the command screen
// shows the real вкл/выкл toggle states.
func SettingsMenuMarkup(args ...any) *telego.InlineKeyboardMarkup {
	var cfg BotConfig
	showSubs := true
	for _, a := range args {
		switch v := a.(type) {
		case BotConfig:
			cfg = v
		case bool:
			showSubs = v
		}
	}
	return SettingsMenuMarkupWithCfg(cfg, showSubs)
}

// SettingsMenuMarkupWithCfg renders the settings keyboard with the live
// runtime state (wave K-B3): the «☀️ Дневная сводка» and «✨ Иконки
// (анимация)» toggles reflect cfg; the «📈 Статистика» entry is REMOVED here
// (promoted to MainMenuMarkup); the Check-Host entry carries the 📡 antenna
// icon and the host-management entry the 🔍 diagnostics icon (🌐 is freed for
// web/dashboard mentions only).
func SettingsMenuMarkupWithCfg(cfg BotConfig, showSubs ...bool) *telego.InlineKeyboardMarkup {
	includeSubs := true
	if len(showSubs) > 0 {
		includeSubs = showSubs[0]
	}

	var alertRow []telego.InlineKeyboardButton
	alertRow = append(alertRow, btn("🧹 Режим алертов", "menu:alert_mode"))
	if includeSubs {
		alertRow = append(alertRow, btn("📋 Подписки", "menu:subs"))
	}

	digestLabel := "☀️ Дневная сводка: выкл"
	if cfg.DayDigestEnabled {
		digestLabel = "☀️ Дневная сводка: вкл"
	}
	iconsLabel := "✨ Иконки (анимация): выкл"
	if cfg.CustomEmoji {
		iconsLabel = "✨ Иконки (анимация): вкл"
	}

	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🚫 Управление прокси", "menu:disabled_proxies:1"),
			btnIcon(IconDiagnostics, " Управление хостами", "menu:disabled_hosts:1"),
		),
		tu.InlineKeyboardRow(
			btnIcon(IconCheckHost, " Фоновый Check-Host", "menu:checkhost_cfg"),
			btnIcon(IconTime, " Интервал проверок", "menu:interval"),
		),
		tu.InlineKeyboardRow(
			btn("🎯 Целевые серверы", "menu:targets"),
			btn("🌙 Тихий режим", "menu:quiet"),
		),
		tu.InlineKeyboardRow(
			btn(digestLabel, "menu:nodes:digest"),
			btn(iconsLabel, "menu:toggle_icons"),
		),
		alertRow,
		tu.InlineKeyboardRow(
			btn("🕒 Часовой пояс", "menu:timezone"),
		),
		tu.InlineKeyboardRow(
			btn("🚀 Проверить обновления", "menu:checkupdate"),
		),
		backRow("Главное меню", "menu:main"),
	)
}

// CheckHostSettingsMarkup returns keyboard for configuring background Check-Host auditing.
// Wave K-B3: the Check-Host surfaces carry the 📡 antenna icon (never 🌐).
func CheckHostSettingsMarkup(cfg BotConfig) *telego.InlineKeyboardMarkup {
	bgLabel := " Фоновая проверка: выкл"
	if cfg.CheckHostBgEnabled {
		bgLabel = " Фоновая проверка: вкл"
	}

	alertStatus := "🔕 Алерты по РФ: выкл"
	if cfg.CheckHostAlertEnabled {
		alertStatus = "🔔 Алерты по РФ: вкл"
	}

	intHours := cfg.CheckHostIntervalHours
	if intHours <= 0 {
		intHours = 1
	}

	makeIntBtn := func(h int) telego.InlineKeyboardButton {
		icon := "⚪"
		if intHours == h {
			icon = "🟢"
		}
		text := fmt.Sprintf("%s %d ч", icon, h)
		return btn(text, fmt.Sprintf("menu:checkhost:int:%d", h))
	}

	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btnIcon(IconCheckHost, bgLabel, "menu:checkhost:toggle_bg"),
		),
		tu.InlineKeyboardRow(
			btn(alertStatus, "menu:checkhost:toggle_alert"),
		),
		tu.InlineKeyboardRow(
			makeIntBtn(1),
			makeIntBtn(2),
			makeIntBtn(4),
			makeIntBtn(6),
			makeIntBtn(12),
		),
		tu.InlineKeyboardRow(
			btn("🚀 Запустить проверку сейчас", "menu:checkhost:run_now"),
		),
		backRow("К настройкам", "menu:settings"),
	)
}

// ProxyToggleItem holds display information for a toggleable proxy.
type ProxyToggleItem struct {
	StableID string
	Name     string
	Protocol string
	Address  string
	Disabled bool
}

// DisabledProxiesMarkup generates paginated toggles for individual proxy connection checks.
func DisabledProxiesMarkup(items []ProxyToggleItem, page, totalPages int) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton

	for _, item := range items {
		// Plain state circles (no pack icon exists for 🟢/⏸️): button text must
		// stay fallback-only — raw tg-emoji output is banned in button text.
		statusIcon := IconStateOK.Fallback
		if item.Disabled {
			statusIcon = IconDisabled.Fallback
		}
		displayName := item.Name
		if displayName == "" {
			displayName = item.Address
		}
		runes := []rune(displayName)
		if len(runes) > 28 {
			displayName = string(runes[:25]) + "..."
		}
		btnText := fmt.Sprintf("%s %s", statusIcon, displayName)
		rows = append(rows, tu.InlineKeyboardRow(
			btn(btnText, fmt.Sprintf("menu:toggle_proxy:%s:%d", item.StableID, page)),
		))
	}

	if totalPages > 1 {
		var prevBtn telego.InlineKeyboardButton
		if page > 1 {
			prevBtn = btn("⬅️ Пред", fmt.Sprintf("menu:disabled_proxies:%d", page-1))
		} else {
			prevBtn = btn("·", "menu:noop")
		}

		var nextBtn telego.InlineKeyboardButton
		if page < totalPages {
			nextBtn = btn("След ➡️", fmt.Sprintf("menu:disabled_proxies:%d", page+1))
		} else {
			nextBtn = btn("·", "menu:noop")
		}

		rows = append(rows, tu.InlineKeyboardRow(
			prevBtn,
			btn(fmt.Sprintf("%d / %d", page, totalPages), "menu:noop"),
			nextBtn,
		))
	}

	rows = append(rows, backRow("К настройкам", "menu:settings"))

	return tu.InlineKeyboard(rows...)
}

// DisabledHostsMarkup generates paginated toggles for host checks.
func DisabledHostsMarkup(hosts []string, disabledMap map[string]bool, page, totalPages int) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton

	for _, host := range hosts {
		// Plain state circles (no pack icon exists for 🟢/⏸️): button text must
		// stay fallback-only — raw tg-emoji output is banned in button text.
		statusIcon := IconStateOK.Fallback
		if disabledMap[strings.ToLower(host)] {
			statusIcon = IconDisabled.Fallback
		}
		displayHost := host
		runes := []rune(displayHost)
		if len(runes) > 28 {
			displayHost = string(runes[:25]) + "..."
		}
		btnText := fmt.Sprintf("%s %s", statusIcon, displayHost)
		rows = append(rows, tu.InlineKeyboardRow(
			btn(btnText, fmt.Sprintf("menu:toggle_host:%s:%d", HostCallbackKey(host), page)),
		))
	}

	if totalPages > 1 {
		var prevBtn telego.InlineKeyboardButton
		if page > 1 {
			prevBtn = btn("⬅️ Пред", fmt.Sprintf("menu:disabled_hosts:%d", page-1))
		} else {
			prevBtn = btn("·", "menu:noop")
		}

		var nextBtn telego.InlineKeyboardButton
		if page < totalPages {
			nextBtn = btn("След ➡️", fmt.Sprintf("menu:disabled_hosts:%d", page+1))
		} else {
			nextBtn = btn("·", "menu:noop")
		}

		rows = append(rows, tu.InlineKeyboardRow(
			prevBtn,
			btn(fmt.Sprintf("%d / %d", page, totalPages), "menu:noop"),
			nextBtn,
		))
	}

	rows = append(rows, backRow("К настройкам", "menu:settings"))

	return tu.InlineKeyboard(rows...)
}

// StatusMenuMarkup returns controls under the /status view.
func StatusMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🔄 Обновить", "menu:status"),
			btnIcon(IconDiagnostics, " Подробнее", "menu:diag"),
		),
		backRow("Главное меню", "menu:main"),
	)
}

// StatsMenuMarkup returns buttons for outage stats. Wave K-B3: the stats
// overview is promoted into the main menu, so the back row keeps the unified
// settings return as the secondary navigation.
func StatsMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("📋 Журнал инцидентов", "menu:stats:incidents"),
			btn("🔝 Топ проблемных", "menu:stats:top"),
		),
		tu.InlineKeyboardRow(
			btnIcon(IconSummary, " По протоколам", "menu:stats:protocols"),
			btn("🌡️ Heatmap", "menu:stats:heatmap"),
		),
		backRow("К настройкам", "menu:settings"),
	)
}

// QuietHoursMarkup generates controls for quiet hours and snooze.
func QuietHoursMarkup(cfg BotConfig) *telego.InlineKeyboardMarkup {
	toggleText := "🌙 Включить расписание"
	if cfg.QuietHoursEnabled {
		toggleText = "☀️ Отключить расписание"
	}

	var rows [][]telego.InlineKeyboardButton
	rows = append(rows, tu.InlineKeyboardRow(
		btn(toggleText, "menu:quiet:toggle"),
	))

	morningTime := "08:00"
	if cfg.QuietHoursEnd != "" {
		morningTime = cfg.QuietHoursEnd
	}

	now := time.Now().Unix()
	if cfg.QuietSnoozeUntil > now {
		remaining := time.Duration(cfg.QuietSnoozeUntil-now) * time.Second
		rows = append(rows, tu.InlineKeyboardRow(
			btn(fmt.Sprintf("🔔 Снять паузу (осталось %s)", FormatDowntime(remaining)), "menu:quiet:unsnooze"),
		))
	} else {
		nowLocal := time.Now().In(cfg.Location())
		hour := nowLocal.Hour()
		endH, _, _ := parseTimeOfDay(morningTime)
		if endH <= 0 {
			endH = 8
		}
		isEveningOrNight := hour >= 18 || hour < endH

		snoozeBtns := []telego.InlineKeyboardButton{
			btn("💤 Пауза 1ч", "menu:quiet:snooze:1h"),
			btn("💤 Пауза 4ч", "menu:quiet:snooze:4h"),
		}
		if isEveningOrNight {
			snoozeBtns = append(snoozeBtns, btn(fmt.Sprintf("🌅 До утра (%s)", morningTime), "menu:quiet:snooze:morning"))
		}
		rows = append(rows, tu.InlineKeyboardRow(snoozeBtns...))
	}

	rows = append(rows, backRow("К настройкам", "menu:settings"))

	return tu.InlineKeyboard(rows...)
}

// AlertModeMarkup creates the toggle menu for live vs clean alert mode.
func AlertModeMarkup(cfg BotConfig) *telego.InlineKeyboardMarkup {
	liveIcon := "⚪"
	cleanIcon := "⚪"
	if cfg.AlertMode == AlertModeClean {
		cleanIcon = "🟢"
	} else {
		liveIcon = "🟢"
	}

	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn(fmt.Sprintf("%s 🔄 Live-режим", liveIcon), "menu:alert_mode:live"),
			btn(fmt.Sprintf("%s 🧹 Чистый чат", cleanIcon), "menu:alert_mode:clean"),
		),
		backRow("К настройкам", "menu:settings"),
	)
}

// TargetsMenuMarkup displays target URLs controls.
func TargetsMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btnIcon(IconDiagnostics, " Подробнее", "menu:diag"),
		),
		backRow("К настройкам", "menu:settings"),
	)
}

// BackToMenuMarkup provides a simple button returning to main menu.
func BackToMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn(mainMenuBtnText, "menu:main"),
		),
	)
}

// BackToSettingsMarkup provides buttons returning to settings or main menu.
func BackToSettingsMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		backRow("К настройкам", "menu:settings"),
	)
}

// BackToStatsMarkup provides buttons returning to stats overview or main menu.
func BackToStatsMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		backRow("К статистике", "menu:stats"),
	)
}

// SubsMenuMarkup returns the keyboard for the global subscriptions screen
// (wave K-B3): the «➕ Добавить подписку» arm button starts the admin-gated
// input flow (exact route menu:subsadd — the next plain text is consumed as a
// subscription URL), and the back row is the unified pair.
func SubsMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("➕ Добавить подписку", "menu:subsadd"),
		),
		backRow("К настройкам", "menu:settings"),
	)
}

// TimezoneMarkup returns keyboard for selecting bot timezone.
func TimezoneMarkup(currentTz string) *telego.InlineKeyboardMarkup {
	if currentTz == "" {
		currentTz = "Local"
	}

	mark := func(tz, label string) string {
		if strings.EqualFold(currentTz, tz) {
			return "🟢 " + label
		}
		return "⚪ " + label
	}

	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn(mark("UTC", "UTC (UTC+0)"), "menu:tz:UTC"),
			btn(mark("Europe/Kaliningrad", "Калининград (+2)"), "menu:tz:Europe/Kaliningrad"),
		),
		tu.InlineKeyboardRow(
			btn(mark("Europe/Moscow", "Москва (+3, MSK)"), "menu:tz:Europe/Moscow"),
			btn(mark("Europe/Samara", "Самара (+4)"), "menu:tz:Europe/Samara"),
		),
		tu.InlineKeyboardRow(
			btn(mark("Asia/Yekaterinburg", "Екатеринбург (+5)"), "menu:tz:Asia/Yekaterinburg"),
			btn(mark("Asia/Omsk", "Омск (+6)"), "menu:tz:Asia/Omsk"),
		),
		tu.InlineKeyboardRow(
			btn(mark("Asia/Krasnoyarsk", "Красноярск (+7)"), "menu:tz:Asia/Krasnoyarsk"),
			btn(mark("Asia/Irkutsk", "Иркутск (+8)"), "menu:tz:Asia/Irkutsk"),
		),
		tu.InlineKeyboardRow(
			btn(mark("Asia/Yakutsk", "Якутск (+9)"), "menu:tz:Asia/Yakutsk"),
			btn(mark("Asia/Vladivostok", "Владивосток (+10)"), "menu:tz:Asia/Vladivostok"),
		),
		tu.InlineKeyboardRow(
			btn(mark("Europe/Kyiv", "Киев (+2/3)"), "menu:tz:Europe/Kyiv"),
			btn(mark("Asia/Almaty", "Алматы / Астана (+5)"), "menu:tz:Asia/Almaty"),
		),
		tu.InlineKeyboardRow(
			btn(mark("Local", "🖥️ Серверное (Local)"), "menu:tz:Local"),
		),
		backRow("К настройкам", "menu:settings"),
	)
}

// IntervalMenuMarkup returns keyboard for selecting proxy check interval.
func IntervalMenuMarkup(currentInterval int) *telego.InlineKeyboardMarkup {
	mark := func(sec int, label string) string {
		if currentInterval == sec {
			return "🟢 " + label
		}
		return "⚪ " + label
	}

	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn(mark(30, "30 сек"), "menu:interval:set:30"),
			btn(mark(60, "1 мин"), "menu:interval:set:60"),
		),
		tu.InlineKeyboardRow(
			btn(mark(120, "2 мин"), "menu:interval:set:120"),
			btn(mark(300, "5 мин"), "menu:interval:set:300"),
		),
		tu.InlineKeyboardRow(
			btn(mark(600, "10 мин"), "menu:interval:set:600"),
			btn(mark(900, "15 мин"), "menu:interval:set:900"),
		),
		backRow("К настройкам", "menu:settings"),
	)
}

// menuListPageSize caps the number of list items (proxies for the Check-Host
// picker, unique hosts for the Cheburcheck picker) per screen. Wave G4:
// pagination replaces the wave-F cap+stub approach.
const menuListPageSize = 20

// menuTotalPages computes the page count for a paginated menu list.
func menuTotalPages(total, pageSize int) int {
	if total <= 0 || pageSize <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

// menuPageSlice returns the items of one paginated page, with the page
// defensively clamped into [1, totalPages].
func menuPageSlice[T any](items []T, page, totalPages int) []T {
	if page < 1 {
		page = 1
	}
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * menuListPageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + menuListPageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

// menuPaginationRow renders the «⬅️ · Стр. N/M · ➡️» row for paginated picker
// menus; prev/next emit the given callback-route prefix for their target page
// (or the routable menu:noop placeholder «·» when the direction is
// unavailable). Wave G4.
func menuPaginationRow(routePrefix string, page, totalPages int) []telego.InlineKeyboardButton {
	var prevBtn telego.InlineKeyboardButton
	if page > 1 {
		prevBtn = btn("⬅️", fmt.Sprintf("%s%d", routePrefix, page-1))
	} else {
		prevBtn = btn("·", "menu:noop")
	}
	var nextBtn telego.InlineKeyboardButton
	if page < totalPages {
		nextBtn = btn("➡️", fmt.Sprintf("%s%d", routePrefix, page+1))
	} else {
		nextBtn = btn("·", "menu:noop")
	}
	return []telego.InlineKeyboardButton{
		prevBtn,
		btn(fmt.Sprintf("Стр. %d/%d", page, totalPages), "menu:noop"),
		nextBtn,
	}
}

// CheckHostMenuMarkup returns the keyboard for the Check-Host host list.
// Wave H6: the list is the UNION of subscription hosts (auto rows — a single
// «<host> · авто» check button, not removable) and manually added customs
// (check button plus a «🗑» delete button, menu:checkhostdel:<key>); the
// «➕ Добавить хост» row arms the manual input flow (menu:checkhostadd).
// Wave G4: pagination — menuListPageSize rows per screen and a
// «⬅️ ➡️ Стр. N/M» row (menu:checkhostpg:<page> routes) when more than one
// page exists. Host keys hash over 36 bytes (HostCallbackKey), so every
// callback stays within the 64-byte limit.
// Wave K-B3: the auto rows and the add-host row carry the 📡 antenna icon
// (IconCheckHost — 🌐 is freed for web/dashboard mentions); the back row is
// the unified «↩️ Главное меню».
func CheckHostMenuMarkup(items []CheckHostListItem, page, totalPages int) *telego.InlineKeyboardMarkup {
	pageItems := menuPageSlice(items, page, totalPages)

	var rows [][]telego.InlineKeyboardButton
	for _, item := range pageItems {
		key := HostCallbackKey(item.Address)
		if item.Auto {
			rows = append(rows, tu.InlineKeyboardRow(
				btnIcon(IconCheckHost, " "+truncateButtonText(item.Address, 28)+" · авто", "menu:checkhosth:"+key),
			))
		} else {
			rows = append(rows, tu.InlineKeyboardRow(
				btn("✍️ "+truncateButtonText(item.Address, 28), "menu:checkhosth:"+key),
				btn("🗑", "menu:checkhostdel:"+key),
			))
		}
	}

	rows = append(rows, tu.InlineKeyboardRow(
		btnIcon(IconCheckHost, " ➕ Добавить хост", "menu:checkhostadd"),
	))

	if totalPages > 1 {
		rows = append(rows, menuPaginationRow("menu:checkhostpg:", page, totalPages))
	}

	rows = append(rows, backRow("Главное меню", "menu:main"))

	return tu.InlineKeyboard(rows...)
}

// CheburMenuMarkup returns keyboard for selecting a host for the active
// blocking probe (Cheburcheck). Wave G1: the list is UNIQUE bare hosts
// (cheburHostsFromSnapshot — port stripped, protocol-agnostic dedup, sorted),
// since Cheburcheck probes hosts and has no port concept; buttons carry
// menu:cheburh:<HostCallbackKey(host)> (host keys hash over 36 bytes,
// test-pinned ≤64). Wave G4: the wave-F cap+stub is replaced by pagination.
// Wave H3: host buttons are PLAIN <host> (the former 🧀 prefix is dropped).
// Wave H4: the «✍️ Проверить свой хост» row (exact route menu:cheburin)
// arms the manual host input flow.
// Wave K-B3: the back row is the unified «↩️ Главное меню».
func CheburMenuMarkup(hosts []string, page, totalPages int) *telego.InlineKeyboardMarkup {
	pageHosts := menuPageSlice(hosts, page, totalPages)

	var rows [][]telego.InlineKeyboardButton
	for i := 0; i < len(pageHosts); i += 2 {
		if i+1 < len(pageHosts) {
			rows = append(rows, tu.InlineKeyboardRow(
				btn(truncateButtonText(pageHosts[i], 28), "menu:cheburh:"+HostCallbackKey(pageHosts[i])),
				btn(truncateButtonText(pageHosts[i+1], 28), "menu:cheburh:"+HostCallbackKey(pageHosts[i+1])),
			))
		} else {
			rows = append(rows, tu.InlineKeyboardRow(
				btn(truncateButtonText(pageHosts[i], 28), "menu:cheburh:"+HostCallbackKey(pageHosts[i])),
			))
		}
	}

	rows = append(rows, tu.InlineKeyboardRow(
		btn("✍️ Проверить свой хост", "menu:cheburin"),
	))

	if totalPages > 1 {
		rows = append(rows, menuPaginationRow("menu:cheburpg:", page, totalPages))
	}

	rows = append(rows, backRow("Главное меню", "menu:main"))

	return tu.InlineKeyboard(rows...)
}

// CheburResultMarkup returns the navigation keyboard under a Cheburcheck probe
// (wave K-B3): «📡 К списку хостов» returns to the host list page the probe
// was launched from (menu:cheburpg:<page> — the open/pager routes record the
// page per chat), plus the unified main-menu row. The probe placeholder uses
// the same markup so the way back never disappears mid-probe.
func CheburResultMarkup(page int) *telego.InlineKeyboardMarkup {
	if page < 1 {
		page = 1
	}
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("📡 К списку хостов", fmt.Sprintf("menu:cheburpg:%d", page)),
		),
		tu.InlineKeyboardRow(
			btn(mainMenuBtnText, "menu:main"),
		),
	)
}

// DiagDeepLink represents a proxy for which a deep diagnostics button is shown.
type DiagDeepLink struct {
	Name     string
	StableID string
}

// DiagPaginationMarkup builds inline keyboard for paginated detailed report (Level 2).
func DiagPaginationMarkup(page, totalPages int, _ ...DiagDeepLink) *telego.InlineKeyboardMarkup {
	if page < 1 {
		page = 1
	}
	if totalPages < 1 {
		totalPages = 1
	}

	var rows [][]telego.InlineKeyboardButton

	// Pagination row if more than 1 page
	if totalPages > 1 {
		var prevBtn telego.InlineKeyboardButton
		if page > 1 {
			prevBtn = btn("⬅️ Пред", fmt.Sprintf("menu:diag:details:%d", page-1))
		} else {
			prevBtn = btn("·", "menu:noop")
		}

		var nextBtn telego.InlineKeyboardButton
		if page < totalPages {
			nextBtn = btn("След ➡️", fmt.Sprintf("menu:diag:details:%d", page+1))
		} else {
			nextBtn = btn("·", "menu:noop")
		}

		rows = append(rows, tu.InlineKeyboardRow(
			prevBtn,
			btn(fmt.Sprintf("%d / %d", page, totalPages), fmt.Sprintf("menu:diag:noop:%d:%d", page, totalPages)),
			nextBtn,
		))
	}

	// Trigger deep connection diagnostics on any proxy from this page
	rows = append(rows, tu.InlineKeyboardRow(
		btn("🔬 Углубленная проверка соединения", fmt.Sprintf("menu:diag:pick_deep:%d", page)),
	))

	// Action row: Back to Detailed Summary + Refresh current page
	rows = append(rows, tu.InlineKeyboardRow(
		btnIcon(IconBack, " К подробной сводке", "menu:diag"),
		btn("🔄 Обновить", fmt.Sprintf("menu:diag:refresh:details:%d", page)),
	))

	// Back row
	rows = append(rows, tu.InlineKeyboardRow(
		btn(mainMenuBtnText, "menu:main"),
	))

	return tu.InlineKeyboard(rows...)
}

// DeepDiagnosticsMarkup returns navigation buttons for Level 3 Deep diagnostics.
// Wave-F B1: both node-name-bearing callbacks (back to details and the deep
// recheck, the latter carrying the "node/id" stableID form) are composed via
// cbNodeCfg over the FULL callback incl. the page suffix; a button whose
// callback would exceed Telegram's 64-byte callback_data limit is omitted.
func DeepDiagnosticsMarkup(stableID string, page ...int) *telego.InlineKeyboardMarkup {
	curPage := 1
	if len(page) > 0 && page[0] > 0 {
		curPage = page[0]
	}
	backTarget := ""
	if strings.Contains(stableID, "/") {
		backTarget = strings.Split(stableID, "/")[0]
	}
	backCb := fmt.Sprintf("menu:diag:details:%d", curPage)
	backOK := true
	if backTarget != "" && backTarget != "local" {
		backCb, backOK = cbNodeCfg("menu:diag:details:", backTarget, ":"+strconv.Itoa(curPage))
	}
	recheckCb, recheckOK := cbNodeCfg("menu:diag:deep:", stableID, ":"+strconv.Itoa(curPage))

	var firstRow []telego.InlineKeyboardButton
	if backOK {
		firstRow = append(firstRow, btnIcon(IconBack, " К детальному отчёту", backCb))
	}
	if recheckOK {
		firstRow = append(firstRow, btn("🔄 Перепроверить", recheckCb))
	}

	var rows [][]telego.InlineKeyboardButton
	if len(firstRow) > 0 {
		rows = append(rows, firstRow)
	}
	rows = append(rows, tu.InlineKeyboardRow(
		btn(mainMenuBtnText, "menu:main"),
	))
	return tu.InlineKeyboard(rows...)
}

// PickDeepDiagnosticsMarkup returns keyboard to select any proxy for deep diagnostics.
func PickDeepDiagnosticsMarkup(reports []checker.ProxyDiagReport, page int) *telego.InlineKeyboardMarkup {
	return PickDeepDiagnosticsMarkupWithTabs("local", nil, reports, page)
}

// PickDeepDiagnosticsMarkupWithTabs returns keyboard with node tabs to select any proxy for deep diagnostics.
// Wave-F B1: every node-name-bearing callback (tabs, per-proxy deep rows with
// the "node/id" stableID form, and the details back button) is composed via
// cbNodeCfg over the FULL callback incl. the page suffix; over-limit buttons
// are omitted individually and one routable hint row (menu:noop) replaces them.
func PickDeepDiagnosticsMarkupWithTabs(target string, tabs []NodeTabItem, reports []checker.ProxyDiagReport, page int) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	omitted := 0
	if len(tabs) > 1 {
		var tabButtons []telego.InlineKeyboardButton
		for _, t := range tabs {
			cb, ok := cbNodeCfg("menu:diag:pick_deep:", t.ID, ":1")
			if !ok {
				omitted++
				continue
			}
			title := t.Label
			if t.Status != "" {
				title += fmt.Sprintf(" (%s)", t.Status)
			}
			if t.IsActive {
				title = fmt.Sprintf("• %s •", title)
			}
			tabButtons = append(tabButtons, btn(title, cb))
		}
		if len(tabButtons) > 0 {
			rows = append(rows, tabButtons)
		}
	}

	for _, rep := range reports {
		// Plain state circles (no pack icon exists for 🟢/🔴/⏸️): button text
		// must stay fallback-only — raw tg-emoji output is banned in buttons.
		icon := IconStateOK.Fallback
		switch rep.Status {
		case "offline":
			icon = IconStateDown.Fallback
		case "degraded":
			icon = "🟡"
		case "disabled":
			icon = IconDisabled.Fallback
		}
		cb, ok := cbNodeCfg("menu:diag:deep:", rep.StableID, ":"+strconv.Itoa(page))
		if !ok {
			omitted++
			continue
		}
		btnText := truncateButtonText(fmt.Sprintf("%s %s", icon, rep.ProxyName), 30)
		rows = append(rows, tu.InlineKeyboardRow(
			btn(btnText, cb),
		))
	}

	backTarget := target
	if backTarget == "" {
		backTarget = "local"
	}
	if backTarget == "local" {
		rows = append(rows, tu.InlineKeyboardRow(
			btnIcon(IconBack, " К детальному отчёту", fmt.Sprintf("menu:diag:details:%d", page)),
		))
	} else if cb, ok := cbNodeCfg("menu:diag:details:", backTarget, ":"+strconv.Itoa(page)); ok {
		rows = append(rows, tu.InlineKeyboardRow(
			btnIcon(IconBack, " К детальному отчёту", cb),
		))
	} else {
		omitted++
	}
	if omitted > 0 {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("ℹ️ длинное имя ноды — используйте /diag", "menu:noop"),
		))
	}
	return tu.InlineKeyboard(rows...)
}

// CheckHostResultMarkup returns keyboard after a Check-Host scan.
// Wave K-B3: «📡 К списку хостов» (the antenna icon replaces 🌐 on all
// Check-Host surfaces) plus the unified main-menu row.
func CheckHostResultMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("📡 К списку хостов", "menu:checkhost"),
			btn(mainMenuBtnText, "menu:main"),
		),
	)
}

// RichReportMarkup returns buttons under the Level 1 detailed summary report.
func RichReportMarkup(deepLinks ...DiagDeepLink) *telego.InlineKeyboardMarkup {
	return RichReportMarkupWithTabs("local", nil, deepLinks...)
}

// NodeTabItem describes one tab in multi-node navigation.
type NodeTabItem struct {
	ID       string // "local" or node name
	Label    string // e.g. "🏠 Мастер" or "🖥 m31a"
	Status   string // e.g. "14/15" or "⚠️ 14/17"
	IsActive bool
}

// RichReportMarkupWithTabs returns buttons under the Level 1 detailed summary report with node tabs.
// Wave-F B1: node-name-bearing callbacks (tabs, details drill-down, quick deep
// links with the "node/id" stableID form, refresh) are composed via cbNodeCfg;
// over-limit buttons are omitted individually (the /diag command remains the
// universal path for long node names).
func RichReportMarkupWithTabs(target string, tabs []NodeTabItem, deepLinks ...DiagDeepLink) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton

	// Tab bar row (if multiple tabs exist)
	if len(tabs) > 1 {
		var tabButtons []telego.InlineKeyboardButton
		for _, t := range tabs {
			cb, ok := cbNodeCfg("menu:diag:node:", t.ID)
			if !ok {
				continue
			}
			title := t.Label
			if t.Status != "" {
				title += fmt.Sprintf(" (%s)", t.Status)
			}
			if t.IsActive {
				title = "• " + title + " •"
			}
			tabButtons = append(tabButtons, btn(title, cb))
		}
		if len(tabButtons) > 0 {
			rows = append(rows, tabButtons)
		}
	}

	// Primary action: Drill down into paginated detailed report for target
	detailsCallback := "menu:diag:details:1"
	if target != "local" && target != "" {
		detailsCallback, _ = cbNodeCfg("menu:diag:details:", target, ":1")
	}
	if detailsCallback != "" {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("📑 Детальный отчёт", detailsCallback),
		))
	}

	// Quick deep links for problematic proxies (only for local target where deep engine runs)
	if target == "local" || target == "" {
		for _, dl := range deepLinks {
			cb, ok := cbNodeCfg("menu:diag:deep:", dl.StableID, ":1")
			if !ok {
				continue
			}
			rows = append(rows, tu.InlineKeyboardRow(
				btn(fmt.Sprintf("🔬 Углублённая: %s", truncateButtonText(dl.Name, 24)), cb),
			))
		}
	}

	// Refresh and navigation
	refreshCallback := "menu:diag:refresh:summary"
	if target != "local" && target != "" {
		refreshCallback, _ = cbNodeCfg("menu:diag:refresh:", target)
	}
	refreshRow := []telego.InlineKeyboardButton{}
	if refreshCallback != "" {
		refreshRow = append(refreshRow, btn("🔄 Обновить", refreshCallback))
	}
	refreshRow = append(refreshRow, btn(mainMenuBtnText, "menu:main"))
	rows = append(rows, refreshRow)

	return tu.InlineKeyboard(rows...)
}

// DiagnosticsPaginationMarkupWithTabs returns paginated navigation for detailed report with node tabs.
// Wave-F B1: node-name-bearing callbacks (tabs, prev/next, pick_deep, back
// summary, refresh:details) are composed via cbNodeCfg over the FULL callback
// incl. the page suffix; over-limit buttons are omitted (prev/next fall back
// to the static «·» noop placeholder) and one routable hint row (menu:noop)
// is appended when any button was dropped.
func DiagnosticsPaginationMarkupWithTabs(target string, page, totalPages int, tabs []NodeTabItem) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	omitted := 0

	// Tab bar row (if multiple tabs exist)
	if len(tabs) > 1 {
		var tabButtons []telego.InlineKeyboardButton
		for _, t := range tabs {
			cb, ok := cbNodeCfg("menu:diag:details:", t.ID, ":1")
			if !ok {
				omitted++
				continue
			}
			title := t.Label
			if t.IsActive {
				title = "• " + title + " •"
			}
			tabButtons = append(tabButtons, btn(title, cb))
		}
		if len(tabButtons) > 0 {
			rows = append(rows, tabButtons)
		}
	}

	// Pagination row if more than 1 page
	if totalPages > 1 {
		var prevBtn telego.InlineKeyboardButton
		if page > 1 {
			prevCb, ok := fmt.Sprintf("menu:diag:details:%d", page-1), true
			if target != "local" && target != "" {
				prevCb, ok = cbNodeCfg("menu:diag:details:", target, ":"+strconv.Itoa(page-1))
			}
			if ok {
				prevBtn = btn("⬅️ Пред", prevCb)
			} else {
				prevBtn = btn("·", "menu:noop")
				omitted++
			}
		} else {
			prevBtn = btn("·", "menu:noop")
		}

		var nextBtn telego.InlineKeyboardButton
		if page < totalPages {
			nextCb, ok := fmt.Sprintf("menu:diag:details:%d", page+1), true
			if target != "local" && target != "" {
				nextCb, ok = cbNodeCfg("menu:diag:details:", target, ":"+strconv.Itoa(page+1))
			}
			if ok {
				nextBtn = btn("След ➡️", nextCb)
			} else {
				nextBtn = btn("·", "menu:noop")
				omitted++
			}
		} else {
			nextBtn = btn("·", "menu:noop")
		}

		rows = append(rows, tu.InlineKeyboardRow(
			prevBtn,
			btn(fmt.Sprintf("%d / %d", page, totalPages), fmt.Sprintf("menu:diag:noop:%d:%d", page, totalPages)),
			nextBtn,
		))
	}

	// Trigger deep connection diagnostics on any proxy from this page
	pickDeepCb, pickDeepOK := fmt.Sprintf("menu:diag:pick_deep:%d", page), true
	if target != "local" && target != "" {
		pickDeepCb, pickDeepOK = cbNodeCfg("menu:diag:pick_deep:", target, ":"+strconv.Itoa(page))
	}
	if pickDeepOK {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("🔬 Углубленная проверка соединения", pickDeepCb),
		))
	} else {
		omitted++
	}

	// Action row: Back to Detailed Summary + Refresh current page
	actionRow := []telego.InlineKeyboardButton{}
	backSummaryCb := "menu:diag"
	if target != "local" && target != "" {
		backSummaryCb, _ = cbNodeCfg("menu:diag:node:", target)
	}
	if backSummaryCb != "" {
		actionRow = append(actionRow, btnIcon(IconBack, " К подробной сводке", backSummaryCb))
	}
	refreshPageCb := fmt.Sprintf("menu:diag:refresh:details:%d", page)
	if target != "local" && target != "" {
		refreshPageCb, _ = cbNodeCfg("menu:diag:refresh:details:", target, ":"+strconv.Itoa(page))
	}
	if refreshPageCb != "" {
		actionRow = append(actionRow, btn("🔄 Обновить", refreshPageCb))
	}
	if len(actionRow) > 0 {
		rows = append(rows, actionRow)
	}

	if omitted > 0 {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("ℹ️ длинное имя ноды — используйте /diag", "menu:noop"),
		))
	}

	// Back row
	rows = append(rows, tu.InlineKeyboardRow(
		btn(mainMenuBtnText, "menu:main"),
	))

	return tu.InlineKeyboard(rows...)
}

// IncidentsFilterMarkup returns controls for filtering the incident log by check point.
func IncidentsFilterMarkup(currentFilter string, tabs []NodeTabItem) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton

	if len(tabs) > 1 {
		var filterButtons []telego.InlineKeyboardButton
		// "All" button
		allTitle := "Все"
		if currentFilter == "" || currentFilter == "all" {
			allTitle = "• Все •"
		}
		filterButtons = append(filterButtons, btn(allTitle, "menu:stats:incidents:all"))

		for _, t := range tabs {
			title := t.Label
			if t.ID == currentFilter {
				title = "• " + title + " •"
			}
			filterButtons = append(filterButtons, btn(title, fmt.Sprintf("menu:stats:incidents:%s", t.ID)))
		}
		rows = append(rows, filterButtons)
	}

	rows = append(rows, backRow("К статистике", "menu:stats"))

	return tu.InlineKeyboard(rows...)
}
