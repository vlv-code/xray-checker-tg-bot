// Package telegram wires the checker's proxy snapshot into a Telegram bot: it
// sends alerts whenever a proxy's online/offline state flips, manages alert lifecycles
// (live-editing and auto-cleanup), tracks outage statistics, enforces quiet hours with
// morning and daytime digests, and provides an interactive inline menu.
package telegram

import (
	"context"
	"fmt"
	"net"
	"net/http"
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

// maxMessageLen keeps outgoing messages under Telegram's ~4096 character limit.
const maxMessageLen = 4000

// DiagnosticsSource provides diagnostic testing across target endpoints.
type DiagnosticsSource interface {
	RunDiagnostics(targets []string) []checker.ProxyDiagReport
	GetTargetManager() *checker.TargetManager
	GetUniqueHosts() []string
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
	richMode        bool

	mu            sync.Mutex
	lastSeen      map[string]bool // stable_id -> last known online status
	lastFlapAlert map[string]time.Time
	seeded        bool // true once the first snapshot has been recorded
	wasQuiet      bool
	lastDayDigest time.Time
	stopChan      chan struct{}

	diagMu       sync.Mutex
	cachedDiag   []checker.ProxyDiagReport
	cachedDiagAt time.Time

	checkHostMu        sync.Mutex
	checkHostRunning   bool
	lastCheckHostAudit time.Time
	checkHostClient    *checker.CheckHostClient
	checkHostNodes     []string

	lastMenuMu  sync.Mutex
	lastMenuMsg map[int64]int

	freshMu      sync.RWMutex
	subFreshness map[string]SubFreshness

	nowFunc func() time.Time
}

// New creates a Bot and verifies the token against the Telegram API.
func New(token string, chatIDs []int64, source metrics.MetricsSource, notifyOnRecovery, commandsEnabled bool, subs SubscriptionManager) (*Bot, error) {
	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: http.DefaultTransport,
	}
	api, err := telego.NewBot(token, telego.WithDiscardLogger(), telego.WithHTTPClient(httpClient))
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
		lastFlapAlert:    make(map[string]time.Time),
		lastMenuMsg:      make(map[int64]int),
		subFreshness:     make(map[string]SubFreshness),
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

// SetSubFreshness records metadata for a subscription URL.
func (b *Bot) SetSubFreshness(url string, count, prevCount, added, removed int, t time.Time) {
	b.freshMu.Lock()
	defer b.freshMu.Unlock()
	if b.subFreshness == nil {
		b.subFreshness = make(map[string]SubFreshness)
	}
	b.subFreshness[url] = SubFreshness{
		LastUpdate: t,
		Count:      count,
		PrevCount:  prevCount,
		Added:      added,
		Removed:    removed,
	}
}

// GetSubFreshness retrieves freshness info for a subscription URL.
func (b *Bot) GetSubFreshness(url string) (SubFreshness, bool) {
	b.freshMu.RLock()
	defer b.freshMu.RUnlock()
	if b.subFreshness == nil {
		return SubFreshness{}, false
	}
	sf, ok := b.subFreshness[url]
	return sf, ok
}

// SetDiagnosticsSource attaches multi-target diagnostic capability.
func (b *Bot) SetDiagnosticsSource(ds DiagnosticsSource) {
	b.diagSource = ds
}

// SetIntervalHandler attaches a dynamic check interval rescheduling callback.
func (b *Bot) SetIntervalHandler(h func(seconds int)) {
	b.intervalHandler = h
}

// SetAlertTracker attaches a custom or persistent alert tracker.
func (b *Bot) SetAlertTracker(tracker *AlertTracker) {
	if tracker != nil {
		b.tracker = tracker
	}
}

// SetRichMode sets default reporting format.
func (b *Bot) SetRichMode(enabled bool) {
	b.richMode = enabled
	if b.configMgr != nil {
		_ = b.configMgr.Update(func(cfg *BotConfig) {
			cfg.RichMode = enabled
		})
	}
}

func (b *Bot) isRichMode() bool {
	if b.configMgr != nil {
		return b.configMgr.Get().RichMode
	}
	return b.richMode
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
	return DefaultBotConfig()
}

func DefaultBotConfig() BotConfig {
	return BotConfig{
		QuietHoursEnabled:      false,
		QuietHoursStart:        "23:00",
		QuietHoursEnd:          "08:00",
		DayDigestEnabled:       false,
		DayDigestIntervalHours: 6,
		AlertMode:              AlertModeClean,
	}
}

// StartCommands begins long-polling for updates and answering commands in a background goroutine.
func (b *Bot) StartCommands() {
	if !b.commandsEnabled {
		return
	}

	b.setupBotMenuButton()
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

func (b *Bot) setupBotMenuButton() {
	commands := []telego.BotCommand{
		{Command: "menu", Description: "Главное меню и сводка"},
		{Command: "diag", Description: "Детальный отчёт"},
		{Command: "checkhost", Description: "Проверка Check-Host"},
		{Command: "settings", Description: "Настройки бота"},
		{Command: "status", Description: "Статус прокси-хостов"},
	}
	_ = b.api.SetMyCommands(b.ctx, &telego.SetMyCommandsParams{
		Commands: commands,
	})
	_ = b.api.SetChatMenuButton(b.ctx, &telego.SetChatMenuButtonParams{
		MenuButton: &telego.MenuButtonCommands{
			Type: telego.ButtonTypeCommands,
		},
	})
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
		endHour, endMin, err := parseTimeOfDay(cfg.QuietHoursEnd)
		if err != nil {
			endHour, endMin = 8, 0
		}
		nowMinutes := now.Hour()*60 + now.Minute()
		endMinutes := endHour*60 + endMin
		diff := nowMinutes - endMinutes
		if diff < 0 {
			diff = -diff
		}
		if diff <= 60 {
			b.sendMorningDigest(now)
		}
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

	// Periodic Check-Host background audit
	if cfg.CheckHostBgEnabled {
		interval := time.Duration(cfg.CheckHostIntervalHours) * time.Hour
		if interval <= 0 {
			interval = 1 * time.Hour
		}
		if b.lastCheckHostAudit.IsZero() {
			// Schedule first audit 2 minutes after startup
			b.lastCheckHostAudit = now.Add(-interval + 2*time.Minute)
		} else if now.Sub(b.lastCheckHostAudit) >= interval {
			b.lastCheckHostAudit = now
			go b.RunCheckHostAudit()
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

func (b *Bot) handleCallbackQuery(cb *telego.CallbackQuery) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.GetChat().ID
	if !b.allowedChatIDs[chatID] {
		return
	}

	msgID := cb.Message.GetMessageID()

	// Handle non-modifying callbacks with informational toasts
	if strings.HasPrefix(cb.Data, "menu:diag:noop:") {
		parts := strings.Split(strings.TrimPrefix(cb.Data, "menu:diag:noop:"), ":")
		if len(parts) == 2 {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText(fmt.Sprintf("Страница %s из %s", parts[0], parts[1])))
		} else {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID))
		}
		return
	}
	if cb.Data == "menu:noop" {
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID))
		return
	}

	// Acknowledge callback immediately to dismiss spinner, EXCEPT for callbacks that return their own custom toast.
	isToastCallback := strings.HasPrefix(cb.Data, "menu:toggle_proxy:") ||
		strings.HasPrefix(cb.Data, "menu:toggle_host:") ||
		cb.Data == "menu:checkhost:run_now"

	if !isToastCallback {
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID))
	}

	switch cb.Data {
	case "menu:main", "menu:main:refresh":
		b.editWithMarkup(chatID, msgID, b.getMenuText(), MainMenuMarkup())
	case "menu:settings":
		b.editWithMarkup(chatID, msgID, b.getSettingsText(), SettingsMenuMarkup())
	case "menu:status":
		b.editWithMarkup(chatID, msgID, b.getStatusText(), StatusMenuMarkup())
	case "menu:diag":
		b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование детального отчёта...</b>\nПожалуйста, подождите несколько секунд.", BackToMenuMarkup())
		go func() {
			reports := b.getDiagnosticsReports(true)
			if b.isRichMode() {
				rich := b.buildDiagnosticsRichMessage(reports)
				b.showRichReport(chatID, msgID, rich)
				return
			}
			pageText, totalPages := b.getDiagnosticsPageText(reports, 1)
			deepLinks := getDeepLinksForPage(reports, 1)
			b.editWithMarkup(chatID, msgID, pageText, DiagPaginationMarkup(1, totalPages, deepLinks...))
		}()
	case "menu:diag:rich":
		b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование детального отчёта...</b>\nПожалуйста, подождите несколько секунд.", BackToMenuMarkup())
		go func() {
			reports := b.getDiagnosticsReports(false)
			rich := b.buildDiagnosticsRichMessage(reports)
			b.showRichReport(chatID, msgID, rich)
		}()
	case "menu:stats":
		b.editWithMarkup(chatID, msgID, b.getStatsOverviewText(), StatsMenuMarkup())
	case "menu:stats:incidents":
		b.editWithMarkup(chatID, msgID, b.getIncidentsText(), BackToStatsMarkup())
	case "menu:stats:top":
		b.editWithMarkup(chatID, msgID, b.getTopProblematicText(), BackToStatsMarkup())
	case "menu:stats:protocols":
		b.editWithMarkup(chatID, msgID, b.getProtocolsStatsText(), BackToStatsMarkup())
	case "menu:stats:heatmap":
		b.editWithMarkup(chatID, msgID, b.getHeatmapText(), BackToStatsMarkup())
	case "menu:timezone":
		b.editWithMarkup(chatID, msgID, b.getTimezoneText(), TimezoneMarkup(b.GetConfig().Timezone))
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
			endHour, endMin, err := parseTimeOfDay(b.GetConfig().QuietHoursEnd)
			if err != nil {
				endHour, endMin = 8, 0
			}
			now := b.now()
			nextMorning := time.Date(now.Year(), now.Month(), now.Day(), endHour, endMin, 0, 0, b.loc())
			if !now.Before(nextMorning) {
				nextMorning = nextMorning.Add(24 * time.Hour)
			}
			until := nextMorning.Unix()
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.QuietSnoozeUntil = until
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
		b.editWithMarkup(chatID, msgID, b.getSubsText(), BackToSettingsMarkup())
	case "menu:digest:now":
		b.replyDigest(chatID)
	case "menu:checkhost":
		snapshot := b.source.MetricsSnapshot()
		b.editWithMarkup(chatID, msgID, b.getCheckHostMenuText(), CheckHostMenuMarkup(snapshot))
	case "menu:checkhost_cfg":
		b.editWithMarkup(chatID, msgID, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(b.GetConfig()))
	case "menu:checkhost:toggle_bg":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.CheckHostBgEnabled = !c.CheckHostBgEnabled
			})
		}
		b.editWithMarkup(chatID, msgID, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(b.GetConfig()))
	case "menu:checkhost:toggle_alert":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.CheckHostAlertEnabled = !c.CheckHostAlertEnabled
			})
		}
		b.editWithMarkup(chatID, msgID, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(b.GetConfig()))
	case "menu:checkhost:run_now":
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("🚀 Запуск фоновой проверки Check-Host..."))
		go b.RunCheckHostAudit()
	default:
		if strings.HasPrefix(cb.Data, "menu:tz:") {
			tz := strings.TrimPrefix(cb.Data, "menu:tz:")
			if b.configMgr != nil {
				_ = b.configMgr.Update(func(c *BotConfig) {
					c.Timezone = tz
				})
			}
			b.editWithMarkup(chatID, msgID, b.getTimezoneText(), TimezoneMarkup(b.GetConfig().Timezone))
		} else if strings.HasPrefix(cb.Data, "menu:checkhost:int:") {
			intStr := strings.TrimPrefix(cb.Data, "menu:checkhost:int:")
			if hours, err := strconv.Atoi(intStr); err == nil && hours > 0 {
				if b.configMgr != nil {
					_ = b.configMgr.Update(func(c *BotConfig) {
						c.CheckHostIntervalHours = hours
					})
				}
				b.editWithMarkup(chatID, msgID, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(b.GetConfig()))
			}
		} else if strings.HasPrefix(cb.Data, "menu:disabled_proxies:") {
			pageStr := strings.TrimPrefix(cb.Data, "menu:disabled_proxies:")
			page, _ := strconv.Atoi(pageStr)
			if page <= 0 {
				page = 1
			}
			text, markup := b.getDisabledProxiesView(page)
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:toggle_proxy:") {
			rest := strings.TrimPrefix(cb.Data, "menu:toggle_proxy:")
			parts := strings.Split(rest, ":")
			if len(parts) >= 2 {
				stableID := parts[0]
				page, _ := strconv.Atoi(parts[1])
				if page <= 0 {
					page = 1
				}
				if b.configMgr != nil {
					disabled, _ := b.configMgr.ToggleProxy(stableID)
					proxyName := b.getProxyNameByStableID(stableID)
					toast := fmt.Sprintf("🟢 Прокси-хост %s включен", proxyName)
					if disabled {
						toast = fmt.Sprintf("⏸️ Прокси-хост %s выключен", proxyName)
					}
					if b.api != nil {
						_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText(toast))
					}
				}
				text, markup := b.getDisabledProxiesView(page)
				b.editWithMarkup(chatID, msgID, text, markup)
			}
		} else if strings.HasPrefix(cb.Data, "menu:disabled_hosts:") {
			pageStr := strings.TrimPrefix(cb.Data, "menu:disabled_hosts:")
			page, _ := strconv.Atoi(pageStr)
			if page <= 0 {
				page = 1
			}
			text, markup := b.getDisabledHostsView(page)
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:toggle_host:") {
			rest := strings.TrimPrefix(cb.Data, "menu:toggle_host:")
			parts := strings.Split(rest, ":")
			if len(parts) >= 2 {
				host := parts[0]
				page, _ := strconv.Atoi(parts[1])
				if page <= 0 {
					page = 1
				}
				if b.configMgr != nil {
					disabled, _ := b.configMgr.ToggleHost(host)
					toast := fmt.Sprintf("🟢 Хост %s включён", host)
					if disabled {
						toast = fmt.Sprintf("⏸️ Хост %s выключен", host)
					}
					_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText(toast))
				}
				text, markup := b.getDisabledHostsView(page)
				b.editWithMarkup(chatID, msgID, text, markup)
			}
		} else if strings.HasPrefix(cb.Data, "menu:diag:p:") {
			pageStr := strings.TrimPrefix(cb.Data, "menu:diag:p:")
			page, _ := strconv.Atoi(pageStr)
			if page <= 0 {
				page = 1
			}
			reports := b.getDiagnosticsReports(false)
			pageText, totalPages := b.getDiagnosticsPageText(reports, page)
			deepLinks := getDeepLinksForPage(reports, page)
			b.editWithMarkup(chatID, msgID, pageText, DiagPaginationMarkup(page, totalPages, deepLinks...))
		} else if strings.HasPrefix(cb.Data, "menu:diag:deep:") {
			stableID := strings.TrimPrefix(cb.Data, "menu:diag:deep:")
			b.handleDeepDiagnostics(chatID, msgID, stableID)
		} else if strings.HasPrefix(cb.Data, "menu:diag:refresh:") {
			arg := strings.TrimPrefix(cb.Data, "menu:diag:refresh:")
			if arg == "rich" {
				b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование детального отчёта...</b>", BackToMenuMarkup())
				go func() {
					reports := b.getDiagnosticsReports(true)
					rich := b.buildDiagnosticsRichMessage(reports)
					b.showRichReport(chatID, msgID, rich)
				}()
			} else {
				page, _ := strconv.Atoi(arg)
				if page <= 0 {
					page = 1
				}
				b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование детального отчёта...</b>", BackToMenuMarkup())
				go func() {
					reports := b.getDiagnosticsReports(true)
					pageText, totalPages := b.getDiagnosticsPageText(reports, page)
					deepLinks := getDeepLinksForPage(reports, page)
					b.editWithMarkup(chatID, msgID, pageText, DiagPaginationMarkup(page, totalPages, deepLinks...))
				}()
			}
		} else if strings.HasPrefix(cb.Data, "menu:checkhost:run:") {
			stableID := strings.TrimPrefix(cb.Data, "menu:checkhost:run:")
			go b.handleCheckHostProxy(chatID, msgID, stableID)
		} else if strings.HasPrefix(cb.Data, "menu:interval:set:") {
			secStr := strings.TrimPrefix(cb.Data, "menu:interval:set:")
			if sec, err := strconv.Atoi(secStr); err == nil && sec >= 10 {
				b.updateCheckInterval(sec)
			}
			b.editWithMarkup(chatID, msgID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
		}
	}
}

func (b *Bot) getMenuText() string {
	var snapshot []metrics.ProxyMetric
	if b.source != nil {
		snapshot = b.source.MetricsSnapshot()
	}
	cfg := b.GetConfig()
	online := 0
	totalActive := 0
	disabledCount := 0
	var downProxies []string

	for _, pm := range snapshot {
		host, _, err := net.SplitHostPort(pm.Address)
		if err != nil {
			host = pm.Address
		}
		if pm.Disabled || cfg.IsDisabled(host, pm.StableID) {
			disabledCount++
			continue
		}
		totalActive++
		if pm.Online {
			online++
		} else {
			downProxies = append(downProxies, pm.Name)
		}
	}

	var statusLine string
	if disabledCount > 0 {
		statusLine = fmt.Sprintf("• Текущий статус: <b>%d/%d онлайн</b> <i>(⏸️ %d отключено)</i>\n", online, totalActive, disabledCount)
	} else {
		statusLine = fmt.Sprintf("• Текущий статус: <b>%d/%d онлайн</b>\n", online, len(snapshot))
	}

	nowStr := b.now().Format("15:04:05 02.01.2006")
	var uptimeStr string
	if avg, ok := b.getAverageUptimePercent(); ok {
		uptimeStr = fmt.Sprintf("• Средний аптайм: <b>%.1f%%</b>\n", avg)
	}

	var sb strings.Builder
	sb.WriteString("<b>📊 Сводка Xray Checker</b>\n\n")
	sb.WriteString(statusLine)
	sb.WriteString(fmt.Sprintf("• Время: <b>%s</b>\n", nowStr))
	if uptimeStr != "" {
		sb.WriteString(uptimeStr)
	}

	if len(downProxies) > 0 {
		sb.WriteString("\n<b>🔴 Требуют внимания:</b>\n")
		limit := 5
		if len(downProxies) < limit {
			limit = len(downProxies)
		}
		for i := 0; i < limit; i++ {
			sb.WriteString(fmt.Sprintf("• %s\n", escapeHTML(downProxies[i])))
		}
		if len(downProxies) > limit {
			sb.WriteString(fmt.Sprintf("<i>...и ещё %d недоступно</i>\n", len(downProxies)-limit))
		}
	} else if totalActive > 0 {
		sb.WriteString("\n🟢 <i>Все активные прокси-хосты доступны и работают стабильно.</i>\n")
	}

	return sb.String()
}

func (b *Bot) getSettingsText() string {
	cfg := b.GetConfig()
	modeName := "🔄 Live (редактирование)"
	if cfg.AlertMode == AlertModeClean {
		modeName = "🧹 Чистый чат (автоочистка)"
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
	var intervalStr string
	if intervalSec < 60 {
		intervalStr = fmt.Sprintf("%d сек.", intervalSec)
	} else {
		intervalStr = FormatDowntime(time.Duration(intervalSec) * time.Second)
	}

	disabledCount := len(cfg.DisabledHosts) + len(cfg.DisabledProxies)

	chBgStatus := "выключен"
	if cfg.CheckHostBgEnabled {
		chBgStatus = fmt.Sprintf("каждые %d ч.", cfg.CheckHostIntervalHours)
		if cfg.CheckHostAlertEnabled {
			chBgStatus += " (алерты по РФ: вкл)"
		} else {
			chBgStatus += " (алерты по РФ: выкл)"
		}
	}

	return fmt.Sprintf("<b>⚙️ Настройки Xray Checker</b>\n\n"+
		"• Интервал проверок: <b>%s</b>\n"+
		"• Фоновый Check-Host: <b>%s</b>\n"+
		"• Режим алертов: <b>%s</b>\n"+
		"• Тихий режим: <b>%s</b>\n"+
		"• Отключено (хосты/прокси): <b>%d</b>\n\n"+
		"Выберите раздел настроек с помощью кнопок ниже:",
		intervalStr, chBgStatus, modeName, quietStatus, disabledCount)
}

func (b *Bot) getProxyNameByStableID(stableID string) string {
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.StableID == stableID {
				if pm.Name != "" {
					return pm.Name
				}
				return pm.Address
			}
		}
	}
	return stableID
}

func (b *Bot) getDisabledProxiesView(page int) (string, *telego.InlineKeyboardMarkup) {
	var snapshot []metrics.ProxyMetric
	if b.source != nil {
		snapshot = b.source.MetricsSnapshot()
	}
	cfg := b.GetConfig()

	var items []ProxyToggleItem
	disabledCount := 0
	for _, pm := range snapshot {
		host, _, err := net.SplitHostPort(pm.Address)
		if err != nil {
			host = pm.Address
		}
		isDisabled := pm.Disabled || cfg.IsDisabled(host, pm.StableID)
		if isDisabled {
			disabledCount++
		}
		items = append(items, ProxyToggleItem{
			StableID: pm.StableID,
			Name:     pm.Name,
			Protocol: pm.Protocol,
			Address:  pm.Address,
			Disabled: isDisabled,
		})
	}

	pageSize := 6
	totalItems := len(items)
	totalPages := (totalItems + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if end > totalItems {
		end = totalItems
	}

	var pageItems []ProxyToggleItem
	if start < totalItems {
		pageItems = items[start:end]
	}

	var sb strings.Builder
	sb.WriteString("<b>🚫 Управление прокси-хостами</b>\n\n")
	sb.WriteString("Нажмите на прокси-хост, чтобы включить или отключить его проверку.\n")
	sb.WriteString("Отключённые прокси-хосты не проверяются, не вызывают алертов и сразу исключаются из сводки.\n\n")
	sb.WriteString(fmt.Sprintf("Всего прокси-хостов: <b>%d</b> | Отключено: <b>%d</b>\n", totalItems, disabledCount))

	markup := DisabledProxiesMarkup(pageItems, page, totalPages)
	return sb.String(), markup
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

func (b *Bot) getDisabledHostsView(page int) (string, *telego.InlineKeyboardMarkup) {
	var allHosts []string
	if b.diagSource != nil {
		allHosts = b.diagSource.GetUniqueHosts()
	}
	cfg := b.GetConfig()
	disabledMap := make(map[string]bool)
	for _, h := range cfg.DisabledHosts {
		disabledMap[strings.ToLower(h)] = true
	}

	pageSize := 6
	totalHosts := len(allHosts)
	totalPages := (totalHosts + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if end > totalHosts {
		end = totalHosts
	}

	var pageHosts []string
	if start < totalHosts {
		pageHosts = allHosts[start:end]
	}

	var sb strings.Builder
	sb.WriteString("<b>🚫 Управление проверками хостов</b>\n\n")
	sb.WriteString("Нажмите на хост, чтобы включить или отключить его проверку во всех подписках.\n")
	sb.WriteString("Отключённые хосты не пингуются, не вызывают алертов и не влияют на статус.\n\n")
	sb.WriteString(fmt.Sprintf("Всего обнаружено хостов: <b>%d</b> | Отключено: <b>%d</b>\n", totalHosts, len(cfg.DisabledHosts)))

	markup := DisabledHostsMarkup(pageHosts, disabledMap, page, totalPages)
	return sb.String(), markup
}

func (b *Bot) replySettings(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getSettingsText(), SettingsMenuMarkup())
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

func (b *Bot) replyInterval(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
}

func (b *Bot) getIntervalText() string {
	sec := b.getIntervalSec()
	var durStr string
	if sec < 60 {
		durStr = fmt.Sprintf("%d сек.", sec)
	} else {
		durStr = FormatDowntime(time.Duration(sec) * time.Second)
	}
	return fmt.Sprintf("<b>⏱️ Интервал проверок прокси-хостов</b>\n\n"+
		"• Текущий интервал: <b>%s</b>\n\n"+
		"Выберите готовый пресет или отправьте команду с произвольным числом секунд:\n"+
		"<code>/interval &lt;секунды&gt;</code> (например, <code>/interval 45</code>)",
		durStr)
}

func (b *Bot) replyMenu(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getMenuText(), MainMenuMarkup())
}

func (b *Bot) getAverageUptimePercent() (float64, bool) {
	if b.statsStore == nil || b.source == nil {
		return 100.0, false
	}
	snapshot := b.source.MetricsSnapshot()
	var totalUptime float64
	var count int
	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		totalUptime += b.statsStore.GetUptimePercent(pm.StableID)
		count++
	}
	if count == 0 {
		return 100.0, false
	}
	return totalUptime / float64(count), true
}

func (b *Bot) getStatusText() string {
	if b.source == nil {
		return "Нет данных о прокси-хостах — проверки ещё не выполнялись."
	}
	snapshot := b.source.MetricsSnapshot()
	if len(snapshot) == 0 {
		return "Нет данных о прокси-хостах — проверки ещё не выполнялись."
	}

	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Name < snapshot[j].Name })

	online := 0
	activeTotal := 0
	disabledCount := 0
	var activeBody strings.Builder
	var disabledBody strings.Builder

	for _, pm := range snapshot {
		if pm.Disabled {
			disabledCount++
			fmt.Fprintf(&disabledBody, "⏸️ <b>%s</b> — отключён\n", escapeHTML(pm.Name))
			continue
		}
		activeTotal++
		if pm.Online {
			online++
			fmt.Fprintf(&activeBody, "✅ <b>%s</b> — %.0f ms\n", escapeHTML(pm.Name), pm.LatencyMs)
		} else {
			fmt.Fprintf(&activeBody, "🔴 <b>%s</b> — недоступен\n", escapeHTML(pm.Name))
		}
	}

	header := fmt.Sprintf("<b>Статус прокси-хостов: %d/%d онлайн</b>\n\n", online, activeTotal)
	if disabledCount > 0 {
		return header + activeBody.String() + fmt.Sprintf("\n<b>Отключённые прокси-хосты (%d):</b>\n", disabledCount) + disabledBody.String()
	}
	return header + activeBody.String()
}

func (b *Bot) replyStatus(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getStatusText(), StatusMenuMarkup())
}

func (b *Bot) getStatsOverviewText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}

	var snapshot []metrics.ProxyMetric
	if b.source != nil {
		snapshot = b.source.MetricsSnapshot()
	}
	totalProxies := len(snapshot)
	now := b.now()

	activeIDs := make([]string, 0, totalProxies)
	for _, pm := range snapshot {
		if !pm.Disabled {
			activeIDs = append(activeIDs, pm.StableID)
		}
	}
	var uptime24h float64 = 100.0
	var avg7d float64 = 100.0
	if b.statsStore != nil && b.statsStore.GetRollingStats() != nil {
		uptime24h = b.statsStore.GetRollingStats().UptimePercent24hAll(now, activeIDs)
		var total7d float64
		for _, id := range activeIDs {
			total7d += b.statsStore.GetRollingStats().UptimePercent7d(id, now)
		}
		if len(activeIDs) > 0 {
			avg7d = total7d / float64(len(activeIDs))
		}
	}

	var sb strings.Builder
	sb.WriteString("<b>📈 Статистика аптайма</b>\n\n")
	fmt.Fprintf(&sb, "• Прокси-хостов в мониторинге: <b>%d</b>\n", totalProxies)
	fmt.Fprintf(&sb, "• Средний аптайм: <b>24ч: %.1f%%</b> (7д: %.1f%%)\n", uptime24h, avg7d)

	incidents := b.statsStore.GetRecentIncidents(1)
	if len(incidents) > 0 {
		inc := incidents[0]
		downTime := time.Unix(inc.DownAt, 0).In(b.loc()).Format("15:04 02.01")
		durText := "ещё не восстановлен"
		if inc.UpAt > 0 {
			durText = FormatDowntime(time.Duration(inc.DurationSec) * time.Second)
		}
		fmt.Fprintf(&sb, "• Последний инцидент: <i>%s: %s (простой: %s)</i>\n", escapeHTML(inc.ProxyName), downTime, durText)
	}

	// Top unstable (flapping / drops in 24h)
	type proxyFlap struct {
		name      string
		flaps     int
		uptime24h float64
	}
	var flapsList []proxyFlap
	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		flaps := b.statsStore.GetFlapCount24h(pm.StableID, now)
		if flaps > 0 {
			u := 100.0
			if b.statsStore.GetRollingStats() != nil {
				u = b.statsStore.GetRollingStats().UptimePercent24h(pm.StableID, now)
			}
			flapsList = append(flapsList, proxyFlap{
				name:      pm.Name,
				flaps:     flaps,
				uptime24h: u,
			})
		}
	}
	sort.Slice(flapsList, func(i, j int) bool {
		return flapsList[i].flaps > flapsList[j].flaps
	})

	if len(flapsList) > 0 {
		sb.WriteString("\n🔝 <b>Топ нестабильных (переходов за 24ч):</b>\n")
		showCount := 3
		if len(flapsList) < showCount {
			showCount = len(flapsList)
		}
		for i := 0; i < showCount; i++ {
			f := flapsList[i]
			fmt.Fprintf(&sb, "  %d. %s — %d переходов, %.1f%% аптайм\n", i+1, escapeHTML(f.name), f.flaps, f.uptime24h)
		}
	}

	// Monospace table for proxies (up to 15)
	if totalProxies > 0 {
		sb.WriteString("\n<pre>")
		sb.WriteString(fmt.Sprintf("%-11s %5s %5s %5s %5s %4s %4s\n", "ПРОКСИ", "24Ч", "7Д", "P50", "P95", "Σ", "ПАД."))

		sortedSnap := make([]metrics.ProxyMetric, len(snapshot))
		copy(sortedSnap, snapshot)
		sort.Slice(sortedSnap, func(i, j int) bool {
			flapsI := b.statsStore.GetFlapCount24h(sortedSnap[i].StableID, now)
			flapsJ := b.statsStore.GetFlapCount24h(sortedSnap[j].StableID, now)
			if flapsI != flapsJ {
				return flapsI > flapsJ
			}
			return sortedSnap[i].Name < sortedSnap[j].Name
		})

		maxRows := 15
		if len(sortedSnap) < maxRows {
			maxRows = len(sortedSnap)
		}
		for i := 0; i < maxRows; i++ {
			pm := sortedSnap[i]
			pName := pm.Name
			if len(pName) > 11 {
				pName = pName[:10] + "…"
			}
			var u24, u7d float64 = 100.0, 100.0
			if b.statsStore.GetRollingStats() != nil {
				u24 = b.statsStore.GetRollingStats().UptimePercent24h(pm.StableID, now)
				u7d = b.statsStore.GetRollingStats().UptimePercent7d(pm.StableID, now)
			}
			ls := b.statsStore.GetLatencySamples(pm.StableID)
			p50 := ls.Percentile(0.50)
			p95 := ls.Percentile(0.95)
			jitter := ls.StdDev()
			flaps := b.statsStore.GetFlapCount24h(pm.StableID, now)

			p50Str := fmt.Sprintf("%.0fms", p50)
			p95Str := fmt.Sprintf("%.0fms", p95)
			if ls.Count() < 5 {
				p50Str = "-"
				p95Str = "-"
			}
			flapWarning := ""
			if flaps > 10 {
				flapWarning = "⚠️"
			}
			sb.WriteString(fmt.Sprintf("%-11s %4.1f%% %4.1f%% %5s %5s %4.0f %3d%s\n",
				pName, u24, u7d, p50Str, p95Str, jitter, flaps, flapWarning))
		}
		if len(sortedSnap) > maxRows {
			sb.WriteString(fmt.Sprintf("... и ещё %d прокси-хостов\n", len(sortedSnap)-maxRows))
		}
		sb.WriteString("</pre>")
	}

	sb.WriteString("\nВыберите раздел ниже:")
	return sb.String()
}

func (b *Bot) getProtocolsStatsText() string {
	if b.source == nil {
		return "📈 <b>Статистика по протоколам</b>\n\nНет данных о прокси-хостах."
	}
	snapshot := b.source.MetricsSnapshot()
	if len(snapshot) == 0 {
		return "📈 <b>Статистика по протоколам</b>\n\nНет данных о прокси-хостах."
	}

	type groupData struct {
		name    string
		proxies []metrics.ProxyMetric
	}

	groupsMap := make(map[string]*groupData)
	var groupOrder []string

	for _, pm := range snapshot {
		key := formatProtocolTransport(pm.Protocol, pm.Transport, pm.Security)
		gd, ok := groupsMap[key]
		if !ok {
			gd = &groupData{name: key}
			groupsMap[key] = gd
			groupOrder = append(groupOrder, key)
		}
		gd.proxies = append(gd.proxies, pm)
	}

	sort.Slice(groupOrder, func(i, j int) bool {
		cntI := len(groupsMap[groupOrder[i]].proxies)
		cntJ := len(groupsMap[groupOrder[j]].proxies)
		if cntI != cntJ {
			return cntI > cntJ
		}
		return groupOrder[i] < groupOrder[j]
	})

	now := b.now()
	var sb strings.Builder
	sb.WriteString("📈 <b>Статистика по протоколам</b>\n\n")

	for _, key := range groupOrder {
		gd := groupsMap[key]
		n := len(gd.proxies)

		if n < 3 {
			fmt.Fprintf(&sb, "<b>%s</b> (n=%d)\n  ⚠️ Слишком мало данных (n<3) для надёжной статистики\n\n", escapeHTML(gd.name), n)
			continue
		}

		var totalUptime float64
		var samples []float64

		for _, p := range gd.proxies {
			if b.statsStore != nil && b.statsStore.GetRollingStats() != nil {
				totalUptime += b.statsStore.GetRollingStats().UptimePercent24h(p.StableID, now)
				ls := b.statsStore.GetLatencySamples(p.StableID)
				if ls.Count() > 0 {
					ls.mu.Lock()
					samples = append(samples, ls.samples[:ls.count]...)
					ls.mu.Unlock()
				} else if p.LatencyMs > 0 {
					samples = append(samples, p.LatencyMs)
				}
			} else {
				if p.Online {
					totalUptime += 100.0
				}
				if p.LatencyMs > 0 {
					samples = append(samples, p.LatencyMs)
				}
			}
		}

		avgUptime := totalUptime / float64(n)
		var p50, p95 float64
		if len(samples) > 0 {
			sort.Float64s(samples)
			p50 = samples[int(float64(len(samples)-1)*0.5)]
			p95 = samples[int(float64(len(samples)-1)*0.95)]
		}

		fmt.Fprintf(&sb, "<b>%s</b> (n=%d)\n  Аптайм 24ч: %.1f%%   Латенси p50: %.0f мс, p95: %.0f мс\n\n",
			escapeHTML(gd.name), n, avgUptime, p50, p95)
	}

	return strings.TrimRight(sb.String(), "\n")
}

func formatProtocolTransport(protocol, transport, security string) string {
	protoName := strings.ToUpper(protocol)
	switch strings.ToLower(protocol) {
	case "vless":
		protoName = "VLESS"
	case "vmess":
		protoName = "VMess"
	case "trojan":
		protoName = "Trojan"
	case "shadowsocks":
		protoName = "Shadowsocks"
	case "hysteria2":
		protoName = "Hysteria2"
	case "wireguard":
		protoName = "WireGuard"
	}

	var transportName string
	secLower := strings.ToLower(security)
	transLower := strings.ToLower(transport)

	if secLower == "reality" {
		transportName = "Reality"
	} else if transLower != "" {
		switch transLower {
		case "ws":
			transportName = "WebSocket"
		case "tcp":
			if secLower == "tls" {
				transportName = "TLS"
			} else {
				transportName = "TCP"
			}
		case "grpc":
			transportName = "gRPC"
		case "httpupgrade":
			transportName = "HTTPUpgrade"
		case "splithttp", "xhttp":
			transportName = "xHTTP"
		default:
			transportName = strings.ToUpper(transport)
		}
	} else if secLower != "" && secLower != "none" {
		transportName = strings.ToUpper(security)
	}

	if transportName != "" {
		return protoName + " / " + transportName
	}
	return protoName
}

func (b *Bot) getHeatmapText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}
	now := b.now()
	loc := b.loc()
	matrix, peakHour, maxDrops := b.statsStore.GetHeatmap7d(now, loc)

	var sb strings.Builder
	sb.WriteString("🌡️ <b>Карта падений за 7 дней</b>\n\n<pre>")
	sb.WriteString("       Пн  Вт  Ср  Чт  Пт  Сб  Вс\n")
	for h := 0; h < 24; h++ {
		fmt.Fprintf(&sb, "%02dч  ", h)
		for d := 0; d < 7; d++ {
			fmt.Fprintf(&sb, "%4d", matrix[h][d])
		}
		if h == peakHour && maxDrops > 0 {
			sb.WriteString("   ← пик")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("</pre>")

	if maxDrops > 0 {
		fmt.Fprintf(&sb, "\nПик падений: %02dч (%d сбоев, вероятно перегрузка канала)\n", peakHour, maxDrops)
	} else {
		sb.WriteString("\nЗа последние 7 дней падений не зафиксировано.\n")
	}
	return sb.String()
}

func (b *Bot) replyStats(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getStatsOverviewText(), StatsMenuMarkup())
}

func (b *Bot) getIncidentsText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}
	incidents := b.statsStore.GetRecentIncidents(15)
	if len(incidents) == 0 {
		return "<b>📋 Журнал инцидентов</b>\n\nЗафиксированных инцидентов нет — все прокси-хосты работают стабильно!"
	}

	var sb strings.Builder
	sb.WriteString("<b>📋 Последние инциденты:</b>\n\n")
	for _, inc := range incidents {
		downTime := time.Unix(inc.DownAt, 0).In(b.loc()).Format("15:04 02.01")
		if inc.UpAt == 0 {
			fmt.Fprintf(&sb, "🔴 <b>%s</b> — сбой %s (<i>сейчас недоступен</i>)\nПричина: %s\n\n",
				escapeHTML(inc.ProxyName), downTime, escapeHTML(inc.Reason))
		} else {
			fmt.Fprintf(&sb, "🟡 <b>%s</b> — %s (простой: %s)\nПричина: %s\n\n",
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
	sb.WriteString("<b>🔝 Топ по инцидентам (прокси-хосты):</b>\n\n")
	now := b.now()
	for i, p := range top {
		incStats := b.statsStore.GetIncidentStats(p.StableID, 24*time.Hour, now)
		mtbfStr := ""
		if incStats.MTBF > 0 {
			mtbfStr = fmt.Sprintf(", MTBF: %s", FormatDowntime(incStats.MTBF))
		}
		mttrStr := ""
		if incStats.MTTR > 0 {
			mttrStr = fmt.Sprintf(", MTTR: %s", FormatDowntime(incStats.MTTR))
		}
		fmt.Fprintf(&sb, "%d. <b>%s</b>: инцидентов: %d, аптайм: %.1f%%%s%s, суммарный простой: %s\n",
			i+1, escapeHTML(p.ProxyName), p.DropCount, p.UptimePct, mtbfStr, mttrStr, FormatDowntime(time.Duration(p.DowntimeSec)*time.Second))
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

	tzName := cfg.Timezone
	if tzName == "" {
		tzName = "Local"
	}

	return fmt.Sprintf("<b>🌙 Тихий режим</b>\n\n"+
		"В тихом режиме звуковые алерты о сбоях не приходят в чат, а копятся для утренней сводки.\n\n"+
		"• Расписание сна: <b>%s</b>\n"+
		"• Ручная пауза: <b>%s</b>\n"+
		"• Часовой пояс: <b>%s</b>\n\n"+
		"Управляйте режимом с помощью кнопок:", status, snoozeStatus, escapeHTML(tzName))
}

func (b *Bot) replyQuiet(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
}

func (b *Bot) getAlertModeText() string {
	cfg := b.GetConfig()
	current := "🔄 Live (редактирование)"
	if cfg.AlertMode == AlertModeClean {
		current = "🧹 Чистый чат (автоочистка)"
	}

	return fmt.Sprintf("<b>⚙️ Режим уведомлений о сбоях</b>\n\n"+
		"Текущий режим: <b>%s</b>\n\n"+
		"• <b>Live-режим</b>: сообщение о сбое не удаляется, а при восстановлении обновляется на статус «Восстановлен» с указанием времени простоя.\n"+
		"• <b>Чистый чат</b>: сообщение о сбое удаляется сразу при восстановлении, а подтверждение восстановления исчезает через 2 минуты, оставляя чат чистым.", current)
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
	sb.WriteString("<b>🎯 Целевые серверы проверки прокси-хостов:</b>\n\n")
	for i, t := range targets {
		fmt.Fprintf(&sb, "%d. <code>%s</code>\n", i+1, escapeHTML(t))
	}
	sb.WriteString("\nЧекер проверяет доступность прокси-хостов по целевым серверам. Если хотя бы один ответил успехом, прокси-хост считается доступным.")
	return sb.String()
}

func (b *Bot) replyTargets(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getTargetsText(), TargetsMenuMarkup())
}

func (b *Bot) loc() *time.Location {
	return b.GetConfig().Location()
}

func (b *Bot) now() time.Time {
	if b.nowFunc != nil {
		return b.nowFunc().In(b.loc())
	}
	return time.Now().In(b.loc())
}

func (b *Bot) getTimezoneText() string {
	cfg := b.GetConfig()
	loc := b.loc()
	now := time.Now().In(loc)
	tzName := cfg.Timezone
	if tzName == "" {
		tzName = "Local"
	}

	zoneName, offset := now.Zone()
	offsetHours := offset / 3600
	offsetSign := "+"
	if offsetHours < 0 {
		offsetSign = "-"
		offsetHours = -offsetHours
	}

	return fmt.Sprintf(
		"🕒 <b>Настройка часового пояса</b>\n\n"+
			"• Текущий пояс: <b>%s</b> (%s, UTC%s%d)\n"+
			"• Локальное время бота: <b>%s</b>\n\n"+
			"Часовой пояс применяется для:\n"+
			"• Расписания тихого режима\n"+
			"• Дневных и утренних сводок\n"+
			"• Времени фиксации инцидентов в журнале\n"+
			"• Всех уведомлений бота\n\n"+
			"Выберите часовой пояс или задайте командой <code>/tz &lt;IANA&gt;</code>:",
		escapeHTML(tzName),
		escapeHTML(zoneName),
		offsetSign,
		offsetHours,
		now.Format("15:04:05 02.01.2006"),
	)
}

func (b *Bot) replyTimezone(chatID int64) {
	b.sendOrUpdateMenu(chatID, b.getTimezoneText(), TimezoneMarkup(b.GetConfig().Timezone))
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

	if b.configMgr != nil {
		_ = b.configMgr.Update(func(c *BotConfig) {
			c.Timezone = arg
		})
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Часовой пояс успешно изменён на <b>%s</b>.\nТекущее время бота: <b>%s</b>", escapeHTML(arg), b.now().Format("15:04:05 02.01.2006")))
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

	renderSub := func(icon, u string) {
		fmt.Fprintf(&sb, "%s <code>%s</code>\n", icon, escapeHTML(u))
		if sf, ok := b.GetSubFreshness(u); ok && !sf.LastUpdate.IsZero() {
			ago := formatTimeAgo(b.now().Sub(sf.LastUpdate))
			if sf.Added == 0 && sf.Removed == 0 {
				fmt.Fprintf(&sb, "   <i>%s · %d прокси (без изменений)</i>\n", ago, sf.Count)
			} else {
				fmt.Fprintf(&sb, "   <i>%s · %d прокси (было %d, +%d, -%d)</i>\n", ago, sf.Count, sf.PrevCount, sf.Added, sf.Removed)
			}
		}
	}

	for _, u := range static {
		renderSub("🔒", u)
	}
	for _, u := range dynamic {
		renderSub("➕", u)
	}
	sb.WriteString("\n🔒 — из окружения / флагов, ➕ — добавлена через /addsub")
	return sb.String()
}

func formatTimeAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "только что"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dм назад", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dч назад", int(d.Hours()))
	}
	return fmt.Sprintf("%dд назад", int(d.Hours()/24))
}

const diagPageSize = 5

func (b *Bot) getDiagnosticsReports(force bool) []checker.ProxyDiagReport {
	if b.diagSource == nil {
		return nil
	}

	b.diagMu.Lock()
	if !force && len(b.cachedDiag) > 0 && time.Since(b.cachedDiagAt) < 60*time.Second {
		reports := make([]checker.ProxyDiagReport, len(b.cachedDiag))
		copy(reports, b.cachedDiag)
		b.diagMu.Unlock()
		return reports
	}
	b.diagMu.Unlock()

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
	b.sortDiagnosticsReports(reports)

	b.diagMu.Lock()
	b.cachedDiag = make([]checker.ProxyDiagReport, len(reports))
	copy(b.cachedDiag, reports)
	b.cachedDiagAt = time.Now()
	b.diagMu.Unlock()

	return reports
}

func (b *Bot) sortDiagnosticsReports(reports []checker.ProxyDiagReport) {
	now := b.now()
	getSeverityRank := func(rep *checker.ProxyDiagReport) int {
		if rep.Disabled || rep.Status == "disabled" {
			return 4
		}
		if rep.Status == "offline" {
			return 1
		}
		if rep.Status == "degraded" {
			return 2
		}
		return 3 // online
	}

	sort.SliceStable(reports, func(i, j int) bool {
		ri, rj := &reports[i], &reports[j]
		rankI, rankJ := getSeverityRank(ri), getSeverityRank(rj)
		if rankI != rankJ {
			return rankI < rankJ
		}

		if rankI == 1 { // offline: longer downtime first
			var dtI, dtJ time.Duration
			if b.tracker != nil {
				dtI = b.tracker.GetDowntime(ri.StableID, now)
				dtJ = b.tracker.GetDowntime(rj.StableID, now)
			}
			if dtI != dtJ {
				return dtI > dtJ
			}
			return ri.ProxyName < rj.ProxyName
		}

		if rankI == 3 { // online: lower latency first
			var latI, latJ time.Duration
			for _, tr := range ri.Targets {
				if tr.Success {
					latI = tr.Latency
					break
				}
			}
			for _, tr := range rj.Targets {
				if tr.Success {
					latJ = tr.Latency
					break
				}
			}
			if latI != latJ {
				return latI < latJ
			}
			return ri.ProxyName < rj.ProxyName
		}

		return ri.ProxyName < rj.ProxyName
	})
}

func formatSingleProxyDiag(sb *strings.Builder, rep checker.ProxyDiagReport) {
	var b *Bot
	b.formatSingleProxyDiagWithStats(sb, rep)
}

func (b *Bot) formatSingleProxyDiagWithStats(sb *strings.Builder, rep checker.ProxyDiagReport) {
	proto := strings.ToUpper(rep.Protocol)
	if proto == "" {
		proto = "PROXY"
	}

	if rep.Disabled || rep.Status == "disabled" {
		fmt.Fprintf(sb, "⏸️ <b>%s</b> <i>(%s)</i> — <i>проверка отключена</i>\n\n", escapeHTML(rep.ProxyName), proto)
		return
	}

	icon := "🟢"
	switch rep.Status {
	case "offline":
		icon = "🔴"
	case "degraded":
		icon = "🟡"
	}

	flappingSuffix := ""
	if b != nil && b.statsStore != nil && b.statsStore.GetFlapCount24h(rep.StableID, b.now()) > 10 {
		flappingSuffix = " ⚠️ флап"
	}

	fmt.Fprintf(sb, "%s <b>%s</b> <i>(%s)</i>%s\n", icon, escapeHTML(rep.ProxyName), proto, flappingSuffix)

	// Status line with soft hint or latency percentiles
	if rep.Status == "offline" {
		var cat checker.ErrorCategory
		if rep.NodeHealth.DNSErr != "" {
			cat = checker.CatDNSError
		} else if rep.NodeHealth.TLSErr != "" {
			cat = checker.CatTLSError
		} else if checker.IsUDPProto(rep.Protocol) && rep.NodeHealth.UDPErr != "" {
			if strings.Contains(strings.ToLower(rep.NodeHealth.UDPErr), "refused") {
				cat = checker.CatConnRefused
			} else {
				cat = checker.CatTimeout
			}
		} else if rep.NodeHealth.TCPErr != "" {
			if strings.Contains(strings.ToLower(rep.NodeHealth.TCPErr), "refused") {
				cat = checker.CatConnRefused
			} else {
				cat = checker.CatTimeout
			}
		} else {
			for _, tr := range rep.Targets {
				if !tr.Success {
					cat = checker.ClassifyError(fmt.Errorf("%s", tr.Error), tr.StatusCode)
					break
				}
			}
			if cat == checker.CatNone {
				cat = checker.CatTimeout
			}
		}
		hint := checker.FormatSoftHint(cat, checker.IsUDPProto(rep.Protocol))
		fmt.Fprintf(sb, "  • Статус: 🔴 недоступен (вероятно: %s)\n", escapeHTML(hint))
	} else if rep.Status == "degraded" {
		fmt.Fprintf(sb, "  • Статус: 🟡 туннель работает, но цель недоступна\n")
	} else {
		var latMs float64
		for _, tr := range rep.Targets {
			if tr.Success {
				latMs = float64(tr.Latency.Milliseconds())
				break
			}
		}
		if b != nil && b.statsStore != nil {
			ls := b.statsStore.GetLatencySamples(rep.StableID)
			if ls.Count() >= 5 {
				p95 := ls.Percentile(0.95)
				p99 := ls.Percentile(0.99)
				jitter := ls.StdDev()
				fmt.Fprintf(sb, "  • Доступен: 🟢 %.0f мс (p95: %.0f мс, p99: %.0f мс, σ=%.0f)\n", latMs, p95, p99, jitter)
			} else {
				fmt.Fprintf(sb, "  • Доступен: 🟢 %.0f мс\n", latMs)
			}
		} else {
			fmt.Fprintf(sb, "  • Доступен: 🟢 %.0f мс\n", latMs)
		}
	}

	// 1. DNS
	if rep.NodeHealth.DNSErr != "" {
		fmt.Fprintf(sb, "  • DNS: ❌ %s\n", escapeHTML(rep.NodeHealth.DNSErr))
	} else if rep.NodeHealth.ResolvedIP != "" {
		if rep.NodeHealth.DNSLatency > 0 {
			fmt.Fprintf(sb, "  • DNS: ✅ <code>%s</code> (%.0f ms)\n", rep.NodeHealth.ResolvedIP, float64(rep.NodeHealth.DNSLatency.Milliseconds()))
		} else {
			fmt.Fprintf(sb, "  • DNS: ✅ <code>%s</code>\n", rep.NodeHealth.ResolvedIP)
		}
	}

	// 2. Transport Protocol / TCP Ping
	if checker.IsUDPProto(rep.Protocol) {
		if rep.Port > 0 {
			fmt.Fprintf(sb, "  • Порт (%d): ⚡ UDP / QUIC\n", rep.Port)
		}
	} else if rep.Port > 0 {
		if rep.NodeHealth.TCPErr != "" {
			fmt.Fprintf(sb, "  • TCP (%d): ❌ %s\n", rep.Port, escapeHTML(rep.NodeHealth.TCPErr))
		} else if rep.NodeHealth.TCPPing > 0 {
			fmt.Fprintf(sb, "  • TCP (%d): ✅ %.0f ms\n", rep.Port, float64(rep.NodeHealth.TCPPing.Milliseconds()))
		}
	}

	// 3. TLS Handshake (if attempted)
	if rep.NodeHealth.TLSErr != "" {
		fmt.Fprintf(sb, "  • TLS: ❌ %s\n", escapeHTML(rep.NodeHealth.TLSErr))
	} else if rep.NodeHealth.TLSLatency > 0 {
		fmt.Fprintf(sb, "  • TLS: ✅ %.0f ms\n", float64(rep.NodeHealth.TLSLatency.Milliseconds()))
	}

	// 4. Target Endpoints
	for _, tr := range rep.Targets {
		siteName := simplifyTargetName(tr.URL)
		if tr.Success {
			fmt.Fprintf(sb, "  • %s: ✅ %.0f ms\n", siteName, float64(tr.Latency.Milliseconds()))
		} else {
			fmt.Fprintf(sb, "  • %s: ❌ %s\n", siteName, escapeHTML(tr.Error))
		}
	}

	// 5. External Check-Host
	if rep.CheckHost != nil {
		ruStatus := "❌ недоступен"
		if rep.CheckHost.RUAvailable {
			ruStatus = "✅ отвечает"
		}
		worldStatus := "❌ недоступен"
		if rep.CheckHost.WorldAvailable {
			worldStatus = "✅ отвечает"
		}
		fmt.Fprintf(sb, "  • Check-Host: РФ %s, Мир %s\n", ruStatus, worldStatus)
		if rep.CheckHost.PermanentLink != "" {
			fmt.Fprintf(sb, "    🔗 <a href=\"%s\">отчёт Check-Host</a>\n", rep.CheckHost.PermanentLink)
		}
	}

	// 6. Verdict
	if rep.Verdict != "" {
		fmt.Fprintf(sb, "  💡 <i>Вердикт: %s</i>\n", escapeHTML(rep.Verdict))
	}

	sb.WriteString("\n")
}

func (b *Bot) getDiagnosticsText() string {
	reports := b.getDiagnosticsReports(true)
	if len(reports) == 0 {
		return "Нет прокси для проверки."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>⚡ Результаты детальной диагностики (%d прокси):</b>\n\n", len(reports)))
	for _, rep := range reports {
		b.formatSingleProxyDiagWithStats(&sb, rep)
	}
	return sb.String()
}

func getDeepLinksForPage(reports []checker.ProxyDiagReport, page int) []DiagDeepLink {
	if len(reports) == 0 {
		return nil
	}
	totalPages := (len(reports) + diagPageSize - 1) / diagPageSize
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * diagPageSize
	end := start + diagPageSize
	if end > len(reports) {
		end = len(reports)
	}

	var links []DiagDeepLink
	for _, rep := range reports[start:end] {
		if rep.Status == "offline" || rep.Status == "degraded" {
			links = append(links, DiagDeepLink{
				Name:     rep.ProxyName,
				StableID: rep.StableID,
			})
		}
	}
	return links
}

func (b *Bot) getDiagnosticsPageText(reports []checker.ProxyDiagReport, page int) (string, int) {
	if len(reports) == 0 {
		return "Нет доступных прокси для проверки.", 1
	}

	totalPages := (len(reports) + diagPageSize - 1) / diagPageSize
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * diagPageSize
	end := start + diagPageSize
	if end > len(reports) {
		end = len(reports)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 <b>Детальный отчёт</b> (Стр. %d из %d, всего %d прокси):\n\n", page, totalPages, len(reports)))

	for _, rep := range reports[start:end] {
		b.formatSingleProxyDiagWithStats(&sb, rep)
	}

	return sb.String(), totalPages
}

func (b *Bot) formatDeepDiagnostics(pm metrics.ProxyMetric, health checker.NodeHealth, ch *checker.CheckHostSummary) string {
	var sb strings.Builder

	proto := strings.ToUpper(pm.Protocol)
	if proto == "" {
		proto = "PROXY"
	}

	transport := "TCP"
	if checker.IsUDPProto(pm.Protocol) {
		transport = "UDP / QUIC"
	} else if strings.Contains(strings.ToLower(pm.Name), "reality") {
		transport = "Reality / TCP"
	}

	fmt.Fprintf(&sb, "🔬 <b>Углублённая проверка — %s</b>\n\n", escapeHTML(pm.Name))
	fmt.Fprintf(&sb, "<b>ПРОТОКОЛ:</b>  %s\n", proto)
	fmt.Fprintf(&sb, "<b>ТРАНСПОРТ:</b> %s\n", transport)
	fmt.Fprintf(&sb, "<b>АДРЕС:</b>     %s\n\n", escapeHTML(pm.Address))

	sb.WriteString("<b>ЭТАПЫ ПРОВЕРКИ:</b>\n")

	// 1. DNS-резолв
	if health.DNSErr != "" {
		fmt.Fprintf(&sb, "  1. DNS-резолв ........... —      ❌  %s\n", escapeHTML(health.DNSErr))
	} else if health.ResolvedIP != "" {
		if health.DNSLatency > 0 {
			fmt.Fprintf(&sb, "  1. DNS-резолв ........... %.0f мс  ✅\n", float64(health.DNSLatency.Milliseconds()))
		} else {
			fmt.Fprintf(&sb, "  1. DNS-резолв ........... ✅ (%s)\n", health.ResolvedIP)
		}
	} else {
		sb.WriteString("  1. DNS-резолв ........... —      (не требуется)\n")
	}

	// 2. TCP / UDP рукопожатие
	if checker.IsUDPProto(pm.Protocol) {
		if health.UDPErr != "" {
			fmt.Fprintf(&sb, "  2. UDP-датаграмма ...... —      ❌  %s\n", escapeHTML(health.UDPErr))
		} else if health.UDPPing > 0 {
			fmt.Fprintf(&sb, "  2. UDP-датаграмма ...... %.0f мс  ✅\n", float64(health.UDPPing.Milliseconds()))
		} else {
			sb.WriteString("  2. UDP-датаграмма ...... —\n")
		}
	} else {
		if health.TCPErr != "" {
			fmt.Fprintf(&sb, "  2. TCP-рукопожатие ...... —      ❌  %s\n", escapeHTML(health.TCPErr))
		} else if health.TCPPing > 0 {
			fmt.Fprintf(&sb, "  2. TCP-рукопожатие ...... %.0f мс  ✅  (к %s)\n", float64(health.TCPPing.Milliseconds()), escapeHTML(pm.Address))
		} else {
			sb.WriteString("  2. TCP-рукопожатие ...... —\n")
		}
	}

	// 3. TLS / Reality handshake
	if health.TLSErr != "" {
		fmt.Fprintf(&sb, "  3. TLS/Reality handshake  —      ❌  %s\n", escapeHTML(health.TLSErr))
	} else if pm.TLSHandshakeMs > 0 {
		fmt.Fprintf(&sb, "  3. TLS/Reality handshake  %d мс  ✅\n", pm.TLSHandshakeMs)
	} else if health.TLSLatency > 0 {
		fmt.Fprintf(&sb, "  3. TLS/Reality handshake  %.0f мс  ✅\n", float64(health.TLSLatency.Milliseconds()))
	} else if !pm.Online && !pm.CanConnect {
		sb.WriteString("  3. TLS/Reality handshake  —      ❌  (не завершено)\n")
	} else {
		sb.WriteString("  3. TLS/Reality handshake  —      ✅\n")
	}

	// 4. TTFB через туннель
	if pm.TTFBMs > 0 {
		fmt.Fprintf(&sb, "  4. TTFB через туннель .... %d мс\n", pm.TTFBMs)
	} else if pm.LatencyMs > 0 {
		fmt.Fprintf(&sb, "  4. TTFB через туннель .... %.0f мс\n", pm.LatencyMs)
	} else {
		sb.WriteString("  4. TTFB через туннель .... —      (не достигнуто)\n")
	}

	// 5. HTTP-ответ
	if pm.CanTransfer || pm.Online {
		sb.WriteString("  5. HTTP-ответ ........... 200 OK\n")
	} else {
		sb.WriteString("  5. HTTP-ответ ........... —      (не достигнуто)\n")
	}

	// 6. Полный ответ
	if pm.LatencyMs > 0 && (pm.CanTransfer || pm.Online) {
		fmt.Fprintf(&sb, "  6. Полный ответ .......... %.0f мс\n\n", pm.LatencyMs)
	} else {
		sb.WriteString("  6. Полный ответ .......... —\n\n")
	}

	// Summary
	if pm.Online || (pm.CanConnect && pm.CanTransfer) {
		sb.WriteString("<b>ИТОГ:</b> ✅ Доступен\n")
	} else {
		sb.WriteString("<b>ИТОГ:</b> ❌ Недоступен\n")
		if pm.LastErrorMsg != "" {
			fmt.Fprintf(&sb, "<b>ОШИБКА:</b>     %s\n", escapeHTML(pm.LastErrorMsg))
		}
		if pm.LastErrorCategory > 0 {
			cat := checker.ErrorCategory(pm.LastErrorCategory)
			fmt.Fprintf(&sb, "<b>ТИП ОШИБКИ:</b> %s\n", cat.String())
		}
	}

	// Latency percentiles & Reliability metrics
	if b != nil && b.statsStore != nil {
		ls := b.statsStore.GetLatencySamples(pm.StableID)
		if ls != nil && ls.Count() >= 5 {
			fmt.Fprintf(&sb, "\n<b>СТАТИСТИКА ЗАДЕРЖКИ:</b>\n")
			fmt.Fprintf(&sb, "  • p50: %.0f мс | p95: %.0f мс | p99: %.0f мс (σ=%.0f, n=%d)\n",
				ls.Percentile(0.50), ls.Percentile(0.95), ls.Percentile(0.99), ls.StdDev(), ls.Count())
		}

		incStats := b.statsStore.GetIncidentStats(pm.StableID, 24*time.Hour, b.now())
		flaps := b.statsStore.GetFlapCount24h(pm.StableID, b.now())
		if incStats.Incidents > 0 || flaps > 0 {
			sb.WriteString("\n<b>НАДЁЖНОСТЬ (24ч):</b>\n")
			if incStats.Incidents > 0 {
				fmt.Fprintf(&sb, "  • Инцидентов: %d\n", incStats.Incidents)
				if incStats.MTBF > 0 {
					fmt.Fprintf(&sb, "  • MTBF (наработка на отказ): %s\n", FormatDowntime(incStats.MTBF))
				}
				if incStats.MTTR > 0 {
					fmt.Fprintf(&sb, "  • MTTR (время восстановления): %s\n", FormatDowntime(incStats.MTTR))
				}
			}
			if flaps > 0 {
				fmt.Fprintf(&sb, "  • Флаппинг: %d переключений за 24ч\n", flaps)
			}
		}
	}

	// HOST-CHECK
	if ch != nil {
		sb.WriteString("\n<b>HOST-CHECK (Check-Host.net):</b>\n")
		ruTotal, ruSuccess, ruRTT := ch.RUStats()
		worldTotal, worldSuccess, worldRTT := ch.WorldStats()

		if ruTotal > 0 {
			if ruSuccess > 0 && ruRTT > 0 {
				fmt.Fprintf(&sb, "  РФ (%d узла):       %d/%d отвечают, средний RTT %.0f мс\n", ruTotal, ruSuccess, ruTotal, float64(ruRTT.Milliseconds()))
			} else {
				fmt.Fprintf(&sb, "  РФ (%d узла):       %d/%d отвечают\n", ruTotal, ruSuccess, ruTotal)
			}
		}
		if worldTotal > 0 {
			if worldSuccess > 0 && worldRTT > 0 {
				fmt.Fprintf(&sb, "  Мир (%d узла):      %d/%d отвечают, средний RTT %.0f мс\n", worldTotal, worldSuccess, worldTotal, float64(worldRTT.Milliseconds()))
			} else {
				fmt.Fprintf(&sb, "  Мир (%d узла):      %d/%d отвечают\n", worldTotal, worldSuccess, worldTotal)
			}
		}

		if ch.Verdict != "" {
			fmt.Fprintf(&sb, "  Вывод: %s\n", escapeHTML(ch.Verdict))
		}
		if ch.PermanentLink != "" {
			fmt.Fprintf(&sb, "  Ссылка: 🔗 <a href=\"%s\">отчёт Check-Host</a>\n", ch.PermanentLink)
		}
	}

	return sb.String()
}

func (b *Bot) handleDeepDiagnostics(chatID int64, msgID int, stableID string) {
	b.editWithMarkup(chatID, msgID, "⏳ <b>Выполняется углублённая проверка...</b>", BackToMenuMarkup())

	go func() {
		var targetMetric metrics.ProxyMetric
		found := false
		if b.source != nil {
			for _, pm := range b.source.MetricsSnapshot() {
				if pm.StableID == stableID {
					targetMetric = pm
					found = true
					break
				}
			}
		}

		// Also find report from cached or run diagnostics
		reports := b.getDiagnosticsReports(false)
		var rep *checker.ProxyDiagReport
		for i := range reports {
			if reports[i].StableID == stableID {
				rep = &reports[i]
				break
			}
		}

		if !found && rep != nil {
			targetMetric = metrics.ProxyMetric{
				Name:      rep.ProxyName,
				Protocol:  rep.Protocol,
				Address:   fmt.Sprintf("%s:%d", rep.Server, rep.Port),
				StableID:  rep.StableID,
				Online:    rep.Status == "online",
				LatencyMs: float64(rep.NodeHealth.TCPPing.Milliseconds()),
			}
			found = true
		}

		if !found {
			b.editWithMarkup(chatID, msgID, "⚠️ Прокси не найден.", BackToMenuMarkup())
			return
		}

		var health checker.NodeHealth
		var ch *checker.CheckHostSummary
		if rep != nil {
			health = rep.NodeHealth
			ch = rep.CheckHost
		}

		// If health is empty, do a direct probe
		if health.ResolvedIP == "" && health.TCPPing == 0 && health.UDPPing == 0 {
			host, portStr, _ := net.SplitHostPort(targetMetric.Address)
			port, _ := strconv.Atoi(portStr)
			health = checker.ProbeNodeHealth(host, port, targetMetric.Protocol, "", "", false)
		}

		text := b.formatDeepDiagnostics(targetMetric, health, ch)
		b.editWithMarkup(chatID, msgID, text, DeepDiagnosticsMarkup(stableID))
	}()
}

func (b *Bot) buildDiagnosticsRichMessage(reports []checker.ProxyDiagReport) *telego.InputRichMessage {
	if len(reports) == 0 {
		msg := tu.RichMessage(tu.RichBlockParagraph(tu.RichTextPlain("Нет доступных прокси-хостов для проверки.")))
		return &msg
	}

	var blocks []telego.InputRichBlock

	// 1. Heading
	blocks = append(blocks, tu.RichBlockSectionHeading(
		tu.RichTextBold(tu.RichTextPlain(fmt.Sprintf("📋 Результаты детального отчёта (%d прокси-хостов)", len(reports)))),
		2,
	))

	// 2. Table: Прокси-хост | Протокол | Пинг | Статус
	headerRow := []telego.RichBlockTableCell{
		tu.RichBlockTableCell(tu.RichTextBold(tu.RichTextPlain("Прокси-хост"))).WithIsHeader(),
		tu.RichBlockTableCell(tu.RichTextBold(tu.RichTextPlain("Прот."))).WithIsHeader(),
		tu.RichBlockTableCell(tu.RichTextBold(tu.RichTextPlain("Пинг"))).WithIsHeader(),
		tu.RichBlockTableCell(tu.RichTextBold(tu.RichTextPlain("Статус"))).WithIsHeader(),
	}

	var tableRows [][]telego.RichBlockTableCell
	tableRows = append(tableRows, headerRow)

	for _, rep := range reports {
		latencyText := "—"
		for _, tr := range rep.Targets {
			if tr.Success {
				latencyText = fmt.Sprintf("%.0f ms", float64(tr.Latency.Milliseconds()))
				break
			}
		}

		statusText := "🟢 Доступен"
		switch rep.Status {
		case "offline":
			statusText = "🔴 Недоступен"
		case "degraded":
			statusText = "🟡 Ошибка"
		case "disabled":
			statusText = "⏸️ Отключён"
			latencyText = "—"
		}

		proto := strings.ToUpper(rep.Protocol)
		if proto == "" {
			proto = "PROXY"
		}

		tableRows = append(tableRows, []telego.RichBlockTableCell{
			tu.RichBlockTableCell(tu.RichTextPlain(rep.ProxyName)),
			tu.RichBlockTableCell(tu.RichTextPlain(proto)),
			tu.RichBlockTableCell(tu.RichTextPlain(latencyText)),
			tu.RichBlockTableCell(tu.RichTextPlain(statusText)),
		})
	}

	table := tu.RichBlockTable(tableRows...).WithIsBordered().WithIsStriped().WithIsCompact()
	blocks = append(blocks, table)
	blocks = append(blocks, tu.RichBlockDivider())

	// 3. Collapsible details for degraded/offline proxies
	hasProblems := false
	for _, rep := range reports {
		if rep.Status == "online" || rep.Status == "disabled" || rep.Disabled {
			continue
		}
		hasProblems = true

		summary := tu.RichTextBold(tu.RichTextPlain(fmt.Sprintf("🔴 %s — детали сбоя (%s)", rep.ProxyName, strings.ToUpper(rep.Protocol))))

		var detailLines []string
		if rep.NodeHealth.DNSErr != "" {
			detailLines = append(detailLines, fmt.Sprintf("DNS: ❌ %s", rep.NodeHealth.DNSErr))
		} else if rep.NodeHealth.ResolvedIP != "" {
			detailLines = append(detailLines, fmt.Sprintf("DNS: ✅ %s", rep.NodeHealth.ResolvedIP))
		}
		if checker.IsUDPProto(rep.Protocol) {
			if rep.Port > 0 {
				detailLines = append(detailLines, fmt.Sprintf("Порт (%d): ⚡ UDP / QUIC", rep.Port))
			}
		} else if rep.Port > 0 {
			if rep.NodeHealth.TCPErr != "" {
				detailLines = append(detailLines, fmt.Sprintf("TCP (%d): ❌ %s", rep.Port, rep.NodeHealth.TCPErr))
			} else if rep.NodeHealth.TCPPing > 0 {
				detailLines = append(detailLines, fmt.Sprintf("TCP (%d): ✅ %.0f ms", rep.Port, float64(rep.NodeHealth.TCPPing.Milliseconds())))
			}
		}
		if rep.NodeHealth.TLSErr != "" {
			detailLines = append(detailLines, fmt.Sprintf("TLS: ❌ %s", rep.NodeHealth.TLSErr))
		}
		for _, tr := range rep.Targets {
			site := simplifyTargetName(tr.URL)
			if tr.Success {
				detailLines = append(detailLines, fmt.Sprintf("%s: ✅ %.0f ms", site, float64(tr.Latency.Milliseconds())))
			} else {
				detailLines = append(detailLines, fmt.Sprintf("%s: ❌ %s", site, tr.Error))
			}
		}
		if rep.CheckHost != nil {
			detailLines = append(detailLines, fmt.Sprintf("Check-Host: %s", rep.CheckHost.Verdict))
		}

		detailsBlock := tu.RichBlockDetails(
			summary,
			tu.RichBlockPreformatted(tu.RichTextPlain(strings.Join(detailLines, "\n"))),
		)
		blocks = append(blocks, detailsBlock)
	}

	if !hasProblems {
		blocks = append(blocks, tu.RichBlockParagraph(tu.RichTextItalic(tu.RichTextPlain("Все прокси-хосты работают стабильно, аварий не обнаружено."))))
	}

	msg := tu.RichMessage(blocks...)
	return &msg
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

// ProcessSnapshot compares the current proxy snapshot against the last known
// state and handles transitions with smart alert lifecycle and stats tracking.
type deleteAction struct {
	chatID    int64
	messageID int
}

type recoveryAction struct {
	chatID    int64
	pm        metrics.ProxyMetric
	alert     *ActiveAlert
	hadAlert  bool
	downtime  time.Duration
	alertMode string
}

// ProcessSnapshot compares the current proxy snapshot against the last known
// state and handles transitions with smart alert lifecycle and stats tracking.
func (b *Bot) ProcessSnapshot(snapshot []metrics.ProxyMetric) {
	b.mu.Lock()

	now := b.now()
	cfg := b.GetConfig()
	isQuiet := IsQuietTime(now, cfg)

	if !b.seeded {
		for _, pm := range snapshot {
			if pm.Disabled {
				continue
			}
			b.lastSeen[pm.StableID] = pm.Online
			if b.statsStore != nil {
				b.statsStore.RecordCheck(pm.StableID, pm.Name, pm.Online, pm.LatencyMs)
				if !pm.Online {
					b.statsStore.RecordInitialDown(pm.StableID, pm.Name, now)
				}
			}
		}
		b.seeded = true
		b.mu.Unlock()
		return
	}

	var disabledDeletions []deleteAction
	var outages []metrics.ProxyMetric
	var recoveries []recoveryAction

	seenNow := make(map[string]bool, len(snapshot))
	for _, pm := range snapshot {
		if pm.Disabled {
			// Proxy is disabled: resolve/clean any active alerts and ignore
			for _, chatID := range b.chatIDs {
				if alert, hadAlert := b.tracker.Resolve(chatID, pm.StableID); hadAlert {
					disabledDeletions = append(disabledDeletions, deleteAction{chatID: chatID, messageID: alert.MessageID})
				}
			}
			delete(b.lastSeen, pm.StableID)
			continue
		}

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
				suppressed := false
				if b.statsStore != nil {
					flaps := b.statsStore.GetFlapCount24h(pm.StableID, now)
					if flaps > 10 {
						lastAlert := b.lastFlapAlert[pm.StableID]
						if !lastAlert.IsZero() && now.Sub(lastAlert) < 15*time.Minute {
							suppressed = true
						} else {
							b.lastFlapAlert[pm.StableID] = now
						}
					}
				}
				if !suppressed {
					outages = append(outages, pm)
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
					recoveries = append(recoveries, recoveryAction{
						chatID:    chatID,
						pm:        pm,
						alert:     alert,
						hadAlert:  hadAlert,
						downtime:  downtime,
						alertMode: cfg.AlertMode,
					})
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

	// Release lock BEFORE executing network calls
	b.mu.Unlock()

	// 1. Process deletions for disabled proxies
	for _, del := range disabledDeletions {
		if b.api != nil {
			_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
				ChatID:    tu.ID(del.chatID),
				MessageID: del.messageID,
			})
		}
	}

	// 2. Process outages
	for _, pm := range outages {
		softHint := ""
		if pm.LastErrorCategory > 0 {
			cat := checker.ErrorCategory(pm.LastErrorCategory)
			hint := checker.FormatSoftHint(cat, checker.IsUDPProto(pm.Protocol))
			softHint = fmt.Sprintf(" (вероятно: %s)", hint)
		}
		timeStr := now.In(b.loc()).Format("15:04")
		dropCount := int64(1)
		if b.statsStore != nil {
			if ps, ok := b.statsStore.Stats[pm.StableID]; ok && ps.DropCount > 0 {
				dropCount = ps.DropCount
			}
		}
		flapNote := ""
		if b.statsStore != nil && b.statsStore.GetFlapCount24h(pm.StableID, now) > 10 {
			flapNote = fmt.Sprintf("\n⚠️ <i>Частые сбои (%d за 24ч). Алерты приостановлены на 15 мин.</i>", b.statsStore.GetFlapCount24h(pm.StableID, now))
		}
		outageText := fmt.Sprintf("🔴 <b>%s</b> — не отвечает%s\n⏱ %s · %d-й сбой\n%s%s", escapeHTML(pm.Name), softHint, timeStr, dropCount, escapeHTML(pm.Address), flapNote)
		for _, chatID := range b.chatIDs {
			if b.tracker.HasAlert(chatID, pm.StableID) {
				continue
			}
			if sent, err := b.sendAndReturn(chatID, outageText); err == nil && sent != nil && sent.MessageID != 0 {
				b.tracker.Track(chatID, sent.MessageID, pm.StableID, pm.Name, now, "Offline")
			}
		}
	}

	// 3. Process recoveries
	for _, rec := range recoveries {
		if rec.alertMode == AlertModeClean {
			if rec.hadAlert && b.api != nil {
				_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
					ChatID:    tu.ID(rec.chatID),
					MessageID: rec.alert.MessageID,
				})
			}
			if b.notifyOnRecovery && rec.hadAlert {
				recoveryText := fmt.Sprintf("✅ <b>%s</b> восстановлен — %.0f ms", escapeHTML(rec.pm.Name), rec.pm.LatencyMs)
				if rec.downtime > 0 {
					recoveryText += fmt.Sprintf(" (простой: %s)", FormatDowntime(rec.downtime))
				}
				if sent, err := b.sendAndReturn(rec.chatID, recoveryText); err == nil && sent != nil && sent.MessageID != 0 {
					cID := rec.chatID
					mID := sent.MessageID
					time.AfterFunc(2*time.Minute, func() {
						if b.api != nil {
							_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
								ChatID:    tu.ID(cID),
								MessageID: mID,
							})
						}
					})
				}
			}
		} else { // AlertModeLive
			if rec.hadAlert && b.api != nil {
				liveText := fmt.Sprintf("✅ <b>%s</b> восстановлен — %.0f ms (простой: %s)",
					escapeHTML(rec.pm.Name), rec.pm.LatencyMs, FormatDowntime(rec.downtime))
				params := &telego.EditMessageTextParams{
					ChatID:    tu.ID(rec.chatID),
					MessageID: rec.alert.MessageID,
					Text:      liveText,
					ParseMode: telego.ModeHTML,
				}
				_, _ = b.api.EditMessageText(b.ctx, params)
			} else if b.notifyOnRecovery {
				recoveryText := fmt.Sprintf("✅ <b>%s</b> восстановлен — %.0f ms", escapeHTML(rec.pm.Name), rec.pm.LatencyMs)
				b.send(rec.chatID, recoveryText)
			}
		}
	}
}

func (b *Bot) broadcast(text string) {
	for _, chatID := range b.chatIDs {
		b.send(chatID, text)
	}
}

func (b *Bot) send(chatID int64, text string) {
	if b.api == nil {
		return
	}
	for _, chunk := range splitMessage(text, maxMessageLen) {
		params := tu.Message(tu.ID(chatID), chunk).WithParseMode(telego.ModeHTML)
		if _, err := b.api.SendMessage(b.ctx, params); err != nil {
			logger.Error("Telegram: failed to send message to %d: %v", chatID, err)
		}
	}
}

func (b *Bot) sendAndReturn(chatID int64, text string) (*telego.Message, error) {
	if b.api == nil {
		return &telego.Message{MessageID: 0}, nil
	}
	params := tu.Message(tu.ID(chatID), text).WithParseMode(telego.ModeHTML)
	sent, err := b.api.SendMessage(b.ctx, params)
	if err != nil {
		logger.Error("Telegram: failed to send message to %d: %v", chatID, err)
		return nil, err
	}
	return sent, nil
}

func (b *Bot) sendWithMarkup(chatID int64, text string, markup *telego.InlineKeyboardMarkup) (*telego.Message, error) {
	if b.api == nil {
		return &telego.Message{MessageID: 0}, nil
	}
	chunks := splitMessage(text, maxMessageLen)
	if len(chunks) == 0 {
		return &telego.Message{MessageID: 0}, nil
	}
	if len(chunks) == 1 {
		params := tu.Message(tu.ID(chatID), chunks[0]).WithParseMode(telego.ModeHTML).WithReplyMarkup(markup)
		sent, err := b.api.SendMessage(b.ctx, params)
		if err != nil {
			logger.Error("Telegram: failed to send message to %d: %v", chatID, err)
			return nil, err
		}
		return sent, nil
	}

	for i := 0; i < len(chunks)-1; i++ {
		params := tu.Message(tu.ID(chatID), chunks[i]).WithParseMode(telego.ModeHTML)
		if _, err := b.api.SendMessage(b.ctx, params); err != nil {
			logger.Error("Telegram: failed to send chunk to %d: %v", chatID, err)
		}
	}

	params := tu.Message(tu.ID(chatID), chunks[len(chunks)-1]).WithParseMode(telego.ModeHTML).WithReplyMarkup(markup)
	sent, err := b.api.SendMessage(b.ctx, params)
	if err != nil {
		logger.Error("Telegram: failed to send final chunk with markup to %d: %v", chatID, err)
		return nil, err
	}
	return sent, nil
}

func (b *Bot) sendOrUpdateMenu(chatID int64, text string, markup *telego.InlineKeyboardMarkup) {
	b.lastMenuMu.Lock()
	if b.lastMenuMsg == nil {
		b.lastMenuMsg = make(map[int64]int)
	}
	oldMsgID, hasOld := b.lastMenuMsg[chatID]
	b.lastMenuMu.Unlock()

	if hasOld && oldMsgID > 0 && b.api != nil {
		_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
			ChatID:    tu.ID(chatID),
			MessageID: oldMsgID,
		})
	}

	sent, _ := b.sendWithMarkup(chatID, text, markup)
	if sent != nil && sent.GetMessageID() > 0 {
		b.lastMenuMu.Lock()
		b.lastMenuMsg[chatID] = sent.GetMessageID()
		b.lastMenuMu.Unlock()
	}
}

func (b *Bot) edit(chatID int64, messageID int, text string) {
	b.editWithMarkup(chatID, messageID, text, nil)
}

func (b *Bot) buildAddSubReport(count int) string {
	var onlineCount, offlineCount int
	var offlineNames []string
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.Disabled {
				continue
			}
			if pm.Online {
				onlineCount++
			} else {
				offlineCount++
				offlineNames = append(offlineNames, pm.Name)
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ <b>Подписка добавлена.</b> Прокси-хостов: %d\n", count))
	sb.WriteString(fmt.Sprintf("🟢 Доступно: %d\n", onlineCount))
	if offlineCount > 0 {
		sb.WriteString(fmt.Sprintf("🔴 Недоступно: %d\n", offlineCount))
		limit := 10
		if len(offlineNames) < limit {
			limit = len(offlineNames)
		}
		for i := 0; i < limit; i++ {
			sb.WriteString(fmt.Sprintf("  • <code>%s</code>\n", escapeHTML(offlineNames[i])))
		}
		if len(offlineNames) > limit {
			sb.WriteString(fmt.Sprintf("  … и ещё %d прокси-хост(а/ов)\n", len(offlineNames)-limit))
		}
	}
	return sb.String()
}

func (b *Bot) editWithMarkup(chatID int64, messageID int, text string, markup *telego.InlineKeyboardMarkup) {
	b.lastMenuMu.Lock()
	if b.lastMenuMsg == nil {
		b.lastMenuMsg = make(map[int64]int)
	}
	b.lastMenuMsg[chatID] = messageID
	b.lastMenuMu.Unlock()

	if b.api == nil {
		return
	}
	params := &telego.EditMessageTextParams{
		ChatID:      tu.ID(chatID),
		MessageID:   messageID,
		Text:        text,
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: markup,
	}
	if _, err := b.api.EditMessageText(b.ctx, params); err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return
		}
		if strings.Contains(err.Error(), "MESSAGE_TOO_LONG") {
			fallbackParams := &telego.EditMessageTextParams{
				ChatID:      tu.ID(chatID),
				MessageID:   messageID,
				Text:        "📄 <b>Текст превышает лимит одного сообщения.</b>\nПолное содержимое отправлено отдельным сообщением ниже ⬇️",
				ParseMode:   telego.ModeHTML,
				ReplyMarkup: markup,
			}
			_, _ = b.api.EditMessageText(b.ctx, fallbackParams)
			b.send(chatID, text)
			return
		}
		logger.Error("Telegram: failed to edit message %d in chat %d: %v", messageID, chatID, err)
	}
}

func (b *Bot) editWithRichMarkup(chatID int64, messageID int, rich *telego.InputRichMessage, markup *telego.InlineKeyboardMarkup) error {
	params := &telego.EditMessageTextParams{
		ChatID:      tu.ID(chatID),
		MessageID:   messageID,
		RichMessage: rich,
		ReplyMarkup: markup,
	}
	_, err := b.api.EditMessageText(b.ctx, params)
	if err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return nil
		}
		logger.Error("Telegram: failed to edit rich message %d in chat %d: %v", messageID, chatID, err)
	}
	return err
}

func (b *Bot) sendRich(chatID int64, rich *telego.InputRichMessage, markup *telego.InlineKeyboardMarkup) (*telego.Message, error) {
	if rich == nil {
		return nil, fmt.Errorf("rich message is nil")
	}
	params := &telego.SendRichMessageParams{
		ChatID:      tu.ID(chatID),
		RichMessage: *rich,
		ReplyMarkup: markup,
	}
	sent, err := b.api.SendRichMessage(b.ctx, params)
	if err != nil {
		logger.Error("Telegram: failed to send rich message to %d: %v", chatID, err)
		return nil, err
	}
	return sent, nil
}

func (b *Bot) showRichReport(chatID int64, messageID int, rich *telego.InputRichMessage) {
	if messageID > 0 {
		if err := b.editWithRichMarkup(chatID, messageID, rich, RichReportMarkup()); err == nil {
			return
		}
	}
	_, _ = b.sendRich(chatID, rich, RichReportMarkup())
}

func splitMessage(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var cur strings.Builder
	var openTags []string

	for _, line := range lines {
		closing := closeTags(openTags)
		if cur.Len() > 0 && cur.Len()+len(line)+1+len(closing) > limit {
			cur.WriteString(closing)
			chunks = append(chunks, cur.String())
			cur.Reset()

			cur.WriteString(openTagsPrefix(openTags))
		}
		if cur.Len() > 0 && cur.Len() != len(openTagsPrefix(openTags)) {
			cur.WriteString("\n")
		}
		cur.WriteString(line)
		openTags = updateOpenTags(line, openTags)
	}
	if cur.Len() > 0 {
		cur.WriteString(closeTags(openTags))
		chunks = append(chunks, cur.String())
	}
	return chunks
}

func updateOpenTags(text string, openTags []string) []string {
	i := 0
	for i < len(text) {
		if text[i] == '<' {
			end := strings.IndexByte(text[i:], '>')
			if end == -1 {
				break
			}
			tagContent := strings.TrimSpace(text[i+1 : i+end])
			if strings.HasPrefix(tagContent, "/") {
				tagName := strings.ToLower(strings.TrimPrefix(tagContent, "/"))
				for j := len(openTags) - 1; j >= 0; j-- {
					openName := strings.ToLower(strings.Fields(openTags[j])[0])
					if openName == tagName {
						openTags = append(openTags[:j], openTags[j+1:]...)
						break
					}
				}
			} else if !strings.HasSuffix(tagContent, "/") {
				parts := strings.Fields(tagContent)
				if len(parts) > 0 {
					tagName := strings.ToLower(parts[0])
					switch tagName {
					case "b", "strong", "i", "em", "u", "ins", "s", "strike", "del", "code", "pre", "tg-spoiler", "blockquote":
						openTags = append(openTags, tagName)
					case "a":
						openTags = append(openTags, tagContent)
					}
				}
			}
			i += end + 1
		} else {
			i++
		}
	}
	return openTags
}

func closeTags(tags []string) string {
	var sb strings.Builder
	for i := len(tags) - 1; i >= 0; i-- {
		tagName := strings.Fields(tags[i])[0]
		sb.WriteString("</" + tagName + ">")
	}
	return sb.String()
}

func openTagsPrefix(tags []string) string {
	var sb strings.Builder
	for _, tag := range tags {
		sb.WriteString("<" + tag + ">")
	}
	return sb.String()
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func (b *Bot) getCheckHostMenuText() string {
	return "🌐 <b>Глобальная проверка доступности через Check-Host.net</b>\n\n" +
		"Выберите прокси из списка ниже для проверки через узлы по всему миру (Россия, Европа, США, Азия):\n\n" +
		"Или отправьте команду с любым хостом:\n" +
		"<code>/checkhost &lt;хост[:порт]&gt;</code>\n" +
		"<i>Примеры: <code>/checkhost 185.120.45.10:443</code> или <code>/checkhost mydomain.com</code></i>"
}

func (b *Bot) handleCheckHostCommand(msg *telego.Message) {
	target := commandArg(msg.Text)
	if target == "" {
		b.send(msg.Chat.ID, "💡 <b>Использование:</b> <code>/checkhost &lt;хост[:порт]&gt;</code>\n\n"+
			"Примеры:\n"+
			"• <code>/checkhost 185.120.45.10:443</code>\n"+
			"• <code>/checkhost mydomain.com</code>\n\n"+
			"Или выберите прокси в меню: /menu")
		return
	}

	if !strings.Contains(target, ":") {
		target = target + ":443"
	}

	sentMsg, _ := b.sendAndReturn(msg.Chat.ID, fmt.Sprintf("⏳ <b>Запрос отправлен в Check-Host.net...</b>\n"+
		"Проверяем <code>%s</code> (TCP) по глобальной сети узлов (РФ, Европа, США, Азия)...\n"+
		"Пожалуйста, подождите 4–6 секунд...", escapeHTML(target)))

	chClient := checker.NewCheckHostClient("", 1500*time.Millisecond)
	ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
	defer cancel()

	summary, err := chClient.CheckTCP(ctx, target, checker.DefaultWorldwideNodes)
	if err != nil {
		errMsg := fmt.Sprintf("❌ <b>Ошибка Check-Host:</b> %s", escapeHTML(err.Error()))
		if sentMsg != nil {
			b.editWithMarkup(msg.Chat.ID, sentMsg.GetMessageID(), errMsg, nil)
		} else {
			b.send(msg.Chat.ID, errMsg)
		}
		return
	}

	report := checker.FormatCheckHostReport(summary)
	if sentMsg != nil {
		b.editWithMarkup(msg.Chat.ID, sentMsg.GetMessageID(), report, nil)
	} else {
		b.send(msg.Chat.ID, report)
	}
}

func (b *Bot) handleCheckHostProxy(chatID int64, msgID int, stableID string) {
	snapshot := b.source.MetricsSnapshot()
	var targetProxy *metrics.ProxyMetric
	for i := range snapshot {
		if snapshot[i].StableID == stableID {
			targetProxy = &snapshot[i]
			break
		}
	}

	if targetProxy == nil {
		b.editWithMarkup(chatID, msgID, "❌ Прокси не найден в текущей конфигурации.", BackToMenuMarkup())
		return
	}

	target := targetProxy.Address
	if !strings.Contains(target, ":") {
		target = target + ":443"
	}

	b.editWithMarkup(chatID, msgID, fmt.Sprintf("⏳ <b>Запрос отправлен в Check-Host.net...</b>\n"+
		"Проверяем <b>%s</b> (<code>%s</code>) по всемирной сети узлов...\nПожалуйста, подождите 4–6 секунд...",
		escapeHTML(targetProxy.Name), escapeHTML(target)), BackToMenuMarkup())

	chClient := checker.NewCheckHostClient("", 1500*time.Millisecond)
	ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
	defer cancel()

	summary, err := chClient.CheckTCP(ctx, target, checker.DefaultWorldwideNodes)
	if err != nil {
		b.editWithMarkup(chatID, msgID, fmt.Sprintf("❌ <b>Ошибка Check-Host:</b> %s", escapeHTML(err.Error())), BackToMenuMarkup())
		return
	}

	report := checker.FormatCheckHostReport(summary)
	b.editWithMarkup(chatID, msgID, report, BackToMenuMarkup())
}

func (b *Bot) getCheckHostSettingsText() string {
	cfg := b.GetConfig()
	bgStatus := "❌ Выключена"
	if cfg.CheckHostBgEnabled {
		bgStatus = "✅ Включена"
	}

	alertStatus := "🔕 Выключены"
	if cfg.CheckHostAlertEnabled {
		alertStatus = "🔔 Включены"
	}

	intHours := cfg.CheckHostIntervalHours
	if intHours <= 0 {
		intHours = 1
	}

	return fmt.Sprintf("🌐 <b>Настройки фоновой проверки Check-Host</b>\n\n"+
		"• Фоновая проверка: <b>%s</b>\n"+
		"• Периодичность: <b>каждые %d ч.</b>\n"+
		"• Алерты по РФ: <b>%s</b>\n\n"+
		"Периодическая проверка хостов из российских и зарубежных локаций через Check-Host.net. Оповещает при блокировках или сетевых сбоях в РФ.\n\n"+
		"<i>Отключённые в настройках хосты автоматически пропускаются.</i>",
		bgStatus, intHours, alertStatus)
}

func (b *Bot) handleCheckHostBgCommand(msg *telego.Message) {
	arg := strings.TrimSpace(commandArg(msg.Text))
	cfg := b.GetConfig()
	if arg == "" {
		b.sendWithMarkup(msg.Chat.ID, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(cfg))
		return
	}

	switch strings.ToLower(arg) {
	case "on", "enable", "1":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.CheckHostBgEnabled = true
			})
		}
		b.send(msg.Chat.ID, "✅ Фоновая проверка Check-Host <b>включена</b>.")
	case "off", "disable", "0":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.CheckHostBgEnabled = false
			})
		}
		b.send(msg.Chat.ID, "❌ Фоновая проверка Check-Host <b>выключена</b>.")
	case "alert_on", "alerts_on":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.CheckHostAlertEnabled = true
			})
		}
		b.send(msg.Chat.ID, "🔔 Алерты по недоступности из РФ <b>включены</b>.")
	case "alert_off", "alerts_off":
		if b.configMgr != nil {
			_ = b.configMgr.Update(func(c *BotConfig) {
				c.CheckHostAlertEnabled = false
			})
		}
		b.send(msg.Chat.ID, "🔕 Алерты по недоступности из РФ <b>выключены</b>.")
	case "run", "now":
		b.send(msg.Chat.ID, "🚀 Запуск фоновой проверки Check-Host...")
		go b.RunCheckHostAudit()
	default:
		cleanArg := strings.TrimSuffix(strings.ToLower(arg), "h")
		cleanArg = strings.TrimSuffix(cleanArg, "ч")
		if hours, err := strconv.Atoi(cleanArg); err == nil && hours > 0 {
			if b.configMgr != nil {
				_ = b.configMgr.Update(func(c *BotConfig) {
					c.CheckHostIntervalHours = hours
				})
			}
			b.send(msg.Chat.ID, fmt.Sprintf("⏱️ Интервал фонового Check-Host установлен на <b>каждые %d ч.</b>", hours))
		} else {
			b.send(msg.Chat.ID, "Использование: <code>/checkhost_bg [on|off|alert_on|alert_off|1h|2h|run]</code>")
		}
	}
}

// RunCheckHostAudit checks all unique active proxy hosts via Check-Host and alerts if unreachable from Russia.
func (b *Bot) RunCheckHostAudit() {
	b.checkHostMu.Lock()
	if b.checkHostRunning {
		b.checkHostMu.Unlock()
		return
	}
	b.checkHostRunning = true
	b.checkHostMu.Unlock()

	defer func() {
		b.checkHostMu.Lock()
		b.checkHostRunning = false
		b.checkHostMu.Unlock()
	}()

	cfg := b.GetConfig()
	if !cfg.CheckHostBgEnabled {
		return
	}

	snapshot := b.source.MetricsSnapshot()
	if len(snapshot) == 0 {
		return
	}

	type auditTarget struct {
		targetAddr string
		host       string
		isUDP      bool
		proxyName  string
		stableID   string
	}

	seen := make(map[string]bool)
	var targets []auditTarget

	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		host, _, err := net.SplitHostPort(pm.Address)
		if err != nil {
			host = pm.Address
		}
		if cfg.IsDisabled(host, pm.StableID) {
			continue
		}

		isUDP := checker.IsUDPProto(pm.Protocol)
		dedupKey := pm.Address
		if isUDP {
			dedupKey = host
		}

		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true

		targets = append(targets, auditTarget{
			targetAddr: pm.Address,
			host:       host,
			isUDP:      isUDP,
			proxyName:  pm.Name,
			stableID:   pm.StableID,
		})
	}

	if len(targets) == 0 {
		return
	}

	logger.Info("Starting periodic Check-Host audit for %d unique active targets", len(targets))
	chClient := b.checkHostClient
	if chClient == nil {
		chClient = checker.NewCheckHostClient("", 1500*time.Millisecond)
	}
	fastNodes := b.checkHostNodes
	if len(fastNodes) == 0 {
		fastNodes = append(checker.DefaultFastRUNodes, checker.DefaultFastWorldNodes...)
	}

	for i, target := range targets {
		if i > 0 {
			time.Sleep(3 * time.Second) // rate-limiting between Check-Host API calls
		}

		ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
		var summary *checker.CheckHostSummary
		var err error

		if target.isUDP {
			summary, err = chClient.CheckPing(ctx, target.host, fastNodes)
		} else {
			summary, err = chClient.CheckTCP(ctx, target.targetAddr, fastNodes)
		}
		cancel()
		if err != nil || summary == nil {
			continue
		}

		alertKey := fmt.Sprintf("checkhost:%s", target.targetAddr)
		now := time.Now()

		if !summary.RUAvailable {
			// Unreachable from Russian federation nodes
			if !cfg.CheckHostAlertEnabled {
				continue
			}
			isQuiet := IsQuietTime(now, cfg)

			for _, chatID := range b.chatIDs {
				if b.tracker.HasAlert(chatID, alertKey) {
					continue
				}

				worldStatus := "❌ недоступен"
				verdict := "Хост недоступен как из РФ, так и из других стран"
				if summary.WorldAvailable {
					worldStatus = "✅ доступен"
					verdict = "Вероятная блокировка РКН на территории РФ"
				}

				alertText := fmt.Sprintf("⚠️ <b>[Check-Host] Проблема доступности из РФ</b>\n\n"+
					"• Сервер: <code>%s</code> <i>(%s)</i>\n"+
					"• Статус: РФ ❌ недоступен | Мир %s\n"+
					"• Вердикт: <b>%s</b>\n"+
					"🔗 <a href=\"%s\">Отчёт Check-Host</a>",
					escapeHTML(target.targetAddr), escapeHTML(target.proxyName),
					worldStatus, escapeHTML(verdict), summary.PermanentLink)

				if isQuiet {
					b.eventBuffer.Add(BufferedEvent{
						Timestamp: now,
						Type:      "down",
						ProxyName: fmt.Sprintf("[Check-Host] %s", target.proxyName),
						Reason:    "Недоступен из РФ",
					})
				} else {
					if sent, err := b.sendAndReturn(chatID, alertText); err == nil {
						b.tracker.Track(chatID, sent.MessageID, alertKey, target.proxyName, now, "CheckHost RU Block")
					}
				}
			}
		} else {
			// RU is available: resolve any previous alert
			for _, chatID := range b.chatIDs {
				alert, hadAlert := b.tracker.Resolve(chatID, alertKey)
				if !hadAlert {
					continue
				}

				downtime := now.Sub(alert.DownAt)
				if cfg.AlertMode == AlertModeClean {
					if b.api != nil {
						_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
							ChatID:    tu.ID(chatID),
							MessageID: alert.MessageID,
						})
					}
					if b.notifyOnRecovery {
						recText := fmt.Sprintf("✅ <b>[Check-Host] Доступность из РФ восстановилась</b>\n\n• Сервер: <code>%s</code> <i>(%s)</i>",
							escapeHTML(target.targetAddr), escapeHTML(target.proxyName))
						if downtime > 0 {
							recText += fmt.Sprintf("\n• Был недоступен: <b>%s</b>", FormatDowntime(downtime))
						}
						if sent, err := b.sendAndReturn(chatID, recText); err == nil {
							go func(cID int64, mID int) {
								time.Sleep(2 * time.Minute)
								if b.api != nil {
									_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
										ChatID:    tu.ID(cID),
										MessageID: mID,
									})
								}
							}(chatID, sent.MessageID)
						}
					}
				} else { // AlertModeLive
					if b.api != nil {
						liveText := fmt.Sprintf("✅ <b>[Check-Host] Доступность из РФ восстановилась</b>\n\n• Сервер: <code>%s</code> <i>(%s)</i> (был недоступен %s)",
							escapeHTML(target.targetAddr), escapeHTML(target.proxyName), FormatDowntime(downtime))
						params := &telego.EditMessageTextParams{
							ChatID:    tu.ID(chatID),
							MessageID: alert.MessageID,
							Text:      liveText,
							ParseMode: telego.ModeHTML,
						}
						_, _ = b.api.EditMessageText(b.ctx, params)
					}
				}
			}
		}
	}
}
