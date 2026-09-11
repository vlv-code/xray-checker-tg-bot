# Telegram Bot Enhancements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement multi-target checks with live diagnostics, persistent outage statistics, smart alert lifecycle with auto-cleanup, quiet hours with scheduled morning and day digests, and an interactive unified Telegram bot menu.

**Architecture:** Modular Go extensions within the `xray-checker` project: `telegram.ConfigStore` and `telegram.StatsStore` for persistent JSON storage in `/app/data/`, `checker.TargetManager` for multi-site checks and diagnostics, `telegram.AlertTracker` for live-editing and auto-cleanup of messages, `telegram.ScheduleManager` for quiet hours and digests, and `telegram.MenuRouter` for inline keyboard navigation.

**Tech Stack:** Go 1.26+, `github.com/kirugan/telegram-bot-api/v5`, standard library `net/http`, `sync`, `time`, `encoding/json`.

**Spec:** `docs/superpowers/specs/2026-09-11-telegram-bot-enhancements-design.md`

## Global Constraints
- Must maintain 100% backward compatibility with existing CLI flags, environment variables, and Prometheus metrics `/metrics`.
- Must work seamlessly in both web-enabled (`WEB_ENABLED=true`) and headless (`WEB_ENABLED=false`) modes.
- Persistent files must be saved under `/app/data/` (or current directory fallback if `/app/data/` is unavailable).
- All tests must pass with `go test ./...`.

---

### Task 1: Persistent Configuration and Outage Statistics Store

**Files:**
- Create: `telegram/config.go`
- Create: `telegram/config_test.go`
- Create: `telegram/stats.go`
- Create: `telegram/stats_test.go`

**Interfaces:**
- `BotConfig`: Holds quiet hours settings (`Enabled`, `Start`, `End`, `SnoozeUntil`), alert mode (`Live` vs `Clean`), daytime digest interval, and custom target URLs.
- `StatsStore`: `RecordCheck(stableID, name string, online bool, latencyMs float64, errReason string)`, `GetUptime24h(stableID string) float64`, `GetRecentIncidents(limit int) []Incident`, `Save() error`.

- [ ] **Step 1: Write tests for BotConfig and StatsStore**
- [ ] **Step 2: Run tests to verify they fail**
- [ ] **Step 3: Implement BotConfig and StatsStore**
- [ ] **Step 4: Run tests to verify they pass**
- [ ] **Step 5: Commit**

---

### Task 2: Multi-Target Check & Live Diagnostics

**Files:**
- Create: `checker/target.go`
- Create: `checker/target_test.go`
- Modify: `checker/checker.go`

**Interfaces:**
- `TargetManager`: Manages list of check URLs (Cloudflare 204, Google 204, custom URLs).
- `CheckTarget(client *http.Client, targetURL string) (latency time.Duration, err error)`
- `RunDiagnostics(proxies []*models.ProxyConfig, targets []string) map[string]ProxyDiagResult`

- [ ] **Step 1: Write tests for TargetManager and diagnostic execution**
- [ ] **Step 2: Run tests to verify they fail**
- [ ] **Step 3: Implement TargetManager and hook into ProxyChecker**
- [ ] **Step 4: Run tests to verify they pass**
- [ ] **Step 5: Commit**

---

### Task 3: Alert Lifecycle & Auto-Cleanup (Live vs Clean)

**Files:**
- Create: `telegram/alert_tracker.go`
- Create: `telegram/alert_tracker_test.go`
- Modify: `telegram/bot.go`

**Interfaces:**
- `AlertTracker`: Tracks active outage alerts (`stable_id -> { ChatID, MessageID, DownAt }`).
- `OnOutage(chatID int64, msgID int, stableID, proxyName string)`
- `OnRecovery(stableID string) (tracked *ActiveAlert, found bool)`
- Supports `Live` mode (replaces outage message with recovery message + duration) and `Clean` mode (deletes outage alert immediately, sends recovery, auto-deletes recovery after 2 minutes).

- [ ] **Step 1: Write tests for AlertTracker state management**
- [ ] **Step 2: Run tests to verify they fail**
- [ ] **Step 3: Implement AlertTracker and integrate into telegram.Bot**
- [ ] **Step 4: Run tests to verify they pass**
- [ ] **Step 5: Commit**

---

### Task 4: Quiet Hours & Scheduled Digest Engine

**Files:**
- Create: `telegram/scheduler.go`
- Create: `telegram/scheduler_test.go`
- Modify: `telegram/bot.go`

**Interfaces:**
- `Scheduler`: Evaluates whether current time is in quiet hours or snoozed. Buffers alerts during quiet hours.
- `SendMorningDigest()`: Triggered at the end of quiet hours with current summary and overnight events.
- `SendDaytimeDigest()`: Periodically triggered according to interval.

- [ ] **Step 1: Write tests for Scheduler quiet hours calculations and event buffering**
- [ ] **Step 2: Run tests to verify they fail**
- [ ] **Step 3: Implement Scheduler**
- [ ] **Step 4: Run tests to verify they pass**
- [ ] **Step 5: Commit**

---

### Task 5: Unified Telegram Bot Menu & Inline Callback Router

**Files:**
- Create: `telegram/menu.go`
- Create: `telegram/menu_test.go`
- Modify: `telegram/bot.go`

**Interfaces:**
- `MenuRouter`: Renders interactive inline keyboards for:
  - 📊 Status
  - ⚡ Check Now (Diagnostics)
  - 📈 Outage Statistics
  - 📋 Subscriptions
  - 🎯 Target Sites
  - 🌙 Quiet Hours
  - ⚙️ Alert Mode Toggle
- Handles `CallbackQuery` updates and edits message in place.

- [ ] **Step 1: Write tests for MenuRouter callback routing**
- [ ] **Step 2: Run tests to verify they fail**
- [ ] **Step 3: Implement MenuRouter and wire into telegram.Bot update loop**
- [ ] **Step 4: Run tests to verify they pass**
- [ ] **Step 5: Commit**

---

### Task 6: Configuration, CLI Flags, Documentation, and E2E Verification

**Files:**
- Modify: `config/config.go`
- Modify: `main.go`
- Modify: `.env.example`
- Modify: `README.md`
- Modify: `README_RU.md`

- [ ] **Step 1: Add new environment variables and CLI flags**
- [ ] **Step 2: Wire components in main.go**
- [ ] **Step 3: Update .env.example, README.md, and README_RU.md**
- [ ] **Step 4: Run full test suite and build binary verification**
- [ ] **Step 5: Commit and push**
