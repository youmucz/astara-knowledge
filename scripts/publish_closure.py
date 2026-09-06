#!/usr/bin/env python3
"""Publish machine-readable closure evidence for one Astara Knowledge release.

Run by the tag-build CI (astara-release.yml) after images are built, SBOM and
provenance attached, and cosign signatures written. The emitted file is the
only artifact Plane release admission consumes for registry-bound evidence;
it is never hand-edited.

Inputs are passed as a single JSON document on stdin (see --help for shape);
signature and SBOM manifest digests are resolved from the registry when
possible and recorded as pending otherwise (admission fails closed on
pending).
"""

import argparse
import json
import subprocess
import sys

EXPECTED_INPUT = {
    "release": {"version", "upstream_baseline", "upstream_commit", "feature_profile"},
    "source": {"revision", "workflow_ref"},
    "images": set(),
}


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

    evidence = {
        "schemaVersion": 1,
        "release": raw["release"],
        "source": raw["source"],
        "images": {},
    }
    for name, image in sorted(images.items()):
        if not isinstance(image, dict) or "ref" not in image or "digest" not in image:
            print(f"image {name} requires ref and digest", file=sys.stderr)
            return 2
        signature_ref = f"{image['ref'].split('@')[0].split(':')[0]}@sha256-{image['digest'].split(':', 1)[1]}.sig"
        sbom_ref = f"{image['ref'].split('@')[0].split(':')[0]}@sha256-{image['digest'].split(':', 1)[1]}.att"
        evidence["images"][name] = {
            "ref": image["ref"],
            "digest": image["digest"],
            "signature": {"ref": signature_ref, "digest": resolve_manifest_digest(signature_ref) or "pending"},
            "sbom": {"ref": sbom_ref, "digest": resolve_manifest_digest(sbom_ref) or "pending"},
        }

    with open(args.output, "w", encoding="utf-8") as handle:
        json.dump(evidence, handle, indent=2, sort_keys=True)
        handle.write("\n")
    print(f"closure evidence written: {args.output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
