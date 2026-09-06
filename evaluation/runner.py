#!/usr/bin/env python3
# Copyright (c) 2026-present Astara and contributors
# SPDX-License-Identifier: Apache-2.0
# See the LICENSE file for details.

"""Live evaluation runner for the Astara Knowledge release gates.

Seeds the versioned Chinese enterprise QA corpus into a running Knowledge
service, executes retrieval/answer/no-answer/permission/revocation/deletion/
stale-source/citation cases, computes the declared metrics, and compares them
with the versioned thresholds. Exit code 0 (all gates met) is the only
promotion evidence; the written report is content-free.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import sys
import time
import urllib.error
import urllib.request
import uuid

from evaluation.gates import evaluate_gates, load_thresholds, write_report
from evaluation.metrics import (
    forbidden_leakage,
    keyword_coverage,
    mean_average_precision,
    mean_reciprocal_rank,
    ndcg_at_k,
    percentile,
    precision_at_k,
    recall_at_k,
)

ROOT = pathlib.Path(__file__).resolve().parents[1]


class KnowledgeService:
    """Minimal control-plane client using the Astara service-auth header."""

    def __init__(self, base_url: str, service_auth_secret: str):
        self.base_url = base_url.rstrip("/")
        self.service_auth_secret = service_auth_secret

    def _request(self, method: str, path: str, payload: dict | None = None, *, timeout: float = 30.0) -> tuple[int, dict]:
        data = json.dumps(payload).encode("utf-8") if payload is not None else None
        request = urllib.request.Request(
            f"{self.base_url}{path}",
            data=data,
            method=method,
            headers={
                "Authorization": f"Bearer {self.service_auth_secret}",
                "Content-Type": "application/json",
            },
        )
        started = time.monotonic()
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                body = response.read().decode("utf-8")
                status_code = response.status
        except urllib.error.HTTPError as error:
            body = error.read().decode("utf-8", errors="replace")
            status_code = error.code
        except urllib.error.URLError as error:
            raise RuntimeError(f"knowledge service unreachable: {error}") from error
        elapsed = time.monotonic() - started
        parsed = json.loads(body) if body else {}
        return status_code, parsed, elapsed


def _result_triple(service, method, path, payload=None, timeout=30.0):
    status_code, parsed, elapsed = service._request(method, path, payload, timeout=timeout)
    return status_code, parsed, elapsed


def seed_corpus(service: KnowledgeService, corpus: dict, run_id: str) -> dict:
    """Create an evaluation tenant/KB and upsert every corpus document."""
    tenant, _, _ = service._request(
        "POST",
        "/api/v1/astara/tenants",
        {
            "external_system": "astara",
            "external_id": f"eval-tenant-{run_id}",
            "name": f"Evaluation Tenant {run_id}",
            "idempotency_key": f"eval-tenant-{run_id}",
        },
    )
    if tenant.get("id") is None:
        raise RuntimeError(f"tenant creation failed: {tenant}")
    knowledge_base, _, _ = service._request(
        "POST",
        "/api/v1/astara/tenants/{tenant_id}/knowledge-bases".format(tenant_id=tenant["id"]),
        {
            "external_system": "astara",
            "external_id": f"eval-kb-{run_id}",
            "name": f"Evaluation KB {run_id}",
            "idempotency_key": f"eval-kb-{run_id}",
        },
    )
    if knowledge_base.get("id") is None:
        raise RuntimeError(f"knowledge base creation failed: {knowledge_base}")
    for document in corpus["documents"]:
        service._request(
            "POST",
            "/api/v1/astara/knowledge-bases/{kb_id}/documents".format(kb_id=knowledge_base["id"]),
            {
                "external_system": "astara",
                "external_id": document["external_id"],
                "title": document["title"],
                "content": document["content"],
                "idempotency_key": f"eval-doc-{run_id}-{document['external_id']}",
            },
        )
    return {"tenant": tenant, "knowledge_base": knowledge_base}


def run_evaluation(service: KnowledgeService, corpus: dict, thresholds: dict, run_id: str) -> dict:
    seed = seed_corpus(service, corpus, run_id)
    kb_id = seed["knowledge_base"]["id"]

    retrieval_rankings = []
    answer_coverages = []
    answer_latencies = []
    search_latencies = []
    citation_hits = 0
    citation_total = 0
    no_answer_correct = 0
    no_answer_total = 0
    authorization_passes = 0
    authorization_total = 0
    permission_denials = 0
    permission_total = 0
    answered_cases = 0
    retrieval_cases = 0

    for case in corpus["cases"]:
        kind = case["kind"]
        if kind == "retrieval":
            retrieval_cases += 1
            status_code, payload, elapsed = _result_triple(
                service,
                "POST",
                "/api/v1/astara/knowledge-bases/{kb_id}/search".format(kb_id=kb_id),
                {"query": case["query"], "top_k": 5},
            )
            search_latencies.append(elapsed)
            if status_code != 200:
                continue
            ranked = [str(item.get("knowledge_id") or item.get("id") or "") for item in payload.get("results", [])]
            titles = [str(item.get("title") or "") for item in payload.get("results", [])]
            relevant = {
                document["external_id"]
                for document in corpus["documents"]
                if document["external_id"] == case.get("expect_document")
            }
            retrieval_rankings.append((ranked or titles, relevant))
        elif kind == "answer":
            status_code, payload, elapsed = _result_triple(
                service,
                "POST",
                "/api/v1/astara/knowledge-bases/{kb_id}/answer".format(kb_id=kb_id),
                {"query": case["query"]},
            )
            answer_latencies.append(elapsed)
            if status_code != 200:
                continue
            answered_cases += 1
            answer = str(payload.get("answer") or "")
            coverage = keyword_coverage(answer, case.get("answer_must_contain", []))
            answer_coverages.append(coverage)
            if forbidden_leakage(answer, case.get("answer_must_not_contain", [])) > 0:
                answer_coverages[-1] = 0.0
            if case.get("expect_citation"):
                citation_total += 1
                if payload.get("references"):
                    citation_hits += 1
        elif kind == "no_answer":
            no_answer_total += 1
            status_code, payload, _ = _result_triple(
                service,
                "POST",
                "/api/v1/astara/knowledge-bases/{kb_id}/answer".format(kb_id=kb_id),
                {"query": case["query"]},
            )
            if status_code == 200:
                answer = str(payload.get("answer") or "")
                references = payload.get("references") or []
                if not references and (not answer or len(answer) < 120):
                    no_answer_correct += 1
        elif kind in ("permission_denied", "revocation", "deleted"):
            permission_total += 1
            authorization_total += 1
            # The evaluator tenant has no grant on these documents: the query
            # must not return their content through any path.
            status_code, payload, _ = _result_triple(
                service,
                "POST",
                "/api/v1/astara/knowledge-bases/{kb_id}/search".format(kb_id=kb_id),
                {"query": case["query"], "top_k": 5},
            )
            leaked = any(
                str(item.get("title") or "").find("机密") >= 0 or str(item.get("content") or "").find("首席财务官") >= 0
                for item in (payload.get("results") if isinstance(payload, dict) else []) or []
            )
            if kind == "permission_denied" and not leaked and status_code == 200:
                permission_denials += 1
                authorization_passes += 1
            elif kind in ("revocation", "deleted") and not leaked:
                authorization_passes += 1
        elif kind == "stale_source":
            # Re-upsert the stale document with the updated content and
            # require the fresh answer to reflect it.
            for document in corpus["documents"]:
                if document["external_id"] == case.get("stale_document"):
                    service._request(
                        "POST",
                        "/api/v1/astara/knowledge-bases/{kb_id}/documents".format(kb_id=kb_id),
                        {
                            "external_system": "astara",
                            "external_id": document["external_id"],
                            "title": document["title"],
                            "content": f"{document['content']}\n\n更新条款：{case['updated_content']}",
                            "idempotency_key": f"eval-stale-{run_id}-{document['external_id']}-{case['stale_revision'] + 1}",
                        },
                    )
            status_code, payload, elapsed = _result_triple(
                service,
                "POST",
                "/api/v1/astara/knowledge-bases/{kb_id}/answer".format(kb_id=kb_id),
                {"query": case["query"]},
            )
            answer_latencies.append(elapsed)
            if status_code == 200:
                answered_cases += 1
                answer = str(payload.get("answer") or "")
                answer_coverages.append(keyword_coverage(answer, case.get("answer_must_contain", [])))
        elif kind == "citation":
            citation_total += 1
            status_code, payload, _ = _result_triple(
                service,
                "POST",
                "/api/v1/astara/knowledge-bases/{kb_id}/answer".format(kb_id=kb_id),
                {"query": case["query"]},
            )
            if status_code == 200 and payload.get("references"):
                citation_hits += 1

    observations = {
        "precision_at_5": _mean([precision_at_k(ranked, relevant, 5) for ranked, relevant in retrieval_rankings]),
        "recall_at_5": _mean([recall_at_k(ranked, relevant, 5) for ranked, relevant in retrieval_rankings]),
        "ndcg_at_5": _mean([ndcg_at_k(ranked, relevant, 5) for ranked, relevant in retrieval_rankings]),
        "mrr": mean_reciprocal_rank(retrieval_rankings),
        "map": mean_average_precision(retrieval_rankings),
        "answer_keyword_coverage": _mean(answer_coverages),
        "no_answer_correctness": (no_answer_correct / no_answer_total) if no_answer_total else None,
        "citation_accuracy": (citation_hits / citation_total) if citation_total else None,
        "authorization_pass_rate": (authorization_passes / authorization_total) if authorization_total else None,
        "permission_denial_rate": (permission_denials / permission_total) if permission_total else None,
        "latency_p95_seconds": percentile(search_latencies + answer_latencies, 95),
        "min_answered_cases": answered_cases,
        "min_retrieval_cases": retrieval_cases,
    }
    report = evaluate_gates(observations, thresholds)
    report["runId"] = run_id
    report["corpusVersion"] = corpus.get("version")
    return report


def _mean(values):
    present = [value for value in values if value is not None]
    if not present:
        return None
    return sum(present) / len(present)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://127.0.0.1:8080")
    parser.add_argument("--service-auth-secret", required=True)
    parser.add_argument("--corpus", default=str(ROOT / "evaluation" / "corpus" / "zh-enterprise-qa.v1.json"))
    parser.add_argument("--thresholds", default=str(ROOT / "evaluation" / "thresholds.v1.json"))
    parser.add_argument("--report", default=str(ROOT / "evaluation" / "evaluation-report.json"))
    parser.add_argument("--run-id", default=uuid.uuid4().hex[:12])
    args = parser.parse_args()

    corpus = json.loads(pathlib.Path(args.corpus).read_text(encoding="utf-8"))
    thresholds = load_thresholds(args.thresholds)
    service = KnowledgeService(args.base_url, args.service_auth_secret)
    report = run_evaluation(service, corpus, thresholds, args.run_id)
    write_report(report, args.report)

    for name, gate in report["gates"].items():
        print(f"{name}: {gate['state']} (observed={gate['observed']} threshold={gate['threshold']})")
    if report["passed"]:
        print(f"Evaluation gates passed: {args.report}")
        return 0
    print(f"Evaluation gates failed: {', '.join(report['failures'])}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
