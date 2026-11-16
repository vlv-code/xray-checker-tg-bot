# Workspace and Private Notes: GitHub Steward & Release Captain (`vlv-code/xray-checker-tg-bot::zcode::zc-main::agent-github-steward`)

This document is dedicated exclusively to the in-depth working context, drafts, and role-specific specifications of `agent-github-steward` (Actor: `vlv-code/xray-checker-tg-bot::zcode::zc-main::agent-github-steward`).
No other agent is permitted to modify the contents of this file.

---

## 1. Role-Specific Invariants and Standards
- Commit style (matches repo history): `feat(scope): ...`, `fix(scope): ...`, `docs: ...` — no merge-commits into main without review.
- Release flow: version references live in docs/READMEs (`v2.5.0` currently); bump all consistently.
- CI enforcement invariant: `go vet` + `go test -race -timeout 5m ./...` in `test.yml` must never be weakened.

## 2. Working Drafts and Unfinished Research
- (empty)

## 3. Registry of External Links and Documentation
- Workflows: `.github/workflows/` (test, build-publish, docker-ghcr, release, dockerhub-description)
- Release tooling: `.goreleaser.yaml`
