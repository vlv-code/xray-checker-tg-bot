# Technical Design Spec: Xray Checker Telegram Bot Enhancements

**Date**: 2026-09-11  
**Status**: Approved / In Progress  

## 1. Overview & Objectives

Enhance the Xray Checker Telegram Bot with comprehensive monitoring, diagnostics, and notification controls:
1. **Target Check URLs & Live Diagnostics**: Support multiple target endpoints (Cloudflare 204, Google 204, custom URLs) with quorum/fallback for background checks to eliminate false alarms (e.g. third-party `ipify.org` EOF drops), plus an instant "⚡ Check Now" diagnostic command showing exact per-target success/failure for each proxy.
2. **Persistent Outage Statistics**: Track uptime percentage (24h, 7d, all-time), total downtime duration, incident counts, and a sliding log of the last 50 outages with failure reasons (timeout, EOF, direct host IP leak). Stored in `/app/data/stats.json`.
3. **Smart Alert Lifecycle & Auto-Cleanup**: Configurable alert mode toggle:
   - `live`: Edits the original outage message upon recovery to show downtime duration (`✅ Restored (offline for 12m)`).
   - `clean`: Automatically deletes the outage alert upon recovery, and deletes the recovery confirmation after 2 minutes, keeping the chat clean.
4. **Quiet Hours & Scheduled Digests**:
   - Quiet hours (e.g. 23:00–08:00) during which audible alert pings are suppressed and buffered.
   - Morning digest sent at the end of quiet hours summarizing overnight events and current status.
   - Configurable daytime digests (e.g. every 6h).
   - Manual override buttons in the bot ("🌙 Mute for 1h / 4h / until morning").
5. **Unified Telegram Bot Menu**: Comprehensive interactive inline keyboard menu covering Status, Diagnostics, Stats, Subscriptions, Target URLs, Quiet Hours, and Alert Settings.

---

## 2. Architecture & Data Storage

### 2.1 File Storage (`/app/data/`)

1. **`/app/data/bot_config.json`**:
   ```json
   {
     "quiet_hours_enabled": true,
     "quiet_hours_start": "23:00",
     "quiet_hours_end": "08:00",
     "quiet_snooze_until": 0,
     "day_digest_enabled": true,
     "day_digest_interval_hours": 6,
     "alert_mode": "live",
     "target_urls": [
       "https://cp.cloudflare.com/generate_204",
       "https://www.gstatic.com/generate_204"
     ]
   }
   ```

2. **`/app/data/stats.json`**:
   ```json
   {
     "incidents": [
       {
         "proxy_name": "🇸🇪 SW-main",
         "stable_id": "vless-sw-main",
         "down_at": 1726050000,
         "up_at": 1726051200,
         "duration_sec": 1200,
         "reason": "Host IP leak (2.27.23.179)"
       }
     ],
     "history": {
       "vless-sw-main": {
         "total_checks": 1440,
         "successful_checks": 1380,
         "downtime_sec": 3600,
         "drop_count": 3
       }
     }
   }
   ```

---

## 3. Subsystem Specifications

### 3.1 Target Check Manager & Live Diagnostics
- **Default Targets**: `https://cp.cloudflare.com/generate_204`, `https://www.gstatic.com/generate_204`.
- **Background Checks**: Checks target endpoints; if at least one target succeeds without IP leak, proxy is marked online.
- **Diagnostics (`/diag` or "⚡ Check Now")**: Concurrently tests each proxy against every configured target endpoint and outputs a formatted breakdown showing latency or specific error per site.

### 3.2 Persistent Stats Manager
- Increments check counts on each cycle.
- Records transition to offline: records `down_at` and `reason`.
- Records transition to online: calculates `duration_sec`, records `up_at`, appends to `incidents` list (capped at 50 most recent).
- Computes 24h and 7d uptime percentages.

### 3.3 Alert Lifecycle & Auto-Cleanup
- **In-Memory Tracking**: Map of `stable_id -> { ChatID, MessageID, DownAt }`.
- **Live Mode**:
  - Outage: Sends `🔴 <b>[Proxy]</b> is down\nReason: ...` and records message ID.
  - Recovery: Calls `editMessageText` replacing the message with `✅ <b>[Proxy]</b> restored — [latency] ms (was down for [Xm Ys])`.
- **Clean Mode**:
  - Outage: Sends `🔴 ...` and records message ID.
  - Recovery: Calls `deleteMessage` on the outage message. Sends `✅ ... restored` and schedules a goroutine to delete it after 2 minutes.

### 3.4 Quiet Hours & Digest Scheduler
- Evaluates current time in local timezone (`time.Local` or `TZ`).
- If within quiet hours (`start <= now < end`) or `quiet_snooze_until > now`:
  - Suppresses outgoing instant alert messages.
  - Buffers outage/recovery events in memory.
- At quiet hours transition (e.g. 08:00):
  - Sends morning digest: summary of current online/offline count + list of incidents that happened during the night.
- Daytime digests:
  - Ticker fires every `day_digest_interval_hours` to post an aggregated health check report.

### 3.5 Unified Interactive Menu (`/menu`)
- Inline Keyboards:
  - `[ 📊 Status ]` `[ ⚡ Check Now ]`
  - `[ 📈 Statistics ]` `[ 📋 Subscriptions ]`
  - `[ 🎯 Target Sites ]` `[ 🌙 Quiet Hours ]`
  - `[ ⚙️ Alert Settings ]`
- Handlers for callback queries (`cb:status`, `cb:diag`, `cb:stats`, `cb:quiet:toggle`, `cb:mode:toggle`, etc.).

---

## 4. Verification Plan
- Unit tests for stats calculations and persistent storage serialization.
- Unit tests for quiet hours time parsing and boundary conditions.
- Unit tests for alert lifecycle (edit vs delete actions).
- Clean `go test ./...` and `go vet ./...` passes.
- Verification on test binary build.
