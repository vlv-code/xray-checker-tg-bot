#!/usr/bin/env python3
"""
Record Observation CLI Utility.
Zero-dependency utility (pure Python 3.10+ standard library).
Appends standardized, validated observation rows to observations.md
with cross-platform file locking (msvcrt on Windows, fcntl on Unix).
"""

from __future__ import annotations

import argparse
import contextlib
import datetime
import os
from pathlib import Path
import re
import sys
from typing import Any, List, Optional, Set, Union

CANONICAL_CATEGORIES: Set[str] = {
    "boundaries",
    "ci",
    "decommission",
    "incident",
    "orchestration",
    "runtime",
    "git",
    "init",
    "testing",
    "architecture",
}

DEFAULT_OBSERVATIONS_PATH = Path(".agents/project_knowledge/observations.md")

DEFAULT_HEADER = """# Agent Observations Base

Cumulative registry of findings, non-obvious environment quirks, and practical solutions.
Entries are logged strictly via the `record-observation` skill in a concise table format (1 line per fact).

---

| Date | Category | Component / Path | Observation / Issue | Solution / Workaround |
| :--- | :--- | :--- | :--- | :--- |
"""


def normalize_category(category: str) -> str:
    """Strip whitespace and brackets to extract inner category name."""
    cleaned = category.strip()
    if cleaned.startswith("[") and cleaned.endswith("]"):
        cleaned = cleaned[1:-1].strip()
    return cleaned.lower()


def escape_pipes(text: str) -> str:
    r"""Escapes raw unescaped pipes ('|' -> '\|') and replaces newlines with spaces."""
    cleaned = text.replace("\r", " ").replace("\n", " ").strip()
    # Replace unescaped pipe | preceded by anything other than backslash
    return re.sub(r"(?<!\\)\|", r"\|", cleaned)


@contextlib.contextmanager
def file_lock(target: Union[str, os.PathLike[str], Any], blocking: bool = True):
    """
    Cross-platform file locking context manager (Windows msvcrt / Unix fcntl).
    Accepts an open file object or a Path/str path.
    """
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


def format_row(
    date_str: str,
    category: str,
    path: str,
    issue: str,
    solution: str,
    pin: bool = False,
    actor_id: Optional[str] = None,
) -> str:
    """Formats a single Markdown table row for observations.md."""
    norm_cat = normalize_category(category)
    if norm_cat not in CANONICAL_CATEGORIES:
        allowed = ", ".join(f"[{c}]" for c in sorted(CANONICAL_CATEGORIES))
        raise ValueError(
            f"Invalid category '{category}'. Must be one of canonical categories: {allowed}"
        )

    cat_tag = f"[{norm_cat}][pin]" if pin else f"[{norm_cat}]"
    clean_path = escape_pipes(path)
    clean_issue = escape_pipes(issue)
    clean_solution = escape_pipes(solution)

    if actor_id:
        clean_issue = f"[actor={actor_id.strip()}] {clean_issue}"

    return f"| {date_str} | {cat_tag} | {clean_path} | {clean_issue} | {clean_solution} |"


def _insert_row_into_table(content: str, row: str) -> str:
    """
    Insert a data row INSIDE the observations table — directly after its last
    data row and strictly before any subsequent text (audit §9: rows appended
    past trailing sections/NOTEs fall outside the table and become invisible
    to the compactor). When no canonical table exists, the header is created.
    """
    lines = content.splitlines()
    sep_idx = None
    for idx, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("|") and re.match(r"^\|\s*:?-+", stripped):
            sep_idx = idx
            break

    if sep_idx is None:
        return DEFAULT_HEADER + row + "\n"

    # Find the end of the contiguous table region (blank line followed by a
    # table row is treated as part of the table, mirroring the compactor).
    end = sep_idx + 1
    idx = sep_idx + 1
    while idx < len(lines):
        stripped = lines[idx].strip()
        if stripped.startswith("|"):
            end = idx + 1
            idx += 1
        elif stripped == "" and idx + 1 < len(lines) and lines[idx + 1].strip().startswith("|"):
            idx += 1
        else:
            break

    new_lines = lines[:end] + [row] + lines[end:]
    return "\n".join(new_lines).rstrip() + "\n"


def _atomic_write_text(path: Path, text: str) -> None:
    """Write text via temp file + atomic replace (audit §9: crash-safe)."""
    import tempfile as _tempfile

    fd, tmp_name = _tempfile.mkstemp(
        prefix=f".{path.name}.", suffix=".tmp", dir=str(path.parent)
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as tmp_file:
            tmp_file.write(text)
        os.replace(tmp_name, path)
    except Exception:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


def record_observation(
    category: str,
    path: str,
    issue: str,
    solution: str,
    pin: bool = False,
    obs_file: Path = DEFAULT_OBSERVATIONS_PATH,
    date_str: Optional[str] = None,
    actor_id: Optional[str] = None,
) -> str:
    """
    Validates, formats, and atomically inserts an observation row into the
    obs_file table under a cross-platform file lock.

    Documented duplicate suppression (audit §9): recording the exact same
    event (same date, category, path, issue, solution, pin) is a no-op — the
    identical row is returned without duplicating the entry. Distinct
    incidents are never merged.
    Returns the table row (inserted or already present).
    """
    target_date = date_str or datetime.date.today().isoformat()
    try:
        datetime.date.fromisoformat(target_date)
    except (ValueError, TypeError) as exc:
        raise ValueError(
            f"Invalid date format '{target_date}'. Expected ISO format 'YYYY-MM-DD'."
        ) from exc

    row = format_row(
        date_str=target_date,
        category=category,
        path=path,
        issue=issue,
        solution=solution,
        pin=pin,
        actor_id=actor_id,
    )

    obs_path = Path(obs_file)
    obs_path.parent.mkdir(parents=True, exist_ok=True)

    # Use a lock file adjacent to the observations file
    lock_path = obs_path.with_name(f"{obs_path.name}.lock")

    with file_lock(lock_path, blocking=True):
        if not obs_path.exists() or obs_path.stat().st_size == 0:
            _atomic_write_text(obs_path, DEFAULT_HEADER + row + "\n")
        else:
            existing_content = obs_path.read_text(encoding="utf-8")
            # Exact-duplicate suppression: identical row already recorded.
            if any(
                existing_line.strip() == row
                for existing_line in existing_content.splitlines()
            ):
                return row
            updated_content = _insert_row_into_table(existing_content, row)
            _atomic_write_text(obs_path, updated_content)

    return row


def main(argv: Optional[List[str]] = None) -> int:
    """CLI entrypoint for recording observations."""
    parser = argparse.ArgumentParser(
        description="Append a standardized observation row to observations.md with category validation and file locking."
    )
    parser.add_argument(
        "--category",
        required=True,
        help="Category of the observation (e.g. runtime, boundaries, ci, etc.)",
    )
    parser.add_argument(
        "--path",
        required=True,
        help="Component or file path where observation occurred",
    )
    parser.add_argument(
        "--issue",
        required=True,
        help="Observation or issue summary",
    )
    parser.add_argument(
        "--solution",
        required=True,
        help="Workaround or solution summary",
    )
    parser.add_argument(
        "--pin",
        action="store_true",
        help="Pin observation with [pin] tag to protect from compaction",
    )
    parser.add_argument(
        "--obs-file",
        default=str(DEFAULT_OBSERVATIONS_PATH),
        help="Path to observations.md markdown file",
    )
    parser.add_argument(
        "--date",
        default=None,
        help="Custom ISO date (YYYY-MM-DD), default is current local date",
    )
    parser.add_argument(
        "--actor-id",
        default=None,
        help="Qualified actor identifier (e.g. project::harness::installation::role) for action attribution",
    )

    try:
        args = parser.parse_args(argv)
    except SystemExit as e:
        return e.code if isinstance(e.code, int) else 1

    try:
        appended_row = record_observation(
            category=args.category,
            path=args.path,
            issue=args.issue,
            solution=args.solution,
            pin=args.pin,
            obs_file=Path(args.obs_file),
            date_str=args.date,
            actor_id=args.actor_id,
        )
        print(f"[OK] Recorded observation: {appended_row}")
        return 0
    except ValueError as e:
        print(f"[ERROR] Validation failed: {e}", file=sys.stderr)
        return 1
    except Exception as e:
        print(f"[ERROR] Failed to record observation: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
