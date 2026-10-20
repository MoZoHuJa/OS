# SCARLIX Compute Fabric — P5 Deployment Simulation Report

**Branch:** `19.2.12.P5`  
**Scope:** Workload lifecycle manager and allocation-owned execution boundary  
**Status:** Automated failure simulation improved; target-host deployment is **not yet accepted**.

## Simulated deployment matrix

| Scenario | Expected result | Automated coverage |
|---|---|---|
| Valid RESERVED allocation; driver reports a ready workload and stable reference | RESERVED → STARTING → READY → RUNNING | `TestStartTransitionsReservedToRunning` |
| Host driver returns an error after start was requested | CLEANUP_REQUIRED; no blind retry; raw driver error is not persisted | `TestStartFailsClosedWhenDriverOutcomeIsUncertain` |
| Driver reports not-ready or omits the workload reference | CLEANUP_REQUIRED; never report RUNNING | `TestStartFailsClosedWhenDriverDoesNotConfirmReadiness` |
| Allocation owner does not match authenticated principal | Reject before any driver side effect | `TestStartRejectsWrongOwnerBeforeDriverCall` |
| Runtime/model/port assignment is incomplete | Reject before any driver side effect | `TestStartRejectsIncompleteAssignmentWithoutSideEffects` |
| Runtime ID is outside the Resource Contract allowlist | Reject before any driver side effect | `TestStartRejectsUnsupportedRuntimeBeforeDriverCall` |
| Workload stop cannot be verified | CLEANUP_REQUIRED; retain reservation for reconciliation | `TestStopFailureRequiresReconciliation` |
| Workload is shared or pre-existing (`WorkloadOwned=false`) | Do not call the host driver; do not terminate the shared runtime | `TestStopNeverStopsSharedWorkload` |
| Allocation-owned workload stop is verified | Remain RELEASING; Phase 6 owns reservation release and RELEASED | `TestStopDoesNotReleaseAllocationBeforePhase6` |

## Defect found and corrected

The manager previously called `Driver.Stop` even when `WorkloadOwned=false`, relying entirely on the driver to refuse termination. That was an unnecessarily weak privilege boundary: a future driver defect could stop a shared runtime.

The manager now bypasses the host driver for shared/pre-existing workloads and treats the workload-cleanup portion as verified. Phase 6 must still perform its own reservation-release checks before final release.

## What this simulation does prove

- Lifecycle decisions are tested against deterministic fake-driver outcomes.
- Uncertain start/stop results fail closed instead of being reported as successful.
- Owner checks and runtime allowlisting occur before the start side effect.
- Shared runtimes are not passed to the stop driver.
- Verified workload cleanup does not prematurely release the allocation.

## What it does not prove

This is **not** a real host deployment. It does not prove Docker Compose starts correctly on the target machine, that the configured model is loaded and callable, that GPU UUID mapping is correct under live contention, or that a privileged host service is correctly installed and hardened.

The repository still needs the concrete, allowlisted host-side workload bridge and target-host acceptance tests. Do not mark P5 production-ready or create a release tag until these checks pass:

1. Install the bridge/service on a disposable Linux host with Docker available.
2. Verify only the five configured runtime IDs are accepted: `sglang`, `vllm`, `beellama`, `ollama`, `litellm`.
3. Reject unknown runtime IDs, model IDs, duplicate request IDs, malformed JSON, oversized input, symlinks, and unsafe file ownership/permissions.
4. Confirm model readiness using the runtime API and the expected model identity; container-running alone is insufficient.
5. Kill the bridge during start/stop and verify reconciliation never causes a duplicate workload or stops a shared runtime.
6. Test host reboot, Docker unavailable, occupied ports, missing model files, and GPU contention.
7. Confirm ScarliHQ has no Docker socket mount and cannot submit arbitrary host commands.
8. Run the acceptance suite against the actual host and attach its output to the release evidence.

## Release decision

**P5 is not complete yet.** The lifecycle logic has stronger automated fault coverage, but the host execution adapter and real-host acceptance remain release blockers. Keep PR #4 as draft until those blockers are resolved.
