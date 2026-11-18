---
name: tune-meta-project
license: MIT
compatibility: "Requires Python 3.10+ and Git"
description: >-
  Evidence-based calibration of the installed agent environment: analyzes
  actual work evidence (tasks, telemetry, observations, reports, history),
  forms verifiable improvement proposals with explicit effect verification,
  and routes changes through manage / decommission / update mechanisms.
---

# Repository Agent Environment Tuner (Tune Meta-Project)

Turns project experience into VERIFIABLE improvements: fewer wasted actions,
less context loss, fewer unnecessary handoffs — while preserving verification
quality, the project's meta-vision, and agreed authorities.

## When to invoke skill

* The user requests: "tune-meta-project", "калибровка обвязки", "прокачай инстанс", "устрани трение", "оптимизируй команду", "recalibrate environment".
* Accumulated friction is SUSPECTED (wasted handoffs, repeated context loss) — as a question to investigate, not a verdict.

## Execution steps

### Phase 1: Configuration Survey (read-only analysis)
Establish the frame with canonical utilities — calling them directly, never
via scan's writing scenario:
- `python scripts/inspect_meta_project.py --json --harness <h> --installation <i>`
- `python scripts/verify_identity.py --check`
- `python scripts/check_boundaries.py --boundaries .agents/project_knowledge/boundaries.md --audit-tree`
- installed framework state: `.agents/framework-lock.json` when present;
  unfinished updates halt tuning of affected components.
Record: agreed scenario, active installations and capabilities, roles and
bindings, verification policy, calibration scope (whole project or one
installation). Calibrating ONE installation never changes shared rules of
another; common parts consider both.

### Phase 2: Evidence Analysis (not heuristics)
Use existing data: tasks.md, observations, reports, checks, change history,
telemetry WITH confirmed identity. Interpretation rules (mandatory):
- repeated edits of one file are a signal TO STUDY — normal TDD is not a verdict;
- thirty days without invocations does not prove a role useless;
- frequent gate blocks may be CORRECT protection;
- frequent ground calls may follow session churn;
- git authorship does not determine the project role;
- missing telemetry means insufficient data — state it, don't guess;
- cost evidence is mandatory for any economy claim: available metrics only
  (re-reads, tool calls, handoffs, rework count, wall time); no invented
  currency conversion; before/after comparison only on comparable tasks;
  a smaller file is not, by itself, proven launch economy.
Choose the analysis window per question (period / completed-task set /
installation); a fixed short commit window is only an optional overview.
Separate FACTS (e.g. "five handoffs between roles") from HYPOTHESES
(e.g. "the dispatcher allocates poorly" — needs verification).

### Phase 3: Verifiable Proposals
For each proposal record EXACTLY:
Observed problem / Evidence and data limits / Hypothesized cause / Proposed
change and scope / Expected effect / Verification method / Preserved
constraints / Rollback method / Executor
(own the fact–hypothesis split). Select a SMALL set with the highest expected
value. "No justified proposals" is a normal result — say so explicitly.

### Phase 4: Instruction & Context-Cost Calibration
Check: duplicate rules across passport / skills / system prompts; scenario↔
instruction contradictions; procedures loaded constantly but rarely needed;
rules without observable behavioral impact; stale temporary workarounds;
excessive mandatory reporting/confirmations. The North Stars limit applies
to the TOTAL active set — propose consolidation (tactical details move to
skills/knowledge); never delete accepted restrictions to hit a number; a new
rule must state its application situation and expected behavior change.

### Phase 5: Application via Mechanisms (after user confirmation)
roles → manage-meta-project; binding disable / archive → decommission;
framework → update-meta-project; local instructions → `local_repo_data`
mechanism; boundaries → existing permission policy (never weaken protection
to bypass a defect — a framework defect is recorded and fixed, not routed
around). Do not re-confirm already-granted authorities; before requesting a
NEW permission present a concrete diff and available checks. A rarely-used
role is judged by purpose (independent verification may stay mandatory).

### Phase 6: Effect Verification Record
Persist per applied change: original observation / expected result /
re-evaluation condition / actual result / significant condition changes.
Statuses: applied-not-verified (applied, not verified) | confirmed | no-effect | regression.
Each applied change also records a measured cost block:
`"cost": {"rereads": <int>, "tool_calls": <int>, "handoffs": <int>, "rework": <int>, "wall_time_s": <int>}`
— observed counters only; state explicitly when a metric was unavailable
instead of inventing a value.
If framework version, harness, and task type all changed, do not attribute
the result to one setting without grounds. Re-evaluate on comparable tasks
or the next tune run; no background observation needed. Rollback restores
only the corresponding change, preserving subsequent user work.

### Phase 7: Repeatability
Same inputs → no new rules for the sake of running; no repeating rejected
proposals without new grounds; no duplicate observations; no team rebuild;
no files written in analysis mode. Store accepted AND rejected proposals
with rationale and scope (local knowledge slice, via the tuning report and
`local_repo_data`). Local calibration is an adaptation of the installed
instance — it never bumps the upstream framework version and MUST be
preserved by update-meta-project.
