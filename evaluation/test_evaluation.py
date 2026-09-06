# Copyright (c) 2026-present Astara and contributors
# SPDX-License-Identifier: Apache-2.0
# See the LICENSE file for details.

"""Unit tests for the evaluation metrics, gates, and fixtures."""

import json
import pathlib
import unittest

from evaluation.gates import GateError, evaluate_gates, load_thresholds
from evaluation.metrics import (
    average_precision,
    forbidden_leakage,
    keyword_coverage,
    mean_average_precision,
    mean_reciprocal_rank,
    ndcg_at_k,
    percentile,
    precision_at_k,
    recall_at_k,
    reciprocal_rank,
)

ROOT = pathlib.Path(__file__).resolve().parents[1]


class MetricTests(unittest.TestCase):
    def test_ranking_metrics_are_deterministic(self):
        ranked = ["a", "b", "c", "d", "e"]
        relevant = {"b", "d"}
        self.assertEqual(precision_at_k(ranked, relevant, 5), 0.4)
        self.assertEqual(recall_at_k(ranked, relevant, 5), 1.0)
        self.assertEqual(reciprocal_rank(ranked, relevant), 0.5)
        # DCG = 1/log2(3) + 1/log2(5); IDCG = 1/log2(2) + 1/log2(3).
        self.assertAlmostEqual(ndcg_at_k(ranked, relevant, 5), 0.6509209298071326)
        self.assertAlmostEqual(average_precision(ranked, relevant), 0.5)

    def test_empty_inputs_fail_closed(self):
        self.assertIsNone(precision_at_k([], {"a"}, 5))
        self.assertIsNone(recall_at_k(["a"], set(), 5))
        self.assertEqual(reciprocal_rank(["a"], {"b"}), 0.0)
        self.assertIsNone(ndcg_at_k(["a"], set(), 5))

    def test_aggregation(self):
        rankings = [(["a", "b"], {"a"}), (["c", "d"], {"d"})]
        self.assertAlmostEqual(mean_reciprocal_rank(rankings), 0.75)
        self.assertAlmostEqual(mean_average_precision(rankings), 0.75)

    def test_answer_coverage_and_leakage(self):
        answer = "一线城市住宿标准是每晚 600 元。"
        self.assertEqual(keyword_coverage(answer, ["600", "一线城市"]), 1.0)
        self.assertEqual(keyword_coverage(answer, ["700"]), 0.0)
        self.assertEqual(forbidden_leakage(answer, ["450 元为一线城市标准"]), 0)

    def test_percentile_is_monotone(self):
        values = [1.0, 2.0, 3.0, 4.0, 5.0]
        self.assertEqual(percentile(values, 50), 3.0)
        self.assertEqual(percentile(values, 95), 5.0)
        self.assertIsNone(percentile([], 95))


class GateTests(unittest.TestCase):
    def test_thresholds_load_and_fail_closed(self):
        gates = load_thresholds()
        for name in (
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
        ):
            self.assertIn(name, gates)
        with self.assertRaises(GateError):
            load_thresholds(__file__)

    def test_breached_and_missing_gates_block_promotion(self):
        thresholds = load_thresholds()
        observations = {
            "precision_at_5": 0.9,
            "recall_at_5": 0.8,
            "ndcg_at_5": 0.9,
            "mrr": 0.9,
            "map": 0.9,
            "answer_keyword_coverage": 0.9,
            "no_answer_correctness": 1.0,
            "citation_accuracy": 1.0,
            "authorization_pass_rate": 1.0,
            "permission_denial_rate": 1.0,
            "latency_p95_seconds": 4.0,
            "min_answered_cases": 10,
            "min_retrieval_cases": 10,
        }
        report = evaluate_gates(observations, thresholds)
        self.assertTrue(report["passed"])

        breached = dict(observations, precision_at_5=0.5, latency_p95_seconds=99.0, citation_accuracy=None)
        report = evaluate_gates(breached, thresholds)
        self.assertFalse(report["passed"])
        self.assertIn("precision_at_5:breached", report["failures"])
        self.assertIn("latency_p95_seconds:breached", report["failures"])
        self.assertIn("citation_accuracy:no_data", report["failures"])


class FixtureTests(unittest.TestCase):
    def test_corpus_and_parse_fixtures_are_versioned_and_closed(self):
        corpus = json.loads((ROOT / "evaluation" / "corpus" / "zh-enterprise-qa.v1.json").read_text(encoding="utf-8"))
        self.assertEqual(corpus["schemaVersion"], 1)
        self.assertEqual(corpus["version"], "zh-enterprise-qa.v1")
        self.assertGreaterEqual(len(corpus["documents"]), 5)
        kinds = {case["kind"] for case in corpus["cases"]}
        self.assertIn("retrieval", kinds)
        self.assertIn("answer", kinds)
        self.assertIn("no_answer", kinds)
        self.assertIn("permission_denied", kinds)
        self.assertIn("revocation", kinds)
        self.assertIn("deleted", kinds)
        self.assertIn("stale_source", kinds)
        self.assertIn("citation", kinds)
        document_ids = {document["external_id"] for document in corpus["documents"]}
        for case in corpus["cases"]:
            referenced = case.get("expect_document") or case.get("denied_document") or case.get("revoked_document") or case.get("deleted_document") or case.get("stale_document")
            if referenced:
                self.assertIn(referenced, document_ids, case["id"])

        fixtures = json.loads((ROOT / "evaluation" / "fixtures" / "parse.v1.json").read_text(encoding="utf-8"))
        self.assertEqual(fixtures["schemaVersion"], 1)
        self.assertGreaterEqual(len(fixtures["cases"]), 5)
        for case in fixtures["cases"]:
            self.assertIn("normalized_contains", case["expect"])
            self.assertTrue(case["expect"]["normalized_contains"])


if __name__ == "__main__":
    unittest.main()
