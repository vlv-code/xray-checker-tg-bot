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

// parseCommand extracts the command (lowercase, stripped of leading / and @bot suffix)
// and the remaining arguments from message text.
func parseCommand(text string) (cmd string, arg string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", ""
	}
	rawCmd := fields[0]
	if !strings.HasPrefix(rawCmd, "/") {
		return "", ""
	}
	rawCmd = strings.TrimPrefix(rawCmd, "/")
	if idx := strings.IndexByte(rawCmd, '@'); idx >= 0 {
		rawCmd = rawCmd[:idx]
	}
	cmd = strings.ToLower(rawCmd)
	if len(fields) > 1 {
		arg = strings.TrimSpace(text[len(fields[0]):])
	}
	return cmd, arg
}

func (b *Bot) handleMessage(msg *telego.Message) {
	if !b.allowedChatIDs[msg.Chat.ID] {
		logger.Warn("Telegram: ignoring message from unauthorized chat %d (%s)", msg.Chat.ID, msg.Chat.Username)
		return
	}

	b.waitingNodeAddMu.Lock()
	var isWaitingAdd bool
	if b.waitingNodeAdd != nil {
		isWaitingAdd = b.waitingNodeAdd[msg.Chat.ID]
		if isWaitingAdd {
			delete(b.waitingNodeAdd, msg.Chat.ID)
		}
	}
	var waitingSubNode string
	if b.waitingNodeSub != nil {
		waitingSubNode = b.waitingNodeSub[msg.Chat.ID]
		if waitingSubNode != "" {
			delete(b.waitingNodeSub, msg.Chat.ID)
		}
	}
	b.waitingNodeAddMu.Unlock()

	cmd, arg := parseCommand(msg.Text)

	mutating := map[string]bool{
		"interval": true, "addsub": true, "delsub": true, "removesub": true,
		"nodeadd": true, "nodedel": true, "nodeaddsub": true, "nodedelsub": true,
		"togglenode": true, "disablenode": true, "togglehost": true,
		"checkhost": true, "checkhost_bg": true, "targets": true, "quiet": true,
		"tz": true, "timezone": true, "digest": true,
	}

	if mutating[cmd] && msg.From != nil && !b.isAuthorizedMutating(msg.From.ID) {
		b.replyCommand(msg, "⛔ Эта команда доступна только администраторам бота.")
		logger.Warn("Telegram: user %d (%s) denied mutating command %q in chat %d",
			msg.From.ID, msg.From.Username, cmd, msg.Chat.ID)
		return
	}

	if (isWaitingAdd || waitingSubNode != "") && msg.From != nil && !b.isAuthorizedMutating(msg.From.ID) {
		b.replyCommand(msg, "⛔ Эта операция доступна только администраторам бота.")
		return
	}

	if isWaitingAdd && cmd == "" {
		b.handleNodeAdd(msg, strings.TrimSpace(msg.Text))
		return
	}
	if waitingSubNode != "" && cmd == "" {
		b.handleNodeAddSubURL(msg, waitingSubNode, strings.TrimSpace(msg.Text))
		return
	}
	if cmd == "" {
		return
	}

	t := targetFromMessage(msg)

	switch cmd {
	case "start", "menu":
		b.replyMenu(t)
	case "status":
		b.replyStatus(t)
	case "diag", "check_now":
		go b.replyDiagnostics(t, arg)
	case "stats":
		b.replyStats(t)
	case "quiet", "sleep":
		b.replyQuiet(t)
	case "targets":
		b.replyTargets(t)
	case "tz", "timezone":
		b.handleTimezoneCommand(msg)
	case "interval":
		b.handleIntervalCommand(msg)
	case "checkhost_bg":
		b.handleCheckHostBgCommand(msg)
	case "checkhost":
		go b.handleCheckHostCommand(msg)
	case "settings":
		b.replySettings(t)
	case "togglehost":
		b.handleToggleHostCommand(msg)
	case "togglenode", "disablenode":
		b.handleToggleNodeCommand(msg)
	case "digest":
		b.replyDigest(t)
	case "nodeadd":
		b.handleNodeAdd(msg, arg)
	case "nodedel":
		b.handleNodeDel(msg, arg)
	case "nodesubs":
		b.replyNodeSubs(msg)
	case "nodeaddsub":
		go b.handleNodeAddSub(msg)
	case "nodedelsub":
		go b.handleNodeDelSub(msg)
	case "nodes":
		b.replyNodes(t)
	case "subs":
		b.replySubs(t)
	case "addsub":
		go b.handleAddSub(msg)
	case "delsub", "removesub":
		go b.handleDelSub(msg)
	case "id":
		b.replyID(msg)
	case "checkupdate", "check_update", "update":
		go b.handleCheckUpdate(msg)
	case "help":
		b.replyHelp(t)
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
	if msg.MessageThreadID > 0 {
		params = params.WithMessageThreadID(msg.MessageThreadID)
	}
	if _, err := b.api.SendMessage(b.ctx, params); err != nil {
		b.send(targetFromMessage(msg), text)
	}
}

// replyID reports the identifiers of the chat the command was issued in so
// operators can fill TELEGRAM_CHAT_IDS without digging through deep links.
func (b *Bot) replyID(msg *telego.Message) {
	if msg == nil {
		return
	}

	var sb strings.Builder
	sb.WriteString("🆔 <b>Идентификаторы этого чата</b>\n\n")
	fmt.Fprintf(&sb, "• chat_id: <code>%d</code>\n", msg.Chat.ID)
	if msg.Chat.Title != "" {
		fmt.Fprintf(&sb, "• Название: %s\n", escapeHTML(msg.Chat.Title))
	}

	if msg.MessageThreadID > 0 {
		fmt.Fprintf(&sb, "• topic_id: <code>%d</code>\n", msg.MessageThreadID)
		fmt.Fprintf(&sb, "\nЧтобы бот писал в этот топик, добавьте в <code>TELEGRAM_CHAT_IDS</code>:\n<code>%d:%d</code>", msg.Chat.ID, msg.MessageThreadID)
	} else {
		fmt.Fprintf(&sb, "\nЧтобы бот писал в этот чат, добавьте в <code>TELEGRAM_CHAT_IDS</code>:\n<code>%d</code>", msg.Chat.ID)
	}

	b.send(targetFromMessage(msg), sb.String())
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
					names = append(names, fmt.Sprintf("«%s»", escapeHTML(m.Name)))
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
		b.replyInterval(targetFromMessage(msg))
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
		b.replyTimezone(targetFromMessage(msg))
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

func (b *Bot) replySettings(t ChatTarget) {
	b.sendOrUpdateMenu(t, b.getSettingsText(), SettingsMenuMarkup(b.subs != nil))
}

func (b *Bot) replyInterval(t ChatTarget) {
	b.sendOrUpdateMenu(t, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
}

func (b *Bot) replyMenu(t ChatTarget) {
	b.sendOrUpdateMenu(t, b.getMenuText(), MainMenuMarkup())
}

func (b *Bot) replyStatus(t ChatTarget) {
	b.sendOrUpdateMenu(t, b.getStatusText(), StatusMenuMarkup())
}

func (b *Bot) replyStats(t ChatTarget) {
	b.sendOrUpdateMenu(t, b.getStatsOverviewText(), StatsMenuMarkup())
}

func (b *Bot) replyQuiet(t ChatTarget) {
	b.sendOrUpdateMenu(t, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
}

func (b *Bot) replyTargets(t ChatTarget) {
	b.sendOrUpdateMenu(t, b.getTargetsText(), TargetsMenuMarkup())
}

func (b *Bot) replyTimezone(t ChatTarget) {
	b.sendOrUpdateMenu(t, b.getTimezoneText(), TimezoneMarkup(b.GetConfig().Timezone))
}

func (b *Bot) replyDiagnostics(t ChatTarget, arg string) {
	sent, _ := b.sendAndReturn(t, "⏳ <b>Формирование подробной сводки...</b>\nПожалуйста, подождите несколько секунд...")
	reports := b.getDiagnosticsReports(true)
	msgID := 0
	if sent != nil {
		msgID = sent.GetMessageID()
	}

	tabs := b.getNodeTabs("local")
	deepLinks := getDeepLinks(reports, 3)
	markup := RichReportMarkupWithTabs("local", tabs, deepLinks...)

	if b.isRichMode() || strings.ToLower(strings.TrimSpace(arg)) == "rich" {
		rich := b.buildDiagnosticsRichMessage(reports)
		b.showRichReport(t, msgID, rich, markup)
		return
	}

	summaryText := b.getDiagnosticsSummaryText(reports)
	if msgID > 0 {
		b.editWithMarkup(t.ChatID, msgID, summaryText, markup)
	} else {
		b.sendWithMarkup(t, summaryText, markup)
	}
}

func (b *Bot) replyDigest(t ChatTarget) {
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
		text += fmt.Sprintf("• Средний аптайм (24ч): <b>%.1f%%</b>\n", avg)
	}

	b.sendWithMarkup(t, text, BackToMenuMarkup())
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
		for _, t := range b.targets {
			b.showRichReport(t, 0, rich)
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

func (b *Bot) replyHelp(t ChatTarget) {
	text := "🤖 <b>Xray Checker Bot — справка</b>\n\n" +
		"/menu — главное интерактивное меню\n" +
		"/status — статус всех прокси-хостов\n" +
		"/diag — детальный отчёт о прокси-хостах\n" +
		"/settings — настройки бота и управление прокси-хостами\n" +
		"/togglenode <имя|ID> — включить/отключить проверку хоста (не ноды-инстанса)\n" +
		"/checkhost <хост[:порт]> — глобальная проверка через Check-Host.net\n" +
		"/checkhost_bg [on|off|1h|run] — фоновая проверка Check-Host\n" +
		"/stats — статистика аптайма и инцидентов\n" +
		"/interval [сек] — интервал проверок прокси-хостов\n" +
		"/quiet — настройки тихого режима\n" +
		"/tz [пояс] — часовой пояс бота (Europe/Moscow, UTC и др.)\n" +
		"/targets — список целевых серверов проверки\n" +
		"/id — ID чата и топика (для TELEGRAM_CHAT_IDS)\n"
	if b.subs != nil {
		text += "/subs — список подписок\n" +
			"/addsub &lt;URL&gt; — добавить подписку\n" +
			"/delsub &lt;URL&gt; — удалить добавленную подписку\n"
	}
	if b.nodeMgr != nil {
		text += "/nodes — ноды-инстансы чекера: статус, ASN, сводка\n" +
			"/nodeadd &lt;имя&gt; — подключить и настроить новую ноду\n" +
			"/nodedel &lt;имя&gt; — удалить ноду с мастера\n" +
			"/nodesubs &lt;имя&gt; — подписки ноды, назначенные с мастера\n" +
			"/nodeaddsub &lt;имя&gt; &lt;URL&gt; — назначить подписку ноде\n" +
			"/nodedelsub &lt;имя&gt; &lt;URL&gt; — снять подписку с ноды\n"
	}
	text += "/checkupdate — проверить наличие новой версии чекера\n" +
		"/help — эта справка\n\n" +
		"🔔 Уведомления о сбоях отправляются автоматически."
	b.sendOrUpdateMenu(t, text, MainMenuMarkup())
}

func (b *Bot) handleCheckUpdate(msg *telego.Message) {
	if msg == nil || b.api == nil {
		return
	}
	t := ChatTarget{ChatID: msg.Chat.ID}
	if msg.MessageThreadID > 0 {
		t.ThreadID = msg.MessageThreadID
	}

	rel, isNewer, err := b.CheckAndNotifyRelease(true)
	if err != nil {
		b.replyCommand(msg, fmt.Sprintf("❌ Ошибка при проверке обновлений: %s", escapeHTML(err.Error())))
		return
	}

	if isNewer {
		text := formatReleaseNotification(b.version, rel)
		markup := tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				btnURL("🔗 Открыть релиз на GitHub", rel.HTMLURL),
			),
		)
		_, _ = b.sendWithMarkup(t, text, markup)
		return
	}

	currentDisplay := b.version
	if currentDisplay == "" || currentDisplay == "unknown" {
		currentDisplay = "unknown (локальная сборка)"
	}
	text := fmt.Sprintf("✅ <b>Установлена актуальная версия!</b>\n\n"+
		"• Текущая версия: <code>%s</code>\n"+
		"• Последний релиз: <a href=\"%s\">%s</a>\n\n"+
		"Обновлений не требуется.",
		escapeHTML(currentDisplay), rel.HTMLURL, escapeHTML(rel.TagName))

	markup := tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btnURL("🔗 Все релизы на GitHub", "https://github.com/vlv-code/xray-checker-tg-bot/releases"),
		),
	)
	_, _ = b.sendWithMarkup(t, text, markup)
}
