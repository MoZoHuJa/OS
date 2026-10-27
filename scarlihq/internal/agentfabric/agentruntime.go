// BF-3 (v19.5.2): the agent runtime model — the closed mapping that binds
// every kernel capability to exactly ONE runtime. Two runtimes exist:
//
//   - research: READ-ONLY capabilities (status.read, gaming.read,
//     compute.plan) — the runtime observes and reports, it never mutates
//     host state. compute.plan is included because the planner is
//     side-effect free BY CONSTRUCTION (a dry-run never allocates).
//   - coding: ACT capabilities (mode.request, compute.reserve,
//     compute.release) — the runtime performs the act through the existing
//     bridged surfaces (the mode bridge / the workload bridge), never
//     through a free shell. There is deliberately NO generic runtime: a
//     capability that fits neither runtime is a kernel change, not a
//     mapping guess.
//
// The mapping is CLOSED and TOTAL: RuntimeForCapability answers for every
// capability in the kernel's closed enumeration, and ValidateRuntimeMapping
// FAILS CLOSED when the two enumerations drift (a new kernel capability
// without a runtime mapping is a load-time construction error, never a
// silent "nothing executes"). This invariant is checked at store/API
// construction (main.go calls it when the fabric is enabled) and tested.
//
// Honest non-goals (BF-3):
//   - no LLM: the runtimes are deterministic executors of kernel-allowed
//     intents (Phase C, the Universal AI Runtime, adds the model).
//   - no multi-principal submitter identity (the approvals line, BF-4).
//   - no sandbox/process execution: acts run inside ScarliHQ through the
//     same bridged surfaces the REST API uses — the kernel's "no free
//     root shell" rule is inherited, not re-implemented.
package agentfabric

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

// ---------------------------------------------------------------------------
// Runtime identity (closed enumeration)
// ---------------------------------------------------------------------------

// RuntimeID is a typed runtime identifier. The enumeration is closed:
// research and coding. An unknown runtime ID is a construction or load
// error — never a fallback.
type RuntimeID string

const (
	// RuntimeResearch executes read-only capabilities: the act observes
	// and reports (status snapshot, gaming inventory snapshot, compute
	// plan dry-run). It cannot mutate host state.
	RuntimeResearch RuntimeID = "research"
	// RuntimeCoding executes act capabilities through the existing bridged
	// surfaces (mode request via the mode bridge; compute reserve/release
	// via the workload bridge with the agent's registry-declared owner).
	RuntimeCoding RuntimeID = "coding"
)

// validRuntimeID reports whether r is part of the closed enumeration.
func validRuntimeID(r RuntimeID) bool {
	switch r {
	case RuntimeResearch, RuntimeCoding:
		return true
	}
	return false
}

// RuntimeIDs returns the closed runtime enumeration (deterministic order).
func RuntimeIDs() []RuntimeID {
	return []RuntimeID{RuntimeResearch, RuntimeCoding}
}

// runtimeCapabilityMap is THE closed mapping (capability → runtime). It is
// deliberately a literal, not a computation: the mapping is policy, and
// policy must be readable in one place.
var runtimeCapabilityMap = map[agentkernel.Capability]RuntimeID{
	agentkernel.CapabilityStatusRead:     RuntimeResearch,
	agentkernel.CapabilityGamingRead:     RuntimeResearch,
	agentkernel.CapabilityComputePlan:    RuntimeResearch,
	agentkernel.CapabilityModeRequest:    RuntimeCoding,
	agentkernel.CapabilityComputeReserve: RuntimeCoding,
	agentkernel.CapabilityComputeRelease: RuntimeCoding,
}

// RuntimeForCapability returns the runtime a capability executes under.
// Unknown capabilities (outside the kernel's closed enumeration) answer
// ok=false — the caller fails closed, it never guesses a runtime.
func RuntimeForCapability(capability agentkernel.Capability) (RuntimeID, bool) {
	rt, ok := runtimeCapabilityMap[capability]
	if !ok {
		return "", false
	}
	return rt, true
}

// ValidateRuntimeMapping enforces the load-time invariant: the mapping is
// TOTAL over the kernel's closed capability enumeration (every capability
// maps to exactly one known runtime) and neither runtime is empty. It is
// called at construction time (main.go, when the fabric is enabled) and by
// the execution store's Load — a drifted enumeration aborts startup, it
// never degrades into "some capabilities execute".
func ValidateRuntimeMapping() error {
	caps := agentkernel.AllCapabilities()
	if len(runtimeCapabilityMap) != len(caps) {
		return fmt.Errorf("agentfabric: runtime mapping covers %d capabilities, the kernel enumerates %d — extend the mapping deliberately (closed total mapping)", len(runtimeCapabilityMap), len(caps))
	}
	counts := make(map[RuntimeID]int, len(RuntimeIDs()))
	for _, c := range caps {
		rt, ok := runtimeCapabilityMap[c]
		if !ok {
			return fmt.Errorf("agentfabric: capability %q has no runtime mapping — a new capability must declare its runtime (research/coding) deliberately", c)
		}
		if !validRuntimeID(rt) {
			return fmt.Errorf("agentfabric: capability %q maps to unknown runtime %q", c, rt)
		}
		counts[rt]++
	}
	for _, rt := range RuntimeIDs() {
		if counts[rt] == 0 {
			return fmt.Errorf("agentfabric: runtime %q maps no capabilities — remove it or map something deliberately", rt)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Execution outcome bounds
// ---------------------------------------------------------------------------

// MaxOutcomeRunes bounds one execution outcome line. The outcome is a
// single-line act summary that lands in a JSON file, a REST response and
// the dashboard DOM — control characters have no honest role there (the
// intent/name/description rule, carried to outcomes).
const MaxOutcomeRunes = 512

// MaxReasonRunes bounds one execution reason (the SKIPPED/FAILED
// explanation — the fresh denial rule or the act error).
const MaxReasonRunes = 256

// ValidateOutcome checks the outcome line: trimmed non-empty when present,
// at most MaxOutcomeRunes runes, NO control characters.
func ValidateOutcome(outcome string) error {
	trimmed := strings.TrimSpace(outcome)
	if trimmed == "" {
		return fmt.Errorf("agentfabric: execution outcome is empty after trimming")
	}
	if utf8.RuneCountInString(trimmed) > MaxOutcomeRunes {
		return fmt.Errorf("agentfabric: execution outcome longer than %d runes", MaxOutcomeRunes)
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("agentfabric: execution outcome contains control character U+%04X (single-line outcomes only)", r)
		}
	}
	return nil
}

// ValidateExecutionReason checks the reason line: trimmed non-empty when
// present, at most MaxReasonRunes runes, NO control characters.
func ValidateExecutionReason(reason string) error {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return fmt.Errorf("agentfabric: execution reason is empty after trimming")
	}
	if utf8.RuneCountInString(trimmed) > MaxReasonRunes {
		return fmt.Errorf("agentfabric: execution reason longer than %d runes", MaxReasonRunes)
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("agentfabric: execution reason contains control character U+%04X (single-line reasons only)", r)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Closed params shapes (DisallowUnknownFields per capability)
// ---------------------------------------------------------------------------

// RuntimeParams is the normalized, validated parameter set for one
// execution. Exactly one field group is meaningful per capability —
// ParseRuntimeParams enforces the closed shape:
//
//   - research capabilities (status.read, gaming.read, compute.plan):
//     NO params — the object must be empty (or absent). An act that only
//     observes takes no steering.
//   - mode.request: {"mode": "<mode>"} — the requested mode (1..32 runes,
//     no control characters; the mode bridge validates it against the
//     available modes at act time).
//   - compute.reserve / compute.release: {"allocation_id": "<id>"} — the
//     allocation the act targets (1..128 chars, the AK-5 intake bound).
//
// Unknown fields are REJECTED (json DisallowUnknownFields): a param shape
// is a contract, not a suggestion.
type RuntimeParams struct {
	Mode         string
	AllocationID string
}

// strictJSONObject is the internal decode shape — every field is optional
// at the JSON level, per-capability requirements are enforced after.
type strictJSONObject struct {
	Mode         *string `json:"mode"`
	AllocationID *string `json:"allocation_id"`
}

// ParseRuntimeParams validates and normalizes raw params JSON for the
// capability. raw may be nil/empty (absent params) or a JSON object.
// Research capabilities accept ONLY the empty object; coding capabilities
// require their named field. Every unknown field is rejected.
func ParseRuntimeParams(capability agentkernel.Capability, raw []byte) (RuntimeParams, error) {
	if !capability.IsValid() {
		return RuntimeParams{}, fmt.Errorf("agentfabric: unknown capability %q (closed enumeration)", capability)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		trimmed = []byte("{}")
	}
	if !bytes.HasPrefix(trimmed, []byte("{")) {
		return RuntimeParams{}, fmt.Errorf("agentfabric: params for %q must be a JSON object", capability)
	}
	var obj strictJSONObject
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return RuntimeParams{}, fmt.Errorf("agentfabric: params for %q are rejected (closed shape — unknown or malformed fields): %v", capability, err)
	}
	// Exactly one JSON object, nothing after it.
	var extra any
	if err := dec.Decode(&extra); err == nil || err.Error() != "EOF" {
		return RuntimeParams{}, fmt.Errorf("agentfabric: params for %q must contain exactly one JSON object", capability)
	}

	rt, _ := RuntimeForCapability(capability)
	switch rt {
	case RuntimeResearch:
		if obj.Mode != nil || obj.AllocationID != nil {
			return RuntimeParams{}, fmt.Errorf("agentfabric: research runtime (%s) takes no params — %s observes and reports", capability, capability)
		}
		return RuntimeParams{}, nil
	case RuntimeCoding:
		switch capability {
		case agentkernel.CapabilityModeRequest:
			if obj.AllocationID != nil {
				return RuntimeParams{}, fmt.Errorf("agentfabric: mode.request params carry allocation_id — unknown field for this capability")
			}
			if obj.Mode == nil {
				return RuntimeParams{}, fmt.Errorf("agentfabric: mode.request requires {\"mode\": \"…\"}")
			}
			mode := strings.TrimSpace(*obj.Mode)
			if mode == "" {
				return RuntimeParams{}, fmt.Errorf("agentfabric: mode.request mode is empty after trimming")
			}
			if utf8.RuneCountInString(mode) > 32 {
				return RuntimeParams{}, fmt.Errorf("agentfabric: mode.request mode longer than 32 runes")
			}
			for _, r := range mode {
				if r < 0x20 || r == 0x7f {
					return RuntimeParams{}, fmt.Errorf("agentfabric: mode.request mode contains control character U+%04X", r)
				}
			}
			return RuntimeParams{Mode: mode}, nil
		case agentkernel.CapabilityComputeReserve, agentkernel.CapabilityComputeRelease:
			if obj.Mode != nil {
				return RuntimeParams{}, fmt.Errorf("agentfabric: %s params carry mode — unknown field for this capability", capability)
			}
			if obj.AllocationID == nil {
				return RuntimeParams{}, fmt.Errorf("agentfabric: %s requires {\"allocation_id\": \"…\"}", capability)
			}
			alloc := strings.TrimSpace(*obj.AllocationID)
			if alloc == "" {
				return RuntimeParams{}, fmt.Errorf("agentfabric: %s allocation_id is empty after trimming", capability)
			}
			if len(alloc) > 128 {
				return RuntimeParams{}, fmt.Errorf("agentfabric: %s allocation_id longer than 128 chars (the AK-5 intake bound)", capability)
			}
			for _, r := range alloc {
				if r < 0x20 || r == 0x7f {
					return RuntimeParams{}, fmt.Errorf("agentfabric: %s allocation_id contains control character U+%04X", capability, r)
				}
			}
			return RuntimeParams{AllocationID: alloc}, nil
		}
	}
	// Unreachable: the mapping is total (ValidateRuntimeMapping) and every
	// coding capability is handled above. Fail closed regardless.
	return RuntimeParams{}, fmt.Errorf("agentfabric: capability %q has no params parser — extend the closed shapes deliberately", capability)
}
