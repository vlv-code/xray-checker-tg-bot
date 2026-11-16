#!/usr/bin/env python3
"""
Managed Update Engine for the Meta-Project framework (scripts/update_meta_project.py)

Wave 0.9.0, Task C2. Deterministic, crash-recoverable update of an installed
framework (ADR #21 contracts):

* **SHA pinning without releases:** a ref (branch/tag/SHA) resolves to ONE
  exact commit SHA; every component is materialized from that single SHA
  (raw blobs via `git cat-file <sha>:<path>` — byte-exact, no EOL smudging);
  mixing revisions in one update is prohibited.
* **Manifest != lock:** the bundle manifest describes ALL available
  components; the project lock (``.agents/framework-lock.json``) records the
  actually-installed SUBSET with sha256 per component.
* **Integral cache fallback:** ``<cache_dir>/<commit_sha>/`` holds a full
  single-revision bundle; a partial cache is refused loudly
  (``PartialCacheError``), never mixed.
* **Three-way merge with base honesty:** for locally modified components the
  merge base is cached content whose sha256 matches the lock record; without
  a matching base the plan yields a ``needs_base`` entry, never a "safe
  merge" claim. ``local_repo_data``-marked blocks are re-injected into the
  merged content; unmarked local edits surface as explicit conflicts.
* **Red Zone refusal:** automated changes to protected components are
  refused: no sanctioned mechanism exists (explicitly open item).
* **Atomicity & recovery:** per-file atomic replace via temp + ``os.replace``,
  an operation journal (``.agents/update-journal.json``) with before/after
  sha256 (and content), and a restore mode that reverts ONLY the engine's own
  changes, keeping third-party edits made during the apply window.
* **check/plan are strictly read-only:** zero writes to the project, reports,
  cache, and lock files.
* **Legacy transition:** canonical runtime location is ``scripts/``; a
  divergent legacy copy under ``.agents/scripts/`` halts the update with a
  report of both paths and hashes.

Update reports are recorded through the canonical report engine
(``scripts/manage_reports.save_report`` with ``report_type="update"``).

Zero-dependency: Python 3.10+ stdlib + Git CLI only.
"""

from __future__ import annotations

import argparse
import base64
import datetime
import fnmatch
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from typing import Any, Dict, List, Optional, Tuple
import uuid

try:  # package-style import (tests: from scripts.update_meta_project import ...)
    from scripts.manage_reports import (
        collect_input_fingerprints,
        generate_run_id,
        get_git_state,
        save_report,
    )
except ImportError:  # direct script execution: python scripts/update_meta_project.py
    try:
        from manage_reports import (
            collect_input_fingerprints,
            generate_run_id,
            get_git_state,
            save_report,
        )
    except ImportError:  # report engine unavailable in this environment
        collect_input_fingerprints = None
        generate_run_id = None
        get_git_state = None
        save_report = None

# ---------------------------------------------------------------------------
# Contracts (ADR #21)
# ---------------------------------------------------------------------------

FRAMEWORK_LOCK_RELPATH = ".agents/framework-lock.json"
UPDATE_JOURNAL_RELPATH = ".agents/update-journal.json"
LOCK_SCHEMA_VERSION = "1.0.0"
REPORT_TYPE = "update"

DEFAULT_PROTECT_PATTERNS = (
    "scripts/check_boundaries.py",
    "*boundaries.md",
    ".github/workflows/**",
    ".github/CODEOWNERS",
    "tests/test_integrity.py",
    "LICENSE",
    ".gitignore",
)

# local_repo_data markers: <!-- local_repo_data:TAG --> ... <!-- /local_repo_data:TAG -->
LOCAL_REPO_DATA_RE = re.compile(
    rb"<!--\s*local_repo_data:([A-Za-z0-9_.:-]+)\s*-->"  # opener
    rb".*?"                                              # preserved local payload
    rb"<!--\s*/local_repo_data:\1\s*-->",                # closer (same tag)
    re.DOTALL,
)

RED_ZONE_REFUSAL_TEXT = (
    "no sanctioned mechanism exists for automated changes to protected "
    "components (Red-Zone sanction is an explicitly open item); apply a "
    "manual, user-confirmed change instead"
)


class UpdateEngineError(Exception):
    """Base class for managed-update engine failures."""


class SourceResolutionError(UpdateEngineError):
    """A ref could not be pinned to an exact commit SHA."""


class MixedBundleError(UpdateEngineError):
    """The bundle cannot be materialized as ONE integral revision."""


class PartialCacheError(MixedBundleError):
    """A cache entry exists but is not an integral bundle: refused."""


class BaseUnavailableError(UpdateEngineError):
    """No merge base matching the lock record is available."""


class LegacyDuplicateError(UpdateEngineError):
    """Divergent canonical/legacy copies of a managed utility: halt."""


class ProtectedComponentError(UpdateEngineError):
    """Refusal to touch a protected (Red Zone) component."""


class ApplyError(UpdateEngineError):
    """The atomic apply failed; the journal preserves the recovery state."""


# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------


def _sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _utc_now_iso() -> str:
    return datetime.datetime.now(datetime.timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )


def _posix(rel: Any) -> str:
    return str(rel).replace("\\", "/").strip("/")


def _run_git(args: List[str], text: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["git", *args],
        capture_output=True,
        text=text,
    )


def _atomic_write_bytes(path: Path, data: bytes) -> None:
    """Per-file atomic replace: write to a sibling temp file, then
    os.replace. On failure the temp file is cleaned up and the destination
    is left untouched."""
    tmp = path.parent / f".{path.name}.updtmp-{uuid.uuid4().hex[:8]}"
    try:
        with open(tmp, "wb") as handle:
            handle.write(data)
        os.replace(tmp, path)
    except OSError:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise


def _atomic_write_text(path: Path, text: str) -> None:
    _atomic_write_bytes(path, text.encode("utf-8"))


def _split_list(value: Optional[str]) -> List[str]:
    if not value:
        return []
    return [item.strip() for item in value.split(",") if item.strip()]


def _read_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def default_cache_dir() -> Path:
    """Cache resolution: explicit flag > env META_PROJECT_CACHE > OS cache
    path (Windows: %LOCALAPPDATA%/meta-project/cache; POSIX:
    ~/.cache/meta-project). The engine never creates or writes the cache."""
    env = os.environ.get("META_PROJECT_CACHE")
    if env:
        return Path(env)
    if os.name == "nt":
        base = os.environ.get("LOCALAPPDATA") or str(
            Path.home() / "AppData" / "Local"
        )
        return Path(base) / "meta-project" / "cache"
    return Path.home() / ".cache" / "meta-project"


# ---------------------------------------------------------------------------
# Source resolution and bundle materialization (fetch strictly by SHA)
# ---------------------------------------------------------------------------


def resolve_source(source: str, ref: str) -> Dict[str, str]:
    """Pin ``ref`` in ``source`` to ONE exact commit SHA.

    Returns a SourcePin dict: {commit_sha, origin_repo, ref}. Every bundle
    byte is later fetched from this SHA only — never from a moving ref.
    """
    repo = str(source)
    if not repo:
        raise SourceResolutionError("source must not be empty")
    if not str(ref or "").strip():
        raise SourceResolutionError("ref must not be empty")
    proc = _run_git(
        ["-C", repo, "rev-parse", "--verify", f"{ref}^{{commit}}"]
    )
    if proc.returncode != 0:
        raise SourceResolutionError(
            f"cannot resolve ref '{ref}' to an exact commit in source "
            f"repository '{repo}': {(proc.stderr or '').strip()}"
        )
    sha = (proc.stdout or "").strip().lower()
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise SourceResolutionError(
            f"ref '{ref}' in '{repo}' did not resolve to a 40-hex commit SHA "
            f"(got: {sha!r})"
        )
    return {"commit_sha": sha, "origin_repo": repo, "ref": str(ref)}


def _git_blob(origin_repo: str, sha: str, path: str) -> bytes:
    """Read the RAW blob of ``<sha>:<path>`` via ``git cat-file`` — strictly
    by SHA. cat-file returns the object bytes verbatim (no checkout/EOL
    smudging), so bundle content is independent of core.autocrlf and
    .gitattributes on either side."""
    proc = subprocess.run(
        ["git", "-C", origin_repo, "cat-file", "blob", f"{sha}:{path}"],
        capture_output=True,
    )
    if proc.returncode != 0:
        raise MixedBundleError(
            f"pinned commit {sha} in '{origin_repo}' does not provide "
            f"'{path}': {proc.stderr.decode(errors='replace').strip()}"
        )
    return proc.stdout


def _fetch_pinned_tree(origin_repo: str, sha: str) -> Dict[str, bytes]:
    """Materialize the pinned commit's component set — strictly by SHA,
    never re-floating a moving ref, never touching the working tree, and
    byte-exact (no EOL conversion)."""
    files: Dict[str, bytes] = {}
    manifest_blob = _git_blob(origin_repo, sha, "manifest.json")
    files["manifest.json"] = manifest_blob
    try:
        manifest = json.loads(manifest_blob.decode("utf-8"))
    except (UnicodeDecodeError, ValueError) as exc:
        raise MixedBundleError(
            f"manifest.json of pinned commit {sha} in '{origin_repo}' is not "
            f"valid JSON: {exc}"
        )
    for comp in _components_of(manifest):
        rel = _posix(comp.get("path", ""))
        files[rel] = _git_blob(origin_repo, sha, rel)
    return files


def _components_of(manifest: Dict[str, Any]) -> List[Dict[str, Any]]:
    comps = manifest.get("components") or []
    return [c for c in comps if isinstance(c, dict) and _posix(c.get("path", ""))]


def _read_bundle_tree(bundle_root: Path) -> Tuple[Dict[str, Any], Dict[str, bytes]]:
    """Read an integral bundle directory: manifest.json + EVERY manifest
    component. Any missing component is a partial cache: refused loudly."""
    manifest_path = bundle_root / "manifest.json"
    if not manifest_path.is_file():
        raise MixedBundleError(
            f"cache entry '{bundle_root}' carries no manifest.json; refusing "
            "to guess an integral component set"
        )
    try:
        manifest = _read_json(manifest_path)
    except (OSError, ValueError) as exc:
        raise MixedBundleError(
            f"cache manifest '{manifest_path}' is unreadable: {exc}"
        )
    files: Dict[str, bytes] = {"manifest.json": manifest_path.read_bytes()}
    for comp in _components_of(manifest):
        rel = _posix(comp.get("path", ""))
        comp_path = bundle_root / rel
        if not comp_path.is_file():
            raise PartialCacheError(
                f"partial cache refused: bundle '{bundle_root}' is missing "
                f"manifest component '{rel}'; only an INTEGRAL single-revision "
                "bundle is acceptable (mixing revisions is prohibited)"
            )
        files[rel] = comp_path.read_bytes()
    return manifest, files


def _collect_base_candidates(cache_root: Optional[Path], exclude_sha: str) -> Dict[str, bytes]:
    """Pool of merge-base candidates: contents of OTHER cached bundles
    (keyed by path). plan_updates accepts a candidate only when its sha256
    matches the lock-recorded hash of the installed component."""
    base: Dict[str, bytes] = {}
    if cache_root is None:
        return base
    cache_root = Path(cache_root)
    if not cache_root.is_dir():
        return base
    for child in sorted(cache_root.iterdir()):
        if not child.is_dir() or child.name == exclude_sha:
            continue
        manifest_path = child / "manifest.json"
        if not manifest_path.is_file():
            continue
        try:
            manifest = _read_json(manifest_path)
        except (OSError, ValueError):
            continue
        for comp in _components_of(manifest):
            rel = _posix(comp.get("path", ""))
            if not rel or rel in base:
                continue
            comp_path = child / rel
            if comp_path.is_file():
                try:
                    base[rel] = comp_path.read_bytes()
                except OSError:
                    continue
    return base


def load_bundle(pin: Dict[str, str], cache_dir: Optional[Path]) -> Dict[str, Any]:
    """Materialize the ENTIRE component set from the pinned SHA.

    Resolution order: integral cache entry ``<cache_dir>/<commit_sha>/``
    first (read-only), then ``git archive <commit_sha>`` from the origin
    repository. Raises MixedBundleError when neither source yields one
    integral revision, PartialCacheError for a partial cache entry, and
    BaseUnavailableError semantics are carried by the empty ``base`` pool
    (plan_updates turns it into ``needs_base`` entries).

    Returns: {manifest, files: {path: bytes}, base: {path: bytes},
    commit_sha, origin_repo, ref, origin: 'cache'|'upstream'}.
    """
    pin = dict(pin or {})
    sha = str(pin.get("commit_sha", ""))
    origin_repo = str(pin.get("origin_repo", ""))
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise MixedBundleError(f"invalid pin (commit_sha={sha!r})")

    cache_root = Path(cache_dir) if cache_dir is not None else None
    pinned_dir = cache_root / sha if cache_root is not None else None

    manifest: Optional[Dict[str, Any]] = None
    files: Optional[Dict[str, bytes]] = None
    origin_kind: str

    if pinned_dir is not None and pinned_dir.is_dir():
        manifest, files = _read_bundle_tree(pinned_dir)
        origin_kind = "cache"

    if files is None:
        check = _run_git(
            ["-C", origin_repo, "rev-parse", "--verify", f"{sha}^{{commit}}"]
        )
        if check.returncode != 0:
            raise MixedBundleError(
                f"cannot materialize the integral bundle for pinned commit "
                f"{sha}: origin '{origin_repo}' does not provide it "
                f"({(check.stderr or '').strip()}) and no integral cache "
                f"entry exists under '{pinned_dir}'"
            )
        files = _fetch_pinned_tree(origin_repo, sha)
        manifest = json.loads(files["manifest.json"].decode("utf-8"))
        origin_kind = "upstream"

    return {
        "manifest": manifest,
        "files": files,
        "base": _collect_base_candidates(cache_root, exclude_sha=sha),
        "commit_sha": sha,
        "origin_repo": origin_repo,
        "ref": str(pin.get("ref") or sha),
        "origin": origin_kind,
    }


def _filter_bundle(bundle: Dict[str, Any], components: List[str]) -> Dict[str, Any]:
    """Restrict a bundle to an explicit component subset (manifest != lock:
    the caller selects the installed subset; `requires` companions must be
    selected explicitly)."""
    wanted = {_posix(c) for c in components}
    manifest = dict(bundle.get("manifest") or {})
    manifest["components"] = [
        c for c in _components_of(manifest)
        if _posix(c.get("path", "")) in wanted
    ]
    filtered = dict(bundle)
    filtered["manifest"] = manifest
    filtered["files"] = {
        p: b for p, b in (bundle.get("files") or {}).items()
        if p == "manifest.json" or p in wanted
    }
    filtered["base"] = {
        p: b for p, b in (bundle.get("base") or {}).items() if p in wanted
    }
    return filtered


# ---------------------------------------------------------------------------
# Lock
# ---------------------------------------------------------------------------


def read_lock(repo_root: Path) -> Optional[Dict[str, Any]]:
    """Read ``.agents/framework-lock.json``; None when absent. A corrupted
    lock fails closed."""
    path = Path(repo_root) / FRAMEWORK_LOCK_RELPATH
    if not path.is_file():
        return None
    try:
        lock = _read_json(path)
    except (OSError, ValueError) as exc:
        raise UpdateEngineError(
            f"framework lock '{path}' is corrupted (fail-closed): {exc}"
        )
    if not isinstance(lock, dict):
        raise UpdateEngineError(
            f"framework lock '{path}' must be a JSON object (fail-closed)"
        )
    return lock


def write_lock_atomic(repo_root: Path, lock: Dict[str, Any]) -> None:
    """Atomically replace ``.agents/framework-lock.json``."""
    path = Path(repo_root) / FRAMEWORK_LOCK_RELPATH
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = json.dumps(lock, indent=2, ensure_ascii=False) + "\n"
    tmp = path.parent / f".framework-lock.tmp-{uuid.uuid4().hex[:8]}"
    try:
        with open(tmp, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(payload)
        os.replace(tmp, path)
    except OSError:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise


# ---------------------------------------------------------------------------
# Read-only modes: check and plan
# ---------------------------------------------------------------------------


def check_updates(
    repo_root: Path,
    source: str,
    ref: str,
    cache_dir: Optional[Path] = None,
) -> Dict[str, Any]:
    """Read-only availability and drift check (zero-write).

    Diagnoses every lock-recorded component against the working tree and
    the pinned bundle. A locally modified installed component is classified
    as 'diverged' with unknown origin — the engine never assumes such an
    edit is upstream content.
    """
    repo_root = Path(repo_root)
    pin = resolve_source(source, ref)
    bundle = load_bundle(pin, cache_dir)
    lock = read_lock(repo_root)

    lock_components = (lock or {}).get("components") or []
    lock_by_path = {
        _posix(c.get("path", "")): c
        for c in lock_components
        if isinstance(c, dict) and _posix(c.get("path", ""))
    }

    bundle_files = bundle.get("files") or {}
    diagnostics: List[Dict[str, Any]] = []
    drifted: List[str] = []
    updates: List[Dict[str, Any]] = []

    for path in sorted(lock_by_path):
        entry = lock_by_path[path]
        local = repo_root / path
        recorded = str(entry.get("sha256", "") or "")
        if not local.is_file():
            diagnostics.append({
                "path": path,
                "status": "missing",
                "detail": "lock-recorded component is absent from the working tree",
            })
            drifted.append(path)
            continue
        current = _sha256_file(local)
        if recorded and current != recorded:
            diagnostics.append({
                "path": path,
                "status": "diverged",
                "origin": "unknown",
                "detail": (
                    "installed content differs from the lock record: local "
                    "edit of unknown origin (drifted from the pinned "
                    "installation); update engine will treat it via merge "
                    "base or surface a conflict"
                ),
            })
            drifted.append(path)

    for comp in _components_of(bundle.get("manifest") or {}):
        path = _posix(comp.get("path", ""))
        if path not in lock_by_path:
            continue
        new_sha = _sha256_bytes(bundle_files.get(path, b""))
        if new_sha != str(lock_by_path[path].get("sha256", "") or ""):
            updates.append({
                "path": path,
                "kind": comp.get("kind", ""),
                "action": "update_available",
            })

    return {
        "source": {
            "repository": pin["origin_repo"],
            "ref": pin.get("ref") or ref,
            "commit_sha": pin["commit_sha"],
            "origin": bundle.get("origin"),
        },
        "lock_present": lock is not None,
        "updates_available": updates,
        "drifted": drifted,
        "diagnostics": diagnostics,
        "status": "updates_available" if updates else "up_to_date",
    }


def _extract_local_blocks(data: bytes) -> List[Tuple[bytes, bytes]]:
    return [(m.group(0), m.group(1)) for m in LOCAL_REPO_DATA_RE.finditer(data)]


def _merge_marked(
    base: bytes, local: bytes, new: bytes
) -> Tuple[Optional[bytes], List[bytes]]:
    """Three-way merge honoring local_repo_data blocks.

    Returns (merged_bytes, preserved_tags) when the local tree differs from
    the base ONLY inside marker-wrapped blocks; returns (None, []) when an
    unmarked local modification exists (conflict — never silently
    overwritten).
    """
    local_blocks = _extract_local_blocks(local)
    stripped_local = LOCAL_REPO_DATA_RE.sub(b"", local).strip(b" \t\r\n")
    stripped_base = LOCAL_REPO_DATA_RE.sub(b"", base).strip(b" \t\r\n")
    if stripped_local != stripped_base:
        return None, []
    merged = new
    preserved: List[bytes] = []
    for block, tag in local_blocks:
        preserved.append(tag)
        if block in merged:
            continue
        if merged and not merged.endswith(b"\n"):
            merged += b"\n"
        merged += block + b"\n"
    return merged, preserved


def _is_protected(path: str, patterns: Tuple[str, ...]) -> bool:
    posix = _posix(path)
    return any(fnmatch.fnmatch(posix, _posix(p)) for p in patterns)


def _legacy_duplicates(repo_root: Path, manifest: Dict[str, Any]) -> None:
    """Legacy-path transition: canonical location is ``scripts/``. When a
    manifest component has a legacy copy under ``.agents/scripts/``:
    identical -> migration note; divergent -> HALT with a report naming both
    paths and hashes. Never silently use the first found."""
    notes: List[Dict[str, Any]] = []
    for comp in _components_of(manifest):
        rel = _posix(comp.get("path", ""))
        if not rel.startswith("scripts/"):
            continue
        canonical = repo_root / rel
        legacy_rel = ".agents/" + rel
        legacy = repo_root / legacy_rel
        if not legacy.is_file():
            continue
        legacy_sha = _sha256_file(legacy)
        if not canonical.is_file():
            notes.append({
                "path": legacy_rel,
                "sha256": legacy_sha,
                "note": "legacy copy without a canonical counterpart: schedule explicit migration to " + rel,
            })
            continue
        canonical_sha = _sha256_file(canonical)
        if canonical_sha != legacy_sha:
            raise LegacyDuplicateError(
                f"divergent duplicate copies of managed component '{rel}': "
                f"canonical '{rel}' sha256={canonical_sha} vs legacy "
                f"'{legacy_rel}' sha256={legacy_sha}; HALT: never silently "
                "use the first found — resolve via an explicit migration "
                "before updating"
            )
        notes.append({
            "path": legacy_rel,
            "sha256": legacy_sha,
            "note": "legacy copy identical to the canonical one: migrate (delete legacy) at next update",
        })
    return notes


def plan_updates(
    repo_root: Path,
    bundle: Dict[str, Any],
    lock: Optional[Dict[str, Any]],
) -> Dict[str, Any]:
    """Compute the three-way merge plan. STRICTLY READ-ONLY (zero-write).

    Returns PlanResult: {actions, conflicts, protected, needs_base} plus
    informational keys (up_to_date, legacy, meta). Actions carry the exact
    bytes to write; base-less local modifications become needs_base entries,
    never "safe merge" actions.
    """
    repo_root = Path(repo_root)
    bundle_files = bundle.get("files") or {}
    manifest = bundle.get("manifest") or {}
    lock = lock or {}
    lock_components = lock.get("components") or []
    lock_by_path = {
        _posix(c.get("path", "")): c
        for c in lock_components
        if isinstance(c, dict) and _posix(c.get("path", ""))
    }

    actions: List[Dict[str, Any]] = []
    conflicts: List[Dict[str, Any]] = []
    protected: List[Dict[str, Any]] = []
    needs_base: List[Dict[str, Any]] = []
    up_to_date: List[Dict[str, Any]] = []

    legacy_notes = _legacy_duplicates(repo_root, manifest)

    for comp in _components_of(manifest):
        rel = _posix(comp.get("path", ""))
        kind = comp.get("kind", "")
        new_bytes = bundle_files.get(rel)
        if new_bytes is None:
            conflicts.append({
                "path": rel,
                "reason": "manifest component is missing from the integral bundle",
            })
            continue

        if _is_protected(rel, DEFAULT_PROTECT_PATTERNS):
            protected.append({
                "path": rel,
                "kind": kind,
                "reason": (
                    f"protected component '{rel}': {RED_ZONE_REFUSAL_TEXT}"
                ),
            })
            continue

        local_path = repo_root / rel
        local_bytes = local_path.read_bytes() if local_path.is_file() else None
        lock_entry = lock_by_path.get(rel)
        recorded_sha = str((lock_entry or {}).get("sha256", "") or "")
        current_sha = _sha256_bytes(local_bytes) if local_bytes is not None else None

        if lock_entry is None:
            if local_bytes is None:
                actions.append({
                    "path": rel, "kind": kind, "op": "create",
                    "content": new_bytes,
                })
            else:
                conflicts.append({
                    "path": rel,
                    "reason": (
                        "target path holds a local file that is not recorded "
                        "in the framework lock (unknown origin); explicit "
                        "user decision required before first install"
                    ),
                })
            continue

        if local_bytes is None:
            actions.append({
                "path": rel, "kind": kind, "op": "install_missing",
                "content": new_bytes,
            })
            continue

        if recorded_sha and current_sha == recorded_sha:
            # Local content matches the installation record: fast-forward,
            # no merge base bytes needed.
            if new_bytes == local_bytes:
                up_to_date.append({"path": rel, "kind": kind, "sha256": current_sha})
            else:
                actions.append({
                    "path": rel, "kind": kind, "op": "update",
                    "content": new_bytes,
                })
            continue

        # Local content diverged from the lock record: an honest three-way
        # merge needs the base (content whose sha256 matches the record).
        base_bytes = (bundle.get("base") or {}).get(rel)
        base_ok = (
            base_bytes is not None
            and bool(recorded_sha)
            and _sha256_bytes(base_bytes) == recorded_sha
        )
        if not base_ok:
            needs_base.append({
                "path": rel,
                "reason": (
                    "merge base unavailable: no cached bundle content "
                    "matches the lock-recorded sha256; refusing to claim a "
                    "safe merge (needs_base)"
                ),
                "lock_sha256": recorded_sha,
            })
            continue

        merged, preserved = _merge_marked(base_bytes, local_bytes, new_bytes)
        if merged is None:
            conflicts.append({
                "path": rel,
                "reason": (
                    "unmarked local modification outside local_repo_data "
                    "blocks (relative to the recorded base); manual "
                    "resolution required, file will NOT be overwritten"
                ),
                "local_sha256": current_sha,
                "base_sha256": recorded_sha,
                "new_sha256": _sha256_bytes(new_bytes),
            })
        else:
            actions.append({
                "path": rel, "kind": kind, "op": "merge",
                "content": merged,
                "preserved_blocks": [tag.decode("utf-8", "replace") for tag in preserved],
            })

    plan: Dict[str, Any] = {
        "actions": actions,
        "conflicts": conflicts,
        "protected": protected,
        "needs_base": needs_base,
        "up_to_date": up_to_date,
        "legacy": legacy_notes,
        "meta": {
            "commit_sha": bundle.get("commit_sha"),
            "ref": bundle.get("ref"),
            "origin": bundle.get("origin"),
            "repository": bundle.get("origin_repo"),
            "framework_version": (manifest or {}).get("framework_version"),
            "channel": (manifest or {}).get("channel"),
            "components_total": len(_components_of(manifest)),
        },
    }
    return plan


# ---------------------------------------------------------------------------
# Journal
# ---------------------------------------------------------------------------


def _save_journal(repo_root: Path, journal: Dict[str, Any]) -> None:
    path = Path(repo_root) / UPDATE_JOURNAL_RELPATH
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = json.dumps(journal, indent=2, ensure_ascii=False) + "\n"
    tmp = path.parent / f".update-journal.tmp-{uuid.uuid4().hex[:8]}"
    try:
        with open(tmp, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(payload)
        os.replace(tmp, path)
    except OSError:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise


def _b64(data: Optional[bytes]) -> Optional[str]:
    if data is None:
        return None
    return base64.b64encode(data).decode("ascii")


def _unb64(value: Optional[str]) -> Optional[bytes]:
    if value is None:
        return None
    return base64.b64decode(value.encode("ascii"))


# ---------------------------------------------------------------------------
# Update report (via the canonical report engine)
# ---------------------------------------------------------------------------


def _build_update_report(
    repo_root: Path,
    installation_id: str,
    actor_id: str,
    plan: Dict[str, Any],
    applied_actions: List[Dict[str, Any]],
    lock_before: Optional[Dict[str, Any]],
) -> Dict[str, Any]:
    repo_root = Path(repo_root)
    registry: Dict[str, Any] = {}
    registry_path = repo_root / ".agents" / "meta-project.json"
    if registry_path.is_file():
        try:
            loaded = _read_json(registry_path)
            if isinstance(loaded, dict):
                registry = loaded
        except (OSError, ValueError):
            registry = {}
    project_id = str(registry.get("project_id") or "unknown-project")
    harness_id = str(
        ((registry.get("installations") or {}).get(installation_id) or {}).get(
            "harness_id"
        )
        or "unknown-harness"
    )

    meta = plan.get("meta") or {}
    before_source = (lock_before or {}).get("source") or {}
    before = str(before_source.get("commit_sha") or "unknown")
    after = str(
        meta.get("framework_version") or meta.get("commit_sha") or "unknown"
    )

    fingerprints: List[Dict[str, str]] = []
    if collect_input_fingerprints is not None:
        raw = collect_input_fingerprints(repo_root)
        for rel, digest in sorted(raw.items()):
            if digest:
                fingerprints.append({"path": rel, "sha256": digest})
    lock_digest = None
    lock_path = repo_root / FRAMEWORK_LOCK_RELPATH
    if lock_path.is_file():
        try:
            lock_digest = _sha256_file(lock_path)
        except OSError:
            lock_digest = None
    if lock_digest:
        fingerprints.append({"path": FRAMEWORK_LOCK_RELPATH, "sha256": lock_digest})

    preserved_adaptations = [
        {
            "path": action["path"],
            "detail": "local_repo_data block(s) preserved: "
            + ", ".join(action.get("preserved_blocks") or []),
        }
        for action in applied_actions
        if action.get("op") == "merge"
    ]

    affected = [
        {
            "path": action["path"],
            "kind": action.get("kind", ""),
            "action": action.get("op", "updated"),
        }
        for action in applied_actions
    ]

    checks = [
        {
            "name": "update_apply",
            "command": "python scripts/update_meta_project.py apply",
            "status": "pass",
            "output_summary": (
                f"{len(applied_actions)} components affected, "
                f"{len(plan.get('conflicts') or [])} conflicts, "
                f"{len(plan.get('needs_base') or [])} needs-base, "
                f"{len(plan.get('protected') or [])} protected-refused, "
                f"journal written to {UPDATE_JOURNAL_RELPATH}"
            ),
        }
    ]

    return {
        "schema_version": LOCK_SCHEMA_VERSION,
        "report_type": REPORT_TYPE,
        "project_id": project_id,
        "installation_id": installation_id,
        "harness_id": harness_id,
        "actor_id": actor_id,
        "run_id": (generate_run_id(REPORT_TYPE) if generate_run_id else REPORT_TYPE),
        "created_at": _utc_now_iso(),
        "status": "completed",
        "git_state": (get_git_state(repo_root) if get_git_state else {}),
        "input_fingerprints": fingerprints,
        "versions": {"before": before, "after": after},
        "source": {
            "ref": str(meta.get("ref") or "unknown"),
            "commit_sha": str(meta.get("commit_sha") or "unknown"),
            "origin": str(meta.get("origin") or "unknown"),
        },
        "affected_components": affected,
        "executed_migrations": [],
        "preserved_adaptations": preserved_adaptations,
        "conflicts": [],
        "checks": checks,
        "remaining_manual_actions": [],
    }


# ---------------------------------------------------------------------------
# Apply (atomic, journaled) and restore
# ---------------------------------------------------------------------------


def apply_updates(
    repo_root: Path,
    plan: Dict[str, Any],
    actor_id: str,
    installation_id: str,
) -> Dict[str, Any]:
    """Apply the plan atomically, per file, with a crash-recovery journal.

    Refuses loudly (no writes at all) when the plan carries protected
    components, unresolved conflicts, or needs_base entries. On success the
    framework lock is rewritten atomically and an 'update' report is stored
    via scripts/manage_reports.save_report.
    """
    repo_root = Path(repo_root)

    protected = plan.get("protected") or []
    if protected:
        paths = ", ".join(str(p.get("path")) for p in protected)
        raise ProtectedComponentError(
            f"refusing to apply updates touching protected components: "
            f"{paths}; {RED_ZONE_REFUSAL_TEXT}"
        )
    conflicts = plan.get("conflicts") or []
    if conflicts:
        paths = ", ".join(str(c.get("path")) for c in conflicts)
        raise ApplyError(
            f"plan carries unresolved conflict(s) for: {paths}; apply "
            "refuses to overwrite local modifications — resolve them "
            "explicitly (local_repo_data markers or manual merge) first"
        )
    needs_base = plan.get("needs_base") or []
    if needs_base:
        paths = ", ".join(str(n.get("path")) for n in needs_base)
        raise ApplyError(
            f"plan carries needs_base component(s): {paths}; no merge base "
            "matching the lock record is available, so no safe merge can "
            "be claimed — provide an integral cache with the base content "
            "or decide manually"
        )

    actions = plan.get("actions") or []
    if not actions:
        return {
            "status": "nothing_to_apply",
            "applied": [],
            "installation_id": installation_id,
            "actor_id": actor_id,
        }

    run_id = generate_run_id("apply") if generate_run_id else f"apply-{uuid.uuid4().hex[:12]}"
    lock_before = read_lock(repo_root)
    journal: Dict[str, Any] = {
        "schema_version": LOCK_SCHEMA_VERSION,
        "run_id": run_id,
        "actor_id": actor_id,
        "installation_id": installation_id,
        "status": "started",
        "started_at": _utc_now_iso(),
        "plan_meta": plan.get("meta") or {},
        "entries": [],
    }
    journal_path = repo_root / UPDATE_JOURNAL_RELPATH

    def _persist_journal() -> None:
        _save_journal(repo_root, journal)

    # The journal must exist BEFORE the first file is replaced.
    _persist_journal()

    applied_actions: List[Dict[str, Any]] = []
    try:
        for action in actions:
            rel = _posix(action["path"])
            dest = repo_root / rel
            content = action["content"]
            if not isinstance(content, bytes):
                raise ApplyError(
                    f"plan action for '{rel}' carries no content bytes"
                )
            before_bytes = dest.read_bytes() if dest.is_file() else None
            entry = {
                "path": rel,
                "op": action.get("op", "update"),
                "kind": action.get("kind", ""),
                "before_sha256": _sha256_bytes(before_bytes) if before_bytes is not None else None,
                "after_sha256": _sha256_bytes(content),
                "before_b64": _b64(before_bytes),
                "after_b64": _b64(content),
                "status": "pending",
            }
            journal["entries"].append(entry)
            _persist_journal()
            try:
                dest.parent.mkdir(parents=True, exist_ok=True)
                _atomic_write_bytes(dest, content)
            except OSError as exc:
                entry["status"] = "failed"
                entry["error"] = str(exc)
                journal["status"] = "aborted"
                journal["aborted_at"] = _utc_now_iso()
                _persist_journal()
                raise ApplyError(
                    f"apply failed at '{rel}': {exc}; per-file writes before "
                    f"the failure are journaled in '{UPDATE_JOURNAL_RELPATH}' "
                    "— run restore_updates to return the tree to the "
                    "pre-apply state"
                )
            entry["status"] = "applied"
            applied_actions.append(action)
            _persist_journal()
    except ApplyError:
        raise

    # Success: rewrite the lock atomically to the installed subset.
    lock = dict(lock_before or {})
    lock["schema_version"] = LOCK_SCHEMA_VERSION
    components = [
        dict(c) for c in (lock_before or {}).get("components") or []
        if isinstance(c, dict) and _posix(c.get("path", ""))
    ]
    by_path = {_posix(c.get("path", "")): c for c in components}
    meta = plan.get("meta") or {}
    for action in applied_actions:
        rel = _posix(action["path"])
        content = action["content"]
        entry = {
            "path": rel,
            "kind": action.get("kind", ""),
            "sha256": _sha256_bytes(content),
        }
        if rel in by_path:
            components[components.index(by_path[rel])] = entry
        else:
            components.append(entry)
        by_path[rel] = entry
    lock["components"] = components
    old_source = (lock_before or {}).get("source") or {}
    lock["source"] = {
        "repository": str(meta.get("repository") or old_source.get("repository") or "unknown"),
        "ref": str(meta.get("ref") or old_source.get("ref") or "unknown"),
        "commit_sha": str(meta.get("commit_sha") or "unknown"),
        "origin": str(meta.get("origin") or old_source.get("origin") or "unknown"),
        "channel": str(
            meta.get("channel") or old_source.get("channel") or "preview"
        ),
    }
    lock["installed_at"] = _utc_now_iso()
    write_lock_atomic(repo_root, lock)

    journal["status"] = "completed"
    journal["completed_at"] = _utc_now_iso()
    _persist_journal()

    # Update report through the canonical report engine (report_type='update').
    report_path: Optional[str] = None
    if save_report is not None:
        report = _build_update_report(
            repo_root=repo_root,
            installation_id=installation_id,
            actor_id=actor_id,
            plan=plan,
            applied_actions=applied_actions,
            lock_before=lock_before,
        )
        saved = save_report(repo_root, installation_id, REPORT_TYPE, report)
        report_path = str(saved)

    return {
        "status": "completed",
        "run_id": run_id,
        "applied": [a["path"] for a in applied_actions],
        "journal": UPDATE_JOURNAL_RELPATH,
        "lock": FRAMEWORK_LOCK_RELPATH,
        "report_path": report_path,
        "installation_id": installation_id,
        "actor_id": actor_id,
    }


def restore_updates(repo_root: Path) -> Dict[str, Any]:
    """Restore ONLY the engine's own changes recorded in the journal.

    Each journaled path is reverted only when its current sha256 still
    matches what the engine wrote; third-party edits made during the apply
    window are detected and preserved (never clobbered).
    """
    repo_root = Path(repo_root)
    journal_path = repo_root / UPDATE_JOURNAL_RELPATH
    if not journal_path.is_file():
        return {
            "status": "nothing_to_restore",
            "restored": [],
            "kept": [],
            "skipped": [],
        }
    journal = _read_json(journal_path)
    entries = journal.get("entries") or []
    restored: List[Dict[str, Any]] = []
    kept: List[Dict[str, Any]] = []
    skipped: List[Dict[str, Any]] = []

    for entry in reversed(entries):
        if not isinstance(entry, dict):
            continue
        rel = _posix(entry.get("path", ""))
        if not rel:
            continue
        target = repo_root / rel
        after_sha = entry.get("after_sha256")
        before_sha = entry.get("before_sha256")
        before_bytes = _unb64(entry.get("before_b64"))
        current_bytes = target.read_bytes() if target.is_file() else None
        current_sha = _sha256_bytes(current_bytes) if current_bytes is not None else None

        if entry.get("status") == "failed":
            skipped.append({
                "path": rel,
                "reason": "apply never completed this step; nothing to revert",
            })
            continue
        if current_bytes is not None and after_sha and current_sha == after_sha:
            # Exactly the engine's own write: safe to revert.
            if before_bytes is None:
                try:
                    target.unlink()
                except OSError as exc:
                    kept.append({"path": rel, "reason": f"removal failed: {exc}"})
                    continue
                restored.append({"path": rel, "action": "removed (created by apply)"})
            else:
                _atomic_write_bytes(target, before_bytes)
                restored.append({"path": rel, "action": "reverted to pre-apply content"})
            continue
        if current_bytes is not None and before_sha and current_sha == before_sha:
            skipped.append({"path": rel, "reason": "already at the pre-apply state"})
            continue
        if current_bytes is None and before_bytes is None:
            skipped.append({"path": rel, "reason": "target absent; nothing created"})
            continue
        kept.append({
            "path": rel,
            "reason": (
                "content changed after the engine wrote it (third-party "
                "edit during the apply window); preserved, never clobbered"
            ),
        })

    journal["status"] = "restored"
    journal["restored_at"] = _utc_now_iso()
    _save_journal(repo_root, journal)

    return {
        "status": "restored",
        "journal": UPDATE_JOURNAL_RELPATH,
        "restored": restored,
        "kept": kept,
        "skipped": skipped,
    }


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def _add_source_args(parser: argparse.ArgumentParser, repo_root_required: bool) -> None:
    parser.add_argument(
        "--repo-root",
        required=repo_root_required,
        default=None if repo_root_required else ".",
        help="Target repository root path",
    )
    parser.add_argument(
        "--source",
        help="Upstream source: local git repository path (remote URLs require "
        "a prior local fetch; remote origin support needs `git fetch` first)",
    )
    parser.add_argument(
        "--ref",
        default="main",
        help="Ref to pin: branch, tag, or (full) commit SHA (default: main)",
    )
    parser.add_argument(
        "--cache-dir",
        default=None,
        help="Integral bundle cache directory (default: META_PROJECT_CACHE "
        "env, else the OS cache path)",
    )
    parser.add_argument(
        "--components",
        default=None,
        help="Comma-separated subset of manifest component paths to include",
    )
    parser.add_argument(
        "--protect",
        default=None,
        help="Comma-separated protect patterns overriding the defaults",
    )


def build_cli_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="update_meta_project",
        description="Meta-Project Managed Update Engine (SHA-pinned, atomic, journaled)",
    )
    subparsers = parser.add_subparsers(dest="subcommand", required=True)

    p_check = subparsers.add_parser(
        "check", help="Read-only availability & drift check (zero-write)"
    )
    _add_source_args(p_check, repo_root_required=False)

    p_plan = subparsers.add_parser(
        "plan", help="Read-only three-way merge plan (zero-write)"
    )
    _add_source_args(p_plan, repo_root_required=False)

    p_apply = subparsers.add_parser(
        "apply", help="Apply the update atomically with journal, lock and update report"
    )
    _add_source_args(p_apply, repo_root_required=True)
    p_apply.add_argument(
        "--installation", required=True, help="Installation ID (e.g. ag-main)"
    )
    p_apply.add_argument(
        "--actor", required=True, help="Qualified actor_id executing the update"
    )

    p_restore = subparsers.add_parser(
        "restore",
        help="Restore ONLY the engine's own changes from the last operation journal",
    )
    p_restore.add_argument(
        "--repo-root", default=".", help="Target repository root path"
    )

    return parser


def main(argv: Optional[List[str]] = None) -> int:
    """CLI entry point: check | plan | apply | restore."""
    parser = build_cli_parser()
    args = parser.parse_args(argv)
    repo_root = Path(getattr(args, "repo_root", ".") or ".").resolve()

    protect_patterns = DEFAULT_PROTECT_PATTERNS
    components = _split_list(getattr(args, "components", None))
    cache_dir: Optional[Path]
    explicit_cache = getattr(args, "cache_dir", None)
    if explicit_cache:
        cache_dir = Path(explicit_cache)
    else:
        cache_dir = default_cache_dir()

    if args.subcommand == "check":
        try:
            result = check_updates(repo_root, args.source, args.ref, cache_dir)
        except UpdateEngineError as exc:
            sys.stderr.write(f"[CHECK-FAILED] {exc}\n")
            return 1
        print(json.dumps(result, indent=2, default=str))
        return 0 if not (result["updates_available"] or result["drifted"]) else 1

    if args.subcommand == "plan":
        try:
            pin = resolve_source(args.source, args.ref)
            bundle = load_bundle(pin, cache_dir)
            if components:
                bundle = _filter_bundle(bundle, components)
            lock = read_lock(repo_root)
            plan = plan_updates(repo_root, bundle, lock)
        except UpdateEngineError as exc:
            sys.stderr.write(f"[PLAN-HALT] {exc}\n")
            return 1
        print(json.dumps(plan, indent=2, default=str))
        return 0

    if args.subcommand == "apply":
        try:
            pin = resolve_source(args.source, args.ref)
            bundle = load_bundle(pin, cache_dir)
            if components:
                bundle = _filter_bundle(bundle, components)
            lock = read_lock(repo_root)
            plan = plan_updates(repo_root, bundle, lock)
            result = apply_updates(repo_root, plan, args.actor, args.installation)
        except UpdateEngineError as exc:
            sys.stderr.write(f"[APPLY-REFUSED] {exc}\n")
            return 1
        except (OSError, ValueError) as exc:
            sys.stderr.write(f"[APPLY-FAILED] {exc}\n")
            return 1
        print(json.dumps(result, indent=2, default=str))
        return 0

    if args.subcommand == "restore":
        try:
            result = restore_updates(repo_root)
        except (UpdateEngineError, OSError, ValueError) as exc:
            sys.stderr.write(f"[RESTORE-FAILED] {exc}\n")
            return 1
        print(json.dumps(result, indent=2, default=str))
        return 0

    parser.error(f"unknown subcommand: {args.subcommand}")
    return 2


if __name__ == "__main__":
    sys.exit(main())
