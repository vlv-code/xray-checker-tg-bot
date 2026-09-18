// Package telegram wires the checker's proxy snapshot into a Telegram bot: it
// sends alerts whenever a proxy's online/offline state flips, manages alert lifecycles
// (live-editing and auto-cleanup), tracks outage statistics, enforces quiet hours with
// morning and daytime digests, and provides an interactive inline menu.
package telegram

import (
	"context"
	"fmt"
	"net/http"
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
	targets        []ChatTarget
	allowedChatIDs map[int64]bool
	source         metrics.MetricsSource
	subs           SubscriptionManager
	nodeMgr        NodeManager

	nodeHealthMu   sync.Mutex
	lastNodeHealth map[string]nodeHealthState

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
	lastMenuMsg map[string]int

	msgSeqMu sync.Mutex
	msgSeq   map[string]int64

	freshMu      sync.RWMutex
	subFreshness map[string]SubFreshness

	nowFunc func() time.Time
}

// New creates a Bot and verifies the token against the Telegram API.
func New(token string, targets []ChatTarget, source metrics.MetricsSource, notifyOnRecovery, commandsEnabled bool, subs SubscriptionManager) (*Bot, error) {
	httpClient := &http.Client{
		Timeout:   75 * time.Second,
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

	allowed := make(map[int64]bool, len(targets))
	for _, t := range targets {
		allowed[t.ChatID] = true
	}

	logger.Info("Telegram bot authorized as @%s", user.Username)

	return &Bot{
		api:              api,
		ctx:              ctx,
		cancel:           cancel,
		targets:          targets,
		allowedChatIDs:   allowed,
		source:           source,
		subs:             subs,
		notifyOnRecovery: notifyOnRecovery,
		commandsEnabled:  commandsEnabled,
		tracker:          NewAlertTracker(),
		eventBuffer:      NewEventBuffer(),
		lastSeen:         make(map[string]bool),
		lastFlapAlert:    make(map[string]time.Time),
		lastNodeHealth:   make(map[string]nodeHealthState),
		lastMenuMsg:      make(map[string]int),
		msgSeq:           make(map[string]int64),
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

func (b *Bot) updateConfig(fn func(*BotConfig)) error {
	if b.configMgr == nil {
		return nil
	}
	if err := b.configMgr.Update(fn); err != nil {
		logger.Error("Telegram: failed to save bot config: %v", err)
		return err
	}
	return nil
}

// SetRichMode sets default reporting format.
func (b *Bot) SetRichMode(enabled bool) {
	b.richMode = enabled
	_ = b.updateConfig(func(cfg *BotConfig) {
		cfg.RichMode = enabled
	})
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
	_ = b.updateConfig(func(c *BotConfig) {
		c.CheckIntervalSec = sec
	})
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
		NodeSyncEnabled:        true,
		NodeAlertsEnabled:      true,
		NodeProxyAlertsChat:    true,
		NodeStaleTimeoutSec:    90,
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
		Timeout: 50,
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

// Stop halts long-polling and scheduled routines and flushes stats to disk.
func (b *Bot) Stop() {
	select {
	case <-b.stopChan:
	default:
		close(b.stopChan)
	}
	if b.cancel != nil {
		b.cancel()
	}
	// The periodic save runs every 5 minutes; without this flush everything
	// since the last tick is lost on shutdown.
	if b.statsStore != nil {
		if err := b.statsStore.Save(); err != nil {
			logger.Error("Telegram: failed to save stats on shutdown: %v", err)
		}
	}
}

func (b *Bot) setupBotMenuButton() {
	commands := []telego.BotCommand{
		{Command: "menu", Description: "Главное меню и сводка"},
		{Command: "diag", Description: "Подробная сводка"},
		{Command: "checkhost", Description: "Проверка Check-Host"},
		{Command: "settings", Description: "Настройки бота"},
		{Command: "status", Description: "Статус прокси-хостов"},
		{Command: "id", Description: "ID чата и топика для настроек"},
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
		localNow := now.In(b.loc())
		nowMinutes := localNow.Hour()*60 + localNow.Minute()
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

func (b *Bot) loc() *time.Location {
	return b.GetConfig().Location()
}

func (b *Bot) now() time.Time {
	if b.nowFunc != nil {
		return b.nowFunc().In(b.loc())
	}
	return time.Now().In(b.loc())
}

func (b *Bot) broadcast(text string) {
	for _, t := range b.targets {
		b.send(t, text)
	}
}

func (b *Bot) send(t ChatTarget, text string) {
	if b.api == nil {
		return
	}
	for _, chunk := range splitMessage(text, maxMessageLen) {
		params := tu.Message(tu.ID(t.ChatID), chunk).WithParseMode(telego.ModeHTML)
		if t.ThreadID > 0 {
			params = params.WithMessageThreadID(t.ThreadID)
		}
		if _, err := b.api.SendMessage(b.ctx, params); err != nil {
			logger.Error("Telegram: failed to send message to %s: %v", t.targetKey(), err)
		}
	}
}

func (b *Bot) sendAndReturn(t ChatTarget, text string) (*telego.Message, error) {
	if b.api == nil {
		return &telego.Message{MessageID: 0}, nil
	}
	params := tu.Message(tu.ID(t.ChatID), text).WithParseMode(telego.ModeHTML)
	if t.ThreadID > 0 {
		params = params.WithMessageThreadID(t.ThreadID)
	}
	sent, err := b.api.SendMessage(b.ctx, params)
	if err != nil {
		logger.Error("Telegram: failed to send message to %s: %v", t.targetKey(), err)
		return nil, err
	}
	return sent, nil
}

func (b *Bot) sendWithMarkup(t ChatTarget, text string, markup *telego.InlineKeyboardMarkup) (*telego.Message, error) {
	if b.api == nil {
		return &telego.Message{MessageID: 0}, nil
	}
	chunks := splitMessage(text, maxMessageLen)
	if len(chunks) == 0 {
		return &telego.Message{MessageID: 0}, nil
	}
	if len(chunks) == 1 {
		params := tu.Message(tu.ID(t.ChatID), chunks[0]).WithParseMode(telego.ModeHTML).WithReplyMarkup(markup)
		if t.ThreadID > 0 {
			params = params.WithMessageThreadID(t.ThreadID)
		}
		sent, err := b.api.SendMessage(b.ctx, params)
		if err != nil {
			logger.Error("Telegram: failed to send message to %s: %v", t.targetKey(), err)
			return nil, err
		}
		return sent, nil
	}

	for i := 0; i < len(chunks)-1; i++ {
		params := tu.Message(tu.ID(t.ChatID), chunks[i]).WithParseMode(telego.ModeHTML)
		if t.ThreadID > 0 {
			params = params.WithMessageThreadID(t.ThreadID)
		}
		if _, err := b.api.SendMessage(b.ctx, params); err != nil {
			logger.Error("Telegram: failed to send chunk to %s: %v", t.targetKey(), err)
		}
	}

	params := tu.Message(tu.ID(t.ChatID), chunks[len(chunks)-1]).WithParseMode(telego.ModeHTML).WithReplyMarkup(markup)
	if t.ThreadID > 0 {
		params = params.WithMessageThreadID(t.ThreadID)
	}
	sent, err := b.api.SendMessage(b.ctx, params)
	if err != nil {
		logger.Error("Telegram: failed to send final chunk with markup to %s: %v", t.targetKey(), err)
		return nil, err
	}
	return sent, nil
}

func (b *Bot) nextMsgSeq(chatID int64, msgID int) int64 {
	b.msgSeqMu.Lock()
	defer b.msgSeqMu.Unlock()
	if b.msgSeq == nil {
		b.msgSeq = make(map[string]int64)
	}
	key := fmt.Sprintf("%d:%d", chatID, msgID)
	b.msgSeq[key]++
	return b.msgSeq[key]
}

func (b *Bot) isMsgSeqValid(chatID int64, msgID int, seq int64) bool {
	b.msgSeqMu.Lock()
	defer b.msgSeqMu.Unlock()
	if b.msgSeq == nil {
		return true
	}
	key := fmt.Sprintf("%d:%d", chatID, msgID)
	return b.msgSeq[key] == seq
}

func (b *Bot) invalidateMsgSeq(chatID int64, msgID int) {
	b.msgSeqMu.Lock()
	defer b.msgSeqMu.Unlock()
	if b.msgSeq != nil {
		key := fmt.Sprintf("%d:%d", chatID, msgID)
		b.msgSeq[key]++
	}
}

func (b *Bot) sendOrUpdateMenu(t ChatTarget, text string, markup *telego.InlineKeyboardMarkup) {
	b.lastMenuMu.Lock()
	defer b.lastMenuMu.Unlock()

	if b.lastMenuMsg == nil {
		b.lastMenuMsg = make(map[string]int)
	}
	key := t.targetKey()
	oldMsgID, hasOld := b.lastMenuMsg[key]

	if hasOld && oldMsgID > 0 && b.api != nil {
		_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
			ChatID:    tu.ID(t.ChatID),
			MessageID: oldMsgID,
		})
	}

	sent, _ := b.sendWithMarkup(t, text, markup)
	if sent != nil && sent.GetMessageID() > 0 {
		b.lastMenuMsg[key] = sent.GetMessageID()
	}
}

func (b *Bot) edit(chatID int64, messageID int, text string) {
	b.editWithMarkup(chatID, messageID, text, nil)
}

func (b *Bot) editWithMarkup(chatID int64, messageID int, text string, markup *telego.InlineKeyboardMarkup) {
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
			b.send(ChatTarget{ChatID: chatID}, text)
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

func (b *Bot) sendRich(t ChatTarget, rich *telego.InputRichMessage, markup *telego.InlineKeyboardMarkup) (*telego.Message, error) {
	if rich == nil {
		return nil, fmt.Errorf("rich message is nil")
	}
	params := &telego.SendRichMessageParams{
		ChatID:      tu.ID(t.ChatID),
		RichMessage: *rich,
		ReplyMarkup: markup,
	}
	if t.ThreadID > 0 {
		params.MessageThreadID = t.ThreadID
	}
	sent, err := b.api.SendRichMessage(b.ctx, params)
	if err != nil {
		logger.Error("Telegram: failed to send rich message to %s: %v", t.targetKey(), err)
		return nil, err
	}
	return sent, nil
}

func (b *Bot) showRichReport(t ChatTarget, messageID int, rich *telego.InputRichMessage, markup ...*telego.InlineKeyboardMarkup) {
	mk := RichReportMarkup()
	if len(markup) > 0 && markup[0] != nil {
		mk = markup[0]
	}
	if messageID > 0 {
		if err := b.editWithRichMarkup(t.ChatID, messageID, rich, mk); err == nil {
			return
		}
	}
	_, _ = b.sendRich(t, rich, mk)
}
