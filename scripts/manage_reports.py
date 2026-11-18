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
import re
import subprocess
import sys
from typing import Any, Dict, List, Optional, Set, Tuple
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

try:  # shared helpers (ADR #22): package-style import (tests: from
    # scripts.manage_reports import ...)
    from scripts._common import _error_envelope, _utc_now_iso
except ImportError:  # direct script execution: python scripts/manage_reports.py
    from _common import _error_envelope, _utc_now_iso

# Wave F M10: the exact supported schema enumeration. The previous
# major-prefix acceptance ("1.*") let unsupported versions such as
# "1.9.9" through validation.
SUPPORTED_SCHEMA_VERSIONS = {"1.0", "1.1", "1.0.0", "1.1.0"}
VALID_REPORT_TYPES = {"init", "manage", "update", "tune"}
VALID_REPORT_STATUSES = {"completed", "awaiting_decision", "blocked"}
VALID_TUNE_DECISIONS = {"proposed", "accepted", "rejected"}

# Wave F M3: canonical staleness reason for an unverifiable git state.
GIT_STATE_UNAVAILABLE_REASON = "git state unavailable"

_SHA256_HEX_RE = re.compile(r"^[0-9a-f]{64}$")
# Wave F M2: a commit SHA handed to a git subprocess must be exactly a
# 40-character hex digest; anything else (notably values starting with
# "--") would be parsed as a git OPTION (argv injection).
_COMMIT_SHA_RE = re.compile(r"^[0-9a-fA-F]{40}$")

# Essential decision inputs fingerprinted into every report (audit §5
# freshness). Reports and runtime logs are intentionally excluded.
ESSENTIAL_INPUT_PATHS = (
    ".agents/meta-project.json",
    ".agents/project_knowledge/boundaries.md",
    "AGENTS.md",
)

# The registry is the one essential input a manage operation may legitimately
# change (it deploys roles + bindings). Wave D T2.1 admits a registry delta
# ONLY when it is exactly covered by the operation's declared changes[].
REGISTRY_PATH = ".agents/meta-project.json"

# Declared-delta vocabulary (Wave D T2.1): the only registry mutations a
# manage operation may declare and have admitted at intake.
VALID_MANAGE_CHANGE_KINDS = {"add_role", "disable", "archive"}
MANAGE_CHANGE_FIELDS = {"kind", "role_id", "installation", "path"}


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


def _require_safe_identifier(value: str, label: str) -> str:
    """
    Path safety for identifiers that build report paths (audit §5, Wave F
    SEC-3): non-empty, a bare filename (no directory components), no '..'
    traversal, no path separators. The exact contract of the run_id check
    in save_report, applied to installation_id / init_run_id /
    report_type. Raises ValueError on refusal.
    """
    value = str(value)
    if (
        not value
        or value != Path(value).name
        or ".." in value
        or "/" in value
        or "\\" in value
    ):
        raise ValueError(f"Unsafe {label} refused: {value!r}")
    return value


def _require_commit_sha(commit_sha: str, context: str) -> Optional[str]:
    """
    Wave F M2: validate a commit SHA BEFORE it is handed to a git
    subprocess. Returns an error message on refusal, None when safe.
    Values starting with '-' would be parsed as git options (argv
    injection class).
    """
    sha = str(commit_sha or "").strip()
    if not _COMMIT_SHA_RE.fullmatch(sha):
        return (
            f"unsafe commit_sha refused in {context}: {commit_sha!r} "
            "(must be a 40-character hex SHA; values starting with '-' "
            "would be parsed as git options)"
        )
    return None


def get_git_state(repo_root: Path) -> Dict[str, Any]:
    """
    Extract current git state without external dependencies.

    Wave F M3 (fail-closed): any git subprocess failure (exception or
    nonzero exit at any of the three call sites) is recorded in
    'git_state_error' instead of being silently swallowed into a fake
    'clean / unknown-commit' state. 'is_clean' is False whenever the
    state could not be fully determined — an unqueryable repository is
    never reported as clean.
    """
    commit_sha = "unknown"
    branch = "unknown"
    is_clean = True
    modified_files: List[str] = []
    errors: List[str] = []

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
        else:
            errors.append(f"git rev-parse HEAD failed (rc={proc.returncode})")
    except Exception as exc:
        errors.append(f"git rev-parse HEAD failed: {exc}")

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
        else:
            errors.append(
                f"git rev-parse --abbrev-ref HEAD failed (rc={proc.returncode})"
            )
    except Exception as exc:
        errors.append(f"git rev-parse --abbrev-ref HEAD failed: {exc}")

    try:
        proc = subprocess.run(
            ["git", "status", "--porcelain", "-z"],
            cwd=str(repo_root),
            capture_output=True,
            timeout=5,
        )
        if proc.returncode == 0:
            raw_entries = proc.stdout.split(b"\x00")
            # Wave F M9: porcelain -z rename/copy entries carry TWO path
            # fields — "XY NEW\0OLD\0" (non-z form: "R old -> new"). The
            # field following a rename/copy entry is the ORIGIN path with
            # no XY prefix; it must be consumed as the rename source and
            # never parsed as an independent entry (which previously left
            # sliced garbage in modified_files). The recorded path is the
            # NEW working-tree path so staleness sees renamed inputs.
            pending_origin = False
            for entry in raw_entries:
                if not entry:
                    continue
                if pending_origin:
                    pending_origin = False
                    continue
                status_letters = entry[:2]
                path_str = entry[3:].decode("utf-8", errors="replace")
                if path_str:
                    modified_files.append(path_str.replace("\\", "/"))
                if len(entry) >= 2 and any(
                    letter in (ord("R"), ord("C"))
                    for letter in status_letters
                ):
                    pending_origin = True
            is_clean = len(modified_files) == 0
        else:
            errors.append(
                f"git status --porcelain failed (rc={proc.returncode})"
            )
    except Exception as exc:
        errors.append(f"git status --porcelain failed: {exc}")

    if errors:
        is_clean = False

    return {
        "commit_sha": commit_sha,
        "branch": branch,
        "is_clean": is_clean,
        "modified_files": modified_files,
        "git_state_error": "; ".join(errors) if errors else None,
    }


def _validate_input_fingerprints(fingerprints: Any) -> List[str]:
    """
    Shape validation for the input_fingerprints mapping (Wave F M6): must
    be non-empty and cover every essential input; values are sha256 hex
    digests or null (null = file absent at fingerprinting time).
    """
    problems: List[str] = []
    if not isinstance(fingerprints, dict) or not fingerprints:
        problems.append(
            "Completed init/manage report must carry a non-empty "
            "'input_fingerprints' mapping of essential decision inputs "
            "(audit §5 freshness; Wave F M6)"
        )
        return problems
    missing = [
        rel for rel in ESSENTIAL_INPUT_PATHS if rel not in fingerprints
    ]
    if missing:
        problems.append(
            "input_fingerprints must cover the essential decision inputs; "
            "missing: " + ", ".join(missing)
        )
    malformed = [
        str(rel)
        for rel, value in fingerprints.items()
        if not (
            value is None
            or (
                isinstance(value, str)
                and _SHA256_HEX_RE.fullmatch(value)
            )
        )
    ]
    if malformed:
        problems.append(
            "input_fingerprints values must be null or a 64-character "
            "lowercase sha256 hex digest; malformed: "
            + ", ".join(sorted(malformed))
        )
    return problems


def validate_report(
    data: Dict[str, Any],
    expected_type: Optional[str] = None,
    allow_legacy_fingerprints: bool = False,
) -> List[str]:
    """
    Validate report structure and schemas against fail-closed invariants.

    Wave F M6: completed init/manage reports must carry
    'input_fingerprints' covering the essential decision inputs. The check
    is STRICT by default (new reports: verify CLI, save_report, intake).
    Backward compatibility: callers that merely READ persisted reports
    (report directory resolution via find_latest_report) pass
    allow_legacy_fingerprints=True so legacy completed reports created
    before fingerprinting remain readable — they are surfaced with their
    recorded data and judged by the staleness legacy safeguards instead of
    being hidden as corrupt documents. update/tune reports are unaffected
    this lane.
    """
    errors: List[str] = []

    if not isinstance(data, dict):
        return ["Report content must be a JSON object"]

    schema_version = str(data.get("schema_version", "")).strip()
    if not schema_version:
        errors.append("Missing required field 'schema_version'")
    elif schema_version not in SUPPORTED_SCHEMA_VERSIONS:
        errors.append(
            f"Unsupported schema_version: '{schema_version}' "
            f"(allowed: {sorted(SUPPORTED_SCHEMA_VERSIONS)})"
        )

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

    # Wave F M6: freshness evidence is mandatory for completed init/manage
    # reports — without input_fingerprints the essential decision inputs
    # (registry, boundaries, passport) cannot be checked for drift.
    if (
        report_type in ("init", "manage")
        and status == "completed"
        and not allow_legacy_fingerprints
    ):
        errors.extend(_validate_input_fingerprints(data.get("input_fingerprints")))

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
    """
    Return destination directory for reports.

    Wave F SEC-3: installation_id and report_type are validated exactly
    like run_id before any path is built, so a hostile identifier cannot
    escape the reports directory — and cannot cause directory creation
    outside the repository via save_report's mkdir.
    """
    _require_safe_identifier(installation_id, "installation_id")
    _require_safe_identifier(report_type, "report_type")
    return repo_root / ".agents" / "reports" / installation_id / report_type


def save_report(
    repo_root: Path,
    installation_id: str,
    report_type: str,
    report_data: Dict[str, Any],
) -> Path:
    """
    Atomically write report to disk and update latest.json pointer.
    Raises ValueError if data is invalid, an identifier (run_id,
    installation_id) is unsafe, or the run already exists (audit §5:
    existing runs must never be overwritten — the guard is the atomic
    O_EXCL reservation below, Wave F M5).
    """
    errors = validate_report(report_data, expected_type=report_type)
    if errors:
        raise ValueError(f"Cannot save invalid report: {'; '.join(errors)}")

    run_id = str(report_data["run_id"])
    # Path safety (audit §5, Wave F SEC-3): identifiers must not escape
    # the report dir. installation_id is validated inside get_report_dir.
    _require_safe_identifier(run_id, "run_id")

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

    # 2. Wave F M5 overwrite-guard: atomically RESERVE the final name via
    # O_EXCL creation. The exists() check above is advisory (it races
    # across processes); the O_EXCL create is the authoritative
    # serialization point, so a second concurrent writer with the same
    # run_id fails loudly instead of silently clobbering the first
    # writer's report with its replace().
    try:
        reservation_fd = os.open(
            str(target_file), os.O_CREAT | os.O_EXCL | os.O_WRONLY
        )
    except FileExistsError:
        temp_file.unlink(missing_ok=True)
        raise ValueError(
            f"Refusing to overwrite existing run: {target_file} "
            "(each attempt must use a unique run_id)"
        ) from None
    os.close(reservation_fd)
    try:
        temp_file.replace(target_file)
    except Exception:
        # Best effort: do not leave a zero-byte reservation tombstone on a
        # failed publish. A tombstone left by a hard kill is surfaced by
        # find_latest_report as a corrupted attempt and is never silently
        # overwritten (audit §5).
        try:
            target_file.unlink()
        except OSError:
            pass
        raise

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
    """
    Load a report file and return it when structurally valid, else None.

    Wave F M6 backward compatibility: this is the READ path for persisted
    reports, so legacy completed init/manage reports created before
    fingerprinting remain readable (allow_legacy_fingerprints=True) —
    find_latest_report must still surface them, and staleness applies its
    legacy safeguards. Strictness for NEW reports lives in the default
    validate_report (verify CLI, save_report, intake).
    """
    try:
        with open(path, "r", encoding="utf-8") as f:
            data = json.load(f)
    except Exception:
        return None
    if validate_report(
        data, expected_type=expected_type, allow_legacy_fingerprints=True
    ):
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

    Pointer resolution is authoritative when latest.json is intact. The
    fallback (no usable pointer) resolves the newest attempt by file
    mtime with the report file name as the final deterministic tiebreak
    (Wave F M4): generate_run_id embeds a 1-second timestamp plus a random
    UUID suffix, so two same-second runs sort randomly by name alone —
    mtime restores creation order while keeping the resolution fully
    deterministic for a given directory state. Files whose mtime cannot
    be stat'ed sort oldest.
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

    # No usable pointer: newest attempt by mtime (creation order) with the
    # file name as the final deterministic tiebreak (Wave F M4 — see the
    # docstring; name-only ordering is random for same-second run_ids).
    def _recency_key(p: Path) -> Tuple[int, str]:
        try:
            return (p.stat().st_mtime_ns, p.name)
        except OSError:
            return (0, p.name)

    candidates = sorted(
        [p for p in target_dir.glob("*.json") if p.name != "latest.json"],
        key=_recency_key,
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
    now_iso = _utc_now_iso()

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
    changes: Optional[List[Dict[str, Any]]] = None,
    run_id: Optional[str] = None,
) -> Dict[str, Any]:
    """Helper to assemble a canonical manage report dictionary."""
    git_state = get_git_state(repo_root)
    actual_run_id = run_id or generate_run_id("manage")
    now_iso = _utc_now_iso()

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
        # Wave D T2.1 formalization: the declared registry delta this
        # operation applied (add_role/disable/archive with ids/paths).
        "changes": list(changes or []),
        "effective_capabilities": effective_capabilities
        or {"team_readiness": "ready"},
        "checks": checks or [],
        "remaining_manual_actions": remaining_manual_actions or [],
    }


def create_tune_report(
    repo_root: Path,
    installation_id: str,
    harness_id: str,
    actor_id: str,
    project_id: str,
    status: str,
    proposals: List[Dict[str, Any]],
    checks: Optional[List[Dict[str, Any]]] = None,
    remaining_manual_actions: Optional[List[str]] = None,
    run_id: Optional[str] = None,
) -> Dict[str, Any]:
    """Helper to assemble a canonical tune report dictionary (Wave D T2.2)."""
    git_state = get_git_state(repo_root)
    actual_run_id = run_id or generate_run_id("tune")
    now_iso = _utc_now_iso()

    return {
        "schema_version": "1.0.0",
        "report_type": "tune",
        "project_id": project_id,
        "installation_id": installation_id,
        "harness_id": harness_id,
        "actor_id": actor_id,
        "run_id": actual_run_id,
        "created_at": now_iso,
        "status": status,
        "git_state": git_state,
        "input_fingerprints": collect_input_fingerprints(repo_root),
        "proposals": proposals,
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
    Wave F M3: a git subprocess failure on either side (current repository
    or recorded in the report) makes freshness uncertifiable — the report
    is treated as stale/suspect with the canonical reason
    'git state unavailable' (STALE_INPUT envelope on the CLI).
    Returns (is_stale, details).
    """
    current_git = get_git_state(repo_root)
    current_sha = current_git.get("commit_sha", "unknown")
    report_git = report_data.get("git_state", {})
    report_sha = report_git.get("commit_sha", "unknown")

    reasons: List[str] = []

    # Wave F M3: fail-closed git state — never certify fresh when git
    # could not be consulted (now) or was already failing at creation.
    current_err = current_git.get("git_state_error")
    if current_err:
        reasons.append(
            f"{GIT_STATE_UNAVAILABLE_REASON}: current repository git state "
            f"could not be determined ({current_err})"
        )
    report_err = (
        report_git.get("git_state_error")
        if isinstance(report_git, dict)
        else None
    )
    if report_err:
        reasons.append(
            f"{GIT_STATE_UNAVAILABLE_REASON}: report recorded a git "
            f"failure at creation ({report_err})"
        )
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


def _registry_identity_delta(
    old_registry: Any, new_registry: Any
) -> Set[Tuple[str, str, str]]:
    """
    Structural identity delta between two registry documents, expressed in
    the declared-change vocabulary: (kind, role_id, installation) tuples.
    Binding mutations the vocabulary cannot express (actor re-pointing,
    planned -> active promotions, field edits) surface as 'mutate_role'
    elements, which no declaration may cover (fail-closed).
    """
    old_roles = (
        old_registry.get("roles")
        if isinstance(old_registry, dict)
        and isinstance(old_registry.get("roles"), dict)
        else {}
    )
    new_roles = (
        new_registry.get("roles")
        if isinstance(new_registry, dict)
        and isinstance(new_registry.get("roles"), dict)
        else {}
    )
    delta: Set[Tuple[str, str, str]] = set()
    for role_id in set(old_roles) | set(new_roles):
        old_role = old_roles.get(role_id)
        new_role = new_roles.get(role_id)
        old_bindings = (
            old_role.get("bindings")
            if isinstance(old_role, dict)
            and isinstance(old_role.get("bindings"), dict)
            else {}
        )
        new_bindings = (
            new_role.get("bindings")
            if isinstance(new_role, dict)
            and isinstance(new_role.get("bindings"), dict)
            else {}
        )
        for inst_id in set(old_bindings) | set(new_bindings):
            old_binding = old_bindings.get(inst_id)
            new_binding = new_bindings.get(inst_id)
            if old_binding is None and new_binding is not None:
                delta.add(("add_role", role_id, inst_id))
            elif new_binding is None and old_binding is not None:
                delta.add(("archive", role_id, inst_id))
            elif old_binding != new_binding:
                old_status = (old_binding or {}).get("status")
                new_status = (new_binding or {}).get("status")
                if old_status == "active" and new_status != "active":
                    delta.add(("disable", role_id, inst_id))
                else:
                    delta.add(("mutate_role", role_id, inst_id))
    return delta


def _normalize_declared_changes(
    declared_changes: Any,
) -> Tuple[Set[Tuple[str, str, Optional[str]]], List[str]]:
    """
    Validate and normalize a declared changes[] list into (kind, role_id,
    installation) tuples (installation None = wildcard for that pair).
    Returns (declared_set, problems); a non-empty problems list refuses.
    """
    problems: List[str] = []
    declared: Set[Tuple[str, str, Optional[str]]] = set()
    if not isinstance(declared_changes, list) or not declared_changes:
        return set(), ["changes[] must be a non-empty list of change objects"]
    for idx, entry in enumerate(declared_changes):
        label = f"changes[{idx}]"
        if not isinstance(entry, dict):
            problems.append(f"{label} must be an object")
            continue
        unknown = sorted(set(entry) - MANAGE_CHANGE_FIELDS)
        if unknown:
            problems.append(
                f"{label} carries unsupported fields: {', '.join(unknown)} "
                "(allowed: kind, role_id, installation, path)"
            )
        kind = str(entry.get("kind", "")).strip()
        role_id = str(entry.get("role_id", "")).strip()
        installation = str(entry.get("installation", "")).strip()
        if kind not in VALID_MANAGE_CHANGE_KINDS:
            problems.append(
                f"{label}.kind '{kind}' is unsupported (allowed: "
                + ", ".join(sorted(VALID_MANAGE_CHANGE_KINDS))
                + ")"
            )
        if not role_id:
            problems.append(f"{label}.role_id is missing or empty")
        path = entry.get("path")
        if path is not None and not isinstance(path, str):
            problems.append(f"{label}.path must be a string")
        if kind in VALID_MANAGE_CHANGE_KINDS and role_id:
            declared.add((kind, role_id, installation or None))
    return declared, problems


def _recover_registry_baseline(
    repo_root: Path, commit_sha: str
) -> Tuple[Optional[bytes], Optional[str]]:
    """
    Registry bytes as committed at the init report's commit (git only,
    per the zero-dependency policy: stdlib + Git). Wave F M2: the commit
    SHA is validated (40-character hex) BEFORE it reaches the git
    subprocess — option-like values (argv injection) refuse here.
    Returns (bytes, None) or (None, error_message).
    """
    sha_error = _require_commit_sha(commit_sha, "git baseline lookup")
    if sha_error:
        return None, sha_error
    try:
        proc = subprocess.run(
            ["git", "show", f"{commit_sha}:{REGISTRY_PATH}"],
            cwd=str(repo_root),
            capture_output=True,
            timeout=15,
        )
    except Exception as exc:
        return None, f"git baseline lookup failed: {exc}"
    if proc.returncode != 0:
        stderr = (proc.stderr or b"").decode("utf-8", errors="replace").strip()
        first_line = stderr.splitlines()[0] if stderr else "git show failed"
        return None, (
            f"registry '{REGISTRY_PATH}' not found in the git tree at report "
            f"commit '{commit_sha[:8]}' ({first_line})"
        )
    return proc.stdout, None


def verify_declared_registry_delta(
    repo_root: Path,
    report_data: Dict[str, Any],
    declared_changes: Any,
) -> Optional[str]:
    """
    Wave D T2.1: registry delta admission for manage intake.

    A registry unchanged since the init report is admitted. A changed
    registry is admitted ONLY when the delta is EXACTLY covered by the
    operation's declared changes[] (add_role/disable/archive with role ids
    and installations): the baseline registry is recovered from git at the
    report's recorded commit and hash-verified against the report's
    recorded fingerprint, so deltas rooted in uncommitted init-time state
    refuse instead of silently admitting. Every registry element not named
    by a declaration — and every declaration without a matching registry
    element — is refused with an explicit diff listing.

    Returns None when admitted, an intake refusal message otherwise.
    """
    fingerprints = report_data.get("input_fingerprints")
    recorded = (
        fingerprints.get(REGISTRY_PATH)
        if isinstance(fingerprints, dict)
        else None
    )
    registry_file = Path(repo_root) / REGISTRY_PATH
    current_bytes: Optional[bytes] = None
    if registry_file.is_file():
        try:
            current_bytes = registry_file.read_bytes()
        except OSError:
            current_bytes = None
    current_sha = (
        hashlib.sha256(current_bytes).hexdigest()
        if current_bytes is not None
        else None
    )

    if recorded is not None and recorded == current_sha:
        return None  # registry fresh
    if recorded is None and current_bytes is None:
        return None  # registry absent on both sides

    if not declared_changes:
        return (
            "Intake rejected: stale report (essential input changed since "
            f"report: {REGISTRY_PATH}; declare the registry delta via "
            "changes[] in the manage data file)"
        )

    declared, declaration_problems = _normalize_declared_changes(
        declared_changes
    )
    if declaration_problems:
        return (
            "Intake rejected: invalid changes[] declaration: "
            + "; ".join(declaration_problems)
        )

    git_state = report_data.get("git_state") or {}
    report_sha = str(git_state.get("commit_sha", "unknown")).strip()
    if recorded is None:
        return (
            "Intake rejected: cannot verify declared registry delta: the "
            "init report records no registry fingerprint; re-run init"
        )
    if not report_sha or report_sha == "unknown":
        return (
            "Intake rejected: cannot verify declared registry delta: the "
            "init report carries no commit reference; re-run init"
        )
    # Wave F M2: the report-recorded SHA is validated before the git
    # subprocess — option-like values (argv injection) refuse here.
    sha_error = _require_commit_sha(report_sha, "init report git_state")
    if sha_error:
        return (
            "Intake rejected: cannot verify declared registry delta: "
            + sha_error
        )

    baseline_bytes, baseline_err = _recover_registry_baseline(
        repo_root, report_sha
    )
    if baseline_err:
        return (
            "Intake rejected: cannot verify declared registry delta: "
            + baseline_err
        )
    # Sanity anchor: the baseline must be the registry state the report
    # fingerprinted. core.autocrlf environments store LF-normalized blobs
    # while Windows working trees carry CRLF, so the recorded fingerprint
    # (taken from raw working-tree bytes) may correspond to either EOL form
    # of the blob. Accept a match on the raw bytes, their LF-normalized
    # form, or their CRLF form; the delta verification below parses JSON,
    # which is line-ending agnostic — the hash only guards against
    # baselines rooted in uncommitted init-time state.
    lf_bytes = baseline_bytes.replace(b"\r\n", b"\n")
    baseline_variants = {
        hashlib.sha256(baseline_bytes).hexdigest(),
        hashlib.sha256(lf_bytes).hexdigest(),
        hashlib.sha256(lf_bytes.replace(b"\n", b"\r\n")).hexdigest(),
    }
    if recorded not in baseline_variants:
        return (
            "Intake rejected: cannot verify declared registry delta: "
            f"registry baseline at report commit '{report_sha[:8]}' does "
            "not match the report's recorded fingerprint (uncommitted "
            "registry changes at init time); re-run init"
        )

    try:
        baseline_registry = json.loads(baseline_bytes.decode("utf-8"))
        current_registry = (
            json.loads(current_bytes.decode("utf-8"))
            if current_bytes is not None
            else {}
        )
    except Exception as exc:
        return (
            "Intake rejected: cannot verify declared registry delta: "
            f"registry content is not valid JSON: {exc}"
        )

    delta = _registry_identity_delta(baseline_registry, current_registry)
    declared_full = {(k, r, i) for (k, r, i) in declared}
    declared_wild = {(k, r) for (k, r, i) in declared if i is None}

    def _covered(element: Tuple[str, str, str]) -> bool:
        kind, role_id, installation = element
        if (kind, role_id, installation) in declared_full:
            return True
        return (kind, role_id) in declared_wild

    uncovered = sorted(el for el in delta if not _covered(el))
    matched_wild_pairs = {
        (k, r) for (k, r, _i) in delta if (k, r) in declared_wild
    }
    unmatched: List[Tuple[str, str, Optional[str]]] = []
    unmatched.extend(sorted(declared_full - delta))
    unmatched.extend(
        sorted(
            (k, r, None) for (k, r) in declared_wild - matched_wild_pairs
        )
    )

    if not uncovered and not unmatched:
        return None

    def _describe(kind: str, role_id: str, installation: Optional[str]) -> str:
        suffix = (
            f" (installation '{installation}')"
            if installation
            else " (any installation)"
        )
        return f"{kind} role '{role_id}'{suffix}"

    listings: List[str] = []
    for kind, role_id, installation in uncovered:
        listings.append(f"uncovered registry delta: {_describe(kind, role_id, installation)}")
    for kind, role_id, installation in unmatched:
        listings.append(
            f"declared change with no matching registry delta: "
            f"{_describe(kind, role_id, installation)}"
        )
    return (
        "Intake rejected: registry delta not covered by declared "
        "changes[]: " + "; ".join(listings)
    )


def evaluate_manage_intake(
    repo_root: Path,
    installation_id: str,
    harness_id: str,
    actor_id: str,
    report_path: Path,
    declared_changes: Optional[List[Dict[str, Any]]] = None,
) -> List[str]:
    """
    Intake admission for the init -> manage contract (audit §5, Wave D T2.1).

    Separates two mechanisms: format validation (is the report a correct
    document?) and intake admission (may THIS document authorize a manage
    operation in THIS project/installation?). A blocked report can be a
    correct document and must still be refused.

    The registry (`.agents/meta-project.json`) is the one essential input a
    manage operation may legitimately change (it deploys roles + bindings).
    A registry change since the init report is admitted only when it is
    EXACTLY covered by `declared_changes` (the manage operation's changes[]
    list, add_role/disable/archive with role ids/installations) — see
    `verify_declared_registry_delta`. Staleness for the other essential
    inputs (boundaries.md, AGENTS.md) is unaffected; commit movement alone
    no longer refuses (supersession by a newer init run is checked instead).

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

    # 7. Freshness (Wave D T2.1): content freshness of the non-registry
    # essential inputs stays mandatory — uncommitted changes to
    # boundaries.md/AGENTS.md make the report stale. The registry delta is
    # NOT a blanket staleness trigger; it is admitted via declared
    # changes[] in step 7c.
    fingerprints = data.get("input_fingerprints")
    if isinstance(fingerprints, dict) and fingerprints:
        current_fps = collect_input_fingerprints(repo_root)
        for rel in sorted(fingerprints):
            if rel == REGISTRY_PATH:
                continue  # handled by declared-delta admission (7c)
            if current_fps.get(rel) != fingerprints.get(rel):
                errors.append(
                    "Intake rejected: stale report (essential input changed "
                    f"since report: {rel})"
                )
    else:
        # Report predates fingerprinting: freshness of essential inputs is
        # unverifiable; treat dirty non-registry essential inputs as stale.
        current_git = get_git_state(repo_root)
        dirty = [
            rel
            for rel in ESSENTIAL_INPUT_PATHS
            if rel != REGISTRY_PATH
            and rel in current_git.get("modified_files", [])
        ]
        if dirty:
            errors.append(
                "Intake rejected: stale report (essential inputs have "
                "uncommitted changes while the report carries no "
                "fingerprints: " + ", ".join(dirty) + ")"
            )

    # 7b. Supersession (Wave D T2.1): a newer init run for this installation
    # supersedes the referenced report. This replaces the removed
    # commit-moved staleness trigger: pure HEAD movement with fresh inputs
    # is admitted, but planning against an outdated initialization is not.
    latest_path, latest_data = find_latest_report(
        repo_root, installation_id, "init"
    )
    if latest_path is not None:
        referenced_id = str(data.get("run_id", "")).strip()
        latest_id = (
            str(latest_data.get("run_id", "")).strip()
            if isinstance(latest_data, dict)
            else ""
        ) or latest_path.stem
        if latest_id and latest_id != referenced_id:
            errors.append(
                "Intake rejected: init report is superseded by a newer init "
                f"run '{latest_id}' ({latest_path}); re-plan against the "
                "current initialization"
            )

    # 7c. Registry delta admission (Wave D T2.1): unchanged registry passes;
    # a changed registry must be EXACTLY covered by the declared changes[].
    registry_error = verify_declared_registry_delta(
        repo_root, data, declared_changes
    )
    if registry_error:
        errors.append(registry_error)

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

    # 5. tune subcommand (Wave D T2.2: friction 1 — tune reports were
    # validatable but had no CLI to create them)
    p_tune = subparsers.add_parser(
        "tune", help="Manage tuning (tune) reports"
    )
    p_tune.add_argument(
        "--action", choices=["create", "verify"], required=True
    )
    p_tune.add_argument(
        "--repo-root", default=".", help="Target repository root path"
    )
    p_tune.add_argument(
        "--installation", help="Target installation ID (e.g. ag-main)"
    )
    p_tune.add_argument("--harness", help="Harness ID (e.g. antigravity)")
    p_tune.add_argument("--actor", help="Qualified actor_id")
    p_tune.add_argument(
        "--project-id",
        default=None,
        help="Project ID (default: resolved from the local registry, never hardcoded)",
    )
    p_tune.add_argument(
        "--status",
        choices=["completed", "awaiting_decision", "blocked"],
        default="completed",
    )
    p_tune.add_argument(
        "--data-file",
        help="Path to JSON file containing proposals[] and checks",
    )
    p_tune.add_argument(
        "--report", help="Path to report file for verification"
    )
    p_tune.add_argument(
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


def _emit_invalid_report_envelope(report_path: Path, errors: List[str]) -> None:
    """Writes the INVALID_REPORT envelope (Wave E) for a failed report
    verification. Presentation only: verdicts and rc are unchanged."""
    first = errors[0] if errors else "unknown validation error"
    sys.stderr.write(_error_envelope(
        "INVALID_REPORT",
        str(report_path),
        "report file failed schema validation",
        "a JSON report matching the canonical report schema",
        f"{len(errors)} validation error(s), first: {first}",
        "regenerate the report via the matching '<type> create' action, or "
        "repair the file to satisfy the errors listed in the verdict",
    ) + "\n")


def _emit_stale_input_envelope(report_path: Path, reason: str) -> None:
    """Writes the STALE_INPUT envelope (Wave E format) when a report's
    freshness cannot be certified because git state is unavailable
    (Wave F M3). Presentation only: verdicts and rc are unchanged."""
    sys.stderr.write(_error_envelope(
        "STALE_INPUT",
        str(report_path),
        f"report freshness cannot be certified: {reason}",
        "a report whose recorded git state can be verified against the "
        "current repository git state",
        "git state unavailable: freshness is suspect, not certified",
        "restore git operation (or resolve the recorded git failure), then "
        "re-run 'latest' to re-certify freshness",
    ) + "\n")


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
                    _emit_invalid_report_envelope(report_path, errors)
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
                sys.stderr.write(_error_envelope(
                    "INVALID_REPORT",
                    str(report_path),
                    "report file is not readable/parsable JSON",
                    "a JSON report file loadable as UTF-8 JSON",
                    f"read/parse error: {e}",
                    "regenerate the report via the matching '<type> create' "
                    "action, or repair the file encoding/JSON syntax",
                ) + "\n")
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
                    _emit_invalid_report_envelope(report_path, errors)
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
                sys.stderr.write(_error_envelope(
                    "INVALID_REPORT",
                    str(report_path),
                    "report file is not readable/parsable JSON",
                    "a JSON report file loadable as UTF-8 JSON",
                    f"read/parse error: {e}",
                    "regenerate the report via the matching '<type> create' "
                    "action, or repair the file encoding/JSON syntax",
                ) + "\n")
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

            # Load the operation payload BEFORE intake: its declared
            # changes[] participate in the admission decision (Wave D T2.1).
            extra_data: Dict[str, Any] = {}
            if args.data_file:
                try:
                    with open(args.data_file, "r", encoding="utf-8") as f:
                        extra_data = json.load(f)
                except Exception as e:
                    sys.stderr.write(
                        f"Error: cannot read --data-file "
                        f"'{args.data_file}': {e}\n"
                    )
                    return 1
            if not isinstance(extra_data, dict):
                sys.stderr.write(
                    "Error: --data-file content must be a JSON object\n"
                )
                return 1

            # Intake admission (audit §5 + Guardian Step 64 finding): a
            # manage operation is authorized ONLY by an admitted init report
            # for this installation. The referenced init run must exist and
            # pass evaluate_manage_intake; hand-built or foreign init
            # references fail closed. A registry delta since the init report
            # is admitted only when exactly covered by the declared
            # changes[] below (Wave D T2.1).
            # Wave F SEC-3: identifiers that build report paths are
            # validated BEFORE any path is constructed or read, so a
            # hostile --installation/--init-run-id cannot probe or read
            # outside the reports directory.
            try:
                _require_safe_identifier(args.installation, "installation_id")
                _require_safe_identifier(args.init_run_id, "init_run_id")
            except ValueError as exc:
                sys.stderr.write(f"Error: {exc}\n")
                return 2
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
                declared_changes=extra_data.get("changes"),
            )
            if intake_errors:
                sys.stderr.write(
                    "Error: manage intake rejected "
                    f"({len(intake_errors)} issue(s)):\n"
                )
                for e in intake_errors:
                    sys.stderr.write(f"  * {e}\n")
                return 1

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
                changes=extra_data.get("changes") or [],
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

    elif args.subcommand == "tune":
        # Wave D T2.2: tune report CLI (friction 1). Tune output is
        # read-only analysis evidence; creation mirrors init/manage flags
        # and requires a non-empty proposals[] payload.
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
                errors = validate_report(data, expected_type="tune")
                if errors:
                    _emit_invalid_report_envelope(report_path, errors)
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
                            f"[FAIL] Invalid tune report '{report_path}':\n"
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
                    print(f"[OK] Tune report '{report_path}' is valid.")
                return 0
            except Exception as e:
                sys.stderr.write(_error_envelope(
                    "INVALID_REPORT",
                    str(report_path),
                    "report file is not readable/parsable JSON",
                    "a JSON report file loadable as UTF-8 JSON",
                    f"read/parse error: {e}",
                    "regenerate the report via the matching '<type> create' "
                    "action, or repair the file encoding/JSON syntax",
                ) + "\n")
                sys.stderr.write(f"Error reading report: {e}\n")
                return 1

        elif args.action == "create":
            if not args.installation or not args.harness or not args.actor:
                sys.stderr.write(
                    "Error: --installation, --harness, and --actor are required for tune create\n"
                )
                return 2
            project_id, pid_err = _resolve_project_id(repo_root, args.project_id)
            if pid_err:
                sys.stderr.write(f"Error: {pid_err}\n")
                return 2

            extra_data: Dict[str, Any] = {}
            if args.data_file:
                try:
                    with open(args.data_file, "r", encoding="utf-8") as f:
                        extra_data = json.load(f)
                except Exception as e:
                    sys.stderr.write(
                        f"Error: cannot read --data-file '{args.data_file}': {e}\n"
                    )
                    return 1
            if not isinstance(extra_data, dict):
                sys.stderr.write(
                    "Error: --data-file content must be a JSON object\n"
                )
                return 1

            proposals = extra_data.get("proposals")
            if not isinstance(proposals, list) or not proposals:
                sys.stderr.write(
                    "Error: --data-file must contain a non-empty "
                    "'proposals' list for tune create\n"
                )
                return 2

            report = create_tune_report(
                repo_root=repo_root,
                installation_id=args.installation,
                harness_id=args.harness,
                actor_id=args.actor,
                project_id=project_id,
                status=args.status,
                proposals=proposals,
                checks=extra_data.get("checks"),
                remaining_manual_actions=extra_data.get(
                    "remaining_manual_actions"
                ),
            )

            try:
                saved_path = save_report(
                    repo_root, args.installation, "tune", report
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
                        f"[OK] Tune report created: {saved_path} (run_id={report['run_id']})"
                    )
                return 0
            except Exception as e:
                sys.stderr.write(f"Error saving report: {e}\n")
                return 1

    elif args.subcommand == "latest":
        # Wave F SEC-3: an unsafe installation identifier refuses cleanly
        # (no traceback) before any report directory is resolved.
        try:
            report_path, report_data = find_latest_report(
                repo_root, args.installation, args.type
            )
        except ValueError as exc:
            sys.stderr.write(f"Error: {exc}\n")
            return 2
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
        # Wave F M3: git-state unavailability is surfaced with the
        # STALE_INPUT envelope (presentation only; rc unchanged).
        if is_stale and any(
            str(r).startswith(GIT_STATE_UNAVAILABLE_REASON)
            for r in stale_info.get("reasons", [])
        ):
            _emit_stale_input_envelope(report_path, GIT_STATE_UNAVAILABLE_REASON)
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
                _emit_invalid_report_envelope(report_path, errors)
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
            sys.stderr.write(_error_envelope(
                "INVALID_REPORT",
                str(report_path),
                "report file is not readable/parsable JSON",
                "a JSON report file loadable as UTF-8 JSON",
                f"read/parse error: {e}",
                "regenerate the report via the matching '<type> create' "
                "action, or repair the file encoding/JSON syntax",
            ) + "\n")
            sys.stderr.write(f"Error reading report: {e}\n")
            return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
