package telegram

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
)

// SubscriptionManager lets the bot inspect and change the set of subscription
// URLs the checker fetches from. It's implemented in main, wrapping a
// subscription.URLStore plus a callback that re-fetches every subscription
// and applies the result — so /addsub and /delsub go through the exact same
// reload path (and the same locking) as the periodic subscription updater.
type SubscriptionManager interface {
	// Static returns the URLs configured via --subscription-url / env. These
	// can't be removed through the bot.
	Static() []string
	// Dynamic returns the URLs previously added via AddSubscription, in the
	// order they were added.
	Dynamic() []string
	// AddSubscription validates url, persists it, reloads every
	// subscription, and returns the resulting total proxy count.
	AddSubscription(url string) (proxyCount int, err error)
	// RemoveSubscription drops url from the dynamic set (static URLs can't
	// be removed) and reloads. found is false if url wasn't a dynamic entry.
	RemoveSubscription(url string) (found bool, proxyCount int, err error)
}

func (b *Bot) handleAddSub(msg *telego.Message) {
	chatID := msg.Chat.ID
	url := commandArg(msg.Text)
	if url == "" {
		b.send(chatID, "Использование: /addsub &lt;URL подписки&gt;")
		return
	}
	if b.subs == nil {
		b.send(chatID, "Управление подписками через бота выключено.")
		return
	}

	b.send(chatID, "⏳ Проверяю и добавляю подписку…")
	count, err := b.subs.AddSubscription(url)
	if err != nil {
		b.send(chatID, fmt.Sprintf("❌ Не удалось добавить подписку.\n%s", escapeHTML(err.Error())))
		return
	}
	b.send(chatID, fmt.Sprintf("✅ Подписка добавлена. Всего прокси: %d", count))
}

func (b *Bot) handleDelSub(msg *telego.Message) {
	chatID := msg.Chat.ID
	url := commandArg(msg.Text)
	if url == "" {
		b.send(chatID, "Использование: /delsub &lt;URL подписки&gt;")
		return
	}
	if b.subs == nil {
		b.send(chatID, "Управление подписками через бота выключено.")
		return
	}

	found, count, err := b.subs.RemoveSubscription(url)
	if err != nil {
		b.send(chatID, fmt.Sprintf("❌ Не удалось удалить подписку.\n%s", escapeHTML(err.Error())))
		return
	}
	if !found {
		b.send(chatID, "Не найдено подписки с таким URL среди добавленных через бота.\n"+
			"Подписки, заданные при запуске (переменные окружения/флаги), удалить нельзя.")
		return
	}
	b.send(chatID, fmt.Sprintf("✅ Подписка удалена. Всего прокси: %d", count))
}

func (b *Bot) replySubs(chatID int64) {
	if b.subs == nil {
		b.send(chatID, "Управление подписками через бота выключено.")
		return
	}

	static := b.subs.Static()
	dynamic := b.subs.Dynamic()

	var body strings.Builder
	body.WriteString("<b>Подписки</b>\n\n")
	if len(static) == 0 && len(dynamic) == 0 {
		body.WriteString("Нет ни одной подписки.\n")
	}
	for _, u := range static {
		fmt.Fprintf(&body, "🔒 %s\n", escapeHTML(u))
	}
	for _, u := range dynamic {
		fmt.Fprintf(&body, "➕ %s\n", escapeHTML(u))
	}
	if len(static) > 0 || len(dynamic) > 0 {
		body.WriteString("\n🔒 — задана при запуске, ➕ — добавлена через /addsub")
	}

	b.send(chatID, body.String())
}

// commandArg returns the first whitespace-separated argument after a
// command, e.g. commandArg("/addsub https://x/sub") == "https://x/sub". It
// also tolerates Telegram's "/addsub@BotName <arg>" form used in groups,
// since strings.Fields simply splits on whitespace regardless of what the
// first field contains.
func commandArg(text string) string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}
