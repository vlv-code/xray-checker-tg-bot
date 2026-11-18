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
  actually-installed SUBSET — component schema v2 carries base_sha256
  (pinned upstream bytes) and installed_sha256 (on-disk bytes) per component
  (``target`` = resolved on-disk destination); v1 entries (single
  ``sha256``) are read as base = installed.
* **The plan pins what apply may do:** a plan is computed against ONE source
  revision (``pin``) and one target state (per-action
  ``expected_before_sha256``); apply re-verifies both and refuses with ZERO
  writes on any deviation. The CLI apply executes a user-approved plan file
  (``--plan-file``) or prints a ``[RE-PLANNED]`` notice when it computes its
  own plan.
* **Integral, VERIFIED cache fallback:** ``<cache_dir>/<commit_sha>/`` holds
  a full single-revision bundle; a partial cache is refused loudly
  (``PartialCacheError``), never mixed. SEC-2 (Wave F FX, T-X2): every cache
  load is verified — a sealed entry (``cache-manifest.json``: per-file
  sha256 + the ref-pin it was built from) is hash-checked on load and
  cross-verified against the origin blobs whenever the origin provides the
  pinned commit; an unsealed entry must cross-verify against the origin or
  is refused (``CacheVerificationError``). apply seals a cross-verified
  entry so later offline loads can verify it.
* **Containment (SEC-1, Wave F FX, T-X1):** managed paths must be relative
  without ``..`` segments (``_posix`` refuses absolutes, drive letters, UNC
  shares and traversal), every destination read/write is resolved and
  contained inside its root (``assert_within``), and existing symlink
  destinations are refused — a hostile manifest, lock, plan file, journal or
  cache cannot move a single byte outside the repository (or cache) root.
* **Serialization (SEC-4, Wave F FX, T-X3):** apply AND restore hold
  ``.agents/update-engine.lock`` exclusively (non-blocking); a concurrent
  second engine operation is refused with an ``ENGINE_BUSY`` envelope and
  writes nothing.
* **Three-way merge with base honesty:** for locally modified components the
  merge base is cached content whose sha256 matches the lock record; without
  a matching base the plan yields a ``needs_base`` entry, never a "safe
  merge" claim. ``local_repo_data``-marked blocks are re-injected into the
  merged content; unmarked local edits surface as explicit conflicts.
* **Red Zone refusal:** automated changes to protected components are
  refused: no sanctioned mechanism exists (explicitly open item). The
  perimeter is enforced in layers (Wave D E2): apply requires an ADMITTED
  actor (registered + active binding in ``.agents/meta-project.json``;
  identity-less bootstrap projects are not blocked), and every component
  target path is additionally consulted against the project's
  ``boundaries.md`` Red zone and the ``--protect`` patterns (union with the
  engine defaults).
* **Offline exact-SHA cache:** when the origin cannot resolve an EXACT
  40-hex ref but an integral cache bundle exists for that SHA, the update
  proceeds with ``origin: cache``; refs that are not exact SHAs still
  require a resolvable origin (Wave D E2, D2).
* **Manifest readability fallback:** the engine reads ``manifest.json`` at
  the source root and falls back to ``templates/release_manifest.json``
  (the canonical framework repository layout).
* **Atomicity & recovery:** per-file atomic replace via temp + ``os.replace``,
  an operation journal (``.agents/update-journal.json``) with before/after
  sha256 (and content), and a restore mode that reverts ONLY the engine's own
  changes, keeping third-party edits made during the apply window. The lock
  write is itself journaled: restore rolls back files AND
  ``.agents/framework-lock.json`` to the coherent pre-apply state.
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
import contextlib
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

try:  # identity admission (Wave D E2, T6.1): defensive import mirroring
    # scripts/inspect_meta_project.py — the engine degrades to a no-op
    # admission check when the identity engine is unavailable.
    from scripts.verify_identity import (
        evaluate_actor_admission,
        load_registry,
        resolve_actor_binding,
    )
except ImportError:  # direct script execution: python scripts/update_meta_project.py
    try:
        from verify_identity import (
            evaluate_actor_admission,
            load_registry,
            resolve_actor_binding,
        )
    except ImportError:  # identity engine unavailable in this environment
        evaluate_actor_admission = None
        load_registry = None
        resolve_actor_binding = None

try:  # project boundary zone classifier (Wave D E2, T6.2): defensive import
    from scripts.check_boundaries import classify_path, parse_boundaries
except ImportError:  # direct script execution: python scripts/update_meta_project.py
    try:
        from check_boundaries import classify_path, parse_boundaries
    except ImportError:  # boundary gate unavailable in this environment
        classify_path = None
        parse_boundaries = None

try:  # shared helpers (ADR #22): package-style import (tests: from
    # scripts.update_meta_project import ...)
    from scripts._common import _error_envelope, _utc_now_iso
except ImportError:  # direct script execution: python scripts/update_meta_project.py
    from _common import _error_envelope, _utc_now_iso

# ---------------------------------------------------------------------------
# Contracts (ADR #21)
# ---------------------------------------------------------------------------

FRAMEWORK_LOCK_RELPATH = ".agents/framework-lock.json"
UPDATE_JOURNAL_RELPATH = ".agents/update-journal.json"
PROJECT_BOUNDARIES_RELPATH = ".agents/project_knowledge/boundaries.md"
# SEC-4 (Wave F FX, T-X3): per-repository serialization of the mutating
# engine operations (apply, restore).
ENGINE_LOCK_RELPATH = ".agents/update-engine.lock"
# SEC-2 (Wave F FX, T-X2): per-cache-entry trust anchor.
CACHE_MANIFEST_RELPATH = "cache-manifest.json"
LOCK_SCHEMA_VERSION = "1.0.0"
REPORT_TYPE = "update"

# Manifest readability (Wave D E2, T6.8): a source revision carries its
# release manifest at the root ``manifest.json`` OR at the canonical framework
# repository layout ``templates/release_manifest.json``.
MANIFEST_RELPATH = "manifest.json"
CANONICAL_MANIFEST_RELPATH = "templates/release_manifest.json"
_EXACT_SHA_RE = re.compile(r"[0-9a-f]{40}")

DEFAULT_PROTECT_PATTERNS = (
    "scripts/check_boundaries.py",
    "*boundaries.md",
    ".github/workflows/**",
    ".github/CODEOWNERS",
    "tests/test_integrity.py",
    "LICENSE",
    ".gitignore",
)

# local_repo_data markers, HTML form (document/markup files):
# <!-- local_repo_data:TAG --> ... <!-- /local_repo_data:TAG -->
LOCAL_REPO_DATA_RE = re.compile(
    rb"<!--\s*local_repo_data:([A-Za-z0-9_.:-]+)\s*-->"  # opener
    rb".*?"                                              # preserved local payload
    rb"<!--\s*/local_repo_data:\1\s*-->",                # closer (same tag)
    re.DOTALL,
)

# local_repo_data markers, code form (code/config files, Wave D E2 D7):
# '# local_repo_data:TAG' ... '# /local_repo_data:TAG' (line-based).
HASH_LOCAL_REPO_DATA_RE = re.compile(
    rb"^[ \t]*#[ \t]*local_repo_data:([A-Za-z0-9_.:-]+)[ \t]*\r?\n"   # opener line
    rb".*?"                                                            # preserved local payload
    rb"^[ \t]*#[ \t]*/local_repo_data:\1[ \t]*(?:\r?\n|\Z)",           # closer line
    re.DOTALL | re.MULTILINE,
)

_HTML_MARKER_EXTENSIONS = frozenset({".md", ".html", ".xml"})
_HASH_MARKER_EXTENSIONS = frozenset(
    {".py", ".toml", ".yml", ".yaml", ".sh", ".cfg", ".ini"}
)


def _marker_regexes(rel: str) -> Tuple[Any, ...]:
    """Marker syntax selection per file extension (Wave D E2, D7):
    code/config extensions use the ``#``-comment form; document/markup
    extensions use the HTML-comment form; unknown extensions accept BOTH."""
    ext = Path(rel).suffix.lower()
    if ext in _HASH_MARKER_EXTENSIONS:
        return (HASH_LOCAL_REPO_DATA_RE,)
    if ext in _HTML_MARKER_EXTENSIONS:
        return (LOCAL_REPO_DATA_RE,)
    return (LOCAL_REPO_DATA_RE, HASH_LOCAL_REPO_DATA_RE)

RED_ZONE_REFUSAL_TEXT = (
    "no sanctioned mechanism exists for automated changes to protected "
    "components (Red-Zone sanction is an explicitly open item); apply a "
    "manual, user-confirmed change instead"
)


class UpdateEngineError(Exception):
    """Base class for managed-update engine failures."""


class SourceResolutionError(UpdateEngineError):
    """A ref could not be pinned to an exact commit SHA."""


class PathContainmentError(UpdateEngineError):
    """SEC-1 (Wave F FX, T-X1): a managed path escapes (or threatens to
    escape) its containment root — absolute, parent-traversing, or a symlink
    destination. Refused with ZERO writes."""


class MixedBundleError(UpdateEngineError):
    """The bundle cannot be materialized as ONE integral revision."""


class PartialCacheError(MixedBundleError):
    """A cache entry exists but is not an integral bundle: refused."""


class CacheVerificationError(MixedBundleError):
    """SEC-2 (Wave F FX, T-X2): a cache bundle cannot be verified as integral
    and untouched (cache-manifest.json mismatch, wrong ref-pin, or origin
    cross-check mismatch). Unverified cache bytes are never used."""


class EngineBusyError(UpdateEngineError):
    """SEC-4 (Wave F FX, T-X3): another engine operation (apply/restore)
    holds the repository lock; concurrent operations are refused."""


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


def _installed_match(local_bytes: Optional[bytes], recorded_sha: str) -> tuple:
    """Compare working-copy bytes against the lock-recorded installed sha256.

    Returns ``(matched, eol_only)``. A raw hash match is an exact match; a
    match after CRLF→LF normalization is an EOL-only match (autocrlf smudge
    on a fresh checkout) — still an installed match, flagged for an explicit
    diagnostic. Content changes never match. Cache-manifest verification and
    origin cross-verification do NOT use this helper: they stay byte-exact
    (Wave G Lane H)."""
    if not recorded_sha or local_bytes is None:
        return (False, False)
    if _sha256_bytes(local_bytes) == recorded_sha:
        return (True, False)
    normalized = local_bytes.replace(b"\r\n", b"\n")
    if _sha256_bytes(normalized) == recorded_sha:
        return (True, True)
    return (False, False)


def _entry_installed_sha(entry: Optional[Dict[str, Any]]) -> str:
    """Installed-state sha256 of a lock component entry.

    Lock component schema v2 records ``installed_sha256`` (bytes actually on
    disk); v1 entries carried a single ``sha256`` — read as the installed
    state (and as the base) for backward compatibility."""
    for key in ("installed_sha256", "sha256"):
        value = str((entry or {}).get(key, "") or "")
        if value:
            return value
    return ""


def _entry_base_sha(entry: Optional[Dict[str, Any]]) -> str:
    """Merge-base sha256 of a lock component entry.

    Schema v2 records ``base_sha256`` — the pinned upstream bytes the
    installation was built from; v1 ``sha256`` is read as the base for
    backward compatibility. Base-candidate acceptance stays hash-verified
    against this record regardless of which cache provided the bytes."""
    for key in ("base_sha256", "sha256"):
        value = str((entry or {}).get(key, "") or "")
        if value:
            return value
    return ""


_WINDOWS_DRIVE_RE = re.compile(r"^[A-Za-z]:")


def _posix(rel: Any) -> str:
    """Normalize a managed relative path to posix form.

    SEC-1 layer (a) (Wave F FX, T-X1): paths that are absolute (posix- or
    Windows-style, including drive letters and UNC shares) or carry ANY
    ``..`` segment are hostile and REFUSED here — every component path, lock
    target, plan action target, journal path and cache-manifest component
    flows through this function, so a hostile manifest cannot even reach the
    write stage."""
    raw = str(rel)
    normalized = raw.replace("\\", "/")
    if (
        normalized.startswith("/")
        or Path(raw).is_absolute()
        or _WINDOWS_DRIVE_RE.match(normalized)
        or ".." in normalized.split("/")
    ):
        raise PathContainmentError(
            f"hostile managed path refused: {raw!r} must be a relative "
            "repo-internal path without '..' segments (SEC-1 containment)"
        )
    return normalized.strip("/")


def assert_within(root: Path, rel: str) -> Path:
    """SEC-1 layers (b)+(c) (Wave F FX, T-X1): resolve ``rel`` against
    ``root`` and REFUSE unless the resolved destination stays inside the
    resolved root, and the destination itself is not an existing symlink.

    Call before EVERY write/read of an engine-managed destination (apply,
    cache reads, restore, journal/lock/report writes): resolve() follows
    directory junctions and symlinks, so a link planted above the destination
    resolves outside the root and is refused; a symlink AT the destination is
    refused outright (U9 exfil guard). Returns the RESOLVED destination."""
    root_resolved = Path(root).resolve()
    dest = Path(root) / _posix(rel)
    if dest.is_symlink():
        raise PathContainmentError(
            f"destination '{_posix(rel)}' is an existing symlink; refusing "
            "to read or write through it (SEC-1 symlink guard)"
        )
    resolved = dest.resolve()
    try:
        resolved.relative_to(root_resolved)
    except ValueError:
        raise PathContainmentError(
            f"managed path '{_posix(rel)}' resolves to '{resolved}' which is "
            f"OUTSIDE the containment root '{root_resolved}'; refusing "
            "(SEC-1 containment)"
        )
    return resolved


def _run_git(args: List[str], text: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["git", *args],
        capture_output=True,
        text=text,
    )


def _atomic_write_bytes(path: Path, data: bytes) -> None:
    """Per-file atomic replace: write to a sibling temp file, fsync the temp
    file (T-X7 durability: the payload survives a crash across the rename),
    then os.replace. On failure the temp file is cleaned up and the
    destination is left untouched."""
    tmp = path.parent / f".{path.name}.updtmp-{uuid.uuid4().hex[:8]}"
    try:
        with open(tmp, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp, path)
    except OSError:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise


def _split_list(value: Optional[str]) -> List[str]:
    if not value:
        return []
    return [item.strip() for item in value.split(",") if item.strip()]


# ---------------------------------------------------------------------------
# SEC-4 serialization (Wave F FX, T-X3): ported from scripts/compact_memory.py
# (file_lock, lines 20-61) with a non-blocking engine-operation wrapper.
# ---------------------------------------------------------------------------


@contextlib.contextmanager
def file_lock(target: Any, blocking: bool = True):
    """Cross-platform file locking context manager (Windows msvcrt / Unix
    fcntl). Accepts an open file object or a Path/str path."""
    opened_here = False
    if isinstance(target, (str, os.PathLike)):
        p = Path(target)
        p.parent.mkdir(parents=True, exist_ok=True)
        f = open(p, "a+b")
        opened_here = True
    else:
        f = target

    fd = f.fileno()

    if sys.platform == "win32":
        import msvcrt

        f.seek(0)
        mode = msvcrt.LK_LOCK if blocking else msvcrt.LK_NBLCK
        msvcrt.locking(fd, mode, 1)
        try:
            yield f
        finally:
            f.seek(0)
            try:
                msvcrt.locking(fd, msvcrt.LK_UNLCK, 1)
            finally:
                if opened_here:
                    f.close()
    else:
        import fcntl

        mode = fcntl.LOCK_EX if blocking else (fcntl.LOCK_EX | fcntl.LOCK_NB)
        fcntl.flock(fd, mode)
        try:
            yield f
        finally:
            try:
                fcntl.flock(fd, fcntl.LOCK_UN)
            finally:
                if opened_here:
                    f.close()


@contextlib.contextmanager
def engine_operation_lock(repo_root: Path):
    """Serialize the MUTATING engine operations (apply, restore) per
    repository (SEC-4): a non-blocking exclusive lock on
    ``.agents/update-engine.lock`` (msvcrt/fcntl, per ``file_lock``). A
    second concurrent engine operation is refused immediately with
    EngineBusyError ("ENGINE_BUSY") — it never writes a journal or touches
    any file. Only lock ACQUISITION maps to EngineBusyError; body failures
    keep their own error types."""
    lock_path = Path(repo_root) / ENGINE_LOCK_RELPATH
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    handle = open(lock_path, "a+b")
    fd = handle.fileno()
    try:
        if sys.platform == "win32":
            import msvcrt

            handle.seek(0)
            msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
        else:
            import fcntl

            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError as exc:
        handle.close()
        raise EngineBusyError(
            "ENGINE_BUSY: another update-engine operation (apply or restore) "
            "is already running on this repository; refusing concurrent "
            "execution"
        ) from exc
    try:
        yield handle
    finally:
        try:
            handle.seek(0)
            if sys.platform == "win32":
                import msvcrt

                msvcrt.locking(fd, msvcrt.LK_UNLCK, 1)
            else:
                import fcntl

                fcntl.flock(fd, fcntl.LOCK_UN)
        except OSError:
            pass
        finally:
            handle.close()


def _read_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def default_cache_dir() -> Path:
    """Cache resolution: explicit flag > env META_PROJECT_CACHE > OS cache
    path (Windows: %LOCALAPPDATA%/meta-project/cache; POSIX:
    ~/.cache/meta-project). The engine reads cache entries read-only; the
    only cache write is the SEC-2 trust anchor ``cache-manifest.json`` that
    apply writes (seal_cache=True) after cross-verifying an unsealed entry
    against the origin."""
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


def resolve_pin_with_cache_fallback(
    source: str, ref: str, cache_dir: Optional[Path] = None
) -> Dict[str, str]:
    """CLI pin resolution (Wave D E2, T6.3/D2): resolve against the origin
    first; when the origin is UNREACHABLE but ``ref`` is already an EXACT
    40-hex commit SHA, trust it as the pin and let ``load_bundle`` satisfy
    the bundle from the integral cache (``origin: cache``). Refs that are
    NOT exact SHAs still require a resolvable origin — a moving ref can
    never be pinned offline."""
    try:
        return resolve_source(source, ref)
    except SourceResolutionError:
        raw = str(ref or "").strip().lower()
        if _EXACT_SHA_RE.fullmatch(raw):
            pin: Dict[str, str] = {
                "commit_sha": raw,
                "origin_repo": str(source),
                "ref": str(ref),
            }
            pinned_dir = (
                Path(cache_dir) / raw if cache_dir is not None else None
            )
            if pinned_dir is not None and pinned_dir.is_dir():
                return pin
            # No cache entry either: fall through to the original refusal so
            # the error names the unresolvable origin, not a missing cache.
        raise


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
    byte-exact (no EOL conversion). The release manifest is read at the
    source root, falling back to the canonical framework layout
    ``templates/release_manifest.json`` (T6.8)."""
    files: Dict[str, bytes] = {}
    try:
        manifest_blob = _git_blob(origin_repo, sha, MANIFEST_RELPATH)
        manifest_rel = MANIFEST_RELPATH
    except MixedBundleError:
        manifest_blob = _git_blob(origin_repo, sha, CANONICAL_MANIFEST_RELPATH)
        manifest_rel = CANONICAL_MANIFEST_RELPATH
    files[manifest_rel] = manifest_blob
    files[MANIFEST_RELPATH] = manifest_blob  # canonical alias
    try:
        manifest = json.loads(manifest_blob.decode("utf-8"))
    except (UnicodeDecodeError, ValueError) as exc:
        raise MixedBundleError(
            f"manifest of pinned commit {sha} in '{origin_repo}' "
            f"({manifest_rel}) is not valid JSON: {exc}"
        )
    for comp in _components_of(manifest):
        rel = _posix(comp.get("path", ""))
        files[rel] = _git_blob(origin_repo, sha, rel)
    return files


def _components_of(manifest: Dict[str, Any]) -> List[Dict[str, Any]]:
    comps = manifest.get("components") or []
    return [c for c in comps if isinstance(c, dict) and _posix(c.get("path", ""))]


# ---------------------------------------------------------------------------
# SEC-2 cache verification (Wave F FX, T-X2)
# ---------------------------------------------------------------------------


def build_cache_manifest(
    files: Dict[str, bytes], commit_sha: str, ref: str = ""
) -> Dict[str, Any]:
    """SEC-2 trust anchor for one cache entry: the ref-pin (commit sha) the
    bundle was built from + the sha256 of EVERY bundle file (manifest.json +
    all manifest components). Written at bundle build/seal time and verified
    at every load."""
    return {
        "schema_version": LOCK_SCHEMA_VERSION,
        "commit_sha": str(commit_sha),
        "ref": str(ref or commit_sha),
        "files": {
            rel: _sha256_bytes(data) for rel, data in sorted(files.items())
        },
    }


def seal_cache_entry(
    pinned_dir: Path, files: Dict[str, bytes], commit_sha: str, ref: str = ""
) -> Optional[Path]:
    """Write ``<pinned_dir>/cache-manifest.json`` (SEC-2). Best-effort: a
    read-only cache directory degrades to an unsealed entry (future loads
    fall back to origin cross-verification)."""
    payload = json.dumps(
        build_cache_manifest(files, commit_sha, ref), indent=2, sort_keys=True
    ) + "\n"
    target = pinned_dir / CACHE_MANIFEST_RELPATH
    try:
        _atomic_write_bytes(target, payload.encode("utf-8"))
    except OSError:
        return None
    return target


def _load_cache_manifest(
    bundle_root: Path, expected_sha: str
) -> Optional[Dict[str, Any]]:
    """Read and validate the cache entry's trust anchor. Returns None when
    the entry is unsealed; raises CacheVerificationError on a malformed
    manifest or a WRONG REF-PIN (cache sealed for pin A but served for pin
    B — the post-rewrite window attack)."""
    cm_path = bundle_root / CACHE_MANIFEST_RELPATH
    if not cm_path.is_file():
        return None
    try:
        entry = _read_json(cm_path)
    except (OSError, ValueError) as exc:
        raise CacheVerificationError(
            f"cache manifest '{cm_path}' is unreadable (fail-closed): {exc}"
        )
    if (
        not isinstance(entry, dict)
        or not str(entry.get("commit_sha", "")).strip()
        or not isinstance(entry.get("files"), dict)
    ):
        raise CacheVerificationError(
            f"cache manifest '{cm_path}' is malformed (expected an object "
            "with 'commit_sha' and a 'files' sha256 map); refusing to use "
            "unverifiable cache bytes"
        )
    sealed_pin = str(entry["commit_sha"])
    if expected_sha and sealed_pin != expected_sha:
        raise CacheVerificationError(
            f"cache bundle '{bundle_root}' was sealed for commit {sealed_pin} "
            f"but the requested pin is {expected_sha}; refusing cross-pin "
            "cache reuse (SEC-2 ref-pin mismatch)"
        )
    return entry


def _read_bundle_tree(
    bundle_root: Path, expected_sha: str = ""
) -> Tuple[Dict[str, Any], Dict[str, bytes], bool]:
    """Read an INTEGRAL and VERIFIED bundle directory: the release manifest
    (root ``manifest.json`` or the canonical ``templates/release_manifest.json``
    fallback, T6.8) + EVERY manifest component.

    SEC-1: every component read is contained (assert_within) — a hostile
    cache manifest component ('../..' etc.) is refused, never read.
    SEC-2: when the entry carries ``cache-manifest.json``, every file hash
    and the ref-pin are verified; returns ``verified=True``. An unsealed
    entry returns ``verified=False`` (the caller must cross-verify against
    the origin or refuse). A missing component stays a PartialCacheError."""
    manifest_path = bundle_root / MANIFEST_RELPATH
    manifest_rel = MANIFEST_RELPATH
    if not manifest_path.is_file():
        manifest_path = bundle_root / Path(*CANONICAL_MANIFEST_RELPATH.split("/"))
        manifest_rel = CANONICAL_MANIFEST_RELPATH
    if not manifest_path.is_file():
        raise MixedBundleError(
            f"cache entry '{bundle_root}' carries no release manifest "
            f"(looked for '{MANIFEST_RELPATH}' and "
            f"'{CANONICAL_MANIFEST_RELPATH}'); refusing to guess an integral "
            "component set"
        )
    try:
        manifest = _read_json(manifest_path)
    except (OSError, ValueError) as exc:
        raise MixedBundleError(
            f"cache manifest '{manifest_path}' is unreadable: {exc}"
        )
    cache_entry = _load_cache_manifest(bundle_root, expected_sha)
    manifest_bytes = manifest_path.read_bytes()
    if cache_entry is not None:
        recorded = str(cache_entry["files"].get(manifest_rel, "") or "")
        if not recorded:
            raise CacheVerificationError(
                f"cache manifest does not cover '{manifest_rel}' of bundle "
                f"'{bundle_root}'; refusing an unverifiable bundle"
            )
        if _sha256_bytes(manifest_bytes) != recorded:
            raise CacheVerificationError(
                f"cache integrity failure for '{manifest_rel}' in bundle "
                f"'{bundle_root}': sha256 mismatch against cache-manifest.json "
                "(tampered or corrupted cache); refusing"
            )
    files: Dict[str, bytes] = {
        manifest_rel: manifest_bytes,
        MANIFEST_RELPATH: manifest_bytes,  # canonical alias
    }
    for comp in _components_of(manifest):
        rel = _posix(comp.get("path", ""))
        comp_path = assert_within(bundle_root, rel)
        if not comp_path.is_file():
            raise PartialCacheError(
                f"partial cache refused: bundle '{bundle_root}' is missing "
                f"manifest component '{rel}'; only an INTEGRAL single-revision "
                "bundle is acceptable (mixing revisions is prohibited)"
            )
        data = comp_path.read_bytes()
        if cache_entry is not None:
            recorded = str(cache_entry["files"].get(rel, "") or "")
            if not recorded:
                raise CacheVerificationError(
                    f"cache manifest does not cover component '{rel}' of "
                    f"bundle '{bundle_root}'; refusing an unverifiable bundle"
                )
            if _sha256_bytes(data) != recorded:
                raise CacheVerificationError(
                    f"cache integrity failure for '{rel}' in bundle "
                    f"'{bundle_root}': sha256 mismatch against "
                    "cache-manifest.json (tampered or corrupted cache); "
                    "refusing"
                )
        files[rel] = data
    return manifest, files, (cache_entry is not None)


def _collect_base_candidates(cache_root: Optional[Path]) -> Dict[str, bytes]:
    """Pool of merge-base candidates: contents of ALL cached bundles (keyed
    by path, first-found wins). NO blanket exclusion of the pinned bundle
    itself (Wave D E1, D6): when the pin matches the installed revision, the
    pinned cache entry holds the exact lock-recorded base bytes. plan_updates
    accepts a candidate only when its sha256 hash-verifies against the
    lock-recorded base sha — a candidate matching ``base_sha256`` is valid
    regardless of which cache directory provided it, and non-matching
    candidates are inert."""
    base: Dict[str, bytes] = {}
    if cache_root is None:
        return base
    cache_root = Path(cache_root)
    if not cache_root.is_dir():
        return base
    for child in sorted(cache_root.iterdir()):
        if not child.is_dir():
            continue
        manifest_path = child / MANIFEST_RELPATH
        if not manifest_path.is_file():
            manifest_path = child / Path(*CANONICAL_MANIFEST_RELPATH.split("/"))
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
            # SEC-1: cache reads are contained — a hostile component path in
            # a cached manifest is refused, never read outside the cache.
            comp_path = assert_within(child, rel)
            if comp_path.is_file():
                try:
                    base[rel] = comp_path.read_bytes()
                except OSError:
                    continue
    return base


def _verify_cache_against_origin(
    origin_repo: str, sha: str, files: Dict[str, bytes]
) -> None:
    """SEC-2 origin cross-check (Wave F FX, T-X2): compare a cache bundle
    against the origin's blobs for the SAME pinned commit — every COMPONENT
    hash must match the origin blob byte-exactly; the manifest file is
    compared newline-normalized (cache builders may have written it in text
    mode, while cat-file returns the canonical blob). Any mismatch — or an
    origin that cannot provide the pinned tree for an unsealed entry — is a
    CacheVerificationError; cache bytes are never served on trust."""
    check = _run_git(
        ["-C", origin_repo, "rev-parse", "--verify", f"{sha}^{{commit}}"]
    )
    if check.returncode != 0:
        raise CacheVerificationError(
            f"cache entry has no cache-manifest.json and the origin "
            f"'{origin_repo}' cannot provide pinned commit {sha} "
            f"({(check.stderr or '').strip()}); refusing unverified cache "
            "bytes (SEC-2 fail-closed)"
        )
    origin_files = _fetch_pinned_tree(origin_repo, sha)
    manifest_rels = {MANIFEST_RELPATH, CANONICAL_MANIFEST_RELPATH}
    for rel, data in sorted(files.items()):
        origin_data = origin_files.get(rel)
        if origin_data is None:
            raise CacheVerificationError(
                f"cache integrity failure for '{rel}': the origin blob of "
                f"pinned commit {sha} is missing; refusing the cache bundle"
            )
        if rel in manifest_rels:
            cached_norm = data.replace(b"\r\n", b"\n")
            origin_norm = origin_data.replace(b"\r\n", b"\n")
            if cached_norm != origin_norm:
                raise CacheVerificationError(
                    f"cache integrity failure for '{rel}': cached bytes "
                    f"differ from the origin blob of pinned commit {sha} "
                    "(tampered or corrupted cache); refusing"
                )
        elif origin_data != data:
            raise CacheVerificationError(
                f"cache integrity failure for '{rel}': cached bytes differ "
                f"from the origin blob of pinned commit {sha} (tampered or "
                "corrupted cache); refusing"
            )


def load_bundle(
    pin: Dict[str, str],
    cache_dir: Optional[Path],
    seal_cache: bool = False,
) -> Dict[str, Any]:
    """Materialize the ENTIRE component set from the pinned SHA.

    Resolution order: integral VERIFIED cache entry ``<cache_dir>/<commit_sha>/``
    first, then ``git cat-file <sha>:<path>`` from the origin repository.
    SEC-2 (Wave F FX, T-X2): a sealed cache entry (``cache-manifest.json``)
    is hash-verified on load and additionally cross-verified against the
    origin blobs whenever the origin provides the pinned commit; an UNSEALED
    cache entry must cross-verify against the origin or is REFUSED —
    unverified cache bytes are never served. ``seal_cache=True`` (apply/load
    time) writes the trust anchor for a cross-verified entry so future
    offline loads can verify it.

    Raises MixedBundleError when neither source yields one integral revision,
    PartialCacheError for a partial cache entry, and CacheVerificationError
    for a tampered/mis-pinned/unverifiable cache. A missing merge base is NOT
    an exception: the empty ``base`` pool flows into plan_updates, which
    turns it into ``needs_base`` entries.

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
        manifest, files, verified = _read_bundle_tree(pinned_dir, expected_sha=sha)
        if verified:
            # SEC-2: sealed entry — hash-verified above; additionally
            # cross-verify against the origin whenever the origin still
            # provides the pinned commit (defense against a malicious sealer).
            check = _run_git(
                ["-C", origin_repo, "rev-parse", "--verify", f"{sha}^{{commit}}"]
            )
            if check.returncode == 0:
                _verify_cache_against_origin(origin_repo, sha, files)
        else:
            # SEC-2: unsealed entry — the origin is the only trust anchor.
            _verify_cache_against_origin(origin_repo, sha, files)
            if seal_cache:
                seal_cache_entry(pinned_dir, files, sha, str(pin.get("ref") or sha))
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

    # Merge-base candidate pool: every cached bundle (NO pinned-sha exclusion,
    # D6), then the pinned bundle's own files as fallback candidates. A
    # candidate becomes a merge base ONLY when its sha256 hash-verifies
    # against the lock record in plan_updates — the pinned bytes are by
    # definition the base whenever pin.sha == lock base_sha256, and inert
    # otherwise.
    base_pool = _collect_base_candidates(cache_root)
    for rel, data in files.items():
        if rel not in base_pool:
            base_pool[rel] = data

    return {
        "manifest": manifest,
        "files": files,
        "base": base_pool,
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


def _lock_payload_bytes(lock: Dict[str, Any]) -> bytes:
    """Canonical on-disk bytes of a framework lock (deterministic, so the
    journal can record the exact after-image before the write)."""
    return (json.dumps(lock, indent=2, ensure_ascii=False) + "\n").encode("utf-8")


def write_lock_atomic(repo_root: Path, lock: Dict[str, Any]) -> None:
    """Atomically replace ``.agents/framework-lock.json`` (SEC-1 contained)."""
    path = assert_within(Path(repo_root), FRAMEWORK_LOCK_RELPATH)
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = _lock_payload_bytes(lock)
    tmp = path.parent / f".framework-lock.tmp-{uuid.uuid4().hex[:8]}"
    try:
        with open(tmp, "wb") as handle:
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
    # U6 (Wave F FX, T-X4): check answers through the SAME source resolution
    # as plan/apply — a dead origin with an exact-SHA ref and a verified
    # integral cache yields ONE answer for the repo state, not a check
    # failure where apply would succeed.
    pin = resolve_pin_with_cache_fallback(source, ref, cache_dir)
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
        # Runtime layout (T6.5): a recorded ``target`` destination stays
        # authoritative — diagnose the file where it actually lives.
        local = repo_root / _posix(entry.get("target") or path)
        recorded = _entry_installed_sha(entry)
        if not local.is_file():
            diagnostics.append({
                "path": path,
                "status": "missing",
                "detail": "lock-recorded component is absent from the working tree",
            })
            drifted.append(path)
            continue
        local_bytes = local.read_bytes()
        current = _sha256_bytes(local_bytes)
        matched, eol_only = _installed_match(local_bytes, recorded)
        if recorded and not matched:
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
        elif eol_only:
            # Wave G Lane H: a fresh autocrlf checkout smudges LF-installed
            # files to CRLF. Line-endings-only difference is NOT drift — the
            # content matches the installation record (an EOL-only flip is
            # not tampering; cache-manifest and origin cross-verification
            # remain byte-exact). Surface an explicit diagnostic instead.
            diagnostics.append({
                "path": path,
                "status": "eol_normalized",
                "detail": (
                    "working-copy bytes differ from the lock record by "
                    "CRLF/LF line endings only (autocrlf checkout); content "
                    "matches the installation record"
                ),
            })

    for comp in _components_of(bundle.get("manifest") or {}):
        path = _posix(comp.get("path", ""))
        if path not in lock_by_path:
            continue
        new_sha = _sha256_bytes(bundle_files.get(path, b""))
        if new_sha != _entry_installed_sha(lock_by_path[path]):
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


def _origin_base_candidate(
    bundle: Dict[str, Any], lock: Optional[Dict[str, Any]], rel: str, want_sha: str
) -> Optional[bytes]:
    """Merge-base candidate fetched from the ORIGIN repository at the
    lock-recorded source commit (Wave D dispatcher tail, live S1): a v1 -> v2
    update on a consumer without a populated cache must still find its base
    in the origin's git history — the lock pins the exact commit the current
    content was installed from. Content is hash-verified against the lock
    record before use; any failure (dead origin, missing path, hash drift)
    returns None so the needs_base refusal path stays fail-closed."""
    origin_repo = str((bundle or {}).get("origin_repo") or "")
    sha = str(((lock or {}).get("source") or {}).get("commit_sha") or "")
    if not origin_repo or not re.fullmatch(r"[0-9a-f]{40}", sha):
        return None
    try:
        blob = _git_blob(origin_repo, sha, rel)
    except MixedBundleError:
        return None
    if _sha256_bytes(blob) == want_sha:
        return blob
    return None


def _extract_local_blocks(
    data: bytes, rel: str = ""
) -> List[Tuple[bytes, bytes]]:
    """All local_repo_data blocks applicable to ``rel`` (syntax per
    extension, D7); duplicate raw blocks are reported once."""
    blocks: List[Tuple[bytes, bytes]] = []
    seen: List[bytes] = []
    for marker_re in _marker_regexes(rel):
        for m in marker_re.finditer(data):
            block = m.group(0)
            if block not in seen:
                seen.append(block)
                blocks.append((block, m.group(1)))
    return blocks


def _reinject_local_blocks(
    new_bytes: bytes, local_blocks: List[Tuple[bytes, bytes]]
) -> bytes:
    """Re-inject extracted local_repo_data blocks into fresh upstream bytes
    (append order preserved; blocks already present upstream are skipped)."""
    merged = new_bytes
    for block, _tag in local_blocks:
        if block in merged:
            continue
        if merged and not merged.endswith(b"\n"):
            merged += b"\n"
        if not block.endswith(b"\n"):
            block = block + b"\n"
        merged += block
    return merged


def _merge_marked(
    base: bytes, local: bytes, new: bytes, rel: str = ""
) -> Tuple[Optional[bytes], List[bytes]]:
    """Three-way merge honoring local_repo_data blocks (syntax selected per
    ``rel`` extension, D7).

    Returns (merged_bytes, preserved_tags) when the local tree differs from
    the base ONLY inside marker-wrapped blocks; returns (None, []) when an
    unmarked local modification exists (conflict — never silently
    overwritten).
    """
    local_blocks = _extract_local_blocks(local, rel)
    stripped_local = local
    stripped_base = base
    for marker_re in _marker_regexes(rel):
        stripped_local = marker_re.sub(b"", stripped_local)
        stripped_base = marker_re.sub(b"", stripped_base)
    if stripped_local.strip(b" \t\r\n") != stripped_base.strip(b" \t\r\n"):
        return None, []
    preserved: List[bytes] = [tag for _block, tag in local_blocks]
    return _reinject_local_blocks(new, local_blocks), preserved


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


# ---------------------------------------------------------------------------
# Perimeter: identity admission, project boundaries, runtime layout (Wave D E2)
# ---------------------------------------------------------------------------


def _actor_admission_denial(repo_root: Path, actor_id: str) -> Optional[str]:
    """Identity admission for the apply perimeter (Wave D E2, T6.1, п.5a).

    Mirrors ``scripts/inspect_meta_project.py``'s defensive import: when
    ``verify_identity`` is unavailable the check degrades to a no-op.
    Identity-LESS projects (no registry, or a registry without recorded role
    bindings — the bootstrap state, cf. manage-meta-project Phase 0.5) are
    not blocked either; otherwise the actor must be registered AND active on
    an active installation. Returns None when admitted, else a denial reason
    starting with ``actor not admitted``."""
    if resolve_actor_binding is None:
        return None
    repo_root = Path(repo_root)
    registry: Optional[Dict[str, Any]] = None
    if load_registry is not None:
        try:
            registry, _err = load_registry(repo_root)
        except Exception:  # noqa: BLE001 - admission must never crash apply setup
            registry = None
    if not isinstance(registry, dict) or not (registry.get("roles") or {}):
        return None  # identity-less installation (bootstrap): admission not applicable
    try:
        found, binding = resolve_actor_binding(repo_root, str(actor_id or ""))
    except Exception:  # noqa: BLE001 - fail closed on lookup errors
        found, binding = False, None
    if not found or not isinstance(binding, dict):
        return (
            f"actor not admitted: actor_id '{actor_id}' is not registered in "
            ".agents/meta-project.json; resolve the qualified identity "
            "(scripts/verify_identity.py) before applying updates"
        )
    if evaluate_actor_admission is not None:
        denial = evaluate_actor_admission(registry, binding)
        if denial:
            return f"actor not admitted: {denial}"
    return None


def _project_red_zones(repo_root: Path) -> Optional[Dict[str, List[str]]]:
    """Parse the project's ``boundaries.md`` into zone patterns (T6.2,
    п.5b). Returns None when the file is absent or the zone classifier is
    unavailable (defensive import, fail-open consult — the engine's own
    DEFAULT protect patterns always remain in force)."""
    if parse_boundaries is None:
        return None
    path = Path(repo_root) / PROJECT_BOUNDARIES_RELPATH
    if not path.is_file():
        return None
    try:
        content = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None
    try:
        return parse_boundaries(content)
    except Exception:  # noqa: BLE001 - the consult must never break planning
        return None


def _project_red_reason(
    rel: str,
    dest_rel: str,
    zones: Optional[Dict[str, List[str]]],
) -> Optional[str]:
    """Explanatory string when the component identity path or its resolved
    on-disk target is classified Red by the project boundaries; else None.
    Only an explicit Red classification protects here (unmapped/Black paths
    are the boundary gate's commit-time business; the engine's DEFAULT
    patterns cover the framework's own protected set)."""
    if not zones or classify_path is None:
        return None
    for candidate in dict.fromkeys((rel, dest_rel)):
        try:
            zone, reason = classify_path(candidate, zones)
        except Exception:  # noqa: BLE001
            continue
        if zone == "red":
            return f"project boundaries Red zone match ({reason})"
    return None


_SKILL_RUNTIME_LAYOUT_RE = re.compile(r"^skills/([^/]+)/SKILL\.md$")
# Wave E Lane P2: skill resources ship WITH the skill (bootstrap invariant),
# so a resource component follows its skill into the runtime layout.
_SKILL_RESOURCE_RUNTIME_LAYOUT_RE = re.compile(r"^skills/([^/]+)/resources/(.+)$")


def _resolve_target(
    rel: str,
    repo_root: Path,
    lock_entry: Optional[Dict[str, Any]],
) -> str:
    """Resolved on-disk destination of a manifest component (T6.5).

    A previously recorded lock ``target`` stays authoritative (no silent
    migration of installed components); otherwise ``skills/<name>/SKILL.md``
    maps to the runtime layout ``.agents/skills/<name>/SKILL.md``, and
    ``skills/<name>/resources/<file>`` maps to
    ``.agents/skills/<name>/resources/<file>`` (Wave E Lane P2), when the
    target repository has ``.agents/skills/``; everything else installs at
    the distribution path."""
    recorded = _posix((lock_entry or {}).get("target") or "")
    if recorded:
        return recorded
    repo_root = Path(repo_root)
    match = _SKILL_RUNTIME_LAYOUT_RE.match(_posix(rel))
    if match and (repo_root / ".agents" / "skills").is_dir():
        return f".agents/skills/{match.group(1)}/SKILL.md"
    res_match = _SKILL_RESOURCE_RUNTIME_LAYOUT_RE.match(_posix(rel))
    if res_match and (repo_root / ".agents" / "skills").is_dir():
        return f".agents/skills/{res_match.group(1)}/resources/{res_match.group(2)}"
    return _posix(rel)


def plan_updates(
    repo_root: Path,
    bundle: Dict[str, Any],
    lock: Optional[Dict[str, Any]],
    protect_patterns: Optional[Tuple[str, ...]] = None,
    adopt_existing: bool = False,
) -> Dict[str, Any]:
    """Compute the three-way merge plan. STRICTLY READ-ONLY (zero-write).

    Returns PlanResult: {actions, conflicts, protected, needs_base, pin}
    plus informational keys (up_to_date, legacy, meta). Actions carry the
    exact bytes to write, the resolved on-disk target, the pinned upstream
    sha256, and expected_before_sha256 — the target state this plan was
    computed against (None for creates), which apply_updates re-verifies
    before writing (п.4: the plan pins what apply may do). Base-less local
    modifications become needs_base entries, never "safe merge" actions.

    Perimeter (Wave D E2):
    * ``protect_patterns`` are ADDITIONAL glob patterns united with
      DEFAULT_PROTECT_PATTERNS (T6.2) — user patterns never replace the
      defaults;
    * every component identity path and its resolved target path is
      consulted against the project's ``.agents/project_knowledge/
      boundaries.md`` Red zone (п.5b);
    * ``adopt_existing`` (friction 5): an unknown-origin local file whose
      bytes hash-equal the pinned upstream content becomes an ``adopt``
      action (lock record only, bytes untouched) instead of a conflict.
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
    effective_patterns: Tuple[str, ...] = tuple(DEFAULT_PROTECT_PATTERNS) + tuple(
        protect_patterns or ()
    )
    red_zones = _project_red_zones(repo_root)

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

        lock_entry = lock_by_path.get(rel)
        dest_rel = _resolve_target(rel, repo_root, lock_entry)

        if _is_protected(rel, effective_patterns):
            protected.append({
                "path": rel,
                "kind": kind,
                "reason": (
                    f"protected component '{rel}': {RED_ZONE_REFUSAL_TEXT}"
                ),
            })
            continue
        project_red = _project_red_reason(rel, dest_rel, red_zones)
        if project_red:
            protected.append({
                "path": rel,
                "kind": kind,
                "reason": (
                    f"protected component '{rel}': classified Red by the "
                    f"project's {PROJECT_BOUNDARIES_RELPATH} "
                    f"({project_red}); {RED_ZONE_REFUSAL_TEXT}"
                ),
            })
            continue

        local_path = repo_root / dest_rel
        local_bytes = local_path.read_bytes() if local_path.is_file() else None
        # Lock component schema v2: installed (on-disk) and base (pinned
        # upstream) shas recorded separately; v1 entries carry a single
        # sha256 read as BOTH (backward compatibility).
        recorded_installed = _entry_installed_sha(lock_entry)
        recorded_base = _entry_base_sha(lock_entry)
        current_sha = _sha256_bytes(local_bytes) if local_bytes is not None else None
        upstream_sha = _sha256_bytes(new_bytes)

        if lock_entry is None:
            if local_bytes is None:
                actions.append({
                    "path": rel, "kind": kind, "op": "create",
                    "target": dest_rel,
                    "content": new_bytes,
                    "upstream_sha256": upstream_sha,
                    "expected_before_sha256": None,
                })
            elif (
                adopt_existing
                and current_sha
                and current_sha == upstream_sha
            ):
                # Sha-match adoption (T6.7, friction 5): the verified local
                # bytes ARE the pinned content of unknown provenance — adopt
                # them into the lock without rewriting the file.
                actions.append({
                    "path": rel, "kind": kind, "op": "adopt",
                    "target": dest_rel,
                    "upstream_sha256": upstream_sha,
                    "expected_before_sha256": current_sha,
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
                "target": dest_rel,
                "content": new_bytes,
                "upstream_sha256": upstream_sha,
                "expected_before_sha256": None,
            })
            continue

        matched_installed, eol_only = _installed_match(
            local_bytes, recorded_installed
        )
        if matched_installed:
            # Local content matches the installation record (exact, or
            # EOL-only after an autocrlf smudge — Wave G Lane H): fast-
            # forward, no merge base bytes needed. NEVER a plain overwrite
            # when the local file carries local_repo_data blocks (п.1): the
            # blocks are extracted and re-injected into the new bytes —
            # equivalent to a merge with an empty base delta. Plain overwrite
            # remains only for marker-free files. EOL-only matches always
            # produce a write (normalizing the copy back to pinned LF bytes);
            # expected_before_sha256 stays the RAW working-tree sha so the
            # apply-time TOCTOU guard remains exact.
            if new_bytes == local_bytes and not eol_only:
                up_to_date.append({"path": rel, "kind": kind, "sha256": current_sha})
            else:
                local_blocks = _extract_local_blocks(local_bytes, rel)
                if local_blocks:
                    actions.append({
                        "path": rel, "kind": kind, "op": "merge",
                        "target": dest_rel,
                        "content": _reinject_local_blocks(new_bytes, local_blocks),
                        "preserved_blocks": [
                            tag.decode("utf-8", "replace") for _block, tag in local_blocks
                        ],
                        "upstream_sha256": upstream_sha,
                        "expected_before_sha256": current_sha,
                    })
                else:
                    actions.append({
                        "path": rel, "kind": kind, "op": "update",
                        "target": dest_rel,
                        "content": new_bytes,
                        "upstream_sha256": upstream_sha,
                        "expected_before_sha256": current_sha,
                    })
            continue

        # Local content diverged from the lock record: an honest three-way
        # merge needs the base (content whose sha256 matches the recorded
        # base — the pinned upstream bytes of the installed revision).
        base_bytes = (bundle.get("base") or {}).get(rel)
        base_ok = (
            base_bytes is not None
            and bool(recorded_base)
            and _sha256_bytes(base_bytes) == recorded_base
        )
        if not base_ok and recorded_base:
            fetched = _origin_base_candidate(bundle, lock, rel, recorded_base)
            if fetched is not None:
                base_bytes = fetched
                base_ok = True
        if not base_ok:
            needs_base.append({
                "path": rel,
                "reason": (
                    "merge base unavailable: no cached or origin-history content "
                    "matches the lock-recorded base sha256; refusing to claim a "
                    "safe merge (needs_base)"
                ),
                "lock_sha256": recorded_base,
            })
            continue

        merged, preserved = _merge_marked(base_bytes, local_bytes, new_bytes, rel)
        if merged is None:
            conflicts.append({
                "path": rel,
                "reason": (
                    "unmarked local modification outside local_repo_data "
                    "blocks (relative to the recorded base); manual "
                    "resolution required, file will NOT be overwritten"
                ),
                "local_sha256": current_sha,
                "base_sha256": recorded_base,
                "new_sha256": upstream_sha,
            })
        else:
            actions.append({
                "path": rel, "kind": kind, "op": "merge",
                "target": dest_rel,
                "content": merged,
                "preserved_blocks": [tag.decode("utf-8", "replace") for tag in preserved],
                "upstream_sha256": upstream_sha,
                "expected_before_sha256": current_sha,
            })

    plan: Dict[str, Any] = {
        "actions": actions,
        "conflicts": conflicts,
        "protected": protected,
        "needs_base": needs_base,
        "up_to_date": up_to_date,
        "legacy": legacy_notes,
        # п.4: the plan pins the exact source revision it was computed at;
        # apply must refuse any deviation from this pin.
        "pin": {
            "source": bundle.get("origin_repo"),
            "commit_sha": bundle.get("commit_sha"),
            "ref": bundle.get("ref"),
        },
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
    path = assert_within(Path(repo_root), UPDATE_JOURNAL_RELPATH)
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
# Plan serialization: CLI plan output must round-trip into `apply --plan-file`
# ---------------------------------------------------------------------------


def _plan_to_jsonable(plan: Dict[str, Any], full: bool = False) -> Dict[str, Any]:
    """Plan -> JSON-safe dict for CLI output (Wave D E2, T6.6).

    Summary mode (default): actions are described by ops/paths/shas — bytes
    values (content) are OMITTED entirely; the printed plan stays human- and
    audit-readable without base64 blobs. Full mode (``--full``): bytes values
    become ``<key>_b64`` strings, so a user-approved plan can be saved to a
    file and reloaded by ``apply --plan-file`` verbatim. A content-less
    summary plan is still executable: apply re-materializes content from the
    pinned source and hash-verifies it against ``upstream_sha256``."""
    out: Dict[str, Any] = {}
    for key, value in plan.items():
        if key == "actions" and isinstance(value, list):
            jsonable_actions: List[Any] = []
            for action in value:
                if not isinstance(action, dict):
                    jsonable_actions.append(action)
                    continue
                entry: Dict[str, Any] = {}
                for a_key, a_value in action.items():
                    if isinstance(a_value, bytes):
                        if full:
                            entry[f"{a_key}_b64"] = _b64(a_value)
                        # summary mode: content bytes are simply omitted
                    else:
                        entry[a_key] = a_value
                jsonable_actions.append(entry)
            out["actions"] = jsonable_actions
        else:
            out[key] = value
    return out


def _plan_from_jsonable(obj: Dict[str, Any]) -> Dict[str, Any]:
    """Inverse of _plan_to_jsonable: restore bytes action content from
    ``<key>_b64`` fields."""
    plan = dict(obj or {})
    actions: List[Any] = []
    for action in plan.get("actions") or []:
        if not isinstance(action, dict):
            actions.append(action)
            continue
        entry = dict(action)
        for key in [k for k in entry if str(k).endswith("_b64")]:
            raw = entry.pop(key)
            try:
                entry[key[:-4]] = _unb64(raw)
            except (ValueError, TypeError) as exc:
                raise UpdateEngineError(
                    f"plan carries an invalid base64 payload for "
                    f"'{key[:-4]}': {exc}"
                )
        actions.append(entry)
    plan["actions"] = actions
    return plan


def _rematerialize_plan_content(
    repo_root: Path,
    plan: Dict[str, Any],
    cache_dir: Optional[Path] = None,
) -> Dict[str, Any]:
    """Fill content bytes into a content-less (summary-mode) approved plan
    (Wave D E2, T6.6).

    Content is re-materialized from the plan's OWN pinned source revision
    (never a moving ref) and hash-verified against the action's
    ``upstream_sha256``. ``merge`` actions re-inject the local_repo_data
    blocks extracted from the on-disk local file — which apply re-verifies
    against ``expected_before_sha256`` before writing — so the materialized
    bytes equal the ones a full plan would carry. ``adopt`` actions carry no
    content by design."""
    repo_root = Path(repo_root)
    pin_block = plan.get("pin") or {}
    plan_sha = str(pin_block.get("commit_sha", "") or "")
    contentless = [
        a for a in plan.get("actions") or []
        if isinstance(a, dict) and "content" not in a
        and a.get("op") != "adopt"
    ]
    if not contentless:
        return plan
    if not re.fullmatch(r"[0-9a-f]{40}", plan_sha):
        raise UpdateEngineError(
            "approved plan carries no valid source pin; cannot re-materialize "
            "content for its content-less actions"
        )
    bundle = load_bundle(
        {
            "commit_sha": plan_sha,
            "origin_repo": str(pin_block.get("source", "") or ""),
            "ref": str(pin_block.get("ref") or plan_sha),
        },
        cache_dir,
        seal_cache=True,
    )
    bundle_files = bundle.get("files") or {}
    for action in contentless:
        rel = _posix(action.get("path", ""))
        upstream = bundle_files.get(rel)
        if upstream is None:
            raise UpdateEngineError(
                f"approved plan action '{rel}' carries no content and the "
                f"pinned source {plan_sha} does not provide the component"
            )
        expected = str(action.get("upstream_sha256", "") or "")
        if expected and _sha256_bytes(upstream) != expected:
            raise UpdateEngineError(
                f"re-materialized content for '{rel}' does not hash-match the "
                f"plan's upstream_sha256 (plan pins {expected}, pinned source "
                f"provides {_sha256_bytes(upstream)}); refusing to apply"
            )
        if action.get("op") == "merge":
            # SEC-1: the read of the local target is containment-checked.
            local_path = assert_within(
                repo_root, _posix(action.get("target") or rel)
            )
            if not local_path.is_file():
                raise UpdateEngineError(
                    f"approved merge action for '{rel}' has no local file to "
                    "extract local_repo_data blocks from; re-plan against the "
                    "current target state"
                )
            local_bytes = local_path.read_bytes()
            action["content"] = _reinject_local_blocks(
                upstream, _extract_local_blocks(local_bytes, rel)
            )
        else:
            action["content"] = upstream
    return plan


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
    plan_file: Optional[str] = None,
    run_id: Optional[str] = None,
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
    # U7 (Wave F FX, T-X5): the post-update version is split into its two
    # identities — the human-readable framework version AND the exact pinned
    # commit sha (a version string alone is not an audit-grade pin).
    after = {
        "framework_version": str(meta.get("framework_version") or ""),
        "commit_sha": str(meta.get("commit_sha") or before),
    }

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
        # U11 (Wave F FX, T-X5): the report carries the APPLY RUN's run_id —
        # the same id the journal records — so restore can mark THIS report
        # "restored" (one run, one journal, one report).
        "run_id": str(run_id or (generate_run_id(REPORT_TYPE) if generate_run_id else REPORT_TYPE)),
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
        "approved_plan_file": plan_file,
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
    resolved_pin: Optional[Dict[str, str]] = None,
    plan_file: Optional[str] = None,
) -> Dict[str, Any]:
    """Apply the plan atomically, per file, with a crash-recovery journal.

    The plan pins what apply may do (п.4): when ``resolved_pin`` carries a
    freshly re-resolved source pin (CLI path), it must equal the plan's
    pinned commit sha; every action's ``expected_before_sha256`` must match
    the target state on disk BEFORE the first write. Any mismatch is an
    ApplyError with ZERO writes (all-or-nothing preserved).

    Refuses loudly (no writes at all) when the plan carries protected
    components, unresolved conflicts, or needs_base entries. On success the
    framework lock is rewritten atomically — the lock write is itself
    journaled (before/after bytes), so restore rolls back files AND lock
    (п.2) — and an 'update' report is stored via
    scripts/manage_reports.save_report (recording the user-approved plan
    file, when one was given).

    Perimeter (Wave D E2, T6.1): apply is a write operation — the actor
    must be ADMITTED by the project identity registry (registered + active
    binding; identity-less bootstrap projects are not blocked) BEFORE any
    journal or file write. ``adopt`` actions (T6.7) write no file bytes:
    they only record the verified pre-existing content in the lock.

    SEC-4 (Wave F FX, T-X3): the whole operation holds the repository engine
    lock (``.agents/update-engine.lock``, non-blocking) — a second concurrent
    apply/restore is refused with EngineBusyError before ANY state change.
    SEC-1 (Wave F FX, T-X1): every destination read/write is contained via
    assert_within BEFORE any journal entry is created.
    """
    repo_root = Path(repo_root)
    with engine_operation_lock(repo_root):
        return _apply_updates_locked(
            repo_root, plan, actor_id, installation_id,
            resolved_pin=resolved_pin, plan_file=plan_file,
        )


def _apply_updates_locked(
    repo_root: Path,
    plan: Dict[str, Any],
    actor_id: str,
    installation_id: str,
    resolved_pin: Optional[Dict[str, str]] = None,
    plan_file: Optional[str] = None,
) -> Dict[str, Any]:
    """apply_updates body, running under the exclusive engine lock (SEC-4)."""

    admission_denial = _actor_admission_denial(repo_root, actor_id)
    if admission_denial:
        raise ApplyError(admission_denial)

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

    # п.4 (TOCTOU guard, source side): the freshly re-resolved pin must
    # equal the plan's pin — never silently apply a different revision.
    plan_pin = plan.get("pin") or {}
    plan_sha = str(plan_pin.get("commit_sha", "") or "")
    if resolved_pin is not None and plan_sha:
        resolved_sha = str((resolved_pin or {}).get("commit_sha", "") or "")
        if resolved_sha != plan_sha:
            raise ApplyError(
                f"plan/source revision changed: re-plan required (approved "
                f"plan pins {plan_sha}; source now resolves to "
                f"{resolved_sha or 'unresolvable'})"
            )

    # п.4 (TOCTOU guard, target side): verify EVERY action's expected
    # target state before the first write; any mismatch -> ApplyError with
    # ZERO writes. The resolved destination is the action's ``target``
    # (runtime layout, T6.5), falling back to the manifest path.
    # SEC-1: each destination is containment-checked BEFORE it is read — a
    # plan (or approved plan file) carrying an escaping target is refused
    # before the journal is even created.
    stale_targets: List[str] = []
    for action in actions:
        rel = _posix(action.get("path", ""))
        if not rel or "expected_before_sha256" not in action:
            continue
        expected = action.get("expected_before_sha256")
        dest = assert_within(repo_root, _posix(action.get("target") or rel))
        actual = _sha256_file(dest) if dest.is_file() else None
        if actual != expected:
            stale_targets.append(
                f"{rel} (plan expected sha256={expected or 'absent'}; "
                f"found {actual or 'absent'})"
            )
    if stale_targets:
        raise ApplyError(
            "target state changed since plan: " + "; ".join(stale_targets)
            + "; NOTHING was written (all-or-nothing preserved) — re-plan "
            "before applying"
        )

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
            dest_rel = _posix(action.get("target") or rel)
            # SEC-1: contained, symlink-refused destination — the plan's
            # target must resolve INSIDE the repository root before any
            # byte is read or written through it.
            dest = assert_within(repo_root, dest_rel)
            if action.get("op") == "adopt":
                # Sha-match adoption (T6.7): the on-disk bytes were verified
                # against expected_before_sha256 above — record them in the
                # lock WITHOUT rewriting the file.
                applied_actions.append(action)
                continue
            content = action.get("content")
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
    # Component schema v2 (Wave D E1, T1.1): base_sha256 = pinned upstream
    # bytes, installed_sha256 = bytes actually written, target = resolved
    # destination (reserved; defaults to the manifest path). Entries not
    # touched by this apply keep their previous (possibly v1) shape.
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
        dest_rel = _posix(action.get("target") or rel)
        if action.get("op") == "adopt":
            # Adoption (T6.7): installed bytes are the verified pre-existing
            # content; the base is the pinned upstream it hash-equals.
            installed_sha = str(action.get("expected_before_sha256") or "")
            base_sha = str(action.get("upstream_sha256") or installed_sha)
        else:
            content = action["content"]
            installed_sha = _sha256_bytes(content)
            base_sha = str(action.get("upstream_sha256") or installed_sha)
        entry = {
            "path": rel,
            "kind": action.get("kind", ""),
            "base_sha256": base_sha,
            "installed_sha256": installed_sha,
            "target": dest_rel,
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

    # Journal the lock write itself (Wave D E1, T1.2/п.2): before/after
    # bytes recorded BEFORE the lock is replaced, so restore_updates rolls
    # the lock back like any other journaled entry. Atomicity of the lock
    # write itself is preserved (write_lock_atomic).
    lock_path = repo_root / FRAMEWORK_LOCK_RELPATH
    lock_before_bytes = lock_path.read_bytes() if lock_path.is_file() else None
    new_lock_bytes = _lock_payload_bytes(lock)
    lock_journal_entry = {
        "path": FRAMEWORK_LOCK_RELPATH,
        "op": "lock_write",
        "kind": "lock",
        "before_sha256": (
            _sha256_bytes(lock_before_bytes) if lock_before_bytes is not None else None
        ),
        "after_sha256": _sha256_bytes(new_lock_bytes),
        "before_b64": _b64(lock_before_bytes),
        "after_b64": _b64(new_lock_bytes),
        "status": "pending",
    }
    journal["entries"].append(lock_journal_entry)
    _persist_journal()
    write_lock_atomic(repo_root, lock)
    lock_journal_entry["status"] = "applied"

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
            plan_file=plan_file,
            run_id=run_id,
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
        "approved_plan_file": plan_file,
        "installation_id": installation_id,
        "actor_id": actor_id,
    }


def restore_updates(repo_root: Path) -> Dict[str, Any]:
    """Restore ONLY the engine's own changes recorded in the journal.

    Each journaled path is reverted only when its current sha256 still
    matches what the engine wrote; third-party edits made during the apply
    window are detected and preserved (never clobbered). The framework lock
    is an ordinary journaled entry since Wave D E1 (п.2): apply journals it
    with before/after bytes before writing, so a successful restore returns
    files AND ``.agents/framework-lock.json`` to the coherent pre-apply
    state.

    SEC-4 (Wave F FX, T-X3): restore holds the repository engine lock — a
    concurrent apply/restore is refused (EngineBusyError). SEC-1: every
    journaled path is containment-checked (hostile journal paths and symlink
    destinations are refused, never followed). U11 (T-X5): the update report
    of the journaled run is marked ``status: "restored"`` (audit trail
    preserved in place).
    """
    repo_root = Path(repo_root)
    with engine_operation_lock(repo_root):
        return _restore_updates_locked(repo_root)


def _mark_update_report_restored(repo_root: Path, run_id: str) -> Optional[str]:
    """U11 (Wave F FX, T-X5): mark the update report of ``run_id``
    ``status: "restored"`` — in place, preserving the audit trail. Returns
    the report's repo-relative path, or None when no matching report exists
    (e.g. the report engine was unavailable during apply)."""
    reports_root = Path(repo_root) / ".agents" / "reports"
    needle = str(run_id or "").strip()
    if not needle or not reports_root.is_dir():
        return None
    marked: Optional[Path] = None
    for update_dir in sorted(reports_root.glob("*/update")):
        if not update_dir.is_dir():
            continue
        for report_file in sorted(update_dir.glob("*.json")):
            if report_file.name == "latest.json":
                continue
            try:
                data = _read_json(report_file)
            except (OSError, ValueError):
                continue
            if (
                not isinstance(data, dict)
                or str(data.get("report_type", "")) != REPORT_TYPE
                or str(data.get("run_id", "")) != needle
            ):
                continue
            data["status"] = "restored"
            try:
                assert_within(repo_root, _posix(
                    report_file.relative_to(Path(repo_root))
                ))
                _atomic_write_bytes(
                    report_file,
                    (json.dumps(data, indent=2, ensure_ascii=False) + "\n").encode(
                        "utf-8"
                    ),
                )
            except (OSError, ValueError, UpdateEngineError):
                continue
            marked = report_file
            # Keep the latest.json pointer coherent with the marked report.
            pointer = update_dir / "latest.json"
            try:
                if pointer.is_file():
                    ptr = _read_json(pointer)
                    if isinstance(ptr, dict) and str(ptr.get("run_id", "")) == needle:
                        ptr["status"] = "restored"
                        assert_within(repo_root, _posix(
                            pointer.relative_to(Path(repo_root))
                        ))
                        _atomic_write_bytes(
                            pointer,
                            (json.dumps(ptr, indent=2, ensure_ascii=False) + "\n").encode(
                                "utf-8"
                            ),
                        )
            except (OSError, ValueError, UpdateEngineError):
                pass
    if marked is None:
        return None
    return _posix(marked.relative_to(Path(repo_root)))


def _restore_updates_locked(repo_root: Path) -> Dict[str, Any]:
    """restore_updates body, running under the exclusive engine lock (SEC-4)."""
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
        # SEC-1: a journal is engine data, but restore never follows a path
        # outside the repository root or through a symlink.
        target = assert_within(repo_root, rel)
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

    # U11 (T-X5): the report of the journaled run is marked "restored" so
    # the audit trail shows the update was rolled back.
    marked_report = _mark_update_report_restored(
        repo_root, str(journal.get("run_id", "") or "")
    )

    return {
        "status": "restored",
        "journal": UPDATE_JOURNAL_RELPATH,
        "restored": restored,
        "kept": kept,
        "skipped": skipped,
        "marked_report": marked_report,
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
        help="Comma-separated ADDITIONAL protect patterns, united with the "
        "engine defaults (union; user patterns never replace the defaults)",
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
    p_plan.add_argument(
        "--full",
        action="store_true",
        help="Print the machine-consumable plan: actions carry content_b64 "
        "bytes (default output is a content-less summary; a summary plan "
        "file still applies via hash-verified re-materialization)",
    )

    p_apply = subparsers.add_parser(
        "apply", help="Apply the update atomically with journal, lock and update report"
    )
    _add_source_args(p_apply, repo_root_required=True)
    p_apply.add_argument(
        "--adopt-existing",
        action="store_true",
        help="Adopt unknown-origin local files whose bytes hash-equal the "
        "pinned upstream content: record them in the lock WITHOUT rewriting "
        "the file bytes (sha-match adoption)",
    )
    p_apply.add_argument(
        "--plan-file",
        default=None,
        help="Path to a user-approved plan file (output of `plan`). When "
        "absent, a fresh plan is computed and treated as approved — the "
        "[RE-PLANNED] notice is printed. The approved plan's pin is "
        "re-verified against the source before any write.",
    )
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

    # --protect patterns are ADDITIONAL: plan_updates unites them with
    # DEFAULT_PROTECT_PATTERNS (T6.2 union; user patterns never replace the
    # defaults).
    user_protects = tuple(_split_list(getattr(args, "protect", None)))
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
            # U8 (Wave F FX, T-X6): a failing check reports itself in the
            # JSON payload as check_status="failed"; the rc contract is
            # UNCHANGED (rc 1 on failure).
            sys.stderr.write(f"[CHECK-FAILED] {exc}\n")
            print(json.dumps({"check_status": "failed", "error": str(exc)}))
            return 1
        result["check_status"] = "ok"
        print(json.dumps(result, indent=2, default=str))
        return 0 if not (result["updates_available"] or result["drifted"]) else 1

    if args.subcommand == "plan":
        try:
            pin = resolve_pin_with_cache_fallback(args.source, args.ref, cache_dir)
            bundle = load_bundle(pin, cache_dir)
            if components:
                bundle = _filter_bundle(bundle, components)
            lock = read_lock(repo_root)
            plan = plan_updates(
                repo_root, bundle, lock, protect_patterns=user_protects
            )
        except UpdateEngineError as exc:
            sys.stderr.write(f"[PLAN-HALT] {exc}\n")
            return 1
        print(json.dumps(
            _plan_to_jsonable(plan, full=bool(getattr(args, "full", False))),
            indent=2,
            default=str,
        ))
        return 0

    if args.subcommand == "apply":
        plan_file = getattr(args, "plan_file", None)
        try:
            if plan_file:
                # The user-approved plan is loaded verbatim; --source/--ref,
                # when given, re-resolve the source to verify the plan's pin
                # (п.4). When omitted, source/ref fall back to the plan's own
                # pin block. A content-less (summary) plan is executable:
                # content bytes are re-materialized from the plan's own pin
                # and hash-verified (T6.6).
                loaded = _read_json(Path(plan_file))
                if not isinstance(loaded, dict) or not isinstance(
                    loaded.get("actions"), list
                ):
                    raise UpdateEngineError(
                        f"plan file '{plan_file}' is not a serialized update "
                        "plan (expected an object with an 'actions' list)"
                    )
                plan = _plan_from_jsonable(loaded)
                pin_block = plan.get("pin") or {}
                source_arg = str(args.source or pin_block.get("source") or "")
                ref_arg = str(args.ref) if args.source else str(
                    pin_block.get("ref") or args.ref
                )
                pin = resolve_pin_with_cache_fallback(source_arg, ref_arg, cache_dir)
                plan = _rematerialize_plan_content(repo_root, plan, cache_dir)
            else:
                pin = resolve_pin_with_cache_fallback(args.source, args.ref, cache_dir)
                # SEC-2 (T-X2): apply seals a cross-verified cache entry at
                # load time so later offline loads can verify it.
                bundle = load_bundle(pin, cache_dir, seal_cache=True)
                if components:
                    bundle = _filter_bundle(bundle, components)
                lock = read_lock(repo_root)
                plan = plan_updates(
                    repo_root,
                    bundle,
                    lock,
                    protect_patterns=user_protects,
                    adopt_existing=bool(getattr(args, "adopt_existing", False)),
                )
                sys.stderr.write(
                    "[RE-PLANNED] no --plan-file provided; a freshly computed "
                    "plan was treated as approved\n"
                )
            result = apply_updates(
                repo_root,
                plan,
                args.actor,
                args.installation,
                resolved_pin=pin,
                plan_file=plan_file,
            )
        except UpdateEngineError as exc:
            if isinstance(exc, EngineBusyError):
                # SEC-4 (T-X3): a concurrent engine operation holds the
                # repository lock.
                sys.stderr.write(_error_envelope(
                    "ENGINE_BUSY",
                    str(repo_root),
                    "another engine operation (apply or restore) is already "
                    "running on this repository",
                    "exclusive acquisition of .agents/update-engine.lock",
                    str(exc),
                    "wait for the running apply/restore to finish, then retry",
                ) + "\n")
                sys.stderr.write(f"[ENGINE-BUSY] {exc}\n")
                return 1
            sys.stderr.write(_error_envelope(
                "APPLY_REFUSED",
                str(plan_file) if plan_file else str(repo_root),
                "apply refused with zero writes (plan perimeter or "
                "preconditions violated)",
                "an admitted actor applying a serialized, pin-matching, "
                "conflict-free plan over the pinned base",
                str(exc),
                "re-run scripts/update_meta_project.py check/plan, resolve "
                "the refusal cause above, and apply the regenerated plan",
            ) + "\n")
            sys.stderr.write(f"[APPLY-REFUSED] {exc}\n")
            return 1
        except (OSError, ValueError) as exc:
            sys.stderr.write(_error_envelope(
                "APPLY_FAILED",
                str(plan_file) if plan_file else str(repo_root),
                "apply failed before completing (journal preserves the "
                "recovery state)",
                "every plan action applied atomically over writable targets",
                str(exc),
                "inspect the journal (.agents/update_journal.json) and use "
                "'restore' to return to the pre-apply state, then re-apply",
            ) + "\n")
            sys.stderr.write(f"[APPLY-FAILED] {exc}\n")
            return 1
        print(json.dumps(result, indent=2, default=str))
        return 0

    if args.subcommand == "restore":
        try:
            result = restore_updates(repo_root)
        except EngineBusyError as exc:
            # SEC-4 (T-X3): a concurrent engine operation holds the lock.
            sys.stderr.write(_error_envelope(
                "ENGINE_BUSY",
                str(repo_root),
                "another engine operation (apply or restore) is already "
                "running on this repository",
                "exclusive acquisition of .agents/update-engine.lock",
                str(exc),
                "wait for the running apply/restore to finish, then retry",
            ) + "\n")
            sys.stderr.write(f"[ENGINE-BUSY] {exc}\n")
            return 1
        except (UpdateEngineError, OSError, ValueError) as exc:
            sys.stderr.write(f"[RESTORE-FAILED] {exc}\n")
            return 1
        print(json.dumps(result, indent=2, default=str))
        return 0

    parser.error(f"unknown subcommand: {args.subcommand}")
    return 2


if __name__ == "__main__":
    sys.exit(main())
