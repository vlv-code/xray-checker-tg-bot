#!/usr/bin/env python3
"""
scripts/inspect_meta_project.py

Deterministic repository inspection utility for Meta-Project (Zero External Dependencies, Python 3.10+ stdlib).
Decouples repository survey from modification:
- Classifies repository state into 8 canonical states (NONE, FOREIGN_RULES, HEALTHY_REGISTERED,
  HEALTHY_NEW_HARNESS, CONTEXT_UNRESOLVED, LEGACY_UNREGISTERED, PARTIAL_CORRUPTED, INCOMPATIBLE_SCHEMA).
- Evaluates 5-dimension Capability Matrix (read_instructions, local_checks, skills, roles, hooks).
- Detects git worktrees, subproject roots, and foreign agent instructions.
- Enforces strict Survey-First Zero-Write Invariant: produces zero disk modifications,
  zero lock files, zero telemetry rows, and executes zero untrusted hooks.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

try:
    from scripts.verify_identity import validate_identity_registry
except ImportError:  # direct script execution: python scripts/inspect_meta_project.py
    try:
        from verify_identity import validate_identity_registry
    except ImportError:
        validate_identity_registry = None

try:  # shared helpers (ADR #22): package-style import (tests: from
    # scripts.inspect_meta_project import ...)
    from scripts._common import _error_envelope
except ImportError:  # direct script execution: python scripts/inspect_meta_project.py
    from _common import _error_envelope


def _registry_validation_errors(repo_root: Path) -> List[str]:
    """Returns registry validity errors, or [] when the validator is
    unavailable/external failure. HEALTHY states require an empty list."""
    if validate_identity_registry is None:
        return []
    try:
        is_valid, errors = validate_identity_registry(repo_root)
        return [] if is_valid else list(errors)
    except Exception as exc:  # noqa: BLE001 - defensive: no tracebacks
        return [f"registry validation failed unexpectedly: {exc}"]

SUPPORTED_SCHEMA_VERSIONS = {"1.0.0", "1.1.0", "1.0", "1.1", "1"}


# Wave G Lane I (facts digest): version of the digest payload contract.
FACTS_DIGEST_VERSION = "1"

# Knowledge slices fingerprinted by --facts-digest (registry included);
# a slice that is absent hashes as null, never as an empty-file hash.
FACTS_DIGEST_SLICES = (
    ".agents/project_knowledge/architecture.md",
    ".agents/project_knowledge/boundaries.md",
    ".agents/project_knowledge/weak_spots.md",
    ".agents/project_knowledge/observations.md",
    ".agents/project_knowledge/tasks.md",
    ".agents/meta-project.json",
)


def _sha256_file(path: Path) -> Optional[str]:
    """sha256 hex digest of a file's raw bytes, or None when the file is
    absent or unreadable (the digest must never raise)."""
    if not path.is_file():
        return None
    try:
        digest = hashlib.sha256()
        with path.open("rb") as fh:
            for chunk in iter(lambda: fh.read(65536), b""):
                digest.update(chunk)
        return digest.hexdigest()
    except Exception:
        return None


def compute_facts_digest(repo_root: Path, report: Dict[str, Any]) -> Dict[str, Any]:
    """
    Build the Wave G Lane I facts digest from a finished inspection report
    (Wave G plan §6.1). A compact, deterministic fingerprint of the
    REPOSITORY — not the runtime: an agent stashes the serialized payload
    and later diffs it cheaply instead of re-reading every slice; a digest
    mismatch names exactly which file to re-read (ground-meta-project).

    Contract:
    - facts_digest_version: FACTS_DIGEST_VERSION (payload schema version);
    - state: the classified canonical repository state;
    - slices: per-file sha256 for the 5 knowledge slices + the registry
      (absent slice -> null);
    - capabilities: capability matrix as per-dimension status strings;
    - harness/installation: echo of the runtime context flags (null when
      not provided — the digest is still emitted, with state
      CONTEXT_UNRESOLVED for an otherwise healthy repository).

    Byte-stability: callers must serialize with json.dumps(..., sort_keys=True,
    separators=(",", ":")) — fixed separators and key order make the output
    byte-identical for identical inputs. Read-only: hashing reads bytes and
    writes nothing (no disk cache — the stash lives with the agent).
    """
    repo_root = Path(repo_root).resolve()
    slices: Dict[str, Optional[str]] = {}
    for rel in FACTS_DIGEST_SLICES:
        slices[rel] = _sha256_file(repo_root / rel)
    capabilities = {
        cap: info.get("status")
        for cap, info in sorted(report["capabilities"].items())
    }
    runtime_context = report.get("runtime_context", {})
    return {
        "facts_digest_version": FACTS_DIGEST_VERSION,
        "state": report["state"],
        "slices": slices,
        "capabilities": capabilities,
        "harness": runtime_context.get("target_harness"),
        "installation": runtime_context.get("target_installation"),
    }


CANONICAL_ACTIONS = {
    "NONE": "scaffold_clean",
    "FOREIGN_RULES": "preserve_and_merge",
    "HEALTHY_REGISTERED": "pass_through",
    "HEALTHY_NEW_HARNESS": "bind_harness",
    "CONTEXT_UNRESOLVED": "provide_runtime_context",
    "LEGACY_UNREGISTERED": "adopt_legacy",
    "PARTIAL_CORRUPTED": "diagnose_and_repair",
    "INCOMPATIBLE_SCHEMA": "read_only_fallback",
}

STATE_DESCRIPTIONS = {
    "NONE": "No agent scaffolding or instructions found.",
    "FOREIGN_RULES": "Foreign instructions found without Meta-Project framework.",
    "HEALTHY_REGISTERED": "Meta-Project intact and current harness installation is registered and active.",
    "HEALTHY_NEW_HARNESS": "Meta-Project intact, but the active harness/installation is not registered or planned.",
    "CONTEXT_UNRESOLVED": (
        "Meta-Project structure is healthy, but no runtime context "
        "(--harness/--installation) was provided: registered status for the "
        "caller cannot be determined without silently adopting an arbitrary "
        "active installation (audit §4)."
    ),
    "LEGACY_UNREGISTERED": "Legacy Meta-Project structure detected without multi-harness registry.",
    "PARTIAL_CORRUPTED": "Meta-Project structure is incomplete, corrupted, or missing critical slices.",
    "INCOMPATIBLE_SCHEMA": "Registry schema version is incompatible with current inspector tooling.",
}

FOREIGN_RULE_FILES = [
    "CLAUDE.md",
    ".cursorrules",
    ".cursor/rules",
    ".windsurfrules",
    ".copilot-instructions.md",
]


def detect_git_worktree(repo_root: Path) -> Tuple[bool, Optional[Path]]:
    """
    Check if repo_root is a git worktree or git repository without invoking git CLI.
    """
    dot_git = repo_root / ".git"
    if dot_git.is_file():
        # A git worktree contains a .git file with 'gitdir: <path>'
        try:
            content = dot_git.read_text(encoding="utf-8").strip()
            if content.startswith("gitdir:"):
                gitdir_str = content[7:].strip()
                gitdir_path = Path(gitdir_str)
                if not gitdir_path.is_absolute():
                    gitdir_path = (repo_root / gitdir_path).resolve()
                return True, gitdir_path
        except Exception:
            return True, None
    elif dot_git.is_dir():
        return False, dot_git.resolve()
    return False, None


def is_metaproject_passport(path: Path) -> bool:
    """Check if an AGENTS.md file belongs to Meta-Project."""
    if not path.is_file():
        return False
    try:
        content = path.read_text(encoding="utf-8")
        has_title = "# Project Passport" in content or "Project Passport:" in content
        has_sections = (
            "## 1. Concept" in content
            or "North Stars" in content
            or "Meta-Project" in content
            or "Directive Meta-Knowledge Protocol" in content
        )
        return has_title and has_sections
    except Exception:
        return False


def inspect_repository(
    repo_root: Path,
    target_harness: Optional[str] = None,
    target_installation: Optional[str] = None,
) -> Dict[str, Any]:
    """Inspect repository and return structured diagnostic state."""
    repo_root = repo_root.resolve()
    is_worktree, gitdir = detect_git_worktree(repo_root)

    # Worktree directory resolution (audit §4): for a linked worktree,
    # `gitdir` points into <main>/.git/worktrees/<name>; resolve the main
    # working tree root and the shared hooks directory correctly.
    main_root = repo_root
    common_hooks_dir = gitdir / "hooks" if gitdir and not is_worktree else None
    if is_worktree and gitdir is not None:
        dot_git_dir = gitdir.parent.parent  # <main>/.git
        if dot_git_dir.name == ".git":
            main_root = dot_git_dir.parent
            common_hooks_dir = dot_git_dir / "hooks"

    # 1. Foreign rules detection
    foreign_instructions = []
    for f in FOREIGN_RULE_FILES:
        target = repo_root / f
        if target.exists():
            foreign_instructions.append(f)

    agents_md = repo_root / "AGENTS.md"
    has_agents_md = agents_md.is_file()
    is_meta_agents = is_metaproject_passport(agents_md) if has_agents_md else False

    if has_agents_md and not is_meta_agents:
        foreign_instructions.append("AGENTS.md")

    # 2. Check Meta-Project components
    agents_dir = repo_root / ".agents"
    pk_dir = agents_dir / "project_knowledge"
    registry_file = agents_dir / "meta-project.json"

    boundaries_file = pk_dir / "boundaries.md"
    tasks_file = pk_dir / "tasks.md"
    obs_file = pk_dir / "observations.md"

    scripts_dir = repo_root / "scripts"
    check_boundaries_script = scripts_dir / "check_boundaries.py"
    verify_identity_script = scripts_dir / "verify_identity.py"

    missing_components = []
    conflicts = []

    # 3. Read registry if present
    registry_data: Optional[Dict[str, Any]] = None
    schema_version: Optional[str] = None
    project_id: Optional[str] = None
    installations: Dict[str, Any] = {}
    roles: Dict[str, Any] = {}
    scenario: Optional[Dict[str, Any]] = None
    registry_corrupted = False

    if registry_file.is_file():
        try:
            raw_text = registry_file.read_text(encoding="utf-8")
            registry_data = json.loads(raw_text)
            if not isinstance(registry_data, dict):
                registry_corrupted = True
            else:
                schema_version = str(registry_data.get("schema_version", ""))
                project_id = registry_data.get("project_id")
                installations = registry_data.get("installations", {})
                roles = registry_data.get("roles", {})
                scenario = registry_data.get("scenario")
        except Exception:
            registry_corrupted = True

    # Registry validity per the canonical identity validator (audit §4):
    # HEALTHY states require a valid registry, not merely a parsable one.
    registry_errors: List[str] = []
    if registry_file.is_file() and not registry_corrupted and registry_data is not None:
        registry_errors = _registry_validation_errors(repo_root)

    # 4. State classification
    state: str
    if not has_agents_md and not agents_dir.exists() and not foreign_instructions:
        state = "NONE"
    elif foreign_instructions and not is_meta_agents and not agents_dir.exists():
        state = "FOREIGN_RULES"
    elif is_meta_agents or pk_dir.exists() or registry_file.exists():
        # Candidate for Meta-Project
        if registry_corrupted:
            state = "PARTIAL_CORRUPTED"
            missing_components.append(".agents/meta-project.json (malformed JSON)")
        elif not registry_file.is_file():
            state = "LEGACY_UNREGISTERED"
            missing_components.append(".agents/meta-project.json")
        elif schema_version and schema_version.split(".")[0] not in [v.split(".")[0] for v in SUPPORTED_SCHEMA_VERSIONS]:
            state = "INCOMPATIBLE_SCHEMA"
            conflicts.append(f"Unsupported schema_version: {schema_version}")
        elif not boundaries_file.is_file():
            state = "PARTIAL_CORRUPTED"
            missing_components.append(".agents/project_knowledge/boundaries.md")
        elif not (has_agents_md and is_meta_agents):
            # Mandatory component: the project passport (audit §4: absence of
            # mandatory components must never yield a HEALTHY state).
            state = "PARTIAL_CORRUPTED"
            missing_components.append(
                "AGENTS.md (project passport missing or not a Meta-Project passport)"
            )
        elif registry_errors:
            # Mandatory component: a structurally valid registry (audit §4).
            state = "PARTIAL_CORRUPTED"
            conflicts.extend(f"registry invalid: {e}" for e in registry_errors[:5])
        else:
            # We have a valid registry, boundaries, and passport
            # Check installation match
            harness = target_harness
            inst_id = target_installation

            if inst_id:
                inst_info = installations.get(inst_id)
                if not isinstance(inst_info, dict) or inst_info.get("status") != "active":
                    state = "HEALTHY_NEW_HARNESS"
                elif harness and inst_info.get("harness_id") != harness:
                    state = "HEALTHY_NEW_HARNESS"
                else:
                    state = "HEALTHY_REGISTERED"
            elif harness:
                # Search installation matching harness
                found_active = False
                for iid, info in installations.items():
                    if isinstance(info, dict) and info.get("harness_id") == harness and info.get("status") == "active":
                        found_active = True
                        break
                state = "HEALTHY_REGISTERED" if found_active else "HEALTHY_NEW_HARNESS"
            else:
                # Unknown runtime context (audit §4): report indeterminacy
                # instead of silently adopting any active installation.
                state = "CONTEXT_UNRESOLVED"
    elif foreign_instructions:
        state = "FOREIGN_RULES"
    else:
        state = "NONE"

    # 5. Evaluate Capability Matrix
    # read_instructions
    can_read_instructions = is_meta_agents and pk_dir.is_dir()
    cap_read_instructions = {
        "status": "available" if can_read_instructions else "unavailable",
        "detail": (
            "Root AGENTS.md and knowledge slices accessible"
            if can_read_instructions
            else "AGENTS.md or .agents/project_knowledge missing"
        ),
    }

    # local_checks
    can_local_checks = check_boundaries_script.is_file() and verify_identity_script.is_file()
    cap_local_checks = {
        "status": "available" if can_local_checks else "unavailable",
        "detail": (
            "check_boundaries.py and verify_identity.py present"
            if can_local_checks
            else "Local inspection and boundary scripts not deployed"
        ),
    }

    # skills
    local_skills_dir = agents_dir / "skills"
    has_local_skills = False
    if local_skills_dir.is_dir():
        for item in local_skills_dir.iterdir():
            if item.is_dir() and (item / "SKILL.md").is_file():
                has_local_skills = True
                break
    cap_skills = {
        "status": "available" if has_local_skills else "unavailable",
        "detail": (
            "Local procedural skills deployed in .agents/skills/"
            if has_local_skills
            else "No local skills found in .agents/skills/"
        ),
    }

    # roles — scoped to the TARGET installation (audit §4): availability of
    # another harness's profiles must not leak into the caller's matrix.
    has_roles = False
    if target_installation:
        for r_name, r_info in roles.items():
            if isinstance(r_info, dict):
                b_info = r_info.get("bindings", {}).get(target_installation)
                if (
                    isinstance(b_info, dict)
                    and b_info.get("status") == "active"
                    and b_info.get("profile_path")
                ):
                    profile_path = repo_root / b_info["profile_path"]
                    if profile_path.is_file():
                        has_roles = True
                        break
        cap_roles = {
            "status": "available" if has_roles else "unavailable",
            "detail": (
                f"Active role profiles located and verified for installation '{target_installation}'"
                if has_roles
                else f"No active role profiles bound to installation '{target_installation}'"
            ),
        }
    else:
        # Unknown runtime context: file presence proves nothing about the
        # caller's installation — report untested, not available.
        cap_roles = {
            "status": "untested",
            "detail": (
                "Runtime context unknown (--installation not provided): "
                "role availability for a specific installation is undetermined"
            ),
        }

    # hooks — resolved via the shared hooks directory (works for linked
    # worktrees whose hooks live in the main .git/hooks).
    hook_file = (common_hooks_dir / "pre-commit") if common_hooks_dir else None
    hook_available = False
    if hook_file and hook_file.is_file():
        try:
            hook_text = hook_file.read_text(encoding="utf-8")
            if "check_boundaries" in hook_text:
                hook_available = True
        except Exception:
            pass
    cap_hooks = {
        "status": "available" if hook_available else "unavailable",
        "detail": (
            "Git pre-commit hook incorporates check_boundaries"
            if hook_available
            else "Git pre-commit hook with boundary check not installed"
        ),
    }

    # 7. Check for latest init report
    latest_init_report: Optional[Dict[str, Any]] = None
    reports_base = agents_dir / "reports"
    if reports_base.is_dir():
        candidate_insts = [target_installation] if target_installation else list(installations.keys())
        for c_inst in candidate_insts:
            if not c_inst:
                continue
            init_dir = reports_base / c_inst / "init"
            if init_dir.is_dir():
                ptr_file = init_dir / "latest.json"
                if ptr_file.is_file():
                    try:
                        ptr_data = json.loads(ptr_file.read_text(encoding="utf-8"))
                        latest_init_report = {
                            "run_id": ptr_data.get("run_id"),
                            "status": ptr_data.get("status"),
                            "created_at": ptr_data.get("created_at"),
                            "path": ptr_data.get("path"),
                            "installation_id": c_inst,
                        }
                        break
                    except Exception:
                        pass
                else:
                    report_files = sorted(
                        [p for p in init_dir.glob("*.json") if p.name != "latest.json"],
                        key=lambda p: p.name,
                        reverse=True,
                    )
                    if report_files:
                        try:
                            rf_data = json.loads(report_files[0].read_text(encoding="utf-8"))
                            latest_init_report = {
                                "run_id": rf_data.get("run_id"),
                                "status": rf_data.get("status"),
                                "created_at": rf_data.get("created_at"),
                                "path": str(report_files[0].resolve()).replace("\\", "/"),
                                "installation_id": c_inst,
                            }
                            break
                        except Exception:
                            pass

    return {
        "inspection_version": "1.0.0",
        "repo_root": str(repo_root).replace("\\", "/"),
        "is_git_worktree": is_worktree,
        "git_toplevel": str(main_root if is_worktree else repo_root).replace("\\", "/"),
        "state": state,
        "state_description": STATE_DESCRIPTIONS.get(state, "Unknown state"),
        "recommended_action": CANONICAL_ACTIONS.get(state, "diagnose_and_repair"),
        "runtime_context": {
            "target_harness": target_harness,
            "target_installation": target_installation,
            "resolved": state in ("HEALTHY_REGISTERED",),
        },
        "schema_version": schema_version,
        "project_id": project_id,
        "scenario": scenario,
        "latest_init_report": latest_init_report,
        "installations": installations,
        "capabilities": {
            "read_instructions": cap_read_instructions,
            "local_checks": cap_local_checks,
            "skills": cap_skills,
            "roles": cap_roles,
            "hooks": cap_hooks,
        },
        "missing_components": missing_components,
        "conflicts": conflicts,
        "foreign_instructions": foreign_instructions,
    }


def format_human_report(report: Dict[str, Any]) -> str:
    """Format inspection result as a clean terminal output."""
    lines = [
        "===========================================================",
        "[Meta-Project Inspection Report]",
        "===========================================================",
        f"Repository Root:    {report['repo_root']}",
        f"Git Worktree:       {report['is_git_worktree']}",
        f"State:              {report['state']} ({report['state_description']})",
        f"Recommended Action: {report['recommended_action']}",
        f"Registry Schema:    {report.get('schema_version') or 'None'}",
        f"Project ID:         {report.get('project_id') or 'Unset'}",
    ]
    if report.get("scenario"):
        sc = report["scenario"]
        lines.append(f"Agreed Scenario:    {sc.get('profile', 'Unset')} (model: {sc.get('collaboration_model', 'unspecified')})")
    if report.get("latest_init_report"):
        ir = report["latest_init_report"]
        lines.append(f"Latest Init Report: {ir.get('run_id')} (status: {ir.get('status')})")

    lines.extend([
        "",
        "Capability Matrix:",
    ])
    for cap, info in report["capabilities"].items():
        status_marker = "[OK]" if info["status"] == "available" else "[--]"
        lines.append(f"  {status_marker} {cap:<18}: {info['status']} ({info['detail']})")

    if report["foreign_instructions"]:
        lines.append("")
        lines.append(f"Foreign Instructions Detected: {', '.join(report['foreign_instructions'])}")

    if report["missing_components"]:
        lines.append("")
        lines.append("Missing Components:")
        for mc in report["missing_components"]:
            lines.append(f"  - {mc}")

    if report["conflicts"]:
        lines.append("")
        lines.append("Conflicts / Issues:")
        for conf in report["conflicts"]:
            lines.append(f"  - {conf}")

    lines.append("===========================================================")
    return "\n".join(lines)


def main() -> int:
    # Deterministic output encoding: section-symbol diagnostics (audit refs)
    # must reach consumers as UTF-8 regardless of the Windows console/pipe
    # code page (cp1251 would otherwise emit bare 0xA7 bytes).
    for _stream in (sys.stdout, sys.stderr):
        try:
            _stream.reconfigure(encoding="utf-8")
        except (AttributeError, ValueError):
            pass
    parser = argparse.ArgumentParser(
        description="Meta-Project Deterministic Repository Inspection Utility (Zero External Dependencies, Python 3.10+ stdlib)"
    )
    parser.add_argument(
        "--repo-root",
        default=".",
        help="Path to repository or subproject root (defaults to current working directory).",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="Output inspection result in machine-readable JSON format.",
    )
    parser.add_argument(
        "--harness",
        default=None,
        help="Target execution harness identifier (e.g., antigravity, zcode).",
    )
    parser.add_argument(
        "--installation",
        default=None,
        help="Target concrete installation identifier (e.g., ag-main, zc-main).",
    )
    parser.add_argument(
        "--read-only",
        action="store_true",
        help="Strict audit assertion ensuring zero write operations are executed.",
    )
    parser.add_argument(
        "--facts-digest",
        action="store_true",
        help=(
            "Print a deterministic compact JSON facts digest of the "
            "repository instead of the full report: facts_digest_version, "
            "classified state, per-slice sha256 for the 5 knowledge slices "
            "and the registry (absent slice -> null), capability matrix "
            "(per-dimension status), and the harness/installation echo. "
            "Byte-stable for identical inputs. Composable with "
            "--harness/--installation; the digest describes the REPOSITORY, "
            "not the runtime — without --harness/--installation it is still "
            "emitted, with state CONTEXT_UNRESOLVED for an otherwise healthy "
            "repository. Read-only: no disk cache is written — stash the "
            "output and diff it later."
        ),
    )

    args = parser.parse_args()
    repo_path = Path(args.repo_root)

    report = inspect_repository(
        repo_root=repo_path,
        target_harness=args.harness,
        target_installation=args.installation,
    )

    # Wave E diagnostic notice (presentation only): a CONTEXT_UNRESOLVED
    # state is a canonical unresolved-context condition; the stdout report
    # and rc=0 are unchanged.
    if report["state"] == "CONTEXT_UNRESOLVED":
        sys.stderr.write(_error_envelope(
            "CONTEXT_UNRESOLVED",
            ".agents/meta-project.json",
            "runtime context not resolved from a healthy installation",
            "an explicit --harness/--installation pair registered active "
            "in the registry",
            "no runtime context supplied or resolvable; refusing to "
            "silently adopt an arbitrary active installation",
            "re-run with --harness <id> --installation <id> (see "
            ".agents/meta-project.json), or resolve context via the "
            "ground-meta-project skill",
        ) + "\n")

    if args.facts_digest:
        # Wave G Lane I: the digest is the whole payload (compact, sorted,
        # fixed separators — byte-stable for identical inputs).
        print(json.dumps(
            compute_facts_digest(repo_path, report),
            sort_keys=True,
            separators=(",", ":"),
            ensure_ascii=True,
        ))
        return 0

    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        print(format_human_report(report))

    return 0


if __name__ == "__main__":
    sys.exit(main())
