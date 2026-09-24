# Astara Knowledge release and operations

Astara Knowledge is released independently from Astara Plane. Plane consumes
only signed OCI images pinned by digest; neither repository is a submodule or
subtree of the other.

## Upstream baseline and sync

The `0.1.0-astara.1` release tracks upstream main at commit
`967ed097062c61723045ae103b665982728dc572`, the v0.8.2 development line
(upstream documents v0.8.2 but has not created the tag, so the commit is the
authoritative anchor). The previous baseline was the v0.8.0 tag,
`1edcd54b43606d9079bb36650efe3f68707a79ea`. A local checkout should keep:

```bash
git remote add upstream https://github.com/Tencent/WeKnora.git
git fetch upstream main
git update-ref refs/remotes/upstream/main 967ed097062c61723045ae103b665982728dc572
```

Upstream's git transport is reachable over SSH when HTTPS is blocked:

```bash
git fetch git@github.com:Tencent/WeKnora.git main
```

For a later upstream sync, fetch the candidate commit, verify it and its
release notes, merge it into `main` as a merge commit, and resolve conflicts by
taking upstream's structure and re-applying the fork's intent into it — in
particular the knowledge-only profile guards in `internal/container` and
`internal/router`. Renumber any fork migration that collides with a new
upstream version (see the migration note below). Run the Knowledge contract
workflow and Plane-to-Knowledge Docker suite before updating
`release/manifest.json`. Never move an existing release tag or reuse an
implementation version.

## Migration numbering

Upstream and the fork both take the next free migration number, so a fork-only
migration will collide with a new upstream one sooner or later — and a
collision is not a merge inconvenience: git keeps both files at the same
version, `golang-migrate` refuses to load the directory, and every deployment
fails to migrate. This has already happened twice (upstream `000091`-`000093` /
`000013`-`000015`, then upstream `000111` / `000031`).

The fork therefore reserves a wide, high range for its own migrations:

| Chain | Upstream range | Fork range |
| --- | --- | --- |
| versioned (PostgreSQL) | `000000`-`000114` | `000200`-`000202` |
| SQLite | `000000`-`000034` | `000100`-`000102` |

The fork's three migrations are external identity (`000200` / `000100`),
embedded identity (`000201` / `000101`) and document identity (`000202` /
`000102`). When syncing, if upstream adds a migration inside the fork's
reserved range, move the fork migration further out rather than reusing an
upstream number. `internal/database/migration_sqlite_versioned_schema_test.go`
pins `expectedSQLiteMigrationVersion` (currently 102) and asserts every column
the fork's Go models expect, so a mis-ordered or missing fork migration fails
the build rather than production.

**Renumbering is only safe before a release.** `golang-migrate` records the
applied *version number*, not the migration's contents, so renumbering a
migration that a database has already applied makes that database re-apply it
at the new number. The versioned (PostgreSQL) files survive this because they
use `ADD COLUMN IF NOT EXISTS` / `CREATE UNIQUE INDEX IF NOT EXISTS`, but
SQLite has no `ADD COLUMN IF NOT EXISTS`, so the astara SQLite files use bare
`ALTER TABLE ... ADD COLUMN` and would fail with `duplicate column name`,
leaving the migration dirty. Both renumberings so far happened while
`implementation_version` was still `0.1.0-astara.1` and no image had been
published, so no deployed database can be affected. Once an implementation
version is released, never renumber its migrations — add a new migration
instead, and if a SQLite database is already dirty, clear the failed version
and re-mark it before retrying.

## Release verification

Run the local gates:

```bash
python3 scripts/verify_astara_release.py
python3 -m unittest deploy/astara-knowledge/compose_inventory_test.py
go test ./internal/astara ./internal/router ./internal/handler ./release
(cd frontend && npm ci && npm test -- --run)
```

A tag matching `v*-astara.*` builds API, web, and DocReader images with
BuildKit SBOM and provenance attestations, pushes them to GHCR, and signs each
immutable digest using GitHub OIDC keyless signing. Record the resulting three
digests in Plane's single Knowledge dependency manifest. Publishing is not
complete until `cosign verify`, the digest pins, and the full cross-repository
Docker contract suite pass.

## Enable and disable

Set `WEKNORA_FEATURE_PROFILE=astara-knowledge` and provide a non-empty
`ASTARA_SERVICE_AUTH_SECRET`. The private API exposes process-only liveness
at `/health/live` and dependency/contract readiness at `/health/ready`.
Unknown profile values fail closed: readiness is false and no application or
control-plane routes are registered.

Enable Knowledge from Plane only after its handshake reports exact matching
implementation, upstream baseline, profile, API/UI/source/tool/readiness, and
migration contract versions. To disable, turn off Plane's Knowledge feature
flag first, allow in-flight provisioning to settle, and then stop the
Knowledge services. Plane core remains usable throughout.

## Provisioning retry and dead-letter recovery

Plane owns provisioning intent and retry state. Tenant and knowledge-base
creates include stable `external_system` and `external_id` values and may be
replayed: the API returns the existing resource after a timeout or duplicate
delivery. Operators should retry from Plane's durable binding record, never
manually create a second provider object. Before recovering a dead letter,
query the matching `/by-external-id` endpoint; reconcile the returned ID when
present, otherwise re-enqueue the same intent and idempotency key. An identity
conflict must be investigated and must not be overwritten.

## Rollback

Disable dispatch in Plane, restore the previous three image digests, and wait
for `/health/ready` to match the previous manifest. Do not roll back by sharing
Plane's database/Redis or deleting binding records. The external identity
migration is additive, so bindings can be retained for a later retry. If a
schema rollback is required, first prove no externally identified rows remain,
back up the isolated Knowledge database and files, then apply the paired down
migration. Keep Knowledge unavailable until reconciliation is complete.

## Backup and restore quarantine

`scripts/knowledge_backup.sh --output <dir>` takes a consistent backup of the
authoritative state: a custom-format database dump, the provider-managed
source files volume, the storage mapping, and a secret-escrow manifest
(secret NAMES only — values are never written). `scripts/knowledge_restore.sh
--backup <dir> --quarantine-project <name>` restores into a fresh quarantine
stack and runs `scripts/knowledge_postrestore_validate.py`, which proves the
applied migration position, storage references, source reconciliation,
authorization probes, and index coverage before the stack may serve. A
restored stack stays quarantined: promote it explicitly by repointing Plane's
edge after the validation report passes.

## Candidate reindex, cutover, and rollback

Plane-side reindex runs never write into the active provider knowledge base:
`manage.py knowledge_provisioning reindex-start --space-id <id>` creates (or
resumes) one durable candidate run, `reindex-process --run-id <id>` performs
resumable version-fenced upserts (matching hashes and revisions are skipped),
`reindex-cutover --confirm <candidate-kb-id>` atomically repoints the Space
only at declared coverage, `reindex-rollback --confirm <previous-kb-id>`
restores the previous knowledge base inside a bounded window, and
`reindex-cleanup --confirm <candidate-kb-id>` deletes only a
confirmed-inactive target. Cleanup refuses the active pointer and any target
protected by the rollback window even when a confirmation is supplied.

## Upstream advisory triage and emergency fork patch

The weekly `Upstream advisory triage` workflow runs `govulncheck`, `npm
audit`, and `cargo audit` against the pinned baseline and opens a bounded
triage issue on findings. Triage each finding against the pinned upstream
commit before any action:

1. **Exposure check.** The astara-knowledge profile constructs only the
   knowledge services; a finding in an unconstructed Agent/Skills/Sandbox/
   MCP/Web-Search/IM/Memory surface is not reachable and is recorded as such
   in the triage issue.
2. **Patch lane.** For reachable findings, create an emergency fork branch
   from the current release commit, apply the minimal upstream fix, bump the
   patch segment of the implementation version (`0.1.0-astara.2`), and run
   the release workflow on a `v*-astara.*` tag.
3. **Pin bump.** Record the new image digests in Plane's dependency manifest
   through a release PR that also re-records the compatibility matrix row
   from a passing integration run. Admission stays closed until the closure
   verify, signature verify, and evaluation gates pass against the new pins.
4. **Never** move an existing release tag, reuse an implementation version,
   or hot-patch a running deployment in place.
