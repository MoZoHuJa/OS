# KpK Deep Audit — ScarLiX OS v19.2.9
**Target commit:** [bfc78025f1880dcd09ad55d7ac90d0548faabd10](https://github.com/MoZoHuJa/OS/commit/bfc78025f1880dcd09ad55d7ac90d0548faabd10)  
**Target branch:** `main` at audit time; `VERSION` reports `19.2.9`  
**Audit type:** Source-based review of reservation commit and persistence path  
**Verdict:** **CHANGES REQUIRED — do not publish v19.2.10 as a stable release until fixes and CI are verified.**

## Executive result

The commit message claims the v19.2.8 findings were closed and reports passing Go tests. The GitHub connector returned no commit status checks and no workflow runs for this SHA, so those claims could not be independently verified. A local clone/test run was blocked because this environment could not resolve `github.com` (DNS/network unavailable).

## Findings

### KPK-19.2.9-P1-01 — Allocation rollback can contradict a durable reservation
**Location:** `scarlihq/internal/reservation/manager.go`, `Reserve()`, around the persistence and `EndReservation(true)` calls.

The reservation record is persisted by `rewriteAll(records)` before `EndReservation(allocationID, true)` is called. If finalization fails, `reservationSuccess` is still false, so the deferred handler calls `EndReservation(allocationID, false)`. The JSONL store can therefore contain a durable `HELD` reservation while the allocation state is rolled back. The idempotency branch has a second defect: it ignores the return value of `EndReservation(..., true)` and returns success even if allocation finalization fails.

**Impact:** Divergent allocation/reservation state, duplicate capacity accounting, or an allocation that appears available while a reservation still blocks its resource.

**Required fix:** Never roll back the allocation after the reservation record is durably committed. Check every finalization result, surface a typed “commit pending/reconciliation required” error, and make idempotent retries able to repair the allocation state. Add regression tests for finalization failure on both the first commit and idempotent replay.

### KPK-19.2.9-P1-02 — Store reports failure after atomic replacement has already succeeded
**Location:** `scarlihq/internal/reservation/store.go`, `rewriteAll()`, after `os.Rename(tmpPath, s.path)`.

The temporary file is already chmodded to `0600` before it is written and renamed. After the rename succeeds, `rewriteAll` calls `os.Chmod(s.path, 0600)` and returns an error if that chmod fails. At that point the destination file has already been replaced. `Reserve()` treats the returned error as a failed persistence and triggers allocation rollback even though the reservation data may already be committed.

**Impact:** Same cross-store inconsistency as P1-01, caused by an error result that does not mean “no write occurred.”

**Required fix:** Ensure all failure-prone permission work happens on the temporary file before rename; after rename, do not report a pre-commit-style failure unless the caller can distinguish “committed but durability/metadata uncertain.” Add a regression test for the commit boundary and document post-rename semantics.

### KPK-19.2.9-P2-01 — Idempotency lookup occurs after allocation claim
**Location:** `scarlihq/internal/reservation/manager.go`, `Reserve()`.

`BeginReservation` runs before the reservation store is loaded and before the idempotency key is checked. A retry for an already-completed reservation may be rejected by a production allocation state machine that does not allow claiming an already-`RESERVED` allocation. The current test stub does not model this state transition, so existing tests cannot prove that replay works against the real implementation.

**Impact:** A valid idempotent retry can fail even when its reservation record already exists.

**Required fix:** Resolve the durable idempotency record before opening a new allocation claim; define and test idempotent finalization/recovery semantics for a previously persisted reservation.

### KPK-19.2.9-P2-02 — Fingerprint is computed from the caller's non-canonical resource ID
**Location:** `scarlihq/internal/reservation/manager.go`, `computeFingerprint(req)` and `Reserve()`.

`Reserve()` canonicalizes `req.ResourceID` into `canonID`, but the request fingerprint is computed from the original request. Two requests that resolve to the same canonical resource can therefore produce different fingerprints when the identifier's accepted representation differs.

**Impact:** False idempotency conflicts for semantically equivalent requests.

**Required fix:** Fingerprint canonical request fields, with an explicit compatibility path for records created by earlier versions whose stored fingerprints were computed from the original input representation.

## Remediation plan for v19.2.10

1. **Fix the write/commit boundary:** remove the post-rename permission failure path; keep permissions enforced on the temporary file before replacement.
2. **Fix allocation finalization:** distinguish “not persisted” from “persisted but allocation finalization pending.” Do not call rollback after durable reservation commit. Never ignore `EndReservation(true)` errors.
3. **Repair idempotency ordering:** look up the existing key under the reservation lock before attempting a new allocation claim. Specify whether finalization is idempotent and how retries repair a pending allocation transition.
4. **Canonicalize fingerprints:** hash the canonical resource ID and define compatibility for old records.
5. **Regression coverage:** finalization failure, persisted-record replay, allocation state already reserved, failed persistence before rename, and canonical-ID replay.
6. **Verification gate:** run `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./scarlihq/internal/allocation ./scarlihq/internal/reservation`, and smoke tests in CI. Do not call the release stable until required checks are green and the residual crash-recovery gap is closed or explicitly accepted.

## Release gate / residual risk

A two-store protocol cannot provide crash-atomicity merely by calling `BeginReservation` and `EndReservation` around a file write. A process crash between durable reservation persistence and allocation finalization can still leave a pending claim. The implementation must provide a recoverable, idempotent state transition or a durable reconciliation mechanism in the allocation layer. The allocation implementation was not discoverable through the available GitHub code-search index during this audit, so this aspect remains **unverified and open**.

## Evidence and limitations

- Repository metadata reports write/admin permissions for the connected GitHub integration.
- Default-branch `VERSION` read as `19.2.9`.
- Commit description: v19.2.9 KpK closure, with stated `go vet`, `go test ./...`, race, and smoke results.
- GitHub combined status checks: empty result for the target SHA.
- GitHub workflow runs: empty result for the target SHA.
- Local `git clone` / execution unavailable in this environment because DNS resolution for `github.com` failed.
