package telegram

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"xray-checker/checker"
	"xray-checker/metrics"
)

func (b *Bot) getMenuText() string {
	var snapshot []metrics.ProxyMetric
	if b.source != nil {
		snapshot = b.source.MetricsSnapshot()
	}
	cfg := b.GetConfig()
	online := 0
	totalActive := 0
	disabledCount := 0
	var downProxies []string

	var nodes []NodeInfo
	if b.nodeMgr != nil {
		nodes = b.nodeMgr.Nodes()
	}

	localOnline := 0
	localTotal := 0
	type nodeStats struct {
		online int
		total  int
	}
	nodeMap := make(map[string]*nodeStats, len(nodes))
	for _, n := range nodes {
		nodeMap[n.Name] = &nodeStats{}
	}

	for _, pm := range snapshot {
		host, _, err := net.SplitHostPort(pm.Address)
		if err != nil {
			host = pm.Address
		}
		if pm.Disabled || cfg.IsDisabled(host, pm.StableID) {
			disabledCount++
			continue
		}
		totalActive++
		if pm.Online {
			online++
		} else {
			pName := pm.Name
			if pm.NodeName != "" {
				pName = fmt.Sprintf("[%s] %s", pm.NodeName, pm.Name)
			}
			downProxies = append(downProxies, pName)
		}

		if pm.NodeName == "" {
			localTotal++
			if pm.Online {
				localOnline++
			}
		} else if ns, ok := nodeMap[pm.NodeName]; ok {
			ns.total++
			if pm.Online {
				ns.online++
			}
		}
	}

	var downNodes []string
	for _, n := range nodes {
		if !n.Up {
			downNodes = append(downNodes, fmt.Sprintf("🖥 Нода %s: оффлайн", escapeHTML(n.Name)))
		}
	}

	var statusLine string
	if len(nodes) > 0 {
		var sb strings.Builder
		if disabledCount > 0 {
			fmt.Fprintf(&sb, "• Статус прокси: <b>%d/%d онлайн</b> <i>(⏸️ %d отключено)</i>\n", online, totalActive, disabledCount)
		} else {
			fmt.Fprintf(&sb, "• Статус прокси: <b>%d/%d онлайн</b>\n", online, len(snapshot))
		}

		masterASN := b.getMasterASN()
		masterASNStr := ""
		if masterASN != "" {
			masterASNStr = fmt.Sprintf(" (%s)", escapeHTML(masterASN))
		}
		prefix := "├"
		if len(nodes) == 0 {
			prefix = "└"
		}
		fmt.Fprintf(&sb, "  %s 🏠 Мастер: <b>%d/%d онлайн</b>%s\n", prefix, localOnline, localTotal, masterASNStr)

		for i, n := range nodes {
			branch := "├"
			if i == len(nodes)-1 {
				branch = "└"
			}
			ns := nodeMap[n.Name]
			nOnline := 0
			nActive := 0
			if ns != nil {
				// nodeMap counts ACTIVE proxies only: disabled ones `continue`
				// before the counters — do NOT subtract DisabledProxies again
				// (double-subtract produced the impossible «3/2»).
				nOnline = ns.online
				nActive = ns.total
			}
			if nActive == 0 && n.Total > 0 {
				nOnline = n.Online
				nActive = n.Total - n.DisabledProxies
			}

			asnInfo := ""
			if n.ASN != "" {
				asnInfo = fmt.Sprintf(" (%s)", escapeHTML(n.ASN))
			}

			paused := ""
			if n.DisabledProxies > 0 {
				paused = fmt.Sprintf(" · %d⏸", n.DisabledProxies)
			}
			active := nActive
			if !n.Up {
				fmt.Fprintf(&sb, "  %s 🖥 %s: 🔴 <b>оффлайн</b> <i>(потеряна связь)</i>\n", branch, escapeHTML(n.Name))
			} else if active > 0 && nOnline == active {
				fmt.Fprintf(&sb, "  %s 🖥 %s: 🟢 <b>%d/%d онлайн</b>%s%s\n", branch, escapeHTML(n.Name), nOnline, active, paused, asnInfo)
			} else if active > 0 {
				downCount := active - nOnline
				fmt.Fprintf(&sb, "  %s 🖥 %s: ⚠️ <b>%d/%d онлайн</b> <i>(%d сбоит)</i>%s%s\n", branch, escapeHTML(n.Name), nOnline, active, downCount, paused, asnInfo)
			} else if n.DisabledProxies > 0 {
				fmt.Fprintf(&sb, "  %s 🖥 %s: 🟢 <b>на связи</b> <i>(%d⏸)</i>%s\n", branch, escapeHTML(n.Name), n.DisabledProxies, asnInfo)
			} else {
				fmt.Fprintf(&sb, "  %s 🖥 %s: 🟢 <b>на связи</b> <i>(0 прокси)</i>%s\n", branch, escapeHTML(n.Name), asnInfo)
			}
		}
		statusLine = sb.String()
	} else {
		if disabledCount > 0 {
			statusLine = fmt.Sprintf("• Текущий статус: <b>%d/%d онлайн</b> <i>(⏸️ %d отключено)</i>\n", online, totalActive, disabledCount)
		} else {
			statusLine = fmt.Sprintf("• Текущий статус: <b>%d/%d онлайн</b>\n", online, len(snapshot))
		}
	}

	nowStr := b.now().Format("15:04:05 02.01.2006")
	var uptimeStr string
	if avg, ok := b.getAverageUptimePercent(); ok {
		uptimeStr = fmt.Sprintf("• Средний аптайм (за всё время): <b>%.1f%%</b>\n", avg)
	}

	var sb strings.Builder
	sb.WriteString("<b>📊 Сводка Xray Checker</b>\n\n")
	sb.WriteString(statusLine)
	sb.WriteString(fmt.Sprintf("• Время: <b>%s</b>\n", nowStr))
	if uptimeStr != "" {
		sb.WriteString(uptimeStr)
	}

	if len(downProxies) > 0 || len(downNodes) > 0 {
		sb.WriteString("\n<b>🔴 Требуют внимания:</b>\n")
		for _, dn := range downNodes {
			sb.WriteString(fmt.Sprintf("• %s\n", dn))
		}
		limit := 5
		if len(downProxies) < limit {
			limit = len(downProxies)
		}
		for i := 0; i < limit; i++ {
			sb.WriteString(fmt.Sprintf("• %s\n", escapeHTML(downProxies[i])))
		}
		if len(downProxies) > limit {
			sb.WriteString(fmt.Sprintf("<i>...и ещё %d недоступно</i>\n", len(downProxies)-limit))
		}
	} else if totalActive > 0 {
		sb.WriteString("\n🟢 <i>Все активные прокси-хосты доступны и работают стабильно.</i>\n")
	} else {
		sb.WriteString("\nℹ️ <i>Прокси не загружены (ожидание получения подписки или добавьте через /addsub).</i>\n")
	}

	return sb.String()
}

// statusOnlineRow renders one online-proxy row of the /status screen in the
// wave-K legend format: state circle 🟢 (never ✅ — that is step-success) and
// legend latency «⚡N мс».
func statusOnlineRow(name string, latencyMs float64) string {
	return fmt.Sprintf("%s <b>%s</b> — %s\n", renderIcon(IconStateOK), escapeHTML(name), fmtLatencyIcon(int64(latencyMs)))
}

// FormatSoftHint renders a short RU soft-hint phrase for an offline proxy,
// derived from the checker error category (checker/error_taxonomy.go), with a
// fallback to the raw last error message truncated to ~60 runes.
// Escape-once contract (wave M9.4): the returned string is ALWAYS HTML-escaped
// here; callers embed it verbatim and must never re-escape it.
func FormatSoftHint(category int, lastMsg string) string {
	var phrase string
	switch checker.ErrorCategory(category) {
	case checker.CatTimeout:
		phrase = "таймаут соединения"
	case checker.CatDNSError:
		phrase = "ошибка DNS"
	case checker.CatTLSError:
		phrase = "ошибка TLS"
	case checker.CatConnRefused, checker.CatConnReset:
		phrase = "сеть недоступна"
	default:
		msg := []rune(lastMsg)
		if len(msg) == 0 {
			return ""
		}
		if len(msg) > 60 {
			msg = msg[:60]
		}
		return escapeHTML(string(msg))
	}
	return escapeHTML(phrase)
}

// statusDownRow renders one offline-proxy row of the /status screen with a
// single short reason segment («причина: …») from FormatSoftHint — already
// escaped, do NOT re-escape (escape-once contract). No reason segment is
// added when neither a known category nor a message is available.
func statusDownRow(pm metrics.ProxyMetric) string {
	reason := FormatSoftHint(pm.LastErrorCategory, pm.LastErrorMsg)
	if reason == "" {
		return fmt.Sprintf("%s <b>%s</b> — недоступен\n", renderIcon(IconStateDown), escapeHTML(pm.Name))
	}
	return fmt.Sprintf("%s <b>%s</b> — недоступен · <i>причина: %s</i>\n", renderIcon(IconStateDown), escapeHTML(pm.Name), reason)
}

// statusDisabledRow renders one manually disabled proxy row of /status.
func statusDisabledRow(name string) string {
	return fmt.Sprintf("⏸️ <b>%s</b> — отключён\n", escapeHTML(name))
}

func (b *Bot) getStatusText() string {
	if b.source == nil {
		return "Нет данных о прокси-хостах — проверки ещё не выполнялись."
	}
	snapshot := b.source.MetricsSnapshot()
	if len(snapshot) == 0 {
		return "Нет данных о прокси-хостах — проверки ещё не выполнялись."
	}

	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Name < snapshot[j].Name })

	var nodes []NodeInfo
	if b.nodeMgr != nil {
		nodes = b.nodeMgr.Nodes()
	}

	if len(nodes) == 0 {
		online := 0
		activeTotal := 0
		disabledCount := 0
		var activeBody strings.Builder
		var disabledBody strings.Builder

		for _, pm := range snapshot {
			if pm.Disabled {
				disabledCount++
				disabledBody.WriteString(statusDisabledRow(pm.Name))
				continue
			}
			activeTotal++
			if pm.Online {
				online++
				activeBody.WriteString(statusOnlineRow(pm.Name, pm.LatencyMs))
			} else {
				activeBody.WriteString(statusDownRow(pm))
			}
		}

		header := fmt.Sprintf("<b>Статус прокси-хостов: %d/%d онлайн</b>\n\n", online, activeTotal)
		if disabledCount > 0 {
			return header + activeBody.String() + fmt.Sprintf("\n<b>Отключённые прокси-хосты (%d):</b>\n", disabledCount) + disabledBody.String()
		}
		return header + activeBody.String()
	}

	// Grouping by local checker and nodes
	var localProxies []metrics.ProxyMetric
	nodeProxiesMap := make(map[string][]metrics.ProxyMetric)
	var disabledProxies []metrics.ProxyMetric

	online := 0
	activeTotal := 0

	for _, pm := range snapshot {
		if pm.Disabled {
			disabledProxies = append(disabledProxies, pm)
			continue
		}
		activeTotal++
		if pm.Online {
			online++
		}
		if pm.NodeName == "" {
			localProxies = append(localProxies, pm)
		} else {
			nodeProxiesMap[pm.NodeName] = append(nodeProxiesMap[pm.NodeName], pm)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>Статус прокси-хостов: %d/%d онлайн</b>\n\n", online, activeTotal)

	if len(localProxies) > 0 {
		locOnline := 0
		for _, pm := range localProxies {
			if pm.Online {
				locOnline++
			}
		}
		fmt.Fprintf(&sb, "🏠 <b>Локальный чекер · %d/%d онлайн:</b>\n", locOnline, len(localProxies))
		for _, pm := range localProxies {
			if pm.Online {
				sb.WriteString(statusOnlineRow(pm.Name, pm.LatencyMs))
			} else {
				sb.WriteString(statusDownRow(pm))
			}
		}
		sb.WriteString("\n")
	}

	handledNodes := make(map[string]bool)
	for _, n := range nodes {
		handledNodes[n.Name] = true
		pms := nodeProxiesMap[n.Name]
		nOnline := 0
		for _, pm := range pms {
			if pm.Online {
				nOnline++
			}
		}

		asnStr := ""
		if n.ASN != "" {
			asnStr = fmt.Sprintf(" (%s)", escapeHTML(n.ASN))
		}

		if !n.Up {
			fmt.Fprintf(&sb, "🖥 <b>Нода %s</b>%s · 🔴 оффлайн:\n", escapeHTML(n.Name), asnStr)
		} else if len(pms) == 0 {
			fmt.Fprintf(&sb, "🖥 <b>Нода %s</b>%s · 🟢 на связи <i>(нет прокси)</i>:\n", escapeHTML(n.Name), asnStr)
		} else if nOnline == len(pms) {
			fmt.Fprintf(&sb, "🖥 <b>Нода %s</b>%s · 🟢 %d/%d онлайн:\n", escapeHTML(n.Name), asnStr, nOnline, len(pms))
		} else {
			fmt.Fprintf(&sb, "🖥 <b>Нода %s</b>%s · ⚠️ %d/%d онлайн:\n", escapeHTML(n.Name), asnStr, nOnline, len(pms))
		}

		if len(pms) == 0 {
			sb.WriteString("<i>• Нет данных о проверенных прокси</i>\n\n")
		} else {
			for _, pm := range pms {
				if pm.Online {
					sb.WriteString(statusOnlineRow(pm.Name, pm.LatencyMs))
				} else {
					sb.WriteString(statusDownRow(pm))
				}
			}
			sb.WriteString("\n")
		}
	}

	// Any node proxies from nodes not currently in registry
	for nodeName, pms := range nodeProxiesMap {
		if handledNodes[nodeName] {
			continue
		}
		nOnline := 0
		for _, pm := range pms {
			if pm.Online {
				nOnline++
			}
		}
		badge := "🟢"
		if nOnline < len(pms) {
			badge = "⚠️"
		}
		fmt.Fprintf(&sb, "🖥 <b>Нода %s</b> · %s %d/%d онлайн:\n", escapeHTML(nodeName), badge, nOnline, len(pms))
		for _, pm := range pms {
			if pm.Online {
				sb.WriteString(statusOnlineRow(pm.Name, pm.LatencyMs))
			} else {
				sb.WriteString(statusDownRow(pm))
			}
		}
		sb.WriteString("\n")
	}

	if len(disabledProxies) > 0 {
		fmt.Fprintf(&sb, "<b>Отключённые прокси-хосты (%d):</b>\n", len(disabledProxies))
		for _, pm := range disabledProxies {
			name := pm.Name
			if pm.NodeName != "" {
				name = fmt.Sprintf("[%s] %s", pm.NodeName, pm.Name)
			}
			sb.WriteString(statusDisabledRow(name))
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}
