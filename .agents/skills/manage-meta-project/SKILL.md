---
name: manage-meta-project
license: MIT
compatibility: "Requires Python 3.10+ and Git"
description: >-
  Analyze repository architecture and stack to discover, configure, and orchestrate specialized
  agent roles (.agents/team/), audit existing roles for activity/pruning, and enforce human-in-the-loop coordination rules.
---

# Repository Agent Team Orchestration (Meta-Project Team Orchestration)

Autonomous skill for composing, auditing, and optimizing a specialized agent team tailored to the project. It analyzes repository structure and the agreed scenario, materializes isolated role profiles in `.agents/team/`, and establishes strict coordination protocols (human-in-the-loop, user-mediated handoffs, immutable notes, evidence-based arbitration).

---

## When to invoke skill

* The user requests: "assemble agent team", "staff project with roles", "configure agent roles", "audit team activity", "review team roles", "update agent team composition".
* New major technology layers are introduced into the project (Docker, CI/CD, mobile UI, database, external payment integrations) requiring dedicated domain governance.

---

## Resource Loading Contract

This skill ships with its team templates in `resources/` (sibling directory of this file). Resources are sync-managed byte-copies of the canonical templates (`templates/team/*.md`) — read them from the skill's own installed directory; NEVER from a framework clone, and never re-create their content from memory.

| Phase | Resource reads |
|---|---|
| Phase 0.5 + Phase 5 (materializing roles) | Each resource is read exactly when its step needs it: `resources/profile.md`, `resources/notes.md`, `resources/system_prompt.md`. |
| Phases 1–4, 6, 7 | **reads NO resources** — survey, topology planning, audit, dialogue, reporting, and rules of engagement are self-contained. |

**Missing mandatory resource:** the dependent step is BLOCKED. Emit the envelope and stop the step:

```text
[MISSING_RESOURCE] resources/<name>: missing mandatory resource | expected: resource shipped with the skill | actual: file absent | next: restore the file from the pinned source, then re-run the blocked step
```

Guidance (verbatim contract): missing mandatory resource 'resources/<name>' — reinstall the skill from the pinned source (update-meta-project) or restore the file; do not improvise the template from memory.

---

## Execution steps

### Phase 0.5: First-Role Bootstrap (roles: {})

Identity-less repositories — `.agents/meta-project.json` with `roles: {}` — bootstrap their first team through a strict three-step report path. Creating role files or registry entries WITHOUT this report chain is prohibited: the chain IS the authorization.

1. **Init report by the initializing operator.** A repository with no deployed roles has no registered actor to admit yet, so the init report records the INVOKING HARNESS AGENT as the init `actor_id` (identity admission is not yet applicable to this first report):
   ```bash
   python scripts/manage_reports.py init --action create \
     --installation <installation_id> --harness <harness_id> \
     --actor <invoking-agent-actor_id> --status completed \
     --scenario-file <scenario.json>
   ```
2. **Manage adds roles + bindings per Phase 5** (materialize profiles, register `roles.<role_id>` with bindings for the active installation).
3. **Manage report declares the changes.** The operation's `--data-file` MUST carry a `changes[]` list exactly describing the registry delta it applied, so the engine's intake admits it:
   ```json
   {"changes": [{"kind": "add_role", "role_id": "agent-dev-lead", "installation": "<installation_id>", "path": ".agents/team/agent-dev-lead/profile.md"}],
    "roles": {"preserved": [], "created": ["agent-dev-lead"], "adapted": [], "archived": []}, "bindings": {}}
   ```
   Allowed change kinds: `add_role`, `disable`, `archive` (with `role_id`, `installation`, and optional `path`). The engine verifies the declared delta against the registry state anchored in the init report (git baseline + recorded fingerprint); undeclared, partially declared, or unverifiable registry mutations fail closed — resolve by re-running init, never by bypassing intake.

---

### Phase 1: Pre-Flight Report Intake & Rapid Repository Scan

Before defining or modifying roles, inspect initialization results and repository markers:

0. **Deterministic Survey & Init Report Intake:** run `python scripts/inspect_meta_project.py --json`; ingest the latest init report for the target installation (`python scripts/manage_reports.py latest --installation <installation_id> --type init --json`).
   - Validate schema version compatibility, matching `project_id` and target `installation_id`.
   - **Fail-Closed Block:** if the init report is missing, or its status is `awaiting_decision` or `blocked`, **HALT**. Do not plan or deploy the team; report the pending decisions or blockers to the user.
   - **Machine-Enforced Intake:** admission is executed by the engine, not by agent judgment. Team changes are recorded via `python scripts/manage_reports.py manage --action create ...`, which runs fail-closed intake (`evaluate_manage_intake`: local-registry project/installation/harness match, admitted actor, existing init run, completed + ready status, fresh input fingerprints). Hand-assembled manage reports are prohibited; a refused intake must be resolved (fresh init run), never bypassed.
   - **Staleness Protocol:** if the git commit in the report differs from current `HEAD`, execute a limited re-survey to verify that boundaries, stacks, and registry remain valid. Do NOT wipe the existing team or force a full re-initialization due to normal commits. Read the agreed `scenario` block from the report or `.agents/meta-project.json`.
1. **Rapid repository scan for role-relevant markers:** hosting/CI (`.github/`, workflows, PR/issue templates); licenses and open-source governance (`LICENSE`, `CONTRIBUTING.md`, dependency manifests); containerization/orchestration (`Dockerfile`, `docker-compose*.yml`, `k8s/`, `helm/`); testing infrastructure (`tests/`, runner configs, fixtures); security and sensitive data (`auth/`, `security/`, token handling, encryption); UI and design (frontend modules, bot keyboards, assets, design tokens); current project passport (`AGENTS.md`, `.agents/project_knowledge/` slices); secondary / polyglot stacks (`.cpp`, `.c`, `.h`, `.rs`, `.go`, `.swift` in `client/`, `native/`, `core/`, `desktop/`).

---

### Phase 2: Scenario-Driven Team Topology Planning & Sizing

Team composition is determined by the agreed **`scenario`** and technology stack (codebase LOC is a secondary sizing guideline).

**Topology Precedence Invariants (`resolve_team_topology`):**
1. The explicitly agreed `collaboration_model` is the USER'S DECISION and is never silently downgraded by size heuristics (LOC never turns an agreed triad into a duo).
2. `specialized_swarm` is declared but NOT YET IMPLEMENTED (`supported: false` in the engine result) — present an explicit choice: proceed as triad now or defer.
3. Contradictory scenario dimensions (e.g. `solo` + `strict_independent`) surface a non-empty `conflicts` list — resolve them with the user; silent picks are prohibited.

#### 1. Scenario-to-Topology Mapping
- **`documentation` or `research` profile:** deploy a **Solo Executor** (`agent-general-worker` or standalone role) with documentation/research stack skills — no unnecessary multi-agent handoff overhead.
- **`maintenance` or compact repository (<3–5k lines, Tier 0):** deploy the **Micro-Mini Duo** (`agent-dev-lead` + `agent-quality-guardian` / `agent-inspector-guardian`) for streamlined 2-stage handoff (`Developer ➔ Inspector/Guardian`).
- **`active_development` with `strict_independent` verification:** deploy the **Canonical Compact Trio** (`agent-dev-lead` + `agent-quality-guardian` + `agent-github-steward`) ensuring distinct separation of development, quality verification, and release/CI governance.
- **Microservices / Multi-stack / Enterprise (>50k lines or polyglot):** deploy a **Specialized Team** (3–6+ roles) tailored to isolated subsystems.

#### 2. Action Differentiation (The 5 Operations)
When orchestrating the team, `manage-meta-project` must distinguish and explicitly report:
1. **Preserve existing team:** if active roles match the scenario and target installation, perform a no-op (zero churn).
2. **Adjust duties:** update system prompts or boundary assignments without destroying private notes (`notes.md`).
3. **Add missing role:** scaffold only newly required role profiles.
4. **Connect existing roles to new harness:** add bindings in `.agents/meta-project.json` for a new harness installation without duplicating profiles or overwriting active bindings.
5. **Archive unused role:** move obsolete roles to `.agents/team/archive/<role_id>/` strictly after verifying that NO other installation references them.

* **Polyglot Stack Isolation Rule (No Toxic Bundles):** strictly forbid bundling secondary compiled or specialized stacks (C/C++, Rust, Go, native GUI) into `agent-general-worker` without dedicated skills — either synthesize a dedicated role (e.g. `agent-native-desktop` with native compilers and toolchains) or explicitly map the native directories to Red Zone with a mandatory Handoff trigger.
* **Mandatory Dispatcher Rule (No Multi-Role Chaos):** when deploying 3 or more specialized roles, an intake dispatcher (`agent-intake-dispatcher` or integrated in `agent-dev-lead`) is strictly mandatory. Forbid leaving the user with an unrouted bundle of multiple isolated roles without a single point of entry.

#### 3. Open Competency Catalog (Role Matrix)
> [!IMPORTANT]
> The catalog below is an **Open Competency Catalog**, not an immutable closed enum. Agents have full autonomy to synthesize combined roles or invent custom, domain-specific roles (e.g., `agent-solana-smart-contracts`, `agent-3d-designer`, `agent-data-pipeline`) matching the repository's real architecture.

| Role | Deploy when (repository markers) | Stack skills | Red-zone defaults (never autonomous) |
| :--- | :--- | :--- | :--- |
| `agent-general-worker` | Any project — fundamental default role | `clean-code`, `code-reviewer`, `systematic-debugging` + stack skills | destructive actions; specialized domain modules without Handoff |
| `agent-intake-dispatcher` | Any project with 2+ roles | `prompt-engineering`, `brainstorming` | direct modification of project source code |
| `agent-meta-guardian` | `meta-project` structure present | `clean-code`, `code-reviewer` | deleting the knowledge base without user confirmation |
| `agent-consensus-judge` | Contested decisions | `architecture-decision-records`, `api-design-principles` | decisions without empirical evidence |
| `agent-deep-auditor` | Mature codebase (>500 lines) | `systematic-debugging`, `code-reviewer` | destructive refactorings, autonomous file deletion |
| `agent-product-roaster` | Product services, startups | `search_web`, `brainstorming` | changing business logic without specifications |
| `agent-creative-director` | UI components, web, themes, icons | `image-generator`, `css-tokens` | replacing brand identity without confirmation |
| `agent-ui-ux-engineer` | Frontend, bots, interfaces | `semantic-html`, `css-coder`, `ui-ux-invariants` | removing confirmations for destructive actions |
| `agent-legal-compliance` | `LICENSE`, public repositories | SPDX/SBOM audit | changing repository license |
| `agent-github-steward` | `.github/`, git remotes | `github-management`, `github-actions-creator`, `clean-code`, `code-reviewer` | force-push, merging into main without review, modifying secrets |
| `agent-devops-infra` | `Dockerfile`, `docker-compose*` | `docker-expert`, `kubernetes-architect`, `bash-linux` | production secrets, `.env*`, production k8s |
| `agent-qa-engineer` | `tests/`, testing frameworks | `test-driven-development`, `verification-before-completion` | disabling tests in CI |
| `agent-tech-writer` | `docs/`, OpenAPI, Swagger | `mermaid-diagram-specialist`, `openapi`, `google-style-guide` | publishing unreviewed docs |

#### 4. Execution Routing (delegation cost control)
When a step can be delegated or mechanized, choose the route by task CERTAINTY — task CERTAINTY beats task size (a Red-Zone policy one-liner goes to a strong model; a batch of identical spec'd edits goes to a cheap executor):

| Route | Use when | Examples |
| :--- | :--- | :--- |
| **script** | Fully deterministic, mechanizable check or change | hashing, sync, link-check, template rollout |
| **cheap subagent** | Identical spec'd edits; a small patch by exact spec; a draft test for a known repro (requires ready tests-as-spec) | batch rename by table, exact-diff apply |
| **regular executor** | Local implementation with a clear contract | feature/script work inside an agreed boundary |
| **strong model + independent verification** | Architecture, migrations, authority boundaries, ambiguous defects, Red-Zone policy one-liners | schema migration, boundary policy text, unclear defect |

A delegation plan records: responsible role / execution route / model tier / change scope / acceptance criterion / verifier.

---

### Phase 3: Existing Team Audit (Role Pruning & Garbage Collection)

If the `.agents/team/` directory already exists:
1. Scan all folders: `.agents/team/*/profile.md`; inspect the `Role Call Log (Telemetry)` table in each profile.
2. Identify inactive or obsolete roles: initialized but the log contains only the initial entry and no real invocations for >30 days; or the role's underlying technology stack was completely removed from the project (e.g., Docker decommissioned but `agent-devops-infra` remained).
3. Compile a candidate list for archiving into `.agents/team/archive/<role_id>/`.
4. *Prohibition on autonomous deletion:* archiving roles requires explicit user approval!

---

### Phase 4: Dialogue and User Alignment

Generate a concise advisory proposal for the user:
1. **Detected markers:** list identified project components.
2. **Recommended roles to enable:** baseline worker (`agent-general-worker`) + 2–4 most critical domain roles matching detected markers, with brief rationales.
3. **Archive candidates:** (if inactive roles exist).
4. **Approval prompt:** "Approve provisioning the following roles: [...]?"

---

### Phase 5: Materializing Profiles and Registering in Passport

After user confirmation, perform the production write (READ each resource exactly when its step needs it):
1. Create directories for each approved role: `.agents/team/<role_id>/`.
2. Register the role in `.agents/meta-project.json` under `roles.<role_id>`:
   - Configure bindings for the project's active installation (`<active_installation_id>` with `status: "active"` and `profile_path: ".agents/team/<role_id>/profile.md"`).
   - Configure planned bindings only for installations the user EXPLICITLY plans (`<planned_installation_id>` with `status: "planned"` and `profile_path: null`); omit when none exist. (`ag-main`/`zc-main` are Meta-Project's own values — examples, never defaults.)
   - Derive the qualified `actor_id`: `<project_id>::<harness_id>::<installation_id>::<role_id>`.
3. Scaffold `.agents/team/<role_id>/profile.md` from `resources/profile.md`: fill in role ID, qualified `actor_id`, `harness_id`, `installation_id`, scope, invocation triggers, active skills, and boundary permissions; initialize the telemetry table with current date, initiator, and `actor_id`.
4. Create a private notes file `.agents/team/<role_id>/notes.md` from `resources/notes.md`.
5. Generate the initial ready-to-use system prompt `.agents/team/<role_id>/system_prompt.md` from `resources/system_prompt.md`: populate role name, purpose, qualified `actor_id`, skill stack, and canonical boundary permissions (Green / Yellow / Red).
6. Register roles in root `AGENTS.md` under the team section (Section 7 in the canonical passport):
   ```markdown
   ## 7. Project Agent Team & Multi-Harness Identity (.agents/team/)
   - [{{ROLE_TITLE}}](.agents/team/{{ROLE_ID}}/profile.md) — {{ROLE_SCOPE}} (invoked by: {{ROLE_TRIGGERS}})
   ```
7. Generate and display a compact Quick-Launch Table without spamming long prompt text in chat:
   ```markdown
   | Role | Purpose | System Prompt Path | Trigger Keywords |
   | :--- | :--- | :--- | :--- |
   | `agent-qa-engineer` | Testing & verification | `.agents/team/agent-qa-engineer/system_prompt.md` | "run tests", "verify validation" |
   ```

---

### Phase 6: Manage Report Generation and Hand-off Verification

Upon applying changes, record the management execution report:
1. **Generate the manage report via the stdlib engine:**
   ```bash
   python scripts/manage_reports.py manage --action create \
     --installation <installation_id> --harness <harness_id> --actor <actor_id> \
     --init-run-id <init_run_id> \
     --status <completed|awaiting_decision|blocked> \
     --data-file <data_json>
   ```
   The report is written atomically to `.agents/reports/<installation_id>/manage/<manage_run_id>.json`, linking back to the source `init_run_id`.
2. **Crucial Invariants of Team Orchestration:**
   - **Profile Created ≠ Loaded by Harness:** creating `.agents/team/<role_id>/profile.md` does not mean the role is actively loaded or recognized by the agent harness.
   - **Execution Role Invariance:** completing `manage-meta-project` does NOT change the active agent's qualified identity (`actor_id`). The agent retains its qualified role identity and reports completion without mutating its session identity.
   - Output a concise human summary displaying preserved, created, and adapted roles, bindings, and remaining manual steps.

---

### Phase 7: Team Rules of Engagement

Every team agent must adhere to core coordination principles:
1. **User-Driven Passivity:** agents never perform background actions without explicit user instructions.
2. **User-Mediated Handoffs:** agents do not transfer control directly to each other, bypassing the user. If a task requires another domain, the agent returns a structured handoff package to the user:
   ```markdown
   ### Task Handoff Package: [agent-A -> agent-B]
   - Original goal: <what was requested>
   - Completed work: <list of touched files>
   - Handoff boundary: <why current agent stopped>
   - Recommended prompt for agent-B: <ready-to-use prompt text>
   ```
   When handing off mid-task, first record a `- [>]` checkpoint per tasks.md rules, so the receiving agent resumes from the last `- [>]` entry instead of re-reading the whole slice.
3. **Handoff Chain Limit:** maximum of 2 consecutive handoffs (hops = 2). On the 3rd step, control must return to the human.
4. **Shared File Annotations Sanctity:** prohibited from editing others' notes in `tasks.md`, `observations.md`, and `architecture.md`; only own status/tasks can be updated. On conflict, present a clear description of the trade-off to the user.
5. **Exclusive Ownership of `notes.md`:** only the role owner can modify `.agents/team/<role>/notes.md`.
6. **Evidence-Based Arbitration:** `agent-consensus-judge` issues verdicts strictly based on test results, benchmark measurements, and official specifications.
7. **Parallel Subagent Delegation (`dispatching-parallel-agents`):** when handling repetitive, parallel, or multi-directory batch workloads (e.g. multi-file migrations, generating multiple test suites, bulk linting/audits), primary agents MUST invoke the skill `dispatching-parallel-agents`. The parent agent partitions the workload across transient subagents by non-overlapping directories or tasks, passing down project invariants from `AGENTS.md` and role boundaries. The parent coordinates completion, runs integrity tests, and returns a consolidated report to the user without burdening the human with intermediate subagent handoffs.
8. **Circuit Breaker & Flow Tracing:** repeatedly failing edits targeting one defect, or new regressions appearing in adjacent code while fixing it, are a semantic signal to STOP editing and trace the full data path (origin ➔ transport ➔ persistence ➔ consumer) before the next diff. A normal iterative TDD loop is NOT a violation.
9. **Target State Audit:** before modifying non-trivial modules in Yellow or Red zones, determine: Single Source of Truth (authoritative state owner), Persistence & Cache (disk, memory, `%APPDATA%`, registry and invalidation), and Contract Duplication (mirrored logic across languages/clients).
10. **Toolchain Handoff Tripwire:** if an error originates from an unspecialized toolchain (e.g., MinGW, cross-compiler, WiX, Docker, kernel build pipelines) belonging to another role, do not attempt heuristic patching — immediately emit a structured Handoff Packet.
