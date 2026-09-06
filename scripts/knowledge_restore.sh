#!/usr/bin/env bash
# Copyright (c) 2026-present Astara and contributors
# SPDX-License-Identifier: Apache-2.0
# See the LICENSE file for details.
#
# Restore an Astara Knowledge backup into a QUARANTINE stack.
#
# Restored Knowledge stays quarantined until scripts/knowledge_postrestore_validate.py
# proves: applied migration position equals the backup identity's position,
# storage references resolve, source documents reconcile, authorization probes
# deny unknown tenants, and required index coverage exists. Browser and DSH
# traffic stays pointed at the OLD stack until the operator promotes the
# quarantine stack explicitly.
#
# Usage:
#   scripts/knowledge_restore.sh --backup <dir> --quarantine-project <name> \
#     [--compose-file <file>]... [--expected-migration-position <n>]
#
# Environment: KNOWLEDGE_DB_PASSWORD, ASTARA_SERVICE_AUTH_SECRET,
# ASTARA_IDENTITY_EXCHANGE_SECRET, SYSTEM_AES_KEY (values from your escrow).

set -euo pipefail

BACKUP_DIR=""
COMPOSE_FILES=("deploy/astara-knowledge/compose.yml")
QUARANTINE_PROJECT="astara-knowledge-quarantine"
EXPECTED_MIGRATION_POSITION=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --backup) BACKUP_DIR="$2"; shift 2 ;;
    --compose-file) COMPOSE_FILES+=("$2"); shift 2 ;;
    --quarantine-project) QUARANTINE_PROJECT="$2"; shift 2 ;;
    --expected-migration-position) EXPECTED_MIGRATION_POSITION="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$BACKUP_DIR" ]; then
  echo "--backup <dir> is required" >&2
  exit 2
fi

: "${KNOWLEDGE_DB_PASSWORD:?KNOWLEDGE_DB_PASSWORD is required}"
: "${ASTARA_SERVICE_AUTH_SECRET:?ASTARA_SERVICE_AUTH_SECRET is required}"
: "${ASTARA_IDENTITY_EXCHANGE_SECRET:?ASTARA_IDENTITY_EXCHANGE_SECRET is required}"
: "${SYSTEM_AES_KEY:?SYSTEM_AES_KEY is required}"

# 0. Verify backup integrity against the recorded digests before touching
#    any volume.
python3 -c "
import pathlib, sys
sys.path.insert(0, 'scripts')
from knowledge_postrestore_validate import verify_backup_integrity
verify_backup_integrity(pathlib.Path('$BACKUP_DIR'))
print('backup integrity verified')
"

compose() {
  local args=()
  local file
  for file in "${COMPOSE_FILES[@]}"; do
    args+=(-f "$file")
  done
  docker compose -p "$QUARANTINE_PROJECT" "${args[@]}" "$@"
}

# 1. Start a FRESH quarantine stack (never the running project).
compose down -v >/dev/null 2>&1 || true
compose up -d --wait db redis

# 2. Restore the database from the custom-format dump (pg_restore reads
#    the dump from stdin when no input file is given).
compose exec -T db sh -c 'pg_restore -U "${DB_USER:-astara_knowledge}" --clean --if-exists --dbname astara_knowledge' \
  < "$BACKUP_DIR/knowledge-db.dump"

# 3. Restore the provider-managed source files into the quarantine files
#    volume.
docker run --rm \
  --volume "${QUARANTINE_PROJECT}_files:/data/files" \
  --volume "$(cd "$BACKUP_DIR" && pwd):/backup:ro" \
  alpine:3.20 sh -c 'rm -rf /data/files/* && tar -xf /backup/knowledge-files.tar -C /data/files'

# 4. Start the API against the restored state. Migrations run through
#    AUTO_MIGRATE; readiness fails closed until DB/Redis/migrations pass.
compose up -d --wait api

# 5. Post-restore validation: the stack stays quarantined until every check
#    passes. Failures exit non-zero and the stack is left for inspection.
COMPOSE_CSV="$(IFS=,; echo -n "${COMPOSE_FILES[*]}")"
python3 scripts/knowledge_postrestore_validate.py \
  --compose-project "$QUARANTINE_PROJECT" \
  --compose-file "$COMPOSE_CSV" \
  --backup "$BACKUP_DIR" \
  ${EXPECTED_MIGRATION_POSITION:+--expected-migration-position "$EXPECTED_MIGRATION_POSITION"}

echo "Quarantine stack restored and validated. It is NOT yet serving traffic."
echo "Promote it explicitly only after reviewing the validation report."
