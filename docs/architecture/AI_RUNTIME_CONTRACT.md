# ScarLiX OS — Universal AI Runtime Contract (v19.6.8)

> **Status:** CANONICAL — the provider set, transport boundary, failover
> and budget contract of the Universal AI Runtime, as implemented in
> v19.6.8. Source of truth: `scarlihq/internal/{airuntime,airt}` +
> `internal/api/airuntime.go` + `internal/api/aipropose.go`. Engineering
> history: `docs/AI_RUNTIME_CF1/CF2/CF3_DESIGN.md` (living per-phase
> docs).

------------------------------------------------------------------------

## 1. Provider set (CLOSED)

Four providers, deliberately closed (`airuntime.Provider`):

| ID | Engine | Placement | Role |
|---|---|---|---|
| `sglang` | SGLang | GPU 0 | Primary inference runtime (Qwen3-14B-AWQ, 32K ctx, `mem-fraction-static` ≈ 0.80, chunked prefill) |
| `vllm` | vLLM | GPU 1 | Experimental |
| `beellama` | BeeLlama / llama.cpp | CPU | CPU path |
| `ollama` | Ollama | CPU | CPU fallback |

Adding a backend is a deliberate multi-surface change (identity, closed
list + validation, capability metadata, health probing, adapter
construction, failover policy, budgets, API schemas, MCP contracts,
config validation, tests, docs, threat model) — never "just a constant".
The roadmap's InferSSD (v19.8.0) will follow exactly this list.

## 2. Transport boundary (`internal/airt`)

The package has exactly ONE `http.Client` constructor, and it is
local-only at three layers:

1. **No redirects** — `http.ErrUseLastResponse`; a 307/308 is handed back
   AS the response and classified as the failure it is (a redirect would
   replay the POST body — the whole normalized conversation — onto an
   unvalidated destination).
2. **No ambient proxy** — `Proxy: nil`; `HTTP_PROXY`/`HTTPS_PROXY` never
   steer local AI traffic.
3. **Dial-guarded + address-pinned** — a configured name is RESOLVED
   through the process resolver, EVERY answered address is validated
   against the trusted local networks, and the connection is PINNED to a
   validated address (no validate-then-resolve TOCTOU gap). A MIXED
   answer set is refused WHOLE. Empty/failed resolutions refuse
   fail-closed. IP literals validate directly; loopback is always
   trusted. Non-loopback trust is the operator declaration
   `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS` (comma-separated CIDRs, parsed
   fail-closed at startup; `0.0.0.0/0`/`::/0` are REFUSED as universal
   routes). Honest residual: lateral movement INSIDE a declared trusted
   network still passes (CIDR trust cannot distinguish hosts within the
   declared net); the IPv6 dual-stack mixed-answer rule applies.

## 3. Failover + budgets

- **One end-to-end budget:** `airt.ChatBudget` = per-attempt × the closed
  four-tier set; sequential attempts SHARE it
  (`SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT`, operator-tunable within the named
  300s bound, validated fail-closed). Expiry is the DELIBERATE 504
  (`ErrBudgetExpired`), never a generic 502 masquerade; the write path
  extends beyond the budget + 10s margin so a legitimate slow local
  generation is WRITTEN.
- **Health honesty:** probe rounds commit through
  `HealthTable.ObserveBatch` — the whole round validates, monotonicity-
  checks and applies under ONE lock; the commit boundary is
  context-aware (cancellation BEFORE the commit records NOTHING,
  cancellation AFTER does not retroactively undo a completed round). A
  cancelled round is not evidence about the providers; last-known-good
  states and timestamps stand.
- **Enforced monthly budgets (v19.6.7):** `/api/ai/chat`
  reserves-before-forward against the profile's durable ledger (0600/0700,
  atomic tmp+rename, tamper fail-closed); settles to the provider-reported
  usage; failed attempts are released whole; exhaustion is a stable 429
  with the quota state; ledger unavailability fails CLOSED (503) when a
  budget applies. Degraded (no ledger at the default path) is loudly
  warned informational — an explicit
  `SCARLIHQ_AIRUNTIME_BUDGET_LEDGER` that fails ABORTS STARTUP.

## 4. Proposal path (CF-3)

`POST /api/ai/propose` runs the unchanged CF-2 failover chat and captures
a well-formed ` ```scarlix-proposal ` block as a PENDING proposal record
(pure deterministic parser; malformed blocks are refused with attribution
— never a guess). **The operator's accept is the only door into the agent
fabric** (`SubmitWithProposal`, durable `proposal_id` linkage). Both
opt-ins fail closed (503 without the runtime OR the fabric). Both
surfaces are admin-gated (v19.6.7 identity model).

## 5. Identity model at this boundary

- Family surfaces (`/api/ai/chat`): per-profile Bearer tokens; the
  profile policy (permissions, mode policy, content filter, budget)
  applies — declared-or-derived, never bypassable.
- Operator surfaces (`/api/ai/probe`, `/api/ai/propose`,
  `/api/ai/proposals/*`, `/api/ai/models` POST): the ADMIN principal
  (household token or `role:admin` profile token).
- Read-only surfaces (`/api/ai/health`, `/api/ai/models` GET): family
  tokens.

## 6. Roadmap alignment

The runtime's future (per
[`docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`](../roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md)):
backend capability contracts (v19.7.2), InferSSD as a fifth backend behind
a tested profile (v19.8.0), SGLang Expert Pack as a SGLang
artifact/capability mode — never a sixth provider. The transport stays
network-free in `airuntime`; all I/O stays in `airt` adapters.
