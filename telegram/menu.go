package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/metrics"
)

func btn(text, data string) telego.InlineKeyboardButton {
	return tu.InlineKeyboardButton(text).WithCallbackData(data)
}

// MainMenuMarkup returns the inline keyboard for the top-level bot menu.
func MainMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🔄 Обновить", "menu:main"),
		),
		tu.InlineKeyboardRow(
			btn("📋 Детальный отчёт", "menu:diag"),
		),
		tu.InlineKeyboardRow(
			btn("🌐 Проверка Check-Host.net", "menu:checkhost"),
		),
		tu.InlineKeyboardRow(
			btn("⚙️ Настройки", "menu:settings"),
		),
	)
}

// SettingsMenuMarkup returns buttons for the settings view.
func SettingsMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🚫 Управление прокси-хостами", "menu:disabled_proxies:1"),
		),
		tu.InlineKeyboardRow(
			btn("🌐 Фоновый Check-Host", "menu:checkhost_cfg"),
			btn("⏱️ Интервал проверок", "menu:interval"),
		),
		tu.InlineKeyboardRow(
			btn("🎯 Целевые серверы", "menu:targets"),
			btn("🌙 Тихий режим", "menu:quiet"),
		),
		tu.InlineKeyboardRow(
			btn("🧹 Режим алертов", "menu:alert_mode"),
			btn("📋 Подписки", "menu:subs"),
		),
		tu.InlineKeyboardRow(
			btn("📈 Статистика инцидентов", "menu:stats"),
		),
		tu.InlineKeyboardRow(
			btn("🔙 Главное меню", "menu:main"),
		),
	)
}

// CheckHostSettingsMarkup returns keyboard for configuring background Check-Host auditing.
func CheckHostSettingsMarkup(cfg BotConfig) *telego.InlineKeyboardMarkup {
	bgStatus := "🌐 Фоновая проверка: выкл"
	if cfg.CheckHostBgEnabled {
		bgStatus = "🌐 Фоновая проверка: вкл"
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
			btn(bgStatus, "menu:checkhost:toggle_bg"),
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
		tu.InlineKeyboardRow(
			btn("🔙 К настройкам", "menu:settings"),
			btn("🏠 Главное меню", "menu:main"),
		),
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
		statusIcon := "🟢"
		if item.Disabled {
			statusIcon = "⏸️"
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
			btn(fmt.Sprintf("%d / %d", page, totalPages), fmt.Sprintf("menu:disabled_proxies:noop:%d:%d", page, totalPages)),
			nextBtn,
		))
	}

	rows = append(rows, tu.InlineKeyboardRow(
		btn("🔙 К настройкам", "menu:settings"),
		btn("🏠 Главное меню", "menu:main"),
	))

	return tu.InlineKeyboard(rows...)
}

// DisabledHostsMarkup generates paginated toggles for host checks.
func DisabledHostsMarkup(hosts []string, disabledMap map[string]bool, page, totalPages int) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton

	for _, host := range hosts {
		statusIcon := "🟢"
		if disabledMap[strings.ToLower(host)] {
			statusIcon = "⏸️"
		}
		displayHost := host
		runes := []rune(displayHost)
		if len(runes) > 28 {
			displayHost = string(runes[:25]) + "..."
		}
		btnText := fmt.Sprintf("%s %s", statusIcon, displayHost)
		rows = append(rows, tu.InlineKeyboardRow(
			btn(btnText, fmt.Sprintf("menu:toggle_host:%s:%d", host, page)),
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
			btn(fmt.Sprintf("%d / %d", page, totalPages), fmt.Sprintf("menu:disabled_hosts:noop:%d:%d", page, totalPages)),
			nextBtn,
		))
	}

	rows = append(rows, tu.InlineKeyboardRow(
		btn("🔙 К настройкам", "menu:settings"),
		btn("🏠 Главное меню", "menu:main"),
	))

	return tu.InlineKeyboard(rows...)
}

// StatusMenuMarkup returns controls under the /status view.
func StatusMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🔄 Обновить", "menu:status"),
			btn("📋 Детальный отчёт", "menu:diag"),
		),
		tu.InlineKeyboardRow(
			btn("🔙 Главное меню", "menu:main"),
		),
	)
}

// StatsMenuMarkup returns buttons for outage stats.
func StatsMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("📋 Журнал инцидентов", "menu:stats:incidents"),
			btn("🔝 Топ проблемных", "menu:stats:top"),
		),
		tu.InlineKeyboardRow(
			btn("🔙 К настройкам", "menu:settings"),
			btn("🏠 Главное меню", "menu:main"),
		),
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
		rows = append(rows, tu.InlineKeyboardRow(
			btn("💤 Пауза 1ч", "menu:quiet:snooze:1h"),
			btn("💤 Пауза 4ч", "menu:quiet:snooze:4h"),
			btn(fmt.Sprintf("🌅 До утра (%s)", morningTime), "menu:quiet:snooze:morning"),
		))
	}

	rows = append(rows, tu.InlineKeyboardRow(
		btn("🔙 К настройкам", "menu:settings"),
		btn("🏠 Главное меню", "menu:main"),
	))

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
		tu.InlineKeyboardRow(
			btn("🔙 К настройкам", "menu:settings"),
			btn("🏠 Главное меню", "menu:main"),
		),
	)
}

// TargetsMenuMarkup displays target URLs controls.
func TargetsMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("📋 Детальный отчёт", "menu:diag"),
		),
		tu.InlineKeyboardRow(
			btn("🔙 К настройкам", "menu:settings"),
			btn("🏠 Главное меню", "menu:main"),
		),
	)
}

// BackToMenuMarkup provides a simple button returning to main menu.
func BackToMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🔙 Главное меню", "menu:main"),
		),
	)
}

// BackToSettingsMarkup provides buttons returning to settings or main menu.
func BackToSettingsMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🔙 К настройкам", "menu:settings"),
			btn("🏠 Главное меню", "menu:main"),
		),
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
		tu.InlineKeyboardRow(
			btn("🔙 К настройкам", "menu:settings"),
			btn("🏠 Главное меню", "menu:main"),
		),
	)
}

// CheckHostMenuMarkup returns keyboard for selecting a proxy to test with Check-Host.
func CheckHostMenuMarkup(proxies []metrics.ProxyMetric) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	limit := len(proxies)
	if limit > 20 {
		limit = 20
	}

	for i := 0; i < limit; i += 2 {
		if i+1 < limit {
			rows = append(rows, tu.InlineKeyboardRow(
				btn(proxies[i].Name, "menu:checkhost:run:"+proxies[i].StableID),
				btn(proxies[i+1].Name, "menu:checkhost:run:"+proxies[i+1].StableID),
			))
		} else {
			rows = append(rows, tu.InlineKeyboardRow(
				btn(proxies[i].Name, "menu:checkhost:run:"+proxies[i].StableID),
			))
		}
	}

	if len(proxies) > 20 {
		rows = append(rows, tu.InlineKeyboardRow(
			btn(fmt.Sprintf("...и ещё %d прокси-хостов (проверьте через /checkhost)", len(proxies)-20), "menu:noop"),
		))
	}

	rows = append(rows, tu.InlineKeyboardRow(
		btn("🔙 Главное меню", "menu:main"),
	))

	return tu.InlineKeyboard(rows...)
}

// DiagPaginationMarkup returns pagination controls for the diagnostics view.
func DiagPaginationMarkup(page, totalPages int) *telego.InlineKeyboardMarkup {
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
			prevBtn = btn("⬅️ Пред", fmt.Sprintf("menu:diag:p:%d", page-1))
		} else {
			prevBtn = btn("·", "menu:noop")
		}

		var nextBtn telego.InlineKeyboardButton
		if page < totalPages {
			nextBtn = btn("След ➡️", fmt.Sprintf("menu:diag:p:%d", page+1))
		} else {
			nextBtn = btn("·", "menu:noop")
		}

		rows = append(rows, tu.InlineKeyboardRow(
			prevBtn,
			btn(fmt.Sprintf("%d / %d", page, totalPages), fmt.Sprintf("menu:diag:noop:%d:%d", page, totalPages)),
			nextBtn,
		))
	}

	// Action row: Open Rich Report + Refresh current page
	rows = append(rows, tu.InlineKeyboardRow(
		btn("📊 Открыть Rich-отчёт", "menu:diag:rich"),
		btn("🔄 Обновить", fmt.Sprintf("menu:diag:refresh:%d", page)),
	))

	// Back row
	rows = append(rows, tu.InlineKeyboardRow(
		btn("🔙 Главное меню", "menu:main"),
	))

	return tu.InlineKeyboard(rows...)
}

// RichReportMarkup returns buttons under a rich diagnostics message.
func RichReportMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("📄 Постраничный вид", "menu:diag:p:1"),
			btn("🔄 Обновить", "menu:diag:refresh:rich"),
		),
		tu.InlineKeyboardRow(
			btn("🔙 Главное меню", "menu:main"),
		),
	)
}


