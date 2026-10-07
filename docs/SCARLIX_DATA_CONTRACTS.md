# SCARLIX OS v19.0.7 — Data Contracts

> Purpose: Single source of truth for the JSON schema of every normalized
> observability structure in v19.0.7. The bash `scarlix` CLI **must** emit JSON
> matching these schemas when invoked with `--json`. ScarliHQ's Go code in
> `scarlihq/internal/inventory` serializes Go structs whose tags match these
> schemas exactly. The future compute fabric (v19.2.x) consumes them.
>
> Scope: **read-only observability**. These structures describe state. They do
> NOT control anything — no mode switch, no container start/stop, no GPU reset.

## 1. Top-level: `SystemStatus`

Root object emitted by `scarlix --json` (planned) and exposed by ScarliHQ at
`GET /api/inventory` (planned v19.1.6).

| Field | Type | JSON tag | Notes |
|---|---|---|---|
| Version | string | `version` | SCARLIX OS version, e.g. `"19.0.7"` |
| Mode | string | `mode` | current scarlix-mode: `ai`/`turbo`/`creative`/`game`/`tv`/`stop`/`offline` |
| UptimeSeconds | int64 | `uptime_seconds` | host uptime |
| Timestamp | string | `timestamp` | ISO 8601 UTC, RFC 3339 |
| GPUs | []GPU | `gpus` | empty array `[]`, never `null` |
| Runtimes | []Runtime | `runtimes` | empty array `[]`, never `null` |
| Models | []Model | `models` | empty array `[]`, never `null` |
| Services | []Service | `services` | empty array `[]`, never `null` |
| Health | []Health | `health` | empty array `[]`, never `null` |

## 2. `GPU` (ScaRgeN §8)

| Field | Type | JSON tag | Example |
|---|---|---|---|
| ID | string | `id` | `"gpu.nvidia.0"` |
| Index | int | `index` | `0` |
| Vendor | string | `vendor` | `"nvidia"` |
| Name | string | `name` | `"RTX 5060 Ti"` |
| VRAMTotalMB | int | `vram_total_mb` | `16384` |
| VRAMUsedMB | int | `vram_used_mb` | `13600` |
| VRAMFreeMB | int | `vram_free_mb` | `2784` |
| UtilizationPct | int | `utilization_percent` | `42` |
| TemperatureC | int | `temperature_c` | `68` |
| PowerW | float64 | `power_w` | `165.5` |
| Driver | string | `driver` | `"570.86.15"` |
| CUDA | string | `cuda` | `"12.8"` |
| ComputeCap | string | `compute_cap` | `"12.0"` (Blackwell sm_120) |
| Healthy | bool | `healthy` | `true` |

```json
{"id":"gpu.nvidia.0","index":0,"vendor":"nvidia","name":"RTX 5060 Ti",
 "vram_total_mb":16384,"vram_used_mb":13600,"vram_free_mb":2784,
 "utilization_percent":42,"temperature_c":68,"power_w":165.5,
 "driver":"570.86.15","cuda":"12.8","compute_cap":"12.0","healthy":true}
```

## 3. `Runtime` (ScaRgeN §12)

| Field | Type | JSON tag | Example |
|---|---|---|---|
| ID | string | `id` | `"sglang"` |
| Version | string | `version` | `"v0.4.9.post6-cu128-b200"` |
| Enabled | bool | `enabled` | `true` |
| Running | bool | `running` | `true` |
| Healthy | bool | `healthy` | `true` |
| Protocol | string | `protocol` | `"openai-compatible"` |
| Port | int | `port` | `30000` |
| GPUIDs | []string | `gpu_ids` | `["gpu.nvidia.0"]` |
| Image | string | `image` | `"lmsysorg/sglang:v0.4.9.post6-cu128-b200"` |
| Capabilities | []string | `capabilities` | `["chat","tools"]` |

Known ID values: `sglang`, `vllm`, `beellama`, `ollama`, `litellm`, `smg`.
Known ports: 30000/8089/11438/11435/4001/4000 respectively.

```json
{"id":"sglang","version":"v0.4.9.post6-cu128-b200","enabled":true,"running":true,
 "healthy":true,"protocol":"openai-compatible","port":30000,
 "gpu_ids":["gpu.nvidia.0"],"image":"lmsysorg/sglang:v0.4.9.post6-cu128-b200",
 "capabilities":["chat","tools"]}
```

## 4. `Model` (ScaRgeN §9)

| Field | Type | JSON tag | Example |
|---|---|---|---|
| ID | string | `id` | `"qwen3-14b-awq"` |
| Path | string | `path` | `"/models/Qwen3-14B-AWQ"` |
| Format | string | `format` | `"safetensors"` or `"gguf"` |
| Quantization | string | `quantization` | `"awq"`, `"q4_k_m"`, `"fp16"` |
| Parameters | string | `parameters` | `"14B"` |
| ContextLength | int | `context_length` | `32768` |
| EstimatedVRAM | int | `estimated_vram_mb` | `13600` |
| Capabilities | []string | `capabilities` | `["coding","reasoning","chat"]` |
| SupportedRuntimes | []string | `supported_runtimes` | `["sglang","vllm","llamacpp"]` |
| Present | bool | `present` | `true` if file exists on disk |

```json
{"id":"qwen3-14b-awq","path":"/models/Qwen3-14B-AWQ","format":"safetensors",
 "quantization":"awq","parameters":"14B","context_length":32768,
 "estimated_vram_mb":13600,"capabilities":["coding","reasoning","chat"],
 "supported_runtimes":["sglang","vllm"],"present":true}
```

## 5. `Service`

| Field | Type | JSON tag | Notes |
|---|---|---|---|
| ID | string | `id` | `"scarlihq"`, `"sglang"`, `"scarlix-host-bridge"` |
| Name | string | `name` | human-readable |
| Type | string | `type` | `"docker"`, `"systemd"`, `"binary"` |
| Status | string | `status` | `"running"`, `"stopped"`, `"failed"`, `"unknown"` |
| Port | int | `port,omitempty` | omitted when 0 |
| ContainerID | string | `container_id,omitempty` | docker short ID |
| Uptime | string | `uptime,omitempty` | human duration, e.g. `"1h0m0s"` |

```json
{"id":"scarlihq","name":"ScarliHQ dashboard","type":"docker","status":"running",
 "port":8090,"container_id":"a1b2c3d4","uptime":"1h0m0s"}
```

## 6. `Health`

| Field | Type | JSON tag | Notes |
|---|---|---|---|
| Component | string | `component` | matches Runtime.ID or `"gpu.0"` |
| State | string | `state` | `healthy`/`unhealthy`/`starting`/`down`/`unknown` |
| Message | string | `message,omitempty` | optional detail |
| CheckedAt | string | `checked_at` | ISO 8601 UTC, RFC 3339 |

```json
{"component":"sglang","state":"healthy","checked_at":"2025-01-01T00:00:00Z"}
```

## 7. Stability contract (FROZEN in v19.0.7)

Every JSON field name in sections 1–6 is **FROZEN** as of v19.0.7. The
following rules apply to all future versions until v2 (explicit migration):

1. **No renames.** `vram_total_mb` stays `vram_total_mb`; `compute_cap` stays `compute_cap` (not `compute_capability`).
2. **No removals.** A field may not be deleted; if unused, retain + emit as zero value.
3. **Additions allowed.** New fields may be appended; use `omitempty` if optional; must not reuse a previously-removed name.
4. **Types are stable.** `int`→`int`, `float64`→`float64`, `bool`→`bool`. A type change requires a rename + migration.
5. **Array vs. null.** All `SystemStatus` arrays (`gpus`, `runtimes`, `models`, `services`, `health`) must serialize as `[]` when empty, never `null`. Producers must initialize non-nil slices.
6. **Timestamps.** All `*_at` / `timestamp` fields are RFC 3339 UTC (`Z`).
7. **Unknown enum values.** Consumers MUST tolerate unknown enum values in `Vendor`, `Protocol`, `Type`, `Status`, `State` (forward compat).

> **Baseline:** This document is part of the v19.0.7 release. The schemas above are the canonical contract for `scarlix --json` output, ScarliHQ serialization, and future compute-fabric consumption. Field names are frozen.
