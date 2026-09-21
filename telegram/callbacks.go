package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/checker"
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

	if !isToastCallback && b.api != nil {
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
			tabs := b.getNodeTabs("local")
			deepLinks := getDeepLinks(reports, 3)
			markup := RichReportMarkupWithTabs("local", tabs, deepLinks...)
			if b.isRichMode() {
				rich := b.buildDiagnosticsRichMessageWithTarget(reports, "local", b.getMasterASN())
				b.showRichReport(ct, msgID, rich, markup)
				return
			}
			summaryText := b.getDiagnosticsSummaryTextWithTarget(reports, "local", b.getMasterASN())
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
			tabs := b.getNodeTabs("local")
			deepLinks := getDeepLinks(reports, 3)
			markup := RichReportMarkupWithTabs("local", tabs, deepLinks...)
			rich := b.buildDiagnosticsRichMessageWithTarget(reports, "local", b.getMasterASN())
			b.showRichReport(ct, msgID, rich, markup)
		}()
	case "menu:stats":
		b.editWithMarkup(chatID, msgID, b.getStatsOverviewText(), StatsMenuMarkup())
	case "menu:stats:incidents":
		tabs := b.getNodeTabs("")
		b.editWithMarkup(chatID, msgID, b.getIncidentsTextFiltered("all"), IncidentsFilterMarkup("all", tabs))
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
	case "menu:checkupdate":
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("🔍 Проверяю релизы на GitHub..."))
		go func() {
			rel, isNewer, err := b.CheckAndNotifyRelease(true)
			if err != nil {
				b.editWithMarkup(chatID, msgID, fmt.Sprintf("❌ Ошибка при проверке обновлений: %s", escapeHTML(err.Error())), BackToSettingsMarkup())
				return
			}
			if isNewer {
				text := formatReleaseNotification(b.version, rel)
				markup := tu.InlineKeyboard(
					tu.InlineKeyboardRow(
						btnURL("🔗 Открыть релиз на GitHub", rel.HTMLURL),
					),
					tu.InlineKeyboardRow(
						btn("🔙 К настройкам", "menu:settings"),
					),
				)
				b.editWithMarkup(chatID, msgID, text, markup)
				return
			}
			currentDisplay := b.version
			if currentDisplay == "" || currentDisplay == "unknown" {
				currentDisplay = "unknown (локальная сборка)"
			}
			text := fmt.Sprintf("✅ <b>Установлена актуальная версия!</b>\n\n"+
				"• Текущая версия: <code>%s</code>\n"+
				"• Последний релиз: <a href=\"%s\">%s</a>\n\n"+
				"Обновлений не требуется.",
				escapeHTML(currentDisplay), rel.HTMLURL, escapeHTML(rel.TagName))
			markup := tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					btnURL("🔗 Все релизы на GitHub", "https://github.com/vlv-code/xray-checker-tg-bot/releases"),
				),
				tu.InlineKeyboardRow(
					btn("🔙 К настройкам", "menu:settings"),
				),
			)
			b.editWithMarkup(chatID, msgID, text, markup)
		}()
	case "menu:nodes", "menu:nodes:refresh":
		b.editWithMarkup(chatID, msgID, b.getNodesMainView(), NodesMainMenuMarkup())
	case "menu:nodes:add":
		b.waitingNodeAddMu.Lock()
		if b.waitingNodeAdd == nil {
			b.waitingNodeAdd = make(map[int64]bool)
		}
		b.waitingNodeAdd[chatID] = true
		b.waitingNodeAddMu.Unlock()
		text := "➕ <b>Подключение новой ноды</b>\n\n" +
			"Как назвать новую ноду?\n" +
			"Отправьте имя ноды ответным сообщением (латиница, цифры, дефис, например: <code>msk-1</code>, <code>vps-germany</code>).\n\n" +
			"<i>Или выполните команду:</i> <code>/nodeadd &lt;имя_ноды&gt;</code>"
		b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				btn("🔙 К нодам", "menu:nodes"),
				btn("🏠 Главное меню", "menu:main"),
			),
		))
	case "menu:nodes:install":
		b.editWithMarkup(chatID, msgID, b.getNodesInstallGuideView(), NodesInstallMarkup())
	case "menu:nodes:health":
		b.editWithMarkup(chatID, msgID, b.getNodesHealthView(), NodesHealthMarkup())
	case "menu:nodes:settings":
		b.editWithMarkup(chatID, msgID, b.getNodesSettingsView(), NodesSettingsMarkup(b.GetConfig()))
	case "menu:nodes:subs":
		b.waitingNodeAddMu.Lock()
		if b.waitingNodeSub != nil {
			delete(b.waitingNodeSub, chatID)
		}
		b.waitingNodeAddMu.Unlock()
		text, markup := b.getNodesSubsListView()
		b.editWithMarkup(chatID, msgID, text, markup)
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
		if strings.HasPrefix(cb.Data, "menu:nodes:subnode:") {
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:subnode:")
			b.waitingNodeAddMu.Lock()
			if b.waitingNodeSub != nil {
				delete(b.waitingNodeSub, chatID)
			}
			b.waitingNodeAddMu.Unlock()
			text, markup := b.getNodeSubsManageView(nodeName)
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:nodes:addsub:") {
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:addsub:")
			b.waitingNodeAddMu.Lock()
			if b.waitingNodeSub == nil {
				b.waitingNodeSub = make(map[int64]string)
			}
			b.waitingNodeSub[chatID] = nodeName
			b.waitingNodeAddMu.Unlock()
			text := fmt.Sprintf("➕ <b>Назначение подписки для ноды</b> <code>%s</code>\n\n"+
				"Отправьте URL подписки ответным сообщением в этот чат (например: <code>https://example.com/sub/token</code>).\n\n"+
				"<i>Или выполните команду:</i> <code>/nodeaddsub %s &lt;URL&gt;</code>",
				escapeHTML(nodeName), escapeHTML(nodeName))
			b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					btn("🔙 Отмена", fmt.Sprintf("menu:nodes:subnode:%s", nodeName)),
				),
			))
		} else if strings.HasPrefix(cb.Data, "menu:nodes:delsub:") {
			rest := strings.TrimPrefix(cb.Data, "menu:nodes:delsub:")
			parts := strings.Split(rest, ":")
			if len(parts) == 2 && b.nodeMgr != nil {
				nodeName := parts[0]
				idx, err := strconv.Atoi(parts[1])
				if err == nil && idx >= 0 {
					subs, err := b.nodeMgr.ManagedSubs(nodeName)
					if err == nil && idx < len(subs) {
						subURL := subs[idx].URL
						if err := b.nodeMgr.RemoveSub(nodeName, subURL); err != nil && b.api != nil {
							_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ Ошибка: "+err.Error()))
						} else if b.api != nil {
							_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("🗑 Подписка удалена"))
						}
					}
				}
				text, markup := b.getNodeSubsManageView(nodeName)
				b.editWithMarkup(chatID, msgID, text, markup)
			}
		} else if strings.HasPrefix(cb.Data, "menu:tz:") {
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
		} else if strings.HasPrefix(cb.Data, "menu:diag:node:") {
			target := strings.TrimPrefix(cb.Data, "menu:diag:node:")
			seq := b.nextMsgSeq(chatID, msgID)
			b.editWithMarkup(chatID, msgID, "⏳ <b>Загрузка данных узла...</b>", BackToMenuMarkup())
			go func() {
				var reports []checker.ProxyDiagReport
				asn := ""
				var deepLinks []DiagDeepLink
				if target == "local" || target == "" {
					reports = b.getDiagnosticsReports(false)
					asn = b.getMasterASN()
					deepLinks = getDeepLinks(reports, 3)
				} else {
					reports = b.getNodeDiagnosticsReports(target)
					asn = b.getNodeASN(target)
					deepLinks = getDeepLinks(reports, 3)
				}
				if !b.isMsgSeqValid(chatID, msgID, seq) {
					return
				}
				tabs := b.getNodeTabs(target)
				markup := RichReportMarkupWithTabs(target, tabs, deepLinks...)
				if b.isRichMode() {
					rich := b.buildDiagnosticsRichMessageWithTarget(reports, target, asn)
					b.showRichReport(ct, msgID, rich, markup)
					return
				}
				summaryText := b.getDiagnosticsSummaryTextWithTarget(reports, target, asn)
				b.editWithMarkup(chatID, msgID, summaryText, markup)
			}()
		} else if strings.HasPrefix(cb.Data, "menu:diag:details:") || cb.Data == "menu:diag:details" {
			rest := strings.TrimPrefix(cb.Data, "menu:diag:details:")
			target := "local"
			page := 1
			if cb.Data != "menu:diag:details" && rest != "" {
				parts := strings.Split(rest, ":")
				if len(parts) == 1 {
					if p, err := strconv.Atoi(parts[0]); err == nil {
						page = p
					} else {
						target = parts[0]
						page = 1
					}
				} else if len(parts) >= 2 {
					target = parts[0]
					page, _ = strconv.Atoi(parts[1])
				}
			}
			if page <= 0 {
				page = 1
			}

			var reports []checker.ProxyDiagReport
			if target == "local" || target == "" {
				reports = b.getCachedDiagnosticsReports()
				if len(reports) == 0 {
					reports = b.getDiagnosticsReports(false)
				}
			} else {
				reports = b.getNodeDiagnosticsReports(target)
			}

			tabs := b.getNodeTabs(target)
			if b.isRichMode() {
				rich, totalPages := b.buildDiagnosticsDetailsRichMessageWithTarget(reports, target, page)
				b.showRichReport(ct, msgID, rich, DiagnosticsPaginationMarkupWithTabs(target, page, totalPages, tabs))
			} else {
				pageText, totalPages := b.getDiagnosticsPageTextWithTarget(reports, target, page)
				b.editWithMarkup(chatID, msgID, pageText, DiagnosticsPaginationMarkupWithTabs(target, page, totalPages, tabs))
			}
		} else if strings.HasPrefix(cb.Data, "menu:stats:incidents:") {
			filter := strings.TrimPrefix(cb.Data, "menu:stats:incidents:")
			tabs := b.getNodeTabs("")
			b.editWithMarkup(chatID, msgID, b.getIncidentsTextFiltered(filter), IncidentsFilterMarkup(filter, tabs))
		} else if strings.HasPrefix(cb.Data, "menu:diag:pick_deep:") {
			rest := strings.TrimPrefix(cb.Data, "menu:diag:pick_deep:")
			target := "local"
			page := 1
			parts := strings.Split(rest, ":")
			if len(parts) == 1 {
				if p, err := strconv.Atoi(parts[0]); err == nil {
					page = p
				} else {
					target = parts[0]
				}
			} else if len(parts) >= 2 {
				target = parts[0]
				page, _ = strconv.Atoi(parts[1])
			}
			if page <= 0 {
				page = 1
			}

			var reports []checker.ProxyDiagReport
			if target == "local" || target == "" {
				reports = b.getCachedDiagnosticsReports()
				if len(reports) == 0 {
					reports = b.getDiagnosticsReports(false)
				}
			} else {
				reports = b.getNodeDiagnosticsReports(target)
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
			tabs := b.getNodeTabs(target)
			markup := PickDeepDiagnosticsMarkupWithTabs(target, tabs, pageReports, page)
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

			if arg == "summary" || arg == "rich" || (!strings.HasPrefix(arg, "details") && arg != "") {
				target := "local"
				if arg != "summary" && arg != "rich" {
					target = arg
				}
				b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование подробной сводки...</b>", BackToMenuMarkup())
				go func() {
					var reports []checker.ProxyDiagReport
					asn := ""
					var deepLinks []DiagDeepLink
					if target == "local" || target == "" {
						reports = b.getDiagnosticsReports(true)
						asn = b.getMasterASN()
						deepLinks = getDeepLinks(reports, 3)
					} else {
						reports = b.getNodeDiagnosticsReports(target)
						asn = b.getNodeASN(target)
						deepLinks = getDeepLinks(reports, 3)
					}
					if !b.isMsgSeqValid(chatID, msgID, seq) {
						return
					}
					tabs := b.getNodeTabs(target)
					markup := RichReportMarkupWithTabs(target, tabs, deepLinks...)
					if b.isRichMode() || arg == "rich" {
						rich := b.buildDiagnosticsRichMessageWithTarget(reports, target, asn)
						b.showRichReport(ct, msgID, rich, markup)
						return
					}
					summaryText := b.getDiagnosticsSummaryTextWithTarget(reports, target, asn)
					b.editWithMarkup(chatID, msgID, summaryText, markup)
				}()
			} else {
				detailsRest := strings.TrimPrefix(arg, "details:")
				target := "local"
				page := 1
				parts := strings.Split(detailsRest, ":")
				if len(parts) == 1 {
					if p, err := strconv.Atoi(parts[0]); err == nil {
						page = p
					} else {
						target = parts[0]
					}
				} else if len(parts) >= 2 {
					target = parts[0]
					page, _ = strconv.Atoi(parts[1])
				}
				if page <= 0 {
					page = 1
				}

				b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование детального отчёта...</b>", BackToMenuMarkup())
				go func() {
					var reports []checker.ProxyDiagReport
					if target == "local" || target == "" {
						reports = b.getDiagnosticsReports(true)
					} else {
						reports = b.getNodeDiagnosticsReports(target)
					}
					if !b.isMsgSeqValid(chatID, msgID, seq) {
						return
					}
					tabs := b.getNodeTabs(target)
					if b.isRichMode() {
						rich, totalPages := b.buildDiagnosticsDetailsRichMessageWithTarget(reports, target, page)
						b.showRichReport(ct, msgID, rich, DiagnosticsPaginationMarkupWithTabs(target, page, totalPages, tabs))
					} else {
						pageText, totalPages := b.getDiagnosticsPageTextWithTarget(reports, target, page)
						b.editWithMarkup(chatID, msgID, pageText, DiagnosticsPaginationMarkupWithTabs(target, page, totalPages, tabs))
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
