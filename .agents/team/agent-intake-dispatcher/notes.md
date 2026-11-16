# Workspace and Private Notes: Intake Dispatcher (`vlv-code/xray-checker-tg-bot::zcode::zc-main::agent-intake-dispatcher`)

This document is dedicated exclusively to the in-depth working context, drafts, and role-specific specifications of `agent-intake-dispatcher` (Actor: `vlv-code/xray-checker-tg-bot::zcode::zc-main::agent-intake-dispatcher`).
No other agent is permitted to modify the contents of this file.

---

## 1. Role-Specific Invariants and Standards
- Routing table lives in `AGENTS.md` Section 7 and `.agents/meta-project.json`; never duplicate it here — reference it.
- Handoff packets must always carry: original goal, completed work, handoff boundary, ready-to-use prompt for the target role.

## 2. Working Drafts and Unfinished Research
- (empty)

## 3. Registry of External Links and Documentation
- Rules of Engagement: manage-meta-project SKILL.md Phase 7 (handoff chain limit = 2 hops).
