package telegram

import (
	"fmt"
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
			btn("📊 Статус", "menu:status"),
			btn("⚡ Проверить сейчас", "menu:diag"),
		),
		tu.InlineKeyboardRow(
			btn("📈 Статистика", "menu:stats"),
			btn("📋 Подписки", "menu:subs"),
		),
		tu.InlineKeyboardRow(
			btn("⏱️ Интервал проверок", "menu:interval"),
			btn("🎯 Сайты проверки", "menu:targets"),
		),
		tu.InlineKeyboardRow(
			btn("🌙 Тихий режим", "menu:quiet"),
			btn("⚙️ Режим алертов", "menu:alert_mode"),
		),
		tu.InlineKeyboardRow(
			btn("🌐 Check-Host", "menu:checkhost"),
			btn("📑 Сводка сейчас", "menu:digest:now"),
		),
	)
}

// StatusMenuMarkup returns controls under the /status view.
func StatusMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🔄 Обновить", "menu:status"),
			btn("⚡ Экспресс-проверка", "menu:diag"),
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
			btn("🔙 Главное меню", "menu:main"),
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
			btn("🌅 До утра (08:00)", "menu:quiet:snooze:morning"),
		))
	}

	rows = append(rows, tu.InlineKeyboardRow(
		btn("🔙 Главное меню", "menu:main"),
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
			btn("🔙 Главное меню", "menu:main"),
		),
	)
}

// TargetsMenuMarkup displays target URLs controls.
func TargetsMenuMarkup() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("⚡ Проверить сейчас", "menu:diag"),
			btn("🔙 Главное меню", "menu:main"),
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
			btn("🔙 Главное меню", "menu:main"),
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

	prevPage := page - 1
	if prevPage < 1 {
		prevPage = totalPages
	}
	nextPage := page + 1
	if nextPage > totalPages {
		nextPage = 1
	}

	var rows [][]telego.InlineKeyboardButton

	// Pagination row if more than 1 page
	if totalPages > 1 {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("⬅️ Пред", fmt.Sprintf("menu:diag:p:%d", prevPage)),
			btn(fmt.Sprintf("%d / %d", page, totalPages), fmt.Sprintf("menu:diag:noop:%d:%d", page, totalPages)),
			btn("След ➡️", fmt.Sprintf("menu:diag:p:%d", nextPage)),
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


