#!/usr/bin/env python3
"""
Template Injection & Skill Parity Synchronizer.
Zero-dependency utility (pure Python 3.10+ standard library).
Byte-copies canonical templates/skills into per-skill resources/ directories
(skills/init-meta-project/resources/ — Wave E Lane P2 lazy-load pilot;
skills/manage-meta-project/resources/ — Wave G Lane S manage-slim), and
mirrors canonical skills/ (SKILL.md plus resources/) into .agents/skills/.

Wave G Lane S supersession: the embedded TEMPLATE-block injection mechanism
(EMBEDDED_TARGETS / replace_embedded_template / sync_embedded_templates) is
removed — its last consumer (manage-meta-project) moved to resource
byte-copy semantics, so no embedded sections remain anywhere.
"""

from __future__ import annotations

import argparse
from pathlib import Path
import sys
from typing import Dict, List, Optional, Tuple

# Wave E Lane P2: init ships its templates/skills as sibling resource files
# (byte-copies of the canonical sources) instead of embedded TEMPLATE blocks.
# Canonical sources: templates/ for the knowledge slices + root passport;
# the skills themselves for the 3 shell-skill resources (hyphenated names).
INIT_RESOURCES_DIR = "skills/init-meta-project/resources"

RESOURCE_TARGETS: Dict[str, str] = {
    "observations.md": "templates/project_knowledge/observations.md",
    "boundaries.md": "templates/project_knowledge/boundaries.md",
    "weak_spots.md": "templates/project_knowledge/weak_spots.md",
    "architecture.md": "templates/project_knowledge/architecture.md",
    "tasks.md": "templates/project_knowledge/tasks.md",
    "AGENTS.md": "templates/AGENTS.md",
    "record-observation-SKILL.md": "skills/record-observation/SKILL.md",
    "sync-architecture-SKILL.md": "skills/sync-architecture/SKILL.md",
    "ground-meta-project-SKILL.md": "skills/ground-meta-project/SKILL.md",
}

# Wave G Lane S: manage ships its team templates the same way (init-P2
# pattern). Canonical sources stay in templates/team/ — the resource files
# are byte-copies, never independent edits.
MANAGE_RESOURCES_DIR = "skills/manage-meta-project/resources"

MANAGE_RESOURCE_TARGETS: Dict[str, str] = {
    "profile.md": "templates/team/profile.md",
    "notes.md": "templates/team/notes.md",
    "system_prompt.md": "templates/team/system_prompt.md",
}

# Per-skill resource sync registry: skill resources/ directory ->
# {resource file name -> canonical source (relative to repo root)}.
RESOURCE_SYNC_SETS: Dict[str, Dict[str, str]] = {
    INIT_RESOURCES_DIR: RESOURCE_TARGETS,
    MANAGE_RESOURCES_DIR: MANAGE_RESOURCE_TARGETS,
}


def sync_skill_resources(
    repo_root: Path, write: bool = False
) -> Tuple[bool, List[str]]:
    """
    Byte-copies canonical sources into per-skill resources/ directories per
    RESOURCE_SYNC_SETS (Wave E Lane P2 for init; Wave G Lane S for manage):
    the canonical knowledge/passport/team templates plus the 3 shell-skill
    SKILL.md files (canonical in skills/ themselves, deployed by verbatim
    file copy). Returns (in_sync, list_of_drift_messages).

    Missing CANONICAL sources are unrecoverable ("Missing canonical ..." —
    fatal on --write); a missing or diverged RESOURCE file is recoverable
    drift that --write repairs by byte copy.
    """
    drift_messages: List[str] = []

    for resources_rel, targets in RESOURCE_SYNC_SETS.items():
        resources_dir = repo_root / resources_rel
        for res_name, canon_rel in targets.items():
            canon_path = repo_root / canon_rel
            if not canon_path.is_file():
                drift_messages.append(
                    f"Missing canonical source file: {canon_rel} "
                    f"(required by resource {resources_rel}/{res_name})"
                )
                continue
            canon_bytes = canon_path.read_bytes()
            res_path = resources_dir / res_name
            if not res_path.is_file():
                drift_messages.append(
                    f"Drift: Missing resource file {resources_rel}/{res_name} "
                    f"(canonical: {canon_rel})"
                )
                if write:
                    resources_dir.mkdir(parents=True, exist_ok=True)
                    res_path.write_bytes(canon_bytes)
                continue
            if res_path.read_bytes() != canon_bytes:
                drift_messages.append(
                    f"Drift: Resource '{resources_rel}/{res_name}' diverges "
                    f"from {canon_rel}"
                )
                if write:
                    res_path.write_bytes(canon_bytes)

    in_sync = len(drift_messages) == 0
    return in_sync, drift_messages


def sync_skill_mirroring(
    repo_root: Path, write: bool = False
) -> Tuple[bool, List[str]]:
    """
    Ensures that every skill in skills/*/SKILL.md has an identical copy in .agents/skills/*/SKILL.md.
    Returns (in_sync, list_of_drift_messages).
    """
    drift_messages: List[str] = []
    skills_dir = repo_root / "skills"
    agent_skills_dir = repo_root / ".agents" / "skills"

    if not skills_dir.is_dir():
        return True, []

    # Wave G follow-up (distribution contract): mirror parity applies only
    # when a self-installation mirror exists. A distribution checkout (the
    # framework branch carries no .agents/) is a valid tree without mirrors.
    if not agent_skills_dir.is_dir():
        return True, []

    for skill_file in sorted(skills_dir.glob("*/SKILL.md")):
        skill_name = skill_file.parent.name
        mirror_file = agent_skills_dir / skill_name / "SKILL.md"

        # Resources ship WITH the skill (Wave E Lane P2 bootstrap invariant):
        # mirror skills/<name>/resources/* into .agents/skills/<name>/resources/*.
        # Checked regardless of the SKILL.md mirror state so a first-time
        # --write lands SKILL.md and resources/ in one pass.
        skill_resources_dir = skill_file.parent / "resources"
        if skill_resources_dir.is_dir():
            for res_file in sorted(skill_resources_dir.iterdir()):
                if not res_file.is_file():
                    continue
                mirror_res = agent_skills_dir / skill_name / "resources" / res_file.name
                res_bytes = res_file.read_bytes()
                if not mirror_res.is_file() or mirror_res.read_bytes() != res_bytes:
                    drift_messages.append(
                        f"Drift: .agents/skills/{skill_name}/resources/{res_file.name} "
                        f"is out of sync with skills/{skill_name}/resources/{res_file.name}"
                    )
                    if write:
                        mirror_res.parent.mkdir(parents=True, exist_ok=True)
                        mirror_res.write_bytes(res_bytes)

        if not mirror_file.is_file():
            drift_messages.append(
                f"Drift: Missing mirrored skill at {mirror_file.relative_to(repo_root)}"
            )
            if write:
                mirror_file.parent.mkdir(parents=True, exist_ok=True)
                mirror_file.write_text(skill_file.read_text(encoding="utf-8"), encoding="utf-8")
            continue

        canonical_text = skill_file.read_text(encoding="utf-8")
        mirrored_text = mirror_file.read_text(encoding="utf-8")

        if canonical_text != mirrored_text:
            drift_messages.append(
                f"Drift: .agents/skills/{skill_name}/SKILL.md is out of sync with skills/{skill_name}/SKILL.md"
            )
            if write:
                mirror_file.write_text(canonical_text, encoding="utf-8")

    in_sync = len(drift_messages) == 0
    return in_sync, drift_messages


def sync_global_skills(
    repo_root: Path, global_dir: Path, write: bool = False
) -> Tuple[bool, List[str]]:
    """
    Ensures that every canonical skill in skills/*/SKILL.md has an identical copy in global_dir/*/SKILL.md.
    Returns (in_sync, list_of_drift_messages).
    """
    drift_messages: List[str] = []
    skills_dir = repo_root / "skills"

    if not skills_dir.is_dir() or not global_dir.is_dir():
        return True, []

    for skill_file in sorted(skills_dir.glob("*/SKILL.md")):
        skill_name = skill_file.parent.name
        target_skill_dir = global_dir / skill_name
        target_file = target_skill_dir / "SKILL.md"

        if not target_file.is_file():
            drift_messages.append(
                f"Global Drift: Missing global skill at {target_file}"
            )
            if write:
                target_skill_dir.mkdir(parents=True, exist_ok=True)
                target_file.write_text(
                    skill_file.read_text(encoding="utf-8"), encoding="utf-8"
                )
            continue

        canonical_text = skill_file.read_text(encoding="utf-8")
        current_text = target_file.read_text(encoding="utf-8")

        if canonical_text != current_text:
            drift_messages.append(
                f"Global Drift: Global skill '{skill_name}' diverges from canonical skills/{skill_name}/SKILL.md"
            )
            if write:
                target_file.write_text(canonical_text, encoding="utf-8")

    in_sync = len(drift_messages) == 0
    return in_sync, drift_messages


def run_sync(
    repo_root: Path,
    write: bool = False,
    global_dir: Optional[Path] = None,
) -> Tuple[bool, List[str]]:
    """
    Executes full synchronization pass:
      1. Byte-copy canonical sources into per-skill resources/ directories
         (init + manage, per RESOURCE_SYNC_SETS).
      2. Mirror skills/ (SKILL.md + resources/) into .agents/skills/.
      3. (Optional) Mirror skills/ into global_dir.
    """
    all_drifts: List[str] = []

    res_ok, res_drifts = sync_skill_resources(repo_root, write=write)
    all_drifts.extend(res_drifts)

    mir_ok, mir_drifts = sync_skill_mirroring(repo_root, write=write)
    all_drifts.extend(mir_drifts)

    if global_dir:
        glob_ok, glob_drifts = sync_global_skills(
            repo_root, global_dir=global_dir, write=write
        )
        all_drifts.extend(glob_drifts)

    in_sync = len(all_drifts) == 0
    return in_sync, all_drifts


def main(argv: Optional[List[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        description="Template Injection & Skill Parity Synchronizer"
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="Dry-run check: verify that skill resources and mirrored skills have zero drift",
    )
    parser.add_argument(
        "--write",
        action="store_true",
        help="Write updates: byte-copy resources/ for skills with sync-managed "
        "resources (init, manage) and mirror to .agents/skills/",
    )
    parser.add_argument(
        "--repo-root",
        type=str,
        default=None,
        help="Repository root directory (default: current working directory)",
    )
    parser.add_argument(
        "--global-skills",
        nargs="?",
        const=str(Path.home() / ".gemini" / "config" / "skills"),
        default=None,
        help="Optional path to global harness skills directory (default: ~/.gemini/config/skills) to check or update parity.",
    )

    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    if hasattr(sys.stderr, "reconfigure"):
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")

    args = parser.parse_args(argv)

    if not args.check and not args.write:
        parser.print_help()
        sys.stderr.write("\nError: Must specify either --check or --write.\n")
        return 2

    repo_root = Path(args.repo_root).resolve() if args.repo_root else Path.cwd()
    global_dir = Path(args.global_skills).resolve() if args.global_skills else None

    in_sync, drifts = run_sync(repo_root, write=args.write, global_dir=global_dir)

    if args.write:
        # Audit §9: unrecoverable errors (missing canonical templates/skills,
        # missing embedded blocks) must yield a non-zero exit — never a
        # success message — and --write must re-verify its own result.
        fatal = [d for d in drifts if d.startswith("Missing") or d.startswith("Error")]
        fixed = [d for d in drifts if d not in fatal]

        unresolved: List[str] = []
        if fixed and not fatal:
            recheck_ok, recheck_drifts = run_sync(
                repo_root, write=False, global_dir=global_dir
            )
            if not recheck_ok:
                unresolved = recheck_drifts

        if fatal or unresolved:
            for d in fatal:
                sys.stderr.write(f"  [FAIL] {d}\n")
            for d in unresolved:
                sys.stderr.write(f"  [FAIL] unresolved after --write: {d}\n")
            sys.stderr.write(
                "Template Synchronizer: FAILED — unrecoverable errors remain; "
                "synchronization is not clean.\n"
            )
            return 1

        if fixed:
            sys.stdout.write(f"Template Synchronizer: Successfully synchronized {len(fixed)} item(s):\n")
            for d in fixed:
                sys.stdout.write(f"  [OK] {d}\n")
        else:
            sys.stdout.write("Template Synchronizer: All templates and mirrored skills are already up to date.\n")
        return 0

    if args.check:
        if in_sync:
            sys.stdout.write("Template Synchronizer: 0 drift detected. Canonical templates and skills are in full parity.\n")
            return 0
        else:
            sys.stderr.write(f"Template Synchronizer: Drift detected in {len(drifts)} item(s):\n")
            for d in drifts:
                sys.stderr.write(f"  [FAIL] {d}\n")
            sys.stderr.write("\nRemedy: Run 'python scripts/sync_templates.py --write' to synchronize templates.\n")
            return 1


    return 0


if __name__ == "__main__":
    sys.exit(main())
