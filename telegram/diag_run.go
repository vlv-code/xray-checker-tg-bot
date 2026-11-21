package telegram

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"xray-checker/checker"
	"xray-checker/metrics"
)

// proxyNotFoundText is the hard-error message for a deep-diagnostics target
// missing from the current snapshot: ❌ (hard-error icon, never ⚠️) plus one
// action hint pointing the user back to the refreshed list.
func proxyNotFoundText() string {
	return renderIcon(IconHardError) + " Прокси не найден — список изменился, вернитесь и нажмите 🔄 Обновить."
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
			b.editWithMarkup(chatID, msgID, proxyNotFoundText(), BackToMenuMarkup())
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
		if rep != nil && rep.Chebur != nil {
			// Cheburcheck enrichment below the Check-Host block (escaped HTML).
			// Wave H3: the header dropped the 🧀 icon and the «РФ» mention.
			text += "\n<b>CHEBURCHECK (активный пробинг):</b>\n" + formatCheburDiagBlock(rep.Chebur)
		}
		b.editWithMarkup(chatID, msgID, text, DeepDiagnosticsMarkup(stableID, curPage))
	}()
}
