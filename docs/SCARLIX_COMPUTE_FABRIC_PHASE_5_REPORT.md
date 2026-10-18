# ScarLiX Compute Fabric — Phase 5: Workload Execution Lifecycle

**Branch:** `19.2.12.P5`  
**Baseline:** P4 candidate `19.2.11.P4`, commit `31a526585527133131be1c37361cba4e20ca34bb`  
**Scope:** durable allocation execution transitions and a narrow trusted-driver contract  
**Status:** initial implementation; concrete host-bridge driver and target-host acceptance remain open  
**Release:** not tagged; not production-ready

## Phase 5 objectives

Phase 2 explicitly deferred runtime startup/stop and privileged Host-Bridge operations to Phase 5. This phase introduces the lifecycle coordinator without giving the ScarliHQ process direct Docker socket or arbitrary shell access.

## Implemented in this increment

- New `internal/workload` package with an injected `Driver` interface.
- Start path: `RESERVED → STARTING → READY → RUNNING`, with the external driver called only after the durable `STARTING` transition.
- Stop path: `RUNNING/READY/STARTING/FAILED/CANCELLED/CLEANUP_REQUIRED → RELEASING`; a positively verified stop records cleanup verification but does not mark the allocation `RELEASED`.
- Owner check before any driver call.
- Runtime ID, model ID, and assigned port are validated before execution.
- Ambiguous start outcomes and unverified stop outcomes fail closed to `CLEANUP_REQUIRED`.
- Raw driver errors are not persisted into allocation records.
- Additive `workload_owned` field records whether the allocation actually started the workload. A shared/pre-existing runtime must not be terminated by a different allocation.
- Tests cover successful transitions, owner rejection, incomplete assignments, uncertain start, verified stop, stop failure, and shared-workload ownership.

## Security and correctness boundaries

- The manager accepts only structured identifiers; it has no command-string or arbitrary environment input.
- A production driver must implement idempotency by allocation ID, strict runtime allowlisting, readiness verification, bounded command timeouts, and sanitized outcomes.
- ScarliHQ must continue to have no Docker socket mount. Privileged execution must cross a separately validated host bridge.
- A successful stop leaves the record in `RELEASING`; Phase 6 is responsible for releasing reservations and performing the final `RELEASED` transition.
- The current increment does not claim that runtime containers were started/stopped on a real host. The production host-bridge driver is the next P5 work item.

## Verification required

- `go vet ./...`
- `gofmt -l .`
- `go test ./...`
- `go test -race ./internal/allocation ./internal/inventory ./internal/reservation ./internal/workload`
- Existing Docker/API/WebSocket integration suite.
- Later target-host tests for process interruption, actual runtime readiness, stop ownership, and reconciliation.

## Exit criteria

P5 is not complete until the concrete allowlisted host-bridge driver and its host-side service are implemented, automated CI is green on the final commit, and target-host acceptance steps are documented. The abstraction and fake-driver tests alone do not count as real runtime execution.
