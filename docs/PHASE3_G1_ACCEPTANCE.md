# Phase 3 G1 — Acceptance Checklist

Candidate branch: `19.2.10.G1`  
Candidate version: `19.2.10`

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
- [ ] Kill the process after reservation-store commit but before allocation finalization; restart and call `ReconcileAllPending`.
- [ ] Verify orphaned `RESERVING` allocations without reservation records become `FAILED`.
- [ ] Verify bundle-wide lease renewal and expiry against real allocation/reservation files.
- [ ] Verify the injected production `CleanupVerifier` observes actual runtime/port/GPU state before release.
- [ ] Verify configured port reservations against competing external processes.

## Release decision

Do not merge or tag a stable `v19.2.10` until the latest branch CI is green and the target-host crash-recovery and cleanup checks pass. The candidate is kept on a draft PR until then.
