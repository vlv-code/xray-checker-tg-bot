# Nodes Deep Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable checker nodes to run multi-stage diagnostics (DNS, TCP/UDP port ping, TLS handshake, Cloudflare/Google 204 HTTP targets, verdict, Check-Host) and push them to the master so the Telegram bot displays the exact same rich diagnostics breakdown for remote nodes as it does for the master.

**Architecture:** 
1. Extend `nodes.ReportProxy` wire schema with optional `NodeHealth`, `Targets`, `Verdict`, and `CheckHost`.
2. Add `nodes.BuildReportFromDiag` to construct payloads from `[]checker.ProxyDiagReport`.
3. In `nodes.Registry`, store and expose `NodeDiagReports(name string) []checker.ProxyDiagReport`.
4. In `telegram.NodeManager` and `telegram.Bot.getNodeDiagnosticsReports`, return `NodeDiagReports` for remote nodes.
5. In `main.go` on nodes, execute `proxyChecker.RunDiagnostics(targets)` during check cycles and send the rich diagnostic report.

**Tech Stack:** Go 1.26, telego (Telegram bot API), goroutines, JSON wire protocol.

**Spec:** `docs/superpowers/specs/2026-09-19-nodes-deep-diagnostics-design.md`

## Global Constraints
- 100% backwards compatibility: existing nodes sending legacy payloads without `NodeHealth`/`Targets` must still be parsed without error.
- Non-blocking push: diagnostic report transmission from node to master must remain asynchronous.
- All 12 Go packages must pass `go test ./... -count=1`.
- Zero compiler warnings or `go vet` errors.

---

### Task 1: Wire Protocol Schema & Diagnostics Serialization

**Files:**
- Modify: `nodes/report.go`
- Test: `nodes/report_test.go`

**Interfaces:**
- Produces: 
  - `ReportProxy` fields: `NodeHealth *checker.NodeHealth`, `Targets []checker.TargetDiagResult`, `Verdict string`, `CheckHost *checker.CheckHostSummary`
  - `nodes.BuildReportFromDiag(reports []checker.ProxyDiagReport, version string, intervalSec int, checkMethod, hostIP string) ReportPayload`

- [ ] **Step 1: Write failing test in `nodes/report_test.go`**
Add `TestBuildReportFromDiag` verifying that `BuildReportFromDiag` correctly populates `ReportProxy` with `NodeHealth`, `Targets`, and `Verdict`, and that JSON roundtrip preserves these fields while remaining backwards compatible with legacy payloads.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test -v ./nodes -run TestBuildReportFromDiag`
Expected: FAIL (compilation error: `BuildReportFromDiag` undefined, fields missing on `ReportProxy`)

- [ ] **Step 3: Implement `ReportProxy` fields and `BuildReportFromDiag` in `nodes/report.go`**
Add fields to `ReportProxy`:
```go
NodeHealth *checker.NodeHealth        `json:"nodeHealth,omitempty"`
Targets    []checker.TargetDiagResult `json:"targets,omitempty"`
Verdict    string                     `json:"verdict,omitempty"`
CheckHost  *checker.CheckHostSummary  `json:"checkHost,omitempty"`
```
Implement `BuildReportFromDiag(reports []checker.ProxyDiagReport, version string, intervalSec int, checkMethod, hostIP string) ReportPayload`.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test -v ./nodes -run TestBuildReportFromDiag`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add nodes/report.go nodes/report_test.go
git commit -m "feat(nodes): add diagnostic fields to ReportProxy wire protocol"
```

---

### Task 2: Registry Diagnostics Storage & Ingest

**Files:**
- Modify: `nodes/registry.go`
- Test: `nodes/registry_test.go`

**Interfaces:**
- Consumes: `ReportProxy.NodeHealth`, `ReportProxy.Targets`, `ReportProxy.Verdict`, `ReportProxy.CheckHost`
- Produces: `func (r *Registry) NodeDiagReports(name string) []checker.ProxyDiagReport`

- [ ] **Step 1: Write failing test in `nodes/registry_test.go`**
Add `TestRegistry_NodeDiagReports` checking that when a report with rich diagnostic proxies is ingested via HTTP `HandleReport`, `reg.NodeDiagReports(name)` returns the deserialized `[]checker.ProxyDiagReport` with matching `NodeHealth`, `Targets`, and `Verdict`.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test -v ./nodes -run TestRegistry_NodeDiagReports`
Expected: FAIL (`NodeDiagReports` undefined on `*Registry`)

- [ ] **Step 3: Implement `diagReports` and `NodeDiagReports` in `nodes/registry.go`**
Add `diagReports []checker.ProxyDiagReport` to `nodeState`.
In `HandleReport`, convert `payload.Proxies` to `[]checker.ProxyDiagReport` if rich fields are present, and store in `st.diagReports`.
Implement:
```go
func (r *Registry) NodeDiagReports(name string) []checker.ProxyDiagReport
```
with read lock and slice copy.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test -v ./nodes -run TestRegistry_NodeDiagReports`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add nodes/registry.go nodes/registry_test.go
git commit -m "feat(nodes): store and retrieve full node diagnostic reports in registry"
```

---

### Task 3: Telegram Diagnostics & Node Manager Integration

**Files:**
- Modify: `telegram/nodes.go`
- Modify: `node_manager.go`
- Modify: `telegram/diagnostics.go`
- Modify: `telegram/views_node_status_test.go`
- Modify: `telegram/nodes_commands_test.go`
- Modify: `telegram/diagnostics_node_test.go`

**Interfaces:**
- Consumes: `Registry.NodeDiagReports(name string) []checker.ProxyDiagReport`
- Produces: `NodeManager.NodeDiagReports(node string) []checker.ProxyDiagReport`
- Updates: `Bot.getNodeDiagnosticsReports(nodeName string) []checker.ProxyDiagReport`

- [ ] **Step 1: Write failing test in `telegram/diagnostics_node_test.go`**
Add `TestGetNodeDiagnosticsReports_FullDiagnostics` verifying that `getNodeDiagnosticsReports("m31a")` retrieves the multi-target and `NodeHealth` report from `nodeMgr.NodeDiagReports("m31a")`, and that `buildDiagnosticsDetailsRichMessageWithTarget` formats DNS, TCP, TLS, and multiple targets for that node.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test -v ./telegram -run TestGetNodeDiagnosticsReports_FullDiagnostics`
Expected: FAIL (`NodeDiagReports` not in `NodeManager` interface)

- [ ] **Step 3: Implement `NodeDiagReports` across `NodeManager`, `node_manager.go`, and `telegram/diagnostics.go`**
1. Add `NodeDiagReports(node string) []checker.ProxyDiagReport` to `NodeManager` in `telegram/nodes.go`.
2. Implement it on `nodeManagerAdapter` in `node_manager.go`.
3. In `telegram/diagnostics.go`:
```go
if b.nodeMgr != nil {
    if diag := b.nodeMgr.NodeDiagReports(nodeName); len(diag) > 0 {
        b.sortDiagnosticsReports(diag)
        return diag
    }
}
```
4. Update mocks in test files.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test -v ./telegram -run TestGetNodeDiagnosticsReports_FullDiagnostics`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add telegram/nodes.go node_manager.go telegram/diagnostics.go telegram/*test.go
git commit -m "feat(telegram): render rich multi-stage diagnostics for remote nodes"
```

---

### Task 4: Node Check Pipeline Integration

**Files:**
- Modify: `main.go`
- Modify: `node_manager_test.go`

**Interfaces:**
- Consumes: `proxyChecker.RunDiagnostics(targets)`, `nodes.BuildReportFromDiag`
- Produces: Rich diagnostic payload sent by node reporter

- [ ] **Step 1: Write test in `node_manager_test.go`**
Verify that `nodeManagerAdapter.NodeDiagReports` delegates properly to `nodes.Registry.NodeDiagReports`.

- [ ] **Step 2: Run test to verify it passes**
Run: `go test -v -run TestNodeManagerAdapter_NodeDiagReports`

- [ ] **Step 3: Update node check loop in `main.go`**
In `main.go`, when `reporter != nil`:
Determine targets:
```go
targets := []string{
    "https://cp.cloudflare.com/generate_204",
    "https://www.gstatic.com/generate_204",
}
if tm := proxyChecker.GetTargetManager(); tm != nil && len(tm.GetTargets()) > 0 {
    targets = tm.GetTargets()
}
diagReports := proxyChecker.RunDiagnostics(targets)
payload := nodes.BuildReportFromDiag(diagReports, version, interval, config.CLIConfig.Proxy.CheckMethod, hostIP)
```

- [ ] **Step 4: Run all tests**
Run: `go test ./... -count=1`
Expected: All 12 packages PASS.
Run: `go fmt ./...; go vet ./...`
Expected: Clean output.

- [ ] **Step 5: Commit**
```bash
git add main.go node_manager_test.go
git commit -m "feat(nodes): run multi-stage diagnostics during node check cycles"
```

---

### Task 5: Deployment & Live Verification

**Files:**
- Git repository `main` branch
- Deployment target: `bedolaga` container `xray_checker_bot`

- [ ] **Step 1: Push commits to GitHub `origin/main`**
Run: `git push origin main`

- [ ] **Step 2: Deploy to server `bedolaga`**
Run:
```bash
ssh root@89.125.214.220 "cd /opt/remnawave-bedolaga-telegram-bot && docker compose build --no-cache xray-checker && docker compose up -d xray-checker"
```

- [ ] **Step 3: Verify container logs on `bedolaga`**
Run: `docker logs --tail 30 xray_checker_bot`
Verify: reports accepted from node `m31a` with rich diagnostic payloads.
