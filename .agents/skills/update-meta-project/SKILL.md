---
name: update-meta-project
license: MIT
compatibility: "Requires Python 3.10+ and Git"
description: >-
  Safely update an installed meta-project framework (skills, templates, scripts)
  by driving the managed update engine scripts/update_meta_project.py:
  read-only check and plan, user alignment, atomic journaled apply, and
  restore; preserves local_repo_data adaptations and knowledge slices.
---

# Managed Framework Updater (Update Meta-Project)

Autonomous non-destructive skill for updating procedural skills, runtime scripts, and canonical templates in a target repository, driven by the SHA-pinned, atomic, crash-recoverable engine `scripts/update_meta_project.py` (subcommands: `check | plan | apply | restore`).

**Division of responsibility:** the engine owns resolution, merging, atomic replacement, the journal, the framework lock, and update reports. The AGENT owns context inspection, conflict/protected/needs-base analysis, user alignment, and post-update verification. The agent never copies framework files manually.

---

## When to invoke skill

* The user requests: "update-meta-project", "update meta project", "обнови обвязку", "накати апдейты meta-project", "pull upstream skills", "upgrade meta framework".
* New skills, templates, or bugfixes were released in the official `meta-project` repository.
* Upstream utilities (`check_boundaries.py`, `compact_memory.py`) have received security or feature improvements.

---

## Execution steps

### Phase 1: Pre-Flight — Context Inspection & Availability Check

1. **Inspect the execution context:**
   - `python scripts/inspect_meta_project.py --json --harness <harness_id> --installation <installation_id>` — diagnose repository state and schema compatibility. If the repository is in `INCOMPATIBLE_SCHEMA`, halt and report the required schema migration to the user.
   - `python scripts/verify_identity.py --check` — validate the registry and role bindings; resolve the qualified `actor_id` (`<project_id>::<harness_id>::<installation_id>::<role_id>`) required by `apply` later.
   - `git status -s` — ensure there are no uncommitted collisions in the target update areas.
2. **Read the framework lock** (`.agents/framework-lock.json`) if present: it records the actually-installed component subset, the pinned source/ref/commit SHA, the channel, and a sha256 per component. No lock means this installation was not produced by the managed engine — surface that to the user before proceeding.
3. **Check available updates THROUGH THE ENGINE** (strictly read-only, zero writes):
   ```bash
   python scripts/update_meta_project.py check --source <upstream_repo_path_or_url> --ref <ref>
   ```
   - `--ref` is a branch, tag, or full commit SHA (default `main`); the engine pins ONE exact SHA and materializes every component from that single revision (never mixing revisions).
   - State the source and its constraints to the user explicitly: a local repository path works directly; a remote URL requires a prior local `git fetch`.
   - Cache resolution (used when the origin is unavailable): `--cache-dir` flag > `META_PROJECT_CACHE` env var > OS cache path (`%LOCALAPPDATA%/meta-project/cache` on Windows, `~/.cache/meta-project` on POSIX). Only INTEGRAL single-revision bundles are accepted; a partial cache is refused. Every cache load is VERIFIED (see Background Notes: cache trust anchor): tampered cache bytes are refused with the origin reachable AND offline, and a cache bundle sealed for a different commit is refused.
   - Interpret the JSON output: `updates_available`, `drifted` (installed files differing from the lock record — local edits of unknown origin), `lock_present`, `diagnostics`, and `check_status` (`"ok"` when the check itself ran to completion — even when it then finds updates; `"failed"` when the check could not answer, with the reason on stderr). Exit code contract is unchanged: 1 means updates or drift were found (or the check failed — read `check_status` and stderr to tell which); 0 means up to date. `check` resolves its source through the SAME cache-fallback rule as `plan`/`apply` (dead origin + exact-SHA ref + verified cache answers identically in all three modes). EOL note (Windows autocrlf): a working copy smudged to CRLF on a fresh checkout is NOT drift — a line-endings-only difference from the lock record yields the `eol_normalized` diagnostic and up-to-date semantics (plan fast-forwards the copy back to pinned LF bytes); cache-manifest and origin cross-verification remain byte-exact.
4. **Framework repository itself:** when running inside the core `meta-project` repository, self-overwrite via the engine is PROHIBITED — the framework evolves through ordinary Git edits in its own repository. The update engine targets OTHER repositories with installed frameworks.

---

### Phase 2: Plan & User Alignment (read-only)

1. **Build the three-way merge plan:**
   ```bash
   python scripts/update_meta_project.py plan --source <...> --ref <...> [--repo-root .] [--components path1,path2] [--protect pattern1,pattern2] [--full]
   ```
   Zero-write. Default scope is the full component set declared in the source's `templates/release_manifest.json` (read at the source root `manifest.json` with fallback to `templates/release_manifest.json`); `--components` narrows it (dependencies from `requires` stay with their dependents); `--protect` patterns are ADDITIONAL — they unite with the engine defaults, never replace them.
   - **Summary mode (default):** the plan JSON prints ops/paths/sha256 per action WITHOUT content blobs; `--full` restores the machine-consumable form (actions carry `content_b64`). A summary plan file saved from stdout is still executable: `apply --plan-file` re-materializes content from the plan's pinned source and hash-verifies it against `upstream_sha256`.
2. **Analyze the plan — this analysis is the agent's job** (the engine only refuses; the agent explains):
   - `conflicts` — unmarked local modifications (outside `local_repo_data` markers) that the engine refuses to overwrite. Present each conflict (path + nature of the local change) to the user. HOW to resolve — add `local_repo_data` markers, merge manually, or skip the component — is the USER's decision; the agent never picks silently. Exception: an unknown-origin local file whose bytes hash-EQUAL the pinned upstream content can be ADOPTED with the user's consent (`--adopt-existing`, Phase 3) — the lock records it without rewriting the file.
   - `protected` — components matching protect patterns (boundary gate, workflows, integrity tests, LICENSE, ...) OR classified Red by the project's own `.agents/project_knowledge/boundaries.md` (union: engine defaults ∪ `--protect` patterns ∪ project Red zone). Automated changes are refused: the Red-Zone sanction is an explicitly OPEN item, no sanctioned mechanism exists. Only the manual, user-confirmed path is available for these.
   - `needs_base` — locally modified components with no merge base matching the lock-recorded sha256 in the cache or in the origin's history (the engine fetches base candidates from the origin at the lock-recorded source commit, hash-verified). If both are unavailable (dead origin, no cache), either supply an integral cache containing the base content, or the user explicitly gives up "safe merge" for those components (manual decision). The engine never claims a safe merge without a base.
3. **Agreement gate:** do NOT proceed to Phase 3 without the user's explicit approval of the plan (scope + per-conflict resolutions). Save the approved plan with `--full` and pass it to `apply --plan-file <path>`; the apply then records `approved_plan_file` in the update report and re-verifies the plan's pin against the source (a stale plan is refused with zero writes).

---

### Phase 3: Apply (engine-driven, atomic)

1. **After user approval:**
   ```bash
   python scripts/update_meta_project.py apply --source <...> --ref <...> --repo-root . --installation <installation_id> --actor <qualified_actor_id> [--cache-dir ...] [--components ...] [--plan-file <approved_plan.json>] [--adopt-existing]
   ```
   **Identity admission (perimeter):** `apply` is a write operation — the `--actor` must be ADMITTED by the project registry (`.agents/meta-project.json`): registered AND on an `active` binding of an `active` installation. An unadmitted actor is refused with `[APPLY-REFUSED] ... actor not admitted` and ZERO writes (not even a journal). Identity-less bootstrap projects (no registry) are not blocked. Resolve the qualified actor via `python scripts/verify_identity.py --actor <actor_id>` before applying.
   **Offline apply (D2):** when the origin is unreachable but `--ref` is an EXACT 40-hex commit SHA and an integral cache bundle exists for it, apply proceeds with the lock declaring `origin: cache`. Refs that are NOT exact SHAs (branches/tags) still require a resolvable origin — pin the SHA first while the origin is reachable.
2. **What the engine guarantees** (no manual steps by the agent):
   - per-file atomic replacement from the single pinned SHA;
   - `local_repo_data`-marked blocks re-injected into merged files (marker syntax chosen per file extension — see Background Notes);
   - operation journal `.agents/update-journal.json` (before/after sha256 per file);
   - framework lock `.agents/framework-lock.json` rewritten atomically — component entries carry `base_sha256`/`installed_sha256` and the resolved `target` destination (runtime layout);
   - an `update` report recorded through the canonical report engine (`scripts/manage_reports.py`), including `approved_plan_file` when a plan file was used;
   - `--adopt-existing`: sha-equal unknown-origin local files become `adopt` actions — the lock records them WITHOUT rewriting the file bytes.
3. **Refusal handling:** `[APPLY-REFUSED]` for an unadmitted actor, protected components, unresolved conflicts, or needs_base entries means ZERO writes were performed. `[ENGINE-BUSY]` (`ENGINE_BUSY`) means another `apply`/`restore` is already running on this repository (the engine serializes mutating operations with `.agents/update-engine.lock`) — wait for it to finish and retry; the refused operation wrote nothing. Refusals also cover containment violations (a manifest, plan file, lock or journal path that is absolute, carries `..` segments, or resolves through a symlink OUTSIDE the repository — the engine never writes a single byte outside the repo root). Return to Phase 2, resolve with the user, retry or abandon.

---

### Phase 4: Verification & Recovery

1. **Boundary gate audit:**
   ```bash
   python scripts/check_boundaries.py --boundaries .agents/project_knowledge/boundaries.md --audit-tree
   ```
   100% boundary mapping coverage is required.
2. **Repository test suite:**
   ```bash
   python -m unittest discover -s tests -p "test_*.py"
   ```
3. **Identity re-check:** `python scripts/verify_identity.py --check` — the update must not have altered role bindings or `.agents/meta-project.json`.
4. **Interrupted or failed operation:** restore ONLY the engine's own changes from the last operation journal:
   ```bash
   python scripts/update_meta_project.py restore --repo-root .
   ```
   Third-party edits made during the apply window are kept and reported, never clobbered. A successful restore also marks the affected update report `status: "restored"` (in place — the audit trail shows the update was rolled back). Restore holds the same engine lock as apply: concurrent operations are refused with `[ENGINE-BUSY]`.
5. **Status truthfulness (session reload):** an updated runtime file on disk is NOT the version loaded by the harness in the current session. After `apply`, instruct the user to reload/restart the session so the new skills/passport take effect, and do not claim updated behavior is already active.
6. **Deliver the update summary to the user:** pinned source + commit SHA, affected components, preserved adaptations (merged `local_repo_data` blocks), remaining manual actions (protected/conflicted components), journal and report locations.

---

## Background Notes (user-facing explanation only)

* **`local_repo_data` mechanism (implemented inside the engine; the agent does not harvest manually):** local adaptations wrapped in markers are preserved through updates. BOTH marker forms are supported, selected by file extension:

  | Extensions | Marker form | Example |
  |---|---|---|
  | `.md`, `.html`, `.xml` | HTML comment | `<!-- local_repo_data:TAG -->` ... `<!-- /local_repo_data:TAG -->` |
  | `.py`, `.toml`, `.yml`, `.yaml`, `.sh`, `.cfg`, `.ini` | hash comment | `# local_repo_data:TAG` ... `# /local_repo_data:TAG` |
  | anything else | both accepted | either form in the same file |

  The engine re-injects these blocks into merged content (also on fast-forward updates of marker-bearing files); any local change OUTSIDE such markers is an explicit conflict requiring a user decision.
* **Runtime layout:** when the target repository has `.agents/skills/`, a `skills/<name>/SKILL.md` component is written to `.agents/skills/<name>/SKILL.md` (no duplicate at the distribution path). A lock-recorded `target` stays authoritative: a component already installed at `skills/<name>/SKILL.md` keeps that destination even if `.agents/skills/` appears later — no silent migration, no duplication. Repositories without `.agents/skills/` use the distribution path as-is.
* **Cache trust anchor (`cache-manifest.json`, security):** each integral cache entry carries `<sha>/cache-manifest.json` — the commit sha it was sealed for and the sha256 of every bundle file. On every load the engine verifies those hashes (any mismatch = tampered/corrupted cache → refusal, online AND offline), refuses a bundle sealed for a different commit than the requested pin, and — whenever the origin still provides the pinned commit — cross-verifies component hashes against the origin blobs (a self-consistent but lying sealed cache is still refused). `apply` seals a cross-verified unsealed entry at load time, so later offline loads can verify it; a cache that cannot be verified (unsealed + dead origin) is refused, never served on trust.
* **Containment perimeter (security):** every managed path — manifest component, lock target, approved-plan target, journal path, cache-manifest component — must be relative, `..`-free, and resolve INSIDE its root. Absolute paths (`C:/...`, `/...`, `//server/share`), parent-traversing paths, and symlink/junction destinations that resolve outside the repository are refused with ZERO writes (not even a journal).
* **Update report versioning:** the report's `versions.after` carries `{framework_version, commit_sha}` — the human-readable version AND the exact pinned commit; `versions.before` stays the pre-apply commit sha. The report's `run_id` equals the journal's `run_id` (one run, one journal, one report), which is how `restore` finds and marks it `restored`.
* **Component composition:** the default update scope is the component set of `templates/release_manifest.json` at the pinned source; the project lock records the installed subset — a minimal scaffold is a valid installation.
