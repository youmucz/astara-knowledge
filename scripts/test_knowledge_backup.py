# Copyright (c) 2026-present Astara and contributors
# SPDX-License-Identifier: Apache-2.0
# See the LICENSE file for details.

"""Unit tests for Knowledge backup integrity and post-restore validation."""

import hashlib
import json
import pathlib
import tempfile
import unittest
from typing import List, Optional

import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "scripts"))

from knowledge_postrestore_validate import verify_backup_integrity  # noqa: E402


def _write_backup(
    root: pathlib.Path,
    *,
    escrow_missing: Optional[List[str]] = None,
    corrupt: Optional[str] = None,
) -> None:
    artifacts = {}
    for name, filename in (
        ("database", "knowledge-db.dump"),
        ("source_files", "knowledge-files.tar"),
        ("storage_mapping", "storage-mapping.json"),
        ("secret_escrow", "secret-escrow.json"),
    ):
        content = json.dumps({"artifact": name}).encode("utf-8")
        if filename == "secret-escrow.json":
            present = {
                "ASTARA_SERVICE_AUTH_SECRET": True,
                "ASTARA_IDENTITY_EXCHANGE_SECRET": True,
                "SYSTEM_AES_KEY": True,
                "DB_PASSWORD": True,
            }
            for missing in escrow_missing or []:
                present[missing] = False
            content = json.dumps({"schemaVersion": 1, "required": sorted(present), "present": present}).encode("utf-8")
        (root / filename).write_bytes(content)
        digest = "sha256:" + hashlib.sha256((root / filename).read_bytes()).hexdigest()
        artifacts[name] = {"file": filename, "digest": digest}
    if corrupt:
        (root / corrupt).write_bytes(b"tampered")
    manifest = {
        "schemaVersion": 1,
        "kind": "astara-knowledge-backup",
        "artifacts": artifacts,
        "identity": {"implementation_version": "0.1.0-astara.1", "migration_position": 93},
    }
    (root / "backup-manifest.json").write_text(json.dumps(manifest, indent=2))


class BackupIntegrityTests(unittest.TestCase):
    def test_valid_backup_passes_and_returns_identity(self):
        with tempfile.TemporaryDirectory() as name:
            root = pathlib.Path(name)
            _write_backup(root)
            manifest = verify_backup_integrity(root)
            self.assertEqual(manifest["identity"]["migration_position"], 93)

    def test_missing_artifact_fails_closed(self):
        with tempfile.TemporaryDirectory() as name:
            root = pathlib.Path(name)
            _write_backup(root)
            (root / "knowledge-files.tar").unlink()
            with self.assertRaises(SystemExit) as raised:
                verify_backup_integrity(root)
            self.assertIn("missing", str(raised.exception))

    def test_tampered_artifact_fails_closed(self):
        with tempfile.TemporaryDirectory() as name:
            root = pathlib.Path(name)
            _write_backup(root, corrupt="knowledge-db.dump")
            with self.assertRaises(SystemExit) as raised:
                verify_backup_integrity(root)
            self.assertIn("checksum mismatch", str(raised.exception))

    def test_missing_escrow_secret_fails_closed(self):
        with tempfile.TemporaryDirectory() as name:
            root = pathlib.Path(name)
            _write_backup(root, escrow_missing=["SYSTEM_AES_KEY"])
            with self.assertRaises(SystemExit) as raised:
                verify_backup_integrity(root)
            self.assertIn("missing escrow secrets", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
