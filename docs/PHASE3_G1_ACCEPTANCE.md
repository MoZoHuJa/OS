# Phase 3 G1 — Acceptance Checklist

Candidate branch: `19.2.10.G1`  
Candidate version: `19.2.10`

> **Status (v19.2.13, KPK-19.2.12 audits):** Phase 3 — including the v19.2.11 commit-consistency closure, the v19.2.12 release-lifecycle closure and the v19.2.13 interrupted-release recovery closure — is **domain-complete in CI**. **Target-host E2E acceptance is still PENDING** (the checklist below remains unchecked). Phase 3 must not be marketed as "Compute Fabric complete" until this checklist passes on real hardware; the target is **v19.2.14**. Until then the honest statement is: *logically strong, empirically unproven on the target host.*
>
> **Scope statement (KPK-19.2.12-P2-03):** `Recover()` / `RunLeaseReaper` / `ReconcileAllPending` are a **domain-layer library** — invoked by tests, smoke checks and CI, but not yet by a production service composition root. Operational wiring is Phase 5 (audit F-04).

## Automated acceptance

- [x] Source SHA `e46cf5a871188c8c17aa8e0fbf2315c3b1d31c87`: `go vet ./...`
- [x] Source SHA `e46cf5a871188c8c17aa8e0fbf2315c3b1d31c87`: `gofmt -l .` returns no files
- [x] Source SHA `e46cf5a871188c8c17aa8e0fbf2315c3b1d31c87`: all Go binaries build
- [x] Source SHA `e46cf5a871188c8c17aa8e0fbf2315c3b1d31c87`: `go test ./...`
- [x] Source SHA `e46cf5a871188c8c17aa8e0fbf2315c3b1d31c87`: `go test -race ./internal/allocation ./internal/inventory ./internal/reservation`
- [x] Source SHA `e46cf5a871188c8c17aa8e0fbf2315c3b1d31c87`: smoke, YAML, ShellCheck, systemd, Compose, Docker build, and API integration jobs pass

All nine GitHub Actions jobs passed on source SHA `e46cf5a871188c8c17aa8e0fbf2315c3b1d31c87`. The merge commit that incorporates the latest `main` history and this documentation update will trigger a final rerun.

## Target-host acceptance (still required)

- [ ] Confirm live `nvidia-smi` UUID collection on the ScarLiX host.
- [ ] Run concurrent reservations from separate OS processes against the same GPU and verify exactly one succeeds.
- [ ] Kill the process after reservation-store commit but before allocation finalization; restart and call `Recover`/`ReconcileAllPending` (v19.2.12: this now runs the FULL sequence — RESERVING scan + expired-lease reaper + interrupted-release completion + reserved-lease repair — and returns a structured `RecoverySummary`).
- [ ] Verify orphaned `RESERVING` allocations without reservation records become `FAILED`.
- [ ] Verify bundle-wide lease renewal and expiry against real allocation/reservation files.
- [ ] Verify the injected production `CleanupVerifier` observes actual runtime/port/GPU state before release.
- [ ] Verify configured port reservations against competing external processes.
- [ ] (v19.2.12) Kill the process between a reservation release and the allocation-store completion write; restart and verify the allocation converges to `RELEASED` with assignments cleared (not stuck `RESERVED`/`RELEASING`).
- [ ] (v19.2.12) Simulate a lease-metadata mirror-write failure (renewal committed to the reservation store, allocation update fails); verify the periodic reaper repairs the allocation lease within one interval.
- [ ] (v19.2.12) Single-GPU host: verify `scarlix-mode` skips vLLM with a warning by default and starts it on GPU 0 only with `SCARLIX_ALLOW_SHARED_GPU=1` (generated `docker-compose.gpu-adapter.yml`).
- [ ] (v19.2.13) Kill the process between `RequestRelease()` and `ConfirmRelease()` (reservation `RELEASING`, allocation possibly still `RESERVED`); restart and verify `Recover()` **actively completes** the release — all bundle members `RELEASED`, allocation `RELEASED`, assignments cleared — without waiting for lease expiry.
- [ ] (v19.2.13) Same interrupted user-initiated release while the service keeps running: verify the periodic lease-reaper tick (not only startup) completes it.
- [ ] (v19.2.13) FAILED/CANCELLED allocation with a reservation bundle whose members are released: verify recovery walks the table-legal path to `RELEASED` (never an illegal direct `FAILED/CANCELLED → RELEASED` jump).
- [ ] (v19.2.13) Verify the production `CleanupVerifier` (not a stub) — see SECURITY_MAP section (j): `RELEASED` implies reservation-layer verified cleanup; hardware proof requires the real verifier.

## Release decision

Do not merge or tag a stable `v19.2.10` until the latest branch CI is green and the target-host crash-recovery and cleanup checks pass. The candidate is kept on a draft PR until then.
