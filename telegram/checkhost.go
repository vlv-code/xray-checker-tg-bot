package telegram

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/checker"
	"xray-checker/logger"
	"xray-checker/metrics"
)

func (b *Bot) getCheckHostMenuText() string {
	// Wave K-B3: the Check-Host header carries the 📡 antenna icon through
	// the dual layer (raw tg-emoji while enabled — the text is static, so no
	// escapeHTML pass ever touches the tag).
	return renderIcon(IconCheckHost) + " <b>Глобальная проверка доступности через Check-Host.net</b>\n\n" +
		"Список хостов: из подписок (помечены «авто», удалить нельзя) и добавленные вручную. " +
		"Выберите хост для проверки через узлы по всему миру (Россия, Европа, США, Азия).\n\n" +
		"Или отправьте команду с любым хостом:\n" +
		"<code>/checkhost &lt;хост[:порт]&gt;</code>\n" +
		"<i>Примеры: <code>/checkhost 185.120.45.10:443</code> или <code>/checkhost mydomain.com</code></i>"
}

// CheckHostListItem is one row of the Check-Host submenu union list (Wave H6):
// a subscription host (Auto, marked «авто», not removable) or a manually added
// custom from cfg.CheckHostHosts (removable via menu:checkhostdel:).
type CheckHostListItem struct {
	Address string // display form: the snapshot address or the stored custom value
	Auto    bool   // subscription host — non-removable, dedup has priority over customs
}

// checkHostNormKey normalizes a host[:port] entry for case-insensitive dedup:
// the host part is lowercased and a bare host gets the :443 default, so a
// custom "Example.com" collides with the subscription entry "example.com:443".
func checkHostNormKey(addr string) string {
	addr = strings.TrimSpace(addr)
	if !strings.Contains(addr, ":") {
		addr += ":443"
	}
	return strings.ToLower(addr)
}

// checkHostUnionList builds the Check-Host submenu source (Wave H6): enabled
// snapshot addresses as auto entries first (dedup priority), then the customs
// from cfg.CheckHostHosts; the union is deduped case-insensitively and sorted
// stably by the lowercased address.
func checkHostUnionList(snapshot []metrics.ProxyMetric, customs []string) []CheckHostListItem {
	seen := make(map[string]bool)
	items := make([]CheckHostListItem, 0, len(snapshot)+len(customs))
	for _, pm := range snapshot {
		if pm.Disabled || pm.Address == "" {
			continue
		}
		key := checkHostNormKey(pm.Address)
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, CheckHostListItem{Address: pm.Address, Auto: true})
	}
	for _, c := range customs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		key := checkHostNormKey(c)
		// An auto (or an earlier custom) entry already covers this host.
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, CheckHostListItem{Address: c})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Address) < strings.ToLower(items[j].Address)
	})
	return items
}

// checkHostListItems recomputes the union list from the current snapshot and
// config — used by the submenu render, the pager and the delete re-render.
func (b *Bot) checkHostListItems() []CheckHostListItem {
	var snapshot []metrics.ProxyMetric
	if b.source != nil {
		snapshot = b.source.MetricsSnapshot()
	}
	return checkHostUnionList(snapshot, b.GetConfig().CheckHostHosts)
}

func (b *Bot) handleCheckHostCommand(msg *telego.Message) {
	t := targetFromMessage(msg)
	target := commandArg(msg.Text)
	if target == "" {
		b.send(t, "💡 <b>Использование:</b> <code>/checkhost &lt;хост[:порт]&gt;</code>\n\n"+
			"Примеры:\n"+
			"• <code>/checkhost 185.120.45.10:443</code>\n"+
			"• <code>/checkhost mydomain.com</code>\n\n"+
			"Или выберите прокси в меню: /menu")
		return
	}

	if !strings.Contains(target, ":") {
		target = target + ":443"
	}

	sentMsg, _ := b.sendAndReturn(t, fmt.Sprintf("⏳ <b>Запрос отправлен в Check-Host.net...</b>\n"+
		"Проверяем <code>%s</code> (TCP) по глобальной сети узлов (РФ, Европа, США, Азия)...\n"+
		"Пожалуйста, подождите 4–6 секунд...", escapeHTML(target)))

	chClient := checker.NewCheckHostClient("", 1500*time.Millisecond)
	ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
	defer cancel()

	summary, err := chClient.CheckTCP(ctx, target, checker.DefaultWorldwideNodes)
	if err != nil {
		errMsg := fmt.Sprintf("❌ <b>Ошибка Check-Host:</b> %s", escapeHTML(err.Error()))
		if sentMsg != nil {
			b.editWithMarkup(t.ChatID, sentMsg.GetMessageID(), errMsg, nil)
		} else {
			b.send(t, errMsg)
		}
		return
	}

	report := checker.FormatCheckHostReport(summary)
	if sentMsg != nil {
		b.editWithMarkup(t.ChatID, sentMsg.GetMessageID(), report, nil)
	} else {
		b.send(t, report)
	}
}

// resolveCheckHostTarget reverse-looks-up a HostCallbackKey against the union
// sources (snapshot addresses, then cfg.CheckHostHosts customs) and returns
// the target for CheckTCP: the stored value, with the :443 default for bare
// hosts (mirrors the /checkhost command semantics).
func (b *Bot) resolveCheckHostTarget(hostKey string) string {
	for _, item := range b.checkHostListItems() {
		if HostCallbackKey(item.Address) == hostKey {
			target := item.Address
			if !strings.Contains(target, ":") {
				target = target + ":443"
			}
			return target
		}
	}
	return ""
}

// handleCheckHostHostKey runs the on-demand Check-Host TCP check for one host
// resolved from a menu:checkhosth:<HostCallbackKey> button (Wave H6, mirror of
// the chebur host handler): the key is reverse-looked-up against the union
// list and the bare host gets the :443 default.
func (b *Bot) handleCheckHostHostKey(chatID int64, msgID int, hostKey string) {
	seq := b.nextMsgSeq(chatID, msgID)

	target := b.resolveCheckHostTarget(hostKey)
	if target == "" {
		b.editWithMarkup(chatID, msgID, "❌ Хост не найден в текущей конфигурации.", BackToMenuMarkup())
		return
	}

	b.editWithMarkup(chatID, msgID, fmt.Sprintf("⏳ <b>Запрос отправлен в Check-Host.net...</b>\n"+
		"Проверяем <code>%s</code> (TCP) по всемирной сети узлов (РФ, Европа, США, Азия)...\n"+
		"Пожалуйста, подождите 4–6 секунд...", escapeHTML(target)), BackToMenuMarkup())

	chClient := b.checkHostClient
	if chClient == nil {
		chClient = checker.NewCheckHostClient("", 1500*time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
	defer cancel()

	summary, err := chClient.CheckTCP(ctx, target, checker.DefaultWorldwideNodes)
	if !b.isMsgSeqValid(chatID, msgID, seq) {
		return
	}
	if err != nil {
		b.editWithMarkup(chatID, msgID, fmt.Sprintf("❌ <b>Ошибка Check-Host:</b> %s", escapeHTML(err.Error())), BackToMenuMarkup())
		return
	}

	b.editWithMarkup(chatID, msgID, checker.FormatCheckHostReport(summary), CheckHostResultMarkup())
}

func (b *Bot) getCheckHostSettingsText() string {
	cfg := b.GetConfig()
	bgStatus := "❌ Выключена"
	if cfg.CheckHostBgEnabled {
		bgStatus = "✅ Включена"
	}

	alertStatus := "🔕 Выключены"
	if cfg.CheckHostAlertEnabled {
		alertStatus = "🔔 Включены"
	}

	intHours := cfg.CheckHostIntervalHours
	if intHours <= 0 {
		intHours = 1
	}

	return fmt.Sprintf("%s <b>Настройки фоновой проверки Check-Host</b>\n\n"+
		"• Фоновая проверка: <b>%s</b>\n"+
		"• Периодичность: <b>каждые %d ч.</b>\n"+
		"• Алерты по РФ: <b>%s</b>\n\n"+
		"Периодическая проверка хостов из российских и зарубежных локаций через Check-Host.net. Оповещает при блокировках или сетевых сбоях в РФ.\n\n"+
		"<i>Отключённые в настройках хосты автоматически пропускаются.</i>",
		renderIcon(IconCheckHost), bgStatus, intHours, alertStatus)
}

func (b *Bot) handleCheckHostBgCommand(msg *telego.Message) {
	t := targetFromMessage(msg)
	arg := strings.TrimSpace(commandArg(msg.Text))
	cfg := b.GetConfig()
	if arg == "" {
		b.sendWithMarkup(t, b.getCheckHostSettingsText(), CheckHostSettingsMarkup(cfg))
		return
	}

	switch strings.ToLower(arg) {
	case "on", "enable", "1":
		_ = b.updateConfig(func(c *BotConfig) {
			c.CheckHostBgEnabled = true
		})
		b.send(t, "✅ Фоновая проверка Check-Host <b>включена</b>.")
	case "off", "disable", "0":
		_ = b.updateConfig(func(c *BotConfig) {
			c.CheckHostBgEnabled = false
		})
		b.send(t, "❌ Фоновая проверка Check-Host <b>выключена</b>.")
	case "alert_on", "alerts_on":
		_ = b.updateConfig(func(c *BotConfig) {
			c.CheckHostAlertEnabled = true
		})
		b.send(t, "🔔 Алерты по недоступности из РФ <b>включены</b>.")
	case "alert_off", "alerts_off":
		_ = b.updateConfig(func(c *BotConfig) {
			c.CheckHostAlertEnabled = false
		})
		b.send(t, "🔕 Алерты по недоступности из РФ <b>выключены</b>.")
	case "run", "now":
		b.send(t, "🚀 Запуск фоновой проверки Check-Host...")
		go b.RunCheckHostAudit()
	default:
		cleanArg := strings.TrimSuffix(strings.ToLower(arg), "h")
		cleanArg = strings.TrimSuffix(cleanArg, "ч")
		if hours, err := strconv.Atoi(cleanArg); err == nil && hours > 0 {
			_ = b.updateConfig(func(c *BotConfig) {
				c.CheckHostIntervalHours = hours
			})
			b.send(t, fmt.Sprintf("⏱️ Интервал фонового Check-Host установлен на <b>каждые %d ч.</b>", hours))
		} else {
			b.send(t, "Использование: <code>/checkhost_bg [on|off|alert_on|alert_off|1h|2h|run]</code>")
		}
	}
}

// validateCheckHostHostInput validates manual Check-Host host input (Wave H6,
// RU errors): empty input, URLs/schemes/paths, whitespace and unparsable colon
// garbage are rejected; the port part of a host:port pair must be NUMERIC —
// net.SplitHostPort alone does not validate that (wave H2 lesson). A bare
// host (domain or IP) passes and keeps its stored form.
func validateCheckHostHostInput(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("Пустой ввод. Отправьте хост (домен или IP) или host:port, например example.com")
	}
	if strings.ContainsAny(raw, " \t") || strings.Contains(raw, "/") || strings.Contains(raw, "://") {
		return "", errors.New("Не похоже на хост. Отправьте домен или IP без схемы и пути, например example.com или 1.2.3.4:443")
	}
	if strings.Contains(raw, ":") {
		h, p, err := net.SplitHostPort(raw)
		if err != nil || h == "" || !isCheburNumericPort(p) {
			return "", errors.New("Не удалось разобрать адрес. Отправьте хост или host:port с числовым портом, например example.com или 1.2.3.4:443")
		}
	}
	return raw, nil
}

// handleCheckHostHostInput consumes the manual host input armed by the
// «➕ Добавить хост» button (Wave H6, route menu:checkhostadd): the armed
// pendingInput FSM entry (wave L-1, admin-gated in handleMessage) delivers
// the next plain text here; host[:port] is validated
// (numeric-port rule), deduped against the union (auto hosts are refused with
// a RU notice) and persisted via updateConfig — the only config writer.
func (b *Bot) handleCheckHostHostInput(msg *telego.Message, raw string) {
	t := targetFromMessage(msg)

	host, err := validateCheckHostHostInput(raw)
	if err != nil {
		b.send(t, "❌ "+err.Error())
		return
	}

	key := checkHostNormKey(host)
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.Disabled || pm.Address == "" {
				continue
			}
			if checkHostNormKey(pm.Address) == key {
				b.send(t, fmt.Sprintf("❌ Хост <code>%s</code> уже есть в подписках (авто) — он и так проверяется, добавлять его не нужно.", escapeHTML(host)))
				return
			}
		}
	}
	for _, c := range b.GetConfig().CheckHostHosts {
		if checkHostNormKey(c) == key {
			b.send(t, fmt.Sprintf("❌ Хост <code>%s</code> уже добавлен.", escapeHTML(host)))
			return
		}
	}

	if err := b.updateConfig(func(c *BotConfig) {
		c.CheckHostHosts = append(c.CheckHostHosts, host)
	}); err != nil {
		b.send(t, "❌ Не удалось сохранить хост: "+escapeHTML(err.Error()))
		return
	}

	b.sendWithMarkup(t, fmt.Sprintf("✅ Хост добавлен: <code>%s</code>\n\n"+
		"Теперь он есть в списке Check-Host и войдёт в фоновую проверку доступности из РФ.", escapeHTML(host)),
		tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				btn("📡 К списку хостов", "menu:checkhost"),
			),
		))
}

// handleCheckHostDel removes a custom Check-Host host (Wave H6, route
// menu:checkhostdel:<HostCallbackKey>): the key is resolved against the stored
// customs; a host that also exists as a subscription (auto) entry is refused
// with the «есть в подписках» toast; success re-renders the union list.
func (b *Bot) handleCheckHostDel(chatID int64, msgID int, cb *telego.CallbackQuery, data string) {
	hostKey := strings.TrimPrefix(data, "menu:checkhostdel:")

	cfg := b.GetConfig()
	var target string
	for _, c := range cfg.CheckHostHosts {
		if HostCallbackKey(strings.TrimSpace(c)) == hostKey {
			target = strings.TrimSpace(c)
			break
		}
	}
	if target == "" {
		if b.api != nil {
			// Wave K-B3: the not-found error toast is an alert.
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ Хост не найден среди добавленных").WithShowAlert())
		}
		return
	}

	// The auto overlap is unreachable through the menu (shadowed customs are
	// not rendered) but is guarded defensively: subscription hosts never leave
	// through this route.
	key := checkHostNormKey(target)
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.Disabled || pm.Address == "" {
				continue
			}
			if checkHostNormKey(pm.Address) == key {
				if b.api != nil {
					_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ Хост есть в подписках — удалить его нельзя"))
				}
				return
			}
		}
	}

	removed := false
	_ = b.updateConfig(func(c *BotConfig) {
		kept := make([]string, 0, len(c.CheckHostHosts))
		for _, h := range c.CheckHostHosts {
			if HostCallbackKey(strings.TrimSpace(h)) == hostKey {
				removed = true
				continue
			}
			kept = append(kept, h)
		}
		c.CheckHostHosts = kept
	})
	if !removed {
		if b.api != nil {
			// Wave K-B3: the removal-failure error toast is an alert.
			_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("❌ Не удалось удалить хост").WithShowAlert())
		}
		return
	}

	if b.api != nil {
		_ = b.api.AnswerCallbackQuery(b.ctx, tu.CallbackQuery(cb.ID).WithText("Удалён: "+target))
	}
	items := b.checkHostListItems()
	b.editWithMarkup(chatID, msgID, b.getCheckHostMenuText(),
		CheckHostMenuMarkup(items, 1, menuTotalPages(len(items), menuListPageSize)))
}

// RunCheckHostAudit checks all unique active proxy hosts via Check-Host and alerts
// if unreachable from Russia. Respects the background-audit gate (CheckHostBgEnabled):
// used by the scheduler and the /checkhost_bg run command.
func (b *Bot) RunCheckHostAudit() {
	if !b.GetConfig().CheckHostBgEnabled {
		return
	}
	b.runAuditNow()
}

// runAuditNow performs the Check-Host audit unconditionally. Explicit
// user-triggered runs (menu:checkhost:run_now button) must bypass the
// background-enabled gate so the button never silently no-ops.
func (b *Bot) runAuditNow() {
	b.checkHostMu.Lock()
	if b.checkHostRunning {
		b.checkHostMu.Unlock()
		return
	}
	b.checkHostRunning = true
	b.checkHostMu.Unlock()

	defer func() {
		b.checkHostMu.Lock()
		b.checkHostRunning = false
		b.checkHostMu.Unlock()
	}()

	cfg := b.GetConfig()

	snapshot := b.source.MetricsSnapshot()

	type auditTarget struct {
		targetAddr string
		proxyName  string
		stableID   string
	}

	seen := make(map[string]bool)
	var targets []auditTarget

	for _, pm := range snapshot {
		// Audit ownership split: node hosts are audited by their own node
		// and delivered via reports; the master audits only local proxies.
		if pm.NodeName != "" || pm.Disabled {
			continue
		}
		host, _, err := net.SplitHostPort(pm.Address)
		if err != nil {
			host = pm.Address
		}
		if cfg.IsDisabled(host, pm.StableID) {
			continue
		}

		// The Check-Host API cannot verify UDP services: UDP-protocol targets
		// are excluded from passive audits entirely (user decision 2026-11-16).
		if checker.IsUDPProto(pm.Protocol) {
			logger.Debug("Check-Host bg audit: skip UDP target %s (API cannot verify UDP)", pm.Address)
			continue
		}

		// Wave H6: the dedup key is lowercased so the customs appended below
		// dedup case-insensitively against subscription hosts (the audited
		// address keeps its original form — alert keys stay unchanged).
		dedupKey := strings.ToLower(pm.Address)
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true

		targets = append(targets, auditTarget{
			targetAddr: pm.Address,
			proxyName:  pm.Name,
			stableID:   pm.StableID,
		})
	}

	// Wave H6 (decision D3): manually added hosts (cfg.CheckHostHosts) join
	// the background RU-block audit. A bare custom host is audited as
	// host:443; customs matching a subscription host case-insensitively are
	// deduped away; hosts disabled in settings are skipped like subscription
	// hosts; the alert keys keep the checkhost:<addr> form. Customs are
	// audited even when the snapshot is empty.
	for _, custom := range cfg.CheckHostHosts {
		targetAddr := strings.TrimSpace(custom)
		if targetAddr == "" {
			continue
		}
		if !strings.Contains(targetAddr, ":") {
			targetAddr = targetAddr + ":443"
		}
		host, _, err := net.SplitHostPort(targetAddr)
		if err != nil {
			host = targetAddr
		}
		if cfg.IsDisabled(host, "") {
			continue
		}
		dedupKey := strings.ToLower(targetAddr)
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true

		targets = append(targets, auditTarget{
			targetAddr: targetAddr,
			proxyName:  targetAddr,
		})
	}

	if len(targets) == 0 {
		return
	}

	logger.Info("Starting periodic Check-Host audit for %d unique active targets", len(targets))
	chClient := b.checkHostClient
	if chClient == nil {
		chClient = checker.NewCheckHostClient("", 1500*time.Millisecond)
	}
	fastNodes := b.checkHostNodes
	if len(fastNodes) == 0 {
		// Explicit copy: appending to the package-level defaults would share
		// (and on growth, corrupt) their backing array.
		fastNodes = make([]string, 0, len(checker.DefaultFastRUNodes)+len(checker.DefaultFastWorldNodes))
		fastNodes = append(fastNodes, checker.DefaultFastRUNodes...)
		fastNodes = append(fastNodes, checker.DefaultFastWorldNodes...)
	}

	for i, target := range targets {
		if i > 0 {
			time.Sleep(3 * time.Second) // rate-limiting between Check-Host API calls
		}

		ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
		summary, err := chClient.CheckTCP(ctx, target.targetAddr, fastNodes)
		cancel()
		if err != nil || summary == nil {
			continue
		}

		alertKey := fmt.Sprintf("checkhost:%s", target.targetAddr)
		now := time.Now()

		if !summary.RUAvailable && !summary.WorldAvailable {
			// ICMP-filtered or fully down host: no asymmetric RU-block
			// signature. Alerting here would stick forever (resolution
			// requires an RU success), so send and track nothing.
			logger.Warn("Check-Host %s: unreachable from RU and world (ICMP filtered or fully down); no RU-block alert", target.targetAddr)
			continue
		}

		if !summary.RUAvailable {
			// Unreachable from Russian federation nodes while reachable from
			// world nodes: the RU-block signature.
			if !cfg.CheckHostAlertEnabled {
				continue
			}
			isQuiet := IsQuietTime(now, cfg)

			for _, ct := range b.targets {
				if b.tracker.HasAlert(ct.ChatID, ct.ThreadID, alertKey) {
					continue
				}

				alertText := fmt.Sprintf("⚠️ <b>[Check-Host] Проблема доступности из РФ</b>\n\n"+
					"• Сервер: <code>%s</code> <i>(%s)</i>\n"+
					"• Статус: РФ ❌ недоступен | Мир ✅ доступен\n"+
					"• Вердикт: <b>Вероятная блокировка РКН на территории РФ</b>\n"+
					"🔗 <a href=\"%s\">Отчёт Check-Host</a>",
					escapeHTML(target.targetAddr), escapeHTML(target.proxyName),
					escapeHTML(summary.PermanentLink))

				if isQuiet {
					b.eventBuffer.Add(BufferedEvent{
						Timestamp: now,
						Type:      "down",
						ProxyName: fmt.Sprintf("[Check-Host] %s", target.proxyName),
						Reason:    "Недоступен из РФ",
					})
				} else {
					if sent, err := b.sendAndReturn(ct, alertText); err == nil {
						b.tracker.Track(ct.ChatID, ct.ThreadID, sent.MessageID, alertKey, target.proxyName, now, "CheckHost RU Block")
					}
				}
			}
		} else {
			// RU is available: resolve any previous alert
			for _, ct := range b.targets {
				alert, hadAlert := b.tracker.Resolve(ct.ChatID, ct.ThreadID, alertKey)
				if !hadAlert {
					continue
				}

				downtime := now.Sub(alert.DownAt)
				if cfg.AlertMode == AlertModeClean {
					if b.api != nil {
						_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
							ChatID:    tu.ID(ct.ChatID),
							MessageID: alert.MessageID,
						})
					}
					if b.notifyOnRecovery {
						recText := fmt.Sprintf("✅ <b>[Check-Host] Доступность из РФ восстановилась</b>\n\n• Сервер: <code>%s</code> <i>(%s)</i>",
							escapeHTML(target.targetAddr), escapeHTML(target.proxyName))
						if downtime > 0 {
							recText += fmt.Sprintf("\n• Был недоступен: <b>%s</b>", FormatDowntime(downtime))
						}
						if sent, err := b.sendAndReturn(ct, recText); err == nil {
							go func(cID int64, mID int) {
								time.Sleep(2 * time.Minute)
								if b.api != nil {
									_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
										ChatID:    tu.ID(cID),
										MessageID: mID,
									})
								}
							}(ct.ChatID, sent.MessageID)
						}
					}
				} else { // AlertModeLive
					if b.api != nil {
						liveText := fmt.Sprintf("✅ <b>[Check-Host] Доступность из РФ восстановилась</b>\n\n• Сервер: <code>%s</code> <i>(%s)</i> (был недоступен %s)",
							escapeHTML(target.targetAddr), escapeHTML(target.proxyName), FormatDowntime(downtime))
						params := &telego.EditMessageTextParams{
							ChatID:    tu.ID(ct.ChatID),
							MessageID: alert.MessageID,
							Text:      liveText,
							ParseMode: telego.ModeHTML,
						}
						_, _ = b.api.EditMessageText(b.ctx, params)
					}
				}
			}
		}
	}
}

// ProcessNodeCheckHostAudits raises Check-Host RU-block alerts for hosts
// audited by a remote node (audit ownership split: the node audits its own
// hosts and ships the summaries in its reports; the master alerts on them).
// The alert key space matches the master's own audit, so a host is tracked
// exactly once; processing is idempotent per report via the alert tracker.
func (b *Bot) ProcessNodeCheckHostAudits(nodeName string, audits map[string]checker.CheckHostSummary) {
	if len(audits) == 0 || nodeName == "" {
		return
	}
	cfg := b.GetConfig()

	// Resolve display proxy names from the merged snapshot.
	names := make(map[string]string, len(audits))
	if b.source != nil {
		for _, pm := range b.source.MetricsSnapshot() {
			if pm.NodeName == nodeName && pm.Address != "" {
				names[pm.Address] = pm.Name
			}
		}
	}

	now := time.Now()
	for addr, summary := range audits {
		alertKey := fmt.Sprintf("checkhost:%s", addr)
		proxyName := addr
		if n, ok := names[addr]; ok && n != "" {
			proxyName = n
		}

		if !summary.RUAvailable && !summary.WorldAvailable {
			// ICMP-filtered or fully down host: no asymmetric RU-block
			// signature. Alerting here would stick forever (resolution
			// requires an RU success), so send and track nothing.
			logger.Warn("Check-Host %s: unreachable from RU and world (ICMP filtered or fully down); no RU-block alert", addr)
			continue
		}

		if !summary.RUAvailable {
			// Unreachable from Russian federation nodes while reachable from
			// world nodes: the RU-block signature.
			if !cfg.CheckHostAlertEnabled {
				continue
			}
			isQuiet := IsQuietTime(now, cfg)

			for _, ct := range b.targets {
				if b.tracker.HasAlert(ct.ChatID, ct.ThreadID, alertKey) {
					continue
				}

				alertText := fmt.Sprintf("⚠️ <b>[Check-Host] Проблема доступности из РФ</b>\n\n"+
					"• Сервер: <code>%s</code> <i>(%s)</i> — через ноду %s\n"+
					"• Статус: РФ ❌ недоступен | Мир ✅ доступен\n"+
					"• Вердикт: <b>Вероятная блокировка РКН на территории РФ</b>\n"+
					"🔗 <a href=\"%s\">Отчёт Check-Host</a>",
					escapeHTML(addr), escapeHTML(proxyName), escapeHTML(nodeName),
					escapeHTML(summary.PermanentLink))

				if isQuiet {
					b.eventBuffer.Add(BufferedEvent{
						Timestamp: now,
						Type:      "down",
						ProxyName: fmt.Sprintf("[Check-Host] %s", proxyName),
						Reason:    "Недоступен из РФ",
					})
				} else {
					if sent, err := b.sendAndReturn(ct, alertText); err == nil {
						b.tracker.Track(ct.ChatID, ct.ThreadID, sent.MessageID, alertKey, proxyName, now, "CheckHost RU Block")
					}
				}
			}
		} else {
			// RU is available: resolve any previous alert.
			for _, ct := range b.targets {
				alert, hadAlert := b.tracker.Resolve(ct.ChatID, ct.ThreadID, alertKey)
				if !hadAlert {
					continue
				}

				downtime := now.Sub(alert.DownAt)
				if cfg.AlertMode == AlertModeClean {
					if b.api != nil {
						_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
							ChatID:    tu.ID(ct.ChatID),
							MessageID: alert.MessageID,
						})
					}
					if b.notifyOnRecovery {
						recText := fmt.Sprintf("✅ <b>[Check-Host] Доступность из РФ восстановилась</b>\n\n• Сервер: <code>%s</code> <i>(%s)</i> — через ноду %s",
							escapeHTML(addr), escapeHTML(proxyName), escapeHTML(nodeName))
						if downtime > 0 {
							recText += fmt.Sprintf("\n• Был недоступен: <b>%s</b>", FormatDowntime(downtime))
						}
						if sent, err := b.sendAndReturn(ct, recText); err == nil {
							go func(cID int64, mID int) {
								time.Sleep(2 * time.Minute)
								if b.api != nil {
									_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
										ChatID:    tu.ID(cID),
										MessageID: mID,
									})
								}
							}(ct.ChatID, sent.MessageID)
						}
					}
				}
			}
		}
	}
}
