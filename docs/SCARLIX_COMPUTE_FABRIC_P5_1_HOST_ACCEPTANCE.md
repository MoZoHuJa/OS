# ScarLiX Compute Fabric — P5.1 Host Acceptance Runbook

Status: **not accepted on a target host**. This runbook defines the evidence required before enabling a production runtime executor. CI unit/integration tests are not a substitute for these checks.

## Current implementation boundary

The P5.1 branch contains the typed Unix-socket protocol, client implementing `workload.Driver`, a fixed-profile Docker Engine executor, durable owner-only ledger, idempotency/reconciliation wrapper, service entrypoint, systemd unit, installer packaging, and a composition helper that injects the client into `workload.Manager`. The service is packaged but intentionally not enabled; target-host acceptance remains outstanding. Do not enable it with the example placeholder config.

The existing `scarlix-host-bridge.service` and `scarlix-host-bridge.timer` are the dashboard/mode bridge. They are not the P5.1 workload executor and must not be repurposed without a separate review. The new `scarlix-workload-bridge.service` uses a narrow API but belongs to the `docker` group, which is effectively host-root-equivalent; keep it disabled until its config and host acceptance are reviewed.

## Security model required before service installation

1. The workload service must have a dedicated service identity and a fixed, administrator-owned configuration.
2. ScarliHQ must never receive `/var/run/docker.sock`, host root privileges, arbitrary command execution, arbitrary image selection, arbitrary environment maps, or host filesystem paths.
3. The socket parent directory must be administrator-controlled. Never use world-writable socket permissions. The current helper creates mode `0600`; container access requires a deliberate, reviewed UID/GID design rather than relaxing permissions.
4. If the service uses the Docker API, treat access to that API as host-root-equivalent. Expose only the typed API and fixed runtime operations; do not forward raw Docker API requests.
5. Runtime and model choices must resolve through administrator-owned allowlists. GPU identifiers, model paths, images, ports, arguments, mounts, and environment variables must not be freely supplied by the client.
6. Stop must verify that the resource has the expected allocation label/identity and reference before deleting it. A caller-supplied `owned=true` flag alone is not proof of ownership.

## Required idempotency/reconciliation behavior

- The allocation ID is the idempotency key. The durable ledger fingerprint also binds runtime, model, GPU ID, and port. A mismatched assignment must never be treated as a successful retry.
- Repeated Start for the same allocation must discover and return the existing matching workload rather than create another.
- Reusing an allocation ID with a different runtime/model/GPU/port assignment must fail closed.
- A timeout after the runtime created a workload must be reconciled by querying runtime-owned metadata (for example, an allocation label), not by blindly retrying creation.
- Stop must be safe to retry and must positively verify absence/cleanup.
- If the runtime outcome is ambiguous, persist `CLEANUP_REQUIRED` and block blind retries until reconciliation resolves it.
- The runtime adapter must retain this behavior across service restarts. In-memory request locks alone are insufficient.

## Acceptance procedure (run only after a production adapter and service exist)

Record host OS/kernel, Go version, Docker Engine version, NVIDIA driver version, GPU UUIDs, commit SHA, and the exact service/config revisions in the acceptance report.

### Static and CI gates

From the repository root:

```bash
cd scarlihq
go vet ./...
test -z "$(gofmt -l .)"
go test ./...
go test -race ./internal/allocation ./internal/reservation ./internal/workload ./internal/hostbridge ./internal/compute
```

Also verify the CI workflow is green for the exact candidate SHA, and inspect the final diff for any Docker socket mount in ScarliHQ Compose.

### Target-host lifecycle matrix

| Case | Procedure | Required result |
|---|---|---|
| Start | Start an allocation assigned to an explicitly allowlisted runtime/model/GPU | Exactly one resource is created; API reports ready only after positive health/readiness evidence |
| Duplicate Start | Submit the same allocation twice, including concurrently | Same resource reference returned; no duplicate resource |
| Conflicting retry | Reuse allocation ID with a different model/GPU/port | Rejected without side effects |
| Stop | Stop a running allocation-owned workload | Only the exact owned resource is stopped; absence is verified |
| Shared runtime | Stop an allocation marked as sharing a pre-existing runtime | Runtime is not stopped |
| Timeout after create | Interrupt client response after runtime creation, then retry | Adapter reconciles by allocation identity and does not duplicate |
| Service restart | Restart the bridge while a workload is running, then retry Start/Stop | Existing resource is discovered; no duplicate or orphan |
| Runtime unavailable | Stop Docker/runtime or make it unreachable | Fail closed; durable state indicates reconciliation required |
| Socket permissions | Connect from the intended ScarliHQ container identity and from an unauthorized identity | Authorized identity works; unauthorized identity is denied; no world access |
| Malformed request | Try unknown JSON fields, oversized body, invalid IDs, unsupported runtime and extra JSON | Rejected before executor side effects |
| Cleanup verification | Force runtime stop/remove failure | No success response; allocation remains in a cleanup/reconciliation state |

### Evidence and release gate

For each case, attach command output/log excerpt, before/after resource inventory, allocation-store state, and the exact commit SHA. Redact tokens and secrets. A release is blocked until all required cases pass on the intended host and the reviewer confirms that no unrelated or shared workload can be stopped.

## Rollback

- Keep the workload service disabled until the runtime adapter and all acceptance gates are approved.
- On a failed rollout, stop new workload requests first; do not delete containers by broad name/image filters.
- Preserve the allocation ledger and service logs for reconciliation.
- Disable/stop only the new workload service. Do not disable the existing dashboard/mode bridge.
- Remove only resources whose allocation ownership has been positively verified.

## Operator setup (do not enable before acceptance)

1. Ensure the model directory exists as a real, non-symlink directory and is readable by the service identity. The adapter only permits read-only model mounts sourced below `/var/lib/scarlix/models` and mapped below `/models`.
2. Create a dedicated Docker network named `scarlix-compute`. Do not use host networking. Prefer an isolated/internal bridge network after confirming the runtime can operate without outbound internet access.
3. Pre-pull each exact digest-pinned image listed in the active config. The executor does not pull mutable tags or resolve image tags dynamically.
4. Copy the example to `/etc/scarlix/workload-bridge.json`, replace the image digest and GPU UUID placeholders, ensure every profile has explicit CPU, memory, PID, shared-memory, port, network and health-check settings, then install it root-owned and not writable by group/other (for example root:docker mode 0640).
5. Confirm `/var/run/docker.sock` is root-owned and group-accessible to `docker`. The bridge service account is in the Docker group; this is host-root-equivalent privilege and must be treated as such.
6. The installer packages the binary, unit and example config but does **not** enable the service or install the example as active config. Keep `scarlix-workload-bridge.service` disabled until the review and host lifecycle matrix pass.
7. Do not mount `/var/run/docker.sock` into ScarliHQ. Do not mount the workload socket into ScarliHQ until the authenticated allocation/principal API is wired and reviewed.

The current executor conservatively permits only one active allocation per logical GPU and per host port. A `STARTING`, `RUNNING`, or `RECONCILE_REQUIRED` ledger entry continues reserving those resources until reconciliation positively marks it stopped. CPU/memory/PID limits are per workload; aggregate resource admission and reservation release must be integrated before multi-workload production use.
