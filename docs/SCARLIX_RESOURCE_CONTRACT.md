# SCARLIX OS v19.1.0 — Resource Contract v1

> Purpose: Single source of truth for the **Resource Contract v1** — the
> canonical schema for compute resource **requests** that agents submit to
> the (future) scheduler. Introduced in v19.1.0 (ScaRgeN master guide §10).
>
> Scope: **request side**. Agents declare what they NEED. The scheduler
> (v19.2.x Compute Fabric) will match these requests against the
> inventory package's STATE and emit allocation responses. For v19.1.0 the
> contract is **defined + parseable + serializable**, but NOT yet enforced
> by a scheduler.

## 1. Purpose

The Resource Contract is the canonical request schema agents submit to
obtain compute resources for a task. It captures six facets of an agent
request:

| Facet     | Field     | Question answered |
|---|---|---|
| Identity  | `id`, `agent_id`, `version` | Who is asking, under which schema version? |
| Task      | `task`     | What does the agent want to do, at what priority? |
| Compute   | `compute`  | What hardware (accelerator + VRAM + CPU + RAM)? |
| Runtime   | `runtime`  | Which inference runtimes are preferred? |
| Model     | `model`    | What model capabilities are required? |
| Security  | `security` | What filesystem / network / shell scope is needed? |

The contract is the **REQUEST** half of the resource model. The
**RESPONSE** half (allocations — granted GPU IDs, port assignments,
runtime selection, allocation IDs) comes in v19.2.x (Compute Fabric).

## 2. Schema (canonical example)

This is the example shipped at `scarlihq/internal/contract/example.yaml`
and produced by `scarlix-contract example`:

```yaml
version: v1
id: 550e8400-e29b-41d4-a716-446655440000
agent_id: agent.coder

task:
  type: coding
  priority: interactive

compute:
  accelerator: cuda
  vram_mb: 12000
  cpu_cores: 4
  ram_mb: 8192

runtime:
  preferred:
    - sglang
    - vllm

model:
  capabilities:
    - coding
    - reasoning

security:
  filesystem: workspace
  network: restricted
  shell: sandbox
```

The same contract as JSON:

```json
{
  "version": "v1",
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "agent_id": "agent.coder",
  "task":     { "type": "coding", "priority": "interactive" },
  "compute":  { "accelerator": "cuda", "vram_mb": 12000, "cpu_cores": 4, "ram_mb": 8192 },
  "runtime":  { "preferred": ["sglang", "vllm"] },
  "model":    { "capabilities": ["coding", "reasoning"] },
  "security": { "filesystem": "workspace", "network": "restricted", "shell": "sandbox" }
}
```

## 3. Field reference

### 3.1 Top-level (`ResourceContract`)

| Field       | Type              | JSON tag    | Description |
|---|---|---|---|
| Version     | `ContractVersion` | `version`   | Schema version. v1 in v19.1.0. Frozen. |
| ID          | `string`          | `id`        | Request UUID (RFC 4122). Agent-generated. |
| AgentID     | `string`          | `agent_id`  | Agent identifier, e.g. `agent.coder`, `agent.researcher`. |
| Task        | `TaskSpec`        | `task`      | What the agent wants to do + at what priority. |
| Compute     | `ComputeSpec`     | `compute`   | Hardware resources requested. |
| Runtime     | `RuntimeSpec`     | `runtime`   | Preferred inference runtimes (ordered). |
| Model       | `ModelSpec`       | `model`     | Required model capabilities. |
| Security    | `SecuritySpec`    | `security`  | Security scope (filesystem / network / shell). |

### 3.2 `TaskSpec`

| Field    | Type     | JSON tag  | Allowed values |
|---|---|---|---|
| Type     | `string` | `type`     | `coding`, `chat`, `research`, `embedding`, `indexing` (extensible) |
| Priority | `string` | `priority` | `realtime`, `interactive`, `normal`, `background`, `batch` (extensible) |

### 3.3 `ComputeSpec`

| Field       | Type     | JSON tag       | Description |
|---|---|---|---|
| Accelerator | `string` | `accelerator` | `cuda`, `cpu`, `rocm` (extensible) |
| VRAMMB       | `int`    | `vram_mb`     | Requested VRAM in MiB. 0 = "do not reserve VRAM". |
| CPUCores     | `int`    | `cpu_cores`   | Requested CPU cores. 0 = "do not reserve CPU". |
| RAMMB        | `int`    | `ram_mb`      | Requested RAM in MiB. 0 = "do not reserve RAM". |

### 3.4 `RuntimeSpec`

| Field     | Type       | JSON tag    | Description |
|---|---|---|---|
| Preferred | `[]string` | `preferred` | Ordered list of runtime IDs (`sglang`, `vllm`, `beellama`, `ollama`, `litellm`, `smg`). Empty = no preference. Serializes as `[]` not `null`. |

### 3.5 `ModelSpec`

| Field        | Type       | JSON tag       | Description |
|---|---|---|---|
| Capabilities | `[]string` | `capabilities` | Unordered list of capability tags (`coding`, `reasoning`, `chat`, `embed`, `vision`, `tools`). Serializes as `[]` not `null`. |

### 3.6 `SecuritySpec`

| Field      | Type     | JSON tag     | Allowed values |
|---|---|---|---|
| Filesystem | `string` | `filesystem` | `workspace`, `none` (extensible) |
| Network    | `string` | `network`    | `restricted`, `none` (extensible) |
| Shell      | `string` | `shell`      | `sandbox`, `none` (extensible) |

## 4. Allowed enum values (extensible)

These are the values defined in v19.1.0. Consumers MUST tolerate unknown
values (forward compat — same rule as `inventory` package): a future
v19.3.x may add `TaskTypeVision`, `AcceleratorIntel`, `FilesystemScopeHost`
etc., and old parsers must not reject them.

### 4.1 Task types (`task.type`)
- `coding` — code generation / refactoring
- `chat` — conversational interaction
- `research` — long-running research / web crawling
- `embedding` — embedding generation (offline batch)
- `indexing` — knowledge base indexing

### 4.2 Priorities (`task.priority`)
Order from highest to lowest scheduling weight:
1. `realtime` — preempts everything (e.g. interactive voice)
2. `interactive` — user-facing, low-latency target
3. `normal` — default
4. `background` — opportunistic
5. `batch` — offline batch jobs

### 4.3 Accelerators (`compute.accelerator`)
- `cuda` — NVIDIA GPU
- `cpu` — no GPU (CPU-only inference)
- `rocm` — AMD GPU

### 4.4 Filesystem scopes (`security.filesystem`)
- `workspace` — read/write to the agent's workspace directory only
- `none` — no filesystem access

### 4.5 Network scopes (`security.network`)
- `restricted` — outbound only to whitelisted hosts (LiteLLM gateway, package mirrors)
- `none` — fully offline

### 4.6 Shell scopes (`security.shell`)
- `sandbox` — shell available inside the agent's sandbox (bwrap/firejail)
- `none` — no shell access

## 5. Stability contract (FROZEN in v19.1.0)

Field names in JSON + YAML tags are **FROZEN** as of v19.1.0. The
following rules apply to all future versions until v2 (explicit
migration):

1. **No renames.** `vram_mb` stays `vram_mb`; `agent_id` stays `agent_id`.
2. **No removals.** A field may not be deleted; if unused, retain + emit as zero value.
3. **Additions allowed.** New fields may be appended; use `omitempty` if optional; must not reuse a previously-removed name.
4. **Types are stable.** `int`→`int`, `string`→`string`, `[]string`→`[]string`. A type change requires a rename + migration.
5. **Array vs. null.** `runtime.preferred` and `model.capabilities` MUST serialize as `[]` when empty, never `null`. Producers must initialize non-nil slices (the `contract` package's `normalize()` helper does this automatically).
6. **Enum extensibility.** Enum value sets may be EXTENDED additively. Consumers MUST tolerate unknown values (forward compat).
7. **Version field.** `version` MUST be `"v1"` for v19.1.x. A v2 schema (if ever introduced) will use `"v2"` and require an explicit migration.

## 6. CLI usage

The `scarlix-contract` binary (built from `scarlihq/cmd/scarlix-contract`)
provides three operations:

```sh
# Print the canonical example contract as YAML
scarlix-contract example

# Print the canonical example contract as JSON
scarlix-contract example --json

# Validate a contract file (YAML or JSON by extension)
scarlix-contract validate path/to/contract.yaml
# → prints "valid" on success, error details on failure, exit 0/1

# Parse + emit normalized JSON (canonical field order, [] not null)
scarlix-contract parse path/to/contract.yaml
```

Exit codes: `0` success, `1` parse/validation error, `2` usage error.

## 7. Relationship to the `inventory` package

| Package | Role | Side | Introduced | Used by |
|---|---|---|---|---|
| `internal/contract` | REQUEST — what the agent WANTS | request | v19.1.0 | future scheduler (v19.2.x), agents (Pi-Bolt + future) |
| `internal/inventory` | STATE — what the system HAS | response/state | v19.0.7 (GPU), v19.0.8 (GPU health), v19.0.9 (Runtime + Model) | future ScarliHQ resource view (v19.1.6), future scheduler (v19.2.x) |

The future scheduler (v19.2.x Compute Fabric) will:

1. Read pending `ResourceContract` requests from a queue.
2. Read current `inventory.SystemStatus` state.
3. Match each contract's `compute.accelerator` against inventory GPU vendors (nvidia → cuda, amd → rocm).
4. Match each contract's `compute.vram_mb` against inventory GPU free VRAM.
5. Match each contract's `runtime.preferred` against inventory runtime IDs (running + healthy).
6. Match each contract's `model.capabilities` against inventory model capabilities.
7. Enforce the contract's `security` scope via the `guard` package.
8. Emit an allocation response (granted GPU IDs, port, runtime, allocation ID, expiry timestamp) — the response schema is defined in v19.2.x.

For v19.1.0, the contract is parseable + serializable + documented, but
no scheduler exists yet. Agents may already start authoring contracts;
they will be honored starting v19.2.x.

---

**Baseline:** This document is part of the SCARLIX OS v19.1.0 release.
The schema above is the canonical Resource Contract v1. Field names are
frozen. See the ScaRgeN master coding guide section 10 for the original
specification.
