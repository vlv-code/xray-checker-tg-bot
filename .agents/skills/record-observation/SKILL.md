---
name: record-observation
description: >-
  Use this skill to record an undocumented bug, environment quirk, runtime edge-case,
  or non-obvious solution into the repository's `.agents/project_knowledge/observations.md` table.
---

# Record Observation in Repository Knowledge Base

This skill ensures disciplined, standardized addition of entries to the project observation registry without corrupting document structure or causing context bloat.

---

## When to invoke skill

* The agent or user encounters an unexpected environment, build, or runtime error that was successfully resolved.
* A critical behavioral nuance of a third-party library or internal API is discovered that is not documented in official project documentation.
* The user instructs: "record observation", "add to knowledge base", "log gotcha", "record finding".

---

## Execution steps

1. **Check for duplicates:**  
   Read the recent entries of `.agents/project_knowledge/observations.md`. Ensure that a similar observation has not already been logged. If an entry already exists, do not create duplicate rows.

2. **Execute standardized CLI utility (`record_obs.py`):**  
   Locate the runtime utility deterministically:
   1. Canonical location first: `scripts/record_obs.py`.
   2. Legacy `.agents/scripts/record_obs.py` is supported ONLY as an explicit migration candidate: if found, compare with the canonical copy.
      - No canonical copy → schedule migration (move legacy ➔ `scripts/`).
      - Both exist and are IDENTICAL → migrate (delete legacy) at next update.
      - Both exist and DIVERGE → HALT and report both paths with hashes; never silently use the first found.
   
   Record the finding atomically with the resolved copy:
   ```bash
   python scripts/record_obs.py --category "<category>" --path "<path_or_component>" --issue "<issue_summary>" --solution "<workaround_or_solution>" --actor-id "<actor_id>"
   ```
   * **Parameters:**
     * `--category`: One of the canonical categories: `boundaries`, `ci`, `decommission`, `incident`, `orchestration`, `runtime`, `git`, `init`, `testing`, `architecture`.
     * `--path`: Component, script, or configuration path.
     * `--issue`: Brief summary of the bug or quirk (pipes `|` and newlines are safely escaped automatically).
     * `--solution`: Practical workaround or fix.
     * `--actor-id`: Optional qualified actor identifier (e.g. `<project_id>::<harness_id>::<installation_id>::<role_id>`) for action attribution (inserts `[actor=<actor_id>]` into the issue field).
     * `--pin`: Optional flag for permanent invariants that must never be rotated by `compact_memory.py` (adds `[pin]` suffix, e.g. `[ci][pin]`).
     * `--obs-file`: Optional custom path (defaults to `.agents/project_knowledge/observations.md`).

3. **Fallback manual entry (only if CLI script unavailable):**  
   Format the observation strictly as a single Markdown table line with escaped pipes `\|`:
   ```markdown
   | YYYY-MM-DD | [<category>] | <Path_or_component> | <Brief issue summary/fact> | <Solution or workaround> |
   ```

4. **Registry size control (Compaction Protocol):**  
   If the number of rows in the table exceeds 20–30, locate the compaction utility deterministically:
   1. Canonical location first: `scripts/compact_memory.py`.
   2. Legacy `.agents/scripts/compact_memory.py` is supported ONLY as an explicit migration candidate: if found, compare with the canonical copy.
      - No canonical copy → schedule migration (move legacy ➔ `scripts/`).
      - Both exist and are IDENTICAL → migrate (delete legacy) at next update.
      - Both exist and DIVERGE → HALT and report both paths with hashes; never silently use the first found.
   
   Invoke the automated compaction utility with the resolved copy:
   ```bash
   python scripts/compact_memory.py --max-observations 20 --keep-observations 10
   ```
   This automatically rotates older records into `.agents/project_knowledge/archive/observations_YYYYMM.md`, keeping the primary file lightweight and preventing context rot.
   *(If `compact_memory.py` is not present in `scripts/`, manually archive older resolved records into `.agents/project_knowledge/archive/observations_YYYYMM.md`)*.
