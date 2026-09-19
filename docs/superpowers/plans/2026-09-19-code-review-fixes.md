# Code Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix confirmed logic bugs, security risks, and concurrency flaws identified during code review while maintaining full test suite integrity.

**Architecture:** Address each issue with minimal, targeted changes following strict TDD. Separate transport security from VMess ciphers in models, correct double-checked locking in host IP resolution, ensure deterministic StableID collision resolution, resolve quiet hours alert tracking, enforce HTTP/HTTPS schemes in Telegram subscription commands, change sensitive store file permissions to 0600, decouple `HandleReport` from alerting I/O, and protect node diagnostics from concurrent Xray reloads.

**Tech Stack:** Go 1.22+, telego (Telegram Bot API), xray-core, prometheus/client_golang.

---

### Task 1: Fix Quiet Hours Alert Tracker Leak & Outage Recovery

**Files:**
- Modify: `telegram/alerts.go:150-174`
- Test: `telegram/alerts_test.go`

- [ ] **Step 1: Write failing test in `telegram/alerts_test.go`**
- [ ] **Step 2: Run test to verify it fails**
- [ ] **Step 3: Implement minimal fix in `telegram/alerts.go`**
- [ ] **Step 4: Run test to verify it passes**

---

### Task 2: Fix Host-IP Cache TTL in Double-Checked Locking

**Files:**
- Modify: `checker/checker.go:161-166`
- Test: `checker/checker_test.go`

- [ ] **Step 1: Write failing test in `checker/checker_test.go`**
- [ ] **Step 2: Run test to verify it fails**
- [ ] **Step 3: Implement fix in `checker/checker.go`**
- [ ] **Step 4: Run test to verify it passes**

---

### Task 3: Fix AssignStableIDs Collision Resolution & Dead Code

**Files:**
- Modify: `models/proxy_config.go:197-210`
- Test: `models/proxy_config_test.go`

- [ ] **Step 1: Write failing test in `models/proxy_config_test.go`**
- [ ] **Step 2: Run test to verify it fails**
- [ ] **Step 3: Implement fix in `models/proxy_config.go`**
- [ ] **Step 4: Run test to verify it passes**

---

### Task 4: Separate VMess Cipher from Transport Security

**Files:**
- Modify: `models/proxy_config.go`, `subscription/json_convert.go`, `xray/config.go`
- Test: `subscription/json_convert_test.go`, `xray/config_test.go`

- [ ] **Step 1: Write failing test in `subscription/json_convert_test.go`**
- [ ] **Step 2: Run test to verify it fails**
- [ ] **Step 3: Implement fix in `models/proxy_config.go`, `subscription/json_convert.go`, `xray/config.go`**
- [ ] **Step 4: Run test to verify it passes**

---

### Task 5: Fix HTML Escaping in `/togglenode`

**Files:**
- Modify: `telegram/commands.go:216`
- Test: `telegram/commands_test.go`

- [ ] **Step 1: Write failing test in `telegram/commands_test.go`**
- [ ] **Step 2: Run test to verify it fails**
- [ ] **Step 3: Implement fix in `telegram/commands.go`**
- [ ] **Step 4: Run test to verify it passes**

---

### Task 6: Restrict `/addsub` and `/nodeaddsub` to HTTP/HTTPS

**Files:**
- Modify: `telegram/subscriptions.go`, `telegram/nodes_commands.go`
- Test: `telegram/subscriptions_test.go`

- [ ] **Step 1: Write failing test in `telegram/subscriptions_test.go`**
- [ ] **Step 2: Run test to verify it fails**
- [ ] **Step 3: Implement fix**
- [ ] **Step 4: Run test to verify it passes**

---

### Task 7: Set File Permissions to 0600 on Token and Config Stores

**Files:**
- Modify: `nodes/nodes_store.go:83`, `telegram/config.go:272`
- Test: `nodes/nodes_store_test.go`

- [ ] **Step 1: Implement fix in `nodes/nodes_store.go` and `telegram/config.go`**
- [ ] **Step 2: Verify tests**

---

### Task 8: Decouple `onUpdate` in `HandleReport` to Prevent Node Timeout

**Files:**
- Modify: `nodes/registry.go:238`
- Test: `nodes/registry_test.go`

- [ ] **Step 1: Write failing test in `nodes/registry_test.go`**
- [ ] **Step 2: Run test to verify it fails**
- [ ] **Step 3: Implement fix in `nodes/registry.go`**
- [ ] **Step 4: Run test to verify it passes**

---

### Task 9: Remove I/O Under Locks in `telegram/bot.go`

**Files:**
- Modify: `telegram/bot.go`
- Test: `telegram/bot_test.go`

- [ ] **Step 1: Refactor `sendOrUpdateMenu` and `getMasterASN`**
- [ ] **Step 2: Run telegram tests to verify no regressions**

---

### Task 10: Protect Node `RunDiagnostics` from Concurrent Xray Reload

**Files:**
- Modify: `main.go:341`

- [ ] **Step 1: Wrap `proxyChecker.RunDiagnostics(targets)` with `checkRunnerMu` in `main.go`**
- [ ] **Step 2: Run full test suite and verify build**
