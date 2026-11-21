package telegram

import (
	"fmt"
	"strings"
)

func (b *Bot) getSubsText() string {
	if b.subs == nil {
		return "Управление подписками отключено."
	}
	static := b.subs.Static()
	dynamic := b.subs.Dynamic()
	if len(static) == 0 && len(dynamic) == 0 {
		return "Нет активных подписок."
	}

	var sb strings.Builder
	sb.WriteString("<b>📋 Список подписок:</b>\n\n")

	renderSub := func(icon, u string) {
		fmt.Fprintf(&sb, "%s <code>%s</code>\n", icon, escapeHTML(u))
		if sf, ok := b.GetSubFreshness(u); ok && !sf.LastUpdate.IsZero() {
			ago := formatTimeAgo(b.now().Sub(sf.LastUpdate))
			if sf.Added == 0 && sf.Removed == 0 {
				fmt.Fprintf(&sb, "   <i>%s · %d прокси (без изменений)</i>\n", ago, sf.Count)
			} else {
				fmt.Fprintf(&sb, "   <i>%s · %d прокси (было %d, +%d, -%d)</i>\n", ago, sf.Count, sf.PrevCount, sf.Added, sf.Removed)
			}
		}
	}

	for _, u := range static {
		renderSub("🔒", u)
	}
	for _, u := range dynamic {
		renderSub("➕", u)
	}
	sb.WriteString("\n🔒 — из окружения / флагов, ➕ — добавлена через /addsub")
	sb.WriteString("\n\n➕ /addsub &lt;URL&gt; — добавить · /delsub &lt;URL&gt; — удалить")
	return sb.String()
}

func (b *Bot) buildAddSubReport(count int) string {
	var onlineCount, offlineCount int
	var offlineNames []string
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.Disabled {
				continue
			}
			if pm.Online {
				onlineCount++
			} else {
				offlineCount++
				offlineNames = append(offlineNames, pm.Name)
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ <b>Подписка добавлена.</b> Прокси-хостов: %d\n", count))
	sb.WriteString(fmt.Sprintf("🟢 Доступно: %d\n", onlineCount))
	if offlineCount > 0 {
		sb.WriteString(fmt.Sprintf("🔴 Недоступно: %d\n", offlineCount))
		limit := 10
		if len(offlineNames) < limit {
			limit = len(offlineNames)
		}
		for i := 0; i < limit; i++ {
			sb.WriteString(fmt.Sprintf("  • <code>%s</code>\n", escapeHTML(offlineNames[i])))
		}
		if len(offlineNames) > limit {
			sb.WriteString(fmt.Sprintf("  … и ещё %d прокси-хост(а/ов)\n", len(offlineNames)-limit))
		}
	}
	return sb.String()
}
