package telegram

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mymmrac/telego"

	"xray-checker/metrics"
)

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
				sb.WriteString(statusDownRow(pm))
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
