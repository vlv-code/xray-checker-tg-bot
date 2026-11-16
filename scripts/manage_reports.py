#!/usr/bin/env python3
"""
Meta-Project Report Management Engine (scripts/manage_reports.py)

Deterministic, zero-dependency Python 3.10+ stdlib CLI and library for creating,
validating, locating, and inspecting initialization ('init') and management ('manage')
JSON reports in .agents/reports/<installation_id>/{init,manage}/.
"""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
from typing import Any, Dict, List, Optional, Tuple
import uuid

try:
    from scripts.verify_identity import (
        evaluate_actor_admission,
        load_registry,
        resolve_actor_binding,
    )
except ImportError:  # direct script execution: python scripts/manage_reports.py
    try:
        from verify_identity import (
            evaluate_actor_admission,
            load_registry,
            resolve_actor_binding,
        )
    except ImportError:
        evaluate_actor_admission = None
        load_registry = None
        resolve_actor_binding = None

SUPPORTED_SCHEMA_VERSIONS = {"1.0.0", "1.0", "1"}
VALID_REPORT_TYPES = {"init", "manage", "update", "tune"}
VALID_REPORT_STATUSES = {"completed", "awaiting_decision", "blocked"}
VALID_TUNE_DECISIONS = {"proposed", "accepted", "rejected"}

# Essential decision inputs fingerprinted into every report (audit §5
# freshness). Reports and runtime logs are intentionally excluded.
ESSENTIAL_INPUT_PATHS = (
    ".agents/meta-project.json",
    ".agents/project_knowledge/boundaries.md",
    "AGENTS.md",
)


def collect_input_fingerprints(repo_root: Path) -> Dict[str, Optional[str]]:
    """SHA-256 fingerprints of essential inputs; None marks a missing file."""
    fingerprints: Dict[str, Optional[str]] = {}
    for rel in ESSENTIAL_INPUT_PATHS:
        path = Path(repo_root) / rel
        if path.is_file():
            try:
                digest = hashlib.sha256(
                    path.read_bytes()
                ).hexdigest()
            except OSError:
                digest = None
            fingerprints[rel] = digest
        else:
            fingerprints[rel] = None
    return fingerprints

VALID_SCENARIO_PROFILES = {
    "active_development",
    "maintenance",
    "research",
    "audit",
    "documentation",
}
VALID_DEPLOYMENT_MODES = {"shared_repo", "local_overlay", "hybrid"}
VALID_COLLABORATION_MODELS = {"solo", "duo", "triad", "specialized_swarm"}
VALID_VERIFICATION_POLICIES = {
    "self_check",
    "duo_peer_review",
    "strict_independent",
}
VALID_DELEGATION_LEVELS = {"strictly_manual", "controlled", "autonomous"}


def generate_run_id(prefix: str = "") -> str:
    """Generate deterministic UTC timestamp + short UUID run identifier."""
    now = datetime.datetime.now(datetime.timezone.utc)
    ts = now.strftime("%Y%m%dT%H%M%SZ")
    uid = uuid.uuid4().hex[:8]
    return f"{prefix}{ts}-{uid}" if not prefix else f"{prefix}-{ts}-{uid}"


def get_git_state(repo_root: Path) -> Dict[str, Any]:
    """Extract current git state without external dependencies."""
    commit_sha = "unknown"
    branch = "unknown"
    is_clean = True
    modified_files: List[str] = []

    try:
        proc = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=str(repo_root),
            capture_output=True,
            text=True,
            timeout=5,
        )
        if proc.returncode == 0:
            commit_sha = proc.stdout.strip()
    except Exception:
        pass

    try:
        proc = subprocess.run(
            ["git", "rev-parse", "--abbrev-ref", "HEAD"],
            cwd=str(repo_root),
            capture_output=True,
            text=True,
            timeout=5,
        )
        if proc.returncode == 0:
            branch = proc.stdout.strip()
    except Exception:
        pass

    try:
        proc = subprocess.run(
            ["git", "status", "--porcelain", "-z"],
            cwd=str(repo_root),
            capture_output=True,
            timeout=5,
        )
        if proc.returncode == 0 and proc.stdout:
            raw_entries = proc.stdout.split(b"\x00")
            for entry in raw_entries:
                if entry:
                    # Format: XY PATH
                    path_str = entry[3:].decode("utf-8", errors="replace")
                    if path_str:
                        modified_files.append(path_str.replace("\\", "/"))
            is_clean = len(modified_files) == 0
    except Exception:
        pass

    return {
        "commit_sha": commit_sha,
        "branch": branch,
        "is_clean": is_clean,
        "modified_files": modified_files,
    }


def validate_report(
    data: Dict[str, Any], expected_type: Optional[str] = None
) -> List[str]:
    """Validate report structure and schemas against fail-closed invariants."""
    errors: List[str] = []

    if not isinstance(data, dict):
        return ["Report content must be a JSON object"]

    schema_version = str(data.get("schema_version", "")).strip()
    if not schema_version:
        errors.append("Missing required field 'schema_version'")
    elif (
        schema_version not in SUPPORTED_SCHEMA_VERSIONS
        and schema_version.split(".")[0]
        not in [v.split(".")[0] for v in SUPPORTED_SCHEMA_VERSIONS]
    ):
        errors.append(f"Unsupported schema_version: '{schema_version}'")

    report_type = str(data.get("report_type", "")).strip()
    if not report_type:
        errors.append("Missing required field 'report_type'")
    elif report_type not in VALID_REPORT_TYPES:
        errors.append(
            f"Invalid report_type: '{report_type}'. Allowed: {sorted(list(VALID_REPORT_TYPES))}"
        )
    elif expected_type and report_type != expected_type:
        errors.append(
            f"Expected report_type '{expected_type}', got '{report_type}'"
        )

    for field in [
        "project_id",
        "installation_id",
        "harness_id",
        "actor_id",
        "run_id",
    ]:
        val = str(data.get(field, "")).strip()
        if not val:
            errors.append(f"Missing required field '{field}'")

    status = str(data.get("status", "")).strip()
    if not status:
        errors.append("Missing required field 'status'")
    elif status not in VALID_REPORT_STATUSES:
        errors.append(
            f"Invalid status: '{status}'. Allowed: {sorted(list(VALID_REPORT_STATUSES))}"
        )

    git_state = data.get("git_state")
    if not isinstance(git_state, dict):
        errors.append("Missing or invalid field 'git_state' (must be an object)")

    # Type-specific validation
    if report_type == "init":
        scenario = data.get("scenario")
        if not isinstance(scenario, dict):
            errors.append("Missing or invalid field 'scenario' in init report")
        else:
            profile = scenario.get("profile")
            if profile and profile not in VALID_SCENARIO_PROFILES:
                errors.append(
                    f"Invalid scenario profile: '{profile}'. Allowed: {sorted(list(VALID_SCENARIO_PROFILES))}"
                )
            # A completed report must carry a real agreement (audit §5): no
            # empty scenarios, no unresolved mandatory questions, no failed
            # mandatory checks hidden behind status 'completed'.
            if status == "completed":
                if not profile:
                    errors.append(
                        "Completed init report must carry an agreed scenario "
                        "('scenario.profile' missing or empty)"
                    )
                unresolved = scenario.get("unresolved_questions")
                if isinstance(unresolved, list) and unresolved:
                    errors.append(
                        "Completed init report must not contain unresolved "
                        "scenario questions"
                    )

        if status == "completed" and isinstance(data.get("checks"), list):
            failed_required = [
                c.get("name", "<unnamed>")
                for c in data["checks"]
                if isinstance(c, dict)
                and c.get("status") in ("fail", "failed", "error")
            ]
            if failed_required:
                errors.append(
                    "Completed init report contains failed checks: "
                    + ", ".join(failed_required)
                )

        readiness = data.get("next_step_readiness")
        if not isinstance(readiness, dict):
            errors.append(
                "Missing or invalid field 'next_step_readiness' in init report"
            )

    elif report_type == "manage":
        init_run_id = str(data.get("init_run_id", "")).strip()
        if not init_run_id:
            errors.append(
                "Missing required field 'init_run_id' in manage report"
            )

        roles = data.get("roles")
        if not isinstance(roles, dict):
            errors.append("Missing or invalid field 'roles' in manage report")

        bindings = data.get("bindings")
        if not isinstance(bindings, dict):
            errors.append("Missing or invalid field 'bindings' in manage report")

    elif report_type == "update":
        versions = data.get("versions")
        if (
            not isinstance(versions, dict)
            or not str(versions.get("before", "")).strip()
            or not str(versions.get("after", "")).strip()
        ):
            errors.append(
                "Missing or invalid field 'versions' in update report "
                "(must be an object with non-empty 'before' and 'after')"
            )

        source = data.get("source")
        if not isinstance(source, dict):
            errors.append(
                "Missing or invalid field 'source' in update report "
                "(must be an object)"
            )
        else:
            for sub_field in ("ref", "commit_sha", "origin"):
                if not str(source.get(sub_field, "")).strip():
                    errors.append(
                        f"Missing or invalid field 'source.{sub_field}' "
                        "in update report"
                    )

        affected = data.get("affected_components")
        if not isinstance(affected, list) or not affected:
            errors.append(
                "Missing or invalid field 'affected_components' in update "
                "report (must be a non-empty list)"
            )

        conflicts = data.get("conflicts")
        if status == "completed" and isinstance(conflicts, list) and conflicts:
            errors.append(
                "Completed update report must not carry unresolved conflicts: "
                "use status 'awaiting_decision' until conflicts are resolved"
            )

    elif report_type == "tune":
        proposals = data.get("proposals")
        if not isinstance(proposals, list) or not proposals:
            errors.append(
                "Missing or invalid field 'proposals' in tune report "
                "(must be a non-empty list)"
            )
        else:
            for idx, proposal in enumerate(proposals):
                if not isinstance(proposal, dict):
                    errors.append(
                        f"Invalid proposal at index {idx} in tune report "
                        "(must be an object)"
                    )
                    continue
                decision = str(proposal.get("decision", "")).strip()
                if not decision:
                    errors.append(
                        f"Proposal {idx} in tune report is missing required "
                        "field 'decision'"
                    )
                elif decision not in VALID_TUNE_DECISIONS:
                    errors.append(
                        f"Invalid decision '{decision}' for proposal {idx} in "
                        f"tune report. Allowed: {sorted(list(VALID_TUNE_DECISIONS))}"
                    )
                if (
                    decision == "rejected"
                    and not str(proposal.get("rationale", "")).strip()
                ):
                    errors.append(
                        f"Rejected proposal {idx} in tune report must carry "
                        "a non-empty 'rationale'"
                    )
                if status == "completed" and decision in ("", "proposed"):
                    errors.append(
                        f"Completed tune report requires a recorded decision "
                        f"for every proposal; proposal {idx} is undecided"
                    )

    return errors


def get_report_dir(
    repo_root: Path, installation_id: str, report_type: str
) -> Path:
    """Return destination directory for reports."""
    return repo_root / ".agents" / "reports" / installation_id / report_type


def save_report(
    repo_root: Path,
    installation_id: str,
    report_type: str,
    report_data: Dict[str, Any],
) -> Path:
    """
    Atomically write report to disk and update latest.json pointer.
    Raises ValueError if data is invalid, the run_id is unsafe, or the run
    already exists (audit §5: existing runs must never be overwritten).
    """
    errors = validate_report(report_data, expected_type=report_type)
    if errors:
        raise ValueError(f"Cannot save invalid report: {'; '.join(errors)}")

    run_id = str(report_data["run_id"])
    # Path safety (audit §5): identifiers must not escape the report dir.
    if not run_id or run_id != Path(run_id).name or ".." in run_id or "/" in run_id or "\\" in run_id:
        raise ValueError(f"Unsafe run_id refused: {run_id!r}")

    target_dir = get_report_dir(repo_root, installation_id, report_type)
    target_dir.mkdir(parents=True, exist_ok=True)

    target_file = target_dir / f"{run_id}.json"
    if target_file.exists():
        raise ValueError(
            f"Refusing to overwrite existing run: {target_file} "
            "(each attempt must use a unique run_id)"
        )
    temp_file = target_dir / f".tmp_{run_id}_{uuid.uuid4().hex[:6]}.json"

    # 1. Atomic write of full report
    with open(temp_file, "w", encoding="utf-8") as f:
        json.dump(report_data, f, indent=2, ensure_ascii=False)
        f.write("\n")

    temp_file.replace(target_file)

    # 2. Atomic write of latest pointer (relative path so a repository copy
    # resolves its OWN reports; audit §5 cross-contamination fix).
    latest_pointer_file = target_dir / "latest.json"
    temp_latest = target_dir / f".tmp_latest_{uuid.uuid4().hex[:6]}.json"
    pointer_data = {
        "run_id": run_id,
        "status": report_data["status"],
        "created_at": report_data.get("created_at"),
        "report_type": report_type,
        "installation_id": installation_id,
        "path": target_file.name,
    }
    with open(temp_latest, "w", encoding="utf-8") as f:
        json.dump(pointer_data, f, indent=2, ensure_ascii=False)
        f.write("\n")

    temp_latest.replace(latest_pointer_file)
    return target_file


def _load_valid_report(path: Path, expected_type: str) -> Optional[Dict[str, Any]]:
    """Load a report file and return it when structurally valid, else None."""
    try:
        with open(path, "r", encoding="utf-8") as f:
            data = json.load(f)
    except Exception:
        return None
    if validate_report(data, expected_type=expected_type):
        return None
    return data


def find_latest_report(
    repo_root: Path, installation_id: str, report_type: str
) -> Tuple[Optional[Path], Optional[Dict[str, Any]]]:
    """
    Find the LATEST ATTEMPT for given installation and type (audit §5):
    'latest' includes blocked or corrupted attempts and is never silently
    substituted with an older valid report. Pointer paths are resolved
    relative to the report directory (repository-copy isolation).
    Returns (path, data) for a valid latest attempt, (path, None) when the
    latest attempt is corrupted/foreign, or (None, None) when absent.
    """
    target_dir = get_report_dir(repo_root, installation_id, report_type)
    if not target_dir.exists():
        return None, None

    def _belongs(data: Optional[Dict[str, Any]]) -> bool:
        return (
            data is not None
            and data.get("installation_id") == installation_id
            and data.get("report_type") == report_type
        )

    # Check latest.json first
    latest_pointer = target_dir / "latest.json"
    if latest_pointer.exists():
        try:
            with open(latest_pointer, "r", encoding="utf-8") as f:
                ptr = json.load(f)
        except Exception:
            ptr = None
        if ptr:
            raw = str(ptr.get("path", ""))
            cand = Path(raw)
            if not cand.is_absolute():
                cand = target_dir / raw
            # Containment: pointer must resolve inside the report directory.
            try:
                cand.resolve().relative_to(target_dir.resolve())
            except (ValueError, OSError):
                cand = None
            if cand is not None and cand.is_file():
                data = _load_valid_report(cand, report_type)
                if _belongs(data):
                    return cand, data
                # Surface the latest attempt as corrupted/foreign instead of
                # hiding it behind an older valid report.
                return cand, None

    # No usable pointer: newest attempt by deterministic run_id name order.
    candidates = sorted(
        [p for p in target_dir.glob("*.json") if p.name != "latest.json"],
        key=lambda p: p.name,
        reverse=True,
    )
    for cand in candidates:
        data = _load_valid_report(cand, report_type)
        if _belongs(data):
            return cand, data
        return cand, None  # newest attempt is unreadable: surface it

    return None, None


def create_init_report(
    repo_root: Path,
    installation_id: str,
    harness_id: str,
    actor_id: str,
    project_id: str,
    status: str,
    scenario: Dict[str, Any],
    environment: Optional[Dict[str, Any]] = None,
    components: Optional[Dict[str, Any]] = None,
    checks: Optional[List[Dict[str, Any]]] = None,
    recommendations: Optional[Dict[str, Any]] = None,
    blockers: Optional[List[str]] = None,
    next_step_readiness: Optional[Dict[str, Any]] = None,
    run_id: Optional[str] = None,
) -> Dict[str, Any]:
    """Helper to assemble a canonical init report dictionary."""
    git_state = get_git_state(repo_root)
    actual_run_id = run_id or generate_run_id("init")
    now_iso = datetime.datetime.now(datetime.timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )

    return {
        "schema_version": "1.0.0",
        "report_type": "init",
        "project_id": project_id,
        "installation_id": installation_id,
        "harness_id": harness_id,
        "actor_id": actor_id,
        "run_id": actual_run_id,
        "created_at": now_iso,
        "status": status,
        "git_state": git_state,
        "input_fingerprints": collect_input_fingerprints(repo_root),
        "references": {
            "registry": ".agents/meta-project.json",
            "passport": "AGENTS.md",
            "boundaries": ".agents/project_knowledge/boundaries.md",
        },
        "scenario": scenario,
        "environment": environment or {"capabilities": {}, "constraints": []},
        "components": components
        or {"created": [], "modified": [], "preserved": []},
        "checks": checks or [],
        "recommendations_for_manage": recommendations
        or {"suggested_roles": [], "target_topology": "", "rationale": ""},
        "blockers_and_unresolved": blockers or [],
        "next_step_readiness": next_step_readiness
        or {
            "ready_for_manage": status == "completed",
            "details": "Initialization complete"
            if status == "completed"
            else "Awaiting resolution",
        },
    }


def create_manage_report(
    repo_root: Path,
    installation_id: str,
    harness_id: str,
    actor_id: str,
    project_id: str,
    init_run_id: str,
    status: str,
    scenario: Dict[str, Any],
    roles: Dict[str, List[str]],
    bindings: Dict[str, Any],
    effective_capabilities: Optional[Dict[str, Any]] = None,
    checks: Optional[List[Dict[str, Any]]] = None,
    remaining_manual_actions: Optional[List[str]] = None,
    run_id: Optional[str] = None,
) -> Dict[str, Any]:
    """Helper to assemble a canonical manage report dictionary."""
    git_state = get_git_state(repo_root)
    actual_run_id = run_id or generate_run_id("manage")
    now_iso = datetime.datetime.now(datetime.timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )

    return {
        "schema_version": "1.0.0",
        "report_type": "manage",
        "project_id": project_id,
        "installation_id": installation_id,
        "harness_id": harness_id,
        "actor_id": actor_id,
        "run_id": actual_run_id,
        "init_run_id": init_run_id,
        "created_at": now_iso,
        "status": status,
        "git_state": git_state,
        "input_fingerprints": collect_input_fingerprints(repo_root),
        "scenario": scenario,
        "roles": roles,
        "bindings": bindings,
        "effective_capabilities": effective_capabilities
        or {"team_readiness": "ready"},
        "checks": checks or [],
        "remaining_manual_actions": remaining_manual_actions or [],
    }


def resolve_team_topology(
    scenario: Dict[str, Any], loc_estimate: int = 0
) -> Dict[str, Any]:
    """
    Map scenario configuration to recommended agent team topology.

    Decision precedence (audit §5/§6):
    1. An explicitly agreed collaboration_model is a USER DECISION and always
       wins; size heuristics (LOC) must never silently downgrade it.
    2. Contradictory wishes (e.g. 'solo' + strict_independent verification)
       surface a non-empty 'conflicts' list for explicit resolution instead
       of a silent choice.
    3. 'specialized_swarm' is declared but NOT YET IMPLEMENTED: the result
       carries "supported": False and must be resolved with the user.
    4. Without an explicit choice, the profile/verification rules apply and
       LOC is only a secondary sizing guideline.
    Returns:
    {
        "target_topology": "solo" | "duo" | "triad" | "specialized_swarm",
        "suggested_roles": [...],
        "rationale": "...",
        "supported": bool,            # always present
        "conflicts": [str, ...],      # always present
    }
    """
    profile = scenario.get("profile", "active_development")
    collab = scenario.get("collaboration_model", "")
    verification = scenario.get("verification_policy", "")

    conflicts: List[str] = []

    def _result(topology: str, roles: List[str], rationale: str,
                supported: bool = True) -> Dict[str, Any]:
        return {
            "target_topology": topology,
            "suggested_roles": roles,
            "rationale": rationale,
            "supported": supported,
            "conflicts": conflicts,
        }

    # 1. Explicit user decision takes precedence.
    if collab == "solo":
        if verification == "strict_independent":
            conflicts.append(
                "collaboration_model 'solo' conflicts with verification_policy "
                "'strict_independent': independent verification requires a "
                "second role; resolve explicitly with the user"
            )
        return _result(
            "solo", ["agent-general-worker"],
            f"Solo Executor honored as explicit user decision (profile '{profile}').",
        )
    if collab == "duo":
        return _result(
            "duo", ["agent-dev-lead", "agent-quality-guardian"],
            "Micro-Mini Duo honored as explicit user decision (2-stage handoff).",
        )
    if collab == "triad":
        return _result(
            "triad",
            ["agent-dev-lead", "agent-quality-guardian", "agent-github-steward"],
            "Canonical Compact Trio honored as explicit user decision; "
            "LOC estimates do not override agreed collaboration models.",
        )
    if collab == "specialized_swarm":
        return _result(
            "specialized_swarm", [],
            "specialized_swarm was requested but is NOT YET IMPLEMENTED; "
            "present the user with an explicit choice (proceed as triad now "
            "or defer until swarm support lands).",
            supported=False,
        )

    # 2. No explicit choice: profile/verification rules; LOC is secondary.
    if profile in ("documentation", "research"):
        return _result(
            "solo", ["agent-general-worker"],
            f"Solo Executor derived for '{profile}' profile (no explicit model agreed).",
        )
    if verification == "strict_independent":
        return _result(
            "triad",
            ["agent-dev-lead", "agent-quality-guardian", "agent-github-steward"],
            "Triad derived from strict_independent verification (independent "
            "verifier + release governance), regardless of repository size.",
        )
    if profile == "maintenance" or (0 < loc_estimate < 3000):
        return _result(
            "duo", ["agent-dev-lead", "agent-quality-guardian"],
            "Micro-Mini Duo derived for maintenance/compact repository "
            f"(profile '{profile}', LOC estimate {loc_estimate}).",
        )
    return _result(
        "duo", ["agent-dev-lead", "agent-quality-guardian"],
        "Standard development team configuration.",
    )


def check_report_staleness(
    repo_root: Path, report_data: Dict[str, Any]
) -> Tuple[bool, Dict[str, Any]]:
    """
    Evaluate report freshness (audit §5): a report is stale when the commit
    SHA moved OR when any essential input (registry, boundaries, passport)
    drifted from its recorded fingerprint — including uncommitted changes.
    Returns (is_stale, details).
    """
    current_git = get_git_state(repo_root)
    current_sha = current_git.get("commit_sha", "unknown")
    report_git = report_data.get("git_state", {})
    report_sha = report_git.get("commit_sha", "unknown")

    reasons: List[str] = []
    if (
        current_sha != "unknown"
        and report_sha != "unknown"
        and current_sha != report_sha
    ):
        reasons.append(
            f"commit moved: report {report_sha[:8]} != HEAD {current_sha[:8]}"
        )

    fingerprints = report_data.get("input_fingerprints")
    if isinstance(fingerprints, dict) and fingerprints:
        current_fps = collect_input_fingerprints(repo_root)
        for rel in sorted(fingerprints):
            if current_fps.get(rel) != fingerprints.get(rel):
                reasons.append(f"essential input changed since report: {rel}")
    elif current_sha != "unknown":
        # Report predates fingerprinting: freshness of essential inputs is
        # unverifiable; treat dirty essential inputs as stale.
        dirty = [
            rel
            for rel in ESSENTIAL_INPUT_PATHS
            if rel in current_git.get("modified_files", [])
        ]
        if dirty:
            reasons.append(
                "essential inputs have uncommitted changes while the report "
                "carries no fingerprints: " + ", ".join(dirty)
            )

    is_stale = bool(reasons)
    details = {
        "is_stale": is_stale,
        "reasons": reasons,
        "current_commit": current_sha,
        "report_commit": report_sha,
        "current_branch": current_git.get("branch", "unknown"),
        "report_branch": report_git.get("branch", "unknown"),
    }
    return is_stale, details


def evaluate_manage_intake(
    repo_root: Path,
    installation_id: str,
    harness_id: str,
    actor_id: str,
    report_path: Path,
) -> List[str]:
    """
    Intake admission for the init -> manage contract (audit §5).

    Separates two mechanisms: format validation (is the report a correct
    document?) and intake admission (may THIS document authorize a manage
    operation in THIS project/installation?). A blocked report can be a
    correct document and must still be refused.

    Returns a list of intake errors; an empty list admits the operation.
    """
    repo_root = Path(repo_root)
    report_path = Path(report_path)
    errors: List[str] = []

    # 1. The report file must exist and parse (structured diagnostics).
    if not report_path.is_file():
        return [f"Intake rejected: report file not found: {report_path}"]
    try:
        with open(report_path, "r", encoding="utf-8") as f:
            data = json.load(f)
    except Exception as exc:
        return [f"Intake rejected: report file is corrupted (JSON parse error): {exc}"]
    if not isinstance(data, dict):
        return ["Intake rejected: report content must be a JSON object"]

    # 2. Format validation (fail-closed on structurally invalid documents).
    format_errors = validate_report(data, expected_type="init")
    if format_errors:
        return [f"Intake rejected: invalid init report: {e}" for e in format_errors]

    # 3. Project identity comes from the local registry, never from a
    # hardcoded default; explicit mismatch is refused.
    if load_registry is None:
        return ["Intake rejected: identity engine unavailable in this environment"]
    registry, reg_err = load_registry(repo_root)
    if registry is None:
        return [f"Intake rejected: local registry unavailable: {reg_err}"]
    registry_project = registry.get("project_id")
    if data.get("project_id") != registry_project:
        errors.append(
            f"Intake rejected: report project_id '{data.get('project_id')}' does "
            f"not match local registry project_id '{registry_project}'"
        )

    # 4. Installation and harness must match the local registry and report.
    inst_info = (registry.get("installations") or {}).get(installation_id)
    if not isinstance(inst_info, dict):
        errors.append(
            f"Intake rejected: installation '{installation_id}' is not "
            f"registered in the local registry"
        )
    else:
        if inst_info.get("harness_id") != harness_id:
            errors.append(
                f"Intake rejected: harness '{harness_id}' does not match "
                f"installation '{installation_id}' harness_id "
                f"'{inst_info.get('harness_id')}'"
            )
        if inst_info.get("status") != "active":
            errors.append(
                f"Intake rejected: installation '{installation_id}' status is "
                f"'{inst_info.get('status')}', not 'active'"
            )
    if data.get("installation_id") != installation_id:
        errors.append(
            f"Intake rejected: report installation_id "
            f"'{data.get('installation_id')}' != target '{installation_id}'"
        )
    if data.get("harness_id") != harness_id:
        errors.append(
            f"Intake rejected: report harness_id "
            f"'{data.get('harness_id')}' != target '{harness_id}'"
        )

    # 5. Actor admission: the initiator must be a registered, ACTIVE identity.
    if resolve_actor_binding is not None and evaluate_actor_admission is not None:
        found, binding = resolve_actor_binding(repo_root, actor_id)
        if not found:
            errors.append(
                f"Intake rejected: actor_id '{actor_id}' not found in registry"
            )
        else:
            denial = evaluate_actor_admission(registry, binding)
            if denial:
                errors.append(f"Intake rejected: {denial}")
    else:
        errors.append("Intake rejected: identity engine unavailable")

    # 6. Status & readiness: only a completed, ready initialization
    # authorizes manage; blocked/awaiting reports are decision ledgers.
    status = data.get("status")
    if status != "completed":
        errors.append(
            f"Intake rejected: init report status is '{status}'; only "
            f"'completed' authorizes manage operations"
        )
    else:
        readiness = data.get("next_step_readiness")
        if not isinstance(readiness, dict) or readiness.get("ready_for_manage") is not True:
            errors.append(
                "Intake rejected: next_step_readiness.ready_for_manage is not "
                "true for a completed report"
            )

    # 7. Freshness: essential inputs must match the report's fingerprints
    # (uncommitted changes to registry/boundaries/passport make it stale).
    is_stale, stale_details = check_report_staleness(repo_root, data)
    if is_stale:
        reasons = "; ".join(stale_details.get("reasons") or ["inputs changed"])
        errors.append(f"Intake rejected: stale report ({reasons})")

    return errors


def _resolve_project_id(
    repo_root: Path, explicit: Optional[str]
) -> Tuple[Optional[str], Optional[str]]:
    """
    Project identity resolution (audit §5): explicit argument wins, otherwise
    the local registry is the source of truth. The hardcoded default
    'vlv-code/meta-project' is removed — foreign projects must never be
    silently stamped with another project's identity.
    Returns (project_id, error_message).
    """
    if explicit:
        return explicit, None
    if load_registry is not None:
        registry, _ = load_registry(Path(repo_root))
        if isinstance(registry, dict) and registry.get("project_id"):
            return registry["project_id"], None
    return None, (
        "--project-id is required when no local registry "
        "(.agents/meta-project.json) defines the project identity"
    )


def build_cli_parser() -> argparse.ArgumentParser:
    """Build command line argument parser."""
    parser = argparse.ArgumentParser(
        description="Meta-Project Report Management Engine"
    )
    subparsers = parser.add_subparsers(dest="subcommand", required=True)

    # 1. init subcommand
    p_init = subparsers.add_parser(
        "init", help="Manage initialization (init) reports"
    )
    p_init.add_argument(
        "--action", choices=["create", "verify"], required=True
    )
    p_init.add_argument(
        "--repo-root", default=".", help="Target repository root path"
    )
    p_init.add_argument(
        "--installation", help="Target installation ID (e.g. ag-main)"
    )
    p_init.add_argument("--harness", help="Harness ID (e.g. antigravity)")
    p_init.add_argument("--actor", help="Qualified actor_id")
    p_init.add_argument(
        "--project-id",
        default=None,
        help="Project ID (default: resolved from the local registry, never hardcoded)",
    )
    p_init.add_argument(
        "--status",
        choices=["completed", "awaiting_decision", "blocked"],
        default="completed",
    )
    p_init.add_argument(
        "--scenario-file", help="Path to JSON file containing scenario data"
    )
    p_init.add_argument(
        "--data-file", help="Path to JSON file with additional init data"
    )
    p_init.add_argument("--report", help="Path to report file for verification")
    p_init.add_argument(
        "--json", action="store_true", help="Output machine-readable JSON"
    )

    # 2. manage subcommand
    p_manage = subparsers.add_parser(
        "manage", help="Manage team management (manage) reports"
    )
    p_manage.add_argument(
        "--action", choices=["create", "verify"], required=True
    )
    p_manage.add_argument(
        "--repo-root", default=".", help="Target repository root path"
    )
    p_manage.add_argument(
        "--installation", help="Target installation ID (e.g. ag-main)"
    )
    p_manage.add_argument("--harness", help="Harness ID (e.g. antigravity)")
    p_manage.add_argument("--actor", help="Qualified actor_id")
    p_manage.add_argument(
        "--project-id",
        default=None,
        help="Project ID (default: resolved from the local registry, never hardcoded)",
    )
    p_manage.add_argument(
        "--init-run-id", help="Reference to preceding init run ID"
    )
    p_manage.add_argument(
        "--status",
        choices=["completed", "awaiting_decision", "blocked"],
        default="completed",
    )
    p_manage.add_argument(
        "--data-file",
        help="Path to JSON file containing roles, bindings, and capabilities",
    )
    p_manage.add_argument(
        "--report", help="Path to report file for verification"
    )
    p_manage.add_argument(
        "--json", action="store_true", help="Output machine-readable JSON"
    )

    # 3. latest subcommand
    p_latest = subparsers.add_parser(
        "latest", help="Find latest report for an installation"
    )
    p_latest.add_argument(
        "--repo-root", default=".", help="Target repository root path"
    )
    p_latest.add_argument(
        "--installation", required=True, help="Installation ID"
    )
    p_latest.add_argument(
        "--type",
        choices=["init", "manage", "update", "tune"],
        required=True,
        help="Report type",
    )
    p_latest.add_argument(
        "--json", action="store_true", help="Output machine-readable JSON"
    )

    # 4. verify subcommand
    p_verify = subparsers.add_parser(
        "verify", help="Validate an existing report file"
    )
    p_verify.add_argument("--report", required=True, help="Path to report file")
    p_verify.add_argument(
        "--type",
        choices=["init", "manage", "update", "tune"],
        help="Expected report type (optional)",
    )
    p_verify.add_argument(
        "--json", action="store_true", help="Output machine-readable JSON"
    )

    return parser


def main() -> int:
    """CLI entry point."""
    parser = build_cli_parser()
    args = parser.parse_args()
    repo_root = Path(getattr(args, "repo_root", ".")).resolve()

    if args.subcommand == "init":
        if args.action == "verify":
            if not args.report:
                sys.stderr.write("Error: --report is required for verify action\n")
                return 2
            report_path = Path(args.report)
            if not report_path.exists():
                sys.stderr.write(f"Error: File '{report_path}' not found\n")
                return 1
            try:
                with open(report_path, "r", encoding="utf-8") as f:
                    data = json.load(f)
                errors = validate_report(data, expected_type="init")
                if errors:
                    if args.json:
                        print(
                            json.dumps(
                                {
                                    "valid": False,
                                    "report": str(report_path),
                                    "errors": errors,
                                }
                            )
                        )
                    else:
                        print(
                            f"[FAIL] Invalid init report '{report_path}':\n"
                            + "\n".join(f"  * {e}" for e in errors)
                        )
                    return 1
                if args.json:
                    print(
                        json.dumps(
                            {
                                "valid": True,
                                "report": str(report_path),
                                "run_id": data.get("run_id"),
                                "status": data.get("status"),
                            }
                        )
                    )
                else:
                    print(f"[OK] Init report '{report_path}' is valid.")
                return 0
            except Exception as e:
                sys.stderr.write(f"Error reading report: {e}\n")
                return 1

        elif args.action == "create":
            if not args.installation or not args.harness or not args.actor:
                sys.stderr.write(
                    "Error: --installation, --harness, and --actor are required for create action\n"
                )
                return 2
            project_id, pid_err = _resolve_project_id(repo_root, args.project_id)
            if pid_err:
                sys.stderr.write(f"Error: {pid_err}\n")
                return 2

            scenario = {}
            if args.scenario_file:
                with open(args.scenario_file, "r", encoding="utf-8") as f:
                    scenario = json.load(f)

            extra_data = {}
            if args.data_file:
                with open(args.data_file, "r", encoding="utf-8") as f:
                    extra_data = json.load(f)

            report = create_init_report(
                repo_root=repo_root,
                installation_id=args.installation,
                harness_id=args.harness,
                actor_id=args.actor,
                project_id=project_id,
                status=args.status,
                scenario=scenario or extra_data.get("scenario", {}),
                environment=extra_data.get("environment"),
                components=extra_data.get("components"),
                checks=extra_data.get("checks"),
                recommendations=extra_data.get("recommendations_for_manage"),
                blockers=extra_data.get("blockers_and_unresolved"),
                next_step_readiness=extra_data.get("next_step_readiness"),
            )

            try:
                saved_path = save_report(
                    repo_root, args.installation, "init", report
                )
                if args.json:
                    print(
                        json.dumps(
                            {
                                "success": True,
                                "run_id": report["run_id"],
                                "report_path": str(
                                    saved_path.resolve()
                                ).replace("\\", "/"),
                                "status": report["status"],
                            }
                        )
                    )
                else:
                    print(
                        f"[OK] Init report created: {saved_path} (run_id={report['run_id']})"
                    )
                return 0
            except Exception as e:
                sys.stderr.write(f"Error saving report: {e}\n")
                return 1

    elif args.subcommand == "manage":
        if args.action == "verify":
            if not args.report:
                sys.stderr.write("Error: --report is required for verify action\n")
                return 2
            report_path = Path(args.report)
            if not report_path.exists():
                sys.stderr.write(f"Error: File '{report_path}' not found\n")
                return 1
            try:
                with open(report_path, "r", encoding="utf-8") as f:
                    data = json.load(f)
                errors = validate_report(data, expected_type="manage")
                if errors:
                    if args.json:
                        print(
                            json.dumps(
                                {
                                    "valid": False,
                                    "report": str(report_path),
                                    "errors": errors,
                                }
                            )
                        )
                    else:
                        print(
                            f"[FAIL] Invalid manage report '{report_path}':\n"
                            + "\n".join(f"  * {e}" for e in errors)
                        )
                    return 1
                if args.json:
                    print(
                        json.dumps(
                            {
                                "valid": True,
                                "report": str(report_path),
                                "run_id": data.get("run_id"),
                                "status": data.get("status"),
                            }
                        )
                    )
                else:
                    print(f"[OK] Manage report '{report_path}' is valid.")
                return 0
            except Exception as e:
                sys.stderr.write(f"Error reading report: {e}\n")
                return 1

        elif args.action == "create":
            if (
                not args.installation
                or not args.harness
                or not args.actor
                or not args.init_run_id
            ):
                sys.stderr.write(
                    "Error: --installation, --harness, --actor, and --init-run-id are required for manage create\n"
                )
                return 2
            project_id, pid_err = _resolve_project_id(repo_root, args.project_id)
            if pid_err:
                sys.stderr.write(f"Error: {pid_err}\n")
                return 2

            # Intake admission (audit §5 + Guardian Step 64 finding): a
            # manage operation is authorized ONLY by an admitted init report
            # for this installation. The referenced init run must exist and
            # pass evaluate_manage_intake; hand-built or foreign init
            # references fail closed.
            init_file = (
                get_report_dir(repo_root, args.installation, "init")
                / f"{args.init_run_id}.json"
            )
            if not init_file.is_file():
                sys.stderr.write(
                    f"Error: referenced init run '{args.init_run_id}' not found "
                    f"at {init_file}\n"
                )
                return 1
            intake_errors = evaluate_manage_intake(
                repo_root=repo_root,
                installation_id=args.installation,
                harness_id=args.harness,
                actor_id=args.actor,
                report_path=init_file,
            )
            if intake_errors:
                sys.stderr.write(
                    "Error: manage intake rejected "
                    f"({len(intake_errors)} issue(s)):\n"
                )
                for e in intake_errors:
                    sys.stderr.write(f"  * {e}\n")
                return 1

            extra_data = {}
            if args.data_file:
                with open(args.data_file, "r", encoding="utf-8") as f:
                    extra_data = json.load(f)

            report = create_manage_report(
                repo_root=repo_root,
                installation_id=args.installation,
                harness_id=args.harness,
                actor_id=args.actor,
                project_id=project_id,
                init_run_id=args.init_run_id,
                status=args.status,
                scenario=extra_data.get("scenario", {}),
                roles=extra_data.get(
                    "roles",
                    {
                        "preserved": [],
                        "created": [],
                        "adapted": [],
                        "archived": [],
                    },
                ),
                bindings=extra_data.get("bindings", {}),
                effective_capabilities=extra_data.get(
                    "effective_capabilities"
                ),
                checks=extra_data.get("checks"),
                remaining_manual_actions=extra_data.get(
                    "remaining_manual_actions"
                ),
            )

            try:
                saved_path = save_report(
                    repo_root, args.installation, "manage", report
                )
                if args.json:
                    print(
                        json.dumps(
                            {
                                "success": True,
                                "run_id": report["run_id"],
                                "report_path": str(
                                    saved_path.resolve()
                                ).replace("\\", "/"),
                                "status": report["status"],
                            }
                        )
                    )
                else:
                    print(
                        f"[OK] Manage report created: {saved_path} (run_id={report['run_id']})"
                    )
                return 0
            except Exception as e:
                sys.stderr.write(f"Error saving report: {e}\n")
                return 1

    elif args.subcommand == "latest":
        report_path, report_data = find_latest_report(
            repo_root, args.installation, args.type
        )
        if report_path is not None and report_data is None:
            # Latest attempt exists but is corrupted/foreign: surface it
            # instead of silently hiding it behind an older valid report.
            if args.json:
                print(
                    json.dumps(
                        {
                            "found": False,
                            "corrupted_latest_attempt": str(report_path),
                            "installation": args.installation,
                            "type": args.type,
                        }
                    )
                )
            else:
                print(
                    f"[FAIL] Latest {args.type} attempt for installation "
                    f"'{args.installation}' is corrupted or foreign: {report_path}"
                )
            return 1
        if not report_path or not report_data:
            if args.json:
                print(
                    json.dumps(
                        {
                            "found": False,
                            "installation": args.installation,
                            "type": args.type,
                        }
                    )
                )
            else:
                print(
                    f"No {args.type} report found for installation '{args.installation}'"
                )
            return 1

        is_stale, stale_info = check_report_staleness(repo_root, report_data)
        if args.json:
            print(
                json.dumps(
                    {
                        "found": True,
                        "report_path": str(report_path.resolve()).replace(
                            "\\", "/"
                        ),
                        "run_id": report_data.get("run_id"),
                        "status": report_data.get("status"),
                        "created_at": report_data.get("created_at"),
                        "is_stale": is_stale,
                        "staleness_reasons": stale_info.get("reasons", []),
                        "current_commit": stale_info.get("current_commit"),
                        "report_commit": stale_info.get("report_commit"),
                    }
                )
            )
        else:
            print(f"[OK] Latest {args.type} report: {report_path}")
            print(f"  * Run ID:     {report_data.get('run_id')}")
            print(f"  * Status:     {report_data.get('status')}")
            print(f"  * Created At: {report_data.get('created_at')}")
            if is_stale:
                reasons = "; ".join(
                    stale_info.get("reasons")
                    or [f"commit {stale_info.get('report_commit')} != HEAD {stale_info.get('current_commit')}"]
                )
                print(f"  * Staleness:  STALE ({reasons})")
            else:
                print("  * Staleness:  UP TO DATE (matches repository HEAD)")
        return 0

    elif args.subcommand == "verify":
        report_path = Path(args.report)
        if not report_path.exists():
            sys.stderr.write(f"Error: File '{report_path}' not found\n")
            return 1
        try:
            with open(report_path, "r", encoding="utf-8") as f:
                data = json.load(f)
            errors = validate_report(data, expected_type=args.type)
            if errors:
                if args.json:
                    print(
                        json.dumps(
                            {
                                "valid": False,
                                "report": str(report_path),
                                "errors": errors,
                            }
                        )
                    )
                else:
                    print(
                        f"[FAIL] Invalid report '{report_path}':\n"
                        + "\n".join(f"  * {e}" for e in errors)
                    )
                return 1
            if args.json:
                print(
                    json.dumps(
                        {
                            "valid": True,
                            "report": str(report_path),
                            "report_type": data.get("report_type"),
                            "run_id": data.get("run_id"),
                            "status": data.get("status"),
                        }
                    )
                )
            else:
                print(f"[OK] Report '{report_path}' is valid.")
            return 0
        except Exception as e:
            sys.stderr.write(f"Error reading report: {e}\n")
            return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
