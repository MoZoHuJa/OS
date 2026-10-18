# P4 — Contract & Integration Gate

**Candidate:** `19.2.11.P4`  
**Baseline:** `19.2.10.G1` / Compute Fabric v19.2.10  
**Status:** contract map implemented; integration behavior remains subject to package-level and later integration verification  
**Hardware:** intentionally out of scope for P4

## Objective

Make component ownership, dependencies, and integration invariants explicit before adding more ScarliHQ behavior or beginning ScarliNote implementation. This phase is a contract gate, not a claim that every planned component is already implemented.

## Deliverables

- `scarlihq/internal/integrationcontract/integration-contract.v1.json`: machine-readable component and contract registry.
- `scarlihq/internal/integrationcontract/contract.go`: strict JSON decoding and structural validation.
- `scarlihq/internal/integrationcontract/contract_test.go`: regression tests for malformed dependencies, unknown contracts, duplicate components, and cycles.
- This report: scope, boundaries, and acceptance criteria.
- `scarlihq/internal/integrationcontract/interfaces_test.go`: cross-package checks for runtime allowlist parity, Resource Contract v1 JSON stability, inventory schema stability, GPU UUID privacy, and Allocation v1 compatibility.

## Component ownership decisions

| Component | Owns | Explicitly does not own |
|---|---|---|
| ScarLiX OS | Host environment and privileged deployment boundary | Agent reasoning or personal memory policy |
| Host Agent / Bridge | Narrow validated host operations and outcomes | User orchestration or model choice |
| ScarliMonitor | Observed health and telemetry | Reservation mutations or execution authority |
| Compute Fabric | Canonical resources, allocation lifecycle, reservation and lease state | Reasoning or UI state |
| Universal AI Runtime | Backend-neutral inference lifecycle/capabilities | Identity or host privilege |
| ScarliHQ | Agent/workflow orchestration, tool authorization, execution correlation | Root access, resource truth, model backend internals, personal-memory persistence |
| ScarliBar | Intent capture and user-visible command status | Privileged operations or durable domain state |
| ScarliNote | User-scoped memory and context assembly | Host privilege, resource reservation, runtime process lifecycle |
| ScarliCenter | Configuration and operator approval UX | Direct persistence writes bypassing domain APIs |

The JSON registry is the canonical machine-readable source for this ownership map. Status values describe current maturity: `implemented`, `partial`, or `planned`. They are not runtime health indicators.

## Frozen integration invariants

1. **Trust boundary:** identity and authorization are verified at the receiving boundary. A caller-provided user ID is not authentication.
2. **Canonical identity:** ownership/exclusivity uses canonical stable identifiers, never mutable display indices or names.
3. **Plan vs. reserve:** planning is side-effect-free. Reservation is an explicit durable operation with idempotency and recovery.
4. **No false atomicity:** the reservation store and allocation store are separate persistence domains. Use reconciliation and surface its required state; do not claim a cross-store ACID transaction.
5. **Desired vs. observed:** desired configuration and observed host/runtime state remain separate. Unknown/stale observation must not be treated as healthy for safety-sensitive actions.
6. **Privilege boundary:** ScarliHQ and UI surfaces never execute arbitrary privileged shell commands directly. Host actions pass through a narrow allowlisted Host Agent/Bridge contract.
7. **Runtime neutrality:** orchestration depends on capability/lifecycle contracts, not backend-specific process details.
8. **Memory isolation:** ScarliNote owns user-scoped memory/context policy; ScarliHQ requests access through a contract and does not directly read its storage.
9. **Auditability:** cross-component operations carry correlation IDs, explicit outcomes, and sanitized errors; secrets and raw bearer tokens are never persisted.
10. **Compatibility:** existing frozen Resource Contract v1 and Allocation schema v1 are not silently renamed or broken. Breaking changes require an explicit new schema version and migration plan.

## Concrete integration checks

The P4 gate imports the existing domain packages instead of validating only a descriptive registry:

- The set of configured inventory runtime IDs must exactly equal the Resource Contract v1 runtime allowlist. A request must not be accepted for a runtime the inventory cannot represent, and an inventory runtime must not be rejected by the contract.
- Resource Contract v1 empty runtime/model slices serialize as JSON arrays, not `null`.
- Inventory snapshot arrays remain arrays and the internal GPU UUID stays excluded from public inventory JSON.
- Allocation records continue to use schema v1 and preserve the Resource Contract v1 version reference and non-null transition history.

This gate exposed a stale `smg` example in the Resource Contract documentation: it was not present in the current inventory runtime table and was not accepted by validation. P4 removes that unsupported example rather than falsely advertising an unavailable runtime. Adding SMG to the runtime request allowlist requires an actual inventory/runtime adapter plus tests.

## Deliberate non-goals

- No runtime startup/termination implementation.
- No new public HTTP/MCP API surface.
- No direct ScarliNote feature implementation.
- No target-host, GPU, container, process-kill, or port-race claims.
- No stable release claim for v19.2.10.

## Acceptance criteria

- `go test ./...` validates the embedded manifest and its dependency graph.
- The manifest rejects duplicate component IDs, unknown dependencies/contracts, and dependency cycles.
- All components explicitly declare ownership and non-ownership.
- Documentation distinguishes current partial implementation from future planned components.
- Existing contract v1 and allocation schema v1 remain unchanged by this phase.

## Exit decision

P4 is ready to close only after CI passes on the exact candidate commit and the manifest has been reviewed against the current repository. Passing this gate authorizes P5 ScarliHQ hardening; it does not authorize a stable release or claim hardware readiness.
