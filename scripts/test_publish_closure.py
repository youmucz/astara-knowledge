#!/usr/bin/env python3
"""Unit tests for publish_closure.py fail-closed evidence assembly.

These tests never touch Docker or a registry: resolve_manifest_digest is
replaced with a deterministic fake, so missing/immutable-evidence refusals
are proven without publishing anything.
"""

import io
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import publish_closure  # noqa: E402


DIGEST = "sha256:" + "a" * 64
SIG_DIGEST = "sha256:" + "b" * 64
SBOM_DIGEST = "sha256:" + "c" * 64


def inputs(**overrides):
    base = {
        "release": {
            "version": "0.1.0-astara.1",
            "upstream_baseline": "v0.8.2",
            "upstream_commit": "1" * 40,
            "feature_profile": "astara-knowledge",
        },
        "source": {"revision": "2" * 40, "workflow_ref": "refs/tags/v0.1.0-astara.1"},
        "images": {
            "api": {
                "ref": f"ghcr.io/youmucz/astara-knowledge-api:0.1.0-astara.1@{DIGEST}",
                "digest": DIGEST,
            }
        },
    }
    for key, value in overrides.items():
        if isinstance(value, dict) and isinstance(base.get(key), dict):
            base[key] = {**base[key], **value}
        else:
            base[key] = value
    return base


class PublishClosureTest(unittest.TestCase):
    def setUp(self):
        self._saved_resolver = publish_closure.resolve_manifest_digest
        publish_closure.resolve_manifest_digest = lambda ref: {
            "sig": SIG_DIGEST,
            "att": SBOM_DIGEST,
        }.get(ref.rsplit(".")[-1], "")

    def tearDown(self):
        publish_closure.resolve_manifest_digest = self._saved_resolver

    def run_main(self, payload, output_path):
        with tempfile.TemporaryDirectory() as tmp:
            output = output_path or os.path.join(tmp, "closure-evidence.json")
            argv = ["publish_closure.py", "--output", output]
            old_argv, old_stdin = sys.argv, sys.stdin
            sys.argv, sys.stdin = argv, io.StringIO(json.dumps(payload))
            try:
                with redirect_stderr(io.StringIO()) as stderr:
                    code = publish_closure.main()
            finally:
                sys.argv, sys.stdin = old_argv, old_stdin
            content = Path(output).read_text() if Path(output).exists() else None
            return code, content, stderr.getvalue()

    def test_complete_closure_is_written(self):
        code, content, _ = self.run_main(inputs(), None)
        self.assertEqual(code, 0)
        evidence = json.loads(content)
        self.assertEqual(evidence["images"]["api"]["signature"]["digest"], SIG_DIGEST)
        self.assertEqual(evidence["images"]["api"]["sbom"]["digest"], SBOM_DIGEST)

    def test_missing_signature_digest_fails_closed_without_output(self):
        publish_closure.resolve_manifest_digest = lambda ref: SBOM_DIGEST if ref.endswith(".att") else ""
        code, content, _ = self.run_main(inputs(), None)
        self.assertEqual(code, 2)
        self.assertIsNone(content)

    def test_missing_sbom_digest_fails_closed_without_output(self):
        publish_closure.resolve_manifest_digest = lambda ref: SIG_DIGEST if ref.endswith(".sig") else ""
        code, content, _ = self.run_main(inputs(), None)
        self.assertEqual(code, 2)
        self.assertIsNone(content)

    def test_wrong_upstream_commit_rejected(self):
        code, _, stderr = self.run_main(inputs(release={"upstream_commit": "v0.8.0"}), None)
        self.assertEqual(code, 2)
        self.assertIn("upstream_commit", stderr)

    def test_wrong_source_revision_rejected(self):
        code, _, stderr = self.run_main(inputs(source={"revision": "refs/tags/v1"}), None)
        self.assertEqual(code, 2)
        self.assertIn("source.revision", stderr)

    def test_mutable_tag_as_version_rejected(self):
        code, _, stderr = self.run_main(inputs(release={"version": "latest"}), None)
        self.assertEqual(code, 2)
        self.assertIn("release.version", stderr)

    def test_mutable_image_digest_rejected(self):
        code, _, stderr = self.run_main(
            inputs(images={"api": {"ref": "ghcr.io/youmucz/astara-knowledge-api:latest", "digest": "not-a-digest"}}),
            None,
        )
        self.assertEqual(code, 2)
        self.assertIn("digest", stderr)

    def test_unpinned_ref_rejected(self):
        code, _, stderr = self.run_main(
            inputs(images={"api": {"ref": "ghcr.io/youmucz/astara-knowledge-api:0.1.0-astara.1", "digest": DIGEST}}),
            None,
        )
        self.assertEqual(code, 2)
        self.assertIn("digest-pinned", stderr)


if __name__ == "__main__":
    unittest.main()
