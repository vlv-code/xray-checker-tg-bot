# Active Tasks and Delegation Registry

## Active Objective
Meta-project bootstrap completed 2026-11-16. Next: materialize the agreed agent team via `manage-meta-project`.

## Team Checklist (agreed scenario: triad + dispatcher)
- [x] Scenario agreement (profile=active_development, deployment=shared_repo, collaboration=triad+dispatcher, verification=duo_peer_review, delegation=controlled) [user @ 2026-11-16]
- [x] Knowledge slices initialized from deep code inspection [zcode::zc-main::init @ 2026-11-16]
- [x] Deploy agent roles via `manage-meta-project`: `agent-intake-dispatcher` (user-approved "диспетчер", canonical catalog id), `agent-dev-lead`, `agent-quality-guardian`, `agent-github-steward` — registry bindings + profiles + system prompts + AGENTS.md §7 roster [zcode::zc-main::agent-intake-dispatcher @ 2026-11-16]
- [x] Verify role identities: `python scripts/verify_identity.py --check` — passed, 0 errors [zcode::zc-main::agent-dev-lead @ 2026-11-16]
- [x] Commit meta-project scaffold to Git (shared_repo mode) — user ratified "отдельным chore(meta)"; committed in d983066 with lock-file hygiene fix (.agents/**/*.lock ignored) [zcode::zc-main::agent-github-steward @ 2026-11-16]

## Delegation Registry
| Task | Actor | Status | Date |
|---|---|---|---|
| Meta-project init (survey, slices, passport, registry, init report) | zcode::zc-main::human-operator (bootstrap actor; roles not yet materialized) | completed | 2026-11-16 |
| Team roles & bindings configuration | manage-meta-project (future) | pending | — |
| Deep repo audit (5 read-only explore workers + dev-lead verification) | zcode::zc-main::agent-dev-lead | completed | 2026-11-16 |
| Fix wave: 6 P0 + 6 P1 + 10 P2/P3 via 8 flash-workers (disjoint scopes, TDD) | zcode::zc-main::agent-dev-lead | completed | 2026-11-16 |
| P2/P3 tail: 6 parallel flash-workers (429, EventBuffer, escaping, tests, openapi, READMEs) | zcode::zc-main::agent-dev-lead | completed | 2026-11-16 |
| Guardian peer-review of fix wave (duo_peer_review) | agent-quality-guardian | completed — verdict FAIL (conditional): all claimed items PASS; 2×P0 + 1×P1 new regressions found (fetcher.go:135 nil-deref, main.go:884-890 shutdown deadlock, last-good cache aliasing) — routed back to dev-lead | 2026-11-16 |
| Fix 2×P0 + 1×P1 found by Guardian (fetcher err-guard, scheduler Stop deadlock, cache deep-copy) | agent-dev-lead | completed 2026-11-16 — fixed directly by dev-lead (no delegation): P0-1 err-guard restored + call-site RED test (nil-deref panic reproduced, then clean error); P0-2 currentCheckInterval RuntimeMu-only + deadlock contract pin (checkSchedulerMu hoisted package-level); P1 cloneProxyConfig deep-copy (6 ref fields) on cache store+merge; gates green 12 pkgs + gofmt — awaiting Guardian re-gate on the three points | 2026-11-16 |
| Guardian re-gate on 2×P0 + 1×P1 fixes (scoped re-review) | agent-quality-guardian | completed — verdict **PASS** on all three points (details: P0-1 guard position + single production call-site + redaction chain + real-call-path test; P0-2 job-path mutex-free confirmed incl. report-goroutine analysis, update-defer safe, pin catches regression; P1 clone completeness 6/6 ref fields vs struct dump, both boundaries, mutation asserts real). Gates reproduced green. Awaiting CI `go test -race` before merge | 2026-11-16 |
| Commit + release of fix waves 1-4 + blocker fixes (see Steward bridge below) | agent-github-steward | committed + pushed 2026-11-16: 7 commits 899c45c..d983066 (security/nodes/concurrency/telegram/subscription/docs/meta; Red Zone ratification obtained for all 10 files; v2.6.0 version refs bumped); **CI `-race` gate SUCCESS** (test.yml run 43830601720, Linux); draft release v2.6.0 prepared — publish requires explicit user approval (Red Zone) | 2026-11-16 |

## Steward Bridge (handoff from Guardian, 2026-11-16)
Scope ready for commit: fix waves 1-4 (baseline a191063, ~2.6k LOC) + 2×P0/P1 blocker fixes (re-gate PASS).
- **CI gate mandatory before merge:** `go test -race -timeout 5m ./...` on Linux CI — local host cannot run `-race` (observations.md:11); race-regression pins (checker/races_regression_test.go, main_lifecycle_test.go) are enforced ONLY by CI.
- **Red Zone ratification (user required at commit):** 8+ files — `.env.example`, `nodes/nodes.go`, `nodes/nodes_store.go`, `nodes/subs_store.go`, `subscription/fetcher.go`, `subscription/redact_test.go`, `telegram/bot.go`, `web/openapi.yaml` (+ now `subscription/subscription.go`, `main.go` re-touched by blocker fixes).
- **Suggested commit split (Steward judgment):** (1) security/races block (fetcher SSRF+redaction+guard, registry tokens, RuntimeMu/lifecycle, runner.mu), (2) telegram block (admin gate, 429, EventBuffer, escaping, alerts), (3) tests+docs block (new tests, openapi.yaml, README/README_RU, .env.example); meta-project scaffold (`.agents/`, `AGENTS.md`, `scripts/`) — separate commit or same, per user's pending decision (checklist line 11).
- **Do NOT include:** nothing to exclude; scratch artifacts removed by Guardian (verified in working tree).
- **Post-release:** P3 backlog (11 items) lives in weak_spots.md §Guardian — not release-blocking.
