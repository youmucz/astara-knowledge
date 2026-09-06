#!/usr/bin/env bash
# Copyright (c) 2026-present Astara and contributors
# SPDX-License-Identifier: Apache-2.0
# See the LICENSE file for details.
#
# Consistent backup for one Astara Knowledge deployment.
#
# The backup set covers exactly the authoritative state: the Knowledge
# database (metadata, bindings, chunks, and vector indexes when stored in the
# DB), the provider-managed source files volume, the storage mapping, and the
# required secret escrow manifest (secret NAMES and presence only — secret
# values are never written into the backup).
#
# Usage:
#   scripts/knowledge_backup.sh --output <dir> [--compose-file <file>] [--project <name>]
#
# The output directory receives:
#   knowledge-db.dump        custom-format pg_dump of the whole database
#   knowledge-files.tar      the /data/files volume (source files)
#   storage-mapping.json     exact storage mapping exported from the API
#   secret-escrow.json       required secret names and presence booleans
#   backup-manifest.json     closure identity + checksums of everything above

set -euo pipefail

COMPOSE_FILE="deploy/astara-knowledge/compose.yml"
PROJECT_NAME="astara-knowledge"
OUTPUT_DIR=""
DB_PASSWORD="${KNOWLEDGE_DB_PASSWORD:?KNOWLEDGE_DB_PASSWORD is required}"
SERVICE_AUTH_SECRET="${ASTARA_SERVICE_AUTH_SECRET:?ASTARA_SERVICE_AUTH_SECRET is required}"
IDENTITY_SECRET="${ASTARA_IDENTITY_EXCHANGE_SECRET:?ASTARA_IDENTITY_EXCHANGE_SECRET is required}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) OUTPUT_DIR="$2"; shift 2 ;;
    --compose-file) COMPOSE_FILE="$2"; shift 2 ;;
    --project) PROJECT_NAME="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$OUTPUT_DIR" ]; then
  echo "--output <dir> is required" >&2
  exit 2
fi

compose() {
  docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

mkdir -p "$OUTPUT_DIR"

# 1. Consistent database dump: a single custom-format dump of the whole DB
#    (schema + data) so the restore can replay migrations deterministically.
compose exec -T db pg_dump -U postgres -d astara_knowledge -Fc > "$OUTPUT_DIR/knowledge-db.dump"

# 2. Provider-managed source files: the files volume holds the authoritative
#    uploaded document binaries; chunks/indexes are reconstructable.
docker run --rm \
  --volume "${PROJECT_NAME}_files:/data/files:ro" \
  --volume "$(cd "$OUTPUT_DIR" && pwd):/backup" \
  alpine:3.20 tar -cf /backup/knowledge-files.tar -C /data/files .

# 3. Storage mapping (exact mapping the running service uses).
# shellcheck disable=SC2016
STORAGE_MAPPING=$(compose exec -T api sh -c 'echo "{\"storage_type\": \"${STORAGE_TYPE:-local}\", \"local_base_dir\": \"${LOCAL_STORAGE_BASE_DIR:-/data/files}\", \"reconstructable\": true}"')
printf '%s' "$STORAGE_MAPPING" > "$OUTPUT_DIR/storage-mapping.json"

# 4. Secret escrow manifest: names and presence only. Values are delivered
#    through the operator's own escrow; a missing secret fails the restore.
cat > "$OUTPUT_DIR/secret-escrow.json" <<'EOF'
{
  "schemaVersion": 1,
  "required": [
    "ASTARA_SERVICE_AUTH_SECRET",
    "ASTARA_IDENTITY_EXCHANGE_SECRET",
    "SYSTEM_AES_KEY",
    "DB_PASSWORD"
  ],
  "present": {
    "ASTARA_SERVICE_AUTH_SECRET": PLACEHOLDER_AUTH,
    "ASTARA_IDENTITY_EXCHANGE_SECRET": PLACEHOLDER_IDENTITY,
    "SYSTEM_AES_KEY": PLACEHOLDER_AES,
    "DB_PASSWORD": PLACEHOLDER_DB
  }
}
EOF
python3 - "$OUTPUT_DIR/secret-escrow.json" <<'PYEOF'
import json, os, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
present = {
    "ASTARA_SERVICE_AUTH_SECRET": bool(os.environ.get("ASTARA_SERVICE_AUTH_SECRET", "")),
    "ASTARA_IDENTITY_EXCHANGE_SECRET": bool(os.environ.get("ASTARA_IDENTITY_EXCHANGE_SECRET", "")),
    "SYSTEM_AES_KEY": bool(os.environ.get("SYSTEM_AES_KEY", "")),
    "DB_PASSWORD": bool(os.environ.get("KNOWLEDGE_DB_PASSWORD", "")),
}
data["present"] = present
path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")
PYEOF

# 5. Backup manifest: closure identity plus checksums of every artifact.
python3 - "$OUTPUT_DIR" <<'PYEOF'
import hashlib, json, os, pathlib, subprocess, sys
output = pathlib.Path(sys.argv[1])
def digest(name):
    return "sha256:" + hashlib.sha256((output / name).read_bytes()).hexdigest()
identity = {}
try:
    identity = json.loads(subprocess.run(
        ["docker", "compose", "-p", os.environ.get("PROJECT_NAME", "astara-knowledge"), "-f", os.environ.get("COMPOSE_FILE", "deploy/astara-knowledge/compose.yml"), "exec", "-T", "api", "sh", "-c", "wget -qO- http://127.0.0.1:8080/health/ready || true"],
        capture_output=True, text=True, env={**os.environ}).stdout or "{}").get("identity", {})
except Exception:
    identity = {}
manifest = {
    "schemaVersion": 1,
    "kind": "astara-knowledge-backup",
    "artifacts": {
        "database": {"file": "knowledge-db.dump", "digest": digest("knowledge-db.dump")},
        "source_files": {"file": "knowledge-files.tar", "digest": digest("knowledge-files.tar")},
        "storage_mapping": {"file": "storage-mapping.json", "digest": digest("storage-mapping.json")},
        "secret_escrow": {"file": "secret-escrow.json", "digest": digest("secret-escrow.json")},
    },
    "identity": identity,
}
(output / "backup-manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
print(f"backup manifest written: {output / 'backup-manifest.json'}")
PYEOF

echo "Knowledge backup complete: $OUTPUT_DIR"
echo "Secret VALUES are not included; deliver them through your escrow."
