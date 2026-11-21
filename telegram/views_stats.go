package telegram

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"xray-checker/metrics"
)

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
