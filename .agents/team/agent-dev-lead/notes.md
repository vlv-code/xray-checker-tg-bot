# Workspace and Private Notes: Lead Developer (`vlv-code/xray-checker-tg-bot::zcode::zc-main::agent-dev-lead`)

This document is dedicated exclusively to the in-depth working context, drafts, and role-specific specifications of `agent-dev-lead` (Actor: `vlv-code/xray-checker-tg-bot::zcode::zc-main::agent-dev-lead`).
No other agent is permitted to modify the contents of this file.

---

## 1. Role-Specific Invariants and Standards
- Delivery gate (before any handoff to guardian): `go build ./... && go vet ./... && go test -race -timeout 5m ./...` — zero failures.
- New config flags ⇒ 4-place sync: `config/config.go` ↔ `.env.example` ↔ `README.md` ↔ `README_RU.md`.
- Telegram callbacks: respect 64-byte `callback_data` limit; reuse msg-seq guards for async edits; all user-facing texts in Russian.

## 2. Working Drafts and Unfinished Research
- (empty)

## 3. Registry of External Links and Documentation
- telego docs: https://github.com/mymmrac/telego
- Project flows: `.agents/project_knowledge/architecture.md` (End-to-End Data Flows)
