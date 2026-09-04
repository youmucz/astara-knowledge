# Astara Knowledge release and operations

Astara Knowledge is released independently from Astara Plane. Plane consumes
only signed OCI images pinned by digest; neither repository is a submodule or
subtree of the other.

## Upstream baseline and sync

The `0.1.0-astara.1` release is based on Tencent/WeKnora `v0.8.0`, immutable
commit `1edcd54b43606d9079bb36650efe3f68707a79ea`. A local checkout should keep:

```bash
git remote add upstream https://github.com/Tencent/WeKnora.git
git fetch upstream refs/tags/v0.8.0
git tag upstream/v0.8.0 1edcd54b43606d9079bb36650efe3f68707a79ea
```

For a later upstream sync, fetch the candidate tag, verify its commit and
release notes, create a dedicated branch from that tag, then replay only the
Astara profile, identity, contract, upsert, ACL, and integration patches. Run
the Knowledge contract workflow and Plane-to-Knowledge Docker suite before
updating `release/manifest.json`. Never move an existing release tag or reuse
an implementation version.

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
