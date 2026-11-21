package telegram

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/checker"
)

func (b *Bot) buildDiagnosticsRichMessage(reports []checker.ProxyDiagReport) *telego.InputRichMessage {
	return b.buildDiagnosticsRichMessageWithTarget(reports, "local", b.getMasterASN())
}

// richDiagCacheNote adapts the HTML cache-age note (diagCacheAgeNote:
// «\n\n<i>данные от 15:04</i>») to the rich RAW-contract plain text — rich
// entities carry raw text without HTML tags or escaping (the same contract
// as detailLines), so the <i> wrapper and the HTML-path blank-line padding
// are stripped here; the italic styling is applied by the block that renders
// the note. Plain-form notes pass through unchanged.
func richDiagCacheNote(note string) string {
	note = strings.TrimSpace(note)
	note = strings.TrimPrefix(note, "<i>")
	note = strings.TrimSuffix(note, "</i>")
	return strings.TrimSpace(note)
}

// appendDiagCacheNoteBlock appends the trailing italic staleness paragraph
// («данные от HH:MM») for cached rich renders (wave M9.3 follow-up: rich
// parity with the plain branch, which appends diagCacheAgeNote to the text).
// An empty note is a no-op — freshly probed renders stay note-free.
func appendDiagCacheNoteBlock(blocks []telego.InputRichBlock, cacheNote string) []telego.InputRichBlock {
	if note := richDiagCacheNote(cacheNote); note != "" {
		blocks = append(blocks, tu.RichBlockParagraph(tu.RichTextItalic(tu.RichTextPlain(note))))
	}
	return blocks
}

func (b *Bot) buildDiagnosticsRichMessageWithTarget(reports []checker.ProxyDiagReport, target string, asn string) *telego.InputRichMessage {
	return b.buildDiagnosticsRichMessageWithTargetNote(reports, target, asn, "")
}

// buildDiagnosticsRichMessageWithTargetNote is the cached-render variant of
// the rich summary builder: cacheNote (the diagCacheAgeNote HTML line, or a
// plain-form equivalent) is rendered as a trailing italic «данные от HH:MM»
// paragraph — see richDiagCacheNote for the RAW-contract adaptation.
func (b *Bot) buildDiagnosticsRichMessageWithTargetNote(reports []checker.ProxyDiagReport, target string, asn string, cacheNote string) *telego.InputRichMessage {
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
				latencyText = fmtLatencyUnit(tr.Latency.Milliseconds())
				break
			}
		}

		statusText := "🟢 Доступен"
		switch rep.Status {
		case "offline":
			statusText = "🔴 Недоступен"
			offlineCount++
		case "degraded":
			statusText = "🟡 " + degradedLabel()
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
	blocks = appendDiagCacheNoteBlock(blocks, cacheNote)

	msg := tu.RichMessage(blocks...)
	return &msg
}

func (b *Bot) buildDiagnosticsDetailsRichMessage(reports []checker.ProxyDiagReport, page int) (*telego.InputRichMessage, int) {
	return b.buildDiagnosticsDetailsRichMessageWithTarget(reports, "local", page)
}

func (b *Bot) buildDiagnosticsDetailsRichMessageWithTarget(reports []checker.ProxyDiagReport, target string, page int) (*telego.InputRichMessage, int) {
	return b.buildDiagnosticsDetailsRichMessageWithTargetNote(reports, target, page, "")
}

// buildDiagnosticsDetailsRichMessageWithTargetNote is the cached-render
// variant of the rich details builder (wave M9.3 follow-up): cacheNote — the
// HTML staleness line produced by diagCacheAgeNote for the plain path — is
// appended as a trailing italic «данные от HH:MM» paragraph, the same parity
// the plain branch gets from `pageText += cacheNote`. Rich entities are RAW
// plain text (no HTML tags, no escaping — the same contract as detailLines),
// so richDiagCacheNote strips the <i> wrapper before rendering.
func (b *Bot) buildDiagnosticsDetailsRichMessageWithTargetNote(reports []checker.ProxyDiagReport, target string, page int, cacheNote string) (*telego.InputRichMessage, int) {
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
					statusDesc = fmtLatencyUnit(tr.Latency.Milliseconds())
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
				detailLines = append(detailLines, fmt.Sprintf("DNS: ✅ %s (%s)", rep.NodeHealth.ResolvedIP, fmtLatencyUnit(rep.NodeHealth.DNSLatency.Milliseconds())))
			} else {
				detailLines = append(detailLines, fmt.Sprintf("DNS: ✅ %s", rep.NodeHealth.ResolvedIP))
			}
		}

		// 2. Transport / TCP
		if checker.IsUDPProto(rep.Protocol) {
			if rep.Port > 0 {
				if rep.NodeHealth.UDPErr != "" {
					if rep.Status == "online" || rep.Status == "degraded" {
						detailLines = append(detailLines, fmt.Sprintf("Порт (%d): ⚡ UDP / QUIC ⚠️ %s (туннель активен)", rep.Port, rep.NodeHealth.UDPErr))
					} else {
						detailLines = append(detailLines, fmt.Sprintf("Порт (%d): ⚡ UDP / QUIC ❌ %s", rep.Port, rep.NodeHealth.UDPErr))
					}
				} else {
					detailLines = append(detailLines, fmt.Sprintf("Порт (%d): ⚡ UDP / QUIC", rep.Port))
				}
			}
		} else if rep.Port > 0 {
			if rep.NodeHealth.TCPErr != "" {
				if rep.Status == "online" || rep.Status == "degraded" {
					detailLines = append(detailLines, fmt.Sprintf("TCP (%d): ⚠️ %s (фильтрация провайдером или защита от сканирования, туннель активен)", rep.Port, rep.NodeHealth.TCPErr))
				} else {
					detailLines = append(detailLines, fmt.Sprintf("TCP (%d): ❌ %s", rep.Port, rep.NodeHealth.TCPErr))
				}
			} else if rep.NodeHealth.TCPPing > 0 {
				detailLines = append(detailLines, fmt.Sprintf("TCP (%d): ✅ %s", rep.Port, fmtLatencyUnit(rep.NodeHealth.TCPPing.Milliseconds())))
			}
		}

		// 3. TLS
		if rep.NodeHealth.TLSErr != "" {
			detailLines = append(detailLines, fmt.Sprintf("TLS: ❌ %s", rep.NodeHealth.TLSErr))
		} else if rep.NodeHealth.TLSLatency > 0 {
			detailLines = append(detailLines, fmt.Sprintf("TLS: ✅ %s", fmtLatencyUnit(rep.NodeHealth.TLSLatency.Milliseconds())))
		}

		// 4. Targets
		for _, tr := range rep.Targets {
			siteName := simplifyTargetName(tr.URL)
			if tr.Success {
				detailLines = append(detailLines, fmt.Sprintf("%s: ✅ %s", siteName, fmtLatencyUnit(tr.Latency.Milliseconds())))
			} else {
				errText := tr.Error
				if (rep.Status == "online" || rep.Status == "degraded") && !strings.Contains(errText, "Туннель не поднят") {
					errText = formatTargetErrorInsideTunnel(errText, tr.StatusCode)
				}
				detailLines = append(detailLines, fmt.Sprintf("%s: ❌ %s", siteName, errText))
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

		// 5b. Cheburcheck active RF probes (plain-text rich context)
		if rep.Chebur != nil {
			verdict := rep.Chebur.Verdict
			if verdict == "" {
				verdict = "нет данных"
			}
			detailLines = append(detailLines, fmt.Sprintf("Cheburcheck: %s", verdict))
			for _, r := range rep.Chebur.Results {
				meta := ""
				switch {
				case r.Region != "" && r.Provider != "":
					meta = fmt.Sprintf(" (%s/%s)", r.Region, r.Provider)
				case r.Region != "":
					meta = fmt.Sprintf(" (%s)", r.Region)
				case r.Provider != "":
					meta = fmt.Sprintf(" (%s)", r.Provider)
				}
				cached := ""
				if r.Cached {
					cached = " (кеш)"
				}
				detailLines = append(detailLines, fmt.Sprintf("  %s %s%s %s%s",
					cheburProbeIcon(r.Verdicts), r.Name, meta, cheburProbeLabels(r.Verdicts), cached))
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

	blocks = appendDiagCacheNoteBlock(blocks, cacheNote)

	msg := tu.RichMessage(blocks...)
	return &msg, totalPages
}
