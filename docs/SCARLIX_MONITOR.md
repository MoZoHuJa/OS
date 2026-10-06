# SCARLIX Monitor — Read-Only System Snapshot (v19.1.4)

> **ScaRgeN master guide §14 — v19.1.4 ScarliMonitor Foundation.**
> Add a read-only monitoring service/API that exposes GPU, CPU, RAM, storage,
> runtime health, model health, and service health. **No control operations.**

## 1. Purpose

ScarliMonitor is a **read-only** monitoring service that produces a
point-in-time `Snapshot` of the entire SCARLIX OS host: GPUs, CPU, RAM,
storage, inference runtimes, model catalog, services, and component health.

It is **observation only**. ScarliMonitor never:

- Starts or stops containers.
- Resets GPUs or unloads models.
- Switches scarlix-mode.
- Writes to disk (except its own stdout / future HTTP response body).
- Mutates any system state.

The foundation (v19.1.4) ships the snapshot collection library + a standalone
CLI. Future versions add an HTTP API (v19.1.5), telemetry history persistence
(v19.1.5), and an in-dashboard resource view (v19.1.6).

## 2. Architecture

ScarliMonitor **composes** the existing inventory package collectors rather
than re-implementing them. Single source of truth, two consumption paths
(`scarlix-inventory` for the raw inventory + `scarlix-monitor` for the unified
snapshot with system reads).

```
                  ┌─────────────────────────────────────────┐
                  │           ScarliMonitor                 │
                  │       (internal/monitor)               │
                  └─────────────────────────────────────────┘
                          │             │
            inventory     │             │  /proc + /sys
            collectors    │             │  reads
                          ▼             ▼
   ┌──────────────────────────┐   ┌──────────────────────────┐
   │  internal/inventory       │   │  readCPU()              │
   │  ├─ CollectGPUs()        │   │  ├─ /proc/cpuinfo       │
   │  ├─ CollectRuntimes()    │   │  ├─ /proc/loadavg       │
   │  ├─ CollectModels()      │   │  └─ /proc/stat          │
   │  ├─ CollectGPUHealth()   │   │  readRAM()              │
   │  └─ CollectRuntimeHealth │   │  └─ /proc/meminfo        │
   └──────────────────────────┘   │  readStorage()          │
                                  │  ├─ os.Stat("/models")   │
                                  │  ├─ df -P -m /models     │
                                  │  ├─ df -P -m /           │
                                  │  └─ filepath.WalkDir    │
                                  └──────────────────────────┘
```

Both code paths gracefully degrade — every read is best-effort. A missing
`nvidia-smi`, no Docker daemon, no `models.yaml`, no `/models` directory,
or even a non-Linux host (where `/proc` is absent) yield zero-valued fields
in the snapshot, never an error and never a panic.

## 3. Snapshot Schema

The `Snapshot` struct is the wire-format contract. Field names are FROZEN in
v19.1.4 — future versions may ADD fields (with `omitempty` where it makes
sense) but MUST NOT rename or remove existing ones. Same stability contract
as `docs/SCARLIX_DATA_CONTRACTS.md` §7.

| Field        | Type                  | Source                                   |
|--------------|-----------------------|------------------------------------------|
| `timestamp`  | string (RFC 3339 UTC) | `time.Now().UTC()`                       |
| `version`    | string                | `/etc/scarlix/VERSION` (read by caller)  |
| `mode`       | string                | `/var/lib/scarlix/current-mode`         |
| `gpus`       | `[]inventory.GPU`    | `inventory.CollectGPUs()`                |
| `cpu`        | `CPUInfo`             | `/proc/cpuinfo` + `/proc/loadavg` + `/proc/stat` |
| `ram`        | `RAMInfo`             | `/proc/meminfo`                          |
| `storage`    | `StorageInfo`         | `df -P -m` + `filepath.WalkDir`          |
| `runtimes`   | `[]inventory.Runtime` | `inventory.CollectRuntimes()`            |
| `models`     | `[]inventory.Model`   | `inventory.CollectModels()`             |
| `services`   | `[]inventory.Service` | Empty in v19.1.4 (no collector yet)     |
| `health`     | `[]inventory.Health`  | GPU health + runtime health (combined)  |

### `CPUInfo`

| Field        | Type    | Source                                  |
|--------------|---------|-----------------------------------------|
| `cores`      | int     | count of `processor` lines in `/proc/cpuinfo` |
| `model_name` | string  | first `model name` in `/proc/cpuinfo`   |
| `load_avg_1m`| float64 | first field of `/proc/loadavg`          |
| `usage_pct`  | float64 | cumulative `(1 - idle/total) * 100` from `/proc/stat` (since boot) |

### `RAMInfo`

| Field          | Type | Source / formula                          |
|----------------|------|-------------------------------------------|
| `total_mb`     | int  | `MemTotal` from `/proc/meminfo` (KiB→MiB) |
| `used_mb`      | int  | `Total - Available`                       |
| `free_mb`      | int  | `MemFree` from `/proc/meminfo`             |
| `available_mb` | int  | `MemAvailable` from `/proc/meminfo`        |

### `StorageInfo`

| Field             | Type   | Source                                |
|-------------------|--------|---------------------------------------|
| `models_dir`      | string | hardcoded `/models`                   |
| `models_total_mb` | int    | `filepath.WalkDir("/models")` sum     |
| `models_free_mb`  | int    | `df -P -m /models` Available column   |
| `root_free_mb`     | int    | `df -P -m /` Available column         |

## 4. CLI Usage

```
scarlix-monitor                  # print a Snapshot as pretty JSON, exit 0
scarlix-monitor --once           # same as default (explicit)
scarlix-monitor --serve <port>   # STUB — prints "HTTP server mode coming in v19.1.5", exit 0
scarlix-monitor -h, --help       # print usage + exit 0
```

Exit codes: `0` success · `1` JSON encode failure (should never happen) ·
`2` flag parse error.

Build:

```
cd scarlihq && CGO_ENABLED=0 go build -o /usr/local/bin/scarlix-monitor ./cmd/scarlix-monitor
```

Sample output (sandbox without GPU/Docker/models):

```json
{
  "timestamp": "2026-10-06T09:52:36Z",
  "version": "unknown",
  "mode": "",
  "gpus": [],
  "cpu": { "cores": 2, "model_name": "Intel(R) Xeon(R) Processor",
           "load_avg_1m": 0.02, "usage_pct": 1.47 },
  "ram": { "total_mb": 4041, "used_mb": 1372, "free_mb": 1171, "available_mb": 2669 },
  "storage": { "models_dir": "/models", "models_total_mb": 0, "models_free_mb": 0, "root_free_mb": 7463 },
  "runtimes": [ ... 5 entries ... ],
  "models": [],
  "services": [],
  "health": [ ... runtime health entries ... ]
}
```

## 5. What's NOT in v19.1.4

| Capability                 | Target version | Notes                                  |
|----------------------------|----------------|----------------------------------------|
| HTTP server (`--serve`)    | v19.1.5        | `--serve` flag exists as a stub in v19.1.4 |
| Telemetry history persistence | v19.1.5     | Persist snapshots every N seconds to disk |
| Alerting / thresholds      | future         | Out-of-scope for v19.1.x foundation     |
| Service health collector  | future         | `services` array is empty `[]` in v19.1.4 |
| Multi-host aggregation    | future         | Single-host only in v19.1.x            |
| Authentication            | v19.1.5+       | HTTP API will need at least token auth |

## 6. Integration

`scarlix-monitor` is added to `install.sh` Phase 4 alongside the other
SCARLIX Go binaries:

| Binary                  | Added in | Purpose                            |
|-------------------------|----------|------------------------------------|
| `scarlix-bridge-reader` | v18.7.4  | Atomic file reader for host-bridge |
| `scarlix-gpu`           | v19.0.8  | GPU telemetry JSON                 |
| `scarlix-inventory`     | v19.0.9  | Full inventory JSON                |
| `scarlix-contract`      | v19.1.0  | Resource Contract v1 CLI           |
| **`scarlix-monitor`**   | **v19.1.4** | **Read-only system snapshot CLI** |

Build pattern: native Go (preferred) → Docker `golang:1.23-alpine` container
fallback → `warn` (non-critical) if neither is available. Copied to
`/usr/local/bin/scarlix-monitor` (root:root, mode 755) in the same binary
copy loop as the other Go binaries.

`scarlix-monitor` is a standalone tool — the bash `scarlix` CLI does **not**
shell out to it (yet). Future integration (v19.1.6 ScarliHQ resource view)
will likely expose the snapshot at `GET /api/monitor` via the ScarliHQ HTTP
server, backed by the `monitor` package.

## 7. Relationship to `scarlix-inventory`

`scarlix-inventory` emits `inventory.SystemStatus` — the **inventory**
view (what the system HAS). `scarlix-monitor` emits `monitor.Snapshot` —
the **monitoring** view (what the system IS DOING, including CPU/RAM/storage
that the inventory package doesn't cover).

Both compose the same `inventory.Collect*` functions, so GPU/runtime/model
entries are byte-identical between the two. `monitor.Snapshot` adds:

- `cpu`, `ram`, `storage` — system observability not in the inventory scope.
- `timestamp` — top-level RFC 3339 UTC (inventory has it on `SystemStatus`
  too — same convention).
- Future (v19.1.5+): `services` populated via a future `CollectServices()`.
