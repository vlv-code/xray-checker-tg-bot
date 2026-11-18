# Responsibility Zones and Change Boundaries

This document defines the agent's level of autonomy when modifying the repository using explicit file path glob patterns.

---

## Green Zone (Autonomous Modification)
Files and directories that the agent can modify autonomously to accomplish the current task:
- Creating new test files (`tests/test_*.py`, `*_test.go`, `*.test.ts`, `*.spec.ts`) to cover new features within a TDD session.
- `src/utils/**`, `src/helpers/**`, `lib/utils/**` — auxiliary local utilities without side effects.
- `docs/**`, `*.md` (except the root `AGENTS.md`) — technical documentation.
- In-code documentation (docstrings, JSDoc, function comments).

---

## Yellow Zone (Modification with Mandatory Verification)
Areas requiring elevated caution and mandatory local test and linter execution prior to delivery:
- `AGENTS.md` — root project passport (requires careful verification and opportunistic refresh).
- `tests/**` (existing regression test suite). **Test emasculation protection invariant:** It is forbidden to weaken `assert`/`expect`, comment out checks, or adjust golden data to match broken code. A test failure indicates a bug in implementation, not a need to rewrite the test. Modifying existing checks is permitted only when user requirements change explicitly.
- `src/api/**`, `src/routes/**`, `src/controllers/**` — public endpoints and routing.
- `src/services/**`, `src/domain/**` — core business logic.
- `package.json`, `pyproject.toml`, `go.mod`, `Cargo.toml`, `requirements.txt` — project dependencies.
- `src/schemas/**`, `src/models/**` — input/output validation schemas (Pydantic / Zod / DTO).
- `scripts/** (except scripts/check_boundaries.py)` — automation scripts and verification utilities.
- `*formatting*`, `*render*`, `templates/** (except boundaries.md)` — formatting modules, template engines, and markup escaping (parsing errors break message delivery).
- `keyboards.*`, `ui/**` — keyboard layouts and UI components.
- `.agents/*.json` — project registries and identity binding definitions.
- `.agents/reports/**` — initialization and team management audit reports.
- `.github/*.yml`, `.github/*.yaml` — repository-level GitHub configurations (e.g., `FUNDING.yml`).

---

## Red Zone (Prohibited Without Direct User Confirmation)
Critical areas of the project. Any modification requires mandatory prior confirmation from the user:
- Architectural integrity and external contract tests: `*integrity*`, `*contract*`, `*schema*`, E2E/snapshot tests of public APIs and integrations.
- `scripts/check_boundaries.py` — core security and boundary gate enforcement script.
- `*boundaries.md` — responsibility boundaries and security policy definitions.
- `migrations/**`, `alembic/**`, `liquibase/**` — database schema and migration files.
- `data/**`, `*.db`, `*.sqlite*` — local database files and directories.
- `.env*`, `config/secrets/**`, `*.pem`, `*.key` — configurations containing secrets and environment variables.
- `Dockerfile*`, `docker-compose*.yml`, `k8s/**`, `helm/**` — containerization and deployment infrastructure.
- `.github/workflows/**`, `.gitlab-ci.yml`, `Jenkinsfile` — CI/CD pipelines.
- `.github/CODEOWNERS` — repository code ownership and pull request approval rules.
- `src/auth/**`, `src/security/**`, `src/crypto/**` — authentication, authorization, sessions, tokens.
- `src/billing/**`, `src/payments/**` — billing and financial transactions.

---

## Default Deny (Black Zone / Unmapped Paths Invariant)
Any directory or file path not explicitly listed in Green, Yellow, or Red zones above is strictly classified as **Red Zone by Default**. Autonomous modifications to unmapped paths are prohibited. Prior to touching unmapped code, the agent MUST consult the user and perform an *Opportunistic Refresh* of `boundaries.md` to map the path to its appropriate zone.

