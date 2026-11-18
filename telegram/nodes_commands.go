package telegram

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

var validNodeName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

// reservedNodeNames lists node names that collide with static Telegram
// diagnostic routes: a node named like one of these tokens would be
// indistinguishable from the built-in diag targets when parsed in
// callbacks.go (menu:diag:details:<name>, menu:diag:refresh:<name>,
// target == "local", etc.). Wave-F B6.
var reservedNodeNames = map[string]bool{
	"local":   true,
	"rich":    true,
	"summary": true,
	"details": true,
}

// isReservedNodeName reports whether the node name is reserved because it
// collides with static Telegram diag routes (Wave-F B6).
func isReservedNodeName(name string) bool {
	return reservedNodeNames[name]
}

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
	fmt.Fprintf(&sb, "%s <b>Ноды (%d)</b>\n", IconNode.Render(false), len(list))
	now := b.now()
	for _, n := range list {
		icon := IconStateDown.Render(false)
		if n.Up {
			icon = IconStateOK.Render(false)
		}
		fmt.Fprintf(&sb, "%s <b>%s</b>", icon, escapeHTML(n.Name))
		if n.Disabled {
			sb.WriteString(" · " + IconDisabled.Render(false) + " отключена")
		}
		if n.ASN != "" {
			sb.WriteString(" · " + escapeHTML(n.ASN))
		}
		if !n.EverReported {
			sb.WriteString(" · отчётов ещё не было")
		} else {
			if badge := nodeHealthBadge(n.Online, n.Total, n.DisabledProxies); badge != "" {
				sb.WriteString(" · " + badge)
			}
			// Wave K-B2: the report-age column uses the legend ⏱️ fmtAgeIcon
			// («отчёт ⏱️ 2 мин») instead of the compact latin formatAge.
			fmt.Fprintf(&sb, " · отчёт %s", fmtAgeIcon(now.Sub(n.LastReport)))
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
	if !isAllowedSubURL(args[1]) {
		b.replyCommand(msg, "❌ Разрешены только URL подписок со схемой http:// или https://")
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
		// Wave L-1 FSM: /nodeadd without a name re-arms the per-chat pending
		// input — the next plain text is consumed as the node name
		// (admin-gated in handleMessage; /nodeadd is a mutating command).
		b.armPending(msg.Chat.ID, pendingKindNodeAdd, "")
		b.replyCommand(msg, "➕ <b>Подключение новой ноды</b>\n\n"+
			"Введите имя для новой ноды (например: <code>germany-1</code>, <code>vps-nl</code>, <code>finland-node</code>):\n\n"+
			"<i>Допустимы латинские буквы, цифры, дефис и подчёркивание (до 32 символов).</i>")
		return
	}

	if !validNodeName.MatchString(name) {
		b.replyCommand(msg, "❌ Недопустимое имя ноды. Используйте только латиницу, цифры, дефис и подчёркивание (от 1 до 32 символов).")
		return
	}

	if isReservedNodeName(name) {
		b.replyCommand(msg, fmt.Sprintf("❌ имя ноды %q зарезервировано — выберите другое.", escapeHTML(name)))
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
	text := buildNodeRegisteredText(name, token, reportURL)

	// Wave H2 (decision D4): the «📋 Подписки нод» entry to the removed
	// global subs screen is gone — node subscriptions are managed via the
	// per-node cfg screen (menu:nodes:cfg:<n> → «📋 Подписки ноды»).
	// Wave K-B2: the values moved into copy-text rows (nodeAddCopyMarkup)
	// and the passive status re-render is honestly labeled «🔄 Обновить».
	markup := nodeAddCopyMarkup(name, token, reportURL)

	t := targetFromMessage(msg)
	b.sendOrUpdateMenu(t, text, markup)
}

// copyTextLimit is Telegram's hard copy-text button cap (1-256 characters).
const copyTextLimit = 256

// shortPreview truncates s to max runes appending an ellipsis when cut — the
// value short form embedded in copy-button labels (Wave K-B2: labels include
// the value's short form so the operator verifies what will be copied).
func shortPreview(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// reportURLPreview strips the scheme and path of a report URL for the copy
// button label («1.2.3.4:2112…»).
func reportURLPreview(raw string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i] + "…"
	}
	return s
}

// copyTextButton returns an inline button that copies value to the clipboard;
// ok=false when the value is empty or exceeds Telegram's 256-character
// copy-text cap — the button is then omitted (the value stays visible in the
// message text), same omit-on-overflow discipline as cbNodeCfg.
func copyTextButton(label, value string) (telego.InlineKeyboardButton, bool) {
	if value == "" || utf8.RuneCountInString(value) > copyTextLimit {
		return telego.InlineKeyboardButton{}, false
	}
	return telego.InlineKeyboardButton{Text: label}.WithCopyText(&telego.CopyTextButton{Text: value}), true
}

// nodeAddDockerCommand composes the docker run one-liner for a freshly
// registered node (the former «Способ 2» <pre> block, now a copy payload).
func nodeAddDockerCommand(name, reportURL, token string) string {
	return "docker run -d --name xray-node-" + name +
		" --restart unless-stopped" +
		" -e REPORT_URL=" + reportURL +
		" -e REPORT_TOKEN=" + token +
		" ghcr.io/vlv-code/xray-checker-tg-bot:latest"
}

// nodeAddCommandCopyParts splits a docker run one-liner into paste-ordered
// copy-text parts within the 256-character cap: one part when it fits,
// otherwise word-boundary continuation parts (each ends with « \», the shell
// continues the command; labels carry «часть N/M»). Returns nil when even a
// single token exceeds the cap (copy rows omitted; the hint row points to
// manual setup) — Wave K-B2.
func nodeAddCommandCopyParts(cmd string) []string {
	reserve := copyTextLimit - 2 // room for the trailing « \» on split parts
	if utf8.RuneCountInString(cmd) <= copyTextLimit {
		return []string{cmd}
	}
	var parts []string
	cur := ""
	for _, w := range strings.Split(cmd, " ") {
		switch {
		case cur == "":
			if utf8.RuneCountInString(w) > reserve {
				return nil
			}
			cur = w
		case utf8.RuneCountInString(cur)+utf8.RuneCountInString(w)+1 <= reserve:
			cur += " " + w
		default:
			parts = append(parts, cur+" \\")
			if utf8.RuneCountInString(w) > reserve {
				return nil
			}
			cur = w
		}
	}
	return append(parts, cur)
}

// nodeAddCopyMarkup composes the /nodeadd success keyboard (Wave K-B2):
// copy-text rows for the token, the report URL and the docker run command —
// payloads are RAW values (buttons are not HTML; escaped payloads would copy
// broken text) with short-form previews in the labels — then the passive
// status re-render row and navigation. The one-liner that exceeds the
// 256-character cap is split into paste-ordered parts; rows that still
// cannot fit degrade to a hint row instead of a dead button.
func nodeAddCopyMarkup(name, token, reportURL string) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	if b, ok := copyTextButton(IconCopy.Render(false)+" Токен ноды · "+shortPreview(token, 8), token); ok {
		rows = append(rows, tu.InlineKeyboardRow(b))
	}
	if b, ok := copyTextButton(IconCopy.Render(false)+" URL отчёта · "+reportURLPreview(reportURL), reportURL); ok {
		rows = append(rows, tu.InlineKeyboardRow(b))
	}
	cmdLabel := IconCopy.Render(false) + " Команда запуска · xray-node-" + name
	if parts := nodeAddCommandCopyParts(nodeAddDockerCommand(name, reportURL, token)); len(parts) == 1 {
		if b, ok := copyTextButton(cmdLabel, parts[0]); ok {
			rows = append(rows, tu.InlineKeyboardRow(b))
		}
	} else if len(parts) > 1 {
		for i, p := range parts {
			label := fmt.Sprintf("%s · часть %d/%d", cmdLabel, i+1, len(parts))
			if b, ok := copyTextButton(label, p); ok {
				rows = append(rows, tu.InlineKeyboardRow(b))
			}
		}
	} else {
		rows = append(rows, tu.InlineKeyboardRow(
			btn("ℹ️ Команда не поместилась в кнопку — задайте REPORT_URL и REPORT_TOKEN по инструкции", "menu:noop"),
		))
	}
	rows = append(rows,
		tu.InlineKeyboardRow(
			btn("🔄 Обновить", "menu:nodes:health"),
		),
		tu.InlineKeyboardRow(
			btn(IconBack.Render(false)+" К списку нод", "menu:nodes"),
			btn("🏠 Главное меню", "menu:main"),
		),
	)
	return tu.InlineKeyboard(rows...)
}

// buildNodeRegisteredText renders the one-time /nodeadd success reply — the
// compact Wave K-B2 form: the values (token, report URL, docker run command)
// moved into the inline copy-text buttons (nodeAddCopyMarkup), so the giant
// <pre> dump is gone; only the short instruction and the verification <code>
// lines remain. The generated token and the operator-configured report URL
// (REPORT_URL / MASTER_PUBLIC_URL) are escaped for Telegram HTML: a raw & in
// a URL query string would break delivery. Copy payloads stay RAW.
func buildNodeRegisteredText(name, token, reportURL string) string {
	copyIcon := IconCopy.Render(false)
	return fmt.Sprintf("%s <b>Нода %s успешно зарегистрирована!</b>\n\n"+
		"🔑 Токен: <code>%s</code>\n"+
		"🌐 URL для отправки отчётов: <code>%s</code>\n\n"+
		"📦 <b>Запуск на сервере ноды (Docker):</b>\n"+
		"1. Нажмите «%s Токен ноды», «%s URL отчёта» и «%s Команда запуска» ниже — значения скопируются в буфер обмена.\n"+
		"2. Вставьте и выполните команду на ноде (compose-вариант: docker-compose.node.yml — см. README).\n\n"+
		"<i>После запуска нода автоматически подключится к мастеру и появится в списке.</i>",
		IconStepSuccess.Render(false), escapeHTML(name), escapeHTML(token), escapeHTML(reportURL),
		copyIcon, copyIcon, copyIcon)
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

// handleNodeRename implements /noderename <old> <new> (Wave G5a).
func (b *Bot) handleNodeRenameCommand(msg *telego.Message) {
	args := commandArgs(msg.Text)
	if len(args) != 2 {
		b.replyCommand(msg, "Использование: /noderename &lt;старое имя&gt; &lt;новое имя&gt;")
		return
	}
	b.renameNodeAction(msg, args[0], args[1])
}

// handleNodeRenameInput applies the waiting-state text input from the node
// cfg screen («✏️ Переименовать»): the message text is the new name.
func (b *Bot) handleNodeRenameInput(msg *telego.Message, oldName string) {
	b.renameNodeAction(msg, oldName, strings.TrimSpace(msg.Text))
}

// renameNodeAction validates the new name (pattern + reserved, mirroring
// /nodeadd) and renames through nodeMgr — the only rename writer. The token
// keeps working, subscriptions and settings follow, so no re-registration on
// the node side is needed.
func (b *Bot) renameNodeAction(msg *telego.Message, oldName, newName string) {
	newName = strings.TrimSpace(newName)
	if !validNodeName.MatchString(newName) {
		b.replyCommand(msg, "❌ Недопустимое имя ноды. Используйте только латиницу, цифры, дефис и подчёркивание (от 1 до 32 символов).")
		return
	}
	if isReservedNodeName(newName) {
		b.replyCommand(msg, fmt.Sprintf("❌ имя ноды %q зарезервировано — выберите другое.", escapeHTML(newName)))
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "❌ Модуль управления нодами недоступен на этом сервере.")
		return
	}
	if err := b.nodeMgr.RenameNode(oldName, newName); err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Нода <b>%s</b> переименована в <b>%s</b>.\n\n"+
		"Токен, подписки и настройки ноды перенесены — отчёты продолжат приниматься без перенастройки ноды.",
		escapeHTML(oldName), escapeHTML(newName)))
}

func (b *Bot) handleNodeAddSubURL(msg *telego.Message, node string, rawURL string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		b.replyCommand(msg, "❌ Пустой URL подписки.")
		return
	}
	if !isAllowedSubURL(rawURL) {
		b.replyCommand(msg, "❌ Разрешены только URL подписок со схемой http:// или https://")
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
	// Wave H2 (decision D4): the back button targets the per-node subs manage
	// screen (the only subs surface now); composed via cbNodeCfg — omitted
	// when the node name would overflow the 64-byte callback limit.
	var rows [][]telego.InlineKeyboardButton
	if cb, ok := cbNodeCfg("menu:nodes:subnode:", node); ok {
		rows = append(rows, tu.InlineKeyboardRow(
			btn(fmt.Sprintf("📋 К подпискам %s", node), cb),
		))
	}
	rows = append(rows, tu.InlineKeyboardRow(
		btn("⚙️ Глобальные настройки нод", "menu:nodes:settings"),
	))
	markup := tu.InlineKeyboard(rows...)
	t := targetFromMessage(msg)
	b.sendOrUpdateMenu(t, text, markup)
}

// nodeSettingsHelp lists the overridable setting keys for /nodeset.
const nodeSettingsHelp = "Ключи: check_interval, check_method, ip_check_url, status_check_url, " +
	"download_url, proxy_timeout, download_timeout, download_min_size, check_concurrency, " +
	"subs_update_interval, target_urls"

func (b *Bot) replyNodeSettings(msg *telego.Message) {
	args := commandArgs(msg.Text)
	if b.nodeMgr == nil {
		b.replyCommand(msg, "Ноды не настроены.")
		return
	}
	switch len(args) {
	case 0:
		b.replyCommand(msg, "Использование:\n/nodeset &lt;имя ноды&gt; — показать настройки\n"+
			"/nodeset &lt;имя&gt; &lt;ключ&gt; &lt;значение&gt; — переопределить\n\n"+nodeSettingsHelp)
	case 1:
		entries, err := b.nodeMgr.NodeSettingsView(args[0])
		if err != nil {
			b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "⚙️ <b>Настройки ноды %s</b>\n", escapeHTML(args[0]))
		for _, e := range entries {
			mark := ""
			if e.Overridden {
				mark = " ✏️"
			}
			fmt.Fprintf(&sb, "• %s: <code>%s</code>%s\n", e.Key, escapeHTML(e.Value), mark)
		}
		sb.WriteString("\n✏️ — переопределено на ноде; /nodereset возвращает к мастеру.")
		b.replyCommand(msg, sb.String())
	case 2:
		b.replyCommand(msg, "Не хватает значения: /nodeset &lt;имя&gt; &lt;ключ&gt; &lt;значение&gt;")
	default:
		value := strings.Join(args[2:], " ")
		if err := b.nodeMgr.SetNodeSetting(args[0], args[1], value); err != nil {
			b.replyCommand(msg, "❌ "+escapeHTML(err.Error())+"\n\n"+nodeSettingsHelp)
			return
		}
		b.replyCommand(msg, fmt.Sprintf("✅ Нода <b>%s</b>: %s = %s.\nПрименится при следующем отчёте ноды.",
			escapeHTML(args[0]), escapeHTML(args[1]), escapeHTML(value)))
	}
}

func (b *Bot) handleNodeReset(msg *telego.Message) {
	args := commandArgs(msg.Text)
	if len(args) < 1 {
		b.replyCommand(msg, "Использование: /nodereset &lt;имя ноды&gt; [ключ]")
		return
	}
	if b.nodeMgr == nil {
		b.replyCommand(msg, "Ноды не настроены.")
		return
	}
	key := ""
	if len(args) >= 2 {
		key = args[1]
	}
	if err := b.nodeMgr.ResetNodeSetting(args[0], key); err != nil {
		b.replyCommand(msg, "❌ "+escapeHTML(err.Error()))
		return
	}
	if key == "" {
		b.replyCommand(msg, fmt.Sprintf("✅ Все настройки ноды <b>%s</b> возвращены к мастеру.\nПрименится при следующем отчёте ноды.", escapeHTML(args[0])))
		return
	}
	b.replyCommand(msg, fmt.Sprintf("✅ Нода <b>%s</b>: %s снова наследуется от мастера.\nПрименится при следующем отчёте ноды.", escapeHTML(args[0]), escapeHTML(key)))
}
