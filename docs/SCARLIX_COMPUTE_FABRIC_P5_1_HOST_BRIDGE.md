# ScarLiX Compute Fabric — P5.1 Host Bridge

Status: **host bridge service and fixed-profile Docker executor implemented; deployment and target-host acceptance still pending**.

Base snapshot: `release/v19.2.12-P5-snapshot`  
Work branch: `19.2.13.P5.1`

## Objective

Introduce a narrow host-side boundary between the ScarliHQ lifecycle manager and runtime-specific execution. ScarliHQ must not gain Docker socket access, host root privileges, shell execution, arbitrary executable selection, arbitrary image selection, arbitrary environment variables, or host filesystem paths.

## Implemented in this increment

- Typed HTTP operations: `POST /v1/workloads/start` and `POST /v1/workloads/stop`.
- A `workload.Driver` client that connects exclusively through a configured Unix socket, with bounded response reads and no TCP fallback.
- A composition helper in `internal/compute` that injects the client into the existing `workload.Manager`; it does not add new dashboard/API routes.
- The server is exposed as an HTTP handler, but the provided listener is Unix-domain socket only. Do not expose it on TCP.
- A host-side service entrypoint and systemd unit are packaged. The installer installs the unit but does not enable it; no active runtime config is generated automatically.
- A Docker Engine adapter uses only typed create/start/inspect/stop/remove operations over the host Docker Unix socket. It is not a raw Docker API proxy.
- Runtime/model pairs must be explicitly configured with a digest-pinned image, fixed command/environment, approved port/GPU, fixed network, health path, and read-only model mounts restricted to `/var/lib/scarlix/models`.
- A durable owner-only ledger records allocation fingerprints and state. Start retries reconcile against Docker labels; conflicting assignments and unresolved states fail closed.
- Socket creation requires an absolute path and a pre-existing real parent directory; existing paths are rejected instead of unlinked; mode is set to `0600`.
- JSON requests are size-bounded, reject unknown fields and trailing JSON values, and require `application/json`.
- Runtime IDs are checked against the frozen Resource Contract allowlist.
- Request IDs, model IDs, GPU IDs and returned workload references are bounded and syntax-validated.
- Shared/pre-existing workloads (`owned=false`) cannot reach the stop executor, and the client independently refuses such stop requests.
- A socket permission-change failure closes the listener without unlinking the pathname, avoiding a cleanup-time path replacement race.
- Executor errors are not returned to clients; readiness and cleanup must be positively confirmed.
- HTTP timeouts and header limits are explicit.

## Deliberate limitations

The code now includes a fixed-profile Docker executor, durable ledger, idempotency/reconciliation wrapper, host service entrypoint, systemd unit, installer packaging, and a composition helper that injects the Unix-socket client into `workload.Manager`. It is **not yet accepted for deployment**: no target-host runtime was exercised, the example config contains placeholder image/GPU values, the systemd service is deliberately not enabled, and the dashboard/API does not yet instantiate the manager because the allocation intake and trusted principal mapping are not wired end-to-end. Docker-group access is host-root-equivalent; the service must remain disabled until the operator reviews the allowlist and completes target-host acceptance.

See [`SCARLIX_COMPUTE_FABRIC_P5_1_HOST_ACCEPTANCE.md`](SCARLIX_COMPUTE_FABRIC_P5_1_HOST_ACCEPTANCE.md) for the security model, rollout gates, lifecycle matrix, and evidence required from the target host.

## Required next work

1. Add an explicit host service entrypoint and systemd unit running under a dedicated service identity.
2. Decide the transport identity model before granting ScarliHQ access: Unix socket ownership/group and container GID mapping must be least-privilege. Never use world-writable socket permissions.
3. Implement runtime-specific adapters using fixed, typed operations and pre-approved runtime configuration. Do not pass user-supplied command lines, image names, environment maps, or file paths.
4. Add request idempotency/reconciliation keyed by allocation ID so retry after a timeout cannot create duplicate workloads.
5. Complete runtime composition/API wiring without adding privileged operations to ScarliHQ; the `internal/compute` helper now provides the first injection seam, but no production API caller exists yet.
6. Add CI tests, service packaging, installer/rollback handling, and target-host acceptance before release.

## Acceptance gates

- [ ] Unit tests for request validation and ownership boundary.
- [ ] Go tests and race tests pass in CI.
- [ ] No Docker socket mount added to `scarlihq/docker-compose.yml`.
- [ ] No TCP listener or arbitrary command execution.
- [ ] Service identity, socket ACL and lifecycle/rollback documented.
- [ ] Real-host tests for start, readiness, stop, timeout, restart and shared runtime behavior.
