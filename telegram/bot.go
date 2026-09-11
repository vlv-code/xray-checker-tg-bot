// Package telegram wires the checker's proxy snapshot into a Telegram bot: it
// sends a message whenever a proxy's online/offline state flips, and (optionally)
// answers /status and /help commands from a fixed set of allowed chats.
package telegram

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	tgbotapi "github.com/kirugan/telegram-bot-api/v5"

	"xray-checker/logger"
	"xray-checker/metrics"
)

// maxMessageLen keeps outgoing messages under Telegram's ~4096 character limit
// with headroom for HTML entities added by escaping.
const maxMessageLen = 3500

// Bot sends proxy-status notifications to Telegram and, optionally, answers
// interactive commands from a fixed set of allowed chats.
type Bot struct {
	api            *tgbotapi.BotAPI
	chatIDs        []int64
	allowedChatIDs map[int64]bool
	source         metrics.MetricsSource
	subs           SubscriptionManager

	notifyOnRecovery bool
	commandsEnabled  bool

	mu       sync.Mutex
	lastSeen map[string]bool // stable_id -> last known online status
	seeded   bool            // true once the first snapshot has been recorded
}

// New creates a Bot and verifies the token against the Telegram API. source
// supplies the live proxy snapshot for /status and for transition detection —
// checker.ProxyChecker satisfies metrics.MetricsSource. subs enables /addsub,
// /delsub and /subs; pass nil to disable those commands (they're the only
// ones that let an allowed chat change what the checker fetches from, so
// callers may want to gate them separately from notifyOnRecovery/commandsEnabled).
func New(token string, chatIDs []int64, source metrics.MetricsSource, notifyOnRecovery, commandsEnabled bool, subs SubscriptionManager) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}

	allowed := make(map[int64]bool, len(chatIDs))
	for _, id := range chatIDs {
		allowed[id] = true
	}

	logger.Info("Telegram bot authorized as @%s", api.Self.UserName)

	return &Bot{
		api:              api,
		chatIDs:          chatIDs,
		allowedChatIDs:   allowed,
		source:           source,
		subs:             subs,
		notifyOnRecovery: notifyOnRecovery,
		commandsEnabled:  commandsEnabled,
		lastSeen:         make(map[string]bool),
	}, nil
}

// StartCommands begins long-polling for updates and answering /status and
// /help in a background goroutine. It is a no-op if commands are disabled.
func (b *Bot) StartCommands() {
	if !b.commandsEnabled {
		return
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)

	go func() {
		for update := range updates {
			if update.Message == nil {
				continue
			}
			b.handleMessage(update.Message)
		}
	}()
}

// Stop halts long-polling. Safe to call even if StartCommands was never called
// or commands are disabled.
func (b *Bot) Stop() {
	b.api.StopReceivingUpdates()
}

func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	if !b.allowedChatIDs[msg.Chat.ID] {
		logger.Warn("Telegram: ignoring message from unauthorized chat %d (%s)", msg.Chat.ID, msg.Chat.UserName)
		return
	}

	switch {
	case strings.HasPrefix(msg.Text, "/status"):
		b.replyStatus(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/subs"):
		b.replySubs(msg.Chat.ID)
	case strings.HasPrefix(msg.Text, "/addsub"):
		// Fetching the subscription and reloading Xray can take a few
		// seconds, so this runs off the update-processing loop to keep the
		// bot responsive to other chats/commands in the meantime.
		go b.handleAddSub(msg)
	case strings.HasPrefix(msg.Text, "/delsub"), strings.HasPrefix(msg.Text, "/removesub"):
		go b.handleDelSub(msg)
	case strings.HasPrefix(msg.Text, "/start"), strings.HasPrefix(msg.Text, "/help"):
		b.replyHelp(msg.Chat.ID)
	}
}

func (b *Bot) replyStatus(chatID int64) {
	snapshot := b.source.MetricsSnapshot()
	if len(snapshot) == 0 {
		b.send(chatID, "Нет данных о прокси — проверки ещё не выполнялись.")
		return
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
	b.send(chatID, header+body.String())
}

func (b *Bot) replyHelp(chatID int64) {
	text := "<b>Xray Checker</b>\n\n" +
		"/status — текущий статус всех прокси\n"
	if b.subs != nil {
		text += "/subs — список подписок\n" +
			"/addsub &lt;URL&gt; — добавить подписку\n" +
			"/delsub &lt;URL&gt; — удалить добавленную подписку\n"
	}
	text += "/help — это сообщение\n\n" +
		"Уведомления о недоступности и восстановлении приходят сюда автоматически."
	b.send(chatID, text)
}

// ProcessSnapshot compares the current proxy snapshot against the last known
// state and sends a notification for every online/offline transition. The
// first call after startup only seeds state — it never fires notifications —
// so a pre-existing outage at boot doesn't trigger a burst of alerts, and a
// proxy added later by a subscription update is likewise seeded silently on
// the cycle it first appears rather than treated as a transition.
func (b *Bot) ProcessSnapshot(snapshot []metrics.ProxyMetric) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.seeded {
		for _, pm := range snapshot {
			b.lastSeen[pm.StableID] = pm.Online
		}
		b.seeded = true
		return
	}

	seenNow := make(map[string]bool, len(snapshot))
	for _, pm := range snapshot {
		seenNow[pm.StableID] = true

		prev, known := b.lastSeen[pm.StableID]
		b.lastSeen[pm.StableID] = pm.Online
		if !known {
			continue
		}

		switch {
		case prev && !pm.Online:
			b.broadcast(fmt.Sprintf("🔴 <b>%s</b> недоступен\n%s", escapeHTML(pm.Name), escapeHTML(pm.Address)))
		case !prev && pm.Online && b.notifyOnRecovery:
			b.broadcast(fmt.Sprintf("✅ <b>%s</b> снова в строю — %.0f ms", escapeHTML(pm.Name), pm.LatencyMs))
		}
	}

	// Drop state for proxies no longer in the current set (removed by a
	// subscription update) so lastSeen doesn't grow without bound.
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
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = tgbotapi.ModeHTML
		if _, err := b.api.Send(msg); err != nil {
			logger.Error("Telegram: failed to send message to %d: %v", chatID, err)
		}
	}
}

// splitMessage breaks text into chunks no larger than limit, breaking on line
// boundaries so a status list with many proxies doesn't get cut mid-line.
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
