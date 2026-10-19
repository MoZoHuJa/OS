# ScarLiX OS v19.2.10 — Reservation Consistency Patch

## Scope
This patch addresses source-level consistency defects found during the v19.2.9 KpK audit.

## Changes
- A durable reservation is no longer rolled back at the allocation layer when `EndReservation(true)` fails after the reservation store commit. The API returns an explicit pending-finalization error for reconciliation.
- Idempotent replay now checks and reports allocation finalization errors instead of ignoring them. **Open:** the allocation claim still occurs before idempotency lookup; replay behavior against an allocation already in RESERVED state remains unverified and is a release gate.
- New idempotency fingerprints use canonical resource IDs; legacy raw-input fingerprints remain accepted for compatibility.
- The reservation store no longer returns a post-rename chmod error after the destination has already been atomically replaced. The temporary file is permissioned to `0600` before rename.
- Regression tests cover canonical-equivalent resource-ID replay and durable reservation retention when allocation finalization fails.

## Verification status
**Not yet verified by execution in this environment.** GitHub returned no CI statuses or workflow runs for the input commit, and a local clone/test run was blocked by DNS resolution failure for `github.com`. Run these checks in GitHub Actions before treating the patch as release-ready:

```bash
gofmt -w scarlihq/internal/reservation/manager.go scarlihq/internal/reservation/store.go scarlihq/internal/reservation/reservation_test.go
go vet ./...
go test ./...
go test -race ./scarlihq/internal/allocation ./scarlihq/internal/reservation
```

## Known limitation
This patch does not make two independent persistence stores crash-atomic. It also does not yet move idempotency lookup ahead of `BeginReservation`; this must be resolved or proven safe against the production allocation state machine before stable release. If the process crashes after reservation persistence but before allocation finalization, production allocation code must support a safe, idempotent reconciliation path. This is a release gate and must be verified against the real allocation implementation.
