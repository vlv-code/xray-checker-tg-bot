#!/usr/bin/env python3
"""
Shared helpers for the scripts/ automation utilities (ADR #22).

Single source of truth for the small Wave E helpers that were previously
duplicated per script (``_error_envelope`` in 7 copies, ``_utc_now_iso`` in
the update engine plus 3 inline copies in the report engine). Consumers
import it through the mirror try/except pattern:

    try:  # package-style import (tests: from scripts.<name> import ...)
        from scripts._common import _error_envelope
    except ImportError:  # direct script execution: python scripts/<name>.py
        from _common import _error_envelope

Delivery contract (ADR #22): ``scripts/_common.py`` is a registered manifest
component and every script that imports it declares
``requires: ["scripts/_common.py"]`` — a subset deployment that selects the
script must select the helper module too. There is deliberately NO local
fallback definition: if ``_common`` is absent the ImportError propagates at
import time (honest fail-fast) instead of silently reviving a drifted copy.

Presentation-only contract: these helpers never alter decisions, refusal
payloads, exit codes, or rc semantics.

Zero-dependency: Python 3.10+ standard library only.
"""

from __future__ import annotations

import datetime


def _error_envelope(
    code: str,
    file: str,
    reason: str,
    expected: str,
    actual: str,
    next_step: str,
) -> str:
    """Uniform Wave E error envelope (presentation ONLY).

    Returns the canonical leading line of a failure block:
        [CODE] <file>: <reason> | expected: <X> | actual: <Y> | next: <step>
    The envelope never alters gate/resolution/compaction/update/report
    decisions, refusal payloads, exit codes, or rc semantics — it wraps the
    existing structured refusals, which keep their full content on the
    following lines."""
    return (
        f"[{code}] {file}: {reason} | expected: {expected} | "
        f"actual: {actual} | next: {next_step}"
    )


def _utc_now_iso() -> str:
    """Canonical UTC timestamp used by reports, journals, and locks."""
    return datetime.datetime.now(datetime.timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )
