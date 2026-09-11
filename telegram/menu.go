package telegram

import (
	"fmt"
	"time"

	tgbotapi "github.com/kirugan/telegram-bot-api/v5"
)

// MainMenuMarkup returns the inline keyboard for the top-level bot menu.
func MainMenuMarkup() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Статус", "menu:status"),
			tgbotapi.NewInlineKeyboardButtonData("⚡ Проверить сейчас", "menu:diag"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📈 Статистика", "menu:stats"),
			tgbotapi.NewInlineKeyboardButtonData("📋 Подписки", "menu:subs"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🎯 Сайты проверки", "menu:targets"),
			tgbotapi.NewInlineKeyboardButtonData("🌙 Тихий режим", "menu:quiet"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⚙️ Режим алертов", "menu:alert_mode"),
			tgbotapi.NewInlineKeyboardButtonData("📑 Сводка сейчас", "menu:digest:now"),
		),
	)
}

// StatusMenuMarkup returns controls under the /status view.
func StatusMenuMarkup() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 Обновить", "menu:status"),
			tgbotapi.NewInlineKeyboardButtonData("⚡ Экспресс-проверка", "menu:diag"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Главное меню", "menu:main"),
		),
	)
}

// StatsMenuMarkup returns buttons for outage stats.
func StatsMenuMarkup() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📋 Журнал инцидентов", "menu:stats:incidents"),
			tgbotapi.NewInlineKeyboardButtonData("🔝 Топ проблемных", "menu:stats:top"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Главное меню", "menu:main"),
		),
	)
}

// QuietHoursMarkup generates controls for quiet hours and snooze.
func QuietHoursMarkup(cfg BotConfig) tgbotapi.InlineKeyboardMarkup {
	toggleText := "🌙 Включить расписание"
	if cfg.QuietHoursEnabled {
		toggleText = "☀️ Отключить расписание"
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(toggleText, "menu:quiet:toggle"),
	))

	now := time.Now().Unix()
	if cfg.QuietSnoozeUntil > now {
		remaining := time.Duration(cfg.QuietSnoozeUntil-now) * time.Second
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("🔔 Снять паузу (осталось %s)", FormatDowntime(remaining)), "menu:quiet:unsnooze"),
		))
	} else {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💤 Пауза 1ч", "menu:quiet:snooze:1h"),
			tgbotapi.NewInlineKeyboardButtonData("💤 Пауза 4ч", "menu:quiet:snooze:4h"),
			tgbotapi.NewInlineKeyboardButtonData("🌅 До утра (08:00)", "menu:quiet:snooze:morning"),
		))
	}

	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("🔙 Главное меню", "menu:main"),
	))

	return tgbotapi.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// AlertModeMarkup creates the toggle menu for live vs clean alert mode.
func AlertModeMarkup(cfg BotConfig) tgbotapi.InlineKeyboardMarkup {
	liveIcon := "⚪"
	cleanIcon := "⚪"
	if cfg.AlertMode == AlertModeClean {
		cleanIcon = "🟢"
	} else {
		liveIcon = "🟢"
	}

	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%s 🔄 Live-режим", liveIcon), "menu:alert_mode:live"),
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%s 🧹 Чистый чат", cleanIcon), "menu:alert_mode:clean"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Главное меню", "menu:main"),
		),
	)
}

// TargetsMenuMarkup displays target URLs controls.
func TargetsMenuMarkup() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⚡ Проверить сейчас", "menu:diag"),
			tgbotapi.NewInlineKeyboardButtonData("🔙 Главное меню", "menu:main"),
		),
	)
}

// BackToMenuMarkup provides a simple button returning to main menu.
func BackToMenuMarkup() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Главное меню", "menu:main"),
		),
	)
}
