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

// isMutatingCallback reports whether the callback route mutates persisted
// configuration or state and therefore requires admin authorization
// (mirrors the mutating-command gate in handleMessage). Informational routes
// (menu views, diagnostics, per-proxy check runs) stay open to allowed chats.
func isMutatingCallback(data string) bool {
	switch data {
	case "menu:quiet:toggle",
		"menu:quiet:snooze:1h",
		"menu:quiet:snooze:4h",
		"menu:quiet:snooze:morning",
		"menu:quiet:unsnooze",
		"menu:alert_mode:live",
		"menu:alert_mode:clean",
		"menu:checkhost:toggle_bg",
		"menu:checkhost:toggle_alert",
		"menu:checkhost:run_now",
		"menu:checkupdate",
		"menu:nodes:toggle_sync",
		"menu:nodes:toggle_alerts",
		"menu:nodes:toggle_proxy_alerts":
		return true
	}
	return strings.HasPrefix(data, "menu:nodes:delsub:") ||
		strings.HasPrefix(data, "menu:nodes:ncfg:") ||
		strings.HasPrefix(data, "menu:nodes:nx:") ||
		strings.HasPrefix(data, "menu:nodes:nreset:") ||
		strings.HasPrefix(data, "menu:nodes:ren:") ||
		strings.HasPrefix(data, "menu:nodes:tg:") ||
		strings.HasPrefix(data, "menu:tz:") ||
		strings.HasPrefix(data, "menu:checkhost:int:") ||
		strings.HasPrefix(data, "menu:toggle_proxy:") ||
		strings.HasPrefix(data, "menu:toggle_host:") ||
		strings.HasPrefix(data, "menu:interval:set:")
}

// isToastCallbackData reports whether the callback route answers with its own
// custom toast text, so the generic immediate acknowledgement must be skipped.
// Wave-F B3: menu:checkupdate joins the set — its handler already answers
// with the «Проверяю релизы на GitHub...» toast.
func isToastCallbackData(data string) bool {
	return strings.HasPrefix(data, "menu:toggle_proxy:") ||
		strings.HasPrefix(data, "menu:toggle_host:") ||
		strings.HasPrefix(data, "menu:nodes:ncfg:") ||
		strings.HasPrefix(data, "menu:nodes:nx:") ||
		strings.HasPrefix(data, "menu:nodes:nreset:") ||
		strings.HasPrefix(data, "menu:nodes:ren:") ||
		strings.HasPrefix(data, "menu:nodes:tg:") ||
		data == "menu:checkhost:run_now" ||
		data == "menu:checkupdate"
}

func (b *Bot) handleCallbackQuery(cb *telego.CallbackQuery) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.GetChat().ID
	if !b.allowedChatIDs[chatID] {
		return
	}

	// Admin gate: mutating callback routes require elevated rights when the
	// admin list is non-empty (mirrors the mutating-command gate in handleMessage).
	if isMutatingCallback(cb.Data) && !b.isAuthorizedMutating(cb.From.ID) {
		if b.api != nil {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("⛔ Эта операция доступна только администраторам бота."))
		}
		logger.Warn("Telegram: user %d (%s) denied mutating callback %q in chat %d",
			cb.From.ID, cb.From.Username, cb.Data, chatID)
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
	isToastCallback := isToastCallbackData(cb.Data)

	if !isToastCallback && b.api != nil {
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID))
	}

	b.invalidateMsgSeq(chatID, msgID)

	switch cb.Data {
	case "menu:main":
		// Wave H4: leaving for the main menu disarms a pending manual
		// Cheburcheck host input (mirrors the wave G5a rename disarm).
		b.waitingNodeAddMu.Lock()
		if b.waitingCheburHost != nil {
			delete(b.waitingCheburHost, chatID)
		}
		b.waitingNodeAddMu.Unlock()
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
		b.editWithMarkup(chatID, msgID, b.getCheckHostMenuText(),
			CheckHostMenuMarkup(snapshot, 1, menuTotalPages(len(snapshot), menuListPageSize)))
	case "menu:chebur":
		// EXACT match only (order invariant, mirrors menu:chebur): the per-host
		// probe route is the "menu:cheburh:" prefix branch, the pager the
		// "menu:cheburpg:" prefix branch in the default clause below, and the
		// manual-input route the exact case "menu:cheburin" next — none can
		// collide with this bare exact case. Re-entering the submenu disarms a
		// pending manual-input state (wave H4).
		b.waitingNodeAddMu.Lock()
		if b.waitingCheburHost != nil {
			delete(b.waitingCheburHost, chatID)
		}
		b.waitingNodeAddMu.Unlock()
		snapshot := b.source.MetricsSnapshot()
		hosts := cheburHostsFromSnapshot(snapshot)
		b.editWithMarkup(chatID, msgID, b.getCheburMenuText(len(hosts)),
			CheburMenuMarkup(hosts, 1, menuTotalPages(len(hosts), menuListPageSize)))
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
	case "menu:checkhost:run_now":
		// Explicit user-triggered run: bypasses the CheckHostBgEnabled gate.
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("🚀 Запуск фоновой проверки Check-Host..."))
		go b.runAuditNow()
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
				escapeHTML(currentDisplay), escapeHTML(rel.HTMLURL), escapeHTML(rel.TagName))
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
		// Wave H2 navigation tree (decision D4 — the global «Подписки нод»
		// screen route menu:nodes:subs is REMOVED; node subscriptions live
		// only in the per-node manage screen):
		//   menu:nodes → menu:nodes:cfg (picker) → menu:nodes:cfg:<n> (node screen)
		//     ├─ menu:nodes:subnode:<n> (subs manage; back → menu:nodes:cfg:<n>;
		//     │    addsub cancel → menu:nodes:subnode:<n>, fallback menu:nodes:cfg)
		//     └─ menu:nodes:excl:<n> (per-proxy exclusions; back → menu:nodes:cfg:<n>;
		//          pager menu:nodes:exclpg:<key>:<page>,
		//          toggle menu:nodes:nx:<key>:<idx>:<page>)
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
	case "menu:nodes:cfg":
		// EXACT match only (order invariant, mirrors menu:chebur): the
		// per-node screen route is the "menu:nodes:cfg:" prefix branch in the
		// default clause below — a prefixed node name must never be swallowed
		// by this picker rule, and the bare picker must never reach the
		// prefix branch. Keep both rules distinct if this routing is reworked.
		// Wave G5a: leaving the node context disarms a pending rename input.
		b.waitingNodeAddMu.Lock()
		if b.waitingNodeRename != nil {
			delete(b.waitingNodeRename, chatID)
		}
		b.waitingNodeAddMu.Unlock()
		text, markup := b.getNodesCfgListView()
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
			// Wave-F B2: the cancel button is composed via cbNodeCfg; if the
			// node name would overflow the 64-byte limit, fall back to the
			// static node-cfg picker so the row never dies (Wave H2/D4: the
			// former menu:nodes:subs fallback route is removed).
			cancelCb, cancelOK := cbNodeCfg("menu:nodes:subnode:", nodeName)
			if !cancelOK {
				cancelCb = "menu:nodes:cfg"
			}
			b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					btn("🔙 Отмена", cancelCb),
				),
			))
		} else if strings.HasPrefix(cb.Data, "menu:nodes:delsub:") {
			rest := strings.TrimPrefix(cb.Data, "menu:nodes:delsub:")
			// Wave-F B2 robust parse: the row index sits after the LAST colon,
			// so node names containing ':' (possible for NODES env entries)
			// round-trip through the emitted delsub button; colon-free names
			// parse identically to the former Split-based logic.
			sep := strings.LastIndex(rest, ":")
			if sep != -1 && b.nodeMgr != nil {
				nodeName := rest[:sep]
				idx, err := strconv.Atoi(rest[sep+1:])
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
		} else if strings.HasPrefix(cb.Data, "menu:nodes:cfg:") {
			// Order invariant (mirrors menu:chebur): this prefix branch is the
			// per-node settings screen; the bare "menu:nodes:cfg" picker rule
			// is an exact-match switch case above and can never match here.
			// Wave G5a: re-entering the screen (incl. the rename cancel button)
			// disarms a pending rename input.
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:cfg:")
			b.waitingNodeAddMu.Lock()
			if b.waitingNodeRename != nil {
				delete(b.waitingNodeRename, chatID)
			}
			b.waitingNodeAddMu.Unlock()
			text, markup := b.getNodeCfgScreen(nodeName)
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:nodes:excl:") {
			// Wave H1: the exclusions screen is now a FUNCTIONAL per-proxy
			// toggle list (was a read-only view); page 1 on entry. Node names
			// may contain ':' (NODES env) — no idx/page suffix here, so the
			// whole remainder is the name.
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:excl:")
			text, markup := b.getNodeExclusionsView(nodeName, 1, "")
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:nodes:nx:") {
			// Wave H1: per-proxy exclusion toggle. Mutating (admin-gated via
			// isMutatingCallback) and self-toasting (isToastCallbackData);
			// writes only through updateConfig — see handleNodeExclToggle.
			b.handleNodeExclToggle(chatID, msgID, cb, cb.Data)
		} else if strings.HasPrefix(cb.Data, "menu:nodes:exclpg:") {
			// Wave H1: exclusions screen pager. View route — not gated, not
			// self-toasting. Pages clamp into range inside the view.
			rest := strings.TrimPrefix(cb.Data, "menu:nodes:exclpg:")
			sep := strings.LastIndex(rest, ":")
			if sep == -1 {
				return
			}
			page, err := strconv.Atoi(rest[sep+1:])
			if err != nil {
				return
			}
			node := b.nodeByKey(rest[:sep])
			if node == "" {
				if b.api != nil {
					_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ Нода не найдена"))
				}
				return
			}
			text, markup := b.getNodeExclusionsView(node, page, "")
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:nodes:ncfg:") {
			// Mutating (admin-gated via isMutatingCallback): apply one setting
			// override through nodeMgr — the only settings writer — then
			// re-render the same screen.
			rest := strings.TrimPrefix(cb.Data, "menu:nodes:ncfg:")
			parts := strings.SplitN(rest, ":", 3)
			if len(parts) == 3 && b.nodeMgr != nil {
				nodeName, key, value := parts[0], parts[1], parts[2]
				if err := b.nodeMgr.SetNodeSetting(nodeName, key, value); err != nil {
					logger.Warn("Telegram: node cfg apply failed for %s (%s=%s): %v", nodeName, key, value, err)
					if b.api != nil {
						_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ "+err.Error()))
					}
				} else if b.api != nil {
					_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("✅ Применится при следующем отчёте ноды"))
				}
				text, markup := b.getNodeCfgScreen(nodeName)
				b.editWithMarkup(chatID, msgID, text, markup)
			}
		} else if strings.HasPrefix(cb.Data, "menu:nodes:nreset:") {
			// Mutating (admin-gated via isMutatingCallback): clear all of the
			// node's overrides through nodeMgr, then re-render the screen.
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:nreset:")
			if b.nodeMgr != nil {
				if err := b.nodeMgr.ResetNodeSetting(nodeName, ""); err != nil {
					logger.Warn("Telegram: node cfg reset failed for %s: %v", nodeName, err)
					if b.api != nil {
						_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ "+err.Error()))
					}
				} else if b.api != nil {
					_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("↩️ Переопределения сброшены — применится при следующем отчёте ноды"))
				}
				text, markup := b.getNodeCfgScreen(nodeName)
				b.editWithMarkup(chatID, msgID, text, markup)
			}
		} else if strings.HasPrefix(cb.Data, "menu:nodes:tg:") {
			// Wave G5b: enable/disable node toggle. Mutating (admin-gated via
			// isMutatingCallback) and self-toasting (isToastCallbackData). The
			// action sits after the LAST colon so node names containing ':'
			// (NODES env) round-trip — same robust parse as delsub.
			rest := strings.TrimPrefix(cb.Data, "menu:nodes:tg:")
			sep := strings.LastIndex(rest, ":")
			if sep != -1 && b.nodeMgr != nil {
				nodeName, action := rest[:sep], rest[sep+1:]
				var enabled bool
				var toast string
				switch action {
				case "on":
					enabled, toast = true, "▶️ Нода включена — вернётся в метрики и алерты"
				case "off":
					enabled, toast = false, "⏸️ Нода отключена — исключена из метрик и алертов"
				default:
					return
				}
				// A context switch from a pending rename input disarms it.
				b.waitingNodeAddMu.Lock()
				if b.waitingNodeRename != nil {
					delete(b.waitingNodeRename, chatID)
				}
				b.waitingNodeAddMu.Unlock()
				if err := b.nodeMgr.SetNodeEnabled(nodeName, enabled); err != nil {
					logger.Warn("Telegram: node toggle failed for %s (%s): %v", nodeName, action, err)
					toast = "❌ " + err.Error()
				}
				if b.api != nil {
					_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText(toast))
				}
				text, markup := b.getNodeCfgScreen(nodeName)
				b.editWithMarkup(chatID, msgID, text, markup)
			}
		} else if strings.HasPrefix(cb.Data, "menu:nodes:ren:") {
			// Wave G5a: arm the rename waiting state and prompt for the new
			// name as a text reply. Mutating (admin-gated via
			// isMutatingCallback) and self-toasting (isToastCallbackData).
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:ren:")
			b.waitingNodeAddMu.Lock()
			if b.waitingNodeRename == nil {
				b.waitingNodeRename = make(map[int64]string)
			}
			b.waitingNodeRename[chatID] = nodeName
			b.waitingNodeAddMu.Unlock()
			if b.api != nil {
				_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("✏️ Отправьте новое имя ноды сообщением в чат"))
			}
			text := fmt.Sprintf("✏️ <b>Переименование ноды</b> <code>%s</code>\n\n"+
				"Отправьте новое имя ответным сообщением в этот чат "+
				"(латиница, цифры, дефис, до 32 символов, например: <code>germany-2</code>).\n\n"+
				"Токен, подписки и настройки ноды перенесутся автоматически.\n\n"+
				"<i>Или выполните команду:</i> <code>/noderename %s &lt;новое_имя&gt;</code>",
				escapeHTML(nodeName), escapeHTML(nodeName))
			// Wave-F B2 fallback discipline: the cancel button is composed via
			// cbNodeCfg; over-limit names fall back to the static picker route.
			cancelCb, cancelOK := cbNodeCfg("menu:nodes:cfg:", nodeName)
			if !cancelOK {
				cancelCb = "menu:nodes:cfg"
			}
			b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					btn("🔙 Отмена", cancelCb),
				),
			))
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

			renderDetails := func(reports []checker.ProxyDiagReport) {
				tabs := b.getNodeTabs(target)
				if b.isRichMode() {
					rich, totalPages := b.buildDiagnosticsDetailsRichMessageWithTarget(reports, target, page)
					b.showRichReport(ct, msgID, rich, DiagnosticsPaginationMarkupWithTabs(target, page, totalPages, tabs))
				} else {
					pageText, totalPages := b.getDiagnosticsPageTextWithTarget(reports, target, page)
					b.editWithMarkup(chatID, msgID, pageText, DiagnosticsPaginationMarkupWithTabs(target, page, totalPages, tabs))
				}
			}

			if target == "local" || target == "" {
				// Fast path: fresh cache — render immediately, no network.
				if cached := b.getCachedDiagnosticsReports(); len(cached) > 0 {
					renderDetails(cached)
					return
				}
				// Cold cache: a full probe sweep takes tens of seconds, so run
				// it in the background like menu:diag instead of blocking the
				// single update-processing goroutine for all chats.
				seq := b.nextMsgSeq(chatID, msgID)
				b.editWithMarkup(chatID, msgID, "⏳ <b>Формирование детального отчёта...</b>", BackToMenuMarkup())
				go b.runUpdateSafely(func() {
					reports := b.getDiagnosticsReports(false)
					if !b.isMsgSeqValid(chatID, msgID, seq) {
						return
					}
					renderDetails(reports)
				})
				return
			}
			// Remote node reports come from memory (no probe sweep).
			renderDetails(b.getNodeDiagnosticsReports(target))
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

			renderPickDeep := func(reports []checker.ProxyDiagReport) {
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
			}

			if target == "local" || target == "" {
				// Fast path: fresh cache — render immediately, no network.
				if cached := b.getCachedDiagnosticsReports(); len(cached) > 0 {
					renderPickDeep(cached)
					return
				}
				// Cold cache: run the probe sweep in the background like
				// menu:diag instead of blocking the update goroutine.
				seq := b.nextMsgSeq(chatID, msgID)
				b.editWithMarkup(chatID, msgID, "⏳ <b>Подготовка углубленной проверки...</b>", BackToMenuMarkup())
				go b.runUpdateSafely(func() {
					reports := b.getDiagnosticsReports(false)
					if !b.isMsgSeqValid(chatID, msgID, seq) {
						return
					}
					renderPickDeep(reports)
				})
				return
			}
			// Remote node reports come from memory (no probe sweep).
			renderPickDeep(b.getNodeDiagnosticsReports(target))
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
		} else if strings.HasPrefix(cb.Data, "menu:cheburh:") {
			// Wave G1: per-host probe route — resolve the HostCallbackKey back
			// to the snapshot host and probe it. Order invariant: this prefix
			// branch (and the menu:cheburpg: pager below) can never collide
			// with the bare "menu:chebur" exact-match switch case above —
			// keep both rules distinct if this routing is reworked.
			hostKey := strings.TrimPrefix(cb.Data, "menu:cheburh:")
			go b.handleCheburcheckHost(chatID, msgID, hostKey)
		} else if strings.HasPrefix(cb.Data, "menu:cheburpg:") {
			// Wave G4: Cheburcheck submenu pager. Pages clamp into range; the
			// host list is recomputed from the current snapshot.
			page, _ := strconv.Atoi(strings.TrimPrefix(cb.Data, "menu:cheburpg:"))
			snapshot := b.source.MetricsSnapshot()
			hosts := cheburHostsFromSnapshot(snapshot)
			totalPages := menuTotalPages(len(hosts), menuListPageSize)
			if page < 1 {
				page = 1
			}
			if page > totalPages {
				page = totalPages
			}
			b.editWithMarkup(chatID, msgID, b.getCheburMenuText(len(hosts)), CheburMenuMarkup(hosts, page, totalPages))
		} else if strings.HasPrefix(cb.Data, "menu:checkhostpg:") {
			// Wave G4: Check-Host submenu pager (replaces the wave-F overflow
			// stub). Pages clamp into range.
			page, _ := strconv.Atoi(strings.TrimPrefix(cb.Data, "menu:checkhostpg:"))
			snapshot := b.source.MetricsSnapshot()
			totalPages := menuTotalPages(len(snapshot), menuListPageSize)
			if page < 1 {
				page = 1
			}
			if page > totalPages {
				page = totalPages
			}
			b.editWithMarkup(chatID, msgID, b.getCheckHostMenuText(), CheckHostMenuMarkup(snapshot, page, totalPages))
		} else if strings.HasPrefix(cb.Data, "menu:interval:set:") {
			secStr := strings.TrimPrefix(cb.Data, "menu:interval:set:")
			if sec, err := strconv.Atoi(secStr); err == nil && sec >= 10 {
				b.updateCheckInterval(sec)
			}
			b.editWithMarkup(chatID, msgID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
		}
	}
}
