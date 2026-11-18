# Project Passport: {{PROJECT_NAME}}

## 1. Concept, Principles, and North Stars
- **Service Purpose:** {{PROJECT_DESCRIPTION}}
- **Technology Stack:** {{PROJECT_STACK}}
- **Key Invariant:** {{CRITICAL_INVARIANT}} (e.g.: API backward compatibility, data immutability, preserving DB schema).
- **Principles and Architectural Orientations (North Stars):**
  - {{PRINCIPLE_1}} (e.g.: dependency minimalism, avoiding unnecessary abstractions).
  - {{PRINCIPLE_2}} (e.g.: strict typing and validation at system boundaries).
  - {{PRINCIPLE_3}} (e.g.: explicit error handling without swallowing exceptions).
  - **Circuit Breaker & Flow Tracing:** repeatedly failing edits targeting one defect, or new regressions appearing in adjacent code while fixing it, are a semantic signal to STOP editing and trace the full data path (origin ➔ transport ➔ persistence ➔ consumer) before the next diff. A normal iterative TDD loop is NOT a violation.
  - **Default Deny Boundaries:** Any directory not explicitly mapped in boundaries is strictly Red Zone.
  - **Default Closed Tooling:** Core framework and agents operate in a zero-dependency closed loop by default. External tools and third-party dependencies are permitted ONLY when explicitly declared (opt-in) in project specification or requested by user. Liability for external tool failures rests on the project/operator.
  - **Adversarial Testing Invariant:** When applying TDD, write negative/adversarial tests (boundary conditions, malformed input, failure modes) and confirm RED before writing implementation code to transition to GREEN.
  *(Any decisions contradicting these principles are prohibited)*

## 2. Repository Meta-Map
- Core Code: `{{SRC_PATH}}`
- Entry Points: `{{ENTRY_POINTS}}`
- Configuration: `{{CONFIG_PATHS}}`
- Tests: `{{TEST_PATHS}}`

## 3. Directive Meta-Knowledge Protocol
Before taking any action, the agent MUST consult the corresponding knowledge slices.
**Opportunistic Knowledge Refresh:** when reading any slice, evaluate data completeness — fill in missing details, fix outdated information, and eliminate blind spots without destructive overwriting:

- **Responsibilities and Boundaries (`.agents/project_knowledge/boundaries.md`):**
  - *When to consult:* before modifying data schemas, module boundaries, dependencies, security, or configs.
  - *What to update:* supplement and refine glob path patterns to match the real repository structure.
- **Weak Spots and Risks (`.agents/project_knowledge/weak_spots.md`):**
  - *When to consult:* before modifying fragile services, external APIs, queues, or complex legacy code.
  - *What to update:* record newly identified risks, hidden couplings, and remove resolved issues.
- **Architectural Foundations (`.agents/project_knowledge/architecture.md`):**
  - *When to consult:* before adding new components, changing data flows, or modifying interaction contracts.
  - *What to update:* eliminate blind spots in the subsystem map, describe actual connections and data flows (directly or via the `sync-architecture` skill).
- **Observation Base (`.agents/project_knowledge/observations.md`):**
  - *When to consult:* upon build failures, runtime errors, or unexpected environment behavior.
  - *What to update:* immediately and atomically record solved non-trivial bugs using the `record-observation` skill.
- **Active Tasks (`.agents/project_knowledge/tasks.md`):**
  - *When to consult:* at the beginning of any session and upon completing subtasks.
  - *What to update:* track execution progress, register new tasks and delegated steps with author and timestamp `[<actor_id> @ ISO_DATETIME]`.

## 4. Procedural Skills Registry
### Built-in meta-project skills:
- `init-meta-project` — repository scaffolding and non-destructive initialization.
- `manage-meta-project` — scale-adaptive agent team orchestration and materialization.
- `decommission-meta-project` — non-destructive decommissioning, resizing, and archiving of agent roles.
- `ground-meta-project` — context recovery and drift diagnosis: restores goal, state, identity, and authority after session changes, moves, updates, or compaction; diagnoses context drift without automatic rollback.
- `scan-meta-project` — integrity inspection, boundary coverage audit, and backfilling meta-knowledge without re-init.
- `tune-meta-project` — git friction diagnostics, role topology tuning, and non-bloating North Star crystallization.
- `update-meta-project` — non-destructive upstream updating from GitHub or local cache with local_repo_data preservation.
- `record-observation` — atomic logging of findings and quirks into `observations.md`.
- `sync-architecture` — updating the component map and data flows in `architecture.md`.

### Skill Responsibility Boundaries (invariant):
| Skill | Owns | Must NOT do |
|---|---|---|
| init | survey, scenario agreement, scaffold prep | deploy teams |
| manage | roles & bindings configuration | re-agree the scenario |
| update | framework components & migrations | revise team composition or North Stars |
| ground | recover understanding of goal/state/authority | change configuration to silence a discrepancy |
| tune | propose config improvements with evidence | apply them directly (routes via manage/update/decommission) |

### Connected stack skills from environment:
{{STACK_SKILLS}}
*(Managing build, linting, testing, and framework specifics is delegated to these skills)*

## 5. Local Verification Tooling & Autonomous Fallback
If global agent skills (such as `python-pro` or Meta-Project skills) are unavailable in the host harness, the agent operates in **Base Fallback Mode** using zero-dependency Python 3.10+ stdlib utilities:
- **Repository Inspection & Capability Audit:**
  `python scripts/inspect_meta_project.py --json`
- **Role Identity & Binding Verification:**
  `python scripts/verify_identity.py --check`
- **Boundary Gate & Responsibility Audit:**
  `python scripts/check_boundaries.py --boundaries .agents/project_knowledge/boundaries.md --audit-tree`
- **Working Tree Diff Verification:**
  `python scripts/check_boundaries.py --boundaries .agents/project_knowledge/boundaries.md --diff HEAD --allow-red-zone`
- **Memory Compaction Check:**
  `python scripts/compact_memory.py --dry-run`
- **Atomic Observation Logging:**
  `python scripts/record_obs.py --category <cat> --path <path> --issue <text> --solution <text>`
- **Initialization & Management Report Audit:**
  `python scripts/manage_reports.py [init|manage|latest|verify]`

### Read-Only Exploration Mode
When tasked with code exploration, repository assessment, or architecture review without explicit instruction to implement changes:
- Do NOT generate temporary files, `.lock` files, or intermediate scratchpads.
- Do NOT add telemetry rows to role profiles or tasks to `tasks.md`.
- Use `python scripts/inspect_meta_project.py --read-only --json` for safe discovery.

## 6. Base Working Protocol
1. Verify against Section 1: ensure the planned solution does not contradict project principles and invariants.
2. Consult the target slice from Section 3; update the slice immediately upon discovering gaps or inaccuracies.
3. Apply stack skills from Section 4 (or local stdlib tools from Section 5 in Base Fallback Mode) to inspect code and run tests.
4. Changes must be minimally sufficient; refactoring of unrelated areas is prohibited; changes must be minimal in diff width, but maximal in root-cause depth.
5. When encountering hidden environment quirks, record the fact via `record-observation` (or `scripts/record_obs.py`).

**Execution Routes** (mode chosen by consequences and uncertainty; checks mandated by the agreed scenario are always preserved):
- **short** — unambiguous local task: action → needed check → result (Read-Only Exploration Mode above is the canonical short route).
- **normal** — several related changes with known contracts.
- **extended** — migrations, ambiguous defects, architecture, protected components.

## 7. Project Agent Team & Multi-Harness Identity
Project roles and multi-harness bindings are authoritative in `.agents/meta-project.json`.
- **Identity Determination Protocol:** Every agent operating in the repository must resolve its qualified `actor_id` (`<project_id>::<harness_id>::<installation_id>::<role_id>`) from `.agents/meta-project.json` using `scripts/verify_identity.py`.
- **Invariance of Identity:** Reading another role's passport (`profile.md`) or private workspace (`notes.md`) does NOT alter the active actor's identity. If identity cannot be verified, the agent must fail closed and report an unresolved identity.
- **Active Team Roster:**
{{TEAM_ROSTER}}

## 8. Dual-Entry & Canonical Repository Badge
For shared discovery across human operators and external agent harnesses, `README.md` incorporates the canonical block:
```markdown
<!-- META-PROJECT:ENTRY:BEGIN -->
[![Built with Meta-Project](https://img.shields.io/badge/Built%20with-Meta--Project-blueviolet)](https://github.com/vlv-code/meta-project)

Agent instructions: [AGENTS.md](AGENTS.md) | Policy: [.agents/project_knowledge/boundaries.md](.agents/project_knowledge/boundaries.md)
*To inspect, configure, or adapt your environment, run `python scripts/inspect_meta_project.py` or invoke the `init-meta-project` skill.*
<!-- META-PROJECT:ENTRY:END -->
```
