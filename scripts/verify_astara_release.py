#!/usr/bin/env python3
import json
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
EXPECTED_TOP = {"schema_version", "implementation_version", "upstream_baseline", "upstream_commit", "feature_profile", "contracts", "images"}
EXPECTED_CONTRACTS = {"api": 1, "ui": 1, "source": 1, "tool": 1, "readiness": 1, "migration": 1}
EXPECTED_IMAGES = {
    "api": "ghcr.io/youmucz/astara-knowledge-api:0.1.0-astara.1",
    "web": "ghcr.io/youmucz/astara-knowledge-web:0.1.0-astara.1",
    "docreader": "ghcr.io/youmucz/astara-knowledge-docreader:0.1.0-astara.1",
}
# Canonical closed-feature profile digest (scripts/profile_digest.py).
EXPECTED_PROFILE_DIGEST = "sha256:0529ddfd1d057a32c5978c5cc027bf2d6c67c6fa24eba0c4f9df0166ecd12779"
CONTRACT_DOCS = ("readiness.v1.json", "source.v1.json", "ui.v1.json")


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


def validate_contract_docs() -> None:
    required = {"name", "version", "description"}
    for name in CONTRACT_DOCS:
        path = ROOT / "release" / "contracts" / name
        value = json.loads(path.read_text())
        assert required.issubset(set(value)), f"{name}: missing fields {required - set(value)}"
        assert value["version"] == 1, f"{name}: version must be 1"
        assert isinstance(value["description"], str) and value["description"].strip(), f"{name}: description required"
        assert value["name"] == name.split(".")[0], f"{name}: name field mismatch"


def validate_profile_digest() -> None:
    output = subprocess.run(
        [sys.executable, str(ROOT / "scripts" / "profile_digest.py"), "--digest"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
    assert output == EXPECTED_PROFILE_DIGEST, f"profile digest drift: {output}"


def main() -> int:
    validate(ROOT / "release" / "manifest.json")
    validate(ROOT / "release" / "fixtures" / "compatible.json")
    for path in sorted((ROOT / "release" / "fixtures").glob("incompatible-*.json")):
        try:
            validate(path)
        except (AssertionError, KeyError, TypeError, ValueError):
            continue
        raise AssertionError(f"incompatible fixture unexpectedly accepted: {path}")
    validate_contract_docs()
    validate_profile_digest()
    return 0


if __name__ == "__main__":
    sys.exit(main())
