package telegram

import (
	"fmt"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
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
			btn("🎯 Сайты проверки", "menu:targets"),
			btn("🌙 Тихий режим", "menu:quiet"),
		),
		tu.InlineKeyboardRow(
			btn("⚙️ Режим алертов", "menu:alert_mode"),
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
