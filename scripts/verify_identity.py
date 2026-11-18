#!/usr/bin/env python3
"""
Multi-Harness Identity Engine & Registry Validator.
Zero-dependency utility (pure Python 3.10+ standard library).
Validates .agents/meta-project.json registry, actor_id bindings, and profile synchronization.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import sys
from typing import Any, Dict, List, Optional, Tuple

try:  # shared helpers (ADR #22): package-style import (tests: from
    # scripts.verify_identity import ...)
    from scripts._common import _error_envelope
except ImportError:  # direct script execution: python scripts/verify_identity.py
    from _common import _error_envelope

DEFAULT_REGISTRY_REL = Path(".agents/meta-project.json")

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

# Supported registry schema major.minor pairs (patch component is ignored:
# patch releases are backwards compatible).
SUPPORTED_REGISTRY_SCHEMA_VERSIONS = ("1.0", "1.1")

# Lifecycle states for installations and role bindings. Admission to perform
# runtime actions additionally requires 'active' on BOTH the binding and its
# installation (see evaluate_actor_admission).
VALID_INSTALLATION_STATUSES = {"planned", "active", "disabled", "archived"}
VALID_BINDING_STATUSES = {"planned", "active", "disabled", "archived"}


def validate_schema_version(value: Any) -> List[str]:
    """Validates the registry schema_version value (audit §3: the actually
    supported version must be enforced, not mere field presence)."""
    raw = str(value or "").strip()
    if not raw:
        return ["Missing required field 'schema_version' in registry"]
    parts = raw.split(".")
    if len(parts) < 2 or not parts[0].isdigit() or not parts[1].isdigit():
        return [
            f"Invalid schema_version '{raw}': expected '<major>.<minor>[.<patch>]'"
            " with numeric components"
        ]
    major_minor = f"{parts[0]}.{parts[1]}"
    if major_minor not in SUPPORTED_REGISTRY_SCHEMA_VERSIONS:
        return [
            f"Unsupported schema_version '{raw}'. Supported major.minor pairs: "
            f"{list(SUPPORTED_REGISTRY_SCHEMA_VERSIONS)}"
        ]
    return []


def evaluate_actor_admission(
    registry: Dict[str, Any], binding: Dict[str, Any]
) -> Optional[str]:
    """
    Separate admission control (audit §3): a located binding authorizes
    runtime actions only when the binding AND its installation are 'active'.
    Looking up a planned entry remains legitimate for inspection purposes;
    this function decides whether the actor may ACT under that identity.
    Returns None when admitted, otherwise a human-readable denial reason.
    """
    inst_id = binding.get("installation_id")
    inst_info = (registry.get("installations") or {}).get(inst_id) or {}
    binding_status = binding.get("status")
    inst_status = inst_info.get("status")
    if binding_status != "active" or inst_status != "active":
        return (
            f"Identity not admitted: binding status is '{binding_status}' "
            f"and installation '{inst_id}' status is '{inst_status}'. Runtime "
            f"authorization requires an 'active' binding on an 'active' "
            f"installation (lookup succeeded; admission denied)."
        )
    return None


def validate_scenario_section(scenario: Any) -> List[str]:
    """Validates structure and enums of the optional scenario section."""
    errors: List[str] = []
    if scenario is None:
        return errors

    if not isinstance(scenario, dict):
        return ["Field 'scenario' must be a dictionary in registry"]

    profile = scenario.get("profile")
    if not profile:
        errors.append("Field 'scenario' missing required key 'profile'")
    elif profile not in VALID_SCENARIO_PROFILES:
        errors.append(
            f"Invalid scenario profile '{profile}'. Allowed: {sorted(list(VALID_SCENARIO_PROFILES))}"
        )

    dep_mode = scenario.get("deployment_mode")
    if dep_mode and dep_mode not in VALID_DEPLOYMENT_MODES:
        errors.append(
            f"Invalid scenario deployment_mode '{dep_mode}'. Allowed: {sorted(list(VALID_DEPLOYMENT_MODES))}"
        )

    collab = scenario.get("collaboration_model")
    if collab and collab not in VALID_COLLABORATION_MODELS:
        errors.append(
            f"Invalid scenario collaboration_model '{collab}'. Allowed: {sorted(list(VALID_COLLABORATION_MODELS))}"
        )

    verif = scenario.get("verification_policy")
    if verif and verif not in VALID_VERIFICATION_POLICIES:
        errors.append(
            f"Invalid scenario verification_policy '{verif}'. Allowed: {sorted(list(VALID_VERIFICATION_POLICIES))}"
        )

    deleg = scenario.get("delegation_level")
    if deleg and deleg not in VALID_DELEGATION_LEVELS:
        errors.append(
            f"Invalid scenario delegation_level '{deleg}'. Allowed: {sorted(list(VALID_DELEGATION_LEVELS))}"
        )

    constraints = scenario.get("constraints")
    if constraints is not None and not isinstance(constraints, list):
        errors.append("Scenario 'constraints' must be a list")

    unresolved = scenario.get("unresolved_questions")
    if unresolved is not None and not isinstance(unresolved, list):
        errors.append("Scenario 'unresolved_questions' must be a list")

    return errors



def parse_yaml_frontmatter(content: str) -> Dict[str, str]:
    """Extracts key-value string pairs from YAML frontmatter between --- markers."""
    match = re.match(r"^---\s*\n(.*?)\n---", content, re.DOTALL)
    if not match:
        return {}
    frontmatter_text = match.group(1)
    result: Dict[str, str] = {}
    for line in frontmatter_text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if ":" in line:
            key, val = line.split(":", 1)
            key = key.strip()
            val = val.strip().strip("'\"")
            result[key] = val
    return result


def load_registry(repo_root: Path) -> Tuple[Optional[Dict[str, Any]], Optional[str]]:
    """Loads and parses .agents/meta-project.json."""
    registry_path = repo_root / DEFAULT_REGISTRY_REL
    if not registry_path.is_file():
        return None, f"Registry file missing: {DEFAULT_REGISTRY_REL} at {repo_root}"
    try:
        content = registry_path.read_text(encoding="utf-8")
        data = json.loads(content)
        return data, None
    except Exception as exc:
        return None, f"Failed to parse {DEFAULT_REGISTRY_REL}: {exc}"


def validate_identity_registry(repo_root: Path) -> Tuple[bool, List[str]]:
    """
    Validates complete identity registry and all role profile bindings.
    Returns (is_valid, list_of_errors).
    """
    errors: List[str] = []
    registry, err = load_registry(repo_root)
    if err:
        return False, [err]

    assert registry is not None

    # Structured diagnostics instead of tracebacks for malformed roots
    # (audit §3: a non-object root must produce errors, never AttributeError).
    if not isinstance(registry, dict):
        return False, [
            f"Registry root JSON must be an object (dict), "
            f"got {type(registry).__name__}"
        ]

    errors.extend(validate_schema_version(registry.get("schema_version")))

    project_id = registry.get("project_id")
    if not project_id:
        errors.append("Missing required field 'project_id' in registry")

    installations: Dict[str, Any] = registry.get("installations", {})
    if not isinstance(installations, dict):
        return False, ["Field 'installations' must be a dictionary in registry"]
    if not installations:
        errors.append("Missing 'installations' dictionary in registry")

    # A correct initial registry without a deployed team is VALID (audit §3:
    # validators must not force the creation of a fictitious role).
    roles: Dict[str, Any] = registry.get("roles", {})
    if not isinstance(roles, dict):
        return False, ["Field 'roles' must be a dictionary in registry"]

    errors.extend(validate_scenario_section(registry.get("scenario")))

    seen_actor_ids: Dict[str, str] = {}

    for inst_id, inst_data in installations.items():
        if not isinstance(inst_data, dict):
            errors.append(f"Installation '{inst_id}' must be an object")
            continue
        harness_id = inst_data.get("harness_id")
        status = inst_data.get("status")
        if not harness_id:
            errors.append(f"Installation '{inst_id}' missing 'harness_id'")
        if not status:
            errors.append(f"Installation '{inst_id}' missing 'status'")
        elif status not in VALID_INSTALLATION_STATUSES:
            errors.append(
                f"Installation '{inst_id}' has unsupported status '{status}'. "
                f"Allowed: {sorted(VALID_INSTALLATION_STATUSES)}"
            )

    for role_id, role_data in roles.items():
        if not isinstance(role_data, dict):
            errors.append(f"Role '{role_id}' must be an object")
            continue

        bindings: Dict[str, Any] = role_data.get("bindings", {})
        if not isinstance(bindings, dict):
            errors.append(f"Role '{role_id}' missing 'bindings' dictionary")
            continue

        for inst_id, binding in bindings.items():
            if not isinstance(binding, dict):
                errors.append(f"Binding '{role_id}.{inst_id}' must be an object")
                continue

            actor_id = binding.get("actor_id")
            b_status = binding.get("status")
            profile_path_rel = binding.get("profile_path")

            if not actor_id:
                errors.append(f"Binding '{role_id}.{inst_id}' missing 'actor_id'")
                continue

            if b_status is not None and b_status not in VALID_BINDING_STATUSES:
                errors.append(
                    f"Binding '{role_id}.{inst_id}' has unsupported status "
                    f"'{b_status}'. Allowed: {sorted(VALID_BINDING_STATUSES)}"
                )

            # Duplicate check
            existing_loc = seen_actor_ids.get(actor_id)
            if existing_loc:
                errors.append(
                    f"Duplicate actor_id '{actor_id}' found in '{role_id}.{inst_id}' and '{existing_loc}'"
                )
            else:
                seen_actor_ids[actor_id] = f"{role_id}.{inst_id}"

            # Format check: project_id::harness_id::installation_id::role_id
            parts = actor_id.split("::")
            h_id: Optional[str] = None
            if len(parts) != 4:
                errors.append(
                    f"Invalid actor_id format '{actor_id}'. Expected '<project_id>::<harness_id>::<installation_id>::<role_id>'"
                )
            else:
                p_id, h_id, i_id, r_id = parts
                if project_id and p_id != project_id:
                    errors.append(
                        f"actor_id '{actor_id}' project_id mismatch: '{p_id}' != '{project_id}'"
                    )
                if i_id != inst_id:
                    errors.append(
                        f"actor_id '{actor_id}' installation_id mismatch: '{i_id}' != '{inst_id}'"
                    )
                if r_id != role_id:
                    errors.append(
                        f"actor_id '{actor_id}' role_id mismatch: '{r_id}' != '{role_id}'"
                    )

            # Check installation existence & planned status rules
            inst_info = installations.get(inst_id)
            if not inst_info:
                errors.append(
                    f"Binding '{role_id}.{inst_id}' references unknown installation '{inst_id}'"
                )
            else:
                inst_status = inst_info.get("status")
                if inst_status == "planned" and b_status == "active":
                    errors.append(
                        f"Binding '{role_id}.{inst_id}' cannot be 'active' while installation '{inst_id}' status is 'planned'"
                    )
                # Harness substitution check (audit §3): the actor_id harness
                # component must match the installation's harness_id.
                inst_harness = inst_info.get("harness_id")
                if h_id and inst_harness and h_id != inst_harness:
                    errors.append(
                        f"actor_id '{actor_id}' harness mismatch: harness component "
                        f"'{h_id}' != installation '{inst_id}' harness_id '{inst_harness}'"
                    )

            # Profile verification if path specified
            if profile_path_rel:
                prof_path = repo_root / profile_path_rel
                if not prof_path.is_file():
                    if b_status == "active":
                        errors.append(
                            f"Active role '{role_id}' missing profile file at {profile_path_rel}"
                        )
                else:
                    prof_content = prof_path.read_text(encoding="utf-8")
                    frontmatter = parse_yaml_frontmatter(prof_content)

                    p_actor_id = frontmatter.get("actor_id")
                    p_role_id = frontmatter.get("role_id")
                    p_harness_id = frontmatter.get("harness_id")
                    p_inst_id = frontmatter.get("installation_id")

                    if p_actor_id and p_actor_id != actor_id:
                        errors.append(
                            f"Mismatch in {profile_path_rel}: profile actor_id '{p_actor_id}' != registry '{actor_id}'"
                        )
                    if p_role_id and p_role_id != role_id:
                        errors.append(
                            f"Mismatch in {profile_path_rel}: profile role_id '{p_role_id}' != registry '{role_id}'"
                        )
                    if p_harness_id and h_id and p_harness_id != h_id:
                        errors.append(
                            f"Mismatch in {profile_path_rel}: profile harness_id "
                            f"'{p_harness_id}' != binding harness '{h_id}'"
                        )

                    # Check for premature active assertion of planned installation
                    referenced_inst = p_inst_id
                    if not referenced_inst and p_actor_id and len(p_actor_id.split("::")) == 4:
                        referenced_inst = p_actor_id.split("::")[2]

                    if referenced_inst and installations.get(referenced_inst, {}).get("status") == "planned":
                        errors.append(
                            f"Profile {profile_path_rel} prematurely references planned installation '{referenced_inst}' (installation status is 'planned')"
                        )

    is_valid = len(errors) == 0
    return is_valid, errors


def resolve_actor_binding(
    repo_root: Path, actor_id: str
) -> Tuple[bool, Optional[Dict[str, Any]]]:
    """
    Looks up exact actor_id in registry and returns binding info if valid.
    Fails closed if actor_id is unknown or invalid.
    """
    registry, _ = load_registry(repo_root)
    if not registry:
        return False, None

    roles: Dict[str, Any] = registry.get("roles", {})
    for role_id, role_data in roles.items():
        bindings: Dict[str, Any] = role_data.get("bindings", {})
        for inst_id, binding in bindings.items():
            if binding.get("actor_id") == actor_id:
                result = dict(binding)
                result["role_id"] = role_id
                result["installation_id"] = inst_id
                return True, result

    return False, None


def main(argv: Optional[List[str]] = None) -> int:
    """CLI entrypoint for verify_identity."""
    parser = argparse.ArgumentParser(
        description="Multi-Harness Identity Engine: Verify registry (.agents/meta-project.json) and role profile bindings."
    )
    parser.add_argument(
        "--repo-root",
        default=".",
        help="Path to repository root (defaults to current directory)",
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="Perform comprehensive validation of registry and role profiles",
    )
    parser.add_argument(
        "--actor",
        help="Validate and resolve a specific qualified actor_id",
    )

    args = parser.parse_args(argv)
    root = Path(args.repo_root).resolve()

    if args.actor:
        found, binding = resolve_actor_binding(root, args.actor)
        if not found:
            sys.stderr.write(_error_envelope(
                "IDENTITY_UNRESOLVED",
                str(DEFAULT_REGISTRY_REL).replace("\\", "/"),
                "actor_id is not resolvable against the registry",
                f"'{args.actor}' present as an active binding in "
                f"{DEFAULT_REGISTRY_REL}",
                "no registry entry matches the qualified actor_id",
                "fix the actor_id or register the binding via "
                "manage-meta-project, then re-run scripts/verify_identity.py "
                "--check",
            ) + "\n")
            sys.stderr.write(f"[ERROR] Unresolved identity: actor_id '{args.actor}' not found in registry.\n")
            return 1
        # Admission control (audit §3): lookup success does not authorize
        # runtime actions; the binding and its installation must be 'active'.
        registry, _ = load_registry(root)
        denial = evaluate_actor_admission(registry or {}, binding)
        if denial:
            sys.stderr.write(_error_envelope(
                "IDENTITY_UNRESOLVED",
                str(DEFAULT_REGISTRY_REL).replace("\\", "/"),
                "identity resolved but not admitted to act",
                "both the actor binding and its installation 'active'",
                f"admission denied: {denial}",
                "activate the binding/installation via manage-meta-project "
                "before performing runtime actions",
            ) + "\n")
            sys.stderr.write(f"[ERROR] {denial}\n")
            return 1
        sys.stdout.write(f"[OK] Identity resolved and admitted: {args.actor} -> role={binding['role_id']}, installation={binding['installation_id']}, status={binding.get('status')}\n")
        return 0

    if args.check:
        valid, errors = validate_identity_registry(root)
        if not valid:
            sys.stderr.write(_error_envelope(
                "IDENTITY_UNRESOLVED",
                str(DEFAULT_REGISTRY_REL).replace("\\", "/"),
                "identity registry validation failed",
                "a schema-valid registry in sync with role profiles",
                f"{len(errors)} validation issue(s) detected",
                "repair the registry/profiles per the issues listed below "
                "(manage-meta-project), then re-run --check",
            ) + "\n")
            sys.stderr.write(f"[ERROR] Identity verification failed ({len(errors)} issues):\n")
            for err in errors:
                sys.stderr.write(f"  * {err}\n")
            return 1
        sys.stdout.write("[OK] Identity verification passed: 0 errors detected. Registry and profiles in sync.\n")
        return 0

    parser.print_help()
    return 0


if __name__ == "__main__":
    sys.exit(main())
