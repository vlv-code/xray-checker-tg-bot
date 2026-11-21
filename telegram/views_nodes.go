package telegram

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
)

func (b *Bot) getNodesMainView() string {
	if b.nodeMgr == nil {
		return "🖥 <b>Ноды (удалённые чекеры)</b>\n\n" +
			"Ноды не настроены на мастере.\n\n" +
			"Ноды позволяют распределённо проверять доступность прокси из разных локаций (РФ, Европа и др.).\n\n" +
			"Нажмите «➕ Подключить ноду» или отправьте команду <code>/nodeadd &lt;имя&gt;</code>."
	}
	list := b.nodeMgr.Nodes()
	onlineCount := 0
	for _, n := range list {
		if n.Up {
			onlineCount++
		}
	}
	var sb strings.Builder
	sb.WriteString("🖥 <b>Ноды (удалённые чекеры)</b>\n\n")
	fmt.Fprintf(&sb, "• Сконфигурировано нод: <b>%d</b>\n", len(list))
	fmt.Fprintf(&sb, "• На связи: <b>%d</b> | Оффлайн: <b>%d</b>\n\n", onlineCount, len(list)-onlineCount)
	sb.WriteString("Ноды выполняют независимые сетевые проверки ваших подписок из удалённых точек и передают данные в общий мониторинг.\n\n" +
		"Выберите действие ниже:")
	return sb.String()
}

func (b *Bot) getNodesInstallGuideView() string {
	reportURL := b.getMasterReportURL()
	return "📥 <b>Инструкция по установке ноды</b>\n\n" +
		"Нода разворачивается на любом внешнем VPS сервере или ПК в Docker.\n\n" +
		"<b>1. Быстрое подключение из бота:</b>\n" +
		"Нажмите <b>«➕ Подключить ноду»</b> или отправьте <code>/nodeadd &lt;имя_ноды&gt;</code> — бот сгенерирует готовый токен и готовую команду запуска с подставленными параметрами.\n\n" +
		"<b>2. Запуск через Docker run:</b>\n" +
		"<pre>docker run -d --name xray-node \\\n" +
		"  --restart unless-stopped \\\n" +
		"  -e REPORT_URL=" + reportURL + " \\\n" +
		"  -e REPORT_TOKEN=&lt;ТОКЕН_НОДЫ&gt; \\\n" +
		"  ghcr.io/vlv-code/xray-checker-tg-bot:latest</pre>\n\n" +
		"<b>3. Синхронизация настроек:</b>\n" +
		"После первого отчёта нода автоматически получит назначенные подписки и параметры проверки прямо от мастера."
}

func (b *Bot) getNodesHealthView() string {
	if b.nodeMgr == nil {
		return "🩺 <b>Проверка работоспособности и связи</b>\n\nНоды не настроены."
	}
	list := b.nodeMgr.Nodes()
	if len(list) == 0 {
		return "🩺 <b>Проверка работоспособности и связи</b>\n\nСписок нод пуст. Нажмите «➕ Подключить ноду» для добавления."
	}
	var sb strings.Builder
	sb.WriteString("🩺 <b>Проверка связи и статуса нод</b>\n\n")
	now := b.now()
	for _, n := range list {
		icon := "🔴"
		statusText := "оффлайн"
		if n.Up {
			icon = "🟢"
			statusText = "на связи"
		}
		fmt.Fprintf(&sb, "%s <b>%s</b> — %s\n", icon, escapeHTML(n.Name), statusText)
		if n.HostIP != "" {
			asnStr := ""
			if n.ASN != "" {
				asnStr = fmt.Sprintf(" (%s)", escapeHTML(n.ASN))
			}
			fmt.Fprintf(&sb, "  • IP: <code>%s</code>%s\n", escapeHTML(n.HostIP), asnStr)
		}
		if !n.EverReported {
			sb.WriteString("  • Отчётов от ноды ещё не поступало\n")
		} else {
			fmt.Fprintf(&sb, "  • Прокси: <b>%d / %d онлайн</b>\n", n.Online, n.Total)
			fmt.Fprintf(&sb, "  • Последний отчёт: %s назад (интервал %ds)\n", fmtAgeIconDyn(now.Sub(n.LastReport)), n.IntervalSec)
			if n.Version != "" {
				fmt.Fprintf(&sb, "  • Версия: v%s\n", escapeHTML(n.Version))
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (b *Bot) getNodesSettingsView() string {
	cfg := b.GetConfig()
	syncStr := "✅ Включена"
	if !cfg.NodeSyncEnabled {
		syncStr = "❌ Отключена"
	}
	alertsStr := "🔔 Включены"
	if !cfg.NodeAlertsEnabled {
		alertsStr = "🔕 Отключены"
	}
	proxyAlertsStr := "📢 В чат и журнал"
	if !cfg.NodeProxyAlertsChat {
		proxyAlertsStr = "📝 Только в журнал инцидентов"
	}

	return fmt.Sprintf("⚙️ <b>Глобальные настройки нод</b>\n\n"+
		"• Передача настроек нодам: <b>%s</b>\n"+
		"• Алерты доступности нод: <b>%s</b>\n"+
		"• Алерты по прокси от нод: <b>%s</b>\n"+
		"• Порог молчания ноды: <b>%d сек</b>\n\n"+
		"<i>При включённой передаче ноды получают интервал проверок, целевые серверы, списки выключенных хостов и режим алертов прямо от бота.</i>\n\n"+
		"<b>Команды управления нодами:</b>\n"+
		"• <code>/nodeadd &lt;имя_ноды&gt;</code> — подключить новую ноду\n"+
		"• <code>/nodedel &lt;имя_ноды&gt;</code> — удалить ноду с мастера\n"+
		"• <code>/nodesubs &lt;имя_ноды&gt;</code> — просмотр подписок ноды\n"+
		"• <code>/nodeaddsub &lt;имя_ноды&gt; &lt;url&gt;</code> — назначить подписку ноде\n"+
		"• <code>/nodedelsub &lt;имя_ноды&gt; &lt;url&gt;</code> — удалить подписку у ноды",
		syncStr, alertsStr, proxyAlertsStr, cfg.NodeStaleTimeoutSec)
}

// getNodeSubsManageView renders one node's subscription manage screen — the
// ONLY subs surface since Wave H2 (decision D4 removed the global
// «Подписки нод» screen; entry is the per-node cfg screen). Unknown-node and
// no-manager errors fall back to the node-cfg picker markup.
func (b *Bot) getNodeSubsManageView(nodeName string) (string, *telego.InlineKeyboardMarkup) {
	if b.nodeMgr == nil {
		return "Ноды не настроены.", NodesCfgListMarkup(nil)
	}
	subs, err := b.nodeMgr.ManagedSubs(nodeName)
	if err != nil {
		return "❌ " + escapeHTML(err.Error()), NodesCfgListMarkup(nil)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "📋 <b>Подписки ноды:</b> <code>%s</code>\n\n", escapeHTML(nodeName))

	if len(subs) == 0 {
		sb.WriteString("<i>Этой ноде пока не назначено ни одной подписки.</i>\nНода ожидает конфигурацию или проводит проверку с 0 прокси.\n\nНажмите <b>«➕ Назначить подписку»</b> ниже, чтобы привязать URL подписки.")
	} else {
		fmt.Fprintf(&sb, "Назначено подписок: <b>%d</b>\n\n", len(subs))
		for i, s := range subs {
			fmt.Fprintf(&sb, "<b>#%d:</b> <code>%s</code>\n", i+1, escapeHTML(s.URL))
			if s.ProxyCount >= 0 {
				fmt.Fprintf(&sb, "   • Прокси: <b>%d</b> (по отчёту ноды)\n\n", s.ProxyCount)
			} else {
				sb.WriteString("   • Прокси: <i>нет данных (нода ещё не применяла)</i>\n\n")
			}
		}
		sb.WriteString("Чтобы удалить подписку, нажмите <b>«🗑 Удалить: #N»</b> ниже.")
	}

	return sb.String(), NodeSubsManageMarkup(nodeName, subs)
}
