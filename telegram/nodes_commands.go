package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"
)

// commandArgs returns every whitespace-separated argument after the command
// (commandArg returns only the first).
func commandArgs(text string) []string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return nil
	}
	return fields[1:]
}

// formatAge renders a duration as a compact human age: 12s, 3m12s, 2h05m.
func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func (b *Bot) replyNodes(t ChatTarget) {
	if b.nodeMgr == nil {
		b.sendOrUpdateMenu(t, "Ноды не настроены (список NODES на мастере пуст).", BackToSettingsMarkup())
		return
	}
	list := b.nodeMgr.Nodes()
	var sb strings.Builder
	fmt.Fprintf(&sb, "🖥 <b>Ноды (%d)</b>\n", len(list))
	now := b.now()
	for _, n := range list {
		icon := "🔴"
		if n.Up {
			icon = "🟢"
		}
		fmt.Fprintf(&sb, "%s <b>%s</b>", icon, escapeHTML(n.Name))
		if n.ASN != "" {
			sb.WriteString(" · " + escapeHTML(n.ASN))
		}
		if !n.EverReported {
			sb.WriteString(" · отчётов ещё не было")
		} else {
			fmt.Fprintf(&sb, " · %d/%d онлайн · отчёт %s назад",
				n.Online, n.Total, formatAge(now.Sub(n.LastReport)))
			if n.Version != "" {
				sb.WriteString(" · v" + escapeHTML(n.Version))
			}
		}
		sb.WriteString("\n")
	}
	b.sendOrUpdateMenu(t, sb.String(), BackToSettingsMarkup())
}

func (b *Bot) replyNodeSubs(msg *telego.Message) {
	args := commandArgs(msg.Text)
	if len(args) != 1 {
		b.replyCommand(msg, "Использование: /nodesubs &lt;имя ноды&gt;")
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "Ноды не настроены.")
		return
	}
	subs, err := b.nodeMgr.ManagedSubs(args[0])
	if err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📡 <b>Подписки ноды %s (%d)</b>\n", escapeHTML(args[0]), len(subs))
	for _, s := range subs {
		if s.ProxyCount >= 0 {
			fmt.Fprintf(&sb, "• %s — %d прокси\n", escapeHTML(s.URL), s.ProxyCount)
		} else {
			fmt.Fprintf(&sb, "• %s — нет данных (нода ещё не применяла)\n", escapeHTML(s.URL))
		}
	}
	b.replyCommand(msg, sb.String())
}

func (b *Bot) handleNodeAddSub(msg *telego.Message) {
	args := commandArgs(msg.Text)
	if len(args) != 2 {
		b.replyCommand(msg, "Использование: /nodeaddsub &lt;имя ноды&gt; &lt;URL подписки&gt;")
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "Ноды не настроены.")
		return
	}
	if err := b.nodeMgr.AddSub(args[0], args[1]); err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Подписка добавлена для ноды <b>%s</b>.\nНода применит её при следующем отчёте.", escapeHTML(args[0])))
}

func (b *Bot) handleNodeDelSub(msg *telego.Message) {
	args := commandArgs(msg.Text)
	if len(args) != 2 {
		b.replyCommand(msg, "Использование: /nodedelsub &lt;имя ноды&gt; &lt;URL подписки&gt;")
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "Ноды не настроены.")
		return
	}
	if err := b.nodeMgr.RemoveSub(args[0], args[1]); err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Подписка удалена для ноды <b>%s</b>.\nНода уберёт её при следующем отчёте.", escapeHTML(args[0])))
}
