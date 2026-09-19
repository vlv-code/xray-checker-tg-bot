package telegram

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/checker"
	"xray-checker/metrics"
)

const diagPageSize = 5
const diagRichDetailsPageSize = 15

func (b *Bot) getCachedDiagnosticsReports() []checker.ProxyDiagReport {
	if b.diagSource == nil {
		return nil
	}
	b.diagMu.Lock()
	defer b.diagMu.Unlock()
	if len(b.cachedDiag) > 0 {
		reports := make([]checker.ProxyDiagReport, len(b.cachedDiag))
		copy(reports, b.cachedDiag)
		return reports
	}
	return nil
}

func (b *Bot) getDiagnosticsReports(force bool) []checker.ProxyDiagReport {
	if b.diagSource == nil {
		return nil
	}

	b.diagMu.Lock()
	if !force && len(b.cachedDiag) > 0 && time.Since(b.cachedDiagAt) < 60*time.Second {
		reports := make([]checker.ProxyDiagReport, len(b.cachedDiag))
		copy(reports, b.cachedDiag)
		b.diagMu.Unlock()
		return reports
	}
	b.diagMu.Unlock()

	targets := []string{
		"https://cp.cloudflare.com/generate_204",
		"https://www.gstatic.com/generate_204",
	}
	if tm := b.diagSource.GetTargetManager(); tm != nil {
		configured := tm.GetTargets()
		if len(configured) > 0 {
			targets = configured
		}
	}

	reports := b.diagSource.RunDiagnostics(targets)
	b.sortDiagnosticsReports(reports)

	b.diagMu.Lock()
	b.cachedDiag = make([]checker.ProxyDiagReport, len(reports))
	copy(b.cachedDiag, reports)
	b.cachedDiagAt = time.Now()
	b.diagMu.Unlock()

	return reports
}

// getNodeDiagnosticsReports converts metrics for a specific remote node into ProxyDiagReport slice.
func (b *Bot) getNodeDiagnosticsReports(nodeName string) []checker.ProxyDiagReport {
	if b.nodeMgr != nil {
		if diag := b.nodeMgr.NodeDiagReports(nodeName); len(diag) > 0 {
			b.sortDiagnosticsReports(diag)
			return diag
		}
	}

	var snapshot []metrics.ProxyMetric
	if b.nodeMgr != nil {
		snapshot = b.nodeMgr.NodeSnapshot(nodeName)
	}
	if len(snapshot) == 0 && b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.NodeName == nodeName {
				snapshot = append(snapshot, pm)
			}
		}
	}
	if len(snapshot) == 0 {
		return nil
	}

	var reports []checker.ProxyDiagReport
	for _, pm := range snapshot {
		status := "online"
		if pm.Disabled {
			status = "disabled"
		} else if !pm.Online {
			status = "offline"
		}
		targets := []checker.TargetDiagResult{
			{
				URL:     "node-check",
				Latency: time.Duration(pm.LatencyMs * float64(time.Millisecond)),
				Success: pm.Online,
				Error:   pm.LastErrorMsg,
			},
		}
		server, portStr, _ := net.SplitHostPort(pm.Address)
		port, _ := strconv.Atoi(portStr)
		rep := checker.ProxyDiagReport{
			ProxyName: pm.Name,
			Protocol:  pm.Protocol,
			Server:    server,
			Port:      port,
			StableID:  pm.StableID,
			Status:    status,
			Targets:   targets,
			Verdict:   pm.LastErrorMsg,
			Disabled:  pm.Disabled,
		}
		reports = append(reports, rep)
	}
	b.sortDiagnosticsReports(reports)
	return reports
}

// getNodeTabs returns the navigation tabs for Master and remote nodes.
func (b *Bot) getNodeTabs(activeTarget string) []NodeTabItem {
	if b.nodeMgr == nil {
		return nil
	}
	nodes := b.nodeMgr.Nodes()
	if len(nodes) == 0 {
		return nil
	}

	masterOnline := 0
	masterTotal := 0
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.NodeName == "" && !pm.Disabled {
				masterTotal++
				if pm.Online {
					masterOnline++
				}
			}
		}
	}

	masterActive := activeTarget == "local" || activeTarget == ""
	tabs := []NodeTabItem{
		{
			ID:       "local",
			Label:    "🏠 Мастер",
			Status:   fmt.Sprintf("%d/%d", masterOnline, masterTotal),
			IsActive: masterActive,
		},
	}

	for _, n := range nodes {
		badge := ""
		if !n.Up {
			badge = "🔴 оффлайн"
		} else if n.Total > 0 && n.Online < n.Total {
			badge = fmt.Sprintf("⚠️ %d/%d", n.Online, n.Total)
		} else if n.Total > 0 {
			badge = fmt.Sprintf("%d/%d", n.Online, n.Total)
		} else {
			badge = "0"
		}

		tabs = append(tabs, NodeTabItem{
			ID:       n.Name,
			Label:    fmt.Sprintf("🖥 %s", n.Name),
			Status:   badge,
			IsActive: activeTarget == n.Name,
		})
	}
	return tabs
}

// getNodeASN returns the ASN of a remote node by name, or empty string.
func (b *Bot) getNodeASN(name string) string {
	if b.nodeMgr == nil {
		return ""
	}
	for _, n := range b.nodeMgr.Nodes() {
		if n.Name == name {
			return n.ASN
		}
	}
	return ""
}

func (b *Bot) sortDiagnosticsReports(reports []checker.ProxyDiagReport) {
	now := b.now()
	getSeverityRank := func(rep *checker.ProxyDiagReport) int {
		if rep.Disabled || rep.Status == "disabled" {
			return 4
		}
		if rep.Status == "offline" {
			return 1
		}
		if rep.Status == "degraded" {
			return 2
		}
		return 3 // online
	}

	sort.SliceStable(reports, func(i, j int) bool {
		ri, rj := &reports[i], &reports[j]
		rankI, rankJ := getSeverityRank(ri), getSeverityRank(rj)
		if rankI != rankJ {
			return rankI < rankJ
		}

		if rankI == 1 { // offline: longer downtime first
			var dtI, dtJ time.Duration
			if b.tracker != nil {
				dtI = b.tracker.GetDowntime(ri.StableID, now)
				dtJ = b.tracker.GetDowntime(rj.StableID, now)
			}
			if dtI != dtJ {
				return dtI > dtJ
			}
			return ri.ProxyName < rj.ProxyName
		}

		if rankI == 3 { // online: lower latency first
			var latI, latJ time.Duration
			for _, tr := range ri.Targets {
				if tr.Success {
					latI = tr.Latency
					break
				}
			}
			for _, tr := range rj.Targets {
				if tr.Success {
					latJ = tr.Latency
					break
				}
			}
			if latI != latJ {
				return latI < latJ
			}
			return ri.ProxyName < rj.ProxyName
		}

		return ri.ProxyName < rj.ProxyName
	})
}

func formatSingleProxyDiag(sb *strings.Builder, rep checker.ProxyDiagReport) {
	var b *Bot
	b.formatSingleProxyDiagWithStats(sb, rep)
}

func (b *Bot) formatSingleProxyDiagWithStats(sb *strings.Builder, rep checker.ProxyDiagReport) {
	proto := strings.ToUpper(rep.Protocol)
	if proto == "" {
		proto = "PROXY"
	}

	if rep.Disabled || rep.Status == "disabled" {
		fmt.Fprintf(sb, "⏸️ <b>%s</b> <i>(%s)</i> — <i>проверка отключена</i>\n\n", escapeHTML(rep.ProxyName), proto)
		return
	}

	icon := "🟢"
	switch rep.Status {
	case "offline":
		icon = "🔴"
	case "degraded":
		icon = "🟡"
	}

	flappingSuffix := ""
	if b != nil && b.statsStore != nil && b.statsStore.GetFlapCount24h(rep.StableID, b.now()) > 10 {
		flappingSuffix = " ⚠️ флап"
	}

	fmt.Fprintf(sb, "%s <b>%s</b> <i>(%s)</i>%s\n", icon, escapeHTML(rep.ProxyName), proto, flappingSuffix)

	// Status line with soft hint or latency percentiles
	if rep.Status == "offline" {
		var cat checker.ErrorCategory
		if rep.NodeHealth.DNSErr != "" {
			cat = checker.CatDNSError
		} else if rep.NodeHealth.TLSErr != "" {
			cat = checker.CatTLSError
		} else if checker.IsUDPProto(rep.Protocol) && rep.NodeHealth.UDPErr != "" {
			if strings.Contains(strings.ToLower(rep.NodeHealth.UDPErr), "refused") {
				cat = checker.CatConnRefused
			} else {
				cat = checker.CatTimeout
			}
		} else if rep.NodeHealth.TCPErr != "" {
			if strings.Contains(strings.ToLower(rep.NodeHealth.TCPErr), "refused") {
				cat = checker.CatConnRefused
			} else {
				cat = checker.CatTimeout
			}
		} else {
			for _, tr := range rep.Targets {
				if !tr.Success {
					cat = checker.ClassifyError(fmt.Errorf("%s", tr.Error), tr.StatusCode)
					break
				}
			}
			if cat == checker.CatNone {
				cat = checker.CatTimeout
			}
		}
		hint := checker.FormatSoftHint(cat, checker.IsUDPProto(rep.Protocol))
		fmt.Fprintf(sb, "  • Статус: 🔴 недоступен (вероятно: %s)\n", escapeHTML(hint))
	} else if rep.Status == "degraded" {
		fmt.Fprintf(sb, "  • Статус: 🟡 туннель работает, но цель недоступна\n")
	} else {
		var latMs float64
		for _, tr := range rep.Targets {
			if tr.Success {
				latMs = float64(tr.Latency.Milliseconds())
				break
			}
		}
		if b != nil && b.statsStore != nil {
			ls := b.statsStore.GetLatencySamples(rep.StableID)
			if ls.Count() >= 50 {
				p95 := ls.Percentile(0.95)
				p99 := ls.Percentile(0.99)
				jitter := ls.StdDev()
				fmt.Fprintf(sb, "  • Доступен: 🟢 %.0f мс (p95: %.0f мс, p99: %.0f мс, σ=%.0f)\n", latMs, p95, p99, jitter)
			} else if ls.Count() >= 5 {
				p95 := ls.Percentile(0.95)
				jitter := ls.StdDev()
				fmt.Fprintf(sb, "  • Доступен: 🟢 %.0f мс (p95: %.0f мс, σ=%.0f)\n", latMs, p95, jitter)
			} else {
				fmt.Fprintf(sb, "  • Доступен: 🟢 %.0f мс\n", latMs)
			}
		} else {
			fmt.Fprintf(sb, "  • Доступен: 🟢 %.0f мс\n", latMs)
		}
	}

	// 1. DNS
	if rep.NodeHealth.DNSErr != "" {
		fmt.Fprintf(sb, "  • DNS: ❌ %s\n", escapeHTML(rep.NodeHealth.DNSErr))
	} else if rep.NodeHealth.ResolvedIP != "" {
		if rep.NodeHealth.DNSLatency > 0 {
			fmt.Fprintf(sb, "  • DNS: ✅ <code>%s</code> (%.0f ms)\n", rep.NodeHealth.ResolvedIP, float64(rep.NodeHealth.DNSLatency.Milliseconds()))
		} else {
			fmt.Fprintf(sb, "  • DNS: ✅ <code>%s</code>\n", rep.NodeHealth.ResolvedIP)
		}
	}

	// 2. Transport Protocol / TCP Ping
	if checker.IsUDPProto(rep.Protocol) {
		if rep.Port > 0 {
			fmt.Fprintf(sb, "  • Порт (%d): ⚡ UDP / QUIC\n", rep.Port)
		}
	} else if rep.Port > 0 {
		if rep.NodeHealth.TCPErr != "" {
			fmt.Fprintf(sb, "  • TCP (%d): ❌ %s\n", rep.Port, escapeHTML(rep.NodeHealth.TCPErr))
		} else if rep.NodeHealth.TCPPing > 0 {
			fmt.Fprintf(sb, "  • TCP (%d): ✅ %.0f ms\n", rep.Port, float64(rep.NodeHealth.TCPPing.Milliseconds()))
		}
	}

	// 3. TLS Handshake (if attempted)
	if rep.NodeHealth.TLSErr != "" {
		fmt.Fprintf(sb, "  • TLS: ❌ %s\n", escapeHTML(rep.NodeHealth.TLSErr))
	} else if rep.NodeHealth.TLSLatency > 0 {
		fmt.Fprintf(sb, "  • TLS: ✅ %.0f ms\n", float64(rep.NodeHealth.TLSLatency.Milliseconds()))
	}

	// 4. Target Endpoints
	for _, tr := range rep.Targets {
		siteName := simplifyTargetName(tr.URL)
		if tr.Success {
			fmt.Fprintf(sb, "  • %s: ✅ %.0f ms\n", siteName, float64(tr.Latency.Milliseconds()))
		} else {
			fmt.Fprintf(sb, "  • %s: ❌ %s\n", siteName, escapeHTML(tr.Error))
		}
	}

	// 5. External Check-Host
	if rep.CheckHost != nil {
		if rep.NodeHealth.DNSErr != "" && !rep.CheckHost.RUAvailable && !rep.CheckHost.WorldAvailable {
			fmt.Fprintf(sb, "  • Check-Host: РФ ❌, Мир ❌ (домен не резолвится внешними узлами)\n")
		} else {
			ruStatus := "❌ недоступен"
			if rep.CheckHost.RUAvailable {
				ruStatus = "✅ отвечает"
			}
			worldStatus := "❌ недоступен"
			if rep.CheckHost.WorldAvailable {
				worldStatus = "✅ отвечает"
			}
			fmt.Fprintf(sb, "  • Check-Host: РФ %s, Мир %s\n", ruStatus, worldStatus)
		}
		if rep.CheckHost.PermanentLink != "" {
			fmt.Fprintf(sb, "    🔗 <a href=\"%s\">отчёт Check-Host</a>\n", escapeHTML(rep.CheckHost.PermanentLink))
		}
	}

	// 6. Verdict
	if rep.Verdict != "" {
		fmt.Fprintf(sb, "  💡 <i>Вердикт: %s</i>\n", escapeHTML(rep.Verdict))
	}

	sb.WriteString("\n")
}

func (b *Bot) getDiagnosticsText() string {
	reports := b.getDiagnosticsReports(true)
	if len(reports) == 0 {
		return "Нет прокси для проверки."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>⚡ Результаты детальной диагностики (%d прокси):</b>\n\n", len(reports)))
	for _, rep := range reports {
		b.formatSingleProxyDiagWithStats(&sb, rep)
	}
	return sb.String()
}

func getDeepLinksForPage(reports []checker.ProxyDiagReport, page int) []DiagDeepLink {
	if len(reports) == 0 {
		return nil
	}
	totalPages := (len(reports) + diagPageSize - 1) / diagPageSize
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * diagPageSize
	end := start + diagPageSize
	if end > len(reports) {
		end = len(reports)
	}

	var links []DiagDeepLink
	for _, rep := range reports[start:end] {
		if rep.Status == "offline" || rep.Status == "degraded" {
			links = append(links, DiagDeepLink{
				Name:     rep.ProxyName,
				StableID: rep.StableID,
			})
		}
	}
	return links
}

func getDeepLinks(reports []checker.ProxyDiagReport, maxCount int) []DiagDeepLink {
	var links []DiagDeepLink
	for _, rep := range reports {
		if rep.Status == "offline" || rep.Status == "degraded" {
			links = append(links, DiagDeepLink{
				Name:     rep.ProxyName,
				StableID: rep.StableID,
			})
			if maxCount > 0 && len(links) >= maxCount {
				break
			}
		}
	}
	return links
}

func (b *Bot) getDiagnosticsSummaryText(reports []checker.ProxyDiagReport) string {
	return b.getDiagnosticsSummaryTextWithTarget(reports, "local", b.getMasterASN())
}

func (b *Bot) getDiagnosticsSummaryTextWithTarget(reports []checker.ProxyDiagReport, target string, asn string) string {
	var sb strings.Builder
	hasNodes := b != nil && b.nodeMgr != nil && len(b.nodeMgr.Nodes()) > 0
	title := ""
	if target != "" && target != "local" {
		title = fmt.Sprintf("📊 <b>Подробная сводка — 🖥 Нода %s (%d всего):</b>", escapeHTML(target), len(reports))
	} else if hasNodes {
		title = fmt.Sprintf("📊 <b>Подробная сводка — 🏠 Мастер (%d всего):</b>", len(reports))
	} else {
		title = fmt.Sprintf("📊 <b>Подробная сводка (%d всего):</b>", len(reports))
	}
	sb.WriteString(title + "\n")
	if asn != "" {
		sb.WriteString(fmt.Sprintf("<i>ASN: %s</i>\n", escapeHTML(asn)))
	}
	sb.WriteString("\n")

	if len(reports) == 0 {
		sb.WriteString("Нет доступных прокси для проверки.")
		return sb.String()
	}

	onlineCount, offlineCount, disabledCount := 0, 0, 0
	for _, rep := range reports {
		latencyText := "—"
		for _, tr := range rep.Targets {
			if tr.Success {
				latencyText = fmt.Sprintf("%.0f ms", float64(tr.Latency.Milliseconds()))
				break
			}
		}

		icon := "🟢"
		statusDesc := latencyText
		switch rep.Status {
		case "offline":
			icon = "🔴"
			statusDesc = "недоступен"
			offlineCount++
		case "degraded":
			icon = "🟡"
			statusDesc = "ошибка"
			offlineCount++
		case "disabled":
			icon = "⏸️"
			statusDesc = "отключён"
			disabledCount++
		default:
			onlineCount++
		}

		proto := strings.ToUpper(rep.Protocol)
		if proto == "" {
			proto = "PROXY"
		}

		fmt.Fprintf(&sb, "%s <b>%s</b> (%s) — %s\n", icon, escapeHTML(rep.ProxyName), proto, statusDesc)
	}

	fmt.Fprintf(&sb, "\n💡 <i>В сети: %d | Сбоев: %d", onlineCount, offlineCount)
	if disabledCount > 0 {
		fmt.Fprintf(&sb, " | Отключено: %d", disabledCount)
	}
	sb.WriteString("</i>\n<i>Нажмите «📑 Детальный отчёт» для постраничного разбора этапов.</i>")

	return sb.String()
}

func (b *Bot) getDiagnosticsPageText(reports []checker.ProxyDiagReport, page int) (string, int) {
	return b.getDiagnosticsPageTextWithTarget(reports, "local", page)
}

func (b *Bot) getDiagnosticsPageTextWithTarget(reports []checker.ProxyDiagReport, target string, page int) (string, int) {
	if len(reports) == 0 {
		return "Нет доступных прокси для проверки.", 1
	}

	totalPages := (len(reports) + diagPageSize - 1) / diagPageSize
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * diagPageSize
	end := start + diagPageSize
	if end > len(reports) {
		end = len(reports)
	}

	var sb strings.Builder
	hasNodes := b != nil && b.nodeMgr != nil && len(b.nodeMgr.Nodes()) > 0
	nodePrefix := ""
	if target != "" && target != "local" {
		nodePrefix = fmt.Sprintf(" — 🖥 Нода %s", escapeHTML(target))
	} else if hasNodes {
		nodePrefix = " — 🏠 Мастер"
	}
	sb.WriteString(fmt.Sprintf("📑 <b>Детальный отчёт%s</b> (Стр. %d из %d, всего %d прокси):\n\n", nodePrefix, page, totalPages, len(reports)))

	for _, rep := range reports[start:end] {
		b.formatSingleProxyDiagWithStats(&sb, rep)
	}

	return sb.String(), totalPages
}

func (b *Bot) formatDeepDiagnostics(pm metrics.ProxyMetric, health checker.NodeHealth, ch *checker.CheckHostSummary) string {
	var sb strings.Builder

	proto := strings.ToUpper(pm.Protocol)
	if proto == "" {
		proto = "PROXY"
	}

	transport := "TCP"
	if checker.IsUDPProto(pm.Protocol) {
		transport = "UDP / QUIC"
	} else if strings.Contains(strings.ToLower(pm.Name), "reality") {
		transport = "Reality / TCP"
	}

	if pm.NodeName != "" {
		fmt.Fprintf(&sb, "🔬 <b>Углублённая проверка — 🖥 [%s] %s</b>\n\n", escapeHTML(pm.NodeName), escapeHTML(pm.Name))
	} else {
		fmt.Fprintf(&sb, "🔬 <b>Углублённая проверка — %s</b>\n\n", escapeHTML(pm.Name))
	}
	if pm.NodeName != "" {
		nodeInfo := fmt.Sprintf("🖥 Нода %s", pm.NodeName)
		if pm.NodeASN != "" {
			nodeInfo += fmt.Sprintf(" (%s)", pm.NodeASN)
		}
		fmt.Fprintf(&sb, "<b>УЗЕЛ:</b>      %s\n", escapeHTML(nodeInfo))
	}
	fmt.Fprintf(&sb, "<b>ПРОТОКОЛ:</b>  %s\n", proto)
	fmt.Fprintf(&sb, "<b>ТРАНСПОРТ:</b> %s\n", transport)
	fmt.Fprintf(&sb, "<b>АДРЕС:</b>     %s\n\n", escapeHTML(pm.Address))

	sb.WriteString("<b>ЭТАПЫ ПРОВЕРКИ:</b>\n")

	// 1. DNS-резолв
	if health.DNSErr != "" {
		fmt.Fprintf(&sb, "  1. DNS-резолв ........... —      ❌  %s\n", escapeHTML(health.DNSErr))
	} else if health.ResolvedIP != "" {
		if health.DNSLatency > 0 {
			fmt.Fprintf(&sb, "  1. DNS-резолв ........... %.0f мс  ✅\n", float64(health.DNSLatency.Milliseconds()))
		} else {
			fmt.Fprintf(&sb, "  1. DNS-резолв ........... ✅ (%s)\n", health.ResolvedIP)
		}
	} else {
		sb.WriteString("  1. DNS-резолв ........... —      (не требуется)\n")
	}

	// 2. TCP / UDP рукопожатие
	if checker.IsUDPProto(pm.Protocol) {
		if health.UDPErr != "" {
			fmt.Fprintf(&sb, "  2. UDP-датаграмма ...... —      ❌  %s\n", escapeHTML(health.UDPErr))
		} else if health.UDPPing > 0 {
			fmt.Fprintf(&sb, "  2. UDP-датаграмма ...... %.0f мс  ✅\n", float64(health.UDPPing.Milliseconds()))
		} else {
			sb.WriteString("  2. UDP-датаграмма ...... —\n")
		}
	} else {
		if health.TCPErr != "" {
			fmt.Fprintf(&sb, "  2. TCP-рукопожатие ...... —      ❌  %s\n", escapeHTML(health.TCPErr))
		} else if health.TCPPing > 0 {
			fmt.Fprintf(&sb, "  2. TCP-рукопожатие ...... %.0f мс  ✅  (к %s)\n", float64(health.TCPPing.Milliseconds()), escapeHTML(pm.Address))
		} else {
			sb.WriteString("  2. TCP-рукопожатие ...... —\n")
		}
	}

	// 3. TLS / Reality handshake
	if health.TLSErr != "" {
		fmt.Fprintf(&sb, "  3. TLS/Reality handshake  —      ❌  %s\n", escapeHTML(health.TLSErr))
	} else if pm.TLSHandshakeMs > 0 {
		fmt.Fprintf(&sb, "  3. TLS/Reality handshake  %d мс  ✅\n", pm.TLSHandshakeMs)
	} else if health.TLSLatency > 0 {
		fmt.Fprintf(&sb, "  3. TLS/Reality handshake  %.0f мс  ✅\n", float64(health.TLSLatency.Milliseconds()))
	} else if !pm.Online && !pm.CanConnect {
		sb.WriteString("  3. TLS/Reality handshake  —      ❌  (не завершено)\n")
	} else {
		sb.WriteString("  3. TLS/Reality handshake  —      ✅\n")
	}

	// 4. TTFB через туннель
	if pm.TTFBMs > 0 {
		fmt.Fprintf(&sb, "  4. TTFB через туннель .... %d мс\n", pm.TTFBMs)
	} else if pm.LatencyMs > 0 {
		fmt.Fprintf(&sb, "  4. TTFB через туннель .... %.0f мс\n", pm.LatencyMs)
	} else {
		sb.WriteString("  4. TTFB через туннель .... —      (не достигнуто)\n")
	}

	// 5. HTTP-ответ
	if pm.CanTransfer || pm.Online {
		sb.WriteString("  5. HTTP-ответ ........... ✅ получен\n")
	} else {
		sb.WriteString("  5. HTTP-ответ ........... —      (не получен)\n")
	}

	// 6. Полный ответ
	if pm.LatencyMs > 0 && (pm.CanTransfer || pm.Online) {
		fmt.Fprintf(&sb, "  6. Полный ответ .......... %.0f мс\n\n", pm.LatencyMs)
	} else {
		sb.WriteString("  6. Полный ответ .......... —\n\n")
	}

	// Summary
	if pm.Online || (pm.CanConnect && pm.CanTransfer) {
		sb.WriteString("<b>ИТОГ:</b> ✅ Доступен\n")
	} else {
		sb.WriteString("<b>ИТОГ:</b> ❌ Недоступен\n")
		if pm.LastErrorMsg != "" {
			fmt.Fprintf(&sb, "<b>ОШИБКА:</b>     %s\n", escapeHTML(pm.LastErrorMsg))
		}
		if pm.LastErrorCategory > 0 {
			cat := checker.ErrorCategory(pm.LastErrorCategory)
			fmt.Fprintf(&sb, "<b>ТИП ОШИБКИ:</b> %s\n", cat.String())
		}
	}

	// Latency percentiles & Reliability metrics
	if b != nil && b.statsStore != nil {
		ls := b.statsStore.GetLatencySamples(pm.StableID)
		if ls != nil && ls.Count() >= 5 {
			fmt.Fprintf(&sb, "\n<b>СТАТИСТИКА ЗАДЕРЖКИ:</b>\n")
			if ls.Count() >= 50 {
				fmt.Fprintf(&sb, "  • p50: %.0f мс | p95: %.0f мс | p99: %.0f мс (σ=%.0f, n=%d)\n",
					ls.Percentile(0.50), ls.Percentile(0.95), ls.Percentile(0.99), ls.StdDev(), ls.Count())
			} else {
				fmt.Fprintf(&sb, "  • p50: %.0f мс | p95: %.0f мс (σ=%.0f, n=%d)\n",
					ls.Percentile(0.50), ls.Percentile(0.95), ls.StdDev(), ls.Count())
			}
		}

		incStats := b.statsStore.GetIncidentStats(pm.StableID, 24*time.Hour, b.now())
		flaps := b.statsStore.GetFlapCount24h(pm.StableID, b.now())
		if incStats.Incidents > 0 || flaps > 0 {
			sb.WriteString("\n<b>НАДЁЖНОСТЬ (24ч):</b>\n")
			if incStats.Incidents > 0 {
				fmt.Fprintf(&sb, "  • Инцидентов: %d\n", incStats.Incidents)
				if incStats.MTBF > 0 {
					fmt.Fprintf(&sb, "  • MTBF (наработка на отказ): %s\n", FormatDowntime(incStats.MTBF))
				}
				if incStats.MTTR > 0 {
					fmt.Fprintf(&sb, "  • MTTR (время восстановления): %s\n", FormatDowntime(incStats.MTTR))
				}
			}
			if flaps > 0 {
				fmt.Fprintf(&sb, "  • Флаппинг: %d переключений за 24ч\n", flaps)
			}
		}
	}

	// HOST-CHECK
	if ch != nil {
		sb.WriteString("\n<b>HOST-CHECK (Check-Host.net):</b>\n")
		ruTotal, ruSuccess, ruRTT := ch.RUStats()
		worldTotal, worldSuccess, worldRTT := ch.WorldStats()

		if ruTotal > 0 {
			if ruSuccess > 0 && ruRTT > 0 {
				fmt.Fprintf(&sb, "  РФ (%d узла):       %d/%d отвечают, средний RTT %.0f мс\n", ruTotal, ruSuccess, ruTotal, float64(ruRTT.Milliseconds()))
			} else {
				fmt.Fprintf(&sb, "  РФ (%d узла):       %d/%d отвечают\n", ruTotal, ruSuccess, ruTotal)
			}
		}
		if worldTotal > 0 {
			if worldSuccess > 0 && worldRTT > 0 {
				fmt.Fprintf(&sb, "  Мир (%d узла):      %d/%d отвечают, средний RTT %.0f мс\n", worldTotal, worldSuccess, worldTotal, float64(worldRTT.Milliseconds()))
			} else {
				fmt.Fprintf(&sb, "  Мир (%d узла):      %d/%d отвечают\n", worldTotal, worldSuccess, worldTotal)
			}
		}

		if ch.Verdict != "" {
			fmt.Fprintf(&sb, "  Вывод: %s\n", escapeHTML(ch.Verdict))
		}
		if ch.PermanentLink != "" {
			fmt.Fprintf(&sb, "  Ссылка: 🔗 <a href=\"%s\">отчёт Check-Host</a>\n", escapeHTML(ch.PermanentLink))
		}
	}

	return sb.String()
}

func (b *Bot) handleDeepDiagnostics(chatID int64, msgID int, stableID string, page ...int) {
	seq := b.nextMsgSeq(chatID, msgID)
	curPage := 1
	if len(page) > 0 && page[0] > 1 {
		curPage = page[0]
	}
	b.editWithMarkup(chatID, msgID, "⏳ <b>Выполняется углублённая проверка...</b>", BackToMenuMarkup())

	go func() {
		var targetMetric metrics.ProxyMetric
		found := false
		if b.source != nil {
			for _, pm := range b.source.MetricsSnapshot() {
				if pm.StableID == stableID {
					targetMetric = pm
					found = true
					break
				}
			}
		}

		nodeName := targetMetric.NodeName
		if nodeName == "" && strings.Contains(stableID, "/") {
			nodeName = strings.Split(stableID, "/")[0]
		}

		// Also find report from cached or run diagnostics
		var rep *checker.ProxyDiagReport
		if nodeName != "" {
			nodeReports := b.getNodeDiagnosticsReports(nodeName)
			rawID := stableID
			if strings.Contains(stableID, "/") {
				rawID = strings.SplitN(stableID, "/", 2)[1]
			}
			for i := range nodeReports {
				if nodeReports[i].StableID == stableID || nodeReports[i].StableID == rawID || nodeName+"/"+nodeReports[i].StableID == stableID {
					rep = &nodeReports[i]
					break
				}
			}
		} else {
			reports := b.getDiagnosticsReports(false)
			for i := range reports {
				if reports[i].StableID == stableID {
					rep = &reports[i]
					break
				}
			}
		}

		if !found && rep != nil {
			targetMetric = metrics.ProxyMetric{
				Name:      rep.ProxyName,
				Protocol:  rep.Protocol,
				Address:   fmt.Sprintf("%s:%d", rep.Server, rep.Port),
				StableID:  rep.StableID,
				Online:    rep.Status == "online",
				LatencyMs: float64(rep.NodeHealth.TCPPing.Milliseconds()),
				NodeName:  nodeName,
			}
			found = true
		}

		if !b.isMsgSeqValid(chatID, msgID, seq) {
			return
		}

		if !found {
			b.editWithMarkup(chatID, msgID, "⚠️ Прокси не найден.", BackToMenuMarkup())
			return
		}

		var health checker.NodeHealth
		var ch *checker.CheckHostSummary
		if rep != nil {
			health = rep.NodeHealth
			ch = rep.CheckHost
		}

		// If health is empty and proxy is local, do a direct probe
		if health.ResolvedIP == "" && health.TCPPing == 0 && health.UDPPing == 0 && nodeName == "" {
			host, portStr, _ := net.SplitHostPort(targetMetric.Address)
			port, _ := strconv.Atoi(portStr)
			health = checker.ProbeNodeHealth(host, port, targetMetric.Protocol, "", "", false)
		}

		if !b.isMsgSeqValid(chatID, msgID, seq) {
			return
		}

		text := b.formatDeepDiagnostics(targetMetric, health, ch)
		b.editWithMarkup(chatID, msgID, text, DeepDiagnosticsMarkup(stableID, curPage))
	}()
}

func (b *Bot) buildDiagnosticsRichMessage(reports []checker.ProxyDiagReport) *telego.InputRichMessage {
	return b.buildDiagnosticsRichMessageWithTarget(reports, "local", b.getMasterASN())
}

func (b *Bot) buildDiagnosticsRichMessageWithTarget(reports []checker.ProxyDiagReport, target string, asn string) *telego.InputRichMessage {
	if len(reports) == 0 {
		msg := tu.RichMessage(tu.RichBlockParagraph(tu.RichTextPlain("Нет доступных прокси-хостов для проверки.")))
		return &msg
	}

	var blocks []telego.InputRichBlock

	hasNodes := b != nil && b.nodeMgr != nil && len(b.nodeMgr.Nodes()) > 0
	title := ""
	if target != "" && target != "local" {
		title = fmt.Sprintf("📊 Подробная сводка — 🖥 Нода %s (%d прокси-хостов)", target, len(reports))
	} else if hasNodes {
		title = fmt.Sprintf("📊 Подробная сводка — 🏠 Мастер (%d прокси-хостов)", len(reports))
	} else {
		title = fmt.Sprintf("📊 Подробная сводка (%d прокси-хостов)", len(reports))
	}

	// 1. Heading
	blocks = append(blocks, tu.RichBlockSectionHeading(
		tu.RichTextBold(tu.RichTextPlain(title)),
		2,
	))

	if asn != "" {
		blocks = append(blocks, tu.RichBlockParagraph(
			tu.RichTextItalic(tu.RichTextPlain("ASN: "+asn)),
		))
	}

	// 2. Table: Прокси-хост | Протокол | Пинг | Статус
	headerRow := []telego.RichBlockTableCell{
		tu.RichBlockTableCell(tu.RichTextBold(tu.RichTextPlain("Прокси-хост"))).WithIsHeader(),
		tu.RichBlockTableCell(tu.RichTextBold(tu.RichTextPlain("Прот."))).WithIsHeader(),
		tu.RichBlockTableCell(tu.RichTextBold(tu.RichTextPlain("Пинг"))).WithIsHeader(),
		tu.RichBlockTableCell(tu.RichTextBold(tu.RichTextPlain("Статус"))).WithIsHeader(),
	}

	var tableRows [][]telego.RichBlockTableCell
	tableRows = append(tableRows, headerRow)

	onlineCount, offlineCount, disabledCount := 0, 0, 0
	for _, rep := range reports {
		latencyText := "—"
		for _, tr := range rep.Targets {
			if tr.Success {
				latencyText = fmt.Sprintf("%.0f ms", float64(tr.Latency.Milliseconds()))
				break
			}
		}

		statusText := "🟢 Доступен"
		switch rep.Status {
		case "offline":
			statusText = "🔴 Недоступен"
			offlineCount++
		case "degraded":
			statusText = "🟡 Ошибка"
			offlineCount++
		case "disabled":
			statusText = "⏸️ Отключён"
			latencyText = "—"
			disabledCount++
		default:
			onlineCount++
		}

		proto := strings.ToUpper(rep.Protocol)
		if proto == "" {
			proto = "PROXY"
		}

		tableRows = append(tableRows, []telego.RichBlockTableCell{
			tu.RichBlockTableCell(tu.RichTextPlain(rep.ProxyName)),
			tu.RichBlockTableCell(tu.RichTextPlain(proto)),
			tu.RichBlockTableCell(tu.RichTextPlain(latencyText)),
			tu.RichBlockTableCell(tu.RichTextPlain(statusText)),
		})
	}

	table := tu.RichBlockTable(tableRows...).WithIsBordered().WithIsStriped().WithIsCompact()
	blocks = append(blocks, table)
	blocks = append(blocks, tu.RichBlockDivider())

	summaryLine := fmt.Sprintf("💡 В сети: %d | Сбоев: %d", onlineCount, offlineCount)
	if disabledCount > 0 {
		summaryLine += fmt.Sprintf(" | Отключено: %d", disabledCount)
	}
	blocks = append(blocks, tu.RichBlockParagraph(tu.RichTextItalic(tu.RichTextPlain(summaryLine))))
	blocks = append(blocks, tu.RichBlockParagraph(tu.RichTextItalic(tu.RichTextPlain("Нажмите «📑 Детальный отчёт» для постраничного разбора этапов."))))

	msg := tu.RichMessage(blocks...)
	return &msg
}

func (b *Bot) buildDiagnosticsDetailsRichMessage(reports []checker.ProxyDiagReport, page int) (*telego.InputRichMessage, int) {
	return b.buildDiagnosticsDetailsRichMessageWithTarget(reports, "local", page)
}

func (b *Bot) buildDiagnosticsDetailsRichMessageWithTarget(reports []checker.ProxyDiagReport, target string, page int) (*telego.InputRichMessage, int) {
	if len(reports) == 0 {
		msg := tu.RichMessage(tu.RichBlockParagraph(tu.RichTextPlain("Нет доступных прокси-хостов для проверки.")))
		return &msg, 1
	}

	totalPages := (len(reports) + diagRichDetailsPageSize - 1) / diagRichDetailsPageSize
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * diagRichDetailsPageSize
	end := start + diagRichDetailsPageSize
	if end > len(reports) {
		end = len(reports)
	}

	var blocks []telego.InputRichBlock

	hasNodes := b != nil && b.nodeMgr != nil && len(b.nodeMgr.Nodes()) > 0
	nodePrefix := ""
	if target != "" && target != "local" {
		nodePrefix = fmt.Sprintf(" — 🖥 Нода %s", target)
	} else if hasNodes {
		nodePrefix = " — 🏠 Мастер"
	}

	blocks = append(blocks, tu.RichBlockSectionHeading(
		tu.RichTextBold(tu.RichTextPlain(fmt.Sprintf("📑 Детальный отчёт%s (Стр. %d из %d, всего %d)", nodePrefix, page, totalPages, len(reports)))),
		2,
	))

	for _, rep := range reports[start:end] {
		proto := strings.ToUpper(rep.Protocol)
		if proto == "" {
			proto = "PROXY"
		}

		if rep.Disabled || rep.Status == "disabled" {
			summary := tu.RichTextBold(tu.RichTextPlain(fmt.Sprintf("⏸️ %s (%s) — отключён", rep.ProxyName, proto)))
			blocks = append(blocks, tu.RichBlockDetails(
				summary,
				tu.RichBlockParagraph(tu.RichTextItalic(tu.RichTextPlain("Проверка данного прокси отключена в конфигурации."))),
			))
			continue
		}

		icon := "🟢"
		statusDesc := "доступен"
		switch rep.Status {
		case "offline":
			icon = "🔴"
			statusDesc = "недоступен"
		case "degraded":
			icon = "🟡"
			statusDesc = "ошибка"
		default:
			for _, tr := range rep.Targets {
				if tr.Success {
					statusDesc = fmt.Sprintf("%.0f ms", float64(tr.Latency.Milliseconds()))
					break
				}
			}
		}

		summary := tu.RichTextBold(tu.RichTextPlain(fmt.Sprintf("%s %s (%s) — %s", icon, rep.ProxyName, proto, statusDesc)))

		var detailLines []string

		// 1. DNS
		if rep.NodeHealth.DNSErr != "" {
			detailLines = append(detailLines, fmt.Sprintf("DNS: ❌ %s", rep.NodeHealth.DNSErr))
		} else if rep.NodeHealth.ResolvedIP != "" {
			if rep.NodeHealth.DNSLatency > 0 {
				detailLines = append(detailLines, fmt.Sprintf("DNS: ✅ %s (%.0f ms)", rep.NodeHealth.ResolvedIP, float64(rep.NodeHealth.DNSLatency.Milliseconds())))
			} else {
				detailLines = append(detailLines, fmt.Sprintf("DNS: ✅ %s", rep.NodeHealth.ResolvedIP))
			}
		}

		// 2. Transport / TCP
		if checker.IsUDPProto(rep.Protocol) {
			if rep.Port > 0 {
				detailLines = append(detailLines, fmt.Sprintf("Порт (%d): ⚡ UDP / QUIC", rep.Port))
			}
		} else if rep.Port > 0 {
			if rep.NodeHealth.TCPErr != "" {
				detailLines = append(detailLines, fmt.Sprintf("TCP (%d): ❌ %s", rep.Port, rep.NodeHealth.TCPErr))
			} else if rep.NodeHealth.TCPPing > 0 {
				detailLines = append(detailLines, fmt.Sprintf("TCP (%d): ✅ %.0f ms", rep.Port, float64(rep.NodeHealth.TCPPing.Milliseconds())))
			}
		}

		// 3. TLS
		if rep.NodeHealth.TLSErr != "" {
			detailLines = append(detailLines, fmt.Sprintf("TLS: ❌ %s", rep.NodeHealth.TLSErr))
		} else if rep.NodeHealth.TLSLatency > 0 {
			detailLines = append(detailLines, fmt.Sprintf("TLS: ✅ %.0f ms", float64(rep.NodeHealth.TLSLatency.Milliseconds())))
		}

		// 4. Targets
		for _, tr := range rep.Targets {
			siteName := simplifyTargetName(tr.URL)
			if tr.Success {
				detailLines = append(detailLines, fmt.Sprintf("%s: ✅ %.0f ms", siteName, float64(tr.Latency.Milliseconds())))
			} else {
				detailLines = append(detailLines, fmt.Sprintf("%s: ❌ %s", siteName, tr.Error))
			}
		}

		// 5. Check-Host
		if rep.CheckHost != nil {
			ruStatus := "❌"
			if rep.CheckHost.RUAvailable {
				ruStatus = "✅"
			}
			worldStatus := "❌"
			if rep.CheckHost.WorldAvailable {
				worldStatus = "✅"
			}
			if rep.NodeHealth.DNSErr != "" && !rep.CheckHost.RUAvailable && !rep.CheckHost.WorldAvailable {
				detailLines = append(detailLines, "Check-Host: РФ ❌ | Мир ❌ (домен не резолвится внешними узлами)")
			} else {
				detailLines = append(detailLines, fmt.Sprintf("Check-Host: РФ %s | Мир %s", ruStatus, worldStatus))
			}
		}

		// 6. Verdict
		if rep.Verdict != "" {
			detailLines = append(detailLines, fmt.Sprintf("Вердикт: %s", rep.Verdict))
		}

		// 7. Downtime / Flapping
		if rep.Status == "offline" && b != nil && b.tracker != nil {
			dt := b.tracker.GetDowntime(rep.StableID, b.now())
			if dt > 0 {
				detailLines = append(detailLines, fmt.Sprintf("Недоступен уже: %s", FormatDowntime(dt)))
			}
		}

		if len(detailLines) == 0 {
			detailLines = append(detailLines, "Данные проверки отсутствуют")
		}

		blocks = append(blocks, tu.RichBlockDetails(
			summary,
			tu.RichBlockPreformatted(tu.RichTextPlain(strings.Join(detailLines, "\n"))),
		))
	}

	msg := tu.RichMessage(blocks...)
	return &msg, totalPages
}
