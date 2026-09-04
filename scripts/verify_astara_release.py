#!/usr/bin/env python3
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
EXPECTED_TOP = {"schema_version", "implementation_version", "upstream_baseline", "upstream_commit", "feature_profile", "contracts", "images"}
EXPECTED_CONTRACTS = {"api": 1, "ui": 1, "source": 1, "tool": 1, "readiness": 1, "migration": 1}
EXPECTED_IMAGES = {
    "api": "ghcr.io/youmucz/astara-knowledge-api:0.1.0-astara.1",
    "web": "ghcr.io/youmucz/astara-knowledge-web:0.1.0-astara.1",
    "docreader": "ghcr.io/youmucz/astara-knowledge-docreader:0.1.0-astara.1",
}

def validate(path: pathlib.Path) -> None:
    value = json.loads(path.read_text())
    assert set(value) == EXPECTED_TOP, f"unknown/missing fields: {set(value) ^ EXPECTED_TOP}"
    assert value["schema_version"] == 1
    assert value["implementation_version"] == "0.1.0-astara.1"
    assert value["upstream_baseline"] == "v0.8.0"
    assert value["upstream_commit"] == "1edcd54b43606d9079bb36650efe3f68707a79ea"
    assert value["feature_profile"] == "astara-knowledge"
    assert value["contracts"] == EXPECTED_CONTRACTS
    assert value["images"] == EXPECTED_IMAGES

def main() -> int:
    validate(ROOT / "release" / "manifest.json")
    validate(ROOT / "release" / "fixtures" / "compatible.json")
    for path in sorted((ROOT / "release" / "fixtures").glob("incompatible-*.json")):
        try:
            validate(path)
        except (AssertionError, KeyError, TypeError, ValueError):
            continue
        raise AssertionError(f"incompatible fixture unexpectedly accepted: {path}")
    return 0

if __name__ == "__main__":
    sys.exit(main())
