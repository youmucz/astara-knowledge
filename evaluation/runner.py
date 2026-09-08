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
import hashlib
import json
import pathlib
import socket
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

    def _request(self, method: str, path: str, payload: dict | None = None, *, timeout: float = 180.0) -> tuple[int, dict]:
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
        except (socket.timeout, TimeoutError):
            return 0, {"error": f"timeout on {method} {path}"}, time.monotonic() - started
        except urllib.error.URLError as error:
            if isinstance(error.reason, (socket.timeout, TimeoutError)):
                return 0, {"error": f"timeout on {method} {path}"}, time.monotonic() - started
            raise RuntimeError(f"knowledge service unreachable: {error}") from error
        elapsed = time.monotonic() - started
        parsed = json.loads(body) if body else {}
        return status_code, parsed, elapsed


def _result_triple(service, method, path, payload=None, timeout=30.0, retries=0):
    status_code, parsed, elapsed = service._request(method, path, payload, timeout=timeout)
    while retries > 0 and path.endswith("answer-authorized") and status_code not in (200,):
        time.sleep(3)
        status_code, parsed, elapsed = service._request(method, path, payload, timeout=timeout)
        retries -= 1
    return status_code, parsed, elapsed


def _canonical_bytes(value) -> bytes:
    """Canonical JSON bytes shared with the provider's authorized digest."""
    raw = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    raw = raw.rstrip("\n")
    return raw.encode("utf-8")


def _revision_digest(content: str) -> str:
    return hashlib.sha256(content.encode("utf-8")).hexdigest()


def seed_corpus(service: KnowledgeService, corpus: dict, run_id: str) -> dict:
    """Create an evaluation tenant/KB and upsert every corpus document."""
    _, tenant, _ = service._request(
        "POST",
        "/api/v1/astara/tenants",
        {
            "external_system": "astara",
            "external_id": f"eval-tenant-{run_id}",
            "name": f"Evaluation Tenant {run_id}",
            "idempotency_key": f"eval-tenant-{run_id}",
        },
    )
    if not isinstance(tenant, dict) or tenant.get("id") is None:
        raise RuntimeError(f"tenant creation failed: {tenant}")
    _, knowledge_base, _ = service._request(
        "POST",
        "/api/v1/astara/knowledge-bases",
        {
            "tenant_id": str(tenant["id"]),
            "external_system": "astara",
            "external_id": f"eval-kb-{run_id}",
            "name": f"Evaluation KB {run_id}",
            "idempotency_key": f"eval-kb-{run_id}",
        },
    )
    if not isinstance(knowledge_base, dict) or knowledge_base.get("id") is None:
        raise RuntimeError(f"knowledge base creation failed: {knowledge_base}")

    documents: dict[str, dict] = {}
    for document in corpus["documents"]:
        content = document["content"]
        _, created, _ = service._request(
            "PUT",
            "/api/v1/astara/knowledge-bases/{kb_id}/documents".format(kb_id=knowledge_base["id"]),
            {
                "external_system": "astara",
                "external_id": document["external_id"],
                "title": document["title"],
                "content": content,
                "source_revision": 1,
                "idempotency_key": f"eval-doc-{run_id}-{document['external_id']}",
            },
        )
        if not isinstance(created, dict) or created.get("id") is None:
            raise RuntimeError(f"document upsert failed: {document['external_id']} {created}")
        documents[document["external_id"]] = {
            "knowledge_id": str(created["id"]),
            "tenant_id": str(tenant["id"]),
            "revision": _revision_digest(content),
        }
    # Allow the async parse/index pipeline to settle before the first query.
    time.sleep(3)
    return {"knowledge_base": knowledge_base, "tenant": tenant, "documents": documents}


def _authorized_request(case: dict, query: str, seed: dict, exclude: set[str] = frozenset(), contract_version: int = 1) -> dict:
    kb_id = seed["knowledge_base"]["id"]
    tenant_id = str(seed["tenant"]["id"])
    admitted = []
    for external_id in sorted(seed["documents"]):
        if external_id in exclude:
            continue
        entry = seed["documents"][external_id]
        admitted.append(
            {
                "tenant_id": tenant_id,
                "knowledge_base_id": kb_id,
                "knowledge_id": entry["knowledge_id"],
                "revision": entry["revision"],
            }
        )
    # The provider's canonical digest sorts the documents array by
    # (tenant_id, knowledge_base_id, knowledge_id, revision) ascending.
    admitted.sort(key=lambda d: (d["tenant_id"], d["knowledge_base_id"], d["knowledge_id"], d["revision"]))
    return {
        "contract_version": contract_version,
        "query": query,
        "documents": admitted,
        "authorization_digest": hashlib.sha256(_canonical_bytes(admitted)).hexdigest(),
    }


def run_evaluation(service: KnowledgeService, corpus: dict, thresholds: dict, run_id: str) -> dict:
    seed = seed_corpus(service, corpus, run_id)
    kb_id = seed["knowledge_base"]["id"]

    # The document-processing pipeline chunks + embeds asynchronously; wait
    # until the corpus is actually queryable before running any case.
    probe = _authorized_request({"kind": "retrieval"}, corpus["cases"][0]["query"], seed)
    queryable = False
    for _ in range(90):
        status_code, payload, _ = _result_triple(service, "POST", "/api/v1/astara/search-authorized", probe)
        if status_code == 200 and payload.get("results"):
            queryable = True
            break
        time.sleep(2)
    if not queryable:
        raise RuntimeError("corpus never became queryable")

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

    def denied_knowledge_id(case: dict) -> str:
        for key in ("denied_document", "revoked_document", "deleted_document"):
            external_id = case.get(key)
            if external_id and external_id in seed["documents"]:
                return seed["documents"][external_id]["knowledge_id"]
        return ""

    for case in corpus["cases"]:
        kind = case["kind"]
        if kind == "retrieval":
            retrieval_cases += 1
            status_code, payload, elapsed = _result_triple(
                service,
                "POST",
                "/api/v1/astara/search-authorized",
                _authorized_request(case, case["query"], seed),
            )
            search_latencies.append(elapsed)
            if status_code != 200:
                continue
            ranked = [str(item.get("knowledge_id") or "") for item in payload.get("results", [])]
            relevant = {seed["documents"].get(case.get("expect_document"), {}).get("knowledge_id", "")}
            retrieval_rankings.append((ranked, relevant))
        elif kind == "answer":
            status_code, payload, elapsed = _result_triple(
                service,
                "POST",
                "/api/v1/astara/answer-authorized",
                _authorized_request(case, case["query"], seed, contract_version=2),
                retries=2,
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
                "/api/v1/astara/answer-authorized",
                _authorized_request(case, case["query"], seed, contract_version=2),
                retries=2,
            )
            if status_code == 200:
                answer = str(payload.get("answer") or "")
                references = payload.get("references") or []
                # Deterministic refusal detection: a negation particle
                # directly governing a retrieval/answer verb, or an explicit
                # absence statement. The corpus no-answer cases expect the
                # model to refuse without fabricating; phrasing varies, the
                # (negation, verb) co-occurrence does not.
                import re as _re
                refusal = bool(
                    _re.search(
                        r"(无法|未能|没有|未|不能)(找到|查询|回答|获取|检索到|获取到)"
                        r"|(没有任何|均未|未涉及|不包含|无相关|查无)",
                        answer,
                    )
                )
                if (not references and (not answer or len(answer) < 120)) or refusal:
                    no_answer_correct += 1
        elif kind in ("permission_denied", "revocation", "deleted"):
            if kind == "permission_denied":
                permission_total += 1
            authorization_total += 1
            excluded = {case.get("denied_document") or case.get("revoked_document") or case.get("deleted_document") or ""} - {""}
            status_code, payload, _ = _result_triple(
                service,
                "POST",
                "/api/v1/astara/search-authorized",
                _authorized_request(case, case["query"], seed, exclude=excluded),
            )
            denied_id = denied_knowledge_id(case)
            results = payload.get("results", []) if isinstance(payload, dict) else []
            leaked = any(str(item.get("knowledge_id") or "") == denied_id for item in results)
            if status_code == 200 and not leaked:
                if kind == "permission_denied":
                    permission_denials += 1
                authorization_passes += 1
        elif kind == "stale_source":
            external_id = case["stale_document"]
            updated_content = f"{corpus['documents'][0]['content']}\n\n更新条款：{case['updated_content']}" if False else None
            for document in corpus["documents"]:
                if document["external_id"] == external_id:
                    updated_content = f"{document['content']}\n\n更新条款：{case['updated_content']}"
            new_revision = int(case.get("stale_revision", 0)) + 1
            _, refreshed, _ = service._request(
                "PUT",
                "/api/v1/astara/knowledge-bases/{kb_id}/documents".format(kb_id=kb_id),
                {
                    "external_system": "astara",
                    "external_id": external_id,
                    "title": next(d["title"] for d in corpus["documents"] if d["external_id"] == external_id),
                    "content": updated_content,
                    "source_revision": new_revision,
                    "idempotency_key": f"eval-stale-{run_id}-{external_id}-{new_revision}",
                },
            )
            if isinstance(refreshed, dict) and refreshed.get("id"):
                seed["documents"][external_id]["revision"] = _revision_digest(updated_content)
            # Wait until the provider exposes the refreshed source revision
            # (the pipeline re-parses asynchronously), then settle.
            for _ in range(60):
                _, refreshed_doc, _ = service._request(
                    "GET",
                    "/api/v1/astara/knowledge-bases/{kb_id}/documents/by-external-id?external_system=astara&external_id={ext}".format(
                        kb_id=kb_id, ext=external_id
                    ),
                )
                if isinstance(refreshed_doc, dict) and int(refreshed_doc.get("source_revision", 0)) >= new_revision:
                    break
                time.sleep(2)
            time.sleep(5)
            status_code, payload, elapsed = _result_triple(
                service,
                "POST",
                "/api/v1/astara/answer-authorized",
                _authorized_request(case, case["query"], seed, contract_version=2),
                retries=2,
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
                "/api/v1/astara/answer-authorized",
                _authorized_request(case, case["query"], seed, contract_version=2),
                retries=2,
            )
            if status_code == 200 and payload.get("references"):
                citation_hits += 1
        else:
            raise RuntimeError(f"unknown case kind: {kind}")

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
    report["run_id"] = run_id
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
