#!/usr/bin/env python3
"""Publish machine-readable closure evidence for one Astara Knowledge release.

Run by the tag-build CI (astara-release.yml) after images are built, SBOM and
provenance attached, and cosign signatures written. The emitted file is the
only artifact Plane release admission consumes for registry-bound evidence;
it is never hand-edited.

Inputs are passed as a single JSON document on stdin (see --help for shape);
signature and SBOM manifest digests are resolved from the registry and missing
immutable evidence fails closed before the closure file is written.
"""

import argparse
import json
import re
import subprocess
import sys

EXPECTED_INPUT = {
    "release": {"version", "upstream_baseline", "upstream_commit", "feature_profile"},
    "source": {"revision", "workflow_ref"},
    "images": set(),
}

VERSION_RE = re.compile(r"^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$")
BASELINE_RE = re.compile(r"^v\d+\.\d+\.\d+$")
SHA256_RE = re.compile(r"^sha256:[a-f0-9]{64}$")
COMMIT_RE = re.compile(r"^[a-f0-9]{40}$")


def resolve_manifest_digest(ref: str) -> str:
    try:
        output = subprocess.run(
            ["docker", "buildx", "imagetools", "inspect", "--format", "{{.Manifest.Digest}}", ref],
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return ""
    return output if output.startswith("sha256:") else ""


def _invalid(where: str, what: str) -> int:
    print(f"closure inputs {where}: {what}", file=sys.stderr)
    return 2


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", default="closure-evidence.json")
    args = parser.parse_args()

    raw = json.loads(sys.stdin.read())
    missing = EXPECTED_INPUT["release"] - set(raw.get("release", {}))
    if missing:
        print(f"closure inputs missing release fields: {sorted(missing)}", file=sys.stderr)
        return 2
    missing = EXPECTED_INPUT["source"] - set(raw.get("source", {}))
    if missing:
        print(f"closure inputs missing source fields: {sorted(missing)}", file=sys.stderr)
        return 2
    images = raw.get("images")
    if not isinstance(images, dict) or not images:
        print("closure inputs require an images object", file=sys.stderr)
        return 2

    release = raw["release"]
    if not VERSION_RE.match(release["version"]):
        return _invalid("release.version", "must be a concrete version, not a mutable tag")
    if not BASELINE_RE.match(release["upstream_baseline"]):
        return _invalid("release.upstream_baseline", "must be a pinned upstream tag")
    if not COMMIT_RE.match(release["upstream_commit"]):
        return _invalid("release.upstream_commit", "must be a 40-char upstream commit")
    if release["feature_profile"] != "astara-knowledge":
        return _invalid("release.feature_profile", "must be astara-knowledge")
    source = raw["source"]
    if not COMMIT_RE.match(source["revision"]):
        return _invalid("source.revision", "must be a 40-char build revision")
    if not source["workflow_ref"] or any(character in source["workflow_ref"] for character in "\r\n"):
        return _invalid("source.workflow_ref", "must be a bounded non-empty reference")

    evidence = {
        "schemaVersion": 1,
        "release": release,
        "source": source,
        "images": {},
    }
    for name, image in sorted(images.items()):
        if not isinstance(image, dict) or "ref" not in image or "digest" not in image:
            print(f"image {name} requires ref and digest", file=sys.stderr)
            return 2
        if not SHA256_RE.match(image["digest"]):
            return _invalid(f"images.{name}.digest", "must be an immutable sha256 manifest digest")
        ref_sha = image["ref"].rsplit("@", 1)[-1] if "@" in image["ref"] else ""
        if ref_sha != image["digest"]:
            return _invalid(f"images.{name}.ref", "must be digest-pinned to the recorded digest")
        signature_ref = f"{image['ref'].split('@')[0].split(':')[0]}@sha256-{image['digest'].split(':', 1)[1]}.sig"
        sbom_ref = f"{image['ref'].split('@')[0].split(':')[0]}@sha256-{image['digest'].split(':', 1)[1]}.att"
        signature_digest = resolve_manifest_digest(signature_ref)
        sbom_digest = resolve_manifest_digest(sbom_ref)
        if not signature_digest or not sbom_digest:
            print(f"image {name} is missing immutable signature/SBOM digest", file=sys.stderr)
            return 2
        evidence["images"][name] = {
            "ref": image["ref"],
            "digest": image["digest"],
            "signature": {"ref": signature_ref, "digest": signature_digest},
            "sbom": {"ref": sbom_ref, "digest": sbom_digest},
        }

    with open(args.output, "w", encoding="utf-8") as handle:
        json.dump(evidence, handle, indent=2, sort_keys=True)
        handle.write("\n")
    print(f"closure evidence written: {args.output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
