package telegram

import (
	"context"
	"errors"
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
		"Активный пробинг <code>%s</code>...\n"+
		"Пожалуйста, подождите до 30 секунд...", escapeHTML(target)))

	// Wave I F1: 30s budget — the 49-scanner real jobs exceed the old 20s,
	// so the stream was cut before done and results were lost mid-flight.
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
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

	// Wave K-B3: the placeholder AND the result keep the way back to the
	// browsed host-list page («📡 К списку хостов» → menu:cheburpg:<page>).
	resultMarkup := CheburResultMarkup(b.lastCheburPage(chatID))

	b.editWithMarkup(chatID, msgID, fmt.Sprintf("⏳ <b>Запрос отправлен в Cheburcheck...</b>\n"+
		"Активный пробинг <code>%s</code>...\n"+
		"Пожалуйста, подождите до 30 секунд...", escapeHTML(host)), resultMarkup)

	// Wave I F1: 30s budget (matches the manual command path — full streams).
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	defer cancel()

	summary, err := chebClient.CheckTarget(ctx, host)
	if !b.isMsgSeqValid(chatID, msgID, seq) {
		return
	}
	if err != nil {
		b.editWithMarkup(chatID, msgID, fmt.Sprintf("❌ <b>Ошибка Cheburcheck:</b> %s", escapeHTML(err.Error())), resultMarkup)
		return
	}

	b.editWithMarkup(chatID, msgID, checker.FormatCheburcheckReport(summary), resultMarkup)
}

// stripCheburPort strips a trailing ":port" from a host:port pair (wave H4:
// Cheburcheck probes bare hosts and has no port concept). The port must be
// numeric — net.SplitHostPort alone would happily split "https://host/path"
// into host "https". Anything else is returned trimmed as-is and
// validateCheburHostInput decides its fate.
func stripCheburPort(raw string) string {
	raw = strings.TrimSpace(raw)
	if h, p, err := net.SplitHostPort(raw); err == nil && h != "" && isCheburNumericPort(p) {
		return h
	}
	return raw
}

// isCheburNumericPort reports whether the port part looks like a TCP port
// (digits only, 1-5 chars).
func isCheburNumericPort(p string) bool {
	if p == "" || len(p) > 5 {
		return false
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validateCheburHostInput rejects manual Cheburcheck input that cannot be a
// probe target (wave H4, RU errors): empty input, URLs/schemes/paths,
// whitespace and multi-colon garbage. A bare host (domain or IP) passes.
func validateCheburHostInput(raw, host string) error {
	if host == "" {
		return errors.New("Пустой ввод. Отправьте хост (домен или IP) или host:port, например example.com")
	}
	if strings.ContainsAny(host, " \t") || strings.Contains(host, "/") || strings.Contains(host, "://") {
		return errors.New("Не похоже на хост. Отправьте домен или IP без схемы и пути, например example.com или 1.2.3.4")
	}
	// A host:port pair must split cleanly; multi-colon IPv6 without brackets
	// and other colon garbage are rejected.
	if strings.Count(raw, ":") > 1 {
		if _, _, err := net.SplitHostPort(raw); err != nil {
			return errors.New("Не удалось разобрать адрес. Отправьте хост или host:port, например example.com или 1.2.3.4:443")
		}
	}
	return nil
}

// handleCheburHostInput consumes the manual host input armed by the
// «✍️ Проверить свой хост» button (wave H4, route menu:cheburin): the armed
// pendingInput FSM entry (wave L-1, admin-gated in handleMessage) delivers
// the next plain text here; host[:port] is validated (port stripped), then
// one-off CheckTarget → FormatCheburcheckReport reply.
func (b *Bot) handleCheburHostInput(msg *telego.Message, raw string) {
	t := targetFromMessage(msg)

	host := stripCheburPort(raw)
	if err := validateCheburHostInput(raw, host); err != nil {
		b.send(t, "❌ "+err.Error())
		return
	}

	chebClient := b.resolveCheburClient()
	if !chebClient.HasCredentials() {
		b.send(t, "Сканеры Cheburcheck не настроены (DefaultCheburScanners пуст)")
		return
	}

	sentMsg, _ := b.sendAndReturn(t, fmt.Sprintf("⏳ <b>Запрос отправлен в Cheburcheck...</b>\n"+
		"Активный пробинг <code>%s</code>...\n"+
		"Пожалуйста, подождите до 30 секунд...", escapeHTML(host)))

	// Wave I F1: 30s budget (matches the manual command path — full streams).
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	defer cancel()

	summary, err := chebClient.CheckTarget(ctx, host)
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
