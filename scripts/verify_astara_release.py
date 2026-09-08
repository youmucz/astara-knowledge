#!/usr/bin/env python3
import json
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
EXPECTED_TOP = {"schema_version", "implementation_version", "upstream_baseline", "upstream_commit", "feature_profile", "contracts", "images"}
CONTRACT_DOCS = ("readiness.v1.json", "source.v1.json", "ui.v1.json")


def validate(path: pathlib.Path) -> None:
    value = json.loads(path.read_text())
    assert set(value) == EXPECTED_TOP, f"unknown/missing fields: {set(value) ^ EXPECTED_TOP}"
    assert value["schema_version"] == 1
    assert isinstance(value["implementation_version"], str) and value["implementation_version"]
    assert isinstance(value["upstream_baseline"], str) and value["upstream_baseline"]
    assert isinstance(value["upstream_commit"], str) and len(value["upstream_commit"]) == 40
    assert value["feature_profile"] == "astara-knowledge"
    assert set(value["contracts"]) == {"api", "ui", "source", "tool", "readiness", "migration"}
    assert all(version == 1 for version in value["contracts"].values()), "contract versions must be 1"
    assert set(value["images"]) == {"api", "web", "docreader"}
    assert all(isinstance(ref, str) and ref for ref in value["images"].values()), "image references are required"


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
    assert output, "profile digest output is required"


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
