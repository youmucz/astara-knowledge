# Copyright (c) 2026-present Astara and contributors
# SPDX-License-Identifier: Apache-2.0
# See the LICENSE file for details.

"""Versioned quality gates: thresholds are compared, never negotiated.

A release candidate passes only when every declared gate is met. The gate
report is machine-readable, content-free (case ids, ratios, and booleans
only), and doubles as promotion evidence.
"""

from __future__ import annotations

import json
import pathlib
from typing import Any, Mapping

ROOT = pathlib.Path(__file__).resolve().parents[1]
DEFAULT_THRESHOLDS = ROOT / "evaluation" / "thresholds.v1.json"

#: Gates that must be evaluated; a missing observation fails closed.
REQUIRED_GATES = (
    "precision_at_5",
    "recall_at_5",
    "ndcg_at_5",
    "mrr",
    "map",
    "answer_keyword_coverage",
    "no_answer_correctness",
    "citation_accuracy",
    "authorization_pass_rate",
    "permission_denial_rate",
    "latency_p95_seconds",
)


class GateError(ValueError):
    """Raised when thresholds or observations are malformed."""


def load_thresholds(path: pathlib.Path | str | None = None) -> dict[str, Any]:
    path = pathlib.Path(path) if path else DEFAULT_THRESHOLDS
    try:
        thresholds = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        raise GateError(f"thresholds are unreadable: {exc}") from exc
    if thresholds.get("schemaVersion") != 1:
        raise GateError("thresholds schemaVersion must be 1")
    gates = thresholds.get("gates")
    if not isinstance(gates, dict) or not gates:
        raise GateError("thresholds gates are missing")
    for name in REQUIRED_GATES:
        if name not in gates:
            raise GateError(f"thresholds are missing required gate: {name}")
        value = gates[name]
        if not isinstance(value, (int, float)) or isinstance(value, bool) or value < 0:
            raise GateError(f"threshold {name} must be a non-negative number")
    return gates


def evaluate_gates(observations: Mapping[str, Any], thresholds: Mapping[str, Any]) -> dict[str, Any]:
    """Compare observations against thresholds; every miss is a failure."""
    results: dict[str, Any] = {}
    failures = []
    for name in REQUIRED_GATES:
        threshold = thresholds[name]
        observed = observations.get(name)
        if observed is None:
            results[name] = {"threshold": threshold, "observed": None, "state": "no_data"}
            failures.append(f"{name}:no_data")
            continue
        # Latency is an upper bound; every other gate is a lower bound.
        at_most = name == "latency_p95_seconds"
        met = observed <= threshold if at_most else observed >= threshold
        results[name] = {
            "threshold": threshold,
            "observed": observed,
            "state": "met" if met else "breached",
        }
        if not met:
            failures.append(f"{name}:breached")

    for name in ("min_answered_cases", "min_retrieval_cases"):
        threshold = thresholds.get(name)
        observed = observations.get(name)
        if threshold is None or observed is None:
            continue
        met = observed >= threshold
        results[name] = {"threshold": threshold, "observed": observed, "state": "met" if met else "breached"}
        if not met:
            failures.append(f"{name}:breached")

    return {
        "schemaVersion": 1,
        "passed": not failures,
        "failures": failures,
        "gates": results,
    }


def write_report(report: Mapping[str, Any], path: pathlib.Path | str) -> None:
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report, indent=2, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")
