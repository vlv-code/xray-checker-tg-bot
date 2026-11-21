# Responsibility Zones and Change Boundaries

This document defines the agent's level of autonomy when modifying the repository using explicit file path glob patterns.
Calibrated for xray-checker-tg-bot (Go, Telegram bot + proxy checker) on 2026-11-16.

---

## Green Zone (Autonomous Modification)
Files and directories that the agent can modify autonomously to accomplish the current task:
- Creating new test files (`**/*_test.go`, root `*_test.go`) to cover new features within a TDD session.
- `.github/screen/**` — repository media assets (screenshots, branding images).
- `docs/**` content pages (`docs/src/**`, `docs/public/**`, `*.md` inside docs) — documentation site content (build config see Yellow).
- `*.md` repository-level documentation (except root `AGENTS.md`, `README.md`, `README_RU.md` — see Yellow due to bilingual sync invariant).
- In-code documentation (doc comments, `//`-annotations in Go sources).
- `.agents/skills/*/SKILL.md` — repo-local skill instructions (verbatim upstream copies; update via `update-meta-project` only).
- `.agents/project_knowledge/observations.md` — via `record-observation` skill only.

---

## Yellow Zone (Modification with Mandatory Verification)
Areas requiring elevated caution and mandatory local test and linter execution (`go build ./... && go vet ./... && go test -race ./...`) prior to delivery:
- `AGENTS.md` — root project passport (requires careful verification and opportunistic refresh).
- `README.md`, `README_RU.md` — **bilingual sync invariant:** any feature change must be reflected in BOTH files before delivery.
- Existing regression test suite (`**/*_test.go` and root-level `*_test.go` — `node_manager_test.go`, `resilient_startup_test.go`, `telegram_subscriptions_test.go`, `xray_retry_test.go` — except security/contract tests listed in Red). **Test emasculation protection invariant:** It is forbidden to weaken assertions, comment out checks, or adjust golden data to match broken code. A test failure indicates a bug in implementation, not a need to rewrite the test. Modifying existing checks is permitted only when user requirements change explicitly.
- `.gitignore`, `.gitattributes` — VCS policy files (changes affect what is committed; review required).
- `main.go`, `node_manager.go`, `telegram_subscriptions.go` — composition root and wiring (high coupling; any signature change ripples through all subsystems).
- `telegram/**` (except security parts below) — bot logic, menus, views, formatting. Telegram platform constraints apply: `callback_data` 64-byte limit, HTML parse mode correctness (a parse error breaks message delivery), msg-seq race protection.
- `checker/**`, `subscription/**` (except Red entries below) — check pipeline, parsing, reconciliation logic.
- `xray/**` — Xray config generation and runner (libxray API coupling).
- `nodes/**` (except Red entries below) — registry, auditor, reporter, node settings inheritance logic.
- `config/**` — CLI/env parsing (kong); every new flag must be documented in `.env.example` and both READMEs.
- `web/**` (except Red entries below) — dashboard handlers and templates.
- `metrics/**`, `models/**`, `asn/**`, `logger/**` — support packages.
- `go.mod`, `go.sum` — project dependencies (`go mod tidy` + full test run required).
- `.golangci.yml`, `Makefile` — build, linting, and quality automation configurations.
- `scripts/**` (except `scripts/check_boundaries.py`) — automation scripts (`xray-checker-node.service` etc.).
- `docs/package.json`, `docs/pnpm-lock.yaml`, `docs/package-lock.json`, `docs/astro.config.mjs`, `docs/tsconfig.json` — docs site build config.
- `.agents/*.json` — project registries and identity binding definitions.
- `.agents/team/**` — role passports, system prompts, and private notes (**notes.md sanctity:** each `.agents/team/<role_id>/notes.md` is modifiable exclusively by its owner role; roster/topology changes route via `manage-meta-project`).
- `.agents/reports/**` — initialization and team management audit reports.
- `.agents/project_knowledge/architecture.md`, `weak_spots.md`, `tasks.md` — knowledge slices (opportunistic refresh).
- `.github/*.yml` not under `workflows/` — repository-level GitHub configurations.

---

## Red Zone (Prohibited Without Direct User Confirmation)
Critical areas of the project. Any modification requires mandatory prior confirmation from the user.
User-confirmed critical areas (2026-11-16 interview): **state persistence, SSRF protection, token handling.**

### State, secrets and security core (user-confirmed):
- `nodes/nodes_store.go` — token hashing at rest (sha256) and node registration persistence.
- `nodes/subs_store.go`, `nodes/nodes.go` (store persistence parts) — dynamic state persistence.
- `subscription/fetcher.go` (`validateSubscriptionTarget` and `newSafeTransport`) — SSRF protection (no opt-out by design) and transport hardening.
- `subscription/ssrf_test.go`, `subscription/redact_test.go`, `web/security_test.go`, `web/auth_test.go` — security contract tests.
- `telegram/bot.go` (chat allowlist `allowedChatIDs` enforcement) — access control to the bot.
- `data/**`, `*.json` state stores (`subscriptions.json`, `proxy_cache.json`, nodes/stats/alert state), `*.db`, `*.sqlite*` — local state databases.
- `.env*`, `*.pem`, `*.key` — configurations containing secrets.

### Infrastructure and external contracts (template defaults, kept conservative):
- `Dockerfile`, `entrypoint.sh`, `docker-compose.example.yml`, `docker-compose.node.yml`, `.dockerignore`, `.goreleaser.yaml` — containerization and deployment infrastructure (privilege-drop scheme).
- `.github/workflows/**` (`test.yml`, `build-publish.yml`, `docker-ghcr.yml`, `release.yaml`, `dockerhub-description.yml`) — CI/CD pipelines.
- `web/openapi.yaml` — public web API contract.
- `geo/*.dat` — generated binary GeoIP/geosite databases (never edit by hand).
- `scripts/check_boundaries.py` — core security and boundary gate enforcement script.
- `*boundaries.md` — responsibility boundaries and security policy definitions.
- `.superpowers/**` — historical SDD session artifacts (audit trail).
- `LICENSE`, `SECURITY.md`, `CODE_OF_CONDUCT.md` — legal and governance documents.

---

## Default Deny (Black Zone / Unmapped Paths Invariant)
Any directory or file path not explicitly listed in Green, Yellow, or Red zones above is strictly classified as **Red Zone by Default**. Autonomous modifications to unmapped paths are prohibited. Prior to touching unmapped code, the agent MUST consult the user and perform an *Opportunistic Refresh* of `boundaries.md` to map the path to its appropriate zone.

### Top-level directory coverage map (Zero Unmapped Dirs):
| Directory | Zone | Rationale |
|---|---|---|
| `asn/`, `logger/`, `metrics/`, `models/` | Yellow | support packages, logic changes need verification |
| `checker/`, `subscription/`, `xray/`, `config/`, `web/` | Yellow | core logic (security parts → Red, see above) |
| `nodes/` | Yellow | registry/auditor/reporter (stores → Red) |
| `telegram/` | Yellow | bot subsystem (allowlist enforcement → Red) |
| `docs/` | Green content / Yellow build config | documentation site |
| `geo/` | Red (`*.dat` binaries) | generated GeoIP databases |
| `scripts/` | Yellow (`check_boundaries.py` → Red) | automation + meta-project runtime |
| `.github/` | Yellow repo-level / Red `workflows/**` | CI/CD |
| `.superpowers/` | Red | historical session artifacts |
| `.agents/` | Green skills / Yellow registries, team & reports / Red boundaries.md | meta-project structure |
