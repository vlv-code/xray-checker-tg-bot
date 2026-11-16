---
name: sync-architecture
description: >-
  Use this skill to analyze repository modules, dependencies, and entry points,
  and generate or update `.agents/project_knowledge/architecture.md`.
---

# Sync Project Architecture Slice

This skill performs a black-box structural analysis of the repository and produces a compact architecture slice for agents without requiring a full codebase re-read during each conversation.

---

## When to invoke skill

* The user requests: "update architecture", "sync component map", "extract architectural foundations".
* Major structural changes occurred in the project (new services added, entry points changed, or tech stack modified).

---

## Execution steps

1. **Analyze configurations and manifests:**  
   Locate and inspect key dependency and build files (`package.json`, `pyproject.toml`, `go.mod`, `Cargo.toml`, `docker-compose.yml`, `.github/workflows/ci.yml`). Identify the current stack and runtime environment.

2. **Analyze file structure:**  
   Identify top-level directories in `src/` (or repository root), and find entry points (`main.*`, `index.*`, `app.*`, CLI commands, test runners).

3. **Identify components and relationships:**  
   Define 3–5 core project subsystems, their roles, and interaction patterns (HTTP REST, gRPC, events, direct function calls).

4. **Update `architecture.md` (with Local Adaptation Preservation):**  
   Before overwriting `.agents/project_knowledge/architecture.md`, read the existing file and extract all local adaptation blocks marked with `<!-- local_repo_data:TAG -->...<!-- /local_repo_data:TAG -->` or `# local_repo_data:TAG...# /local_repo_data:TAG`.
   Write the result to `.agents/project_knowledge/architecture.md`, preserving the canonical section structure and seamlessly restoring any extracted `local_repo_data` blocks into their respective sections:
   - High-level overview (stack, application type).
   - Core components and data flows (preserving customized flows and single source of truth).
   - Data invariants and state management.
   - Architectural patterns.

5. **Report:**  
   Provide a brief summary of updated components for user alignment.
