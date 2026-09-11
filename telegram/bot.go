// Package telegram wires the checker's proxy snapshot into a Telegram bot: it
// sends alerts whenever a proxy's online/offline state flips, manages alert lifecycles
// (live-editing and auto-cleanup), tracks outage statistics, enforces quiet hours with
// morning and daytime digests, and provides an interactive inline menu.
package telegram

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/checker"
	"xray-checker/logger"
	"xray-checker/metrics"
)

// maxMessageLen keeps outgoing messages under Telegram's ~4096 character limit
// with headroom for HTML entities added by escaping.
const maxMessageLen = 3500

// DiagnosticsSource provides diagnostic testing across target endpoints.
type DiagnosticsSource interface {
	RunDiagnostics(targets []string) []checker.ProxyDiagReport
	GetTargetManager() *checker.TargetManager
}

// Bot sends proxy-status notifications to Telegram and answers interactive commands
// and inline button queries from a fixed set of allowed chats.
type Bot struct {
	api            *telego.Bot
	ctx            context.Context
	cancel         context.CancelFunc
	chatIDs        []int64
	allowedChatIDs map[int64]bool
	source         metrics.MetricsSource
	subs           SubscriptionManager

	notifyOnRecovery bool
	commandsEnabled  bool

	configMgr       *ConfigManager
	statsStore      *StatsStore
	tracker         *AlertTracker
	eventBuffer     *EventBuffer
	diagSource      DiagnosticsSource
	intervalHandler func(seconds int)

	mu            sync.Mutex
	lastSeen      map[string]bool // stable_id -> last known online status
	seeded        bool            // true once the first snapshot has been recorded
	wasQuiet      bool
	lastDayDigest time.Time
	stopChan      chan struct{}
}

// New creates a Bot and verifies the token against the Telegram API.
func New(token string, chatIDs []int64, source metrics.MetricsSource, notifyOnRecovery, commandsEnabled bool, subs SubscriptionManager) (*Bot, error) {
	api, err := telego.NewBot(token, telego.WithDiscardLogger())
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	user, err := api.GetMe(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("telegram: getMe failed: %w", err)
	}

	allowed := make(map[int64]bool, len(chatIDs))
	for _, id := range chatIDs {
		allowed[id] = true
	}

	logger.Info("Telegram bot authorized as @%s", user.Username)

	return &Bot{
		api:              api,
		ctx:              ctx,
		cancel:           cancel,
		chatIDs:          chatIDs,
		allowedChatIDs:   allowed,
		source:           source,
		subs:             subs,
		notifyOnRecovery: notifyOnRecovery,
		commandsEnabled:  commandsEnabled,
		tracker:          NewAlertTracker(),
		eventBuffer:      NewEventBuffer(),
		lastSeen:         make(map[string]bool),
		stopChan:         make(chan struct{}),
	}, nil
}

// SetConfigManager attaches persistent configuration management.
func (b *Bot) SetConfigManager(cm *ConfigManager) {
	b.configMgr = cm
}

// SetStatsStore attaches outage statistics tracking.
func (b *Bot) SetStatsStore(ss *StatsStore) {
	b.statsStore = ss
}

// SetDiagnosticsSource attaches multi-target diagnostic capability.
func (b *Bot) SetDiagnosticsSource(ds DiagnosticsSource) {
	b.diagSource = ds
}

// SetIntervalHandler attaches a dynamic check interval rescheduling callback.
func (b *Bot) SetIntervalHandler(h func(seconds int)) {
	b.intervalHandler = h
}

func (b *Bot) getIntervalSec() int {
	cfg := b.GetConfig()
	if cfg.CheckIntervalSec > 0 {
		return cfg.CheckIntervalSec
	}
	return 300
}

func (b *Bot) updateCheckInterval(sec int) {
	if sec < 10 {
		sec = 10
	}
	if b.configMgr != nil {
		_ = b.configMgr.Update(func(c *BotConfig) {
			c.CheckIntervalSec = sec
		})
	}
	if b.intervalHandler != nil {
		b.intervalHandler(sec)
	}
}

// GetConfig returns the active configuration or sensible defaults.
func (b *Bot) GetConfig() BotConfig {
	if b.configMgr != nil {
		return b.configMgr.Get()
	}
	return BotConfig{
		QuietHoursEnabled:      false,
		QuietHoursStart:        "23:00",
		QuietHoursEnd:          "08:00",
		DayDigestEnabled:       false,
		DayDigestIntervalHours: 6,
		AlertMode:              AlertModeLive,
	}
}

// StartCommands begins long-polling for updates and answering commands in a background goroutine.
func (b *Bot) StartCommands() {
	if !b.commandsEnabled {
		return
	}

	b.startScheduler()

	updates, err := b.api.UpdatesViaLongPolling(b.ctx, &telego.GetUpdatesParams{
		Timeout: 60,
	})
	if err != nil {
		logger.Error("Telegram: failed to start long polling: %v", err)
		return
	}

	go func() {
		for update := range updates {
			if update.Message != nil {
				b.handleMessage(update.Message)
			} else if update.CallbackQuery != nil {
				b.handleCallbackQuery(update.CallbackQuery)
			}
		}
	}()
}

// Stop halts long-polling and scheduled routines.
func (b *Bot) Stop() {
	select {
	case <-b.stopChan:
	default:
		close(b.stopChan)
	}
	if b.cancel != nil {
		b.cancel()
	}
}

func (b *Bot) startScheduler() {
	b.wasQuiet = IsQuietTime(time.Now(), b.GetConfig())
	b.lastDayDigest = time.Now()

	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		for {
			select {
			case <-b.stopChan:
				ticker.Stop()
				return
			case now := <-ticker.C:
				b.checkSchedules(now)
			}
		}
	}()
}

func (b *Bot) checkSchedules(now time.Time) {
	cfg := b.GetConfig()
	isQuiet := IsQuietTime(now, cfg)

	// Morning transition: quiet ended -> send morning digest
	if b.wasQuiet && !isQuiet {
		b.sendMorningDigest(now)
	}
	b.wasQuiet = isQuiet

	// Daytime digest
	if cfg.DayDigestEnabled && !isQuiet {
		interval := time.Duration(cfg.DayDigestIntervalHours) * time.Hour
		if interval <= 0 {
			interval = 6 * time.Hour
		}
		if now.Sub(b.lastDayDigest) >= interval {
			b.sendDaytimeDigest(now)
			b.lastDayDigest = now
		}
	}

	// Periodically persist stats to disk
	if b.statsStore != nil && now.Minute()%5 == 0 {
		_ = b.statsStore.Save()
	}
}

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
		go b.replyDiagnostics(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/stats"):
		b.replyStats(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/quiet"), strings.HasPrefix(msg.Text, "/sleep"):
		b.replyQuiet(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/targets"):
		b.replyTargets(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/interval"):
		b.handleIntervalCommand(msg)
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

func (b *Bot) handleCallbackQuery(cb *telego.CallbackQuery) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.GetChat().ID
	if !b.allowedChatIDs[chatID] {
		return
	}

	// Acknowledge callback immediately to dismiss spinner
	_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID))

	msgID := cb.Message.GetMessageID()

	switch cb.Data {
	case "menu:main":
		b.editWithMarkup(chatID, msgID, b.getMenuText(), MainMenuMarkup())
	case "menu:status":
		b.editWithMarkup(chatID, msgID, b.getStatusText(), StatusMenuMarkup())
	case "menu:diag":
		b.editWithMarkup(chatID, msgID, "⏳ <b>Выполняется экспресс-диагностика всех прокси...</b>\nПожалуйста, подождите несколько секунд.", BackToMenuMarkup())
		go func() {
			reportText := b.getDiagnosticsText()
			b.editWithMarkup(chatID, msgID, reportText, StatusMenuMarkup())
		}()
	case "menu:stats":
		b.editWithMarkup(chatID, msgID, b.getStatsOverviewText(), StatsMenuMarkup())
	case "menu:stats:incidents":
		b.editWithMarkup(chatID, msgID, b.getIncidentsText(), BackToMenuMarkup())
	case "menu:stats:top":
		b.editWithMarkup(chatID, msgID, b.getTopProblematicText(), BackToMenuMarkup())
	case "menu:quiet":
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:toggle":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.QuietHoursEnabled = !c.QuietHoursEnabled
			})
		}
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:snooze:1h":
		if b.configMgr != nil {
			until := time.Now().Add(1 * time.Hour).Unix()
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.QuietSnoozeUntil = until
			})
		}
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:snooze:4h":
		if b.configMgr != nil {
			until := time.Now().Add(4 * time.Hour).Unix()
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.QuietSnoozeUntil = until
			})
		}
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:snooze:morning":
		if b.configMgr != nil {
			now := time.Now()
			next8 := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
			if !now.Before(next8) {
				next8 = next8.Add(24 * time.Hour)
			}
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.QuietSnoozeUntil = next8.Unix()
			})
		}
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:unsnooze":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.QuietSnoozeUntil = 0
			})
		}
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:alert_mode":
		b.editWithMarkup(chatID, msgID, b.getAlertModeText(), AlertModeMarkup(b.GetConfig()))
	case "menu:alert_mode:live":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.AlertMode = AlertModeLive
			})
		}
		b.editWithMarkup(chatID, msgID, b.getAlertModeText(), AlertModeMarkup(b.GetConfig()))
	case "menu:alert_mode:clean":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.AlertMode = AlertModeClean
			})
		}
		b.editWithMarkup(chatID, msgID, b.getAlertModeText(), AlertModeMarkup(b.GetConfig()))
	case "menu:targets":
		b.editWithMarkup(chatID, msgID, b.getTargetsText(), TargetsMenuMarkup())
	case "menu:interval":
		b.editWithMarkup(chatID, msgID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
	case "menu:subs":
		b.editWithMarkup(chatID, msgID, b.getSubsText(), BackToMenuMarkup())
	case "menu:digest:now":
		b.replyDigest(chatID)
	default:
		if strings.HasPrefix(cb.Data, "menu:interval:set:") {
			secStr := strings.TrimPrefix(cb.Data, "menu:interval:set:")
			if sec, err := strconv.Atoi(secStr); err == nil && sec >= 10 {
				b.updateCheckInterval(sec)
			}
			b.editWithMarkup(chatID, msgID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
		}
	}
}

func (b *Bot) getMenuText() string {
	cfg := b.GetConfig()
	modeName := "🔄 Live (редактирование)"
	if cfg.AlertMode == AlertModeClean {
		modeName = "🧹 Clean (автоочистка)"
	}

	quietStatus := "выключен"
	if cfg.QuietHoursEnabled {
		quietStatus = fmt.Sprintf("активен (%s–%s)", cfg.QuietHoursStart, cfg.QuietHoursEnd)
	}
	if cfg.QuietSnoozeUntil > time.Now().Unix() {
		rem := time.Duration(cfg.QuietSnoozeUntil-time.Now().Unix()) * time.Second
		quietStatus = fmt.Sprintf("пауза ещё %s", FormatDowntime(rem))
	}

	intervalSec := b.getIntervalSec()
	intervalStr := fmt.Sprintf("%d сек (%s)", intervalSec, FormatDowntime(time.Duration(intervalSec)*time.Second))

	return fmt.Sprintf("<b>📱 Главное меню Xray Checker</b>\n\n"+
		"• Интервал проверок: <b>%s</b>\n"+
		"• Режим алертов: <b>%s</b>\n"+
		"• Тихий режим: <b>%s</b>\n\n"+
		"Выберите нужный раздел с помощью кнопок ниже:", intervalStr, modeName, quietStatus)
}

func (b *Bot) handleIntervalCommand(msg *telego.Message) {
	arg := commandArg(msg.Text)
	if arg == "" {
		b.replyInterval(msg.Chat.ID)
		return
	}

	sec, err := strconv.Atoi(arg)
	if err != nil || sec < 10 {
		b.send(msg.Chat.ID, "❌ Укажите корректный интервал в секундах (минимум 10 сек).\nПример: <code>/interval 60</code>")
		return
	}

	b.updateCheckInterval(sec)
	b.sendWithMarkup(msg.Chat.ID, fmt.Sprintf("✅ Интервал проверки прокси установлен на <b>%d сек (%s)</b>.",
		sec, FormatDowntime(time.Duration(sec)*time.Second)), IntervalMenuMarkup(sec))
}

func (b *Bot) replyInterval(chatID int64) {
	b.sendWithMarkup(chatID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
}

func (b *Bot) getIntervalText() string {
	sec := b.getIntervalSec()
	return fmt.Sprintf("<b>⏱️ Интервал проверок прокси</b>\n\n"+
		"• Текущий интервал: <b>%d сек (%s)</b>\n\n"+
		"Выберите готовый пресет или отправьте команду с произвольным числом секунд:\n"+
		"<code>/interval &lt;секунды&gt;</code> (например, <code>/interval 45</code>)",
		sec, FormatDowntime(time.Duration(sec)*time.Second))
}

func (b *Bot) replyMenu(chatID int64) {
	b.sendWithMarkup(chatID, b.getMenuText(), MainMenuMarkup())
}

func (b *Bot) getStatusText() string {
	snapshot := b.source.MetricsSnapshot()
	if len(snapshot) == 0 {
		return "Нет данных о прокси — проверки ещё не выполнялись."
	}

	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Name < snapshot[j].Name })

	online := 0
	var body strings.Builder
	for _, pm := range snapshot {
		if pm.Online {
			online++
			fmt.Fprintf(&body, "✅ <b>%s</b> — %.0f ms\n", escapeHTML(pm.Name), pm.LatencyMs)
		} else {
			fmt.Fprintf(&body, "🔴 <b>%s</b> — недоступен\n", escapeHTML(pm.Name))
		}
	}

	header := fmt.Sprintf("<b>Статус прокси: %d/%d online</b>\n\n", online, len(snapshot))
	return header + body.String()
}

func (b *Bot) replyStatus(chatID int64) {
	b.sendWithMarkup(chatID, b.getStatusText(), StatusMenuMarkup())
}

func (b *Bot) getStatsOverviewText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}

	snapshot := b.source.MetricsSnapshot()
	totalProxies := len(snapshot)

	var totalUptime float64
	for _, pm := range snapshot {
		totalUptime += b.statsStore.GetUptimePercent(pm.StableID)
	}
	avgUptime := 100.0
	if totalProxies > 0 {
		avgUptime = totalUptime / float64(totalProxies)
	}

	incidents := b.statsStore.GetRecentIncidents(1)
	lastIncidentText := "Нет зарегистрированных инцидентов"
	if len(incidents) > 0 {
		inc := incidents[0]
		downTime := time.Unix(inc.DownAt, 0).Format("15:04 02.01")
		durText := "ещё не восстановился"
		if inc.UpAt > 0 {
			durText = FormatDowntime(time.Duration(inc.DurationSec) * time.Second)
		}
		lastIncidentText = fmt.Sprintf("%s: %s (длительность: %s)", escapeHTML(inc.ProxyName), downTime, durText)
	}

	return fmt.Sprintf("<b>📈 Статистика аптайма</b>\n\n"+
		"• Всего прокси в мониторинге: <b>%d</b>\n"+
		"• Средний аптайм пула: <b>%.1f%%</b>\n"+
		"• Последний сбой: <i>%s</i>\n\n"+
		"Выберите детализацию:", totalProxies, avgUptime, lastIncidentText)
}

func (b *Bot) replyStats(chatID int64) {
	b.sendWithMarkup(chatID, b.getStatsOverviewText(), StatsMenuMarkup())
}

func (b *Bot) getIncidentsText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}
	incidents := b.statsStore.GetRecentIncidents(15)
	if len(incidents) == 0 {
		return "<b>📋 Журнал инцидентов</b>\n\nЗафиксированных сбоев нет — все прокси работают стабильно!"
	}

	var sb strings.Builder
	sb.WriteString("<b>📋 Последние инциденты:</b>\n\n")
	for _, inc := range incidents {
		downTime := time.Unix(inc.DownAt, 0).Format("15:04 02.01")
		if inc.UpAt == 0 {
			fmt.Fprintf(&sb, "🔴 <b>%s</b> — упал %s (<i>сейчас оффлайн</i>)\nПричина: %s\n\n",
				escapeHTML(inc.ProxyName), downTime, escapeHTML(inc.Reason))
		} else {
			fmt.Fprintf(&sb, "🟡 <b>%s</b> — %s (оффлайн %s)\nПричина: %s\n\n",
				escapeHTML(inc.ProxyName), downTime, FormatDowntime(time.Duration(inc.DurationSec)*time.Second), escapeHTML(inc.Reason))
		}
	}
	return sb.String()
}

func (b *Bot) getTopProblematicText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}
	top := b.statsStore.GetTopProblematic(10)
	if len(top) == 0 {
		return "Нет данных для отображения."
	}

	var sb strings.Builder
	sb.WriteString("<b>🔝 Топ проблемных прокси (по падениям):</b>\n\n")
	for i, p := range top {
		fmt.Fprintf(&sb, "%d. <b>%s</b>: падений: %d, аптайм: %.1f%%, суммарный простой: %s\n",
			i+1, escapeHTML(p.ProxyName), p.DropCount, p.UptimePct, FormatDowntime(time.Duration(p.DowntimeSec)*time.Second))
	}
	return sb.String()
}

func (b *Bot) getQuietHoursText() string {
	cfg := b.GetConfig()
	status := "Отключен"
	if cfg.QuietHoursEnabled {
		status = fmt.Sprintf("Включен (%s – %s)", cfg.QuietHoursStart, cfg.QuietHoursEnd)
	}

	snoozeStatus := "Нет"
	now := time.Now().Unix()
	if cfg.QuietSnoozeUntil > now {
		rem := time.Duration(cfg.QuietSnoozeUntil-now) * time.Second
		snoozeStatus = fmt.Sprintf("Активен (осталось %s)", FormatDowntime(rem))
	}

	return fmt.Sprintf("<b>🌙 Тихий режим (ночной сон)</b>\n\n"+
		"В тихом режиме звуковые алерты об авариях не приходят в чат, а копятся для утренней сводки.\n\n"+
		"• Расписание сна: <b>%s</b>\n"+
		"• Ручная пауза: <b>%s</b>\n\n"+
		"Управляйте режимом с помощью кнопок:", status, snoozeStatus)
}

func (b *Bot) replyQuiet(chatID int64) {
	b.sendWithMarkup(chatID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
}

func (b *Bot) getAlertModeText() string {
	cfg := b.GetConfig()
	current := "🔄 Live (редактирование)"
	if cfg.AlertMode == AlertModeClean {
		current = "🧹 Чистый чат (автоочистка)"
	}

	return fmt.Sprintf("<b>⚙️ Режим уведомлений об авариях</b>\n\n"+
		"Текущий режим: <b>%s</b>\n\n"+
		"• <b>Live-режим</b>: аварийное сообщение о падении не удаляется, а при восстановлении обновляется на статус «Восстановлен» с длительностью даунтайма.\n"+
		"• <b>Чистый чат</b>: аварийное сообщение удаляется сразу при восстановлении, а подтверждение восстановления исчезает через 2 минуты, оставляя чат чистым.", current)
}

func (b *Bot) getTargetsText() string {
	targets := []string{
		"https://cp.cloudflare.com/generate_204",
		"https://www.gstatic.com/generate_204",
	}
	if b.diagSource != nil {
		if tm := b.diagSource.GetTargetManager(); tm != nil {
			configured := tm.GetTargets()
			if len(configured) > 0 {
				targets = configured
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("<b>🎯 Целевые серверы для проверки прокси:</b>\n\n")
	for i, t := range targets {
		fmt.Fprintf(&sb, "%d. <code>%s</code>\n", i+1, escapeHTML(t))
	}
	sb.WriteString("\nЧекер проверяет доступность нод по этим эндпоинтам. Если хотя бы один ответил успехом, нода считается рабочей.")
	return sb.String()
}

func (b *Bot) replyTargets(chatID int64) {
	b.sendWithMarkup(chatID, b.getTargetsText(), TargetsMenuMarkup())
}

func (b *Bot) getSubsText() string {
	if b.subs == nil {
		return "Управление подписками отключено."
	}
	static := b.subs.Static()
	dynamic := b.subs.Dynamic()
	if len(static) == 0 && len(dynamic) == 0 {
		return "Нет активных подписок."
	}

	var sb strings.Builder
	sb.WriteString("<b>📋 Список подписок:</b>\n\n")
	for _, u := range static {
		fmt.Fprintf(&sb, "🔒 <code>%s</code>\n", escapeHTML(u))
	}
	for _, u := range dynamic {
		fmt.Fprintf(&sb, "➕ <code>%s</code>\n", escapeHTML(u))
	}
	sb.WriteString("\n🔒 — из окружения / флагов, ➕ — добавлена через /addsub")
	return sb.String()
}

func (b *Bot) getDiagnosticsText() string {
	if b.diagSource == nil {
		return "Диагностика недоступна."
	}

	targets := []string{
		"https://cp.cloudflare.com/generate_204",
		"https://www.gstatic.com/generate_204",
	}
	if tm := b.diagSource.GetTargetManager(); tm != nil {
		configured := tm.GetTargets()
		if len(configured) > 0 {
			targets = configured
		}
	}

	reports := b.diagSource.RunDiagnostics(targets)
	if len(reports) == 0 {
		return "Нет прокси для проверки."
	}

	sort.Slice(reports, func(i, j int) bool { return reports[i].ProxyName < reports[j].ProxyName })

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>⚡ Результаты детальной диагностики (%d прокси):</b>\n\n", len(reports)))

	for _, rep := range reports {
		icon := "🟢"
		switch rep.Status {
		case "offline":
			icon = "🔴"
		case "degraded":
			icon = "🟡"
		}

		proto := strings.ToUpper(rep.Protocol)
		if proto == "" {
			proto = "PROXY"
		}

		fmt.Fprintf(&sb, "%s <b>%s</b> <i>(%s)</i>\n", icon, escapeHTML(rep.ProxyName), proto)

		// 1. DNS
		if rep.NodeHealth.DNSErr != "" {
			fmt.Fprintf(&sb, "  • DNS: ❌ %s\n", escapeHTML(rep.NodeHealth.DNSErr))
		} else if rep.NodeHealth.ResolvedIP != "" {
			if rep.NodeHealth.DNSLatency > 0 {
				fmt.Fprintf(&sb, "  • DNS: ✅ <code>%s</code> (%.0f ms)\n", rep.NodeHealth.ResolvedIP, float64(rep.NodeHealth.DNSLatency.Milliseconds()))
			} else {
				fmt.Fprintf(&sb, "  • DNS: ✅ <code>%s</code>\n", rep.NodeHealth.ResolvedIP)
			}
		}

		// 2. TCP Ping
		if rep.Port > 0 {
			if rep.NodeHealth.TCPErr != "" {
				fmt.Fprintf(&sb, "  • TCP (%d): ❌ %s\n", rep.Port, escapeHTML(rep.NodeHealth.TCPErr))
			} else if rep.NodeHealth.TCPPing > 0 {
				fmt.Fprintf(&sb, "  • TCP (%d): ✅ %.0f ms\n", rep.Port, float64(rep.NodeHealth.TCPPing.Milliseconds()))
			}
		}

		// 3. TLS Handshake (if attempted)
		if rep.NodeHealth.TLSErr != "" {
			fmt.Fprintf(&sb, "  • TLS: ❌ %s\n", escapeHTML(rep.NodeHealth.TLSErr))
		} else if rep.NodeHealth.TLSLatency > 0 {
			fmt.Fprintf(&sb, "  • TLS: ✅ %.0f ms\n", float64(rep.NodeHealth.TLSLatency.Milliseconds()))
		}

		// 4. Target Endpoints
		for _, tr := range rep.Targets {
			siteName := simplifyTargetName(tr.URL)
			if tr.Success {
				fmt.Fprintf(&sb, "  • %s: ✅ %.0f ms\n", siteName, float64(tr.Latency.Milliseconds()))
			} else {
				fmt.Fprintf(&sb, "  • %s: ❌ %s\n", siteName, escapeHTML(tr.Error))
			}
		}

		// 5. Verdict
		if rep.Verdict != "" {
			fmt.Fprintf(&sb, "  💡 <i>Вердикт: %s</i>\n", escapeHTML(rep.Verdict))
		}

		sb.WriteString("\n")
	}

	return sb.String()
}

func simplifyTargetName(targetURL string) string {
	switch {
	case strings.Contains(targetURL, "cloudflare"):
		return "Cloudflare 204"
	case strings.Contains(targetURL, "gstatic") || strings.Contains(targetURL, "google"):
		return "Google 204"
	case strings.Contains(targetURL, "ipify"):
		return "ipify.org"
	default:
		u, err := url.Parse(targetURL)
		if err == nil && u.Host != "" {
			return u.Host
		}
		return targetURL
	}
}

func (b *Bot) replyDiagnostics(chatID int64) {
	b.send(chatID, "⏳ <b>Выполняется экспресс-диагностика всех прокси...</b>\nПожалуйста, подождите...")
	text := b.getDiagnosticsText()
	b.sendWithMarkup(chatID, text, BackToMenuMarkup())
}

func (b *Bot) replyDigest(chatID int64) {
	snapshot := b.source.MetricsSnapshot()
	online := 0
	for _, pm := range snapshot {
		if pm.Online {
			online++
		}
	}

	text := fmt.Sprintf("<b>📊 Сводка Xray Checker</b>\n\n"+
		"• Текущий статус: <b>%d/%d online</b>\n"+
		"• Время: <b>%s</b>\n", online, len(snapshot), time.Now().Format("15:04:05 02.01.2006"))

	if b.statsStore != nil {
		text += fmt.Sprintf("• Средний аптайм: <b>%.1f%%</b>\n", b.statsStore.GetUptimePercent(""))
	}

	b.sendWithMarkup(chatID, text, BackToMenuMarkup())
}

func (b *Bot) sendMorningDigest(now time.Time) {
	snapshot := b.source.MetricsSnapshot()
	online := 0
	for _, pm := range snapshot {
		if pm.Online {
			online++
		}
	}

	events := b.eventBuffer.Drain()
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>🌅 Утренняя сводка Xray Checker</b>\n\n"+
		"• Статус прокси: <b>%d/%d online</b>\n"+
		"• Время: <b>%s</b>\n\n", online, len(snapshot), now.Format("15:04"))

	if len(events) == 0 {
		sb.WriteString("🌙 <i>За ночь аварий не зафиксировано, все серверы работали стабильно.</i>")
	} else {
		sb.WriteString("<b>События за ночь:</b>\n")
		for _, e := range events {
			tStr := e.Timestamp.Format("15:04")
			if e.Type == "down" {
				fmt.Fprintf(&sb, "• 🔴 %s: <b>%s</b> упал (%s)\n", tStr, escapeHTML(e.ProxyName), escapeHTML(e.Reason))
			} else {
				fmt.Fprintf(&sb, "• ✅ %s: <b>%s</b> восстановился (был оффлайн %s)\n", tStr, escapeHTML(e.ProxyName), FormatDowntime(e.Downtime))
			}
		}
	}

	b.broadcast(sb.String())
}

func (b *Bot) sendDaytimeDigest(now time.Time) {
	snapshot := b.source.MetricsSnapshot()
	online := 0
	for _, pm := range snapshot {
		if pm.Online {
			online++
		}
	}

	text := fmt.Sprintf("<b>📊 Дневная сводка Xray Checker</b>\n\n"+
		"• Доступность: <b>%d/%d онлайн</b>\n"+
		"• Время: <b>%s</b>", online, len(snapshot), now.Format("15:04"))

	b.broadcast(text)
}

func (b *Bot) replyHelp(chatID int64) {
	text := "<b>Xray Checker Bot</b>\n\n" +
		"/menu — главное интерактивное меню\n" +
		"/status — статус всех прокси\n" +
		"/diag — экспресс-проверка по сайтам\n" +
		"/stats — статистика аптайма и инцидентов\n" +
		"/interval [сек] — интервал проверок прокси\n" +
		"/quiet — настройки тихого режима (сна)\n" +
		"/targets — список целевых серверов проверки\n"
	if b.subs != nil {
		text += "/subs — список подписок\n" +
			"/addsub &lt;URL&gt; — добавить подписку\n" +
			"/delsub &lt;URL&gt; — удалить добавленную подписку\n"
	}
	text += "/help — эта справка\n\n" +
		"Уведомления об авариях приходят сюда автоматически."
	b.sendWithMarkup(chatID, text, MainMenuMarkup())
}

// ProcessSnapshot compares the current proxy snapshot against the last known
// state and handles transitions with smart alert lifecycle and stats tracking.
func (b *Bot) ProcessSnapshot(snapshot []metrics.ProxyMetric) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	cfg := b.GetConfig()
	isQuiet := IsQuietTime(now, cfg)

	if !b.seeded {
		for _, pm := range snapshot {
			b.lastSeen[pm.StableID] = pm.Online
			if b.statsStore != nil {
				b.statsStore.RecordCheck(pm.StableID, pm.Name, pm.Online, pm.LatencyMs)
			}
		}
		b.seeded = true
		return
	}

	seenNow := make(map[string]bool, len(snapshot))
	for _, pm := range snapshot {
		seenNow[pm.StableID] = true

		if b.statsStore != nil {
			b.statsStore.RecordCheck(pm.StableID, pm.Name, pm.Online, pm.LatencyMs)
		}

		prev, known := b.lastSeen[pm.StableID]
		b.lastSeen[pm.StableID] = pm.Online
		if !known {
			continue
		}

		switch {
		case prev && !pm.Online:
			if b.statsStore != nil {
				b.statsStore.RecordTransition(pm.StableID, pm.Name, false, "Offline", now)
			}

			if isQuiet {
				b.eventBuffer.Add(BufferedEvent{
					Timestamp: now,
					Type:      "down",
					ProxyName: pm.Name,
					Reason:    "Offline",
				})
			} else {
				outageText := fmt.Sprintf("🔴 <b>%s</b> недоступен\n%s", escapeHTML(pm.Name), escapeHTML(pm.Address))
				for _, chatID := range b.chatIDs {
					if sent, err := b.sendAndReturn(chatID, outageText); err == nil {
						b.tracker.Track(chatID, sent.MessageID, pm.StableID, pm.Name, now, "Offline")
					}
				}
			}

		case !prev && pm.Online:
			if b.statsStore != nil {
				b.statsStore.RecordTransition(pm.StableID, pm.Name, true, "", now)
			}

			if isQuiet {
				b.eventBuffer.Add(BufferedEvent{
					Timestamp: now,
					Type:      "up",
					ProxyName: pm.Name,
					LatencyMs: pm.LatencyMs,
				})
			} else {
				for _, chatID := range b.chatIDs {
					alert, hadAlert := b.tracker.Resolve(chatID, pm.StableID)
					var downtime time.Duration
					if hadAlert {
						downtime = now.Sub(alert.DownAt)
					}

					if cfg.AlertMode == AlertModeClean {
						// Delete outage alert
						if hadAlert {
							_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
								ChatID:    tu.ID(chatID),
								MessageID: alert.MessageID,
							})
						}
						if b.notifyOnRecovery {
							recoveryText := fmt.Sprintf("✅ <b>%s</b> снова в строю — %.0f ms", escapeHTML(pm.Name), pm.LatencyMs)
							if hadAlert && downtime > 0 {
								recoveryText += fmt.Sprintf(" (был оффлайн %s)", FormatDowntime(downtime))
							}
							if sent, err := b.sendAndReturn(chatID, recoveryText); err == nil {
								// Delete recovery confirmation after 2 minutes
								go func(cID int64, mID int) {
									time.Sleep(2 * time.Minute)
									_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
										ChatID:    tu.ID(cID),
										MessageID: mID,
									})
								}(chatID, sent.MessageID)
							}
						}
					} else { // AlertModeLive
						if hadAlert {
							liveText := fmt.Sprintf("✅ <b>%s</b> снова в строю — %.0f ms (был оффлайн %s)",
								escapeHTML(pm.Name), pm.LatencyMs, FormatDowntime(downtime))
							params := &telego.EditMessageTextParams{
								ChatID:    tu.ID(chatID),
								MessageID: alert.MessageID,
								Text:      liveText,
								ParseMode: telego.ModeHTML,
							}
							_, _ = b.api.EditMessageText(b.ctx, params)
						} else if b.notifyOnRecovery {
							recoveryText := fmt.Sprintf("✅ <b>%s</b> снова в строю — %.0f ms", escapeHTML(pm.Name), pm.LatencyMs)
							b.send(chatID, recoveryText)
						}
					}
				}
			}
		}
	}

	// Drop state for removed proxies
	for id := range b.lastSeen {
		if !seenNow[id] {
			delete(b.lastSeen, id)
		}
	}
}

func (b *Bot) broadcast(text string) {
	for _, chatID := range b.chatIDs {
		b.send(chatID, text)
	}
}

func (b *Bot) send(chatID int64, text string) {
	for _, chunk := range splitMessage(text, maxMessageLen) {
		params := tu.Message(tu.ID(chatID), chunk).WithParseMode(telego.ModeHTML)
		if _, err := b.api.SendMessage(b.ctx, params); err != nil {
			logger.Error("Telegram: failed to send message to %d: %v", chatID, err)
		}
	}
}

func (b *Bot) sendAndReturn(chatID int64, text string) (*telego.Message, error) {
	params := tu.Message(tu.ID(chatID), text).WithParseMode(telego.ModeHTML)
	sent, err := b.api.SendMessage(b.ctx, params)
	if err != nil {
		logger.Error("Telegram: failed to send message to %d: %v", chatID, err)
		return nil, err
	}
	return sent, nil
}

func (b *Bot) sendWithMarkup(chatID int64, text string, markup *telego.InlineKeyboardMarkup) (*telego.Message, error) {
	params := tu.Message(tu.ID(chatID), text).WithParseMode(telego.ModeHTML).WithReplyMarkup(markup)
	sent, err := b.api.SendMessage(b.ctx, params)
	if err != nil {
		logger.Error("Telegram: failed to send message to %d: %v", chatID, err)
		return nil, err
	}
	return sent, nil
}

func (b *Bot) editWithMarkup(chatID int64, messageID int, text string, markup *telego.InlineKeyboardMarkup) {
	params := &telego.EditMessageTextParams{
		ChatID:      tu.ID(chatID),
		MessageID:   messageID,
		Text:        text,
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: markup,
	}
	if _, err := b.api.EditMessageText(b.ctx, params); err != nil {
		logger.Error("Telegram: failed to edit message %d in chat %d: %v", messageID, chatID, err)
	}
}

func splitMessage(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var cur strings.Builder
	for _, line := range lines {
		if cur.Len() > 0 && cur.Len()+len(line)+1 > limit {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
