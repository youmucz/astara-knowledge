# Copyright (c) 2026-present Astara and contributors
# SPDX-License-Identifier: Apache-2.0
# See the LICENSE file for details.

"""Deterministic retrieval and answer quality metrics.

Pure functions only: no network, no service, no Django/Go runtime state.
Every metric is computed over opaque case ids and passage anchors so the
evaluation report never carries corpus content.
"""

from __future__ import annotations

import math
from typing import Iterable, Mapping, Sequence


def precision_at_k(ranked: Sequence[str], relevant: set[str], k: int) -> float | None:
    if k <= 0 or not ranked:
        return None
    top = list(ranked)[:k]
    hits = sum(1 for item in top if item in relevant)
    return hits / k


def recall_at_k(ranked: Sequence[str], relevant: set[str], k: int) -> float | None:
    if not relevant:
        return None
    top = list(ranked)[:k]
    hits = sum(1 for item in top if item in relevant)
    return hits / len(relevant)


def reciprocal_rank(ranked: Sequence[str], relevant: set[str]) -> float:
    for position, item in enumerate(ranked, start=1):
        if item in relevant:
            return 1.0 / position
    return 0.0


def average_precision(ranked: Sequence[str], relevant: set[str]) -> float | None:
    if not relevant:
        return None
    hits = 0
    precision_sum = 0.0
    for position, item in enumerate(ranked, start=1):
        if item in relevant:
            hits += 1
            precision_sum += hits / position
    return precision_sum / len(relevant)


def mean_average_precision(rankings: Iterable[tuple[Sequence[str], set[str]]]) -> float | None:
    scores = [average_precision(ranked, relevant) for ranked, relevant in rankings]
    scores = [score for score in scores if score is not None]
    if not scores:
        return None
    return sum(scores) / len(scores)


def mean_reciprocal_rank(rankings: Iterable[tuple[Sequence[str], set[str]]]) -> float | None:
    scores = [reciprocal_rank(ranked, relevant) for ranked, relevant in rankings]
    if not scores:
        return None
    return sum(scores) / len(scores)


def ndcg_at_k(ranked: Sequence[str], relevant: set[str], k: int) -> float | None:
    if k <= 0 or not relevant:
        return None
    dcg = 0.0
    for position, item in enumerate(list(ranked)[:k], start=1):
        if item in relevant:
            dcg += 1.0 / math.log2(position + 1)
    ideal_hits = min(len(relevant), k)
    idcg = sum(1.0 / math.log2(position + 1) for position in range(1, ideal_hits + 1))
    if idcg <= 0:
        return None
    return dcg / idcg


def keyword_coverage(answer: str, must_contain: Sequence[str]) -> float:
    if not must_contain:
        return 1.0
    present = sum(1 for keyword in must_contain if keyword in answer)
    return present / len(must_contain)


def forbidden_leakage(answer: str, must_not_contain: Sequence[str]) -> int:
    return sum(1 for phrase in must_not_contain if phrase in answer)


def percentile(values: Sequence[float], percentile_value: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = min(len(ordered) - 1, max(0, math.ceil(percentile_value / 100.0 * len(ordered)) - 1))
    return ordered[index]


def ratio(numerator: float, denominator: float) -> float | None:
    if denominator <= 0:
        return None
    return numerator / denominator
