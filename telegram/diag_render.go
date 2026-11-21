package telegram

import (
	"fmt"
	"strings"
	"time"

	"xray-checker/checker"
	"xray-checker/metrics"
)

const diagPageSize = 5
const diagRichDetailsPageSize = 15

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
		fmt.Fprintf(sb, "⏸️ <b>%s</b> <i>(%s)</i> — <i>проверка отключена</i>\n\n", escapeHTML(rep.ProxyName), escapeHTML(proto))
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

	fmt.Fprintf(sb, "%s <b>%s</b> <i>(%s)</i>%s\n", icon, escapeHTML(rep.ProxyName), escapeHTML(proto), flappingSuffix)

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
		fmt.Fprintf(sb, "  • Статус: 🟡 %s\n", degradedLabel())
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
				fmt.Fprintf(sb, "  • Доступен: 🟢 %s (p95: %s, p99: %s, Σ=%.0f)\n", fmtLatencyIcon(int64(latMs)), fmtLatencyUnit(int64(p95)), fmtLatencyUnit(int64(p99)), jitter)
			} else if ls.Count() >= 5 {
				p95 := ls.Percentile(0.95)
				jitter := ls.StdDev()
				fmt.Fprintf(sb, "  • Доступен: 🟢 %s (p95: %s, Σ=%.0f)\n", fmtLatencyIcon(int64(latMs)), fmtLatencyUnit(int64(p95)), jitter)
			} else {
				fmt.Fprintf(sb, "  • Доступен: 🟢 %s\n", fmtLatencyIcon(int64(latMs)))
			}
		} else {
			fmt.Fprintf(sb, "  • Доступен: 🟢 %s\n", fmtLatencyIcon(int64(latMs)))
		}
	}

	// 1. DNS
	if rep.NodeHealth.DNSErr != "" {
		fmt.Fprintf(sb, "  • DNS: ❌ %s\n", escapeHTML(rep.NodeHealth.DNSErr))
	} else if rep.NodeHealth.ResolvedIP != "" {
		if rep.NodeHealth.DNSLatency > 0 {
			fmt.Fprintf(sb, "  • DNS: ✅ <code>%s</code> (%s)\n", escapeHTML(rep.NodeHealth.ResolvedIP), fmtLatencyUnit(rep.NodeHealth.DNSLatency.Milliseconds()))
		} else {
			fmt.Fprintf(sb, "  • DNS: ✅ <code>%s</code>\n", escapeHTML(rep.NodeHealth.ResolvedIP))
		}
	}

	// 2. Transport Protocol / TCP Ping
	if checker.IsUDPProto(rep.Protocol) {
		if rep.Port > 0 {
			if rep.NodeHealth.UDPErr != "" {
				if rep.Status == "online" || rep.Status == "degraded" {
					fmt.Fprintf(sb, "  • Порт (%d): ⚡ UDP / QUIC ⚠️ %s (туннель активен)\n", rep.Port, escapeHTML(rep.NodeHealth.UDPErr))
				} else {
					fmt.Fprintf(sb, "  • Порт (%d): ⚡ UDP / QUIC ❌ %s\n", rep.Port, escapeHTML(rep.NodeHealth.UDPErr))
				}
			} else {
				fmt.Fprintf(sb, "  • Порт (%d): ⚡ UDP / QUIC\n", rep.Port)
			}
		}
	} else if rep.Port > 0 {
		if rep.NodeHealth.TCPErr != "" {
			if rep.Status == "online" || rep.Status == "degraded" {
				fmt.Fprintf(sb, "  • TCP (%d): ⚠️ %s (фильтрация провайдером или защита от сканирования / anti-probe на сервере, но туннель активен)\n", rep.Port, escapeHTML(rep.NodeHealth.TCPErr))
			} else {
				fmt.Fprintf(sb, "  • TCP (%d): ❌ %s\n", rep.Port, escapeHTML(rep.NodeHealth.TCPErr))
			}
		} else if rep.NodeHealth.TCPPing > 0 {
			fmt.Fprintf(sb, "  • TCP (%d): ✅ %s\n", rep.Port, fmtLatencyUnit(rep.NodeHealth.TCPPing.Milliseconds()))
		}
	}

	// 3. TLS Handshake (if attempted)
	if rep.NodeHealth.TLSErr != "" {
		fmt.Fprintf(sb, "  • TLS: ❌ %s\n", escapeHTML(rep.NodeHealth.TLSErr))
	} else if rep.NodeHealth.TLSLatency > 0 {
		fmt.Fprintf(sb, "  • TLS: ✅ %s\n", fmtLatencyUnit(rep.NodeHealth.TLSLatency.Milliseconds()))
	}

	// 4. Target Endpoints
	for _, tr := range rep.Targets {
		siteName := simplifyTargetName(tr.URL)
		if tr.Success {
			fmt.Fprintf(sb, "  • %s: ✅ %s\n", escapeHTML(siteName), fmtLatencyUnit(tr.Latency.Milliseconds()))
		} else {
			errText := tr.Error
			if (rep.Status == "online" || rep.Status == "degraded") && !strings.Contains(errText, "Туннель не поднят") {
				errText = formatTargetErrorInsideTunnel(errText, tr.StatusCode)
			}
			fmt.Fprintf(sb, "  • %s: ❌ %s\n", escapeHTML(siteName), escapeHTML(errText))
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

	// 5b. Cheburcheck active RF probes (present only when scanners are configured)
	if rep.Chebur != nil {
		sb.WriteString(formatCheburDiagBlock(rep.Chebur))
	}

	// 6. Verdict
	if rep.Verdict != "" {
		fmt.Fprintf(sb, "  💡 <i>Вердикт: %s</i>\n", escapeHTML(rep.Verdict))
	}

	sb.WriteString("\n")
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
				latencyText = fmtLatencyIcon(tr.Latency.Milliseconds())
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
			statusDesc = degradedLabel()
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

		fmt.Fprintf(&sb, "%s <b>%s</b> (%s) — %s\n", icon, escapeHTML(rep.ProxyName), escapeHTML(proto), statusDesc)
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
	fmt.Fprintf(&sb, "<b>ПРОТОКОЛ:</b>  %s\n", escapeHTML(proto))
	fmt.Fprintf(&sb, "<b>ТРАНСПОРТ:</b> %s\n", transport)
	fmt.Fprintf(&sb, "<b>АДРЕС:</b>     %s\n\n", escapeHTML(pm.Address))

	sb.WriteString("<b>ЭТАПЫ ПРОВЕРКИ:</b>\n<pre>")

	// 1. DNS-резолв
	if health.DNSErr != "" {
		fmt.Fprintf(&sb, "  1. DNS-резолв ........... —      ❌  %s\n", escapeHTML(health.DNSErr))
	} else if health.ResolvedIP != "" {
		if health.DNSLatency > 0 {
			fmt.Fprintf(&sb, "  1. DNS-резолв ........... %.0f мс  ✅\n", float64(health.DNSLatency.Milliseconds()))
		} else {
			fmt.Fprintf(&sb, "  1. DNS-резолв ........... ✅ (%s)\n", escapeHTML(health.ResolvedIP))
		}
	} else {
		sb.WriteString("  1. DNS-резолв ........... —      (не требуется)\n")
	}

	// 2. TCP / UDP рукопожатие
	if checker.IsUDPProto(pm.Protocol) {
		if health.UDPErr != "" {
			if pm.Online || pm.CanConnect {
				fmt.Fprintf(&sb, "  2. UDP-датаграмма ...... —      ⚠️  %s (туннель активен)\n", escapeHTML(health.UDPErr))
			} else {
				fmt.Fprintf(&sb, "  2. UDP-датаграмма ...... —      ❌  %s\n", escapeHTML(health.UDPErr))
			}
		} else if health.UDPPing > 0 {
			fmt.Fprintf(&sb, "  2. UDP-датаграмма ...... %.0f мс  ✅\n", float64(health.UDPPing.Milliseconds()))
		} else {
			sb.WriteString("  2. UDP-датаграмма ...... —\n")
		}
	} else {
		if health.TCPErr != "" {
			if pm.Online || pm.CanConnect {
				fmt.Fprintf(&sb, "  2. TCP-рукопожатие ...... —      ⚠️  %s (фильтрация провайдером или скрытый порт / anti-probe, туннель активен)\n", escapeHTML(health.TCPErr))
			} else {
				fmt.Fprintf(&sb, "  2. TCP-рукопожатие ...... —      ❌  %s\n", escapeHTML(health.TCPErr))
			}
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
		fmt.Fprintf(&sb, "  6. Полный ответ .......... %.0f мс\n</pre>\n\n", pm.LatencyMs)
	} else {
		sb.WriteString("  6. Полный ответ .......... —\n</pre>\n\n")
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
				fmt.Fprintf(&sb, "  • p50: %.0f мс | p95: %.0f мс | p99: %.0f мс (Σ=%.0f, n=%d)\n",
					ls.Percentile(0.50), ls.Percentile(0.95), ls.Percentile(0.99), ls.StdDev(), ls.Count())
			} else {
				fmt.Fprintf(&sb, "  • p50: %.0f мс | p95: %.0f мс (Σ=%.0f, n=%d)\n",
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
