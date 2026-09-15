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
			downProxies = append(downProxies, pm.Name)
		}
	}

	var statusLine string
	if disabledCount > 0 {
		statusLine = fmt.Sprintf("• Текущий статус: <b>%d/%d онлайн</b> <i>(⏸️ %d отключено)</i>\n", online, totalActive, disabledCount)
	} else {
		statusLine = fmt.Sprintf("• Текущий статус: <b>%d/%d онлайн</b>\n", online, len(snapshot))
	}

	nowStr := b.now().Format("15:04:05 02.01.2006")
	var uptimeStr string
	if avg, ok := b.getAverageUptimePercent(); ok {
		uptimeStr = fmt.Sprintf("• Средний аптайм: <b>%.1f%%</b>\n", avg)
	}

	var sb strings.Builder
	sb.WriteString("<b>📊 Сводка Xray Checker</b>\n\n")
	sb.WriteString(statusLine)
	sb.WriteString(fmt.Sprintf("• Время: <b>%s</b>\n", nowStr))
	if uptimeStr != "" {
		sb.WriteString(uptimeStr)
	}

	if len(downProxies) > 0 {
		sb.WriteString("\n<b>🔴 Требуют внимания:</b>\n")
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

	pageSize := 6
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

	pageSize := 6
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
		return 100.0, false
	}
	snapshot := b.source.MetricsSnapshot()
	var totalUptime float64
	var count int
	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		totalUptime += b.statsStore.GetUptimePercent(pm.StableID)
		count++
	}
	if count == 0 {
		return 100.0, false
	}
	return totalUptime / float64(count), true
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

	online := 0
	activeTotal := 0
	disabledCount := 0
	var activeBody strings.Builder
	var disabledBody strings.Builder

	for _, pm := range snapshot {
		if pm.Disabled {
			disabledCount++
			fmt.Fprintf(&disabledBody, "⏸️ <b>%s</b> — отключён\n", escapeHTML(pm.Name))
			continue
		}
		activeTotal++
		if pm.Online {
			online++
			fmt.Fprintf(&activeBody, "✅ <b>%s</b> — %.0f ms\n", escapeHTML(pm.Name), pm.LatencyMs)
		} else {
			fmt.Fprintf(&activeBody, "🔴 <b>%s</b> — недоступен\n", escapeHTML(pm.Name))
		}
	}

	header := fmt.Sprintf("<b>Статус прокси-хостов: %d/%d онлайн</b>\n\n", online, activeTotal)
	if disabledCount > 0 {
		return header + activeBody.String() + fmt.Sprintf("\n<b>Отключённые прокси-хосты (%d):</b>\n", disabledCount) + disabledBody.String()
	}
	return header + activeBody.String()
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
	fmt.Fprintf(&sb, "• Средний аптайм: <b>24ч: %.1f%%</b> (7д: %.1f%%)\n", uptime24h, avg7d)

	incidents := b.statsStore.GetRecentIncidents(1)
	if len(incidents) > 0 {
		inc := incidents[0]
		downTime := time.Unix(inc.DownAt, 0).In(b.loc()).Format("15:04 02.01")
		durText := "ещё не восстановлен"
		if inc.UpAt > 0 {
			durText = FormatDowntime(time.Duration(inc.DurationSec) * time.Second)
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

	// Monospace table for proxies (up to 15)
	if totalProxies > 0 {
		sb.WriteString("\n<pre>")
		sb.WriteString(fmt.Sprintf("%-11s %5s %5s %5s %5s %4s %4s\n", "ПРОКСИ", "24Ч", "7Д", "P50", "P95", "Σ", "ПАД."))

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
			pName := pm.Name
			if len(pName) > 11 {
				pName = pName[:10] + "…"
			}
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

			p50Str := fmt.Sprintf("%.0fms", p50)
			p95Str := fmt.Sprintf("%.0fms", p95)
			if ls.Count() < 5 {
				p50Str = "-"
				p95Str = "-"
			}
			flapWarning := ""
			if flaps > 10 {
				flapWarning = "⚠️"
			}
			sb.WriteString(fmt.Sprintf("%-11s %4.1f%% %4.1f%% %5s %5s %4.0f %3d%s\n",
				pName, u24, u7d, p50Str, p95Str, jitter, flaps, flapWarning))
		}
		if len(sortedSnap) > maxRows {
			sb.WriteString(fmt.Sprintf("... и ещё %d прокси-хостов\n", len(sortedSnap)-maxRows))
		}
		sb.WriteString("</pre>")
	}

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

		fmt.Fprintf(&sb, "<b>%s</b> (n=%d)\n  Аптайм 24ч: %.1f%%   Латенси p50: %.0f мс, p95: %.0f мс\n\n",
			escapeHTML(gd.name), n, avgUptime, p50, p95)
	}

	return strings.TrimRight(sb.String(), "\n")
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
		fmt.Fprintf(&sb, "\nПик падений: %02dч (%d сбоев, вероятно перегрузка канала)\n", peakHour, maxDrops)
	} else {
		sb.WriteString("\nЗа последние 7 дней падений не зафиксировано.\n")
	}
	return sb.String()
}

func (b *Bot) getIncidentsText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}
	incidents := b.statsStore.GetRecentIncidents(15)
	if len(incidents) == 0 {
		return "<b>📋 Журнал инцидентов</b>\n\nЗафиксированных инцидентов нет — все прокси-хосты работают стабильно!"
	}

	var sb strings.Builder
	sb.WriteString("<b>📋 Последние инциденты:</b>\n\n")
	for _, inc := range incidents {
		downTime := time.Unix(inc.DownAt, 0).In(b.loc()).Format("15:04 02.01")
		if inc.UpAt == 0 {
			fmt.Fprintf(&sb, "🔴 <b>%s</b> — сбой %s (<i>сейчас недоступен</i>)\nПричина: %s\n\n",
				escapeHTML(inc.ProxyName), downTime, escapeHTML(inc.Reason))
		} else {
			fmt.Fprintf(&sb, "🟡 <b>%s</b> — %s (простой: %s)\nПричина: %s\n\n",
				escapeHTML(inc.ProxyName), downTime, FormatDowntime(time.Duration(inc.DurationSec)*time.Second), escapeHTML(inc.Reason))
		}
	}
	return sb.String()
}

func (b *Bot) getTopProblematicText() string {
	if b.statsStore == nil {
		return "Статистика недоступна."
	}
	top := b.statsStore.GetTopProblematic(10)
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
			mtbfStr = fmt.Sprintf(", MTBF: %s", FormatDowntime(incStats.MTBF))
		}
		mttrStr := ""
		if incStats.MTTR > 0 {
			mttrStr = fmt.Sprintf(", MTTR: %s", FormatDowntime(incStats.MTTR))
		}
		fmt.Fprintf(&sb, "%d. <b>%s</b>: инцидентов: %d, аптайм: %.1f%%%s%s, суммарный простой: %s\n",
			i+1, escapeHTML(p.ProxyName), p.DropCount, p.UptimePct, mtbfStr, mttrStr, FormatDowntime(time.Duration(p.DowntimeSec)*time.Second))
	}
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
