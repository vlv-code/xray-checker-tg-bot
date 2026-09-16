package telegram

import (
	"context"
	"fmt"
	"net"
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
	return "🌐 <b>Глобальная проверка доступности через Check-Host.net</b>\n\n" +
		"Выберите прокси из списка ниже для проверки через узлы по всему миру (Россия, Европа, США, Азия):\n\n" +
		"Или отправьте команду с любым хостом:\n" +
		"<code>/checkhost &lt;хост[:порт]&gt;</code>\n" +
		"<i>Примеры: <code>/checkhost 185.120.45.10:443</code> или <code>/checkhost mydomain.com</code></i>"
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

func (b *Bot) handleCheckHostProxy(chatID int64, msgID int, stableID string) {
	seq := b.nextMsgSeq(chatID, msgID)
	snapshot := b.source.MetricsSnapshot()
	var targetProxy *metrics.ProxyMetric
	for i := range snapshot {
		if snapshot[i].StableID == stableID {
			targetProxy = &snapshot[i]
			break
		}
	}

	if targetProxy == nil {
		b.editWithMarkup(chatID, msgID, "❌ Прокси не найден в текущей конфигурации.", BackToMenuMarkup())
		return
	}

	target := targetProxy.Address
	if !strings.Contains(target, ":") {
		target = target + ":443"
	}

	b.editWithMarkup(chatID, msgID, fmt.Sprintf("⏳ <b>Запрос отправлен в Check-Host.net...</b>\n"+
		"Проверяем <b>%s</b> (<code>%s</code>) по всемирной сети узлов...\nПожалуйста, подождите 4–6 секунд...",
		escapeHTML(targetProxy.Name), escapeHTML(target)), BackToMenuMarkup())

	chClient := checker.NewCheckHostClient("", 1500*time.Millisecond)
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

	report := checker.FormatCheckHostReport(summary)
	b.editWithMarkup(chatID, msgID, report, CheckHostResultMarkup())
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

	return fmt.Sprintf("🌐 <b>Настройки фоновой проверки Check-Host</b>\n\n"+
		"• Фоновая проверка: <b>%s</b>\n"+
		"• Периодичность: <b>каждые %d ч.</b>\n"+
		"• Алерты по РФ: <b>%s</b>\n\n"+
		"Периодическая проверка хостов из российских и зарубежных локаций через Check-Host.net. Оповещает при блокировках или сетевых сбоях в РФ.\n\n"+
		"<i>Отключённые в настройках хосты автоматически пропускаются.</i>",
		bgStatus, intHours, alertStatus)
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

// RunCheckHostAudit checks all unique active proxy hosts via Check-Host and alerts if unreachable from Russia.
func (b *Bot) RunCheckHostAudit() {
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
	if !cfg.CheckHostBgEnabled {
		return
	}

	snapshot := b.source.MetricsSnapshot()
	if len(snapshot) == 0 {
		return
	}

	type auditTarget struct {
		targetAddr string
		host       string
		isUDP      bool
		proxyName  string
		stableID   string
	}

	seen := make(map[string]bool)
	var targets []auditTarget

	for _, pm := range snapshot {
		if pm.Disabled {
			continue
		}
		host, _, err := net.SplitHostPort(pm.Address)
		if err != nil {
			host = pm.Address
		}
		if cfg.IsDisabled(host, pm.StableID) {
			continue
		}

		isUDP := checker.IsUDPProto(pm.Protocol)
		dedupKey := pm.Address
		if isUDP {
			dedupKey = host
		}

		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true

		targets = append(targets, auditTarget{
			targetAddr: pm.Address,
			host:       host,
			isUDP:      isUDP,
			proxyName:  pm.Name,
			stableID:   pm.StableID,
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
		fastNodes = append(checker.DefaultFastRUNodes, checker.DefaultFastWorldNodes...)
	}

	for i, target := range targets {
		if i > 0 {
			time.Sleep(3 * time.Second) // rate-limiting between Check-Host API calls
		}

		ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
		var summary *checker.CheckHostSummary
		var err error

		if target.isUDP {
			summary, err = chClient.CheckPing(ctx, target.host, fastNodes)
		} else {
			summary, err = chClient.CheckTCP(ctx, target.targetAddr, fastNodes)
		}
		cancel()
		if err != nil || summary == nil {
			continue
		}

		alertKey := fmt.Sprintf("checkhost:%s", target.targetAddr)
		now := time.Now()

		if !summary.RUAvailable {
			// Unreachable from Russian federation nodes
			if !cfg.CheckHostAlertEnabled {
				continue
			}
			isQuiet := IsQuietTime(now, cfg)

			for _, ct := range b.targets {
				if b.tracker.HasAlert(ct.ChatID, ct.ThreadID, alertKey) {
					continue
				}

				worldStatus := "❌ недоступен"
				verdict := "Хост недоступен как из РФ, так и из других стран"
				if summary.WorldAvailable {
					worldStatus = "✅ доступен"
					verdict = "Вероятная блокировка РКН на территории РФ"
				}

				alertText := fmt.Sprintf("⚠️ <b>[Check-Host] Проблема доступности из РФ</b>\n\n"+
					"• Сервер: <code>%s</code> <i>(%s)</i>\n"+
					"• Статус: РФ ❌ недоступен | Мир %s\n"+
					"• Вердикт: <b>%s</b>\n"+
					"🔗 <a href=\"%s\">Отчёт Check-Host</a>",
					escapeHTML(target.targetAddr), escapeHTML(target.proxyName),
					worldStatus, escapeHTML(verdict), summary.PermanentLink)

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
