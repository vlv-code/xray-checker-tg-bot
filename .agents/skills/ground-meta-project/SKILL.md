---
name: ground-meta-project
description: >-
  Context recovery and drift diagnosis skill: restores goal, state, identity
  and authority after session changes, moves, updates or compaction; diagnoses
  context drift (unconfirmed hypotheses, contradictions, authority overreach)
  without automatic rollback.
---

# Agent Grounding: Context Recovery & Drift Diagnosis

## When to invoke skill

Two modes:
- **Recovery:** new session, repository moved, task handoff, after `update-meta-project`, after context compaction.
- **Drift diagnosis:** repeated unconfirmed hypotheses, self-contradictions, actions beyond authority, success criteria adjusted to pass checks.

Semantic triggers only (a normal iterative TDD loop is NOT a violation):
repeatedly failing edits on one defect, new adjacent regressions, or the
user explicitly calling for a zoom-out.

## Execution steps

### Phase 0: Mode & Stop
1. Determine the mode from the invocation context.
2. Drift mode: FIRST stop all modifications, snapshot `git status -s` /
   `git diff --stat`, and only then inspect. NEVER roll back automatically;
   unknown authorship of changes is reported, not reverted.

### Phase 1: Identity (assignment + validation)
1. The assigned role comes from the session assignment or direct user
   instruction — this skill never reassigns it.
2. Validate it mechanically:
   `python scripts/verify_identity.py --actor <actor_id>`
   Admission requires an ACTIVE binding on an ACTIVE installation; an
   unresolvable identity is reported as unknown, never guessed from
   directories or foreign passports.

### Phase 2: Selective Context Recovery (read-only)
Read ONLY what the situation needs, by semantic section reference:
- passport: North Stars + Local Verification Tooling;
- the agreed scenario from `.agents/meta-project.json`;
- the live task: the CURRENT user request first; `tasks.md` second — a fresh
  request wins over a stale entry, but never silently cancels previously
  agreed restrictions; contradictions are surfaced explicitly;
- relevant fragments of `architecture.md` / `boundaries.md`;
- the assigned role's OWN `notes.md` (foreign passports are not loaded);
- situation-relevant reports and observations (a found report is context,
  NOT an authorization).
Prior-session chat is assumed unavailable; if the original task is not
recoverable from the sources above, state the absence.

### Phase 3: Diagnosis (drift mode)
Compare the working tree against the recovered context: boundary drift,
symptom masking vs root cause, data-flow tracing (Origin ➔ Transport ➔
Persistence ➔ Consumer), contract duplication. Separate FACTS from
HYPOTHESES (a file in the diff does not prove who changed it).

### Phase 4: Compact Result & Conditional Continuation
Output exactly this structure:

    Goal and completion criterion:
    Task meaning & preserved invariants:
    Environment & identity:
    Established state:
    Discrepancies & unknown facts:
    Next step & how it will be verified:
    CONTINUE / DECISION NEEDED: <if decision — ONE concrete question>

Then continue conditionally:
- conditions of already-authorized work restored → CONTINUE it;
- only a technical plan re-draft is needed within existing authority → do it;
- a new choice, authority extension, or substantive conflict → ask ONE
  concrete question;
- an explicit user pause request always wins.

Grounding itself stays read-only: no telemetry rows, no tasks.md changes,
no lock files, no persistent reports.
