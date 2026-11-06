# ScarLiX OS — Universal AI Runtime CF-1 Design (v19.6.x line — OPEN)

**Branch:** `ai-runtime/cf1-foundation` → merged to main as **v19.6.0**
**Status:** CF-1 complete — Phase C opens (the typed foundation, UNWIRED by
the AK-1 precedent: the contract is established and tested first; adapters,
probing and the REST surface are CF-2+, after review)
**Strategy source:** `docs/ScaRgeN_CuLT_Strategy_Obsidian_Lattice_SK_2026-10.pdf`
(pages 13, 22, 24) · `docs/PHASE_B_ACCEPTANCE.md` §5 (the release decision
that opened this phase) · the pre-v19.2 roadmap slot "normalized adapters,
model router, failover"

---

## 1. Where this fits

Phase B closed the deterministic fabric (v19.5.4): command lifecycle,
workspaces, runtimes, operator approvals — nothing in it contains an LLM,
deliberately. Phase C builds the layer that will PROPOSE to that fabric.
The charter is the same line that closed Phase B: *"LLM navrhuje, politika
rozhoduje"* — the LLM proposes; policy decides. The fabric + kernel remain
the deciding, auditable path; the runtime is the normalized LLM layer above
them that turns model traffic into attributable, bounded, routable
conversations.

CF-1 lands the typed foundation of that layer only — the AK-1 precedent
applied to a new theme: identity/enumeration/contract/state first, wiring
later, after review. No user-facing surface changes in this release.

## 2. Sub-phase plan (each with green CI + smoke before merge)

| Phase | Content | Status |
|---|---|---|
| **CF-1** | Typed foundations: closed provider enumeration (the REAL local stack, v19.0.6-locked failover order), bounded explicit model registry, observed fail-closed health, normalized chat contract, pure deterministic router | **DONE — merged as v19.6.0** |
| **CF-2** | Real adapters (SGLang/vLLM/BeeLlama/Ollama behind the contract) + health probing + the auth-gated REST surface (opt-in) | **DONE — merged as v19.6.1** (design: `docs/AI_RUNTIME_CF2_DESIGN.md`) |
| **CF-3** | Propose surface: an LLM response becomes a fabric PROPOSAL — never an auto-submitted command | **DONE — merged as v19.6.2** (design: `docs/AI_RUNTIME_CF3_DESIGN.md`) |
| **CF-4** | Runtime audit + dashboard (the operator's view into the routing layer) | planned |
| **CF-5** | Phase closure (the acceptance gate: cross-release coherence, honest ledger) | planned |

## 3. CF-1 contract (as implemented in `internal/airuntime`)

### Providers — a CLOSED enumeration of the REAL local stack
`sglang` (Tier 1, GPU 0) · `vllm` (Tier 2, GPU 1, experimental) ·
`beellama` (Tier 3, CPU llama.cpp) · `ollama` (Tier 4, CPU fallback) — in
the **v19.0.6 release-locked 4-tier failover order** (SGLang → vLLM →
BeeLlama → Ollama), encoded once as `DefaultFailoverOrder`: not a runtime
option, not a request parameter. There is deliberately **no
openai/anthropic/azure** — the runtime normalizes the LOCAL sovereign
stack; it is not a cloud-LLM gateway. Cloud names, ports, paths,
whitespace and control characters are rejected at validation (tested).
Extending the enumeration is a reviewed act: `AllProviders` + `Tier` + the
order + the tests change together.

### Model registry — explicit, bounded, no auto-discovery
Registration is an operator act: "provider P serves model M". `ModelID` is
typed/validated (`[a-z0-9._:-]`, ≤ 64 runes — the real naming shapes);
duplicate pairs are rejected; the registry is bounded to
`DefaultMaxModels = 64` **distinct models** (provider fan-out on an
already-registered model does not consume a slot — the fan-out is routing
information, not capacity). Listings are deterministic: providers in tier
order, models ascending, 20× repetition-tested; reads return copies. **No
auto-discovery — the registry IS the routing surface.**

### Observed health — fail-closed
Closed `UNKNOWN/HEALTHY/DEGRADED/UNAVAILABLE` enumeration (no "starting":
health is what the LAST probe saw). The table keeps the LATEST observation
per provider with a **monotone observation time** — a late-arriving older
probe is rejected BY NAME (it must not overwrite a newer one); same-time
re-observation is allowed. **UNKNOWN never serves**: never observed means
never serves — trust is earned by observation, not assumed by default.
DEGRADED serves — honestly, cited in the plan. The table never expires
entries on its own (freshness is the CF-2 prober's job — stated, not
hidden).

### The normalized chat contract
Closed roles (`system/user/assistant`; no tool role until a real consumer
exists), content ≤ 4096 **runes** (a rune bound is what a token-ish budget
means), 1..32 messages per request, control runes rejected except newline
and tab (both legitimate prose), non-negative usage, closed finish reasons
(`stop/length` — deliberately **no "error" value**: a failed generation is
an adapter ERROR the router skips past, never a response shape). Every
response carries **provider attribution + a required UTC timestamp** —
attribution survives failover: the caller sees which tier answered.
Requests hand out deep copies (copy isolation).

### The router — PURE and deterministic
`Router.Plan(registry, health, model)`: same inputs, same plan, byte for
byte (100× repetition test). No clocks, no randomness, no network, no LLM.
Candidates walk `DefaultFailoverOrder`; every step cites the health state
it was planned under; every skip fires a named rule
(`rule/health-unknown` / `rule/health-unavailable`) that names the
provider; the first serving candidate is `primary`, the rest are
`fallback`. Fail-closed errors: `ErrUnknownModel` (**the router never
guesses a model**) and `ErrNoRoute` (all candidates non-serving — never an
empty plan as success; the named skips return alongside the error:
fail-closed, not fail-blind). **The router never executes anything** — a
plan is an ordered list of attempts. Execution is the CF-2 adapters' job,
behind the fabric's approvals.

### Network/IO-free by construction
No `net/http`, no sockets, no file access, no exec anywhere in the package
— runtime AND tests. The Phase C charter ("LLM navrhuje, politika
rozhoduje") is encoded in the package doc itself, on one line, and smoke
check 28 enforces the scan comment-stripped (prose that says "no net/http"
cannot mask a real import — the scan fires on code, not prose).

## 4. Non-goals (honest scope — carried, not hidden)

- **No adapter implementations** — no HTTP client, no real SGLang/Ollama
  call exists yet. The contract is the deliverable; adapters are CF-2.
- **No health probing** — the table accepts observations; producing them is
  CF-2. Nothing in CF-1 fabricates health.
- **No HTTP/MCP surface, no persistence, no compose env** — the unwired
  AK-1 precedent (main.go and docker-compose.yml carry no `airuntime`).
  Nothing user-facing changes.
- **No LLM anywhere in the decision path** — the router is pure rule
  evaluation; an LLM will PROPOSE (CF-3), never decide.
- **No cross-tier model translation** — failover plans over providers that
  serve the SAME registered model (the LiteLLM-style mapping would be a
  deliberate future act, not an implicit guess).

## 5. Verification on this branch

- `gofmt -l .` clean; `go vet ./...` clean; `go build ./...` OK
- `go test -count=1 ./...` — all 22 packages green (incl. the new
  `internal/airuntime`); `go test -race -count=1 ./internal/airuntime`
  green (concurrent Register/Observe/Plan)
- Test matrices in `internal/airuntime`: provider validation (incl.
  openai/anthropic/port/path/whitespace/control-char rejection), tier
  mapping, the locked failover order, AllProviders copy isolation; model id
  validation, registry construction fail-closed, duplicate-pair rejection,
  bounds (distinct-model bound vs provider fan-out), tier-ordered +
  deterministic listings (20×); health enumeration + Serves matrix,
  snapshot validation, monotone observation (stale probe rejected by name,
  same-time re-observation allowed), UNKNOWN default for misses, snapshot
  determinism (20×), never-expires discipline; role/finish/usage matrices
  (incl. the deliberately-absent "error" finish), content bounds
  (at/over the bound, control runes, newline/tab legitimate), request
  bounds (0/32/33), request copy isolation, response validation + UTC
  stamping + attribution; router nil-input rejection, ErrUnknownModel,
  primary/fallback labels in tier order, degraded-serves, UNKNOWN
  fail-closed (skip cites the rule), UNAVAILABLE skips to fallback (both
  flavors distinguishable), all-non-serving → ErrNoRoute with skips
  returned, 100× determinism loop, concurrent planning, skip rules name
  the provider
- Smoke check 28 (`check_28_ai_runtime_foundation`) — the source-level
  contract gate: closed enumeration + the locked order, the
  **comment-stripped network/IO scan**, the cloud-token scan over runtime
  files, chat bounds, UNKNOWN-fail-closed markers, named routing errors +
  never-guesses-a-model, the purity marker, the charter (one line), the
  design-doc genre + CF-1..CF-5 plan, the README v19.6.0 surface, and the
  **unwired contract** (main.go + compose carry no airuntime). Negative
  control verified: a `var nethttpProbe = "net/http"` line injected into
  the runtime source FAILS the check (the scan fires on code, not prose —
  the BF-4/BF-5 prefix-edit lesson respected).
- Real-binary sanity: the ScarliHQ binary builds and boots unchanged —
  CF-1 adds a package and changes no wiring; the unit + race +
  determinism gates ARE the CF-1 proof, honestly stated (an E2E would have
  nothing user-facing to exercise; the AK-1 precedent)
