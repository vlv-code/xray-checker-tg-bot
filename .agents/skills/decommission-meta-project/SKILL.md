---
name: decommission-meta-project
license: MIT
compatibility: "Requires Python 3.10+ and Git"
description: >-
  Safely decommission, prune, or hibernate agent team roles (.agents/team/):
  performs non-destructive archiving into .agents/team/archive/, finalizes telemetry,
  synchronizes root AGENTS.md, and reassigns orphaned tasks without data loss.
---

# Agent Team Decommissioning and Archival (Meta-Project Decommission)

Autonomous skill for safe, non-destructive decommissioning, resizing, and archiving of specialized agent roles in `.agents/team/`.
Adheres to the core zero-data-loss invariant: physical deletion (`rm`, `Remove-Item`) of role workspaces is strictly prohibited. Accumulated context in private `notes.md` and historical telemetry in `profile.md` are atomically preserved via archiving into `.agents/team/archive/<role_id>/`.

---

## When to invoke skill

* The user requests: "decommission team", "archive agent X", "sunset role X", "hibernate agent team", "prune inactive roles", "prepare team for re-deployment", "сверни команду", "законсервируй агентов", "выведи роль X", "удали агента X из проекта".
* Project undergoes technology deprecation (e.g., Docker decommissioned, frontend removed, or team resized to Compact Trio).
* Repository enters long-term maintenance or dormant hibernation mode.

---

## Execution steps

### Phase 0: Safety & Scope Verification

Before performing any file operations:
1. Scan active role directories in `.agents/team/` (excluding `archive/`).
2. Confirm the target roles to decommission with the user (unless an explicit full reset was commanded).
3. **Core Invariant (Zero Data Loss):** Under no circumstances may an agent execute destructive deletion (`rm -rf`, `Remove-Item -Recurse`) on role workspaces. Workspaces must always be preserved via atomic relocation to `.agents/team/archive/`.
4. **Registry-Aware Decommissioning (multi-harness model):**
   - Read `.agents/meta-project.json` and enumerate every binding that references the target role, per installation.
   - **Distinguish two different operations:**
     - **(A) Disable ONE installation's binding** (e.g. disconnecting ZCode while Antigravity keeps working): set that binding's `status` to `"disabled"` in the registry and archive ONLY the installation-specific profile copy (e.g. `.agents/team/<role_id>-<installation_suffix>/`) if one exists. The shared role, other bindings, other installations' profiles, notes, and telemetry stay untouched.
     - **(B) Archive the SHARED role:** allowed ONLY when NO other `active` or `planned` installation still references the role. If any other installation references it, **REFUSE with an explicit report** listing the referencing installations and offer operation (A) instead.
   - Apply registry changes, the `AGENTS.md` Section 7 roster, and the archive relocation CONSISTENTLY in one pass; preserve memory and historical authorship (`profile.md` telemetry, `notes.md`) byte-for-byte.
   - Verify integrity after changes: `python scripts/verify_identity.py --check` must pass (0 errors) — a dangling `profile_path` or stale binding is a failed decommission.

---

### Scenario A: Targeted Role Sunsetting (Single Role Pruning)
*Used when a single technology layer or specialization is retired from the project.*

1. **Telemetry Closeout:**
   Append a closing entry to `.agents/team/<role_id>/profile.md` in the telemetry table:
   ```markdown
   | <YYYY-MM-DD> | User | Decommissioning / Sunsetting | Role archived into .agents/team/archive/<role_id>/ |
   ```
2. **Directory Archival:**
   - Ensure target directory `.agents/team/archive/` exists.
   - Atomically move `.agents/team/<role_id>/` to `.agents/team/archive/<role_id>/`.
3. **Passport Synchronization (`AGENTS.md`):**
   - Remove the decommissioned role's link and description from Section `## 7. Project Agent Team & Multi-Harness Identity` in root `AGENTS.md`.
4. **Orphaned Tasks Reassignment:**
   - Scan `.agents/project_knowledge/tasks.md`.
   - Reassign any pending subtasks delegated to this role to the default generalist (`agent-general-worker` / `agent-dev-lead`) or mark them as `[CANCELLED]`.
5. **Observation Logging:**
   - Record the role sunsetting fact into `.agents/project_knowledge/observations.md` via `record-observation`.

---

### Scenario B: Team Resizing & Re-deployment Reset (Batch Archival)
*Used when transitioning from a bloated team to a Scale-Adaptive Compact Trio, or rotating team structure.*

1. **Batch Telemetry Closeout:**
   For every active role in `.agents/team/`, append a transition entry in `profile.md`:
   ```markdown
   | <YYYY-MM-DD> | User | Decommissioning / Resizing | Archived during team re-deployment and sizing calibration |
   ```
2. **Batch Archival Relocation:**
   - Ensure `.agents/team/archive/` exists.
   - Move all active role directories from `.agents/team/<role_id>/` into `.agents/team/archive/<role_id>/`.
   *(If a role with the same ID already exists in archive, append timestamp suffix: `.agents/team/archive/<role_id>-<YYYYMMDD>/`)*.
3. **Passport Section Reset:**
   - In root `AGENTS.md`, clear the decommissioned entries under `## 7. Project Agent Team & Multi-Harness Identity` to prepare for the new synthesized team.
4. **Tasks Quarantine:**
   - In `.agents/project_knowledge/tasks.md`, close or quarantine active tasks from the prior team cycle.
5. **Report & Handoff:**
   - Report archival completion to the user. The repository is now clean and ready for `manage-meta-project` to materialize the new synthesized team.

---

### Scenario C: Full Repository Hibernation (Dormant Mode)
*Used when active development concludes and the repository transitions into a dormant maintenance state.*

1. Execute Scenario B steps to archive all active roles into `.agents/team/archive/`.
2. Leave root `AGENTS.md` and `.agents/project_knowledge/` slices intact as the repository's permanent collective memory.
3. Inform the user:
   "All agent roles have been safely hibernated into `.agents/team/archive/`. Static project memory and knowledge slices remain active. Re-activate anytime via `skills/manage-meta-project/SKILL.md`."

---

### Phase Final: Decommissioning Report

Upon completing any scenario, output a concise status table:
```markdown
| Decommissioned Role | Archive Destination Path | Unfinished Tasks Handled | Status |
| :--- | :--- | :--- | :--- |
| `agent-deep-auditor` | `.agents/team/archive/agent-deep-auditor/` | Reassigned / None | Archived |
| `agent-qa-engineer` | `.agents/team/archive/agent-qa-engineer/` | Reassigned / None | Archived |
```
