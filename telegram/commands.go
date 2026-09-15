package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/logger"
	"xray-checker/metrics"
)

func (b *Bot) handleMessage(msg *telego.Message) {
	if !b.allowedChatIDs[msg.Chat.ID] {
		logger.Warn("Telegram: ignoring message from unauthorized chat %d (%s)", msg.Chat.ID, msg.Chat.Username)
		return
	}

	switch {
	case strings.HasPrefix(msg.Text, "/start"), strings.HasPrefix(msg.Text, "/menu"):
		b.replyMenu(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/status"):
		b.replyStatus(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/diag"), strings.HasPrefix(msg.Text, "/check_now"):
		go b.replyDiagnostics(msg.Chat.ID, commandArg(msg.Text))
	case strings.HasPrefix(msg.Text, "/stats"):
		b.replyStats(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/quiet"), strings.HasPrefix(msg.Text, "/sleep"):
		b.replyQuiet(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/targets"):
		b.replyTargets(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/tz"), strings.HasPrefix(msg.Text, "/timezone"):
		b.handleTimezoneCommand(msg)
	case strings.HasPrefix(msg.Text, "/interval"):
		b.handleIntervalCommand(msg)
	case strings.HasPrefix(msg.Text, "/checkhost_bg"):
		b.handleCheckHostBgCommand(msg)
	case strings.HasPrefix(msg.Text, "/checkhost"):
		go b.handleCheckHostCommand(msg)
	case strings.HasPrefix(msg.Text, "/settings"):
		b.replySettings(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/togglehost"):
		b.handleToggleHostCommand(msg)
	case strings.HasPrefix(msg.Text, "/togglenode"), strings.HasPrefix(msg.Text, "/disablenode"):
		b.handleToggleNodeCommand(msg)
	case strings.HasPrefix(msg.Text, "/digest"):
		b.replyDigest(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/subs"):
		b.replySubs(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/addsub"):
		go b.handleAddSub(msg)
	case strings.HasPrefix(msg.Text, "/delsub"), strings.HasPrefix(msg.Text, "/removesub"):
		go b.handleDelSub(msg)
	case strings.HasPrefix(msg.Text, "/help"):
		b.replyHelp(msg.Chat.ID)
	}
}

func (b *Bot) replyCommand(msg *telego.Message, text string) {
	if msg == nil {
		return
	}
	if b.api == nil {
		return
	}
	params := tu.Message(tu.ID(msg.Chat.ID), text).
		WithParseMode(telego.ModeHTML).
		WithReplyParameters(&telego.ReplyParameters{
			MessageID: msg.MessageID,
		})
	if _, err := b.api.SendMessage(b.ctx, params); err != nil {
		b.send(msg.Chat.ID, text)
	}
}

func (b *Bot) handleToggleNodeCommand(msg *telego.Message) {
	arg := strings.TrimSpace(commandArg(msg.Text))
	if arg == "" {
		b.replyCommand(msg, "❌ Укажите имя или ID прокси-хоста. Пример: <code>/togglenode PL-main</code>")
		return
	}
	if b.configMgr == nil {
		return
	}

	var targetStableID string
	var targetName string
	if b.source != nil {
		snapshot := b.source.MetricsSnapshot()

		// Pass 1: exact match by StableID or Name
		for _, pm := range snapshot {
			if pm.StableID == arg || strings.EqualFold(pm.Name, arg) {
				targetStableID = pm.StableID
				targetName = pm.Name
				break
			}
		}

		// Pass 2: substring match if no exact match found
		if targetStableID == "" {
			var matches []metrics.ProxyMetric
			for _, pm := range snapshot {
				if strings.Contains(strings.ToLower(pm.Name), strings.ToLower(arg)) {
					matches = append(matches, pm)
				}
			}
			if len(matches) == 1 {
				targetStableID = matches[0].StableID
				targetName = matches[0].Name
			} else if len(matches) > 1 {
				var names []string
				for i, m := range matches {
					if i >= 5 {
						names = append(names, fmt.Sprintf("...и ещё %d", len(matches)-5))
						break
					}
					names = append(names, fmt.Sprintf("«%s»", m.Name))
				}
				b.replyCommand(msg, fmt.Sprintf("⚠️ Найдено несколько прокси-хостов с фрагментом «%s»:\n%s\nУточните полное имя или ID.", escapeHTML(arg), strings.Join(names, ", ")))
				return
			}
		}
	}

	if targetStableID == "" {
		b.replyCommand(msg, fmt.Sprintf("❌ Прокси-хост «%s» не найден среди текущих подключений.", escapeHTML(arg)))
		return
	}

	disabled, err := b.configMgr.ToggleProxy(targetStableID)
	if err != nil {
		b.replyCommand(msg, fmt.Sprintf("❌ Ошибка сохранения конфигурации: %v", err))
		return
	}
	if disabled {
		b.replyCommand(msg, fmt.Sprintf("⏸️ Проверка прокси-хоста <b>%s</b> <b>отключена</b>.", escapeHTML(targetName)))
	} else {
		b.replyCommand(msg, fmt.Sprintf("🟢 Проверка прокси-хоста <b>%s</b> <b>включена</b>.", escapeHTML(targetName)))
	}
}

func (b *Bot) handleToggleHostCommand(msg *telego.Message) {
	arg := strings.TrimSpace(commandArg(msg.Text))
	if arg == "" {
		b.replyCommand(msg, "❌ Укажите хост для переключения.\nПример: <code>/togglehost example.com</code>")
		return
	}
	if b.configMgr != nil {
		disabled, err := b.configMgr.ToggleHost(arg)
		if err != nil {
			b.replyCommand(msg, fmt.Sprintf("❌ Ошибка сохранения конфигурации: %v", err))
			return
		}
		if disabled {
			b.replyCommand(msg, fmt.Sprintf("⏸️ Проверка хоста <code>%s</code> <b>отключена</b> во всех подписках.", escapeHTML(arg)))
		} else {
			b.replyCommand(msg, fmt.Sprintf("🟢 Проверка хоста <code>%s</code> <b>включена</b>.", escapeHTML(arg)))
		}
	}
}

func (b *Bot) handleIntervalCommand(msg *telego.Message) {
	arg := commandArg(msg.Text)
	if arg == "" {
		b.replyInterval(msg.Chat.ID)
		return
	}

	sec, err := strconv.Atoi(arg)
	if err != nil || sec < 10 {
		b.replyCommand(msg, "❌ Укажите корректный интервал в секундах (минимум 10 сек).\nПример: <code>/interval 60</code>")
		return
	}

	b.updateCheckInterval(sec)
	var durStr string
	if sec < 60 {
		durStr = fmt.Sprintf("%d сек.", sec)
	} else {
		durStr = FormatDowntime(time.Duration(sec) * time.Second)
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Интервал проверки прокси-хостов установлен на <b>%s</b>.", durStr))
}

func (b *Bot) handleTimezoneCommand(msg *telego.Message) {
	arg := strings.TrimSpace(commandArg(msg.Text))
	if arg == "" {
		b.replyTimezone(msg.Chat.ID)
		return
	}

	if !strings.EqualFold(arg, "local") {
		if _, err := time.LoadLocation(arg); err != nil {
			b.replyCommand(msg, fmt.Sprintf("❌ Некорректный часовой пояс <code>%s</code>.\nУкажите корректный IANA-пояс (например, <code>Europe/Moscow</code>, <code>UTC</code>, <code>Asia/Yekaterinburg</code>) или выберите из меню.", escapeHTML(arg)))
			return
		}
	}

	_ = b.updateConfig(func(c *BotConfig) {
		c.Timezone = arg
	})
	b.replyCommand(msg, fmt.Sprintf("✅ Часовой пояс успешно изменён на <b>%s</b>.\nТекущее время бота: <b>%s</b>", escapeHTML(arg), b.now().Format("15:04:05 02.01.2006")))
}

func (b *Bot) replySettings(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getSettingsText(), SettingsMenuMarkup(b.subs != nil))
}

func (b *Bot) replyInterval(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
}

func (b *Bot) replyMenu(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getMenuText(), MainMenuMarkup())
}

func (b *Bot) replyStatus(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getStatusText(), StatusMenuMarkup())
}

func (b *Bot) replyStats(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getStatsOverviewText(), StatsMenuMarkup())
}

func (b *Bot) replyQuiet(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
}

func (b *Bot) replyTargets(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getTargetsText(), TargetsMenuMarkup())
}

func (b *Bot) replyTimezone(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getTimezoneText(), TimezoneMarkup(b.GetConfig().Timezone))
}

func (b *Bot) replyDiagnostics(chatID int64, arg string) {
	sent, _ := b.sendAndReturn(chatID, "⏳ <b>Формирование детального отчёта...</b>\nПожалуйста, подождите несколько секунд...")
	reports := b.getDiagnosticsReports(true)
	msgID := 0
	if sent != nil {
		msgID = sent.GetMessageID()
	}

	if b.isRichMode() || strings.ToLower(strings.TrimSpace(arg)) == "rich" {
		rich := b.buildDiagnosticsRichMessage(reports)
		b.showRichReport(chatID, msgID, rich)
		return
	}

	pageText, totalPages := b.getDiagnosticsPageText(reports, 1)
	if msgID > 0 {
		b.editWithMarkup(chatID, msgID, pageText, DiagPaginationMarkup(1, totalPages))
	} else {
		b.sendWithMarkup(chatID, pageText, DiagPaginationMarkup(1, totalPages))
	}
}

func (b *Bot) replyDigest(chatID int64) {
	snapshot := b.source.MetricsSnapshot()
	online := 0
	totalActive := 0
	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		totalActive++
		if pm.Online {
			online++
		}
	}

	text := fmt.Sprintf("<b>📊 Сводка Xray Checker</b>\n\n"+
		"• Текущий статус: <b>%d/%d онлайн</b>\n"+
		"• Время: <b>%s</b>\n", online, totalActive, b.now().Format("15:04:05 02.01.2006"))

	if avg, ok := b.getAverageUptimePercent(); ok {
		text += fmt.Sprintf("• Средний аптайм: <b>%.1f%%</b>\n", avg)
	}

	b.sendWithMarkup(chatID, text, BackToMenuMarkup())
}

func (b *Bot) sendMorningDigest(now time.Time) {
	snapshot := b.source.MetricsSnapshot()
	online := 0
	totalActive := 0
	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		totalActive++
		if pm.Online {
			online++
		}
	}

	events := b.eventBuffer.Drain()
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>🌅 Утренняя сводка Xray Checker</b>\n\n"+
		"• Статус прокси-хостов: <b>%d/%d онлайн</b>\n"+
		"• Время: <b>%s</b>\n\n", online, totalActive, now.In(b.loc()).Format("15:04"))

	if len(events) == 0 {
		sb.WriteString("🌙 <i>За ночь инцидентов не зафиксировано, все прокси-хосты работали стабильно.</i>")
	} else {
		sb.WriteString("<b>Инциденты за ночь:</b>\n")
		for _, e := range events {
			tStr := e.Timestamp.In(b.loc()).Format("15:04")
			if e.Type == "down" {
				fmt.Fprintf(&sb, "• 🔴 %s: <b>%s</b> — сбой (%s)\n", tStr, escapeHTML(e.ProxyName), escapeHTML(e.Reason))
			} else {
				fmt.Fprintf(&sb, "• ✅ %s: <b>%s</b> — восстановлен (простой: %s)\n", tStr, escapeHTML(e.ProxyName), FormatDowntime(e.Downtime))
			}
		}
	}

	b.broadcast(sb.String())
}

func (b *Bot) sendDaytimeDigest(now time.Time) {
	if b.isRichMode() {
		reports := b.getDiagnosticsReports(false)
		rich := b.buildDiagnosticsRichMessage(reports)
		for chatID := range b.allowedChatIDs {
			b.showRichReport(chatID, 0, rich)
		}
		return
	}

	snapshot := b.source.MetricsSnapshot()
	online := 0
	totalActive := 0
	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		totalActive++
		if pm.Online {
			online++
		}
	}

	text := fmt.Sprintf("<b>📊 Дневная сводка Xray Checker</b>\n\n"+
		"• Доступность прокси-хостов: <b>%d/%d онлайн</b>\n"+
		"• Время: <b>%s</b>", online, totalActive, now.In(b.loc()).Format("15:04"))

	b.broadcast(text)
}

func (b *Bot) replyHelp(chatID int64) {
	text := "🤖 <b>Xray Checker Bot — справка</b>\n\n" +
		"/menu — главное интерактивное меню\n" +
		"/status — статус всех прокси-хостов\n" +
		"/diag — детальный отчёт о прокси-хостах\n" +
		"/settings — настройки бота и управление прокси-хостами\n" +
		"/togglenode <имя|ID> — включить/отключить проверку прокси-хоста\n" +
		"/checkhost <хост[:порт]> — глобальная проверка через Check-Host.net\n" +
		"/checkhost_bg [on|off|1h|run] — фоновая проверка Check-Host\n" +
		"/stats — статистика аптайма и инцидентов\n" +
		"/interval [сек] — интервал проверок прокси-хостов\n" +
		"/quiet — настройки тихого режима\n" +
		"/tz [пояс] — часовой пояс бота (Europe/Moscow, UTC и др.)\n" +
		"/targets — список целевых серверов проверки\n"
	if b.subs != nil {
		text += "/subs — список подписок\n" +
			"/addsub &lt;URL&gt; — добавить подписку\n" +
			"/delsub &lt;URL&gt; — удалить добавленную подписку\n"
	}
	text += "/help — эта справка\n\n" +
		"🔔 Уведомления о сбоях отправляются автоматически."
	b.sendOrUpdateMenu(chatID, text, MainMenuMarkup())
}
