package api

// AK-3 (v19.4.4): persisted admin-owned grant registry REST surface tests —
// auth-gating, 503 default, the full grant/revoke/disable/enable lifecycle
// over HTTP, registry-mode session creation (request MUST NOT carry
// capabilities; owner comes from the registry), the runtime policy toggle
// taking effect on the next evaluation, and the legacy-mode backward
// compatibility contract.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
)

// newAgentGrantsTestServer builds a server with the session store AND the
// grant registry configured (registry mode — the AK-3 deployment shape).
func newAgentGrantsTestServer(t *testing.T) (*http.ServeMux, *agentkernel.GrantRegistry) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg, err := agentkernel.NewGrantRegistry(filepath.Join(dir, "grants.json"), []string{"zmor", "system.reservation-governor"}, agentkernel.DefaultMaxGrants, agentkernel.DefaultMaxAgents)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); err != nil {
		t.Fatal(err)
	}
	store, err := agentkernel.NewStore(agentkernel.DefaultMaxSessions)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.4")
	if err := h.ConfigureAgentKernel(store, reg.Policy()); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureAgentGrants(reg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, reg
}

func TestAgentGrantRoutesRequireAuth(t *testing.T) {
	mux, _ := newAgentGrantsTestServer(t)
	for _, path := range []string{"/api/agent/grants", "/api/agent/grants/revoke", "/api/agent/capabilities/disable", "/api/agent/capabilities/enable"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: expected 401 without token, got %d", path, rec.Code)
		}
	}
}

func TestAgentGrantRoutesDisabledByDefault(t *testing.T) {
	// Sessions configured, registry NOT — grant routes must 503.
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.4")
	store, err := agentkernel.NewStore(agentkernel.DefaultMaxSessions)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureAgentKernel(store, agentkernel.Policy{}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, path := range []string{"/api/agent/grants", "/api/agent/grants/revoke", "/api/agent/capabilities/disable", "/api/agent/capabilities/enable"} {
		code, body := agentRequest(t, mux, http.MethodPost, path, `{"agent_id":"x"}`)
		if code != http.StatusServiceUnavailable {
			t.Errorf("%s: expected 503 without registry, got %d (%v)", path, code, body)
		}
	}
}

func TestAgentGrantLifecycleOverHTTP(t *testing.T) {
	mux, _ := newAgentGrantsTestServer(t)

	// Empty registry: zero counts, empty arrays.
	code, body := agentRequest(t, mux, http.MethodGet, "/api/agent/grants", "")
	if code != http.StatusOK {
		t.Fatalf("list grants: expected 200, got %d (%v)", code, body)
	}
	if body["active_grants"].(float64) != 0 || body["total_records"].(float64) != 0 {
		t.Fatalf("empty registry counts: %v", body)
	}
	if gr, ok := body["grants"].([]interface{}); !ok || len(gr) != 0 {
		t.Fatalf("grants must be an empty array, got %v", body["grants"])
	}

	// Grant: happy path (201).
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/grants", `{"agent_id":"coder.agent-one","capability":"status.read","owner":"zmor","granted_by":"admin.zmor"}`)
	if code != http.StatusCreated {
		t.Fatalf("grant: expected 201, got %d (%v)", code, body)
	}
	if body["agent_id"] != "coder.agent-one" || body["capability"] != "status.read" || body["active"] != true {
		t.Fatalf("grant view: %v", body)
	}

	// Grant: validation matrix.
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"unknown capability", `{"agent_id":"a.b","capability":"shell.exec","owner":"zmor","granted_by":"admin"}`, 400},
		{"unknown principal", `{"agent_id":"a.b","capability":"gaming.read","owner":"stranger","granted_by":"admin"}`, 400},
		{"empty granted_by", `{"agent_id":"a.b","capability":"gaming.read","owner":"zmor","granted_by":""}`, 400},
		{"duplicate active grant", `{"agent_id":"coder.agent-one","capability":"status.read","owner":"zmor","granted_by":"admin.zmor"}`, 400},
		{"owner mismatch", `{"agent_id":"coder.agent-one","capability":"gaming.read","owner":"system.reservation-governor","granted_by":"admin.zmor"}`, 400},
		{"unknown field", `{"agent_id":"a.b","capability":"gaming.read","owner":"zmor","granted_by":"admin","extra":1}`, 400},
		{"malformed json", `{`, 400},
	} {
		code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/grants", tc.body)
		if code != tc.want {
			t.Errorf("grant %s: expected %d, got %d (%v)", tc.name, tc.want, code, body)
		}
	}

	// Revoke: happy path.
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/grants/revoke", `{"agent_id":"coder.agent-one","capability":"status.read","revoked_by":"admin.zmor"}`)
	if code != http.StatusOK {
		t.Fatalf("revoke: expected 200, got %d (%v)", code, body)
	}
	if body["active"] != false || body["revoked_by"] != "admin.zmor" {
		t.Fatalf("revoke view: %v", body)
	}
	// Double revoke: 400.
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/grants/revoke", `{"agent_id":"coder.agent-one","capability":"status.read","revoked_by":"admin.zmor"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("double revoke: expected 400, got %d", code)
	}
	// Unknown pair: 400.
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/grants/revoke", `{"agent_id":"coder.nope","capability":"status.read","revoked_by":"admin.zmor"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown revoke: expected 400, got %d", code)
	}

	// Disable → list shows it → enable → list shows none.
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/capabilities/disable", `{"capability":"compute.reserve","disabled_by":"admin.zmor","reason":"capacity freeze"}`)
	if code != http.StatusOK {
		t.Fatalf("disable: expected 200, got %d (%v)", code, body)
	}
	code, body = agentRequest(t, mux, http.MethodGet, "/api/agent/grants", "")
	if code != http.StatusOK {
		t.Fatal("list after disable failed")
	}
	if ds, ok := body["disables"].([]interface{}); !ok || len(ds) != 1 {
		t.Fatalf("disables after disable: %v", body["disables"])
	}
	// Duplicate disable: 400.
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/capabilities/disable", `{"capability":"compute.reserve","disabled_by":"admin.zmor","reason":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("duplicate disable: expected 400, got %d", code)
	}
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/capabilities/enable", `{"capability":"compute.reserve","enabled_by":"admin.zmor"}`)
	if code != http.StatusOK || body["enabled"] != true {
		t.Fatalf("enable: expected 200/true, got %d (%v)", code, body)
	}
	// Enable of not-disabled: 400.
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/capabilities/enable", `{"capability":"compute.reserve","enabled_by":"admin.zmor"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("enable of not-disabled: expected 400, got %d", code)
	}
	// Disable with unknown capability: 400 (closed enumeration).
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/capabilities/disable", `{"capability":"shell.exec","disabled_by":"a","reason":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("disable of unknown capability: expected 400, got %d", code)
	}

	// Method discipline.
	code, _ = agentRequest(t, mux, http.MethodGet, "/api/agent/grants/revoke", "")
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("GET revoke: expected 405, got %d", code)
	}
	code, _ = agentRequest(t, mux, http.MethodDelete, "/api/agent/grants", "")
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE grants: expected 405, got %d", code)
	}
}

func TestAgentSessionCreate_RegistryMode(t *testing.T) {
	mux, reg := newAgentGrantsTestServer(t)

	// Grant two capabilities to the agent first.
	for _, capName := range []string{"status.read", "compute.plan"} {
		code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/grants", `{"agent_id":"coder.agent-one","capability":"`+capName+`","owner":"zmor","granted_by":"admin.zmor"}`)
		if code != http.StatusCreated {
			t.Fatalf("grant %s: expected 201, got %d (%v)", capName, code, body)
		}
	}

	// Registry-mode create: NO capabilities field, NO owner field.
	code, body := agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one"}`)
	if code != http.StatusCreated {
		t.Fatalf("registry-mode create: expected 201, got %d (%v)", code, body)
	}
	if body["owner"] != "zmor" {
		t.Fatalf("registry-mode create must derive owner from the registry, got %v", body["owner"])
	}
	caps, ok := body["capabilities"].([]interface{})
	if !ok || len(caps) != 2 {
		t.Fatalf("registry-mode create must derive grants from the registry, got %v", body["capabilities"])
	}
	sid, _ := body["session_id"].(string)
	if !strings.HasPrefix(sid, "aks-") {
		t.Fatalf("session id shape: %v", sid)
	}

	// Request MUST NOT carry capabilities (grants are admin-owned).
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one","capabilities":["status.read"]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("create with capabilities in registry mode: expected 400, got %d (%v)", code, body)
	}
	// Owner mismatch with the registry declaration: 400.
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one","owner":"someone-else"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("create with mismatched owner: expected 400, got %d (%v)", code, body)
	}
	// Owner matching the registry declaration is accepted (harmless).
	code, _ = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one","owner":"zmor"}`)
	if code != http.StatusCreated {
		t.Fatalf("create with declared owner: expected 201, got %d", code)
	}
	// Agent without grants: 400 (nothing is granted implicitly).
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.no-grants"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("create for grantless agent: expected 400, got %d (%v)", code, body)
	}

	// Full kernel flow: activate → evaluate granted (allow) → evaluate
	// not-granted (deny, 200) → runtime disable → deny citing the rule.
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/transition", `{"session_id":"`+sid+`","to":"ACTIVE"}`)
	if code != http.StatusOK {
		t.Fatalf("activate: expected 200, got %d (%v)", code, body)
	}
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate", `{"session_id":"`+sid+`","capability":"status.read"}`)
	if code != http.StatusOK || body["allow"] != true {
		t.Fatalf("evaluate granted: expected 200/allow, got %d (%v)", code, body)
	}
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate", `{"session_id":"`+sid+`","capability":"compute.reserve"}`)
	if code != http.StatusOK || body["allow"] != false {
		t.Fatalf("evaluate not-granted: expected 200/deny, got %d (%v)", code, body)
	}
	// Runtime disable takes effect on the NEXT evaluation (no restart).
	if _, err := reg.Disable(agentkernel.CapabilityStatusRead, "admin.zmor", "incident drill", testTimeAK()); err != nil {
		t.Fatal(err)
	}
	code, body = agentRequest(t, mux, http.MethodPost, "/api/agent/sessions/evaluate", `{"session_id":"`+sid+`","capability":"status.read"}`)
	if code != http.StatusOK || body["allow"] != false {
		t.Fatalf("evaluate after disable: expected 200/deny, got %d (%v)", code, body)
	}
	if reason, _ := body["reason"].(string); !strings.Contains(reason, "rule/capability-disabled") {
		t.Fatalf("deny reason must cite rule/capability-disabled, got %v", body["reason"])
	}
}

func TestAgentSessionCreate_LegacyModeUnchanged(t *testing.T) {
	// AK-2 backward compatibility: no registry configured → capabilities
	// stay REQUIRED in the request (the documented transitional contract).
	mux := newAgentKernelTestServer(t, nil, agentkernel.Policy{})
	store, err := agentkernel.NewStore(agentkernel.DefaultMaxSessions)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.4.4")
	if err := h.ConfigureAgentKernel(store, agentkernel.Policy{}); err != nil {
		t.Fatal(err)
	}
	m2 := http.NewServeMux()
	h.RegisterRoutes(m2)

	code, body := agentRequest(t, m2, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one","owner":"zmor","capabilities":["status.read"]}`)
	if code != http.StatusCreated {
		t.Fatalf("legacy create: expected 201, got %d (%v)", code, body)
	}
	code, body = agentRequest(t, m2, http.MethodPost, "/api/agent/sessions", `{"agent_id":"coder.agent-one","owner":"zmor"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("legacy create without capabilities: expected 400, got %d (%v)", code, body)
	}
	_ = mux
}
