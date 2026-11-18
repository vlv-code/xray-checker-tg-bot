# Weak Spots, Risks, and Technical Debt

Registry of fragile areas, known bugs, and constraints of the repository. Agents MUST consult this registry before modifying listed modules.

---

## 1. Fragile Modules and High Coupling
- *(Example)* `src/legacy_client.py`: high coupling with third-party API; timeout modifications break background workers.

## 2. Environment and Test Constraints
- *(Example)* Integration tests require local Redis running on port 6379, otherwise fail with timeout.
- *(Example)* Windows build fails when paths exceed 260 characters unless LongPathsEnabled is enabled.

## 3. Deferred Technical Debt
- *(Example)* Missing request idempotency locks in transaction processor.
- *(Example)* Hardcoded configuration parameters in report generation module.
