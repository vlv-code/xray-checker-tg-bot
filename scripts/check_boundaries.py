#!/usr/bin/env python3
"""
Boundary Gate CLI - Automated Default Deny & Zone Policy Enforcement.
Zero-dependency utility (pure Python standard library).
Validates modified or specified files against repository boundaries.md.
"""

from __future__ import annotations

import argparse
from pathlib import Path
import re
import subprocess
import sys
from typing import Dict, List, Optional, Set, Tuple


class BoundaryGateError(RuntimeError):
    """Raised when a git query fails; the gate must fail closed, never
    silently degrade into an empty 'Clean' result (audit §2)."""


def glob_to_regex(pat: str) -> re.Pattern:
    """
    Compile a file path glob pattern into a regex pattern.
    Supports '**' (arbitrary directory depth), '*' (single directory segment), and '?' (single char).
    Supports zero-depth directory matching for '/**/' (e.g. src/**/test.py matches src/test.py).
    """
    pat = pat.strip().replace("\\", "/")
    if pat.startswith("./"):
        pat = pat[2:]
    if pat.startswith("/"):
        pat = pat[1:]

    # Remove trailing comments or annotations in parentheses if present
    pat = re.sub(r"\s*\(.*?\)$", "", pat).strip()

    TOKEN_ZERO_DEPTH = "__ZERO_DEPTH_DIR__"
    pat_normalized = pat.replace("/**/", TOKEN_ZERO_DEPTH)

    parts = pat_normalized.split("**")
    sub_res = []
    for part in parts:
        escaped = re.escape(part)
        escaped = escaped.replace(TOKEN_ZERO_DEPTH, r"(?:/.+/|/)")
        escaped = escaped.replace(r"\*", r"[^/]*").replace(r"\?", r"[^/]")
        sub_res.append(escaped)

    regex = r".*".join(sub_res)
    if "/" not in pat:
        regex = rf"(^|.*/){regex}$"
    else:
        regex = rf"^{regex}$"

    return re.compile(regex, re.IGNORECASE)


def parse_boundaries(content: str) -> Dict[str, List[str]]:
    """
    Extract glob patterns for Green, Yellow, and Red zones from markdown text.
    Handles headers in English and Russian.
    Strips parenthesized exception phrases like (except ...) before extraction.
    """
    zones: Dict[str, List[str]] = {"green": [], "yellow": [], "red": []}
    current_zone: Optional[str] = None

    for line in content.splitlines():
        line_str = line.strip()

        # Zone header detection
        if re.search(r"##\s*(?:Green\s*Zone|Зелен(?:ая|ое|ые)\s*зон(?:а|ы))", line_str, re.IGNORECASE):
            current_zone = "green"
            continue
        elif re.search(r"##\s*(?:Yellow\s*Zone|Желт(?:ая|ое|ые)\s*зон(?:а|ы))", line_str, re.IGNORECASE):
            current_zone = "yellow"
            continue
        elif re.search(r"##\s*(?:Red\s*Zone|Красн(?:ая|ое|ые)\s*зон(?:а|ы))", line_str, re.IGNORECASE):
            current_zone = "red"
            continue
        elif line_str.startswith("## ") and current_zone:
            # Another section started (e.g. Default Deny)
            current_zone = None
            continue

        if current_zone and line_str.startswith("-"):
            # Strip parenthesized exclusion / exception clauses before candidate extraction
            # e.g. "docs/**, *.md (except the root AGENTS.md)" -> "docs/**, *.md "
            cleaned_line = re.sub(
                r"\(\s*(?:except|кроме|excluding|but not)\b[^)]*\)", "", line_str, flags=re.IGNORECASE
            )
            # Extract backticked candidates
            matches = re.findall(r"`([^`]+)`", cleaned_line)
            for m in matches:
                candidate = m.strip()
                # Exclude command strings and assert/expect keywords
                if any(kw in candidate for kw in ["python ", "pytest", "assert", "expect", "git "]):
                    continue
                # Include paths/globs with slashes, wildcards, extensions, or standalone filenames (e.g. LICENSE)
                if "/" in candidate or "*" in candidate or "." in candidate or " " not in candidate:
                    clean_pat = re.sub(r"\s*\(.*?\)$", "", candidate).strip()
                    if clean_pat and clean_pat not in zones[current_zone]:
                        zones[current_zone].append(clean_pat)


    return zones


def classify_path(file_path: str, zones: Dict[str, List[str]]) -> Tuple[str, str]:
    """
    Classify a normalized relative file path into a zone:
    Precedence:
      1. Red Zone patterns always win (strict security boundary).
      2. AGENTS.md protection invariant: cannot be Green; if not explicitly in Red, returns Yellow.
      3. Specific Green patterns (e.g. tests/test_*.py) take precedence over broad Yellow directory
         globs (e.g. tests/**) when targeting the same directory prefix.
      4. Yellow Zone patterns.
      5. Remaining Green Zone patterns.
      6. Default Deny (Black Zone).
    Returns (zone_name, reason).
    """
    norm_path = file_path.strip().replace("\\", "/")
    if norm_path.startswith("./"):
        norm_path = norm_path[2:]

    # 1. Check Red Zone
    for pat in zones.get("red", []):
        if glob_to_regex(pat).match(norm_path):
            return "red", f"Matches Red Zone pattern '{pat}'"

    # 2. Invariant: root AGENTS.md is strictly protected from Green Zone
    if norm_path == "AGENTS.md":
        for pat in zones.get("yellow", []):
            if glob_to_regex(pat).match(norm_path):
                return "yellow", f"Matches Yellow Zone pattern '{pat}'"
        return "yellow", "Protected project passport (AGENTS.md is prohibited from Green Zone)"

    def is_broad_dir_pattern(pat: str) -> bool:
        clean = pat.strip().replace("\\", "/")
        return clean.endswith("/**") or clean.endswith("/*")

    def get_dir_prefix(pat: str) -> str:
        clean = pat.strip().replace("\\", "/")
        if "/" in clean:
            return clean.split("/")[0]
        return ""

    # 3. Check specific Green patterns (e.g. tests/test_*.py) before broad Yellow dir globs (e.g. tests/**)
    # This applies when a specific green pattern explicitly targets a directory covered broadly by Yellow.
    for pat in zones.get("green", []):
        if not is_broad_dir_pattern(pat) and glob_to_regex(pat).match(norm_path):
            g_prefix = get_dir_prefix(pat)
            if g_prefix:
                # Check if Yellow has a specific file match for this path
                yellow_specific_match = None
                for y_pat in zones.get("yellow", []):
                    if not is_broad_dir_pattern(y_pat) and glob_to_regex(y_pat).match(norm_path):
                        yellow_specific_match = y_pat
                        break
                if yellow_specific_match:
                    return "yellow", f"Matches Yellow Zone pattern '{yellow_specific_match}'"
                return "green", f"Matches Green Zone pattern '{pat}'"

    # 4. Check Yellow Zone
    for pat in zones.get("yellow", []):
        if glob_to_regex(pat).match(norm_path):
            return "yellow", f"Matches Yellow Zone pattern '{pat}'"

    # 5. Check remaining Green Zone patterns
    for pat in zones.get("green", []):
        if glob_to_regex(pat).match(norm_path):
            return "green", f"Matches Green Zone pattern '{pat}'"

    # 6. Default Deny (Black Zone)
    return "black", f"Path '{norm_path}' is not mapped to any zone (Default Deny)"




def evaluate_files(
    files: List[str], zones: Dict[str, List[str]], allow_red_zone: bool = False
) -> Tuple[bool, List[Dict[str, str]]]:
    """
    Evaluate a list of files against zone policies.
    Returns (passed, list_of_violations).
    """
    violations: List[Dict[str, str]] = []

    for f in files:
        if not f.strip():
            continue
        zone, reason = classify_path(f, zones)
        if zone == "black":
            violations.append({"file": f, "zone": "black", "reason": reason})
        elif zone == "red" and not allow_red_zone:
            violations.append({"file": f, "zone": "red", "reason": reason})

    passed = len(violations) == 0
    return passed, violations


def audit_tree(
    repo_root: Path, zones: Dict[str, List[str]]
) -> Tuple[bool, List[str], Dict[str, int]]:
    """
    Audit all tracked and untracked non-ignored files across the repository tree.
    Returns (passed, unmapped_files, zone_counts).
    """
    repo_root = Path(repo_root).resolve()
    tracked_files: List[str] = []

    try:
        res = subprocess.run(
            ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
            cwd=str(repo_root),
            capture_output=True,
            check=False,
        )
        if res.returncode == 0 and res.stdout:
            raw_entries = res.stdout.split(b"\0")
            tracked_files = [
                entry.decode("utf-8", errors="replace").replace("\\", "/")
                for entry in raw_entries
                if entry
            ]
    except Exception:
        tracked_files = []

    if not tracked_files:
        # Fallback to filesystem walk
        ignored_names = {".git", "__pycache__", ".pytest_cache", ".venv", "venv", "node_modules"}
        for p in repo_root.rglob("*"):
            if p.is_file():
                rel_parts = p.relative_to(repo_root).parts
                if any(part in ignored_names for part in rel_parts):
                    continue
                tracked_files.append(str(p.relative_to(repo_root)).replace("\\", "/"))

    unmapped: List[str] = []
    counts: Dict[str, int] = {"green": 0, "yellow": 0, "red": 0, "black": 0}

    for f in tracked_files:
        zone, _ = classify_path(f, zones)
        counts[zone] = counts.get(zone, 0) + 1
        if zone == "black":
            unmapped.append(f)

    passed = len(unmapped) == 0
    return passed, unmapped, counts


def check_architecture_drift(files: List[str]) -> Tuple[bool, List[str]]:
    """
    Check if functional code files were modified without a corresponding update to architecture.md.
    Returns (has_drift, list_of_functional_code_files).

    Suppressed if:
    - No functional code files were touched.
    - An architecture slice ('architecture.md' or '.agents/project_knowledge/architecture.md') is among files.
    """
    normalized_files = [f.strip().replace("\\", "/") for f in files if f.strip()]
    if not normalized_files:
        return False, []

    # Check if architecture file is among the modified files
    has_arch_update = any(
        f == "architecture.md" or f.endswith("/architecture.md")
        for f in normalized_files
    )
    if has_arch_update:
        return False, []

    # Noise suppression: non-code directories and files to ignore
    ignored_prefixes = (
        "tests/", "test/", "docs/", ".agents/", ".github/", ".idea/", ".vscode/"
    )
    ignored_extensions = (
        ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".ini", ".log", ".lock",
        ".svg", ".png", ".jpg", ".jpeg", ".ico", ".css"
    )
    code_extensions = (
        ".py", ".ts", ".js", ".tsx", ".jsx", ".go", ".rs", ".java", ".c", ".cpp",
        ".h", ".hpp", ".cs", ".kt", ".swift", ".rb", ".php"
    )

    code_files: List[str] = []
    for f in normalized_files:
        is_ignored_dir = any(f.startswith(p) for p in ignored_prefixes)
        # Exception: skills/ and scripts/ represent functional framework code
        is_framework_code = f.startswith("skills/") or f.startswith("scripts/")

        if is_ignored_dir and not is_framework_code:
            continue

        ext = Path(f).suffix.lower()
        if ext in code_extensions or is_framework_code or f.startswith("src/") or f.startswith("app/") or f.startswith("lib/"):
            if not f.endswith(ignored_extensions) or is_framework_code:
                code_files.append(f)

    has_drift = len(code_files) > 0
    return has_drift, code_files


def get_git_modified_files(
    diff_target: Optional[str] = None,
    staged_only: bool = False,
    repo_root: Optional[Path] = None,
) -> List[str]:
    """
    Extract list of changed files from git using NUL-separated output.

    Fail-closed contract (audit §2):
    - Renames are broken into BOTH old and new paths via --no-renames, so a
      Red Zone file renamed into a Green Zone location is still checked.
    - An explicit --diff-target / --staged query that fails (unresolvable
      revision, git error) raises BoundaryGateError instead of silently
      falling back to a possibly-empty working-tree status.
    - The default mode (no target, no staging filter) inspects uncommitted
      working-tree state via `git status --porcelain -z`, preserving the
      original paths of renames (bare NUL record after an R/C entry).
      A failing status query (e.g. not a git repository) also raises
      BoundaryGateError — never a successful empty 'Clean'.
    - All git invocations run inside repo_root when provided.
    """
    cwd = str(repo_root) if repo_root is not None else None

    if diff_target or staged_only:
        cmd = ["git", "diff", "-z", "--name-only", "--no-renames"]
        if staged_only:
            cmd.append("--cached")
        if diff_target:
            cmd.append(diff_target)
        res = subprocess.run(cmd, capture_output=True, check=False, cwd=cwd)
        if res.returncode != 0:
            stderr_text = res.stderr.decode("utf-8", errors="replace").strip()
            raise BoundaryGateError(
                f"git diff failed (revision "
                f"'{diff_target or '--cached'}' unavailable or git error); "
                f"failing closed: {stderr_text}"
            )
        return [
            item.decode("utf-8", errors="replace").replace("\\", "/")
            for item in res.stdout.split(b"\0")
            if item
        ]

    # Default mode: uncommitted working-tree changes (staged + unstaged +
    # untracked) with rename old-paths preserved.
    res = subprocess.run(
        ["git", "status", "--porcelain", "-z"],
        capture_output=True,
        check=False,
        cwd=cwd,
    )
    if res.returncode != 0:
        stderr_text = res.stderr.decode("utf-8", errors="replace").strip()
        raise BoundaryGateError(
            f"git status failed (not a git repository or git error); "
            f"failing closed: {stderr_text}"
        )

    files: List[str] = []
    expects_old_path = False
    for entry in res.stdout.split(b"\0"):
        if not entry:
            continue
        text = entry.decode("utf-8", errors="replace")
        if expects_old_path:
            # Bare NUL record following an R/C entry: original path.
            files.append(text.replace("\\", "/"))
            expects_old_path = False
        elif len(text) > 3 and text[2] == " ":
            files.append(text[3:].replace("\\", "/"))
            if text[0] in ("R", "C"):
                expects_old_path = True
    return files


EXTERNAL_HOOK_MANAGER_MARKERS = ("husky", "lefthook", "pre-commit.com")

DISPATCHER_TEMPLATE = """#!/usr/bin/env sh
# Meta-Project Boundary Gate Pre-Commit Hook (dispatcher)
# Runs any preserved legacy hook as a SEPARATE process (its exit 0 must not
# terminate this dispatcher; its non-zero exit preserves the denial), then
# enforces: check_boundaries.py --staged
LEGACY_HOOK="$(dirname -- "$0")/pre-commit.legacy-meta"
if [ -f "$LEGACY_HOOK" ]; then
  if [ -x "$LEGACY_HOOK" ]; then
    "$LEGACY_HOOK"
  else
    sh "$LEGACY_HOOK"
  fi
  legacy_rc=$?
  if [ "$legacy_rc" -ne 0 ]; then
    exit "$legacy_rc"
  fi
fi
ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [ -f "$ROOT/scripts/check_boundaries.py" ]; then
  python "$ROOT/scripts/check_boundaries.py" --staged
  exit $?
elif [ -f "$ROOT/.agents/scripts/check_boundaries.py" ]; then
  # Legacy pre-relocation deployment of the boundary gate.
  python "$ROOT/.agents/scripts/check_boundaries.py" --staged
  exit $?
fi
exit 0
"""


def _detect_external_manager(hook_content: str) -> Optional[str]:
    """Returns the name of an external hook manager detected in an existing
    hook, or None (audit §8: conflicts must be reported explicitly)."""
    lowered = hook_content.lower()
    for marker in EXTERNAL_HOOK_MANAGER_MARKERS:
        if marker in lowered:
            return marker
    return None


def _resolve_hooks_dir(repo_root: Path) -> Tuple[Optional[Path], Optional[str]]:
    """
    Resolves the effective git hooks directory for repo_root, honoring
    core.hooksPath and linked worktrees (audit §8). Returns (hooks_dir, err).
    """
    repo_root = Path(repo_root).resolve()

    # Explicit core.hooksPath configuration wins.
    try:
        res = subprocess.run(
            ["git", "config", "core.hooksPath"],
            cwd=str(repo_root),
            capture_output=True,
            text=True,
            check=False,
        )
        if res.returncode == 0 and res.stdout.strip():
            configured = Path(res.stdout.strip())
            hooks_dir = configured if configured.is_absolute() else repo_root / configured
            return hooks_dir, None
    except Exception:
        pass

    dot_git = repo_root / ".git"
    if dot_git.is_file():
        # Linked worktree: .git file contains 'gitdir: <main>/.git/worktrees/<n>'
        try:
            content = dot_git.read_text(encoding="utf-8").strip()
            gitdir = Path(content.split("gitdir:", 1)[1].strip())
            if not gitdir.is_absolute():
                gitdir = (repo_root / gitdir).resolve()
            common_dir = gitdir.parent.parent  # <main>/.git
            if common_dir.name == ".git" and common_dir.is_dir():
                return common_dir / "hooks", None
            return gitdir / "hooks", None
        except Exception as exc:
            return None, f"Failed to resolve worktree git dir: {exc}"
    if dot_git.is_dir():
        return dot_git / "hooks", None
    return None, f"Not a git repository: '{dot_git}' does not exist"


def install_git_hook(repo_root: Path) -> Tuple[bool, str]:
    """
    Installs a dispatcher pre-commit hook that:
    - preserves an existing hook as 'pre-commit.legacy-meta' and runs it as a
      separate process with its own interpreter (exit 0 does not stop the
      gate; non-zero preserves the denial);
    - then enforces `python <repo>/scripts/check_boundaries.py --staged`;
    - supports linked worktrees and core.hooksPath;
    - refuses with an explicit conflict message when an external hook manager
      (husky/lefthook/pre-commit) owns the existing hook.
    Idempotent: reinstalling over the dispatcher is a no-op.
    Returns (success, message).
    """
    repo_root = Path(repo_root).resolve()
    hooks_dir, err = _resolve_hooks_dir(repo_root)
    if hooks_dir is None:
        return False, err

    hooks_dir.mkdir(parents=True, exist_ok=True)
    hook_file = hooks_dir / "pre-commit"
    legacy_file = hooks_dir / "pre-commit.legacy-meta"

    if hook_file.is_file():
        existing_content = hook_file.read_text(encoding="utf-8")
        if "Meta-Project Boundary Gate Pre-Commit Hook" in existing_content:
            return True, (
                f"Pre-commit hook installed successfully (already configured) at {hook_file}"
            )
        manager = _detect_external_manager(existing_content)
        if manager:
            return False, (
                f"Hook conflict: existing pre-commit at {hook_file} appears to be "
                f"managed by an external hook manager ('{manager}'). Refusing to "
                f"take ownership; integrate the boundary gate manually."
            )
        if legacy_file.exists():
            return False, (
                f"Refusing to install: legacy companion {legacy_file} already "
                f"exists; resolve it manually to avoid losing preserved hook content."
            )
        # Non-destructive preservation: move the existing hook aside, byte-for-byte.
        hook_file.replace(legacy_file)
        try:
            legacy_file.chmod(legacy_file.stat().st_mode | 0o111)
        except Exception:
            pass

    hook_file.write_text(DISPATCHER_TEMPLATE, encoding="utf-8")
    try:
        hook_file.chmod(hook_file.stat().st_mode | 0o111)
    except Exception:
        pass

    preserved = (
        f" (preserved existing hook as {legacy_file.name})"
        if legacy_file.is_file()
        else ""
    )
    return True, f"Pre-commit hook installed successfully at {hook_file}{preserved}"


def main(argv: Optional[List[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        description="Boundary Gate CLI: Enforce Default Deny & Repository Change Boundaries"
    )
    parser.add_argument(
        "--boundaries",
        type=str,
        default=None,
        help="Path to boundaries.md (default: .agents/project_knowledge/boundaries.md or templates/)",
    )
    parser.add_argument(
        "--files",
        nargs="*",
        default=None,
        help="Explicit list of files to check instead of inspecting git diff",
    )
    parser.add_argument(
        "--diff-target",
        "--diff",
        dest="diff_target",
        type=str,
        default=None,
        help="Git diff comparison target (e.g. HEAD~1, origin/main, commit SHA)",
    )
    parser.add_argument(
        "--staged",
        action="store_true",
        help="Check only staged git files (--cached)",
    )
    parser.add_argument(
        "--allow-red-zone",
        action="store_true",
        help="Allow Red Zone modifications with a warning instead of failing",
    )
    parser.add_argument(
        "--audit-tree",
        action="store_true",
        help="Audit the entire repository tree for 100%% boundary mapping coverage",
    )
    parser.add_argument(
        "--install-hook",
        action="store_true",
        help="Install local git pre-commit hook to automatically verify staged changes",
    )
    parser.add_argument(
        "--repo-root",
        type=str,
        default=None,
        help="Root directory of the repository (default: current working directory)",
    )

    args = parser.parse_args(argv)

    # Authoritative repository root for all modes and git operations (audit §2:
    # --repo-root must define the repository for every git invocation).
    repo_root = Path(args.repo_root).resolve() if args.repo_root else Path.cwd()

    # Mode 0: Install pre-commit hook
    if args.install_hook:
        ok, msg = install_git_hook(repo_root)
        if ok:
            sys.stdout.write(f"[BOUNDARY GATE] {msg}\n")
            return 0
        else:
            sys.stderr.write(f"[BOUNDARY GATE ERROR] {msg}\n")
            return 1


    # Locate boundaries.md
    boundaries_path: Optional[Path] = None
    if args.boundaries:
        boundaries_path = Path(args.boundaries)
    else:
        for candidate in [
            repo_root / ".agents" / "project_knowledge" / "boundaries.md",
            repo_root / "templates" / "project_knowledge" / "boundaries.md",
        ]:
            if candidate.is_file():
                boundaries_path = candidate
                break

    if not boundaries_path or not boundaries_path.is_file():
        sys.stderr.write(f"Error: boundaries.md file not found at '{boundaries_path}'\n")
        return 1

    content = boundaries_path.read_text(encoding="utf-8")
    zones = parse_boundaries(content)

    # Mode 1: Audit entire tree
    if args.audit_tree:
        repo_root = Path(args.repo_root).resolve() if args.repo_root else (
            boundaries_path.parent.parent.parent if boundaries_path.parent.name == "project_knowledge" else Path.cwd()
        )
        passed, unmapped, counts = audit_tree(repo_root, zones)
        total = sum(counts.values())
        if passed:
            sys.stdout.write(
                f"[BOUNDARY GATE] Audit passed: 100% boundary coverage across repository.\n"
                f"Total files audited: {total} (Green: {counts['green']}, Yellow: {counts['yellow']}, Red: {counts['red']}).\n"
            )
            return 0
        else:
            sys.stderr.write(
                f"\n[BOUNDARY GATE ERROR] UNMAPPED PATHS DETECTED (Default Deny violation)!\n"
                f"The following {len(unmapped)} of {total} file(s) are not covered by boundaries.md:\n"
            )
            for f in unmapped:
                sys.stderr.write(f"  [X] {f}\n")
            sys.stderr.write("\nRemedy: Update boundaries.md to assign these paths to Green, Yellow, or Red zone.\n")
            return 1

    # Mode 2: Check modified / explicit files
    if args.files is not None and len(args.files) > 0:
        files = args.files

    else:
        try:
            files = get_git_modified_files(
                diff_target=args.diff_target,
                staged_only=args.staged,
                repo_root=repo_root,
            )
        except BoundaryGateError as exc:
            sys.stderr.write(f"[BOUNDARY GATE ERROR] {exc}\n")
            return 1

    if not files:
        sys.stdout.write("Boundary Gate: No modified files detected. Clean.\n")
        return 0

    passed, violations = evaluate_files(files, zones, allow_red_zone=args.allow_red_zone)

    has_drift, drift_files = check_architecture_drift(files)
    if has_drift:
        sys.stdout.write(
            "\n[!] [ARCHITECTURE DRIFT WARNING]: Functional code modified without updating architecture.md:\n"
        )
        for df in drift_files:
            sys.stdout.write(f"   * {df}\n")
        sys.stdout.write("   Suggested action: Invoke 'sync-architecture' if components, entrypoints, or data flows were modified.\n\n")

    if passed:
        sys.stdout.write(f"Boundary Gate: Checked {len(files)} files. All files conform to allowed boundaries.\n")
        return 0

    # Print violations report
    sys.stderr.write("\n===========================================================\n")
    sys.stderr.write("[FAILURE] BOUNDARY GATE FAILURE: Zone Violations Detected\n")
    sys.stderr.write("===========================================================\n\n")

    for v in violations:
        if v["zone"] == "black":
            sys.stderr.write(f"[X] [DEFAULT DENY / UNMAPPED] {v['file']}\n")
            sys.stderr.write(f"   Reason: {v['reason']}\n")
            sys.stderr.write("   Remedy: Perform Opportunistic Refresh of boundaries.md to assign this path to a zone.\n\n")
        elif v["zone"] == "red":
            sys.stderr.write(f"[!] [RED ZONE VIOLATION] {v['file']}\n")
            sys.stderr.write(f"   Reason: {v['reason']}\n")
            sys.stderr.write("   Remedy: Modifications to Red Zone require user confirmation or --allow-red-zone override.\n\n")

    sys.stderr.write(f"Total violations: {len(violations)} of {len(files)} files inspected.\n")
    return 1


if __name__ == "__main__":
    sys.exit(main())
