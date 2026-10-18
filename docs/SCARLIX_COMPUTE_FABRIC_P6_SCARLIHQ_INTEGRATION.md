# ScarLiXoS v19.2.14 — P6 integration candidate

This candidate wires the existing workload lifecycle manager into authenticated ScarliHQ REST endpoints while keeping execution behind the Unix-socket Host Bridge.

## Enablement

Compute lifecycle remains disabled by default. For a **single-principal deployment only**, set:

- `SCARLIHQ_COMPUTE_ENABLED=true`
- `SCARLIHQ_COMPUTE_SOCKET=/run/scarlix-workload-bridge/workload.sock` (must match the host bridge configuration)
- `SCARLIHQ_ALLOCATION_STORE=/var/lib/scarlix/allocations.jsonl`
- `SCARLIHQ_COMPUTE_PRINCIPAL=<trusted owner identifier already stored on allocations>`

The current ScarliHQ API uses one shared bearer token, so this principal mapping must not be treated as per-user authentication. Do not enable these routes in a multi-user deployment until authenticated caller identities are mapped to allocation owners.

## Endpoints

Both routes require the existing `Authorization: Bearer` token and accept one bounded JSON object:

- `POST /api/compute/start` with `{"allocation_id":"..."}`
- `POST /api/compute/stop` with `{"allocation_id":"..."}`

Start accepts only an existing allocation in `RESERVED` with its immutable resource assignment already persisted. It does not plan or reserve resources on behalf of a caller. Host Bridge calls remain typed and Unix-socket-only.

Stop requests host cleanup and reports success only after positive cleanup verification. **It does not mark the reservation bundle RELEASED.** The allocation remains `RELEASING`; the existing reservation manager's trusted cleanup-verifier/reconciler workflow must finish release before those resources can be reused. Never free a GPU or port merely because the stop endpoint returned.

## Candidate gates

CI must pass for the exact candidate SHA. This is a virtual integration increment, not proof of target-host Docker/NVIDIA/model acceptance. The service remains opt-in and must not be enabled with the example placeholder config.
