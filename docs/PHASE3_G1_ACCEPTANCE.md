# Phase 3 G1 — Acceptance Checklist

Candidate branch: `19.2.10.G1`  
Candidate version: `19.2.10`

> **Status (v19.2.12, Deep Audit F-03):** Phase 3 — including the v19.2.11 commit-consistency closure and the v19.2.12 release-lifecycle closure — is **domain-complete in CI**. **Target-host E2E acceptance is still PENDING** (the checklist below remains unchecked). Phase 3 must not be marketed as "Compute Fabric complete" until this checklist passes on real hardware; the target is v19.2.13. Until then the honest statement is: *logically strong, empirically unproven on the target host.*

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

## Release decision

Do not merge or tag a stable `v19.2.10` until the latest branch CI is green and the target-host crash-recovery and cleanup checks pass. The candidate is kept on a draft PR until then.
