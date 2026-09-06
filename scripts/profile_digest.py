#!/usr/bin/env python3
"""Canonical feature-profile digest for the closed astara-knowledge profile.

The digest binds the exact enabled and prohibited feature sets declared in
internal/astara/profile.go. Plane release admission compares this digest
against the Plane dependency manifest, so the canonicalization here must stay
byte-stable and mirror internal/astara/profile_digest.go.

Usage:
    scripts/profile_digest.py            # print canonical document and digest
    scripts/profile_digest.py --digest   # print only sha256:<hex>
    scripts/profile_digest.py --check sha256:<hex>   # exit 1 on mismatch
"""

import hashlib
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
PROFILE_GO = ROOT / "internal" / "astara" / "profile.go"

_CONST = re.compile(r'^\s*Feature([A-Za-z0-9_]+)\s+Feature\s*=\s*"([a-z0-9_]+)"', re.M)


def parse_profile(path: pathlib.Path):
    text = path.read_text(encoding="utf-8")
    constants = {m.group(1): m.group(2) for m in _CONST.finditer(text)}
    if not constants:
        raise SystemExit(f"profile constants not found in {path}")

    def block(braced_var: str):
        marker = re.search(re.escape(braced_var) + r"\s*=\s*(?:map\[Feature\]struct\{\}|\[\]Feature)\s*\{(.*?)\n\}", text, re.S)
        if not marker:
            raise SystemExit(f"{braced_var} block not found in {path}")
        return {
            constants[name]
            for name in re.findall(r"\bFeature([A-Za-z0-9_]+)\b", marker.group(1))
        }

    enabled = block("var knowledgeFeatures")
    prohibited = block("var prohibitedFeatures")
    return enabled, prohibited


def canonical_document(enabled, prohibited) -> dict:
    return {
        "name": "astara-knowledge",
        "enabled": sorted(enabled),
        "prohibited": sorted(prohibited),
    }


def canonical_bytes(enabled, prohibited) -> bytes:
    return json.dumps(
        canonical_document(enabled, prohibited), separators=(",", ":"), sort_keys=True
    ).encode("utf-8")


def digest(enabled, prohibited) -> str:
    return "sha256:" + hashlib.sha256(canonical_bytes(enabled, prohibited)).hexdigest()


def main() -> int:
    enabled, prohibited = parse_profile(PROFILE_GO)
    value = digest(enabled, prohibited)
    if "--digest" in sys.argv:
        print(value)
        return 0
    if "--check" in sys.argv:
        wanted = sys.argv[sys.argv.index("--check") + 1]
        if value != wanted:
            print(f"profile digest mismatch: computed {value}, wanted {wanted}", file=sys.stderr)
            return 1
        print(f"profile digest matches: {value}")
        return 0
    print(json.dumps(canonical_document(enabled, prohibited), indent=2, sort_keys=True))
    print(value)
    return 0


if __name__ == "__main__":
    sys.exit(main())
