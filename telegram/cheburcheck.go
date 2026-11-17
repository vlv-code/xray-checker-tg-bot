package telegram

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
)

// resolveCheburClient returns the injected client or lazily builds the default
// one from DefaultCheburScanners (inactive while that list is empty).
func (b *Bot) resolveCheburClient() *checker.CheburcheckClient {
	if b.cheburClient != nil {
		return b.cheburClient
	}
	return checker.NewCheburcheckClient("", 0, checker.DefaultCheburScanners)
}

// handleCheburcheckCommand runs an active RF blocking probe for an arbitrary
// host (domain or IP — port is not needed and is stripped if passed).
func (b *Bot) handleCheburcheckCommand(msg *telego.Message) {
	t := targetFromMessage(msg)
	target := strings.TrimSpace(commandArg(msg.Text))
	if target == "" {
		b.send(t, "💡 <b>Использование:</b> <code>/cheburcheck &lt;хост&gt;</code>\n\n"+
			"Домен или IP; порт не нужен (в пробинг передаётся только хост).\n\n"+
			"Примеры:\n"+
			"• <code>/cheburcheck mydomain.com</code>\n"+
			"• <code>/cheburcheck 185.120.45.10</code>")
		return
	}

	if h, _, err := net.SplitHostPort(target); err == nil && h != "" {
		target = h
	}

	chebClient := b.resolveCheburClient()
	if !chebClient.HasCredentials() {
		b.send(t, "Сканеры Cheburcheck не настроены (DefaultCheburScanners пуст)")
		return
	}

	sentMsg, _ := b.sendAndReturn(t, fmt.Sprintf("⏳ <b>Запрос отправлен в Cheburcheck...</b>\n"+
		"Активный пробинг <code>%s</code> со сканеров РФ...\n"+
		"Пожалуйста, подождите 5–20 секунд...", escapeHTML(target)))

	ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
	defer cancel()

	summary, err := chebClient.CheckTarget(ctx, target)
	if err != nil {
		errMsg := fmt.Sprintf("❌ <b>Ошибка Cheburcheck:</b> %s", escapeHTML(err.Error()))
		if sentMsg != nil {
			b.editWithMarkup(t.ChatID, sentMsg.GetMessageID(), errMsg, nil)
		} else {
			b.send(t, errMsg)
		}
		return
	}

	report := checker.FormatCheburcheckReport(summary)
	if sentMsg != nil {
		b.editWithMarkup(t.ChatID, sentMsg.GetMessageID(), report, nil)
	} else {
		b.send(t, report)
	}
}

// handleCheburcheckHost runs the active probe for one host resolved from a
// menu:cheburh:<HostCallbackKey> button: the key is reverse-looked-up against
// the snapshot's unique hosts (same HostCallbackKey round-trip as the toggle
// host route) and the bare host is probed directly — cheburcheck has no port
// concept (UDP protocols included).
func (b *Bot) handleCheburcheckHost(chatID int64, msgID int, hostKey string) {
	seq := b.nextMsgSeq(chatID, msgID)

	var host string
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.Disabled || pm.Address == "" {
				continue
			}
			h, _, splitErr := net.SplitHostPort(pm.Address)
			if splitErr != nil || h == "" {
				h = pm.Address
			}
			if HostCallbackKey(h) == hostKey {
				host = h
				break
			}
		}
	}

	if host == "" {
		b.editWithMarkup(chatID, msgID, "❌ Хост не найден в текущей конфигурации.", BackToMenuMarkup())
		return
	}

	chebClient := b.resolveCheburClient()
	if !chebClient.HasCredentials() {
		b.editWithMarkup(chatID, msgID, "Сканеры Cheburcheck не настроены (DefaultCheburScanners пуст)", BackToMenuMarkup())
		return
	}

	b.editWithMarkup(chatID, msgID, fmt.Sprintf("⏳ <b>Запрос отправлен в Cheburcheck...</b>\n"+
		"Активный пробинг <code>%s</code> со сканеров РФ...\n"+
		"Пожалуйста, подождите 5–20 секунд...", escapeHTML(host)), BackToMenuMarkup())

	ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
	defer cancel()

	summary, err := chebClient.CheckTarget(ctx, host)
	if !b.isMsgSeqValid(chatID, msgID, seq) {
		return
	}
	if err != nil {
		b.editWithMarkup(chatID, msgID, fmt.Sprintf("❌ <b>Ошибка Cheburcheck:</b> %s", escapeHTML(err.Error())), BackToMenuMarkup())
		return
	}

	b.editWithMarkup(chatID, msgID, checker.FormatCheburcheckReport(summary), BackToMenuMarkup())
}
