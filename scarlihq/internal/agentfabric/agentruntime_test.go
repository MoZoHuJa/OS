package agentfabric

import (
	"strings"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

func validExecutionID() string {
	return "afx-" + strings.Repeat("a", 32)
}

// ---------------------------------------------------------------------------
// Runtime mapping (closed, total)
// ---------------------------------------------------------------------------

func TestRuntimeForCapabilityMapping(t *testing.T) {
	want := map[agentkernel.Capability]RuntimeID{
		agentkernel.CapabilityStatusRead:     RuntimeResearch,
		agentkernel.CapabilityGamingRead:     RuntimeResearch,
		agentkernel.CapabilityComputePlan:    RuntimeResearch,
		agentkernel.CapabilityModeRequest:    RuntimeCoding,
		agentkernel.CapabilityComputeReserve: RuntimeCoding,
		agentkernel.CapabilityComputeRelease: RuntimeCoding,
	}
	for cap, wantRT := range want {
		got, ok := RuntimeForCapability(cap)
		if !ok || got != wantRT {
			t.Errorf("RuntimeForCapability(%s) = %q,%v — want %q,true", cap, got, ok, wantRT)
		}
	}
	if _, ok := RuntimeForCapability("shell.exec"); ok {
		t.Error("unknown capability answered ok=true — the caller must fail closed")
	}
}

func TestValidateRuntimeMapping(t *testing.T) {
	if err := ValidateRuntimeMapping(); err != nil {
		t.Fatalf("ValidateRuntimeMapping on the shipped enumerations: %v", err)
	}
}

func TestRuntimeIDsClosed(t *testing.T) {
	if len(RuntimeIDs()) != 2 {
		t.Errorf("RuntimeIDs = %v — the closed enumeration is research+coding", RuntimeIDs())
	}
	if !validRuntimeID(RuntimeResearch) || !validRuntimeID(RuntimeCoding) {
		t.Error("validRuntimeID rejects a known runtime")
	}
	if validRuntimeID("shell") {
		t.Error("validRuntimeID accepts an unknown runtime — closed enumeration")
	}
}

// ---------------------------------------------------------------------------
// Outcome / reason bounds
// ---------------------------------------------------------------------------

func TestValidateOutcome(t *testing.T) {
	if err := ValidateOutcome("status snapshot: mode=ai"); err != nil {
		t.Errorf("valid outcome rejected: %v", err)
	}
	if err := ValidateOutcome("  "); err == nil {
		t.Error("empty-after-trim outcome accepted")
	}
	if err := ValidateOutcome("bad\nline"); err == nil {
		t.Error("control-character outcome accepted")
	}
	if err := ValidateOutcome(strings.Repeat("x", MaxOutcomeRunes+1)); err == nil {
		t.Error("over-bound outcome accepted")
	}
}

func TestValidateExecutionReason(t *testing.T) {
	if err := ValidateExecutionReason("rule/session-state: session is SUSPENDED"); err != nil {
		t.Errorf("valid reason rejected: %v", err)
	}
	if err := ValidateExecutionReason(""); err == nil {
		t.Error("empty reason accepted")
	}
	if err := ValidateExecutionReason("bad\nreason"); err == nil {
		t.Error("control-character reason accepted")
	}
	if err := ValidateExecutionReason(strings.Repeat("x", MaxReasonRunes+1)); err == nil {
		t.Error("over-bound reason accepted")
	}
}

// ---------------------------------------------------------------------------
// Closed params shapes
// ---------------------------------------------------------------------------

func TestParseRuntimeParams(t *testing.T) {
	tests := []struct {
		name       string
		capability agentkernel.Capability
		raw        string
		wantErr    bool
		wantMode   string
		wantAlloc  string
	}{
		// research: no params, empty object or absent
		{"research empty object", agentkernel.CapabilityStatusRead, `{}`, false, "", ""},
		{"research absent (nil raw)", agentkernel.CapabilityGamingRead, ``, false, "", ""},
		{"research null", agentkernel.CapabilityComputePlan, `null`, false, "", ""},
		{"research rejects mode", agentkernel.CapabilityStatusRead, `{"mode":"ai"}`, true, "", ""},
		{"research rejects allocation", agentkernel.CapabilityComputePlan, `{"allocation_id":"a"}`, true, "", ""},
		{"research rejects unknown field", agentkernel.CapabilityGamingRead, `{"depth":3}`, true, "", ""},
		{"research rejects non-object", agentkernel.CapabilityStatusRead, `[1,2]`, true, "", ""},
		// mode.request: {"mode"}
		{"mode ok", agentkernel.CapabilityModeRequest, `{"mode":"ai"}`, false, "ai", ""},
		{"mode missing", agentkernel.CapabilityModeRequest, `{}`, true, "", ""},
		{"mode empty", agentkernel.CapabilityModeRequest, `{"mode":"  "}`, true, "", ""},
		{"mode rejects allocation field", agentkernel.CapabilityModeRequest, `{"mode":"ai","allocation_id":"a"}`, true, "", ""},
		{"mode over-bound", agentkernel.CapabilityModeRequest, `{"mode":"` + strings.Repeat("x", 33) + `"}`, true, "", ""},
		// compute.reserve/release: {"allocation_id"}
		{"reserve ok", agentkernel.CapabilityComputeReserve, `{"allocation_id":"alloc-1"}`, false, "", "alloc-1"},
		{"release ok", agentkernel.CapabilityComputeRelease, `{"allocation_id":"alloc-42"}`, false, "", "alloc-42"},
		{"reserve missing", agentkernel.CapabilityComputeReserve, `{}`, true, "", ""},
		{"reserve rejects mode field", agentkernel.CapabilityComputeReserve, `{"allocation_id":"a","mode":"ai"}`, true, "", ""},
		{"reserve over-bound", agentkernel.CapabilityComputeReserve, `{"allocation_id":"` + strings.Repeat("x", 129) + `"}`, true, "", ""},
		{"reserve control char", agentkernel.CapabilityComputeReserve, `{"allocation_id":"a\nb"}`, true, "", ""},
		{"trailing garbage", agentkernel.CapabilityModeRequest, `{"mode":"ai"} trailing`, true, "", ""},
		{"unknown capability", "shell.exec", `{}`, true, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			if tc.raw != "" {
				raw = []byte(tc.raw)
			}
			p, err := ParseRuntimeParams(tc.capability, raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("params accepted, want rejection (%+v)", p)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid params rejected: %v", err)
			}
			if p.Mode != tc.wantMode || p.AllocationID != tc.wantAlloc {
				t.Fatalf("parsed params = %+v — want mode=%q allocation_id=%q", p, tc.wantMode, tc.wantAlloc)
			}
		})
	}
}
