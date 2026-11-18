# Universal Agentic Harness Invariant: Meta-Project Compatibility

Add these rules to your global agent environment (`~/.gemini/GEMINI.md`, `~/.claude/CLAUDE.md`, or IDE User Rules) to prepare your agent harness for any repository initialized with Meta-Project.

---

## 1. First-Contact Rule
When first exploring or inspecting any repository or project:
- Check for `AGENTS.md` at the repository root.
- If present, read `AGENTS.md` BEFORE exploring `README.md` or arbitrary source files.
- Never assume project conventions from generic pretraining when a project passport exists.

## 2. Directive Meta-Knowledge Protocol
Before modifying schemas, architectural contracts, or core logic:
- Consult the target knowledge slices in `.agents/project_knowledge/`:
  - `boundaries.md` — responsibility zones and file modification limits.
  - `weak_spots.md` — known failure modes, monoliths, and technical debt.
  - `architecture.md` — component topology and end-to-end data flows.
  - `observations.md` — recorded environment quirks and workarounds.
  - `tasks.md` — active objective, checklists, and delegated subtasks.
- **Opportunistic Refresh:** Update knowledge slices upon discovering gaps or inaccuracies, without destructive overwriting.

## 3. Responsibility Boundaries & Default Deny
- Respect Green, Yellow, and Red zones defined in `boundaries.md`.
- **Default Deny (Black Zone):** Any directory or file path not explicitly mapped in `boundaries.md` is strictly Red Zone by default. Autonomous modification of unmapped paths is prohibited.

## 4. Circuit Breaker & Root Cause Invariant
- **Circuit Breaker:** Repeatedly failing edits targeting one defect, or new regressions appearing in adjacent code while fixing it, are a semantic signal to STOP editing and trace the full data path (`Origin ➔ Transport ➔ Persistence ➔ Consumer`) before the next diff. A normal iterative TDD loop is NOT a violation.
- **Minimal Diffs, Maximal Depth:** Minimize the width of file interference (no unrelated refactoring); maximize the depth of root-cause investigation (fix the origin, not symptom errors).
- **Definition of Done:** Whenever an undocumented environment quirk, compiler macro mismatch, or runtime edge-case is solved, log it atomically via `record-observation`.

## 5. Tooling & Testing Invariants
- **Default Closed Tooling:** Core framework and agents operate in a zero-dependency closed loop by default. External tools and third-party dependencies are permitted ONLY when explicitly declared (opt-in) in project specification or requested by user. Operational liabilities for third-party tools belong to the project/operator.
- **Adversarial TDD:** Never accept trivial green tests. Any test-driven development must include negative and adversarial boundary checks (confirming failure on missing implementation) before transitioning to green.
