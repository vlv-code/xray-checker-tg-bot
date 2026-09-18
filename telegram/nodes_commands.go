package telegram

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

var validNodeName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

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

// GenerateNodeToken creates a secure 32-byte (64 hex characters) cryptographic token.
func GenerateNodeToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (b *Bot) getMasterReportURL() string {
	cfg := b.GetConfig()
	if cfg.MasterPublicURL != "" {
		u := strings.TrimRight(cfg.MasterPublicURL, "/")
		if !strings.HasSuffix(u, "/api/v1/nodes/report") {
			u += "/api/v1/nodes/report"
		}
		return u
	}
	if b.diagSource != nil {
		if ip, err := b.diagSource.GetCurrentIP(); err == nil && ip != "" {
			return fmt.Sprintf("http://%s:2112/api/v1/nodes/report", ip)
		}
	}
	return "http://<IP_МАСТЕРА>:2112/api/v1/nodes/report"
}

func (b *Bot) handleNodeAdd(msg *telego.Message, rawName string) {
	name := strings.TrimSpace(rawName)
	if name == "" {
		b.waitingNodeAddMu.Lock()
		if b.waitingNodeAdd == nil {
			b.waitingNodeAdd = make(map[int64]bool)
		}
		b.waitingNodeAdd[msg.Chat.ID] = true
		b.waitingNodeAddMu.Unlock()
		b.replyCommand(msg, "➕ <b>Подключение новой ноды</b>\n\n"+
			"Введите имя для новой ноды (например: <code>germany-1</code>, <code>vps-nl</code>, <code>finland-node</code>):\n\n"+
			"<i>Допустимы латинские буквы, цифры, дефис и подчёркивание (до 32 символов).</i>")
		return
	}

	if !validNodeName.MatchString(name) {
		b.replyCommand(msg, "❌ Недопустимое имя ноды. Используйте только латиницу, цифры, дефис и подчёркивание (от 1 до 32 символов).")
		return
	}

	if b.nodeMgr == nil {
		b.replyCommand(msg, "❌ Модуль управления нодами недоступен на этом сервере.")
		return
	}

	token := GenerateNodeToken()
	if err := b.nodeMgr.AddNode(name, token); err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}

	reportURL := b.getMasterReportURL()
	text := fmt.Sprintf("✅ <b>Нода %s успешно зарегистрирована!</b>\n\n"+
		"🔑 <b>Токен ноды:</b>\n<code>%s</code>\n\n"+
		"🌐 <b>URL для отправки отчётов:</b>\n<code>%s</code>\n\n"+
		"📋 <b>Способ 1: через Docker Compose (рекомендуется):</b>\n"+
		"<pre>services:\n"+
		"  xray-node-%s:\n"+
		"    build: https://github.com/vlv-code/xray-checker-tg-bot.git#main\n"+
		"    container_name: xray-node-%s\n"+
		"    restart: unless-stopped\n"+
		"    environment:\n"+
		"      - REPORT_URL=%s\n"+
		"      - REPORT_TOKEN=%s</pre>\n\n"+
		"📦 <b>Способ 2: через Docker run (однострочник):</b>\n"+
		"<pre>docker run -d --name xray-node-%s \\\n"+
		"  --restart unless-stopped \\\n"+
		"  -e REPORT_URL=%s \\\n"+
		"  -e REPORT_TOKEN=%s \\\n"+
		"  ghcr.io/vlv-code/xray-checker-tg-bot:latest</pre>\n"+
		"<i>(Если образ ещё не скачан: <code>docker build -t xray-checker-node https://github.com/vlv-code/xray-checker-tg-bot.git#main</code>)</i>\n\n"+
		"<i>После запуска на удалённом сервере нода автоматически подключится к мастеру и появится в списке.</i>",
		escapeHTML(name), token, reportURL,
		escapeHTML(name), escapeHTML(name), reportURL, token,
		escapeHTML(name), reportURL, token)

	markup := tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn("🩺 Проверить связь", "menu:nodes:health"),
			btn("📋 Подписки нод", "menu:nodes:subs"),
		),
		tu.InlineKeyboardRow(
			btn("🔙 К списку нод", "menu:nodes"),
			btn("🏠 Главное меню", "menu:main"),
		),
	)

	t := targetFromMessage(msg)
	b.sendOrUpdateMenu(t, text, markup)
}

func (b *Bot) handleNodeDel(msg *telego.Message, rawName string) {
	name := strings.TrimSpace(rawName)
	if name == "" {
		b.replyCommand(msg, "Использование: /nodedel &lt;имя_ноды&gt;")
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "Ноды не настроены.")
		return
	}
	if err := b.nodeMgr.RemoveNode(name); err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Нода <b>%s</b> удалена с мастера.", escapeHTML(name)))
}

func (b *Bot) handleNodeAddSubURL(msg *telego.Message, node string, rawURL string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		b.replyCommand(msg, "❌ Пустой URL подписки.")
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "❌ Модуль управления нодами недоступен на этом сервере.")
		return
	}
	if err := b.nodeMgr.AddSub(node, rawURL); err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	text := fmt.Sprintf("✅ <b>Подписка успешно назначена ноде <code>%s</code>!</b>\n\n"+
		"URL: <code>%s</code>\n\n"+
		"Нода применит её и начнёт проверку прокси при следующем отчёте.",
		escapeHTML(node), escapeHTML(rawURL))
	markup := tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			btn(fmt.Sprintf("📋 К подпискам %s", node), fmt.Sprintf("menu:nodes:subnode:%s", node)),
		),
		tu.InlineKeyboardRow(
			btn("🔙 К списку нод", "menu:nodes:subs"),
			btn("⚙️ Настройки нод", "menu:nodes:settings"),
		),
	)
	t := targetFromMessage(msg)
	b.sendOrUpdateMenu(t, text, markup)
}
