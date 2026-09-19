package telegram

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mymmrac/telego"
)

// isAllowedSubURL checks that a subscription URL uses http or https scheme.
// Non-HTTP schemes like file://, folder://, base64:// are disallowed for security.
func isAllowedSubURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// SubFreshness holds update metadata for a subscription URL.
type SubFreshness struct {
	LastUpdate time.Time
	Count      int
	PrevCount  int
	Added      int
	Removed    int
}

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
	t := targetFromMessage(msg)
	url := commandArg(msg.Text)
	if url == "" {
		b.replyCommand(msg, "Использование: /addsub &lt;URL подписки&gt;")
		return
	}
	if !isAllowedSubURL(url) {
		b.replyCommand(msg, "❌ Разрешены только URL подписок со схемой http:// или https://")
		return
	}
	if b.subs == nil {
		b.replyCommand(msg, "Управление подписками через бота выключено.")
		return
	}

	sent, _ := b.sendAndReturn(t, "⏳ Проверка и добавление подписки…")
	placeholderID := 0
	if sent != nil {
		placeholderID = sent.GetMessageID()
	}

	count, err := b.subs.AddSubscription(url)
	if err != nil {
		errText := fmt.Sprintf("❌ Не удалось добавить подписку.\n%s", escapeHTML(err.Error()))
		if placeholderID > 0 {
			b.edit(chatID, placeholderID, errText)
		} else {
			b.replyCommand(msg, errText)
		}
		return
	}

	report := b.buildAddSubReport(count)
	if placeholderID > 0 {
		b.edit(chatID, placeholderID, report)
	} else {
		b.replyCommand(msg, report)
	}
}

func (b *Bot) handleDelSub(msg *telego.Message) {
	url := commandArg(msg.Text)
	if url == "" {
		b.replyCommand(msg, "Использование: /delsub &lt;URL подписки&gt;")
		return
	}
	if b.subs == nil {
		b.replyCommand(msg, "Управление подписками через бота выключено.")
		return
	}

	found, count, err := b.subs.RemoveSubscription(url)
	if err != nil {
		b.replyCommand(msg, fmt.Sprintf("❌ Не удалось удалить подписку.\n%s", escapeHTML(err.Error())))
		return
	}
	if !found {
		b.replyCommand(msg, "Не найдено подписки с таким URL среди добавленных через бота.\n"+
			"Подписки, заданные при запуске (переменные окружения/флаги), удалить нельзя.")
		return
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Подписка удалена. Прокси-хостов: %d", count))
}

func (b *Bot) replySubs(t ChatTarget) {
	b.sendOrUpdateMenu(t, b.getSubsText(), BackToSettingsMarkup())
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
