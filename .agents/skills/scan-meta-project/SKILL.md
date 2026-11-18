---
name: scan-meta-project
license: MIT
compatibility: "Requires Python 3.10+ and Git"
description: >-
  Inspect repository meta-knowledge integrity, verify 100% boundary coverage,
  detect architectural drift, and opportunistically backfill missing facts without re-initialization.
---

# Repository Meta-Knowledge Integrity Scanner (Scan Meta-Project)

Operational skill for conducting fast express scans or deep-dive integrity audits of an existing meta-project setup without destructive re-initialization.

The skill operates in two modes:
- **`--shallow` (Express Sanity Check):** Verifies existence and non-emptiness of root `AGENTS.md`, 5 knowledge slices, executes Boundary Gate (`check_boundaries.py --audit-tree`) for 100% file coverage, and checks task freshness.
- **`--deep` (Core Deep-Dive & Drift Check):** Runs all shallow checks, invokes the Core Inspection Checklist across key runtime modules, delegates component/data-flow synchronization to `sync-architecture`, audits technical debt in `weak_spots.md`, and runs local integrity tests.

---

## When to invoke skill

* The user requests: "scan-meta-project", "scan meta project", "инспекция мета-знаний", "чекни покрытие", "audit meta-knowledge", "check boundaries coverage", "найди слепые зоны", "verify slices".
* Pre-release verification, PR reviews, or resuming development after extensive changes made outside the agentic session.
* Automatically invoked by `init-meta-project` at the conclusion of initialization.

---

## Execution steps

### Phase 1: Mode Determination & Target Resolution

1. Identify invocation mode from user prompt or default to `--shallow`:
   - `--shallow`: Routine integrity check, boundary coverage validation, task freshness.
   - `--deep`: Full architectural drift detection, Core Inspection Checklist, test execution.
2. Locate the Boundary Gate script deterministically:
   1. Canonical location first: `scripts/check_boundaries.py`.
   2. Legacy `.agents/scripts/check_boundaries.py` is supported ONLY as an explicit migration candidate: if found, compare with the canonical copy.
      - No canonical copy → schedule migration (move legacy ➔ `scripts/`).
      - Both exist and are IDENTICAL → migrate (delete legacy) at next update.
      - Both exist and DIVERGE → HALT and report both paths with hashes; never silently use the first found.
   - If neither script exists, verify boundaries manually against `boundaries.md` glob patterns.

---

### Phase 2: Shallow Inspection (`--shallow`)

1. **Passport Integrity:**
   - Verify presence of root `AGENTS.md`.
   - Ensure Section 1 contains North Stars and invariants (`Circuit Breaker`, `Default Deny`).
   - Ensure Sections 2-5 (`Repository Meta-Map`, `Directive Meta-Knowledge Protocol`, `Procedural Skills Registry`, `Local Verification Tooling & Autonomous Fallback`) are populated without placeholder tags (`{{...}}`).
2. **Knowledge Slices Audit:**
   - Verify the 5 canonical slices in `.agents/project_knowledge/`:
     * `architecture.md` (components and data flows).
     * `boundaries.md` (responsibility zones).
     * `weak_spots.md` (fragile modules and risks).
     * `observations.md` (5-column observation table).
     * `tasks.md` (active objective and checklists).
3. **Boundary Coverage (Zero Unmapped Dirs Rule):**
   - Locate the Boundary Gate utility deterministically:
     1. Canonical location first: `scripts/check_boundaries.py`.
     2. Legacy `.agents/scripts/check_boundaries.py` is supported ONLY as an explicit migration candidate: if found, compare with the canonical copy.
        - No canonical copy → schedule migration (move legacy ➔ `scripts/`).
        - Both exist and are IDENTICAL → migrate (delete legacy) at next update.
        - Both exist and DIVERGE → HALT and report both paths with hashes; never silently use the first found.
   - Run the automated Boundary Gate audit with the resolved copy:
     ```bash
     python scripts/check_boundaries.py --audit-tree
     ```
   - If any unmapped files or directories are reported, treat them as Black Zone breaches. Flag them immediately for inclusion in `boundaries.md`.
4. **Task Register Check (`tasks.md`):**
   - Verify that the active objective is clear and completed items are marked with `- [x]`.

---

### Phase 3: Deep Inspection (`--deep`)

*When executed with `--deep`, the agent runs all Phase 2 checks, followed by:*

1. **Core Inspection Checklist (Key Modules):**
   - **Database & Storage:** Read primary model/repository files. Inspect schema coupling, migrations, and monolithic growth.
   - **External Clients & APIs:** Read integrations (AI SDKs, HTTP clients). Check timeout settings, circuit breakers, and error propagation.
   - **Containerization & CI:** Read `Dockerfile`, `docker-compose*.yml`, `.github/workflows/`. Verify build matrix and security flags.
   - **Entrypoints & UI:** Read main routers, CLI entrypoints, and command handlers. Check platform payload constraints and input validation.
2. **Architectural Drift Detection (Delegation to `sync-architecture`):**
   - Do NOT duplicate custom code parsing logic. Invoke the procedural skill `sync-architecture`:
     * `sync-architecture` performs Pre-Flight Preservation of all `local_repo_data` blocks.
     * Updates `.agents/project_knowledge/architecture.md` with current components and End-to-End Data Flows.
3. **Risk & Debt Recalibration (`weak_spots.md`):**
   - Check existing items in `weak_spots.md`: if a previously recorded technical debt is resolved, mark it as resolved or remove it.
   - If new fragile couplings or architectural bottlenecks were discovered during the Core Inspection, record them.
4. **Automated Test Run:**
   - Execute project test suite using standard test discovery:
     ```bash
     python -m unittest discover -s tests -p "test_*.py" -v
     # Or stack-specific runner (e.g. pytest, npm test, cargo test)
     ```

---

### Phase 4: Opportunistic Refresh & Report

1. **Backfill Identified Gaps (Read + Opportunistic Refresh):**
   - Atomically write discovered unmapped paths into `boundaries.md` (within `<!-- local_repo_data:boundaries -->`).
   - Update `tasks.md` with new subtasks or completed milestones.
   - Record newly resolved environment quirks into `observations.md`.
2. **Deliver Scan Report:**
   Present a concise summary to the user:
   ```markdown
   ### 🔍 Scan Report (Meta-Project Scanner)
   - **Mode:** `--shallow` | `--deep`
   - **Passport & Slices:** 5/5 slices present, AGENTS.md valid
   - **Boundary Coverage:** 100% mapped (or N unmapped paths detected)
   - **Architectural Drift:** In sync (or updated via sync-architecture)
   - **Identified Action Items:** [List of 1–3 concrete recommendations, if any]
   ```
