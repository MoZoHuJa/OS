// Package airt is the Universal AI Runtime transport layer (CF-2, the
// v19.6.x line — Phase C): the real local-stack adapters, the health
// prober and the failover executor BEHIND the pure internal/airuntime
// contract.
//
// CF-1 (v19.6.0) landed the typed foundation network/IO-free by
// construction; this package is deliberately SEPARATE so that purity is
// preserved — the network lives here, behind the contract, never inside
// it (smoke checks 28 and 29 enforce the separation in both directions).
// The charter is unchanged: "LLM navrhuje, politika rozhoduje" — the LLM
// proposes, policy decides.
//
// Hard rules encoded here:
//   - the transport normalizes the LOCAL sovereign stack only — endpoints
//     are loopback addresses or single-label docker service names, never
//     a dotted public host: the runtime is not a cloud-LLM gateway
//   - the dial guard validates DESTINATIONS, not just names (KPK-03,
//     v19.6.4): a configured name is resolved, every answered address is
//     validated against the trusted local networks (loopback always; the
//     Docker default pools or the operator's declared set), and the
//     connection is PINNED to a validated address — a poisoned resolver
//     can only produce refused dials
//   - a cancelled observation is not evidence (KPK-04, v19.6.4): a probe
//     round whose caller context is done aborts and records NOTHING —
//     the health table's last-known-good stands; only a completed round
//     with a live parent context may write observations
//   - probing is honest: a 2xx answer is HEALTHY, a standing-but-unready
//     server is DEGRADED, a dead one is UNAVAILABLE — and UNKNOWN still
//     never serves (the CF-1 fail-closed contract, fed by real probes)
//   - execution walks the CF-1 router's plan and attributes the answer:
//     the caller sees WHICH tier answered and which tiers were skipped
//     past (attribution survives failover)
//   - the LLM never decides anything here: Chat produces normalized text,
//     nothing else — proposals to the fabric are CF-3, behind its
//     approvals
package airt

import (
	"context"
	"errors"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

var (
	// ErrAllAdaptersFailed is returned when every step in the plan failed.
	// The failed attempts return alongside the error (fail-closed, not
	// fail-blind) — the caller sees which tiers were tried and why each
	// one was skipped past.
	ErrAllAdaptersFailed = errors.New("airt: every plan step failed (attempts returned alongside — fail-closed, not fail-blind)")

	// ErrNilRuntimeInput is returned when the registry, the health table
	// or the prober is nil — the runtime composes REAL CF-1 state and
	// REAL transports, never defaults.
	ErrNilRuntimeInput = errors.New("airt: nil runtime input (registry, health table and prober are required)")

	// ErrIncompleteAdapterSet is returned when the adapter set does not
	// cover exactly the closed four-provider local stack. All four tiers
	// are ALWAYS constructed — health gates routing, construction does
	// not (a missing tier would be a silent fail-open hole in failover).
	ErrIncompleteAdapterSet = errors.New("airt: adapters must cover exactly the closed local stack (sglang/vllm/beellama/ollama)")

	// ErrBudgetExpired is returned when the END-TO-END chat budget (the
	// request context deadline the API layer imposes — ChatBudget(per-
	// attempt), KPK-02, v19.6.3) expires before any tier answered. A
	// deliberate, named timeout: the API maps it to 504 with the failed
	// attempts riding along — never a socket write deadline artifact, and
	// never a generic all-failed masquerade. The wrapped error is the
	// context's own (context.DeadlineExceeded or context.Canceled), so
	// errors.Is keeps working for callers that reason about cancellation.
	ErrBudgetExpired = errors.New("airt: chat budget expired before a tier answered (the end-to-end request deadline — attempts returned alongside, fail-closed not fail-blind)")
)

// Adapter is one provider's transport behind the normalized chat
// contract. Implementations live in this package (LocalAdapter over the
// OpenAI-compatible wire the whole real local stack speaks); the
// interface exists so the executor is testable without network.
type Adapter interface {
	// Provider identifies which closed-enumeration tier this adapter
	// serves.
	Provider() airuntime.Provider

	// Chat performs one normalized chat completion against the provider.
	// A failed generation is an ERROR the executor skips past — never a
	// response shape with an "error" finish reason (the CF-1 closed
	// enumeration has no such value, deliberately).
	Chat(ctx context.Context, req airuntime.Request) (airuntime.Response, error)
}
