package telegram

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/mymmrac/telego"

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

func (b *Bot) getSettingsText() string {
	cfg := b.GetConfig()
	modeName := "🔄 Live (редактирование)"
	if cfg.AlertMode == AlertModeClean {
		modeName = "🧹 Чистый чат (автоочистка)"
	}

	quietStatus := "выключен"
	if cfg.QuietHoursEnabled {
		quietStatus = fmt.Sprintf("активен (%s–%s)", cfg.QuietHoursStart, cfg.QuietHoursEnd)
	}
	if cfg.QuietSnoozeUntil > time.Now().Unix() {
		rem := time.Duration(cfg.QuietSnoozeUntil-time.Now().Unix()) * time.Second
		quietStatus = fmt.Sprintf("пауза ещё %s", FormatDowntime(rem))
	}

	intervalSec := b.getIntervalSec()
	var intervalStr string
	if intervalSec < 60 {
		intervalStr = fmt.Sprintf("%d сек.", intervalSec)
	} else {
		intervalStr = FormatDowntime(time.Duration(intervalSec) * time.Second)
	}

	disabledCount := 0
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.Disabled {
				disabledCount++
			}
		}
	} else {
		disabledCount = len(cfg.DisabledProxies)
	}
	if b.diagSource != nil {
		disabledMap := make(map[string]bool, len(cfg.DisabledHosts))
		for _, h := range cfg.DisabledHosts {
			disabledMap[strings.ToLower(h)] = true
		}
		for _, h := range b.diagSource.GetUniqueHosts() {
			if disabledMap[strings.ToLower(h)] {
				disabledCount++
			}
		}
	} else {
		disabledCount += len(cfg.DisabledHosts)
	}

	chBgStatus := "выключен"
	if cfg.CheckHostBgEnabled {
		chBgStatus = fmt.Sprintf("каждые %d ч.", cfg.CheckHostIntervalHours)
		if cfg.CheckHostAlertEnabled {
			chBgStatus += " (алерты по РФ: вкл)"
		} else {
			chBgStatus += " (алерты по РФ: выкл)"
		}
	}

	return fmt.Sprintf("<b>⚙️ Настройки Xray Checker</b>\n\n"+
		"• Интервал проверок: <b>%s</b>\n"+
		"• Фоновый Check-Host: <b>%s</b>\n"+
		"• Режим алертов: <b>%s</b>\n"+
		"• Тихий режим: <b>%s</b>\n"+
		"• Отключено (хосты/прокси): <b>%d</b>\n\n"+
		"Выберите раздел настроек с помощью кнопок ниже:",
		intervalStr, chBgStatus, modeName, quietStatus, disabledCount)
}

func (b *Bot) getProxyNameByStableID(stableID string) string {
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.StableID == stableID {
				if pm.Name != "" {
					return pm.Name
				}
				return pm.Address
			}
		}
	}
	return stableID
}

func (b *Bot) getDisabledProxiesView(page int) (string, *telego.InlineKeyboardMarkup) {
	var snapshot []metrics.ProxyMetric
	if b.source != nil {
		snapshot = b.source.MetricsSnapshot()
	}
	cfg := b.GetConfig()

	var items []ProxyToggleItem
	disabledCount := 0
	for _, pm := range snapshot {
		host, _, err := net.SplitHostPort(pm.Address)
		if err != nil {
			host = pm.Address
		}
		isDisabled := pm.Disabled || cfg.IsDisabled(host, pm.StableID)
		if isDisabled {
			disabledCount++
		}
		items = append(items, ProxyToggleItem{
			StableID: pm.StableID,
			Name:     pm.Name,
			Protocol: pm.Protocol,
			Address:  pm.Address,
			Disabled: isDisabled,
		})
	}

	pageSize := menuListPageSize
	totalItems := len(items)
	totalPages := (totalItems + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if end > totalItems {
		end = totalItems
	}

	var pageItems []ProxyToggleItem
	if start < totalItems {
		pageItems = items[start:end]
	}

	var sb strings.Builder
	sb.WriteString("<b>🚫 Управление прокси-хостами</b>\n\n")
	sb.WriteString("Нажмите на прокси-хост, чтобы включить или отключить его проверку.\n")
	sb.WriteString("Отключённые прокси-хосты не проверяются, не вызывают алертов и сразу исключаются из сводки.\n\n")
	sb.WriteString(fmt.Sprintf("Всего прокси-хостов: <b>%d</b> | Отключено: <b>%d</b>\n", totalItems, disabledCount))

	markup := DisabledProxiesMarkup(pageItems, page, totalPages)
	return sb.String(), markup
}

func (b *Bot) getDisabledHostsView(page int) (string, *telego.InlineKeyboardMarkup) {
	var allHosts []string
	if b.diagSource != nil {
		allHosts = b.diagSource.GetUniqueHosts()
	}
	cfg := b.GetConfig()
	disabledMap := make(map[string]bool)
	for _, h := range cfg.DisabledHosts {
		disabledMap[strings.ToLower(h)] = true
	}

	pageSize := menuListPageSize
	totalHosts := len(allHosts)
	totalPages := (totalHosts + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if end > totalHosts {
		end = totalHosts
	}

	var pageHosts []string
	if start < totalHosts {
		pageHosts = allHosts[start:end]
	}

	var sb strings.Builder
	sb.WriteString("<b>🚫 Управление проверками хостов</b>\n\n")
	sb.WriteString("Нажмите на хост, чтобы включить или отключить его проверку во всех подписках.\n")
	sb.WriteString("Отключённые хосты не пингуются, не вызывают алертов и не влияют на статус.\n\n")
	sb.WriteString(fmt.Sprintf("Всего обнаружено хостов: <b>%d</b> | Отключено: <b>%d</b>\n", totalHosts, len(cfg.DisabledHosts)))

	markup := DisabledHostsMarkup(pageHosts, disabledMap, page, totalPages)
	return sb.String(), markup
}

func (b *Bot) getIntervalText() string {
	sec := b.getIntervalSec()
	var durStr string
	if sec < 60 {
		durStr = fmt.Sprintf("%d сек.", sec)
	} else {
		durStr = FormatDowntime(time.Duration(sec) * time.Second)
	}
	return fmt.Sprintf("<b>⏱️ Интервал проверок прокси-хостов</b>\n\n"+
		"• Текущий интервал: <b>%s</b>\n\n"+
		"Выберите готовый пресет или отправьте команду с произвольным числом секунд:\n"+
		"<code>/interval &lt;секунды&gt;</code> (например, <code>/interval 45</code>)",
		durStr)
}

func (b *Bot) getAverageUptimePercent() (float64, bool) {
	if b.statsStore == nil || b.source == nil {
		return 0.0, false
	}
	snapshot := b.source.MetricsSnapshot()
	var totalUptime float64
	var count int
	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		if u, ok := b.statsStore.GetUptimePercent(pm.StableID); ok {
			totalUptime += u
			count++
		}
	}
	if count == 0 {
		return 0.0, false
	}
	return totalUptime / float64(count), true
}

// statusOnlineRow renders one online-proxy row of the /status screen in the
// wave-K legend format: state circle 🟢 (never ✅ — that is step-success) and
// legend latency «⚡N мс».
func statusOnlineRow(name string, latencyMs float64) string {
	return fmt.Sprintf("%s <b>%s</b> — %s\n", renderIcon(IconStateOK), escapeHTML(name), fmtLatencyIcon(int64(latencyMs)))
}

// statusDownRow renders one offline-proxy row of the /status screen.
func statusDownRow(name string) string {
	return fmt.Sprintf("%s <b>%s</b> — недоступен\n", renderIcon(IconStateDown), escapeHTML(name))
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
				activeBody.WriteString(statusDownRow(pm.Name))
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
				sb.WriteString(statusDownRow(pm.Name))
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
					sb.WriteString(statusDownRow(pm.Name))
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
				sb.WriteString(statusDownRow(pm.Name))
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

func (b *Bot) getStatsOverviewText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}

	var snapshot []metrics.ProxyMetric
	if b.source != nil {
		snapshot = b.source.MetricsSnapshot()
	}
	totalProxies := len(snapshot)
	now := b.now()

	activeIDs := make([]string, 0, totalProxies)
	for _, pm := range snapshot {
		if !pm.Disabled {
			activeIDs = append(activeIDs, pm.StableID)
		}
	}
	var uptime24h float64 = 100.0
	var avg7d float64 = 100.0
	if b.statsStore != nil && b.statsStore.GetRollingStats() != nil {
		uptime24h = b.statsStore.GetRollingStats().UptimePercent24hAll(now, activeIDs)
		var total7d float64
		for _, id := range activeIDs {
			total7d += b.statsStore.GetRollingStats().UptimePercent7d(id, now)
		}
		if len(activeIDs) > 0 {
			avg7d = total7d / float64(len(activeIDs))
		}
	}

	var sb strings.Builder
	sb.WriteString("<b>📈 Статистика аптайма</b>\n\n")
	fmt.Fprintf(&sb, "• Прокси-хостов в мониторинге: <b>%d</b>\n", totalProxies)
	var allTimeStr string
	if avgAll, ok := b.getAverageUptimePercent(); ok {
		allTimeStr = fmt.Sprintf(", за всё время: %.1f%%", avgAll)
	}
	fmt.Fprintf(&sb, "• Средний аптайм: <b>24ч: %.1f%%</b> (7д: %.1f%%%s)\n", uptime24h, avg7d, allTimeStr)

	incidents := b.statsStore.GetRecentIncidents(1)
	if len(incidents) > 0 {
		inc := incidents[0]
		downTime := time.Unix(inc.DownAt, 0).In(b.loc()).Format("15:04 02.01")
		durText := "ещё не восстановлен"
		if inc.UpAt > 0 {
			durText = fmtAgeIconDyn(time.Duration(inc.DurationSec) * time.Second)
		}
		fmt.Fprintf(&sb, "• Последний инцидент: <i>%s: %s (простой: %s)</i>\n", escapeHTML(inc.ProxyName), downTime, durText)
	}

	// Top unstable (flapping / drops in 24h)
	type proxyFlap struct {
		name      string
		flaps     int
		uptime24h float64
	}
	var flapsList []proxyFlap
	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		flaps := b.statsStore.GetFlapCount24h(pm.StableID, now)
		if flaps > 0 {
			u := 100.0
			if b.statsStore.GetRollingStats() != nil {
				u = b.statsStore.GetRollingStats().UptimePercent24h(pm.StableID, now)
			}
			flapsList = append(flapsList, proxyFlap{
				name:      pm.Name,
				flaps:     flaps,
				uptime24h: u,
			})
		}
	}
	sort.Slice(flapsList, func(i, j int) bool {
		return flapsList[i].flaps > flapsList[j].flaps
	})

	if len(flapsList) > 0 {
		sb.WriteString("\n🔝 <b>Топ нестабильных (переходов за 24ч):</b>\n")
		showCount := 3
		if len(flapsList) < showCount {
			showCount = len(flapsList)
		}
		for i := 0; i < showCount; i++ {
			f := flapsList[i]
			fmt.Fprintf(&sb, "  %d. %s — %d переходов, %.1f%% аптайм\n", i+1, escapeHTML(f.name), f.flaps, f.uptime24h)
		}
	}

	// Proxies list (up to 15) with full names and clear metrics
	if totalProxies > 0 {
		sb.WriteString("\n<b>📊 Мониторинг прокси-хостов:</b>\n")

		sortedSnap := make([]metrics.ProxyMetric, len(snapshot))
		copy(sortedSnap, snapshot)
		sort.Slice(sortedSnap, func(i, j int) bool {
			flapsI := b.statsStore.GetFlapCount24h(sortedSnap[i].StableID, now)
			flapsJ := b.statsStore.GetFlapCount24h(sortedSnap[j].StableID, now)
			if flapsI != flapsJ {
				return flapsI > flapsJ
			}
			return sortedSnap[i].Name < sortedSnap[j].Name
		})

		maxRows := 15
		if len(sortedSnap) < maxRows {
			maxRows = len(sortedSnap)
		}
		for i := 0; i < maxRows; i++ {
			pm := sortedSnap[i]
			var u24, u7d float64 = 100.0, 100.0
			if b.statsStore.GetRollingStats() != nil {
				u24 = b.statsStore.GetRollingStats().UptimePercent24h(pm.StableID, now)
				u7d = b.statsStore.GetRollingStats().UptimePercent7d(pm.StableID, now)
			}
			ls := b.statsStore.GetLatencySamples(pm.StableID)
			p50 := ls.Percentile(0.50)
			p95 := ls.Percentile(0.95)
			jitter := ls.StdDev()
			flaps := b.statsStore.GetFlapCount24h(pm.StableID, now)

			p50Str := fmtLatencyUnit(int64(p50))
			p95Str := fmtLatencyUnit(int64(p95))
			if ls.Count() < 5 {
				p50Str = "-"
				p95Str = "-"
			}
			flapWarning := ""
			if flaps > 10 {
				flapWarning = " ⚠️"
			}
			protoStr := ""
			if pm.Protocol != "" {
				protoStr = fmt.Sprintf(" <i>(%s)</i>", escapeHTML(strings.ToUpper(pm.Protocol)))
			}
			fmt.Fprintf(&sb, "• <b>%s</b>%s%s\n  24ч: %.1f%% | 7д: %.1f%% | P50: %s | P95: %s | Σ: %.0f | ПАД.: %d\n",
				escapeHTML(pm.Name), protoStr, flapWarning, u24, u7d, p50Str, p95Str, jitter, flaps)
		}
		if len(sortedSnap) > maxRows {
			fmt.Fprintf(&sb, "<i>... и ещё %d прокси-хостов</i>\n", len(sortedSnap)-maxRows)
		}
	}

	sb.WriteString("\n<b>ℹ️ Справка по метрикам</b> (нажмите, чтобы развернуть):\n" +
		"<blockquote expandable>" +
		"• <b>24ч / 7д</b>: скользящий аптайм за последние 24 часа и 7 дней (% успешных проверок).\n" +
		"• <b>P50 / P95</b>: медиана и 95-й перцентиль сетевой задержки (RTT пинга) в миллисекундах.\n" +
		"• <b>Σ (Джиттер)</b>: среднеквадратичное отклонение латенси (вариативность задержки).\n" +
		"• <b>ПАД.</b>: число переходов статуса (онлайн ↔ оффлайн / флаппинг) за 24 часа.</blockquote>\n")

	sb.WriteString("\nВыберите раздел ниже:")
	return sb.String()
}

func (b *Bot) getProtocolsStatsText() string {
	if b.source == nil {
		return "📈 <b>Статистика по протоколам</b>\n\nНет данных о прокси-хостах."
	}
	snapshot := b.source.MetricsSnapshot()
	if len(snapshot) == 0 {
		return "📈 <b>Статистика по протоколам</b>\n\nНет данных о прокси-хостах."
	}

	type groupData struct {
		name    string
		proxies []metrics.ProxyMetric
	}

	groupsMap := make(map[string]*groupData)
	var groupOrder []string

	for _, pm := range snapshot {
		key := formatProtocolTransport(pm.Protocol, pm.Transport, pm.Security)
		gd, ok := groupsMap[key]
		if !ok {
			gd = &groupData{name: key}
			groupsMap[key] = gd
			groupOrder = append(groupOrder, key)
		}
		gd.proxies = append(gd.proxies, pm)
	}

	sort.Slice(groupOrder, func(i, j int) bool {
		cntI := len(groupsMap[groupOrder[i]].proxies)
		cntJ := len(groupsMap[groupOrder[j]].proxies)
		if cntI != cntJ {
			return cntI > cntJ
		}
		return groupOrder[i] < groupOrder[j]
	})

	now := b.now()
	var sb strings.Builder
	sb.WriteString("📈 <b>Статистика по протоколам</b>\n\n")

	for _, key := range groupOrder {
		gd := groupsMap[key]
		n := len(gd.proxies)

		if n < 3 {
			fmt.Fprintf(&sb, "<b>%s</b> (n=%d)\n  ⚠️ Слишком мало данных (n<3) для надёжной статистики\n\n", escapeHTML(gd.name), n)
			continue
		}

		var totalUptime float64
		var samples []float64

		for _, p := range gd.proxies {
			if b.statsStore != nil && b.statsStore.GetRollingStats() != nil {
				totalUptime += b.statsStore.GetRollingStats().UptimePercent24h(p.StableID, now)
				ls := b.statsStore.GetLatencySamples(p.StableID)
				if ls.Count() > 0 {
					ls.mu.Lock()
					samples = append(samples, ls.samples[:ls.count]...)
					ls.mu.Unlock()
				} else if p.LatencyMs > 0 {
					samples = append(samples, p.LatencyMs)
				}
			} else {
				if p.Online {
					totalUptime += 100.0
				}
				if p.LatencyMs > 0 {
					samples = append(samples, p.LatencyMs)
				}
			}
		}

		avgUptime := totalUptime / float64(n)
		var p50, p95 float64
		if len(samples) > 0 {
			sort.Float64s(samples)
			p50 = samples[int(float64(len(samples)-1)*0.5)]
			p95 = samples[int(float64(len(samples)-1)*0.95)]
		}

		fmt.Fprintf(&sb, "<b>%s</b> (n=%d)\n  Аптайм 24ч: %.1f%%   Латенси p50: %s, p95: %s\n\n",
			escapeHTML(gd.name), n, avgUptime, fmtLatencyUnit(int64(p50)), fmtLatencyUnit(int64(p95)))
	}

	sb.WriteString("<b>ℹ️ Справка</b> (нажмите, чтобы развернуть):\n" +
		"<blockquote expandable>" +
		"• <b>(n=X)</b>: число серверов с данной связкой протокола и транспорта (при n < 3 данные ориентировочные).\n" +
		"• <b>Аптайм 24ч</b>: средневзвешенная доступность протокольной группы.\n" +
		"• <b>Латенси P50 / P95</b>: медиана и 95-й перцентиль задержки пакетов для протокола.</blockquote>")

	return sb.String()
}

func (b *Bot) getHeatmapText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}
	now := b.now()
	loc := b.loc()
	matrix, peakHour, maxDrops := b.statsStore.GetHeatmap7d(now, loc)

	var sb strings.Builder
	sb.WriteString("🌡️ <b>Карта падений за 7 дней</b>\n\n<pre>")
	sb.WriteString("       Пн  Вт  Ср  Чт  Пт  Сб  Вс\n")
	for h := 0; h < 24; h++ {
		fmt.Fprintf(&sb, "%02dч  ", h)
		for d := 0; d < 7; d++ {
			fmt.Fprintf(&sb, "%4d", matrix[h][d])
		}
		if h == peakHour && maxDrops > 0 {
			sb.WriteString("   ← пик")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("</pre>")

	if maxDrops > 0 {
		fmt.Fprintf(&sb, "\nПик падений: %02dч (%d сбоев, вероятно перегрузка канала)\n\n", peakHour, maxDrops)
	} else {
		sb.WriteString("\nЗа последние 7 дней падений не зафиксировано.\n\n")
	}

	sb.WriteString("<b>ℹ️ Справка</b> (нажмите, чтобы развернуть):\n" +
		"<blockquote expandable>" +
		"• <b>Сетка 24ч × 7д</b>: строки — часы суток (00–23) по вашему часовому поясу, столбцы — дни недели (Пн–Вс).\n" +
		"• <b>Числа в ячейках</b>: суммарное количество сбоев за конкретный час.\n" +
		"• <b>Пик</b>: час суток с наибольшим числом отказов (помогает выявить часы перегрузок или блокировок).</blockquote>")

	return sb.String()
}

func parseIncidentSource(inc Incident) (badge string, displayName string, origin string) {
	if strings.HasPrefix(inc.StableID, "node:") || strings.HasPrefix(inc.ProxyName, "🖥 [Нода]") {
		nodeName := strings.TrimPrefix(inc.StableID, "node:")
		if nodeName == "" || nodeName == inc.StableID {
			parts := strings.Split(inc.ProxyName, "] ")
			if len(parts) > 1 {
				nodeName = strings.TrimSpace(parts[1])
			}
		}
		return "🔌 [Связь]", inc.ProxyName, nodeName
	}
	if strings.HasPrefix(inc.ProxyName, "[") {
		idx := strings.Index(inc.ProxyName, "]")
		if idx > 1 {
			nodeName := inc.ProxyName[1:idx]
			name := strings.TrimSpace(inc.ProxyName[idx+1:])
			return fmt.Sprintf("🖥 [%s]", nodeName), name, nodeName
		}
	}
	return "🏠 [Мастер]", inc.ProxyName, "local"
}

func (b *Bot) getIncidentsText() string {
	return b.getIncidentsTextFiltered("")
}

// incidentsPageSize caps the incidents journal per screen (wave K-B1:
// pagination replaces the old hard [:15] cap — the paginated variant
// getIncidentsTextPage flips through the full 30-incident window).
const incidentsPageSize = 15

// incidentsFilterHeader renders the shared «(🏠 Мастер)» / «(🖥 Нода)»
// filter suffix for the journal title.
func incidentsFilterHeader(filterNode string) string {
	if filterNode == "local" {
		return " (🏠 Мастер)"
	}
	if filterNode != "" && filterNode != "all" {
		return fmt.Sprintf(" (🖥 %s)", escapeHTML(filterNode))
	}
	return ""
}

// incidentsSpravkaHTML renders the journal help wall as ONE expandable
// blockquote (single wording shared by the empty and list branches).
func incidentsSpravkaHTML() string {
	return "<b>ℹ️ Справка</b> (нажмите, чтобы развернуть):\n" +
		"<blockquote expandable>" +
		"• <b>Сбой</b>: точное время фиксации отказа в часовом поясе бота.\n" +
		"• <b>Простой</b>: суммарная длительность недоступности до момента восстановления (или текущее время аварии).\n" +
		"• <b>Причина</b>: сетевая ошибка при тестировании (DNS failure, TCP connect timeout, TLS handshake, HTTP 204 failure).</blockquote>"
}

// renderIncidentRow formats one journal row: local time of the outage,
// downtime through the dual-layer age icon, escaped name/reason.
func (b *Bot) renderIncidentRow(inc Incident) string {
	badge, name, _ := parseIncidentSource(inc)
	downTime := time.Unix(inc.DownAt, 0).In(b.loc()).Format("15:04 02.01")
	if inc.UpAt == 0 {
		return fmt.Sprintf("🔴 %s <b>%s</b> — сбой %s (<i>сейчас недоступен</i>)\nПричина: %s\n\n",
			badge, escapeHTML(name), downTime, escapeHTML(inc.Reason))
	}
	return fmt.Sprintf("🟡 %s <b>%s</b> — %s (простой: %s)\nПричина: %s\n\n",
		badge, escapeHTML(name), downTime, fmtAgeIconDyn(time.Duration(inc.DurationSec)*time.Second), escapeHTML(inc.Reason))
}

// filterIncidentsByNode returns the journal window filtered by origin node
// ("" / "all" disable filtering), newest first.
func (b *Bot) filterIncidentsByNode(filterNode string) []Incident {
	allIncidents := b.statsStore.GetRecentIncidents(30)
	var filtered []Incident
	for _, inc := range allIncidents {
		_, _, origin := parseIncidentSource(inc)
		if filterNode == "" || filterNode == "all" || origin == filterNode {
			filtered = append(filtered, inc)
		}
	}
	return filtered
}

func (b *Bot) getIncidentsTextFiltered(filterNode string) string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}
	filtered := b.filterIncidentsByNode(filterNode)
	if len(filtered) > incidentsPageSize {
		filtered = filtered[:incidentsPageSize]
	}

	if len(filtered) == 0 {
		return fmt.Sprintf("<b>📋 Журнал инцидентов%s</b>\n\nЗафиксированных инцидентов нет — все прокси-хосты работают стабильно!\n\n",
			incidentsFilterHeader(filterNode)) + incidentsSpravkaHTML()
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>📋 Последние инциденты%s:</b>\n\n", incidentsFilterHeader(filterNode)))

	for _, inc := range filtered {
		sb.WriteString(b.renderIncidentRow(inc))
	}

	sb.WriteString(incidentsSpravkaHTML())
	return sb.String()
}

// getIncidentsTextPage returns one page of the incident journal for the
// given node filter plus the total page count (wave K-B1 pagination view;
// wired into the callback dispatch by the menus-cluster owner).
func (b *Bot) getIncidentsTextPage(filterNode string, page int) (string, int) {
	if b.statsStore == nil {
		return "Статистика недоступна.", 1
	}
	filtered := b.filterIncidentsByNode(filterNode)
	totalPages := (len(filtered) + incidentsPageSize - 1) / incidentsPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	if len(filtered) == 0 {
		return fmt.Sprintf("<b>📋 Журнал инцидентов%s</b>\n\nЗафиксированных инцидентов нет — все прокси-хосты работают стабильно!\n\n",
			incidentsFilterHeader(filterNode)) + incidentsSpravkaHTML(), totalPages
	}

	start := (page - 1) * incidentsPageSize
	end := start + incidentsPageSize
	if end > len(filtered) {
		end = len(filtered)
	}

	var sb strings.Builder
	pageNote := ""
	if totalPages > 1 {
		pageNote = fmt.Sprintf(" — стр. %d из %d", page, totalPages)
	}
	sb.WriteString(fmt.Sprintf("<b>📋 Последние инциденты%s%s:</b>\n\n", incidentsFilterHeader(filterNode), pageNote))

	for _, inc := range filtered[start:end] {
		sb.WriteString(b.renderIncidentRow(inc))
	}

	sb.WriteString(incidentsSpravkaHTML())
	return sb.String(), totalPages
}

func (b *Bot) getTopProblematicText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}
	var activeIDs map[string]bool
	if b.source != nil {
		activeIDs = make(map[string]bool)
		for _, pm := range b.source.MetricsSnapshot() {
			if !pm.Disabled {
				activeIDs[pm.StableID] = true
			}
		}
	}
	top := b.statsStore.GetTopProblematicActive(10, activeIDs)
	if len(top) == 0 {
		return "Нет данных для отображения."
	}

	var sb strings.Builder
	sb.WriteString("<b>🔝 Топ по инцидентам (прокси-хосты):</b>\n\n")
	now := b.now()
	for i, p := range top {
		incStats := b.statsStore.GetIncidentStats(p.StableID, 24*time.Hour, now)
		mtbfStr := ""
		if incStats.MTBF > 0 {
			mtbfStr = fmt.Sprintf(", MTBF: %s", fmtAgeIconDyn(incStats.MTBF))
		}
		mttrStr := ""
		if incStats.MTTR > 0 {
			mttrStr = fmt.Sprintf(", MTTR: %s", fmtAgeIconDyn(incStats.MTTR))
		}
		uptimeStr := "—"
		if p.HasData {
			uptimeStr = fmt.Sprintf("%.1f%%", p.UptimePct)
		}
		fmt.Fprintf(&sb, "%d. <b>%s</b>: инцидентов: %d, аптайм: %s%s%s, суммарный простой: %s\n",
			i+1, escapeHTML(p.ProxyName), p.DropCount, uptimeStr, mtbfStr, mttrStr, fmtAgeIconDyn(time.Duration(p.DowntimeSec)*time.Second))
	}

	sb.WriteString("\n<b>ℹ️ Справка</b> (нажмите, чтобы развернуть):\n" +
		"<blockquote expandable>" +
		"• <b>Инцидентов</b>: количество зафиксированных падений хоста за период.\n" +
		"• <b>Аптайм</b>: процент доступности хоста за последние 24 часа.\n" +
		"• <b>MTBF</b>: среднее время безотказной работы между инцидентами (Mean Time Between Failures).\n" +
		"• <b>MTTR</b>: среднее время восстановления после аварии (Mean Time To Recovery).\n" +
		"• <b>Суммарный простой</b>: общее накопленное время недоступности прокси.</blockquote>")

	return sb.String()
}

func (b *Bot) getQuietHoursText() string {
	cfg := b.GetConfig()
	status := "Отключен"
	if cfg.QuietHoursEnabled {
		status = fmt.Sprintf("Включен (%s – %s)", cfg.QuietHoursStart, cfg.QuietHoursEnd)
	}

	snoozeStatus := "Нет"
	now := time.Now().Unix()
	if cfg.QuietSnoozeUntil > now {
		rem := time.Duration(cfg.QuietSnoozeUntil-now) * time.Second
		snoozeStatus = fmt.Sprintf("Активен (осталось %s)", FormatDowntime(rem))
	}

	tzName := cfg.Timezone
	if tzName == "" {
		tzName = "Local"
	}

	return fmt.Sprintf("<b>🌙 Тихий режим</b>\n\n"+
		"В тихом режиме звуковые алерты о сбоях не приходят в чат, а копятся для утренней сводки.\n\n"+
		"• Расписание сна: <b>%s</b>\n"+
		"• Ручная пауза: <b>%s</b>\n"+
		"• Часовой пояс: <b>%s</b>\n\n"+
		"Управляйте режимом с помощью кнопок:", status, snoozeStatus, escapeHTML(tzName))
}

func (b *Bot) getAlertModeText() string {
	cfg := b.GetConfig()
	current := "🔄 Live (редактирование)"
	if cfg.AlertMode == AlertModeClean {
		current = "🧹 Чистый чат (автоочистка)"
	}

	return fmt.Sprintf("<b>⚙️ Режим уведомлений о сбоях</b>\n\n"+
		"Текущий режим: <b>%s</b>\n\n"+
		"• <b>Live-режим</b>: сообщение о сбое не удаляется, а при восстановлении обновляется на статус «Восстановлен» с указанием времени простоя.\n"+
		"• <b>Чистый чат</b>: сообщение о сбое удаляется сразу при восстановлении, а подтверждение восстановления исчезает через 2 минуты, оставляя чат чистым.", current)
}

func (b *Bot) getTargetsText() string {
	targets := []string{
		"https://cp.cloudflare.com/generate_204",
		"https://www.gstatic.com/generate_204",
	}
	if b.diagSource != nil {
		if tm := b.diagSource.GetTargetManager(); tm != nil {
			configured := tm.GetTargets()
			if len(configured) > 0 {
				targets = configured
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("<b>🎯 Целевые серверы проверки прокси-хостов:</b>\n\n")
	for i, t := range targets {
		fmt.Fprintf(&sb, "%d. <code>%s</code>\n", i+1, escapeHTML(t))
	}
	sb.WriteString("\nЧекер проверяет доступность прокси-хостов по целевым серверам. Если хотя бы один ответил успехом, прокси-хост считается доступным.")
	return sb.String()
}

func (b *Bot) getTimezoneText() string {
	cfg := b.GetConfig()
	loc := b.loc()
	now := time.Now().In(loc)
	tzName := cfg.Timezone
	if tzName == "" {
		tzName = "Local"
	}

	zoneName, offset := now.Zone()
	offsetHours := offset / 3600
	offsetSign := "+"
	if offsetHours < 0 {
		offsetSign = "-"
		offsetHours = -offsetHours
	}

	return fmt.Sprintf(
		"🕒 <b>Настройка часового пояса</b>\n\n"+
			"• Текущий пояс: <b>%s</b> (%s, UTC%s%d)\n"+
			"• Локальное время бота: <b>%s</b>\n\n"+
			"Часовой пояс применяется для:\n"+
			"• Расписания тихого режима\n"+
			"• Дневных и утренних сводок\n"+
			"• Времени фиксации инцидентов в журнале\n"+
			"• Всех уведомлений бота\n\n"+
			"Выберите часовой пояс или задайте командой <code>/tz &lt;IANA&gt;</code>:",
		escapeHTML(tzName),
		escapeHTML(zoneName),
		offsetSign,
		offsetHours,
		now.Format("15:04:05 02.01.2006"),
	)
}

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

// --- Wave K-B1 pagination (content-cluster local pager) ---

// statusPageSize caps the /status proxy rows per screen. The menu-cluster
// helpers (menuPaginationRow etc.) live in menu.go, owned by another wave-K
// worker, so views.go carries its own pager below — same visual pattern
// («⬅️ · Стр. N/M · ➡️»), zero compile-time coupling.
const statusPageSize = 15

// pagerRouteNoop is the already-wired no-op callback used for unavailable
// pager directions (placeholder «·»).
const pagerRouteNoop = "menu:noop"

// maxCallbackDataBytes is the Telegram limit for callback_data; a pager
// button that cannot fit degrades to the no-op route instead of poisoning
// the whole keyboard.
const maxCallbackDataBytes = 64

// pagerButton builds a callback button, degrading to the no-op route when
// the composed data would exceed the Telegram callback_data limit.
func pagerButton(text, data string) telego.InlineKeyboardButton {
	if len(data) > maxCallbackDataBytes {
		data = pagerRouteNoop
	}
	return telego.InlineKeyboardButton{Text: text, CallbackData: data}
}

// viewPagerRow renders the «⬅️ · Стр. N/M · ➡️» row for paginated views.
// prev/next emit routePrefix+targetPage; unavailable directions render the
// «·» placeholder on the no-op route. Local views.go copy of the menu.go
// pager pattern (disjoint ownership — see statusPageSize).
func viewPagerRow(routePrefix string, page, totalPages int) []telego.InlineKeyboardButton {
	var prevBtn telego.InlineKeyboardButton
	if page > 1 {
		prevBtn = pagerButton("⬅️", fmt.Sprintf("%s%d", routePrefix, page-1))
	} else {
		prevBtn = telego.InlineKeyboardButton{Text: "·", CallbackData: pagerRouteNoop}
	}
	var nextBtn telego.InlineKeyboardButton
	if page < totalPages {
		nextBtn = pagerButton("➡️", fmt.Sprintf("%s%d", routePrefix, page+1))
	} else {
		nextBtn = telego.InlineKeyboardButton{Text: "·", CallbackData: pagerRouteNoop}
	}
	label := fmt.Sprintf("· Стр. %d/%d ·", page, totalPages)
	return []telego.InlineKeyboardButton{prevBtn, pagerButton(label, pagerRouteNoop), nextBtn}
}

// withPagerRow appends a pager row to a base inline keyboard (e.g. the
// incidents filter tabs produced by the menus cluster), keeping every
// original route intact.
func withPagerRow(base *telego.InlineKeyboardMarkup, routePrefix string, page, totalPages int) *telego.InlineKeyboardMarkup {
	if base == nil || totalPages <= 1 {
		return base
	}
	rows := append(base.InlineKeyboard, viewPagerRow(routePrefix, page, totalPages))
	return &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// statusPageSections flattens the /status view into ordered sections of
// per-proxy rows, mirroring getStatusText formatting (same row builders).
func (b *Bot) statusPageSections(snapshot []metrics.ProxyMetric, nodes []NodeInfo, nodeProxiesMap map[string][]metrics.ProxyMetric) []statusSection {
	var sections []statusSection

	var localProxies []metrics.ProxyMetric
	for _, pm := range snapshot {
		if pm.NodeName == "" && !pm.Disabled {
			localProxies = append(localProxies, pm)
		}
	}
	if len(localProxies) > 0 {
		locOnline := 0
		for _, pm := range localProxies {
			if pm.Online {
				locOnline++
			}
		}
		sections = append(sections, statusSection{
			header: fmt.Sprintf("%s <b>Локальный чекер · %d/%d онлайн:</b>\n", renderIcon(IconMaster), locOnline, len(localProxies)),
			rows:   localProxies,
		})
	}

	for _, n := range nodes {
		sections = append(sections, b.nodeStatusSection(n, nodeProxiesMap[n.Name]))
	}

	handled := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		handled[n.Name] = true
	}
	// Deterministic order for orphan node groups (map iteration is random).
	var orphans []string
	for nodeName := range nodeProxiesMap {
		if !handled[nodeName] {
			orphans = append(orphans, nodeName)
		}
	}
	sort.Strings(orphans)
	for _, nodeName := range orphans {
		pms := nodeProxiesMap[nodeName]
		badge := renderIcon(IconStateOK)
		if countOnline(pms) < len(pms) {
			badge = "⚠️"
		}
		sections = append(sections, statusSection{
			header: fmt.Sprintf("%s <b>Нода %s</b> · %s %d/%d онлайн:\n", renderIcon(IconNode), escapeHTML(nodeName), badge, countOnline(pms), len(pms)),
			rows:   pms,
		})
	}
	return sections
}

// statusSection is one grouped block of the /status view.
type statusSection struct {
	header string
	rows   []metrics.ProxyMetric
}

// nodeStatusSection renders the section header for one registry node.
func (b *Bot) nodeStatusSection(n NodeInfo, pms []metrics.ProxyMetric) statusSection {
	asnStr := ""
	if n.ASN != "" {
		asnStr = fmt.Sprintf(" (%s)", escapeHTML(n.ASN))
	}
	name := escapeHTML(n.Name)
	var header string
	if !n.Up {
		header = fmt.Sprintf("%s <b>Нода %s</b>%s · %s оффлайн:\n", renderIcon(IconNode), name, asnStr, renderIcon(IconStateDown))
	} else if len(pms) == 0 {
		header = fmt.Sprintf("%s <b>Нода %s</b>%s · %s на связи <i>(нет прокси)</i>:\n", renderIcon(IconNode), name, asnStr, renderIcon(IconStateOK))
	} else if countOnline(pms) == len(pms) {
		header = fmt.Sprintf("%s <b>Нода %s</b>%s · %s %d/%d онлайн:\n", renderIcon(IconNode), name, asnStr, renderIcon(IconStateOK), countOnline(pms), len(pms))
	} else {
		header = fmt.Sprintf("%s <b>Нода %s</b>%s · ⚠️ %d/%d онлайн:\n", renderIcon(IconNode), name, asnStr, countOnline(pms), len(pms))
	}
	return statusSection{header: header, rows: pms}
}

// countOnline counts online proxies in a slice.
func countOnline(pms []metrics.ProxyMetric) int {
	n := 0
	for _, pm := range pms {
		if pm.Online {
			n++
		}
	}
	return n
}

// getStatusPageText renders one page of the /status view plus the total
// page count (wave K-B1 pagination view; wired into the callback dispatch by
// the menus-cluster owner). The header totals always reflect the FULL
// snapshot; disabled proxies are listed on the last page only.
func (b *Bot) getStatusPageText(page int) (string, int) {
	if b.source == nil {
		return "Нет данных о прокси-хостах — проверки ещё не выполнялись.", 1
	}
	snapshot := b.source.MetricsSnapshot()
	if len(snapshot) == 0 {
		return "Нет данных о прокси-хостах — проверки ещё не выполнялись.", 1
	}
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Name < snapshot[j].Name })

	var nodes []NodeInfo
	if b.nodeMgr != nil {
		nodes = b.nodeMgr.Nodes()
	}

	var disabledProxies []metrics.ProxyMetric
	nodeProxiesMap := make(map[string][]metrics.ProxyMetric)
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
		if pm.NodeName != "" {
			nodeProxiesMap[pm.NodeName] = append(nodeProxiesMap[pm.NodeName], pm)
		}
	}

	sections := b.statusPageSections(snapshot, nodes, nodeProxiesMap)

	totalRows := 0
	for _, sec := range sections {
		totalRows += len(sec.rows)
	}
	totalPages := (totalRows + statusPageSize - 1) / statusPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	var sb strings.Builder
	if totalPages > 1 {
		fmt.Fprintf(&sb, "<b>Статус прокси-хостов: %d/%d онлайн</b> — стр. %d из %d\n\n", online, activeTotal, page, totalPages)
	} else {
		fmt.Fprintf(&sb, "<b>Статус прокси-хостов: %d/%d онлайн</b>\n\n", online, activeTotal)
	}

	remaining := statusPageSize
	pageStart := (page - 1) * statusPageSize
	skipped := 0
	for _, sec := range sections {
		if len(sec.rows) == 0 {
			if page == 1 {
				sb.WriteString(sec.header + "<i>• Нет данных о проверенных прокси</i>\n\n")
			}
			continue
		}
		// Skip whole sections that belong to earlier pages.
		if skipped+len(sec.rows) <= pageStart {
			skipped += len(sec.rows)
			continue
		}
		start := pageStart - skipped
		if start < 0 {
			start = 0
		}
		end := start + remaining
		if end > len(sec.rows) {
			end = len(sec.rows)
		}
		if end <= start {
			break
		}
		cont := start > 0
		header := sec.header
		if cont {
			header = continuationHeader(sec)
		}
		sb.WriteString(header)
		for _, pm := range sec.rows[start:end] {
			if pm.Online {
				sb.WriteString(statusOnlineRow(pm.Name, pm.LatencyMs))
			} else {
				sb.WriteString(statusDownRow(pm.Name))
			}
		}
		sb.WriteString("\n")
		remaining -= (end - start)
		skipped += len(sec.rows)
		if remaining <= 0 {
			break
		}
	}

	if page == totalPages && len(disabledProxies) > 0 {
		fmt.Fprintf(&sb, "<b>Отключённые прокси-хосты (%d):</b>\n", len(disabledProxies))
		for _, pm := range disabledProxies {
			name := pm.Name
			if pm.NodeName != "" {
				name = fmt.Sprintf("[%s] %s", pm.NodeName, pm.Name)
			}
			sb.WriteString(statusDisabledRow(name))
		}
	}

	return strings.TrimRight(sb.String(), "\n"), totalPages
}

// continuationHeader rebuilds a section header for a page that resumes the
// section mid-list («(продолжение)»).
func continuationHeader(sec statusSection) string {
	header := sec.header
	if idx := strings.Index(header, ":\n"); idx >= 0 {
		header = header[:idx] + " (" + "продолжение" + ")" + header[idx:]
	}
	return header
}

// StatusPageMarkup builds the controls for the paginated /status screen:
// the local pager row (when several pages exist) plus the same action rows
// as the fixed StatusMenuMarkup — all routes except the pager prefix are
// already-wired menu routes.
func StatusPageMarkup(page, totalPages int) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	if totalPages > 1 {
		rows = append(rows, viewPagerRow("menu:statuspg:", page, totalPages))
	}
	rows = append(rows,
		[]telego.InlineKeyboardButton{
			pagerButton("🔄 Обновить", "menu:status"),
			pagerButton("🔎 Подробнее", "menu:diag"),
		},
		[]telego.InlineKeyboardButton{
			// btnIcon contract (Guardian re-gate 2026-11-18): plain fallback in
			// Text, animated icon via WithIconCustomEmojiID — renderIcon output
			// (raw <tg-emoji>) is banned in button text. The route is a fixed
			// 9-byte literal, so pagerButton's 64-byte degradation is moot.
			btnIcon(IconBack, " Главное меню", "menu:main"),
		},
	)
	return &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// renderStatusPage re-renders the paginated /status screen in place. The
// menus-cluster owner (callbacks.go) wires the «menu:statuspg:<page>» prefix
// to this one-liner.
func (b *Bot) renderStatusPage(chatID int64, msgID int, page int) {
	text, totalPages := b.getStatusPageText(page)
	b.editWithMarkup(chatID, msgID, text, StatusPageMarkup(page, totalPages))
}

// incidentsPagerPrefix composes the incidents pager route prefix for a node
// filter: «menu:stats:incpg:<filter>:<page>».
func incidentsPagerPrefix(filter string) string {
	return "menu:stats:incpg:" + filter + ":"
}

// renderIncidentsPage re-renders the paginated incident journal in place.
// base carries the filter-tab keyboard from the menus cluster (IncidentsFilterMarkup);
// the pager row is appended locally. The menus-cluster owner wires the
// «menu:stats:incpg:<filter>:<page>» prefix to this one-liner.
func (b *Bot) renderIncidentsPage(chatID int64, msgID int, filter string, page int, base *telego.InlineKeyboardMarkup) {
	text, totalPages := b.getIncidentsTextPage(filter, page)
	b.editWithMarkup(chatID, msgID, text, withPagerRow(base, incidentsPagerPrefix(filter), page, totalPages))
}
