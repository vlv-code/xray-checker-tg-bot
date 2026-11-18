---
name: init-meta-project
license: MIT
compatibility: "Requires Python 3.10+ and Git"
description: >-
  Scaffold and initialize the meta-project agent environment in any repository:
  checks for existing setup to safely upgrade without data loss, or runs a 2-pass
  scan to create AGENTS.md, .agents/project_knowledge/, and install skills.
---

# Agent Environment Initialization and Upgrade (Meta-Project Init & Audit)

Autonomous skill for preparing any repository for agent workflows: checks for an existing `meta-project` structure, prevents overwriting accumulated data, and performs a safe upgrade to the latest standard or bootstraps the structure from scratch.

Relies on continuous/opportunistic refresh: when accessing meta-knowledge slices, close gaps and maintain a cohesive project vision.

---

## When to invoke skill

* The user requests: "prepare repo for agents", "initialize meta-project", "check agents.md", "update agent structure", "refresh meta-knowledge".

---

## Resource Loading Contract

This skill ships with its resources in `resources/` (sibling directory of this file). Resources are sync-managed copies of the canonical templates (`templates/project_knowledge/*.md`, `templates/AGENTS.md`, `skills/*/SKILL.md`) — read them from the skill's own installed directory; NEVER from a framework clone, and never re-create their content from memory.

| Invocation | Resource reads |
|---|---|
| Repeat-run / healthy-repo re-check (Scenario 3) | **reads NO resources** — zero-diff verification and capability reporting only. |
| Clean init (Scenario 1) | Each resource is read exactly when its step needs it — every step below names its file. |
| New-harness adoption (Scenario 4) | reads only its procedure in this file; no resources. |
| Repair scenarios (2, 5-7) | Read only the resource(s) the repaired step names. |

**Missing mandatory resource:** the dependent step is BLOCKED. Emit the envelope and stop the step:

```text
[MISSING_RESOURCE] resources/<name>: missing mandatory resource | expected: resource shipped with the skill | actual: file absent | next: restore the file from the pinned source, then re-run the blocked step
```

Guidance (verbatim contract): missing mandatory resource 'resources/<name>' — reinstall the skill from the pinned source (update-meta-project) or restore the file; do not improvise the template from memory.

---

## Execution steps

### Phase 1: Survey and Deterministic Inspection (Zero-Write Phase)

**Survey-First Zero-Write Invariant:** Phase 1 performs only discovery and diagnosis — no file creation, telemetry, `.lock` files, or execution of untrusted repository scripts.

1. **Execute deterministic inspection** with EXPLICIT runtime context (registration states are undetermined without it — the survey reports `CONTEXT_UNRESOLVED` instead of silently adopting any active installation):
   ```bash
   python scripts/inspect_meta_project.py --json --harness <harness_id> --installation <installation_id>
   ```
   *(If the script is not yet present, perform equivalent read-only inspection with pure Python stdlib, zero writes.)*
2. **Classify into one of 8 canonical states:** `NONE` (no scaffolding), `FOREIGN_RULES` (foreign instruction files: `CLAUDE.md`, `.cursorrules`, `.windsurfrules`, foreign `AGENTS.md`), `HEALTHY_REGISTERED`, `HEALTHY_NEW_HARNESS` (target harness/installation unregistered or `planned`), `CONTEXT_UNRESOLVED` (no `--harness`/`--installation` — re-run with explicit context before routing), `LEGACY_UNREGISTERED` (legacy structure without `.agents/meta-project.json`), `PARTIAL_CORRUPTED` (missing/corrupted critical slices), `INCOMPATIBLE_SCHEMA` (registry schema incompatible with current tooling).
3. **Evaluate capability matrix:** readiness per `read_instructions`, `local_checks`, `skills`, `roles`, `hooks` (`available` / `unavailable` / `untested`).
4. **Select execution strategy:** route to Phase 2 (Scenario Agreement) and the Phase 3 scenario matching the state.

---

### Phase 2: Scenario Agreement and Decision Capture (Alignment Phase)

Agree the scenario across 4 dimensions (present options, capture decisions):
1. **profile:** `active_development` | `maintenance` | `research` | `audit` | `documentation`.
2. **deployment_mode:** `shared_repo` (committed to Git for team sync and CI gates) | `local_overlay` (`.git/info/exclude`, experimental — NOT yet test-verified; present as such) | `hybrid` (passport committed, agent notes/reports local).
3. **collaboration_model:** `solo` | `duo` (Tier 0 Micro-Mini Duo) | `triad` (Canonical Compact Trio) | `specialized_swarm` (declared but NOT implemented in `resolve_team_topology` — offer triad-now-or-defer explicitly). Multi-harness: identify primary and planned harnesses by identifiers agreed with the user for THIS project (configuration, not fixed constants).
4. **execution rules:** `verification_policy`: `strict_independent` | `duo_peer_review` | `self_check`; `delegation_level`: `strictly_manual` | `controlled` | `autonomous`; constraints: zero-dependency core, platform limits, sensitive paths.

#### Core Invariants of Scenario Agreement:
- **Repeat Run Invariant:** `HEALTHY_REGISTERED` with a scenario already recorded in `.agents/meta-project.json` → do NOT re-interview the user; reuse the existing scenario.
- **Strict Separation of Truth:** outputs strictly separate User Decisions, Established Facts, Agent Recommendations; proposals are never recorded as approved user decisions without confirmation.
- **Capability-Desire Conflict Resolution:** desires conflicting with environment capabilities (e.g. independent verification requested, solo-only harness) → present a concrete binary choice.
- **Persistence:** save the agreed scenario into `.agents/meta-project.json` under the `"scenario"` block with `schema_version: "1.1.0"`.

---

### Phase 3: Execution and Scaffolding by Canonical State

#### Scenario 1: State `NONE` (Clean Scaffold Mode)
Standard two-pass algorithm:

##### Pass 1: Express Scan and Skeleton Generation (Fast & High-Level)
1. **Express root inspection:** manifests (`package.json`, `pyproject.toml`, `go.mod`, `Cargo.toml`, `requirements.txt`, `pom.xml`, `docker-compose.yml`); core directories (`src/`, `app/`, `tests/`, `migrations/`, `scripts/`, `data/`); project name, purpose, core concepts from `README.md`; required keys from `.env.example`. **Zero Unmapped Dirs Rule:** after filtering `.gitignore` paths, VCS metadata, and standard caches (`.git`, `node_modules`, `.venv`, `venv`, `dist`, `build`, `coverage`, `__pycache__`, `.pytest_cache`, `.idea`, `.vscode`), every remaining top-level source directory MUST be explicitly mapped to Green, Yellow, or Red in boundaries — unmapped falls under Default Deny / Red Zone.
2. **Stack and orientations:** concise stack string (e.g. `Python 3.12, FastAPI, uv, pytest, Docker`) + 2-3 baseline North Stars (simplicity, strict typing, data immutability, offline-first as applicable).
3. **Create directories:** `.agents/project_knowledge/`, `.agents/project_knowledge/archive/`, `.agents/skills/record-observation/`, `.agents/skills/sync-architecture/`, `.agents/skills/ground-meta-project/`, `scripts/`.
4. **Deploy bundled meta-project skills and runtime scripts** (READ: `resources/record-observation-SKILL.md`, `resources/sync-architecture-SKILL.md`, `resources/ground-meta-project-SKILL.md`): deploy the agreed SUBSET defined by `templates/release_manifest.json` of the pinned source. The 3 shell skills are deployed by VERBATIM FILE COPY of those resource files to `.agents/skills/<skill>/SKILL.md` (no in-context transit). Runtime scripts deploy verbatim to repository `scripts/` (component paths and `kind` values from the manifest; `requires` dependencies deploy together with their dependent).
   **Canonical Script Source Invariant:** deploy these files VERBATIM from the installed meta-project resources (pinned upstream `vlv-code/meta-project` @ `main`, or the matching version in the global skills cache). NEVER re-create, paraphrase, or partially copy runtime scripts from descriptions — a diverged script is a defect, not an adaptation.
5. **Create initial knowledge slice drafts and registry:** (READ: `resources/observations.md`, `resources/boundaries.md`, `resources/weak_spots.md`) — materialize the three slice drafts from these resources into `.agents/project_knowledge/` byte-canonically (Pass 2 calibrates them in place). Write `.agents/meta-project.json` registering the CURRENT installation as `<installation_id>`/`<harness_id>` with status `active`; add a `planned` second-harness entry ONLY when the user explicitly plans one (`ag-main`/`zc-main` are Meta-Project's own configuration values, never generic scaffold defaults).
   **Scaffold baseline `.gitignore` hygiene block (append-if-absent, never overwrite):**
   If the repository root has no `.gitignore`, create it with the baseline block below; if a `.gitignore` already exists, APPEND only the missing entries (append-if-absent semantics — never overwrite or rewrite an existing file). Generated bytecode and lock artifacts are hygiene paths mapped Green: ignored by VCS, excluded from boundary audits and architecture-drift warnings.
   ```gitignore
   # Meta-Project hygiene baseline (append-if-absent)
   __pycache__/
   *.pyc
   *.lock
   ```

##### Pass 2: Repository Deep-Dive, Principles & Shell Skills

1. **Mandatory Core Inspection Checklist** (never populate `weak_spots.md` / `architecture.md` with boilerplate without reading key-module source): Storage & Database — primary data access module (LOC, schema coupling, migration setup); External API & Service Clients — retries, timeouts, fallbacks, global singletons; Containerization & Runtime — `Dockerfile`, compose, CI workflows (system binaries, permissions, volume mounts); User Interface & Input — keyboard/router modules (platform constraints, FSM states, destructive-action confirmation).
2. **Interactive Calibration Interview (Codified Two-Question Protocol):** exactly 2 focused questions via `ask_question` or a direct interactive prompt:
   - **Question 1 (North Stars & Core Invariants):** *"Какие ключевые архитектурные инварианты, бизнес-правила и ограничения продакшена (North Stars) нельзя нарушать при любых доработках?"* (What core architectural invariants, business rules, and production constraints must never be broken?)
   - **Question 2 (Red Zone & Sensitive Paths):** *"Какие специфические каталоги или модули (биллинг, БД, криптография, ключи, легаси) являются критическими и должны быть заблокированы в Red Zone?"* (Which directories, databases, credentials, or legacy modules must be strictly placed into Red Zone?)
   *Processing:* Q1 answers → Section 1 of `AGENTS.md` ("Principles and Architectural Orientations (North Stars)"); Q2 answers → Red Zone of `boundaries.md` before single-pass flush; skipped/empty answers → fall back gracefully to inspection-derived orientations.
3. **Calibrate project concepts:** synthesize checklist findings and interview answers into 2-3 concise architectural orientations (North Stars) preventing contradictory agent actions.
4. **Match stack skills from the agent shell registry:** Python/uv/ruff → `python-pro`; FastAPI → `fastapi-pro`; TypeScript/JavaScript → `typescript-expert`; Next.js → `nextjs-best-practices`; Docker → `docker-expert`; PostgreSQL/SQL → `postgresql-optimization`, `sql-pro`; Kubernetes/Helm → `kubernetes-architect`; Telegram/aiogram → `telegram-bot-builder`; CI/CD → `github-actions-creator`; clean code & review → `clean-code`, `code-reviewer`.
5. **Calibrate boundaries (`boundaries.md` draft from Pass 1):** refine globs for actual project directories. Default Deny / 100% path coverage (Zero Unmapped Dirs) with the Black Zone rule: any unmapped path is Red by default. Test zone differentiation: new-feature tests Green; existing regression suites (`tests/**`, `*_test.*`, `*.spec.*`) Yellow with the no-weakening invariant (`assert`/`expect` must never be weakened); architectural integrity, API contract, schema, E2E tests strictly Red. Formatting/template engines Yellow; database files (`data/**`, `*.db`, `*.sqlite*`) Red; replace placeholder paths with actual migration (`alembic/versions/**`, `prisma/migrations/**`) and route/controller locations. **Never drop the mandatory security defaults:** `src/auth/**`, `src/billing/**`, `src/payments/**`, `src/crypto/**`, `*.pem`, `*.key`, `config/secrets/**`, CI/CD pipelines (`.github/workflows/**`), `.github/CODEOWNERS`, `scripts/check_boundaries.py`.
6. **Risk and weak spot analysis (`weak_spots.md` draft):** record concrete technical debt from inspection — monoliths, singletons, external runtime dependencies (Redis, Postgres, brokers, system utilities like FFmpeg), platform constraints, mandatory `.env.example` variables.
7. **Populate architecture and tasks slices** (READ: `resources/architecture.md`, `resources/tasks.md`): compose `architecture.md` strictly from the resource (map-first card) — Component Map table `Component | Location | Role | Depends on` (one row per component, no prose; the "updated IN THE SAME CHANGE that touches a component" rule applies from the very first commit); three one-line Stack / State + Single Source of Truth / Cross-border contracts slots; conditional "End-to-End Data Flows" section ONLY for multi-tier systems (omit for single-layer CLI/stdlib libraries to prevent bloat). `Design Patterns` is not part of the canonical format — do not add it. Compose `tasks.md` from the resource as well (team rules with the `- [>]` checkpoint convention, active objective, checklist, delegation registry).
8. **In-Memory Synthesis Flush Protocol:** no intermediate drafts on disk; write in strict bottom-up order — Step A: pre-installed skills (`.agents/skills/record-observation/`, `sync-architecture/`, `ground-meta-project/`) and automation scripts (`scripts/check_boundaries.py`, `scripts/compact_memory.py` if shared-repo setup); Step B: knowledge slices (`observations.md` → `boundaries.md` → `weak_spots.md` → `architecture.md`); Step C (READ: `resources/AGENTS.md`): root passport composed from the resource — strictly last, as the aggregator of slice references and stack skills.

#### Scenario 2: State `FOREIGN_RULES` (Preserve & Merge Mode)
1. Strictly preserve foreign rule files (`.cursorrules`, `CLAUDE.md`, `.windsurfrules`, foreign `AGENTS.md`) — never delete or modify.
2. Map foreign rule files into Yellow or Green zones in `boundaries.md` (avoid false Red triggers).
3. Scaffold the `.agents/` structure non-destructively; incorporate foreign instructions into `AGENTS.md` by explicit reference or a designated coexistence section.

#### Scenario 3: State `HEALTHY_REGISTERED` (Pass-Through / No-Op Mode)
1. Zero-diff verification: **0 modified files**, no temporary files, no locks; report "Meta-Project is intact, verified, and active for current harness. All capability levels operational. Ready for work."
2. Repeat-run semantics: fresh init report → strict no-op; no fresh report → ONE limited re-survey and an adoption init report (`python scripts/manage_reports.py init --action create`), WITHOUT re-scaffolding; read-only request → no report at all; `INCOMPATIBLE_SCHEMA` → diagnostics only, never automatic overwrite (Scenario 7).

#### Scenario 4: State `HEALTHY_NEW_HARNESS` (Multi-Harness Adaptation Mode)
1. Preserve existing installations — strictly forbidden to delete, disconnect, or alter active entries in `.agents/meta-project.json`.
2. Register the new harness installation (e.g. `zc-main` / `zcode`) with active status.
3. Scaffold role bindings in `.agents/team/` for the new harness, maintaining qualified `actor_id` coordinates.

#### Scenario 5: State `LEGACY_UNREGISTERED` (Legacy Adoption Mode)
1. Prohibition on destructive overwrite: preserve existing `observations.md` (all historical rows) and `tasks.md`.
2. Upgrade standards: North Stars and directive protocol in `AGENTS.md` (no deletion of custom user invariants); Default Deny / 100% coverage in `boundaries.md`; 5-column observations table with `Category` column.
3. Deploy registry and runtime utilities: `.agents/meta-project.json`, `scripts/inspect_meta_project.py`, `scripts/verify_identity.py`, `scripts/record_obs.py`.

#### Scenario 6: State `PARTIAL_CORRUPTED` (Diagnose & Repair Mode)
Report exact missing components or syntax errors from the inspection report (`missing_components`, `conflicts`); surgically recreate ONLY missing/corrupted files without touching intact knowledge bases or profiles (READ only the resource matching the file being recreated).

#### Scenario 7: State `INCOMPATIBLE_SCHEMA` (Read-Only Fallback Mode)
Fail-closed protection: no automatic writes when `schema_version` is incompatible; report the mismatch, enter read-only inspection mode, and provide manual migration instructions.

---

### Phase 4: Formal Initialization Report Generation and Manage Handoff

1. **Generate report file via stdlib engine:**
   ```bash
   python scripts/manage_reports.py init --action create \
     --installation <installation_id> \
     --harness <harness_id> \
     --actor <actor_id> \
     --status <completed|awaiting_decision|blocked> \
     --scenario-file <path_or_inline>
   ```
   Written atomically to `.agents/reports/<installation_id>/init/<init_run_id>.json` and safely updates `latest.json`.
2. **Status semantics:** `completed` (slices initialized, scenario agreed, baseline checks passed) | `awaiting_decision` (critical choices unresolved; a decision ledger, NOT deployment authorization) | `blocked` (environment incompatibility or broken invariants).
3. **Machine-Enforced Manage Admission:** manage-side intake runs through the same engine — `python scripts/manage_reports.py manage --action create ...` fails closed (`evaluate_manage_intake`: local-registry project/installation/harness match, admitted actor, existing init run, completed + ready status, fresh input fingerprints). NEVER assemble manage reports by hand; a refused intake must be resolved, not bypassed.
4. **No Automatic Team Deployment Invariant:** `init-meta-project` NEVER deploys agent roles or teams — deploying or adapting roles is the exclusive responsibility of `manage-meta-project`. End initialization with a concise human-readable summary:
   ```text
   Initialization complete. Report saved to .agents/reports/<installation_id>/init/<init_run_id>.json.
   Recommended next step: Invoke 'manage-meta-project' to configure team roles according to the agreed scenario.
   ```

---

### Finalization and Verification

1. Verify created/updated files (`ls -R .agents`).
2. **Local Invisible Overlay Option:** in an external/client project or personal no-commit mode, add `AGENTS.md` and `.agents/` to `.git/info/exclude`; verify they do not appear in `git status` untracked files.
3. Output a concise summary: execution mode (initial setup vs audit upgrade; shared vs invisible overlay), created/updated file tree, captured North Stars, linked stack skills.
