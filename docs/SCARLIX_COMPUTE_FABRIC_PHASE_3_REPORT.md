# SCARLIX Compute Fabric — Phase 3 Implementation Report

**Candidate branch:** `19.2.10.G1`  
**Version:** `19.2.10`  
**Scope:** Atomic resource reservation, cross-process locking, lease management, and allocation-state integration  
**Status:** **IMPLEMENTED IN CANDIDATE — EXECUTION/CI VERIFICATION PENDING**  
**Hardware verification:** **BLOCKED** — requires the target ScarLiX host and live runtimes

## 1. Scope from the Phase 2 handoff

The Phase 2 report explicitly lists Phase 3 as:
- a reservation manager that uses the allocation lifecycle to reserve resources;
- file-level locking for multi-process safety;
- TTL-based leases, renewal, and expiry;
- concurrent-request tests for exclusive resources.

This candidate implements those items while keeping runtime startup/termination and public API endpoints out of scope (the project plan assigns those to later phases).

## 2. Implementation

### 2.1 Atomic reservation bundles
- `Manager.Reserve()` remains source-compatible and delegates to `ReserveBundle()`.
- `ReserveBundle()` validates and canonicalizes every resource before acquiring a cross-process lock.
- The reservation store is read and rewritten once under the same lock; no partial bundle is written when any resource conflicts.
- Duplicate resources in one request bundle are rejected.
- All bundle members share one lease ID, expiry, idempotency key, fingerprint, and declared bundle size.
- Idempotency lookup happens before opening a new allocation claim. Replays verify owner, allocation ID, resource set, bundle size, and fingerprint.
- Legacy single-resource records without a bundle-size field remain supported.

### 2.2 Cross-process serialization
- `ResourceLock` uses the reservation store's stable flock file so reservation selection and persistence share the same process-independent lock.
- Existing store update operations use that same flock, preventing a second lock ordering from being introduced.
- The critical section includes the durable read, policy evaluation, and atomic rewrite.

### 2.3 Allocation lifecycle integration
- `AllocationReservationAdapter` connects the reservation manager to the existing allocation state machine and allocation store.
- `PLANNED → RESERVING → RESERVED` is performed through validated allocation transitions.
- Allocation owner is checked against the trusted authenticated principal.
- Resource assignments and allocation lease metadata are written in the same allocation-store update that transitions to `RESERVED`.
- Failed pre-commit reservation attempts abort the pending allocation claim; post-commit finalization failures are reported as reconciliation-required, not rolled back.

### 2.4 Crash recovery and reconciliation
- `ReconcilePending()` repairs one known allocation after a durable reservation commit whose allocation finalization did not complete.
- `ReconcileAllPending()` scans allocations left in `RESERVING` and finalizes complete durable bundles, or marks orphan claims failed when no reservation records were committed.
- Bundle-size validation rejects incomplete reservation sets rather than finalizing a partial allocation.
- `ReapExpiredOnce()` marks expired leases and asks the injected cleanup verifier to reconcile them.
- `RunLeaseReaper()` provides a cancellable periodic worker with explicit error reporting.

### 2.5 Lease handling
- `RenewLease()` now renews the whole allocation bundle rather than one member.
- `RenewBundleLease()` updates all bundle members in one atomic reservation-store rewrite and synchronizes allocation metadata when the adapter supports it.
- Expired leases cannot be renewed.
- A failed second-store metadata update returns `ErrReconciliationRequired`; the durable reservation lease remains authoritative and can be repaired.

### 2.6 Stable GPU identity
- `inventory.GPU` gains an internal-only `UUID` field populated from `nvidia-smi`; it is excluded from JSON so the frozen public inventory schema remains unchanged.
- The NVIDIA collector queries UUID from `nvidia-smi`.
- The reservation inventory adapter uses stable GPU UUIDs, never mutable display indices, and fails closed for missing/malformed UUIDs.
- Only healthy GPUs, enabled/healthy runtimes, present models, and explicitly configured ports are exposed as reservable.
- If the caller does not provide a generation, the adapter derives one from resource availability state and canonical IDs.

## 3. New/updated source files

- `scarlihq/internal/reservation/manager.go`
- `scarlihq/internal/reservation/resources.go`
- `scarlihq/internal/reservation/resource_lock.go`
- `scarlihq/internal/reservation/allocation_adapter.go`
- `scarlihq/internal/reservation/inventory_adapter.go`
- `scarlihq/internal/reservation/reservation_test.go`
- `scarlihq/internal/reservation/allocation_adapter_test.go`
- `scarlihq/internal/reservation/inventory_adapter_test.go`
- `scarlihq/internal/reservation/token_identity.go`
- `scarlihq/internal/reservation/token_identity_test.go`
- `scarlihq/internal/inventory/types.go`
- `scarlihq/internal/inventory/gpu.go`
- `scarlihq/internal/inventory/gpu_test.go`

## 4. Regression coverage added

The candidate includes tests for:
- bundle conflict leaves no partial reservation bundle in persistent storage;
- idempotent bundle replay does not claim an already-reserved allocation again;
- all resources in a bundle renew to the same expiry and lease ID;
- pending allocation finalization can be retried by a trusted reconciler;
- allocation ownership is checked against the trusted principal;
- assignment and `RESERVED` state are persisted together;
- interrupted allocation claims can be enumerated for startup recovery;
- GPU inventory snapshots use canonical stable UUIDs and reject unhealthy/missing resources;
- inventory generation changes when available GPU capacity changes.

## 5. Verification status

**No test result is claimed yet.** The GitHub integration permits branch writes, but this execution environment has not run Go commands against a local checkout. Earlier attempts to clone the repository were blocked by DNS/network resolution. GitHub CI status must be checked on the candidate PR.

Required checks before merge:
```bash
cd scarlihq
gofmt -w internal/reservation internal/inventory
go vet ./...
go test ./...
go test -race ./internal/allocation ./internal/reservation ./internal/inventory
```

Also run shell/config validation and the offline smoke test from the repository root, then perform target-host verification for:
- actual UUID output from `nvidia-smi`;
- real allocation store and reservation store under concurrent processes;
- a process kill between reservation commit and allocation finalization, followed by `ReconcileAllPending()`;
- lease expiry, cleanup verification, and recovery against the live runtime containers.

## 6. Remaining limits / release gates

1. **Two independent stores are not physically one transaction.** The design uses a durable reservation commit plus idempotent reconciliation. Crash-injection tests must prove that recovery works with the real allocation store.
2. **Cleanup is an injected trust boundary.** The production `CleanupVerifier` must inspect actual runtime/resource state; the test stub is not a production implementation.
3. **Ports require trusted configuration.** The inventory adapter deliberately does not infer free ports from a configured port number; the caller must supply an authoritative managed-port set and production port checks must handle races with external processes.
4. **GPU identity now has a stable UUID field, but all consumers must use it for reservation IDs.** Display IDs such as `gpu.nvidia.0` remain inventory/UI identifiers and are not accepted as reservation IDs.
5. **No runtime execution/termination or REST/MCP endpoint is added here.** These remain later Compute Fabric phases.
6. **Hardware validation is still blocked.** This report does not claim that the candidate is production-ready.

## 7. Phase 3 gate

**Code-level scope:** implemented in branch `19.2.10.G1`.  
**Acceptance gate:** pending successful Go/CI checks and crash-injection verification.  
**Release decision:** do not merge/tag as stable until the acceptance gate passes.
