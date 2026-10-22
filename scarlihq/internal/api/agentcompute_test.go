package api

// AK-5 (v19.4.6): agent-gated compute intake tests — the kernel decides
// (denial-is-200), the registry is REQUIRED (principal mapping must be
// admin-verified), the workload bridge rejects unowned/unknown allocations,
// and every attempt lands in the audit sink. The full happy path (a real
// bridge executing the workload) is integration-level: these tests cover
// the intake contract with the bridge error path (socket absent → the same
// error mapping as the direct lifecycle surface).

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
	"github.com/MoZoHuJa/OS/scarlihq/internal/compute"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
)

// newAgentComputeTestServer builds the AK-5 deployment shape: session
// store + grant registry + audit sink + compute runtime (socket path in a
// temp dir — absent, so bridge calls fail deterministically).
func newAgentComputeTestServer(t *testing.T) (*http.ServeMux, *agentkernel.AuditSink, *agentkernel.GrantRegistry) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg, err := agentkernel.NewGrantRegistry(filepath.Join(dir, "grants.json"), []string{"zmor"}, agentkernel.DefaultMaxGrants, agentkernel.DefaultMaxAgents)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); err != nil {
		t.Fatal(err)
	}
	sink, err := agentkernel.NewAuditSink(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.Close() })
	store, err := agentkernel.NewStore(agentkernel.DefaultMaxSessions)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := compute.New(allocation.NewStore(filepath.Join(dir, "allocations.jsonl")), filepath.Join(dir, "bridge.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.6")
	if err := h.ConfigureAgentKernel(store, reg.Policy()); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureAgentGrants(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureAgentAudit(sink); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureCompute(runtime.Manager, "server.principal"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, sink, reg
}

func TestAgentComputeRouteRequireAuth(t *testing.T) {
	mux, _, _ := newAgentComputeTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/sessions/compute", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
}

func TestAgentComputeDisabledSurfaces(t *testing.T) {
	// No agent kernel at all → 503 (agent surface missing).
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.6")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/compute", `{"session_id":"x","action":"reserve","allocation_id":"a1"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no kernel: expected 503, got %d (%v)", code, body)
	}

	// Kernel configured but no compute lifecycle → 503 (compute missing).
	store, err := agentkernel.NewStore(agentkernel.DefaultMaxSessions)
	if err != nil {
		t.Fatal(err)
	}
	h2 := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.6")
	if err := h2.ConfigureAgentKernel(store, agentkernel.Policy{}); err != nil {
		t.Fatal(err)
	}
	mux2 := http.NewServeMux()
	h2.RegisterRoutes(mux2)
	code, body = agentRequest(t, mux2, http.MethodPost, "/api/agent/sessions/compute", `{"session_id":"x","action":"reserve","allocation_id":"a1"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no compute: expected 503, got %d (%v)", code, body)
	}
}

func TestAgentComputeRegistryRequired(t *testing.T) {
	// Kernel + compute but NO registry (legacy mode) → 400: the acting
	// principal must be admin-verified, a caller-supplied owner would be
	// spoofable.
	store, err := agentkernel.NewStore(agentkernel.DefaultMaxSessions)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := compute.New(allocation.NewStore(filepath.Join(t.TempDir(), "a.jsonl")), filepath.Join(t.TempDir(), "b.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.6")
	if err := h.ConfigureAgentKernel(store, agentkernel.Policy{}); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureCompute(runtime.Manager, "server.principal"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/compute", `{"session_id":"x","action":"reserve","allocation_id":"a1"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("legacy mode: expected 400 (registry required), got %d (%v)", code, body)
	}
}

func TestAgentComputeValidationAndDenials(t *testing.T) {
	mux, sink, reg := newAgentComputeTestServer(t)

	// Setup: grant status.read only (NOT compute.reserve), create + activate.
	agentRequest(t, mux, http.MethodPost, "/api/agent/grants", `{"agent_id":"coder.agent-one","capability":"status.read","owner":"zmor","granted_by":"admin.zmor"}`)
	code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	sid, _ := body["session_id"].(string)
	agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition", `{"session_id":"`+sid+`","to":"ACTIVE"}`)

	// Validation matrix.
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"bad action", `{"session_id":"` + sid + `","action":"nuke","allocation_id":"a1"}`, 400},
		{"missing allocation", `{"session_id":"` + sid + `","action":"reserve","allocation_id":""}`, 400},
		{"unknown session", `{"session_id":"aks-nope","action":"reserve","allocation_id":"a1"}`, 404},
		{"unknown field", `{"session_id":"` + sid + `","action":"reserve","allocation_id":"a1","principal":"forged"}`, 400},
		{"malformed", `{`, 400},
	} {
		code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/compute", tc.body)
		if code != tc.want {
			t.Errorf("%s: expected %d, got %d (%v)", tc.name, tc.want, code, body)
		}
	}
	// Method discipline.
	code, _ = agentRequest(t, mux, http.MethodGet, "/api/agent/sessions/compute", "")
	if code != http.StatusMethodNotAllowed {
		t.Errorf("GET: expected 405, got %d", code)
	}

	// Denial-is-200: the session holds status.read, not compute.reserve.
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/compute", `{"session_id":"`+sid+`","action":"reserve","allocation_id":"alloc-42"}`)
	if code != http.StatusOK {
		t.Fatalf("denied intake: expected 200 (denial-is-200), got %d (%v)", code, body)
	}
	if body["allow"] != false {
		t.Fatalf("denied intake must carry allow=false: %v", body)
	}
	if reason, _ := body["reason"].(string); reason == "" || !containsSubstr(reason, "capability-not-granted") {
		t.Fatalf("denial reason must cite the rule: %v", body["reason"])
	}

	// The denial is audited with the allocation id.
	lines, _, err := sink.ReadLast(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lines {
		if l.Type == agentkernel.AuditTypeComputeReserve && l.AllocationID == "alloc-42" && l.Allow != nil && !*l.Allow {
			found = true
		}
	}
	if !found {
		t.Fatalf("compute.reserve denial must be audited with allocation id: %+v", lines)
	}
	_ = reg
}

func TestAgentComputeAllowThenBridgeRejection(t *testing.T) {
	mux, sink, reg := newAgentComputeTestServer(t)

	// Grant compute.reserve + create + activate.
	agentRequest(t, mux, http.MethodPost, "/api/agent/grants", `{"agent_id":"coder.agent-one","capability":"compute.reserve","owner":"zmor","granted_by":"admin.zmor"}`)
	code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	sid, _ := body["session_id"].(string)
	agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition", `{"session_id":"`+sid+`","to":"ACTIVE"}`)

	// Allowed by the kernel; the bridge (socket absent) rejects → 409 with
	// the same mapping as the direct lifecycle surface.
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/compute", `{"session_id":"`+sid+`","action":"reserve","allocation_id":"alloc-42"}`)
	if code != http.StatusConflict {
		t.Fatalf("bridge-rejected intake: expected 409, got %d (%v)", code, body)
	}

	// The ATTEMPT is audited: allow=true + outcome rejected:<kind>.
	lines, _, err := sink.ReadLast(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lines {
		if l.Type == agentkernel.AuditTypeComputeReserve && l.AllocationID == "alloc-42" {
			if l.Allow != nil && *l.Allow && containsSubstr(l.Detail, "rejected") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("allowed-but-rejected intake must be audited with outcome: %+v", lines)
	}

	// Runtime disable of compute.reserve flips the intake to a 200 denial on
	// the NEXT call (no restart) — the kill-switch contract across surfaces.
	if _, err := reg.Disable(agentkernel.CapabilityComputeReserve, "admin.zmor", "incident drill", time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/compute", `{"session_id":"`+sid+`","action":"reserve","allocation_id":"alloc-43"}`)
	if code != http.StatusOK || body["allow"] != false {
		t.Fatalf("disabled capability intake: expected 200/deny, got %d (%v)", code, body)
	}
}

func TestAgentComputeReleaseCapabilityFlow(t *testing.T) {
	mux, _, _ := newAgentComputeTestServer(t)
	// compute.release granted (not compute.reserve): release action allowed
	// to the bridge, reserve denied by the kernel.
	agentRequest(t, mux, http.MethodPost, "/api/agent/grants", `{"agent_id":"coder.agent-one","capability":"compute.release","owner":"zmor","granted_by":"admin.zmor"}`)
	code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	sid, _ := body["session_id"].(string)
	agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition", `{"session_id":"`+sid+`","to":"ACTIVE"}`)

	// release → allowed by kernel → bridge rejects (409, socket absent).
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/compute", `{"session_id":"`+sid+`","action":"release","allocation_id":"alloc-42"}`)
	if code != http.StatusConflict {
		t.Fatalf("release intake: expected 409 (bridge rejection), got %d", code)
	}
	// reserve → denied by the kernel (200, allow=false).
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/compute", `{"session_id":"`+sid+`","action":"reserve","allocation_id":"alloc-42"}`)
	if code != http.StatusOK || body["allow"] != false {
		t.Fatalf("reserve without grant: expected 200/deny, got %d (%v)", code, body)
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
