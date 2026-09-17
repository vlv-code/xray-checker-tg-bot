package telegram

import (
	"fmt"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"xray-checker/checker"
	"xray-checker/metrics"
)

type deleteAction struct {
	chatID    int64
	messageID int
}

type recoveryAction struct {
	target    ChatTarget
	pm        metrics.ProxyMetric
	alert     *ActiveAlert
	hadAlert  bool
	downtime  time.Duration
	alertMode string
}

// ProcessSnapshot compares the current proxy snapshot against the last known
// state and handles transitions with smart alert lifecycle and stats tracking.
func (b *Bot) ProcessSnapshot(snapshot []metrics.ProxyMetric) {
	b.mu.Lock()

	now := b.now()
	cfg := b.GetConfig()
	isQuiet := IsQuietTime(now, cfg)

	if !b.seeded {
		for _, pm := range snapshot {
			if pm.Disabled {
				continue
			}
			b.lastSeen[pm.StableID] = pm.Online
			if b.statsStore != nil {
				b.statsStore.RecordCheck(pm.StableID, pm.Name, pm.Online, pm.LatencyMs)
				if !pm.Online {
					b.statsStore.RecordInitialDown(pm.StableID, pm.Name, now)
				} else {
					b.statsStore.SyncOnlineState(pm.StableID, now)
				}
			}
			if pm.Online {
				for _, t := range b.targets {
					b.tracker.Resolve(t.ChatID, t.ThreadID, pm.StableID)
				}
			}
		}
		b.seeded = true
		b.mu.Unlock()
		return
	}

	var disabledDeletions []deleteAction
	var outages []metrics.ProxyMetric
	var recoveries []recoveryAction

	seenNow := make(map[string]bool, len(snapshot))
	for _, pm := range snapshot {
		if pm.Disabled {
			// Proxy is disabled: resolve/clean any active alerts and ignore
			for _, t := range b.targets {
				if alert, hadAlert := b.tracker.Resolve(t.ChatID, t.ThreadID, pm.StableID); hadAlert {
					disabledDeletions = append(disabledDeletions, deleteAction{chatID: t.ChatID, messageID: alert.MessageID})
				}
			}
			delete(b.lastSeen, pm.StableID)
			continue
		}

		seenNow[pm.StableID] = true

		if b.statsStore != nil {
			b.statsStore.RecordCheck(pm.StableID, pm.Name, pm.Online, pm.LatencyMs)
		}

		prev, known := b.lastSeen[pm.StableID]
		b.lastSeen[pm.StableID] = pm.Online
		if !known {
			continue
		}

		switch {
		case prev && !pm.Online:
			if b.statsStore != nil {
				b.statsStore.RecordTransition(pm.StableID, pm.Name, false, "Offline", now)
			}

			if isQuiet {
				b.eventBuffer.Add(BufferedEvent{
					Timestamp: now,
					Type:      "down",
					ProxyName: pm.Name,
					Reason:    "Offline",
				})
			} else {
				suppressed := false
				if b.statsStore != nil {
					flaps := b.statsStore.GetFlapCount24h(pm.StableID, now)
					if flaps > 10 {
						lastAlert := b.lastFlapAlert[pm.StableID]
						if !lastAlert.IsZero() && now.Sub(lastAlert) < 15*time.Minute {
							suppressed = true
						} else {
							b.lastFlapAlert[pm.StableID] = now
						}
					}
				}
				if !suppressed {
					outages = append(outages, pm)
				}
			}

		case !prev && pm.Online:
			var downtime time.Duration
			if b.statsStore != nil {
				downtime = b.statsStore.RecordTransition(pm.StableID, pm.Name, true, "", now)
			}

			if isQuiet {
				b.eventBuffer.Add(BufferedEvent{
					Timestamp: now,
					Type:      "up",
					ProxyName: pm.Name,
					LatencyMs: pm.LatencyMs,
					Downtime:  downtime,
				})
			} else {
				for _, t := range b.targets {
					alert, hadAlert := b.tracker.Resolve(t.ChatID, t.ThreadID, pm.StableID)
					if hadAlert && alert != nil && !alert.DownAt.IsZero() {
						downtime = now.Sub(alert.DownAt)
					}
					recoveries = append(recoveries, recoveryAction{
						target:    t,
						pm:        pm,
						alert:     alert,
						hadAlert:  hadAlert,
						downtime:  downtime,
						alertMode: cfg.AlertMode,
					})
				}
			}
		}
	}

	// Drop state for removed proxies
	for id := range b.lastSeen {
		if !seenNow[id] {
			delete(b.lastSeen, id)
			delete(b.lastFlapAlert, id)
		}
	}
	if b.statsStore != nil && len(seenNow) > 0 {
		b.statsStore.PruneInactive(seenNow)
	}

	// Release lock BEFORE executing network calls
	b.mu.Unlock()

	// 1. Process deletions for disabled proxies
	for _, del := range disabledDeletions {
		if b.api != nil {
			_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
				ChatID:    tu.ID(del.chatID),
				MessageID: del.messageID,
			})
		}
	}

	// 2. Process outages
	for _, pm := range outages {
		softHint := ""
		if pm.LastErrorCategory > 0 {
			cat := checker.ErrorCategory(pm.LastErrorCategory)
			hint := checker.FormatSoftHint(cat, checker.IsUDPProto(pm.Protocol))
			softHint = fmt.Sprintf(" (вероятно: %s)", hint)
		}
		timeStr := now.In(b.loc()).Format("15:04")
		dropCount := int64(1)
		if b.statsStore != nil {
			if drops := b.statsStore.GetDropCount(pm.StableID); drops > 0 {
				dropCount = drops
			}
		}
		flapNote := ""
		if b.statsStore != nil && b.statsStore.GetFlapCount24h(pm.StableID, now) > 10 {
			flapNote = fmt.Sprintf("\n⚠️ <i>Частые сбои (%d за 24ч). Алерты приостановлены на 15 мин.</i>", b.statsStore.GetFlapCount24h(pm.StableID, now))
		}
		outageText := fmt.Sprintf("🔴 <b>%s</b> — не отвечает%s%s\n⏱ %s · %d-й сбой\n%s%s", escapeHTML(pm.Name), softHint, nodeLineFor(pm), timeStr, dropCount, escapeHTML(pm.Address), flapNote)
		for _, t := range b.targets {
			if b.tracker.HasAlert(t.ChatID, t.ThreadID, pm.StableID) {
				continue
			}
			if sent, err := b.sendAndReturn(t, outageText); err == nil && sent != nil && sent.MessageID != 0 {
				b.mu.Lock()
				stillDown := !b.lastSeen[pm.StableID]
				b.mu.Unlock()
				if !stillDown {
					if b.api != nil {
						_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
							ChatID:    tu.ID(t.ChatID),
							MessageID: sent.MessageID,
						})
					}
					continue
				}
				b.tracker.Track(t.ChatID, t.ThreadID, sent.MessageID, pm.StableID, pm.Name, now, "Offline")
			}
		}
	}

	// 3. Process recoveries
	for _, rec := range recoveries {
		if rec.alertMode == AlertModeClean {
			if rec.hadAlert && b.api != nil {
				_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
					ChatID:    tu.ID(rec.target.ChatID),
					MessageID: rec.alert.MessageID,
				})
			}
			if b.notifyOnRecovery && rec.hadAlert {
				recoveryText := fmt.Sprintf("✅ <b>%s</b> восстановлен — %.0f ms", escapeHTML(rec.pm.Name), rec.pm.LatencyMs)
				if rec.downtime > 0 {
					recoveryText += fmt.Sprintf(" (простой: %s)", FormatDowntime(rec.downtime))
				}
				recoveryText += nodeLineFor(rec.pm)
				if sent, err := b.sendAndReturn(rec.target, recoveryText); err == nil && sent != nil && sent.MessageID != 0 {
					cID := rec.target.ChatID
					mID := sent.MessageID
					time.AfterFunc(2*time.Minute, func() {
						if b.api != nil {
							_ = b.api.DeleteMessage(b.ctx, &telego.DeleteMessageParams{
								ChatID:    tu.ID(cID),
								MessageID: mID,
							})
						}
					})
				}
			}
		} else { // AlertModeLive
			if rec.hadAlert && b.api != nil {
				liveText := fmt.Sprintf("✅ <b>%s</b> восстановлен — %.0f ms (простой: %s)%s",
					escapeHTML(rec.pm.Name), rec.pm.LatencyMs, FormatDowntime(rec.downtime), nodeLineFor(rec.pm))
				params := &telego.EditMessageTextParams{
					ChatID:    tu.ID(rec.target.ChatID),
					MessageID: rec.alert.MessageID,
					Text:      liveText,
					ParseMode: telego.ModeHTML,
				}
				_, _ = b.api.EditMessageText(b.ctx, params)
			} else if b.notifyOnRecovery {
				recoveryText := fmt.Sprintf("✅ <b>%s</b> восстановлен — %.0f ms%s", escapeHTML(rec.pm.Name), rec.pm.LatencyMs, nodeLineFor(rec.pm))
				b.send(rec.target, recoveryText)
			}
		}
	}
}

// nodeLineFor renders the "via node" line for remote-proxy alerts; empty
// for locally checked proxies.
func nodeLineFor(pm metrics.ProxyMetric) string {
	if pm.NodeName == "" {
		return ""
	}
	s := "\n📍 via " + escapeHTML(pm.NodeName)
	if pm.NodeASN != "" {
		s += " · " + escapeHTML(pm.NodeASN)
	}
	return s
}
