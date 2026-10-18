# ScarLiX Compute Fabric — P5.1 Host Bridge

Status: **protocol core implemented; not yet deployable for real workload execution**.

Base snapshot: `release/v19.2.12-P5-snapshot`  
Work branch: `19.2.13.P5.1`

## Objective

Introduce a narrow host-side boundary between the ScarliHQ lifecycle manager and runtime-specific execution. ScarliHQ must not gain Docker socket access, host root privileges, shell execution, arbitrary executable selection, arbitrary image selection, arbitrary environment variables, or host filesystem paths.

## Implemented in this increment

- Typed HTTP operations: `POST /v1/workloads/start` and `POST /v1/workloads/stop`.
- The server is exposed as an HTTP handler, but the only provided listener is a Unix-domain socket helper. Do not expose it on TCP.
- Socket creation requires an absolute path and a pre-existing real parent directory; existing paths are rejected instead of unlinked; mode is set to `0600`.
- JSON requests are size-bounded, reject unknown fields and trailing JSON values, and require `application/json`.
- Runtime IDs are checked against the frozen Resource Contract allowlist.
- Request IDs, model IDs, GPU IDs and returned workload references are bounded and syntax-validated.
- Shared/pre-existing workloads (`owned=false`) cannot reach the stop executor.
- Executor errors are not returned to clients; readiness and cleanup must be positively confirmed.
- HTTP timeouts and header limits are explicit.

## Deliberate limitations

This is the **protocol and security core**, not yet a working host execution adapter. There is no production `Executor` implementation, service unit, installer wiring, or ScarliHQ client connection in this commit. The API must fail closed until a separately reviewed executor is supplied. Do not mark P5.1 complete or claim host workload execution works from these tests alone.

## Required next work

1. Add an explicit host service entrypoint and systemd unit running under a dedicated service identity.
2. Decide the transport identity model before granting ScarliHQ access: Unix socket ownership/group and container GID mapping must be least-privilege. Never use world-writable socket permissions.
3. Implement runtime-specific adapters using fixed, typed operations and pre-approved runtime configuration. Do not pass user-supplied command lines, image names, environment maps, or file paths.
4. Add request idempotency/reconciliation keyed by allocation ID so retry after a timeout cannot create duplicate workloads.
5. Add a narrow ScarliHQ client implementing the existing `workload.Driver` interface; keep API and Resource Contract v1 stable.
6. Add CI tests, service packaging, installer/rollback handling, and target-host acceptance before release.

## Acceptance gates

- [ ] Unit tests for request validation and ownership boundary.
- [ ] Go tests and race tests pass in CI.
- [ ] No Docker socket mount added to `scarlihq/docker-compose.yml`.
- [ ] No TCP listener or arbitrary command execution.
- [ ] Service identity, socket ACL and lifecycle/rollback documented.
- [ ] Real-host tests for start, readiness, stop, timeout, restart and shared runtime behavior.
