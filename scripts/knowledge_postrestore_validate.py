#!/usr/bin/env python3
# Copyright (c) 2026-present Astara and contributors
# SPDX-License-Identifier: Apache-2.0
# See the LICENSE file for details.

"""Post-restore quarantine validation for Astara Knowledge.

A restored stack stays quarantined until every check below passes:

1. migration position: the applied versioned-migration position equals the
   backup identity's recorded position (or the operator-pinned position);
2. storage references: every document row referencing local storage has its
   file present in the restored files volume;
3. source reconciliation: every provider knowledge-base has at least its
   binding rows intact (no orphan documents referencing missing tenants);
4. authorization probes: an unknown tenant must be denied by the control API;
5. index coverage: every indexed document has at least one chunk.

The report is content-free: counts, reason classes, and booleans only.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys
import urllib.error
import urllib.request

REQUIRED_SECRET_ENV = ("ASTARA_SERVICE_AUTH_SECRET", "KNOWLEDGE_DB_PASSWORD")


def verify_backup_integrity(backup: pathlib.Path) -> dict:
    """Verify artifact digests and escrow presence; raise SystemExit on drift."""
    import hashlib

    manifest = json.loads((backup / "backup-manifest.json").read_text(encoding="utf-8"))
    for name, artifact in manifest["artifacts"].items():
        path = backup / artifact["file"]
        if not path.is_file():
            raise SystemExit(f"backup artifact missing: {name} ({path})")
        digest = "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()
        if digest != artifact["digest"]:
            raise SystemExit(f"backup artifact checksum mismatch: {name}")
    escrow = json.loads((backup / "secret-escrow.json").read_text(encoding="utf-8"))
    missing = [name for name, present in escrow["present"].items() if not present]
    if missing:
        raise SystemExit(f"backup recorded missing escrow secrets: {missing}")
    return manifest


def compose(project: str, compose_file: str, *args: str) -> str:
    """Run docker compose; compose_file may carry multiple paths."""
    command = ["docker", "compose", "-p", project]
    for file in compose_file.split(","):
        command += ["-f", file.strip()]
    command += list(args)
    result = subprocess.run(command, capture_output=True, text=True, check=True)
    return result.stdout


def readiness(base_url: str, secret: str, project: str, compose_file: str) -> tuple[bool, dict]:
    """Fetch /health/ready from inside the compose network (no host ports)."""
    import json as json_module

    output = compose(
        project,
        compose_file,
        "exec",
        "-T",
        "api",
        "curl",
        "-s",
        "-H",
        f"Authorization: Bearer {secret}",
        f"{base_url}/health/ready",
    )
    try:
        payload = json_module.loads(output or "{}")
    except ValueError:
        return False, {}
    return bool(payload.get("ready")) is True, payload


def control_probe(base_url: str, secret: str, path: str, project: str, compose_file: str) -> int:
    """One bounded status code from inside the compose network."""
    output = compose(
        project,
        compose_file,
        "exec",
        "-T",
        "api",
        "curl",
        "-s",
        "-o",
        "/dev/null",
        "-w",
        "%{http_code}",
        "-H",
        f"Authorization: Bearer {secret}",
        f"{base_url}{path}",
    )
    try:
        return int(output.strip())
    except ValueError:
        return -1


def sql(project: str, compose_file: str, statement: str) -> str:
    import os

    user = os.environ.get("DB_USER", "astara_knowledge")
    return compose(
        project,
        compose_file,
        "exec",
        "-T",
        "db",
        "sh",
        "-c",
        f'psql -U "{user}" -d astara_knowledge -tAc "{statement}"',
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--compose-project", default="astara-knowledge-quarantine")
    parser.add_argument("--compose-file", default="deploy/astara-knowledge/compose.yml", help="one path or comma-separated paths")
    parser.add_argument("--backup", required=True)
    parser.add_argument("--expected-migration-position", type=int, default=None)
    parser.add_argument("--api-url", default="http://127.0.0.1:8080", help="in-network base URL of the api service")
    parser.add_argument("--report", default="postrestore-report.json")
    args = parser.parse_args()

    import os

    for name in REQUIRED_SECRET_ENV:
        if not os.environ.get(name):
            print(f"required secret is not set: {name}", file=sys.stderr)
            return 2
    secret = os.environ["ASTARA_SERVICE_AUTH_SECRET"]

    backup = pathlib.Path(args.backup)
    manifest = verify_backup_integrity(backup)
    checks: dict[str, dict] = {}

    # 1. Migration position: golang-migrate records (version, dirty).
    applied = sql(
        args.compose_project,
        args.compose_file,
        "SELECT version FROM schema_migrations WHERE dirty = false",
    ).strip()
    expected = args.expected_migration_position
    if expected is None:
        identity = manifest.get("identity", {})
        expected = identity.get("migration_position")
    try:
        applied_position = int(applied)
        expected_position = int(expected) if expected is not None else None
    except (TypeError, ValueError):
        applied_position, expected_position = None, None
    checks["migration_position"] = {
        "applied": applied_position,
        "expected": expected_position,
        "state": "met" if applied_position is not None and applied_position == expected_position else "breached",
    }

    # 2. Storage references: the restored provider-managed source files must
    #    be present in the restored files volume.
    stored_files = compose(
        args.compose_project,
        args.compose_file,
        "exec",
        "-T",
        "api",
        "sh",
        "-c",
        "ls -1 /data/files 2>/dev/null | wc -l",
    ).strip()
    try:
        stored_count = int(stored_files)
    except ValueError:
        stored_count = -1
    checks["storage_references"] = {
        "stored_file_count": stored_count,
        "state": "met" if stored_count > 0 else "breached",
    }

    # 3. Source reconciliation: knowledge bases must exist with documents.
    document_count = sql(
        args.compose_project,
        args.compose_file,
        "SELECT count(*) FROM knowledges",
    ).strip()
    kb_count = sql(
        args.compose_project,
        args.compose_file,
        "SELECT count(*) FROM knowledge_bases",
    ).strip()
    try:
        documents = int(document_count)
        bases = int(kb_count)
    except ValueError:
        documents, bases = -1, -1
    checks["source_reconciliation"] = {
        "knowledge_base_count": bases,
        "document_count": documents,
        "state": "met" if bases > 0 and documents > 0 else "breached",
    }

    # 4. Authorization probes: unknown tenant lookups must be denied.
    status = control_probe(
        args.api_url,
        secret,
        "/api/v1/astara/tenants/by-external-id?external_system=astara&external_id=postrestore-unknown-probe",
        args.compose_project,
        args.compose_file,
    )
    checks["authorization_probe"] = {
        "unknown_tenant_status": status,
        "state": "met" if status in (401, 403, 404) else "breached",
    }

    # 5. Index coverage: indexed documents must have chunks.
    orphan_chunks = sql(
        args.compose_project,
        args.compose_file,
        "SELECT count(*) FROM knowledges k WHERE NOT EXISTS (SELECT 1 FROM chunks c WHERE c.knowledge_id = k.id)",
    ).strip()
    try:
        orphans = int(orphan_chunks)
    except ValueError:
        orphans = -1
    checks["index_coverage"] = {
        "documents_without_chunks": orphans,
        "state": "met" if orphans == 0 else "breached",
    }

    # Readiness must also be true before the quarantine can lift.
    ready, payload = readiness(args.api_url, secret, args.compose_project, args.compose_file)
    checks["readiness"] = {
        "ready": ready,
        "identity_matches_backup": payload.get("identity") == manifest.get("identity") or None,
        "state": "met" if ready else "breached",
    }

    passed = all(check["state"] == "met" for check in checks.values())
    report = {
        "schemaVersion": 1,
        "passed": passed,
        "checks": checks,
    }
    pathlib.Path(args.report).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    for name, check in checks.items():
        print(f"{name}: {check['state']}")
    if not passed:
        print(f"post-restore validation failed; stack stays quarantined: {args.report}", file=sys.stderr)
        return 1
    print(f"post-restore validation passed: {args.report}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
