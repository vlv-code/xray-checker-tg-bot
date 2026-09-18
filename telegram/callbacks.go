package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/logger"
)

func (b *Bot) handleCallbackQuery(cb *telego.CallbackQuery) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.GetChat().ID
	if !b.allowedChatIDs[chatID] {
		return
	}

	msgID := cb.Message.GetMessageID()

	ct := ChatTarget{ChatID: chatID}
	if m := cb.Message.Message(); m != nil {
		ct.ThreadID = m.MessageThreadID
	}

	// Handle non-modifying callbacks with informational toasts
	if strings.HasPrefix(cb.Data, "menu:diag:noop:") {
		parts := strings.Split(strings.TrimPrefix(cb.Data, "menu:diag:noop:"), ":")
		if len(parts) == 2 {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText(fmt.Sprintf("Страница %s из %s", parts[0], parts[1])))
		} else {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID))
		}
		return
	}
	if cb.Data == "menu:noop" {
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID))
		return
	}

	// Acknowledge callback immediately to dismiss spinner, EXCEPT for callbacks that return their own custom toast.
	isToastCallback := strings.HasPrefix(cb.Data, "menu:toggle_proxy:") ||
		strings.HasPrefix(cb.Data, "menu:toggle_host:") ||
		cb.Data == "menu:checkhost:run_now"

	if !isToastCallback {
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID))
	}

	b.invalidateMsgSeq(chatID, msgID)

	switch cb.Data {
	case "menu:main":
		b.editWithMarkup(chatID, msgID, b.getMenuText(), MainMenuMarkup())
	case "menu:settings":
		b.editWithMarkup(chatID, msgID, b.getSettingsText(), SettingsMenuMarkup(b.subs != nil))
	case "menu:status":
		b.editWithMarkup(chatID, msgID, b.getStatusText(), StatusMenuMarkup())
	case "menu:diag", "menu:diag:summary":
		seq := b.nextMsgSeq(chatID, msgID)
		b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование подробной сводки...</b>\nПожалуйста, подождите несколько секунд.", BackToMenuMarkup())
		go func() {
			reports := b.getDiagnosticsReports(false)
			if !b.isMsgSeqValid(chatID, msgID, seq) {
				return
			}
			deepLinks := getDeepLinks(reports, 3)
			markup := RichReportMarkup(deepLinks...)
			if b.isRichMode() {
				rich := b.buildDiagnosticsRichMessage(reports)
				b.showRichReport(ct, msgID, rich, markup)
				return
			}
			summaryText := b.getDiagnosticsSummaryText(reports)
			b.editWithMarkup(chatID, msgID, summaryText, markup)
		}()
	case "menu:diag:rich":
		seq := b.nextMsgSeq(chatID, msgID)
		b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование подробной сводки...</b>\nПожалуйста, подождите несколько секунд.", BackToMenuMarkup())
		go func() {
			reports := b.getDiagnosticsReports(false)
			if !b.isMsgSeqValid(chatID, msgID, seq) {
				return
			}
			deepLinks := getDeepLinks(reports, 3)
			markup := RichReportMarkup(deepLinks...)
			rich := b.buildDiagnosticsRichMessage(reports)
			b.showRichReport(ct, msgID, rich, markup)
		}()
	case "menu:stats":
		b.editWithMarkup(chatID, msgID, b.getStatsOverviewText(), StatsMenuMarkup())
	case "menu:stats:incidents":
		b.editWithMarkup(chatID, msgID, b.getIncidentsText(), BackToStatsMarkup())
	case "menu:stats:top":
		b.editWithMarkup(chatID, msgID, b.getTopProblematicText(), BackToStatsMarkup())
	case "menu:stats:protocols":
		b.editWithMarkup(chatID, msgID, b.getProtocolsStatsText(), BackToStatsMarkup())
	case "menu:stats:heatmap":
		b.editWithMarkup(chatID, msgID, b.getHeatmapText(), BackToStatsMarkup())
	case "menu:timezone":
		b.editWithMarkup(chatID, msgID, b.getTimezoneText(), TimezoneMarkup(b.GetConfig().Timezone))
	case "menu:quiet":
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:toggle":
		_ = b.updateConfig(func(c *BotConfig) {
			c.QuietHoursEnabled = !c.QuietHoursEnabled
		})
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:snooze:1h":
		until := time.Now().Add(1 * time.Hour).Unix()
		_ = b.updateConfig(func(c *BotConfig) {
			c.QuietSnoozeUntil = until
		})
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:snooze:4h":
		until := time.Now().Add(4 * time.Hour).Unix()
		_ = b.updateConfig(func(c *BotConfig) {
			c.QuietSnoozeUntil = until
		})
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:snooze:morning":
		endHour, endMin, err := parseTimeOfDay(b.GetConfig().QuietHoursEnd)
		if err != nil {
			endHour, endMin = 8, 0
		}
		now := b.now()
		nextMorning := time.Date(now.Year(), now.Month(), now.Day(), endHour, endMin, 0, 0, b.loc())
		if !now.Before(nextMorning) {
			nextMorning = nextMorning.Add(24 * time.Hour)
		}
		until := nextMorning.Unix()
		_ = b.updateConfig(func(c *BotConfig) {
			c.QuietSnoozeUntil = until
		})
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:quiet:unsnooze":
		_ = b.updateConfig(func(c *BotConfig) {
			c.QuietSnoozeUntil = 0
		})
		b.editWithMarkup(chatID, msgID, b.getQuietHoursText(), QuietHoursMarkup(b.GetConfig()))
	case "menu:alert_mode":
		b.editWithMarkup(chatID, msgID, b.getAlertModeText(), AlertModeMarkup(b.GetConfig()))
	case "menu:alert_mode:live":
		_ = b.updateConfig(func(c *BotConfig) {
			c.AlertMode = AlertModeLive
		})
		b.editWithMarkup(chatID, msgID, b.getAlertModeText(), AlertModeMarkup(b.GetConfig()))
	case "menu:alert_mode:clean":
		_ = b.updateConfig(func(c *BotConfig) {
			c.AlertMode = AlertModeClean
		})
		b.editWithMarkup(chatID, msgID, b.getAlertModeText(), AlertModeMarkup(b.GetConfig()))
	case "menu:targets":
		b.editWithMarkup(chatID, msgID, b.getTargetsText(), TargetsMenuMarkup())
	case "menu:interval":
		b.editWithMarkup(chatID, msgID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
	case "menu:subs":
		b.editWithMarkup(chatID, msgID, b.getSubsText(), BackToSettingsMarkup())
	case "menu:checkhost":
		snapshot := b.source.MetricsSnapshot()
		b.editWithMarkup(chatID, msgID, b.getCheckHostMenuText(), CheckHostMenuMarkup(snapshot))
	case "menu:checkhost_cfg":
		b.editWithMarkup(chatID, msgID, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(b.GetConfig()))
	case "menu:checkhost:toggle_bg":
		_ = b.updateConfig(func(c *BotConfig) {
			c.CheckHostBgEnabled = !c.CheckHostBgEnabled
		})
		b.editWithMarkup(chatID, msgID, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(b.GetConfig()))
	case "menu:checkhost:toggle_alert":
		_ = b.updateConfig(func(c *BotConfig) {
			c.CheckHostAlertEnabled = !c.CheckHostAlertEnabled
		})
		b.editWithMarkup(chatID, msgID, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(b.GetConfig()))
	case "menu:checkhost:run":
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("🚀 Запуск фоновой проверки Check-Host..."))
		go b.RunCheckHostAudit()
	case "menu:nodes", "menu:nodes:refresh":
		b.editWithMarkup(chatID, msgID, b.getNodesMainView(), NodesMainMenuMarkup())
	case "menu:nodes:install":
		b.editWithMarkup(chatID, msgID, b.getNodesInstallGuideView(), NodesInstallMarkup())
	case "menu:nodes:health":
		b.editWithMarkup(chatID, msgID, b.getNodesHealthView(), NodesHealthMarkup())
	case "menu:nodes:settings":
		b.editWithMarkup(chatID, msgID, b.getNodesSettingsView(), NodesSettingsMarkup(b.GetConfig()))
	case "menu:nodes:toggle_sync":
		_ = b.updateConfig(func(c *BotConfig) {
			c.NodeSyncEnabled = !c.NodeSyncEnabled
		})
		b.editWithMarkup(chatID, msgID, b.getNodesSettingsView(), NodesSettingsMarkup(b.GetConfig()))
	case "menu:nodes:toggle_alerts":
		_ = b.updateConfig(func(c *BotConfig) {
			c.NodeAlertsEnabled = !c.NodeAlertsEnabled
		})
		b.editWithMarkup(chatID, msgID, b.getNodesSettingsView(), NodesSettingsMarkup(b.GetConfig()))
	case "menu:nodes:toggle_proxy_alerts":
		_ = b.updateConfig(func(c *BotConfig) {
			c.NodeProxyAlertsChat = !c.NodeProxyAlertsChat
		})
		b.editWithMarkup(chatID, msgID, b.getNodesSettingsView(), NodesSettingsMarkup(b.GetConfig()))
	default:
		if strings.HasPrefix(cb.Data, "menu:tz:") {
			tz := strings.TrimPrefix(cb.Data, "menu:tz:")
			_ = b.updateConfig(func(c *BotConfig) {
				c.Timezone = tz
			})
			b.editWithMarkup(chatID, msgID, b.getTimezoneText(), TimezoneMarkup(b.GetConfig().Timezone))
		} else if strings.HasPrefix(cb.Data, "menu:checkhost:int:") {
			intStr := strings.TrimPrefix(cb.Data, "menu:checkhost:int:")
			if hours, err := strconv.Atoi(intStr); err == nil && hours > 0 {
				_ = b.updateConfig(func(c *BotConfig) {
					c.CheckHostIntervalHours = hours
				})
				b.editWithMarkup(chatID, msgID, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(b.GetConfig()))
			}
		} else if strings.HasPrefix(cb.Data, "menu:disabled_proxies:") {
			pageStr := strings.TrimPrefix(cb.Data, "menu:disabled_proxies:")
			if strings.Contains(pageStr, "noop") {
				return
			}
			page, _ := strconv.Atoi(pageStr)
			if page <= 0 {
				page = 1
			}
			text, markup := b.getDisabledProxiesView(page)
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:toggle_proxy:") {
			rest := strings.TrimPrefix(cb.Data, "menu:toggle_proxy:")
			idx := strings.LastIndex(rest, ":")
			if idx != -1 {
				stableID := rest[:idx]
				page, _ := strconv.Atoi(rest[idx+1:])
				if page <= 0 {
					page = 1
				}
				if b.configMgr != nil {
					disabled, err := b.configMgr.ToggleProxy(stableID)
					if err != nil {
						logger.Error("Telegram: failed to toggle proxy %s: %v", stableID, err)
					}
					proxyName := b.getProxyNameByStableID(stableID)
					toast := fmt.Sprintf("🟢 Прокси-хост %s включен", proxyName)
					if disabled {
						toast = fmt.Sprintf("⏸️ Прокси-хост %s выключен", proxyName)
					}
					if b.api != nil {
						_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText(toast))
					}
				}
				text, markup := b.getDisabledProxiesView(page)
				b.editWithMarkup(chatID, msgID, text, markup)
			}
		} else if strings.HasPrefix(cb.Data, "menu:disabled_hosts:") {
			pageStr := strings.TrimPrefix(cb.Data, "menu:disabled_hosts:")
			if strings.Contains(pageStr, "noop") {
				return
			}
			page, _ := strconv.Atoi(pageStr)
			if page <= 0 {
				page = 1
			}
			text, markup := b.getDisabledHostsView(page)
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:toggle_host:") {
			rest := strings.TrimPrefix(cb.Data, "menu:toggle_host:")
			idx := strings.LastIndex(rest, ":")
			if idx != -1 {
				hostKey := rest[:idx]
				page, _ := strconv.Atoi(rest[idx+1:])
				if page <= 0 {
					page = 1
				}
				host := hostKey
				if strings.HasPrefix(hostKey, "h:") && b.diagSource != nil {
					for _, h := range b.diagSource.GetUniqueHosts() {
						if HostCallbackKey(h) == hostKey {
							host = h
							break
						}
					}
				}
				if b.configMgr != nil {
					disabled, err := b.configMgr.ToggleHost(host)
					if err != nil {
						logger.Error("Telegram: failed to toggle host %s: %v", host, err)
					}
					toast := fmt.Sprintf("🟢 Хост %s включён", host)
					if disabled {
						toast = fmt.Sprintf("⏸️ Хост %s выключен", host)
					}
					_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText(toast))
				}
				text, markup := b.getDisabledHostsView(page)
				b.editWithMarkup(chatID, msgID, text, markup)
			}
		} else if strings.HasPrefix(cb.Data, "menu:diag:details:") || strings.HasPrefix(cb.Data, "menu:diag:p:") {
			pageStr := strings.TrimPrefix(cb.Data, "menu:diag:details:")
			if strings.HasPrefix(cb.Data, "menu:diag:p:") {
				pageStr = strings.TrimPrefix(cb.Data, "menu:diag:p:")
			}
			page, _ := strconv.Atoi(pageStr)
			if page <= 0 {
				page = 1
			}
			reports := b.getCachedDiagnosticsReports()
			if len(reports) == 0 {
				reports = b.getDiagnosticsReports(false)
			}
			deepLinks := getDeepLinksForPage(reports, page)
			if b.isRichMode() {
				rich, totalPages := b.buildDiagnosticsDetailsRichMessage(reports, page)
				b.showRichReport(ct, msgID, rich, DiagPaginationMarkup(page, totalPages, deepLinks...))
			} else {
				pageText, totalPages := b.getDiagnosticsPageText(reports, page)
				b.editWithMarkup(chatID, msgID, pageText, DiagPaginationMarkup(page, totalPages, deepLinks...))
			}
		} else if strings.HasPrefix(cb.Data, "menu:diag:pick_deep:") {
			pageStr := strings.TrimPrefix(cb.Data, "menu:diag:pick_deep:")
			page, _ := strconv.Atoi(pageStr)
			if page <= 0 {
				page = 1
			}
			reports := b.getCachedDiagnosticsReports()
			if len(reports) == 0 {
				reports = b.getDiagnosticsReports(false)
			}
			pageSize := diagPageSize
			if b.isRichMode() {
				pageSize = diagRichDetailsPageSize
			}
			start := (page - 1) * pageSize
			end := start + pageSize
			if start > len(reports) {
				start = len(reports)
			}
			if end > len(reports) {
				end = len(reports)
			}
			pageReports := reports[start:end]
			text := "🔬 <b>Углубленная проверка соединения</b>\n\n" +
				"Выберите прокси-хост для глубокого тестирования сетевых этапов (DNS, TCP, TLS, HTTP-мишени, Check-Host):"
			markup := PickDeepDiagnosticsMarkup(pageReports, page)
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:diag:deep:") {
			rest := strings.TrimPrefix(cb.Data, "menu:diag:deep:")
			parts := strings.Split(rest, ":")
			stableID := parts[0]
			page := 1
			if len(parts) > 1 {
				if p, err := strconv.Atoi(parts[1]); err == nil && p > 0 {
					page = p
				}
			}
			b.handleDeepDiagnostics(chatID, msgID, stableID, page)
		} else if strings.HasPrefix(cb.Data, "menu:diag:refresh:") || cb.Data == "menu:diag:refresh" {
			arg := strings.TrimPrefix(cb.Data, "menu:diag:refresh:")
			if cb.Data == "menu:diag:refresh" {
				arg = "summary"
			}
			seq := b.nextMsgSeq(chatID, msgID)

			if arg == "summary" || arg == "rich" {
				b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование подробной сводки...</b>", BackToMenuMarkup())
				go func() {
					reports := b.getDiagnosticsReports(true)
					if !b.isMsgSeqValid(chatID, msgID, seq) {
						return
					}
					deepLinks := getDeepLinks(reports, 3)
					markup := RichReportMarkup(deepLinks...)
					if b.isRichMode() || arg == "rich" {
						rich := b.buildDiagnosticsRichMessage(reports)
						b.showRichReport(ct, msgID, rich, markup)
						return
					}
					summaryText := b.getDiagnosticsSummaryText(reports)
					b.editWithMarkup(chatID, msgID, summaryText, markup)
				}()
			} else {
				pageStr := strings.TrimPrefix(arg, "details:")
				page, _ := strconv.Atoi(pageStr)
				if page <= 0 {
					page = 1
				}
				b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование детального отчёта...</b>", BackToMenuMarkup())
				go func() {
					reports := b.getDiagnosticsReports(true)
					if !b.isMsgSeqValid(chatID, msgID, seq) {
						return
					}
					deepLinks := getDeepLinksForPage(reports, page)
					if b.isRichMode() {
						rich, totalPages := b.buildDiagnosticsDetailsRichMessage(reports, page)
						b.showRichReport(ct, msgID, rich, DiagPaginationMarkup(page, totalPages, deepLinks...))
					} else {
						pageText, totalPages := b.getDiagnosticsPageText(reports, page)
						b.editWithMarkup(chatID, msgID, pageText, DiagPaginationMarkup(page, totalPages, deepLinks...))
					}
				}()
			}
		} else if strings.HasPrefix(cb.Data, "menu:checkhost:run:") {
			stableID := strings.TrimPrefix(cb.Data, "menu:checkhost:run:")
			go b.handleCheckHostProxy(chatID, msgID, stableID)
		} else if strings.HasPrefix(cb.Data, "menu:interval:set:") {
			secStr := strings.TrimPrefix(cb.Data, "menu:interval:set:")
			if sec, err := strconv.Atoi(secStr); err == nil && sec >= 10 {
				b.updateCheckInterval(sec)
			}
			b.editWithMarkup(chatID, msgID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
		}
	}
}
