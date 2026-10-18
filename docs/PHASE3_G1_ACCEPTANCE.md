# Phase 3 G1 — Acceptance Checklist

Candidate branch: `19.2.10.G1`  
Candidate version: `19.2.10`

## Automated acceptance

- [ ] Latest branch SHA: `go vet ./...`
- [ ] Latest branch SHA: `gofmt -l .` returns no files
- [ ] Latest branch SHA: all Go binaries build
- [ ] Latest branch SHA: `go test ./...`
- [ ] Latest branch SHA: `go test -race ./internal/allocation ./internal/inventory ./internal/reservation`
- [ ] Latest branch SHA: smoke, YAML, ShellCheck, systemd, Compose, Docker build, and API integration jobs pass

Earlier candidate SHA `097a8ca82ce4879f4ac5c8bc94b5b0703daf68b5` passed Go vet, formatting, build, full Go tests, race tests, smoke checks, and Docker build. Later source changes to the file-backed manager factory, inventory-generation hashing, and adapter declaration order require a final rerun against the branch head.

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
