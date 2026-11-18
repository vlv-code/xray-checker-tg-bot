# Architectural Foundations

**Map update rule:** The map is updated IN THE SAME CHANGE that touches a component — a decision without a map update is an incomplete change. One row per component, no prose.

## 1. Component Map

| Component | Location | Role | Depends on |
| :--- | :--- | :--- | :--- |
| API Gateway | `src/api/` | Validates and routes external requests | Auth Service, Domain Core |
| Task Scheduler | `src/scheduler/` | Runs background jobs and retries | Storage Layer |

## 2. Stack and Invariants
- **Stack:** (language, framework, key libraries — one line)
- **State + Single Source of Truth:** (where state lives and which component owns it — one line)
- **Cross-border contracts:** (interfaces and data formats at component boundaries — one line)

## 3. End-to-End Data Flows (Pre-Flight Flow Tracing)
*(Conditional section: include ONLY for multi-tier systems (client-server, API, UI, or storage boundaries); omit for single-layer CLI/library projects to prevent context bloat.)*

`[Origin: UI/CLI/Event] ➔ [Transport: API Route / Message] ➔ [Domain / Storage: Persistence] ➔ [Assembly / Packager] ➔ [Consumer: Client / Daemon]`
- **Primary Data Flow:** [e.g. User Configuration ➔ Sync Route ➔ Atomic Store ➔ Client Polling]
