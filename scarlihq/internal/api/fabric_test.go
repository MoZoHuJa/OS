// Package api tests — the distributed fabric's control-plane HTTP
// surface (v19.9.0, Compute Fabric Phase 5, roadmap §9.2/§9.3): the
// 503-before-configuration contract, the invite-based enrollment flow
// over HTTP (the one-time secret transport), the node-credential
// inventory push with replay protection (identical request IDs → 409;
// wrong credentials → 401; quarantine → the named NODE_QUARANTINED
// code), the execution-report reconciliation decisions through the
// ledger-view provider, and the /api/compute/plan fabric preference
// (zero remote views ≡ the single-node decision — the regression pin
// at the HTTP layer).
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
	"github.com/MoZoHuJa/OS/scarlihq/internal/fabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
	"github.com/MoZoHuJa/OS/scarlihq/internal/nodeidentity"
	"github.com/MoZoHuJa/OS/scarlihq/internal/planner"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// ---- the fabric HTTP fixture --------------------------------------------------

// identityStatusSource adapts the nodeidentity registry to the fabric
// registry's NodeStatusSource (defense in depth under the API gate).
type identityStatusSource struct{ reg *nodeidentity.Registry }

func (s identityStatusSource) NodeActive(nodeID string) bool {
	view, err := s.reg.Record(nodeID)
	if err != nil {
		return false
	}
	return view.Status == nodeidentity.StatusActive
}

// fabricLedgerFake is the ledger-view provider fixture.
type fabricLedgerFake struct {
	held map[string][]fabric.LedgerReservationView
}

func (f fabricLedgerFake) HeldReservations(nodeID string) []fabric.LedgerReservationView {
	return f.held[nodeID]
}

// fixedFabricClock drives the registries deterministically.
type fixedFabricClock struct{ now time.Time }

func (c *fixedFabricClock) Now() time.Time { return c.now }

type fabricFixture struct {
	server    *httptest.Server
	nodeReg   *nodeidentity.Registry
	fabricReg *fabric.Registry
	clock     *fixedFabricClock
	snapshots planSnapshots
}

func newFabricFixture(t *testing.T, ledger FabricLedgerViewProvider) *fabricFixture {
	t.Helper()
	dir := t.TempDir()
	clock := &fixedFabricClock{now: time.Now().UTC()}
	nodeReg, err := nodeidentity.NewRegistry(nodeidentity.NewStore(filepath.Join(dir, "nodes.jsonl")), clock, nodeidentity.RegistryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	fabricReg, err := fabric.NewRegistry(clock, identityStatusSource{reg: nodeReg}, 0)
	if err != nil {
		t.Fatal(err)
	}
	replay := nodeidentity.NewReplayGuard(clock, 0, 0)

	// The single-node planner + the local snapshot source (the fabric
	// planner's base + local view — the same fixture shape the planner
	// HTTP tests use).
	observed := clock.now
	m := &modelmeta.Manifest{
		SchemaVersion: modelmeta.SchemaVersionV2,
		ID:            "qwen3-14b-awq",
		Revision:      "rev-1",
	}
	m.Checkpoint.Format = "safetensors"
	m.Checkpoint.SHA256 = strings.Repeat("ab", 32)
	m.Architecture.Kind = modelmeta.ArchitectureDense
	m.Architecture.SharedBytes = 8 << 30
	m.Runtime.ContextLimit = 32768
	m.Runtime.KVCache.BytesPerTokenEstimate = 8192
	m.Runtime.SupportedBackends = []string{"sglang"}
	m.Storage.ModelPath = "/models/qwen3-14b-awq"
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "snap0000000000000000000000000001",
		ObservedAt:    observed,
		Host:          storage.HostResources{CPUThreads: 16, RAMTotalBytes: 32 << 30, RAMAvailableBytes: 16 << 30},
		GPUs: []inventory.GPU{
			{ID: "gpu.nvidia.0", UUID: "GPU-aaaa", VRAMTotalMB: 16384, VRAMFreeMB: 16384, UtilizationPct: 0, Healthy: true},
		},
		Runtimes: []inventory.Runtime{{ID: "sglang", Running: true, Healthy: true, Port: 30000}},
	}
	snapshots := planSnapshots{snap: snap}
	models := planModels{
		manifests: map[string]*modelmeta.Manifest{"qwen3-14b-awq": m},
		verifs: map[string]*modelmeta.ArtifactVerification{"qwen3-14b-awq": {
			ModelID: "qwen3-14b-awq", ManifestFingerprint: modelmeta.ManifestFingerprint(m), AllVerified: true, VerifiedAt: observed.Add(-time.Hour),
			Checkpoint: &modelmeta.ArtifactDigest{Path: "/models/qwen3-14b-awq", Match: true},
		}},
	}
	policy := planner.DefaultPlannerPolicy()
	policy.Images = map[airuntime.BackendID]string{
		airuntime.BackendSGLang: "ghcr.io/scarlix/sglang:v0.4@sha256:" + strings.Repeat("11", 32),
	}
	base, err := planner.NewPlanner(policy, snapshots, models)
	if err != nil {
		t.Fatal(err)
	}
	base.SetNow(func() time.Time { return clock.now })

	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.9.0")
	h.ConfigurePlanner(base)
	if err := h.ConfigureFabric(FabricConfig{
		Nodes:          nodeReg,
		Registry:       fabricReg,
		Replay:         replay,
		BasePlanner:    base,
		LocalSnapshots: snapshots,
		Ledger:         ledger,
	}); err != nil {
		t.Fatalf("ConfigureFabric: %v", err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &fabricFixture{server: ts, nodeReg: nodeReg, fabricReg: fabricReg, clock: clock, snapshots: snapshots}
}

// fabricPost/post with headers.
func fabricDo(t *testing.T, method, url string, headers map[string]string, body interface{}) (int, map[string]interface{}, string) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	var parsed map[string]interface{}
	_ = json.Unmarshal([]byte(raw), &parsed)
	return resp.StatusCode, parsed, raw
}

// enrollNode drives the full enrollment flow over HTTP and returns the
// node credential.
func enrollNode(t *testing.T, fx *fabricFixture, nodeID string) string {
	t.Helper()
	_, inviteBody, _ := fabricDo(t, "POST", fx.server.URL+"/api/fabric/nodes", map[string]string{"Authorization": "Bearer secret"}, map[string]interface{}{"node_id": nodeID})
	secret, _ := inviteBody["secret"].(string)
	if secret == "" {
		t.Fatalf("invite secret missing: %v", inviteBody)
	}
	code, credBody, raw := fabricDo(t, "POST", fx.server.URL+"/api/fabric/agent/enroll", nil, map[string]interface{}{"node_id": nodeID, "invite_secret": secret, "nonce": "n1"})
	if code != http.StatusOK {
		t.Fatalf("enroll: %d %s", code, raw)
	}
	cred, _ := credBody["secret"].(string)
	if cred == "" {
		t.Fatalf("credential missing: %v", credBody)
	}
	return cred
}

// nodeHeaders builds the authenticated node push headers.
func nodeHeaders(cred string, requestID string, ts time.Time) map[string]string {
	return map[string]string{
		"Authorization":             "Bearer " + cred,
		"X-Scarlix-Node-ID":         "node-a",
		"X-Scarlix-Node-Request-ID": requestID,
		"X-Scarlix-Node-Timestamp":  ts.UTC().Format(time.RFC3339Nano),
	}
}

// ---- the tests ------------------------------------------------------------------

func TestFabricRoutes503BeforeConfiguration(t *testing.T) {
	h := NewHandler(guard.New(), scarlix_mode.New(), profiles.New(t.TempDir()), "secret", "19.9.0")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	code, _, raw := fabricDo(t, "GET", ts.URL+"/api/fabric/nodes", map[string]string{"Authorization": "Bearer secret"}, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured fabric must 503: %d %s", code, raw)
	}
}

func TestFabricEnrollmentFlowOverHTTP(t *testing.T) {
	fx := newFabricFixture(t, nil)
	cred := enrollNode(t, fx, "node-a")

	// The issued credential authenticates the node principal directly
	// (the registry-side proof behind every later push).
	if _, err := fx.nodeReg.Authenticate("node-a", cred); err != nil {
		t.Fatalf("the enrolled credential must authenticate: %v", err)
	}

	// The replayed enrollment (a second agent racing the same invite)
	// is refused — the invite is consumed.
	_, inviteBody, _ := fabricDo(t, "POST", fx.server.URL+"/api/fabric/nodes", map[string]string{"Authorization": "Bearer secret"}, map[string]interface{}{"node_id": "node-a"})
	if _, live := inviteBody["secret"]; live {
		t.Fatal("an ACTIVE node must not be re-invitable")
	}

	// The admin list shows the enrolled node, sanitized.
	code, listBody, raw := fabricDo(t, "GET", fx.server.URL+"/api/fabric/nodes", map[string]string{"Authorization": "Bearer secret"}, nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, raw)
	}
	nodes, _ := listBody["nodes"].([]interface{})
	if len(nodes) != 1 {
		t.Fatalf("nodes = %v", listBody)
	}
	node := nodes[0].(map[string]interface{})
	if node["node_id"] != "node-a" || node["status"] != "ACTIVE" {
		t.Fatalf("node view = %v", node)
	}
	if _, leaked := node["credential_digest"]; leaked {
		t.Fatal("the sanitized view must never carry digests")
	}

	// The non-admin principal is refused on the admin surface.
	code, _, raw = fabricDo(t, "GET", fx.server.URL+"/api/fabric/nodes", map[string]string{"Authorization": "Bearer wrong"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong admin token: %d %s", code, raw)
	}
}

func TestFabricInventoryPushReplayProtected(t *testing.T) {
	fx := newFabricFixture(t, nil)
	cred := enrollNode(t, fx, "node-a")
	snap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "snap-node-a-1",
		ObservedAt:    fx.clock.now,
	}

	// No credential → 401 (Tailscale-style reachability authorizes
	// nothing — roadmap §9.2's verbatim rule).
	code, _, raw := fabricDo(t, "POST", fx.server.URL+"/api/fabric/agent/inventory", map[string]string{"X-Scarlix-Node-ID": "node-a"}, snap)
	if code != http.StatusUnauthorized {
		t.Fatalf("no credential: %d %s", code, raw)
	}
	// Wrong credential → 401.
	code, _, raw = fabricDo(t, "POST", fx.server.URL+"/api/fabric/agent/inventory", nodeHeaders("wrong", "req-1", fx.clock.now), snap)
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong credential: %d %s", code, raw)
	}
	// Missing replay headers on a mutating push → 400.
	code, _, raw = fabricDo(t, "POST", fx.server.URL+"/api/fabric/agent/inventory", map[string]string{
		"Authorization":     "Bearer " + cred,
		"X-Scarlix-Node-ID": "node-a",
	}, snap)
	if code != http.StatusBadRequest {
		t.Fatalf("missing replay headers: %d %s", code, raw)
	}
	// The happy push.
	code, _, raw = fabricDo(t, "POST", fx.server.URL+"/api/fabric/agent/inventory", nodeHeaders(cred, "req-1", fx.clock.now), snap)
	if code != http.StatusOK {
		t.Fatalf("inventory push: %d %s", code, raw)
	}
	// The observation landed in the fabric registry.
	obs, ok := fx.fabricReg.Observation("node-a")
	if !ok || obs.Snapshot == nil || obs.Snapshot.SnapshotID != "snap-node-a-1" {
		t.Fatalf("observation = %+v", obs)
	}
	// The REPLAY (identical request ID) → 409.
	code, _, raw = fabricDo(t, "POST", fx.server.URL+"/api/fabric/agent/inventory", nodeHeaders(cred, "req-1", fx.clock.now), snap)
	if code != http.StatusConflict {
		t.Fatalf("replay must be 409: %d %s", code, raw)
	}
	// A fresh request ID succeeds.
	code, _, raw = fabricDo(t, "POST", fx.server.URL+"/api/fabric/agent/inventory", nodeHeaders(cred, "req-2", fx.clock.now), snap)
	if code != http.StatusOK {
		t.Fatalf("fresh request id: %d %s", code, raw)
	}

	// Quarantine: revoking the node refuses subsequent pushes with the
	// named code (the holder of the valid credential learns the
	// quarantine — the agent must stop new work).
	if err := fx.nodeReg.Revoke("admin", "node-a"); err != nil {
		t.Fatal(err)
	}
	code, body, raw := fabricDo(t, "POST", fx.server.URL+"/api/fabric/agent/inventory", nodeHeaders(cred, "req-3", fx.clock.now), snap)
	if code != http.StatusUnauthorized {
		t.Fatalf("quarantined push: %d %s", code, raw)
	}
	errEnvelope, _ := body["error"].(map[string]interface{})
	if errEnvelope == nil || errEnvelope["code"] != "NODE_QUARANTINED" {
		t.Fatalf("the quarantine must be named: %s", raw)
	}
}

func TestFabricExecutionsReportReconciles(t *testing.T) {
	ledger := fabricLedgerFake{held: map[string][]fabric.LedgerReservationView{
		"node-a": {
			{AllocationID: "alloc-running", Held: true},
			{AllocationID: "alloc-gone", Held: true},
			{AllocationID: "alloc-foreign-ledger", Held: false},
		},
	}}
	fx := newFabricFixture(t, ledger)
	cred := enrollNode(t, fx, "node-a")

	report := map[string]interface{}{
		"executions": []map[string]interface{}{
			{"allocation_id": "alloc-running", "state": "running", "observed_at": fx.clock.now.Format(time.RFC3339Nano)},
			{"allocation_id": "alloc-gone", "state": "stopped", "observed_at": fx.clock.now.Format(time.RFC3339Nano)},
			{"allocation_id": "alloc-foreign-agent", "state": "running", "observed_at": fx.clock.now.Format(time.RFC3339Nano)},
		},
	}
	code, body, raw := fabricDo(t, "POST", fx.server.URL+"/api/fabric/agent/executions", nodeHeaders(cred, "req-exec-1", fx.clock.now), report)
	if code != http.StatusOK {
		t.Fatalf("execution report: %d %s", code, raw)
	}
	decisions, _ := body["decisions"].([]interface{})
	if len(decisions) != 3 {
		t.Fatalf("decisions = %v (%s)", decisions, raw)
	}
	by := map[string]string{}
	for _, d := range decisions {
		dm := d.(map[string]interface{})
		by[dm["allocation_id"].(string)] = dm["action"].(string)
	}
	if by["alloc-running"] != string(fabric.ActionReservationStands) {
		t.Fatalf("alloc-running: %v", by)
	}
	if by["alloc-gone"] != string(fabric.ActionReconcileRequired) {
		t.Fatalf("alloc-gone: %v", by)
	}
	if by["alloc-foreign-agent"] != string(fabric.ActionStopForeignExecution) {
		t.Fatalf("alloc-foreign-agent: %v", by)
	}
}

func TestFabricPlanRoutePrefersFabricPlanner(t *testing.T) {
	// Zero remote views: the fabric-configured route computes the SAME
	// decision the single-node planner would (the HTTP-level
	// regression pin — the route is unchanged, the surface is the same).
	fx := newFabricFixture(t, nil)
	body := map[string]interface{}{
		"request_id":     "req-plan-1",
		"model_id":       "qwen3-14b-awq",
		"task":           "chat-serving",
		"context_tokens": 16384,
		"max_concurrent": 4,
		"latency_class":  "interactive",
	}
	code, parsed, raw := fabricDo(t, "POST", fx.server.URL+"/api/compute/plan", map[string]string{"Authorization": "Bearer secret"}, body)
	if code != http.StatusOK {
		t.Fatalf("plan: %d %s", code, raw)
	}
	if parsed["selected"] != true {
		t.Fatalf("plan must select: %s", raw)
	}
	plan, _ := parsed["plan"].(map[string]interface{})
	if plan["node_id"] != "local" {
		t.Fatalf("zero remote views → the local node, got %v", plan["node_id"])
	}

	// An observed remote node joins the fabric planner's views. The
	// local GPU is now 40% utilized; the remote's idle GPU (artifact
	// present + the SAME verified identity + class A) wins the
	// utilization tie-break after locality/latency tie — the
	// deterministic §5.2 ordering. (The node is ENROLLED first: only
	// ACTIVE nodes may push observations.)
	enrollNode(t, fx, "node-b")
	fx.snapshots.snap.GPUs[0].UtilizationPct = 40
	remoteSnap := &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "snap-node-b-1",
		ObservedAt:    fx.clock.now,
		Host:          storage.HostResources{CPUThreads: 8, RAMTotalBytes: 16 << 30, RAMAvailableBytes: 8 << 30},
		GPUs: []inventory.GPU{
			{ID: "gpu.nvidia.0", UUID: "GPU-bbbb", VRAMTotalMB: 16384, VRAMFreeMB: 16384, UtilizationPct: 0, Healthy: true},
		},
		Runtimes: []inventory.Runtime{{ID: "sglang", Running: true, Healthy: true, Port: 30000}},
		Models: []inventory.ModelExecutionView{{
			Catalog: inventory.Model{ID: "qwen3-14b-awq", Present: true},
			ArtifactVerification: &modelmeta.ArtifactVerification{
				ModelID:             "qwen3-14b-awq",
				ManifestFingerprint: planModelsManifestFingerprint(),
				AllVerified:         true,
				VerifiedAt:          fx.clock.now.Add(-time.Hour),
			},
		}},
	}
	if err := fx.fabricReg.ObserveInventory("node-b", remoteSnap, "A"); err != nil {
		t.Fatalf("observe node-b: %v", err)
	}
	code, parsed, raw = fabricDo(t, "POST", fx.server.URL+"/api/compute/plan", map[string]string{"Authorization": "Bearer secret"}, body)
	if code != http.StatusOK {
		t.Fatalf("plan with remote: %d %s", code, raw)
	}
	plan, _ = parsed["plan"].(map[string]interface{})
	if plan == nil || plan["node_id"] != "node-b" {
		t.Fatalf("the idle remote GPU must win the utilization tie-break: %s", raw)
	}
	// Deterministic: identical POST → identical PlanID.
	_, second, _ := fabricDo(t, "POST", fx.server.URL+"/api/compute/plan", map[string]string{"Authorization": "Bearer secret"}, body)
	plan2, _ := second["plan"].(map[string]interface{})
	if plan["plan_id"] != plan2["plan_id"] {
		t.Fatalf("identical POSTs must produce identical PlanIDs: %v vs %v", plan["plan_id"], plan2["plan_id"])
	}
}

// planModelsManifestFingerprint reproduces the fixture manifest's
// fingerprint for the remote node's verification binding.
func planModelsManifestFingerprint() string {
	m := &modelmeta.Manifest{
		SchemaVersion: modelmeta.SchemaVersionV2,
		ID:            "qwen3-14b-awq",
		Revision:      "rev-1",
	}
	m.Checkpoint.Format = "safetensors"
	m.Checkpoint.SHA256 = strings.Repeat("ab", 32)
	m.Architecture.Kind = modelmeta.ArchitectureDense
	m.Architecture.SharedBytes = 8 << 30
	m.Runtime.ContextLimit = 32768
	m.Runtime.KVCache.BytesPerTokenEstimate = 8192
	m.Runtime.SupportedBackends = []string{"sglang"}
	m.Storage.ModelPath = "/models/qwen3-14b-awq"
	return modelmeta.ManifestFingerprint(m)
}
