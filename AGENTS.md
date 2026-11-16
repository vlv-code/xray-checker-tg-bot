# Project Passport: xray-checker-tg-bot

## 1. Concept, Principles, and North Stars
- **Service Purpose:** Xray proxy monitor with a fully integrated Telegram bot — multi-stage connection diagnostics, Check-Host.net reachability audits (RU/World), outage alerts with anti-flapping, persistent uptime statistics, runtime subscription and per-node settings management; fork of kutovoys/xray-checker.
- **Technology Stack:** Go 1.26, xtls/libxray + xray-core, mymmrac/telego, go-co-op/gocron, alecthomas/kong, maxminddb-golang, Prometheus client_golang, Docker/Compose (multi-stage Alpine, UPX, su-exec), GitHub Actions.
- **Key Invariant:** State in `/app/data` survives container rebuilds (subscriptions, proxy cache, nodes, stats, alerts); all state writes are atomic (tmp+rename) and mutex-guarded.
- **Principles and Architectural Orientations (North Stars)** *(user-confirmed 2026-11-16)*:
  - **Security invariants without opt-out:** SSRF validation of subscription targets (`subscription/fetcher.go: validateSubscriptionTarget` — localhost/private/CGNAT/reserved ranges rejected, no bypass switch), node tokens hashed sha256 at rest (`nodes/nodes_store.go`), Telegram chat allowlist enforcement (`telegram/bot.go`). These guarantees must never be weakened.
  - **Graceful degradation over crash-loops:** unavailable geo files, unreachable subscription upstream (keep previous proxy set), or read-only volumes degrade with warnings — the instance keeps running.
  - **Persisted state + atomic writes:** runtime-added subscriptions, per-node settings, stats and alert state are persisted; schema changes require legacy-format migrations (see `nodes_store.go` migration pattern).
  - **RU texts + bilingual docs sync:** bot user-facing texts are Russian; `README.md` and `README_RU.md` must stay in sync with every feature change (also `.env.example` — 4-place sync with `config/config.go`).
  - **Circuit Breaker & Flow Tracing:** repeatedly failing edits targeting one defect, or new regressions appearing in adjacent code while fixing it, are a semantic signal to STOP editing and trace the full data path (origin ➔ transport ➔ persistence ➔ consumer; see architecture.md flows) before the next diff. A normal iterative TDD loop is NOT a violation.
  - **Default Deny Boundaries:** Any directory not explicitly mapped in boundaries is strictly Red Zone.
  - **Default Closed Tooling:** Core framework and agents operate in a zero-dependency closed loop by default. External tools and third-party dependencies are permitted ONLY when explicitly declared (opt-in) in project specification or requested by user. Liability for external tool failures rests on the project/operator.
  - **Adversarial Testing Invariant:** When applying TDD, write negative/adversarial tests (boundary conditions, malformed input, failure modes) and confirm RED before writing implementation code to transition to GREEN. CI runs `go test -race` — race-safety is part of every change.
  *(Any decisions contradicting these principles are prohibited)*

## 2. Repository Meta-Map
- Core Code: `main.go` (composition root), `node_manager.go`, packages `checker/`, `subscription/`, `telegram/`, `nodes/`, `xray/`, `config/`, `web/`, `metrics/`, `models/`, `asn/`, `logger/`
- Entry Points: `main.go` (master mode), `main.go --report.url` (node mode), `entrypoint.sh` (container)
- Configuration: `config/config.go` (kong CLI+env), `.env.example` (annotated reference), `docker-compose.example.yml`, `docker-compose.node.yml`
- Tests: co-located `*_test.go` per package (CI gate: `.github/workflows/test.yml` — `go vet` + `go test -race ./...`)
- State (runtime): JSON stores under `/app/data` (subscriptions.json, proxy_cache.json, node/stats/alert stores)

## 3. Directive Meta-Knowledge Protocol
Before taking any action, the agent MUST consult the corresponding knowledge slices.
**Opportunistic Knowledge Refresh:** when reading any slice, evaluate data completeness — fill in missing details, fix outdated information, and eliminate blind spots without destructive overwriting:

- **Responsibilities and Boundaries (`.agents/project_knowledge/boundaries.md`):**
  - *When to consult:* before modifying data schemas, module boundaries, dependencies, security, or configs.
  - *What to update:* supplement and refine glob path patterns to match the real repository structure.
- **Weak Spots and Risks (`.agents/project_knowledge/weak_spots.md`):**
  - *When to consult:* before modifying fragile services, external APIs, queues, or complex legacy code (start with `main.go`, `telegram/views.go`, migration code in `nodes_store.go`).
  - *What to update:* record newly identified risks, hidden couplings, and remove resolved issues.
- **Architectural Foundations (`.agents/project_knowledge/architecture.md`):**
  - *When to consult:* before adding new components, changing data flows, or modifying interaction contracts.
  - *What to update:* eliminate blind spots in the subsystem map, describe actual connections and data flows (directly or via the `sync-architecture` skill).
- **Observation Base (`.agents/project_knowledge/observations.md`):**
  - *When to consult:* upon build failures, runtime errors, or unexpected environment behavior.
  - *What to update:* immediately and atomically record solved non-trivial bugs using the `record-observation` skill.
- **Active Tasks (`.agents/project_knowledge/tasks.md`):**
  - *When to consult:* at the beginning of any session and upon completing subtasks.
  - *What to update:* track execution progress, register new tasks and delegated steps with author and timestamp `[<actor_id> @ ISO_DATETIME]`.

## 4. Procedural Skills Registry
### Built-in meta-project skills:
- `init-meta-project` — repository scaffolding and non-destructive initialization.
- `manage-meta-project` — scale-adaptive agent team orchestration and materialization.
- `decommission-meta-project` — non-destructive decommissioning, resizing, and archiving of agent roles.
- `ground-meta-project` — context recovery and drift diagnosis: restores goal, state, identity, and authority after session changes, moves, updates, or compaction; diagnoses context drift without automatic rollback.
- `scan-meta-project` — integrity inspection, boundary coverage audit, and backfilling meta-knowledge without re-init.
- `tune-meta-project` — git friction diagnostics, role topology tuning, and non-bloating North Star crystallization.
- `update-meta-project` — non-destructive upstream updating from GitHub or local cache with local_repo_data preservation.
- `record-observation` — atomic logging of findings and quirks into `observations.md`.
- `sync-architecture` — updating the component map and data flows in `architecture.md`.

### Skill Responsibility Boundaries (invariant):
| Skill | Owns | Must NOT do |
|---|---|---|
| init | survey, scenario agreement, scaffold prep | deploy teams |
| manage | roles & bindings configuration | re-agree the scenario |
| update | framework components & migrations | revise team composition or North Stars |
| ground | recover understanding of goal/state/authority | change configuration to silence a discrepancy |
| tune | propose config improvements with evidence | apply them directly (routes via manage/update/decommission) |

### Connected stack skills from environment:
- **Go core:** `go-style-core`, `go-code-review`, `go-concurrency` (msg-seq, mutex-guarded stores), `go-error-handling`, `go-testing` (table tests + `-race`), `go-performance`, `go-context`, `go-linting`
- **Domain:** `telegram-bot-builder` (telego patterns, callback_data limits), `remnawave-xray` (Xray/panel specifics), `security-review` (SSRF/token/allowlist audits)
- **Infra:** `docker-expert` (multi-stage, su-exec, compose), `github-actions-creator` (CI/CD), `kubernetes-architect` (if k8s deployment appears)
- **Quality:** `clean-code`, `code-reviewer`, `incident-responder`
*(Managing build, linting, testing, and framework specifics is delegated to these skills)*

## 5. Local Verification Tooling & Autonomous Fallback
Project-native checks (CI parity — run before every delivery):
```
go build ./...
go vet ./...
go test -race -timeout 5m ./...
```
If global agent skills (such as `go-*` or Meta-Project skills) are unavailable in the host harness, the agent operates in **Base Fallback Mode** using zero-dependency Python 3.10+ stdlib utilities:
- **Repository Inspection & Capability Audit:**
  `python scripts/inspect_meta_project.py --json --harness zcode --installation zc-main`
- **Role Identity & Binding Verification:**
  `python scripts/verify_identity.py --check`
- **Boundary Gate & Responsibility Audit:**
  `python scripts/check_boundaries.py --boundaries .agents/project_knowledge/boundaries.md --audit-tree`
- **Working Tree Diff Verification:**
  `python scripts/check_boundaries.py --boundaries .agents/project_knowledge/boundaries.md --diff HEAD --allow-red-zone`
- **Memory Compaction Check:**
  `python scripts/compact_memory.py --dry-run`
- **Atomic Observation Logging:**
  `python scripts/record_obs.py --category <cat> --path <path> --issue <text> --solution <text>`
- **Initialization & Management Report Audit:**
  `python scripts/manage_reports.py [init|manage|latest|verify]`

### Read-Only Exploration Mode
When tasked with code exploration, repository assessment, or architecture review without explicit instruction to implement changes:
- Do NOT generate temporary files, `.lock` files, or intermediate scratchpads.
- Do NOT add telemetry rows to role profiles or tasks to `tasks.md`.
- Use `python scripts/inspect_meta_project.py --read-only --json` for safe discovery.

## 6. Base Working Protocol
1. Verify against Section 1: ensure the planned solution does not contradict project principles and invariants (especially security invariants and bilingual sync).
2. Consult the target slice from Section 3; update the slice immediately upon discovering gaps or inaccuracies.
3. Apply stack skills from Section 4 (or local stdlib tools from Section 5 in Base Fallback Mode) to inspect code and run tests.
4. Changes must be minimally sufficient; refactoring of unrelated areas is prohibited; changes must be minimal in diff width, but maximal in root-cause depth.
5. When encountering hidden environment quirks, record the fact via `record-observation` (or `scripts/record_obs.py`).

## 7. Project Agent Team & Multi-Harness Identity (.agents/team/)
Project roles and multi-harness bindings are authoritative in `.agents/meta-project.json`.
- **Identity Determination Protocol:** Every agent operating in the repository must resolve its qualified `actor_id` (`<project_id>::<harness_id>::<installation_id>::<role_id>`) from `.agents/meta-project.json` using `scripts/verify_identity.py`.
- **Invariance of Identity:** Reading another role's passport (`profile.md`) or private workspace (`notes.md`) does NOT alter the active actor's identity. If identity cannot be verified, the agent must fail closed and report an unresolved identity.
- **Topology:** triad + dispatcher (agreed scenario; dispatcher = user-approved 4th role, materialized as canonical `agent-intake-dispatcher`). Handoff chain: `Dev ➔ Guardian ➔ Steward`, max 2 hops, user-mediated; single point of entry — dispatcher.

**Active Team Roster:**
- [Intake Dispatcher](.agents/team/agent-intake-dispatcher/profile.md) — request decomposition, role routing, handoff packets, Fast-Track for trivial tasks (invoked by: ambiguous/multi-domain requests, "разбери задачу", "route", "dispatch")
- [Lead Developer](.agents/team/agent-dev-lead/profile.md) — Go feature implementation, wiring, TDD across subsystems (invoked by: "реализуй", "добавь фичу", "bugfix", "refactor")
- [Quality & Meta-Architecture Guardian](.agents/team/agent-quality-guardian/profile.md) — peer review (`duo_peer_review`), race-test gates, boundary audits, North Star compliance (invoked by: "проверь", "ревью", "verify", "audit")
- [GitHub Steward & Release Captain](.agents/team/agent-github-steward/profile.md) — git hygiene, releases, CI/CD governance, Code-to-README parity (invoked by: "закоммить", "релиз", "CI", "issue")

## 8. Dual-Entry & Canonical Repository Badge
For shared discovery across human operators and external agent harnesses, `README.md` incorporates the canonical block:
```markdown
<!-- META-PROJECT:ENTRY:BEGIN -->
[![Built with Meta-Project](https://img.shields.io/badge/Built%20with-Meta--Project-blueviolet)](https://github.com/vlv-code/meta-project)

Agent instructions: [AGENTS.md](AGENTS.md) | Policy: [.agents/project_knowledge/boundaries.md](.agents/project_knowledge/boundaries.md)
*To inspect, configure, or adapt your environment, run `python scripts/inspect_meta_project.py` or invoke the `init-meta-project` skill.*
<!-- META-PROJECT:ENTRY:END -->
```
