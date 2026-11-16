# Workspace and Private Notes: Quality & Meta-Architecture Guardian (`vlv-code/xray-checker-tg-bot::zcode::zc-main::agent-quality-guardian`)

This document is dedicated exclusively to the in-depth working context, drafts, and role-specific specifications of `agent-quality-guardian` (Actor: `vlv-code/xray-checker-tg-bot::zcode::zc-main::agent-quality-guardian`).
No other agent is permitted to modify the contents of this file.

---

## 1. Role-Specific Invariants and Standards
- Review verdict format: PASS / FAIL + per-finding severity + exact file:line + required remediation.
- Verification gate is non-negotiable: `go vet ./...` and `go test -race -timeout 5m ./...` must both run on the reviewed tree.
- North Star checklist on every diff: SSRF guard intact, token hashing intact, chat allowlist intact, atomic writes intact, README/README_RU parity.

## 2. Working Drafts and Unfinished Research
- (empty)

## 3. Registry of External Links and Documentation
- Boundary gate: `python scripts/check_boundaries.py --boundaries .agents/project_knowledge/boundaries.md --audit-tree`
- CI parity reference: `.github/workflows/test.yml`
