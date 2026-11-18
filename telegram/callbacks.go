package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
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
		// Wave H6: the manual custom-host input leads to a persisted config
		// write, so arming is admin-gated too (unlike the informational
		// menu:cheburin arm).
		"menu:checkhostadd",
		"menu:checkupdate",
		"menu:nodes:toggle_sync",
		"menu:nodes:toggle_alerts",
		"menu:nodes:toggle_proxy_alerts",
		// Wave K-B3: the digest / custom-emoji settings toggles persist config,
		// and the «➕ Добавить подписку» arm ends in a persisted subscription
		// write (the input itself is admin-gated in handleMessage).
		"menu:nodes:digest",
		"menu:toggle_icons",
		"menu:subsadd":
		return true
	}
	return strings.HasPrefix(data, "menu:nodes:delsub:") ||
		strings.HasPrefix(data, "menu:nodes:ncfg:") ||
		strings.HasPrefix(data, "menu:nodes:nx:") ||
		// Wave M-A M1: two-step node delete — the prefix covers BOTH steps (the
		// confirm-screen tap and the explicit «:yes» removal), like nreset.
		strings.HasPrefix(data, "menu:nodes:ndel:") ||
		strings.HasPrefix(data, "menu:nodes:nreset:") ||
		strings.HasPrefix(data, "menu:nodes:ren:") ||
		strings.HasPrefix(data, "menu:nodes:tg:") ||
		strings.HasPrefix(data, "menu:tz:") ||
		strings.HasPrefix(data, "menu:checkhost:int:") ||
		// Wave H6: removing a custom Check-Host host persists config.
		strings.HasPrefix(data, "menu:checkhostdel:") ||
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
		// Wave M-A M1: the ndel route answers with its own toasts (confirm
		// prompt, «🗑 Нода удалена», or the ❌ error alert).
		strings.HasPrefix(data, "menu:nodes:ndel:") ||
		strings.HasPrefix(data, "menu:nodes:nreset:") ||
		strings.HasPrefix(data, "menu:nodes:ren:") ||
		strings.HasPrefix(data, "menu:nodes:tg:") ||
		data == "menu:checkhost:run_now" ||
		// Wave H6: the add-host arm answers with its own toast, the delete
		// route with the «Удалён: …» toast.
		data == "menu:checkhostadd" ||
		strings.HasPrefix(data, "menu:checkhostdel:") ||
		data == "menu:checkupdate" ||
		// Wave K-B3: the delsub route answers with the «🗑 Подписка удалена»
		// (or a ❌ error) toast, the exclusions pager with its «❌ Нода не
		// найдена» fallback toast, and the subs arm with its own prompt toast.
		strings.HasPrefix(data, "menu:nodes:delsub:") ||
		strings.HasPrefix(data, "menu:nodes:exclpg:") ||
		data == "menu:subsadd" ||
		// Wave L-1: the «Отмена» route answers with the «Ввод отменён» toast.
		data == "menu:cancelinput"
}

// isPendingKeepRoute reports whether the callback route never changes the
// on-screen message (pure acknowledgement toasts), so it must NOT disarm a
// pending text input (wave L-1 disarm hook allowlist). Everything else
// clears: arming routes clear-then-re-arm, navigation/mutation routes leave
// the prompt context behind.
func isPendingKeepRoute(data string) bool {
	switch data {
	case "menu:noop",
		// Wave M3/U-8: the run_now route still counts as a keep-route — its
		// immediate handling is a toast only (the completion report edits the
		// message asynchronously, seconds later, without any input flow).
		"menu:checkhost:run_now":
		return true
	}
	return strings.HasPrefix(data, "menu:diag:noop:")
}

// toggleDebounceWindow is the double-tap suppression window for the settings
// toggle rows (wave K-B3): a second tap on the same button within the window
// is answered with a toast instead of flipping the persisted value twice.
const toggleDebounceWindow = 300 * time.Millisecond

var (
	toggleDebounceMu sync.Mutex
	toggleDebounceAt = map[string]time.Time{}
)

// isToggleRoute classifies the single-shot settings toggle rows that are
// double-tap debounced (each flip persists config and re-renders the same
// message). Payload list rows (menu:toggle_proxy:/menu:toggle_host:) stay
// out — their callbacks are per-item and double-flip is visible in the label.
func isToggleRoute(data string) bool {
	switch data {
	case "menu:quiet:toggle",
		"menu:alert_mode:live",
		"menu:alert_mode:clean",
		"menu:checkhost:toggle_bg",
		"menu:checkhost:toggle_alert",
		"menu:nodes:toggle_sync",
		"menu:nodes:toggle_alerts",
		"menu:nodes:toggle_proxy_alerts",
		"menu:nodes:digest",
		"menu:toggle_icons":
		return true
	}
	return false
}

// toggleDebounceCheck reports whether (chatID, msgID, data) is a double-tap
// within toggleDebounceWindow; otherwise it records the tap. Best-effort and
// in-memory only: the map is pruned by reset when it grows, the mutex guards
// only the map access and is never held across network calls.
func toggleDebounceCheck(chatID int64, msgID int, data string) bool {
	key := fmt.Sprintf("%d:%d:%s", chatID, msgID, data)
	now := time.Now()
	toggleDebounceMu.Lock()
	defer toggleDebounceMu.Unlock()
	if last, ok := toggleDebounceAt[key]; ok && now.Sub(last) < toggleDebounceWindow {
		toggleDebounceAt[key] = now
		return true
	}
	toggleDebounceAt[key] = now
	if len(toggleDebounceAt) > 4096 {
		toggleDebounceAt = make(map[string]time.Time)
	}
	return false
}

// promptTTLHint is the uniform TTL hint appended to every input-arming prompt
// screen (wave M5/N-2): the armed text input expires lazily after
// pendingInputTTL (5 minutes), and the «🚫 Отмена» row is rendered below the
// prompt text.
const promptTTLHint = "\n\n⏱ Ввод активен 5 минут · 🚫 Отмена — ниже"

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

	// Wave K-B3: double-tap debounce on the settings toggle rows — a second
	// rapid tap on the same button answers with a toast and does not flip the
	// persisted value twice (best-effort in-memory window, no network locks).
	if isToggleRoute(cb.Data) && toggleDebounceCheck(chatID, msgID, cb.Data) {
		if b.api != nil {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("⏳ Уже применяется — подождите мгновение"))
		}
		return
	}

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

	// Wave L-1: the single disarm hook — ANY screen-changing callback clears
	// a pending text input (the former per-route disarm blocks are gone).
	// Pure-ack toast routes keep the input; arming routes clear-then-re-arm
	// below, so the hook is transparent for them.
	if !isPendingKeepRoute(cb.Data) {
		b.clearPending(chatID)
	}
	if cb.Data == "menu:cancelinput" {
		// Wave L-1: the «Отмена» route — the hook above already cleared the
		// pending input; confirm with the RU toast. Exact route (no payload),
		// not admin-gated: a clear is harmless.
		if b.api != nil {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("Ввод отменён"))
		}
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
		// Wave L-1: the pending-input disarm happens in the single hook above.
		b.editWithMarkup(chatID, msgID, b.getMenuText(), MainMenuMarkup())
	case "menu:settings":
		// Wave K-B3: the state-aware settings keyboard (digest + icons
		// toggles render the live вкл/выкл labels).
		b.editWithMarkup(chatID, msgID, b.getSettingsText(), SettingsMenuMarkup(b.GetConfig(), b.subs != nil))
	case "menu:status":
		// Wave K-B1 integration (I1): the status screen is the paginated
		// render (page 1 on entry; the menu:statuspg: pager branch below
		// flips pages in place).
		b.renderStatusPage(chatID, msgID, 1)
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
		// Wave K-B3: the subs screen carries the «➕ Добавить подписку» arm
		// button and the unified back row (SubsMenuMarkup).
		b.editWithMarkup(chatID, msgID, b.getSubsText(), SubsMenuMarkup())
	case "menu:subsadd":
		// Wave K-B3, migrated to the wave L-1 FSM: manual subscription input —
		// arm the per-chat pending input (the flow ends in a persisted
		// subscription write, so the arm is admin-gated via isMutatingCallback).
		// The next plain text is consumed in handleMessage via takePending.
		b.armPending(chatID, pendingKindSubURL, "")
		if b.api != nil {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("➕ Отправьте URL подписки сообщением в чат"))
		}
		text := "➕ <b>Добавление подписки</b>\n\n" +
			"Отправьте URL подписки ответным сообщением в этот чат (например: <code>https://example.com/sub</code>).\n\n" +
			"<i>Или выполните команду:</i> <code>/addsub &lt;URL&gt;</code>" +
			promptTTLHint
		b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				btnIcon(IconBack, " К списку подписок", "menu:subs"),
				btn(mainMenuBtnText, "menu:main"),
			),
			tu.InlineKeyboardRow(
				btn("🚫 Отмена", "menu:cancelinput"),
			),
		))
	case "menu:nodes:digest":
		// Wave K-B3: daytime digest toggle (persisted; the scheduler already
		// gates on DayDigestEnabled). Admin-gated, debounce-guarded.
		_ = b.updateConfig(func(c *BotConfig) {
			c.DayDigestEnabled = !c.DayDigestEnabled
		})
		b.editWithMarkup(chatID, msgID, b.getSettingsText(), SettingsMenuMarkup(b.GetConfig(), b.subs != nil))
	case "menu:toggle_icons":
		// Wave K-B3: animated custom-emoji icons toggle (persisted;
		// updateConfig re-syncs the package icon layer for the markup
		// builders). Admin-gated, debounce-guarded.
		_ = b.updateConfig(func(c *BotConfig) {
			c.CustomEmoji = !c.CustomEmoji
		})
		b.editWithMarkup(chatID, msgID, b.getSettingsText(), SettingsMenuMarkup(b.GetConfig(), b.subs != nil))
	case "menu:checkhost":
		// EXACT match only (order invariant, mirrors menu:chebur): the per-host
		// check route is the "menu:checkhosth:" prefix branch, the delete route
		// the "menu:checkhostdel:" prefix branch, the pager the
		// "menu:checkhostpg:" prefix branch and the manual-input route the
		// exact case "menu:checkhostadd" — none can collide with this bare
		// exact case. Re-entering the submenu disarms a pending custom-host
		// input via the wave L-1 hook and recomputes the union list
		// (snapshot + customs).
		items := b.checkHostListItems()
		b.editWithMarkup(chatID, msgID, b.getCheckHostMenuText(),
			CheckHostMenuMarkup(items, 1, menuTotalPages(len(items), menuListPageSize)))
	case "menu:checkhostadd":
		// Wave H6, migrated to the wave L-1 FSM: manual custom-host input —
		// arm the per-chat pending input (admin-gated here: the flow ends in
		// a persisted config write); the next plain text is consumed in
		// handleMessage via takePending.
		b.armPending(chatID, pendingKindCheckHostHost, "")
		if b.api != nil {
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("➕ Отправьте хост сообщением в чат"))
		}
		text := "➕ <b>Добавление хоста в Check-Host</b>\n\n" +
			"Отправьте хост ответным сообщением: домен или IP, можно с портом.\n" +
			"Например: <code>example.com</code> или <code>1.2.3.4:443</code>.\n\n" +
			"Хосты из подписок (авто) дублировать не нужно — они уже проверяются." +
			promptTTLHint
		b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				btnIcon(IconBack, " К списку хостов", "menu:checkhost"),
				btn(mainMenuBtnText, "menu:main"),
			),
			tu.InlineKeyboardRow(
				btn("🚫 Отмена", "menu:cancelinput"),
			),
		))
	case "menu:chebur":
		// EXACT match only (order invariant): the per-host probe route is the
		// "menu:cheburh:" prefix branch, the pager the "menu:cheburpg:"
		// prefix branch in the default clause below, and the manual-input
		// route the exact case "menu:cheburin" next — none can collide with
		// this bare exact case. Re-entering the submenu disarms a pending
		// manual-input state via the wave L-1 hook.
		snapshot := b.source.MetricsSnapshot()
		hosts := cheburHostsFromSnapshot(snapshot)
		// Wave K-B3: opening the submenu records page 1 so a probe launched
		// from the list returns to the browsed page.
		b.noteCheburPage(chatID, 1)
		b.editWithMarkup(chatID, msgID, b.getCheburMenuText(len(hosts)),
			CheburMenuMarkup(hosts, 1, menuTotalPages(len(hosts), menuListPageSize)))
	case "menu:cheburin":
		// Wave H4, migrated to the wave L-1 FSM: manual host input — arm the
		// per-chat pending input; the next plain text is consumed in
		// handleMessage via takePending and admin-gated there. Not itself
		// mutating: arming is harmless, the actual probe is gated.
		b.armPending(chatID, pendingKindCheburHost, "")
		text := "✍️ <b>Проверить свой хост</b>\n\n" +
			"Отправьте хост для активного пробинга: домен или IP, можно с портом — порт будет отброшен.\n" +
			"Например: <code>example.com</code> или <code>1.2.3.4:443</code>." +
			promptTTLHint
		b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				btnIcon(IconBack, " К Cheburcheck", "menu:chebur"),
				btn(mainMenuBtnText, "menu:main"),
			),
			tu.InlineKeyboardRow(
				btn("🚫 Отмена", "menu:cancelinput"),
			),
		))
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
		// Wave M3/U-8: the completion aggregates are reported back onto this
		// message. The wrapper is called (not `go`ed) so it captures the
		// msg-seq synchronously in this handler goroutine — a newer
		// interaction with this message then suppresses the stale report;
		// the audit itself runs in its background goroutine, keeping the
		// update loop non-blocking (toast stays).
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("🚀 Запуск фоновой проверки Check-Host..."))
		b.runAuditNowAndReport(chatID, msgID)
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
		// Wave L-1 FSM: arm the per-chat pending input; the next plain text
		// is consumed in handleMessage via takePending (admin-gated there).
		b.armPending(chatID, pendingKindNodeAdd, "")
		text := "➕ <b>Подключение новой ноды</b>\n\n" +
			"Как назвать новую ноду?\n" +
			"Отправьте имя ноды ответным сообщением (латиница, цифры, дефис, например: <code>msk-1</code>, <code>vps-germany</code>).\n\n" +
			"<i>Или выполните команду:</i> <code>/nodeadd &lt;имя_ноды&gt;</code>" +
			promptTTLHint
		b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				btnIcon(IconBack, " К нодам", "menu:nodes"),
				btn(mainMenuBtnText, "menu:main"),
			),
			tu.InlineKeyboardRow(
				btn("🚫 Отмена", "menu:cancelinput"),
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
		// Wave G5a/L-1: leaving the node context disarms a pending rename
		// input via the single hook.
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
			// Wave L-1: leaving the add-sub flow disarms via the single hook.
			text, markup := b.getNodeSubsManageView(nodeName)
			b.editWithMarkup(chatID, msgID, text, markup)
		} else if strings.HasPrefix(cb.Data, "menu:nodes:addsub:") {
			// Wave H2/L-1: arm the per-chat pending input with the target node
			// as payload; the next plain text is consumed in handleMessage via
			// takePending (admin-gated there).
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:addsub:")
			b.armPending(chatID, pendingKindNodeSub, nodeName)
			text := fmt.Sprintf("➕ <b>Назначение подписки для ноды</b> <code>%s</code>\n\n"+
				"Отправьте URL подписки ответным сообщением в этот чат (например: <code>https://example.com/sub/token</code>).\n\n"+
				"<i>Или выполните команду:</i> <code>/nodeaddsub %s &lt;URL&gt;</code>",
				escapeHTML(nodeName), escapeHTML(nodeName)) + promptTTLHint
			// Wave L-1: the «Отмена» button clears the pending input (toast,
			// no navigation); the back row keeps the way to the node's subs
			// screen, composed via cbNodeCfg with the static picker fallback
			// when the node name would overflow the 64-byte callback limit
			// (Wave H2/D4: the former menu:nodes:subs fallback is removed).
			cancelCb, cancelOK := cbNodeCfg("menu:nodes:subnode:", nodeName)
			if !cancelOK {
				cancelCb = "menu:nodes:cfg"
			}
			b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					btn("🚫 Отмена", "menu:cancelinput"),
				),
				tu.InlineKeyboardRow(
					btn(fmt.Sprintf("📋 К подпискам %s", nodeName), cancelCb),
				),
			))
		} else if strings.HasPrefix(cb.Data, "menu:nodes:delsub:") {
			// Wave K-B2 two-step delete (wired by K-B3): the first tap renders
			// the «Точно удалить #N (URL)?» confirm screen (B2's helpers); only
			// the explicit «:yes» callback performs the removal. The suffix
			// stays inside the admin-gated menu:nodes:delsub: prefix, so both
			// steps require elevated rights.
			rest := strings.TrimPrefix(cb.Data, "menu:nodes:delsub:")
			confirming := strings.HasSuffix(rest, ":yes")
			rest = strings.TrimSuffix(rest, ":yes")
			// Wave-F B2 robust parse: the row index sits after the LAST colon,
			// so node names containing ':' (possible for NODES env entries)
			// round-trip through the emitted delsub button; colon-free names
			// parse identically to the former Split-based logic.
			sep := strings.LastIndex(rest, ":")
			if sep != -1 && b.nodeMgr != nil {
				nodeName := rest[:sep]
				idx, err := strconv.Atoi(rest[sep+1:])
				if err == nil && idx >= 0 {
					subs, subsErr := b.nodeMgr.ManagedSubs(nodeName)
					if confirming {
						if subsErr == nil && idx < len(subs) {
							subURL := subs[idx].URL
							if err := b.nodeMgr.RemoveSub(nodeName, subURL); err != nil && b.api != nil {
								// Wave K-B3: the error toast is an alert — the failure
								// must be acknowledged explicitly.
								_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ Ошибка: "+err.Error()).WithShowAlert())
							} else if b.api != nil {
								_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("🗑 Подписка удалена"))
							}
						}
					} else if subsErr == nil && idx < len(subs) {
						// First tap: confirm screen, nothing removed.
						if b.api != nil {
							_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("🗑 Подтвердите удаление"))
						}
						b.editWithMarkup(chatID, msgID,
							nodeSubsConfirmText(nodeName, idx, subs[idx].URL),
							nodeSubsConfirmMarkup(nodeName, idx))
						return
					}
				}
				text, markup := b.getNodeSubsManageView(nodeName)
				b.editWithMarkup(chatID, msgID, text, markup)
			}
		} else if strings.HasPrefix(cb.Data, "menu:nodes:cfg:") {
			// Order invariant (mirrors menu:chebur): this prefix branch is the
			// per-node settings screen; the bare "menu:nodes:cfg" picker rule
			// is an exact-match switch case above and can never match here.
			// Wave G5a/L-1: re-entering the screen (incl. the rename cancel
			// button) disarms a pending rename input via the single hook.
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:cfg:")
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
					// Wave K-B3: the not-found fallback toast is an alert.
					_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ Нода не найдена").WithShowAlert())
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
			// Mutating (admin-gated via isMutatingCallback). Wave K-B2 two-step
			// reset (wired by K-B3): the bare route renders the «Точно
			// сбросить ВСЕ…?» confirm screen (B2's helpers); only the explicit
			// «:yes» callback clears the node's overrides through nodeMgr.
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:nreset:")
			confirming := strings.HasSuffix(nodeName, ":yes")
			nodeName = strings.TrimSuffix(nodeName, ":yes")
			if b.nodeMgr != nil {
				if !confirming {
					b.editWithMarkup(chatID, msgID,
						nodeResetConfirmText(nodeName),
						nodeResetConfirmMarkup(nodeName))
					return
				}
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
		} else if strings.HasPrefix(cb.Data, "menu:nodes:ndel:") {
			// Wave M-A M1 (U-1b): two-step node delete, mirroring the nreset
			// pattern. Mutating (admin-gated via isMutatingCallback): the bare
			// route renders the «Точно удалить ноду…?» confirm screen (M1's
			// helpers); only the explicit «:yes» callback removes the node
			// through nodeMgr — the token is revoked and the persisted record
			// with its settings overrides is deleted (nodes.Registry).
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:ndel:")
			confirming := strings.HasSuffix(nodeName, ":yes")
			nodeName = strings.TrimSuffix(nodeName, ":yes")
			if b.nodeMgr != nil {
				if !confirming {
					b.editWithMarkup(chatID, msgID,
						nodeDelConfirmText(nodeName),
						nodeDelConfirmMarkup(nodeName))
					return
				}
				if err := b.nodeMgr.RemoveNode(nodeName); err != nil {
					logger.Warn("Telegram: node delete failed for %s: %v", nodeName, err)
					if b.api != nil {
						_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ "+err.Error()).WithShowAlert())
					}
					text, markup := b.getNodeCfgScreen(nodeName)
					b.editWithMarkup(chatID, msgID, text, markup)
					return
				}
				if b.api != nil {
					_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("🗑 Нода удалена"))
				}
				b.editWithMarkup(chatID, msgID,
					fmt.Sprintf("✅ Нода <b>%s</b> удалена с мастера.", escapeHTML(nodeName)),
					NodesMainMenuMarkup())
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
				// A context switch from a pending rename input disarms it via
				// the wave L-1 hook.
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
			// Wave G5a, migrated to the wave L-1 FSM: arm the rename pending
			// input (the node name rides as payload) and prompt for the new
			// name as a text reply, consumed in handleMessage via takePending.
			// Mutating (admin-gated via isMutatingCallback) and self-toasting
			// (isToastCallbackData).
			nodeName := strings.TrimPrefix(cb.Data, "menu:nodes:ren:")
			b.armPending(chatID, pendingKindNodeRename, nodeName)
			if b.api != nil {
				_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("✏️ Отправьте новое имя ноды сообщением в чат"))
			}
			text := fmt.Sprintf("✏️ <b>Переименование ноды</b> <code>%s</code>\n\n"+
				"Отправьте новое имя ответным сообщением в этот чат "+
				"(латиница, цифры, дефис, до 32 символов, например: <code>germany-2</code>).\n\n"+
				"Токен, подписки и настройки ноды перенесутся автоматически.\n\n"+
				"<i>Или выполните команду:</i> <code>/noderename %s &lt;новое_имя&gt;</code>",
				escapeHTML(nodeName), escapeHTML(nodeName)) + promptTTLHint
			// Wave L-1: the «Отмена» button clears the pending input (toast,
			// no navigation); the back row keeps the way to the node cfg
			// screen, composed via cbNodeCfg; over-limit names fall back to
			// the static picker route (Wave-F B2 discipline).
			cancelCb, cancelOK := cbNodeCfg("menu:nodes:cfg:", nodeName)
			if !cancelOK {
				cancelCb = "menu:nodes:cfg"
			}
			b.editWithMarkup(chatID, msgID, text, tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					btn("🚫 Отмена", "menu:cancelinput"),
				),
				tu.InlineKeyboardRow(
					btnIcon(IconBack, " К настройкам ноды", cancelCb),
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

			renderDetails := func(reports []checker.ProxyDiagReport, cacheNote string) {
				tabs := b.getNodeTabs(target)
				if b.isRichMode() {
					rich, totalPages := b.buildDiagnosticsDetailsRichMessageWithTarget(reports, target, page)
					b.showRichReport(ct, msgID, rich, DiagnosticsPaginationMarkupWithTabs(target, page, totalPages, tabs))
				} else {
					pageText, totalPages := b.getDiagnosticsPageTextWithTarget(reports, target, page)
					pageText += cacheNote
					b.editWithMarkup(chatID, msgID, pageText, DiagnosticsPaginationMarkupWithTabs(target, page, totalPages, tabs))
				}
			}

			if target == "local" || target == "" {
				// Fast path: fresh cache — render immediately, no network.
				if cached := b.getCachedDiagnosticsReports(); len(cached) > 0 {
					renderDetails(cached, b.diagCacheAgeNote())
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
					renderDetails(reports, "")
				})
				return
			}
			// Remote node reports come from memory (no probe sweep).
			renderDetails(b.getNodeDiagnosticsReports(target), "")
		} else if strings.HasPrefix(cb.Data, "menu:stats:incpg:") {
			// Wave K-B1 integration (I2): the incident journal pager —
			// route «menu:stats:incpg:<filter>:<page>»; the tab keyboard is
			// rebuilt for the filter and the page row comes from B1's
			// renderIncidentsPage. View route — not gated, not self-toasting.
			rest := strings.TrimPrefix(cb.Data, "menu:stats:incpg:")
			sep := strings.LastIndex(rest, ":")
			if sep == -1 {
				return
			}
			page, err := strconv.Atoi(rest[sep+1:])
			if err != nil {
				return
			}
			filter := rest[:sep]
			tabs := b.getNodeTabs("")
			b.renderIncidentsPage(chatID, msgID, filter, page, IncidentsFilterMarkup(filter, tabs))
		} else if strings.HasPrefix(cb.Data, "menu:stats:incidents:") {
			filter := strings.TrimPrefix(cb.Data, "menu:stats:incidents:")
			tabs := b.getNodeTabs("")
			// Wave K-B1 integration (I2): the filter re-render is paginated
			// (page 1 on entry; the pager routes above flip pages).
			b.renderIncidentsPage(chatID, msgID, filter, 1, IncidentsFilterMarkup(filter, tabs))
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

			renderPickDeep := func(reports []checker.ProxyDiagReport, cacheNote string) {
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
				text += cacheNote
				tabs := b.getNodeTabs(target)
				markup := PickDeepDiagnosticsMarkupWithTabs(target, tabs, pageReports, page)
				b.editWithMarkup(chatID, msgID, text, markup)
			}

			if target == "local" || target == "" {
				// Fast path: fresh cache — render immediately, no network.
				if cached := b.getCachedDiagnosticsReports(); len(cached) > 0 {
					renderPickDeep(cached, b.diagCacheAgeNote())
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
					renderPickDeep(reports, "")
				})
				return
			}
			// Remote node reports come from memory (no probe sweep).
			renderPickDeep(b.getNodeDiagnosticsReports(target), "")
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
		} else if strings.HasPrefix(cb.Data, "menu:checkhosth:") {
			// Wave H6: per-host Check-Host check route — resolve the
			// HostCallbackKey back to the union host (snapshot address or
			// custom) and run the TCP check; bare hosts get the :443 default.
			// Order invariant (mirrors menu:cheburh): this prefix branch (and
			// the menu:checkhostpg: pager below) can never collide with the
			// bare "menu:checkhost" exact-match switch case above — keep both
			// rules distinct if this routing is reworked.
			hostKey := strings.TrimPrefix(cb.Data, "menu:checkhosth:")
			go b.handleCheckHostHostKey(chatID, msgID, hostKey)
		} else if strings.HasPrefix(cb.Data, "menu:checkhostdel:") {
			// Wave H6: remove a custom Check-Host host. Mutating (admin-gated
			// via isMutatingCallback) and self-toasting (isToastCallbackData);
			// writes only through updateConfig — see handleCheckHostDel.
			b.handleCheckHostDel(chatID, msgID, cb, cb.Data)
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
			// Wave K-B3: the browsed page is remembered for the probe result
			// (the requested page — the render below clamps out-of-range
			// pages into range, and so does the result's return route).
			b.noteCheburPage(chatID, page)
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
			// Wave H6: the pager recomputes the UNION list (subscription
			// hosts + customs) from the current sources, mirroring the
			// menu:cheburpg: recompute-and-clamp discipline. View route — not
			// gated, not self-toasting.
			page, _ := strconv.Atoi(strings.TrimPrefix(cb.Data, "menu:checkhostpg:"))
			items := b.checkHostListItems()
			totalPages := menuTotalPages(len(items), menuListPageSize)
			if page < 1 {
				page = 1
			}
			if page > totalPages {
				page = totalPages
			}
			b.editWithMarkup(chatID, msgID, b.getCheckHostMenuText(), CheckHostMenuMarkup(items, page, totalPages))
		} else if strings.HasPrefix(cb.Data, "menu:statuspg:") {
			// Wave K-B1 integration (I1): the /status pager. Pages clamp into
			// range inside renderStatusPage. View route — not gated, not
			// self-toasting. Order invariant: this prefix branch can never
			// collide with the bare exact case "menu:status" above.
			page, _ := strconv.Atoi(strings.TrimPrefix(cb.Data, "menu:statuspg:"))
			b.renderStatusPage(chatID, msgID, page)
		} else if strings.HasPrefix(cb.Data, "menu:interval:set:") {
			secStr := strings.TrimPrefix(cb.Data, "menu:interval:set:")
			if sec, err := strconv.Atoi(secStr); err == nil && sec >= 10 {
				b.updateCheckInterval(sec)
			}
			b.editWithMarkup(chatID, msgID, b.getIntervalText(), IntervalMenuMarkup(b.getIntervalSec()))
		}
	}
}

// handleSubURLInput persists the subscription URL delivered through the
// menu:subsadd arm flow (wave K-B3) — the same validation/reload path as the
// /addsub command: http(s)-only URLs (isAllowedSubURL), the write goes only
// through SubscriptionManager.AddSubscription, and the result is the shared
// add-report view.
func (b *Bot) handleSubURLInput(msg *telego.Message, raw string) {
	t := targetFromMessage(msg)
	url := strings.TrimSpace(raw)
	if !isAllowedSubURL(url) {
		b.send(t, "❌ Разрешены только URL подписок со схемой http:// или https://")
		return
	}
	if b.subs == nil {
		b.send(t, "Управление подписками через бота выключено.")
		return
	}

	sent, _ := b.sendAndReturn(t, "⏳ Проверка и добавление подписки…")
	placeholderID := 0
	if sent != nil {
		placeholderID = sent.GetMessageID()
	}

	count, err := b.subs.AddSubscription(url)
	if err != nil {
		errText := fmt.Sprintf("❌ Не удалось добавить подписку.\n%s", escapeHTML(err.Error()))
		if placeholderID > 0 {
			b.edit(t.ChatID, placeholderID, errText)
		} else {
			b.send(t, errText)
		}
		return
	}

	report := b.buildAddSubReport(count)
	if placeholderID > 0 {
		b.edit(t.ChatID, placeholderID, report)
	} else {
		b.send(t, report)
	}
}
