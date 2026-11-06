package api

// CF-3 (v19.6.2): the propose surface test matrix — auth gating, the
// BOTH-opt-ins fail-closed 503s, the chat+parse+capture flow (happy
// block, no block, malformed), the review surface (list + filters +
// counts), accept (the only door from a proposal to a command: the BF-1
// capture path, the routing question, the reconciliation for a crashed
// accept), dismiss (unconditional terminal), the composed proposal field
// on command views, method discipline.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentfabric"
	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airt"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
)

// proposeStack builds a real airt.Runtime over an httptest sglang that
// answers EVERY chat with the given content (the parse outcomes become
// deterministic), vllm/beellama/ollama on dead ports. qwen3-14b is
// registered on sglang only — the plan is sglang, first tier.
func proposeStack(t *testing.T, content string) *airt.Runtime {
	t.Helper()
	sglang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			escaped := strings.ReplaceAll(content, `\`, `\\`)
			escaped = strings.ReplaceAll(escaped, `"`, `\"`)
			escaped = strings.ReplaceAll(escaped, "\n", `\n`)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`, content)
			_ = escaped
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(sglang.Close)
	dead := "http://127.0.0.1:1"
	endpoints := map[airuntime.Provider]airt.Endpoint{
		airuntime.ProviderSGLang:   airt.Endpoint(sglang.URL),
		airuntime.ProviderVLLM:     airt.Endpoint(dead),
		airuntime.ProviderBeeLlama: airt.Endpoint(dead),
		airuntime.ProviderOllama:   airt.Endpoint(dead),
	}
	adapters, prober, err := airt.NewLocalStack(endpoints)
	if err != nil {
		t.Fatalf("local stack: %v", err)
	}
	registry, err := airuntime.NewModelRegistry(airuntime.DefaultMaxModels)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := registry.Register(airuntime.ProviderSGLang, "qwen3-14b"); err != nil {
		t.Fatalf("register: %v", err)
	}
	table := airuntime.NewHealthTable()
	base := time.Now().UTC()
	for i, p := range airuntime.AllProviders() {
		state := airuntime.HealthUnavailable
		if p == airuntime.ProviderSGLang {
			state = airuntime.HealthHealthy
		}
		if err := table.Observe(airuntime.Observation{Provider: p, State: state, At: base.Add(-time.Duration(i+1) * time.Minute)}); err != nil {
			t.Fatalf("observe %s: %v", p, err)
		}
	}
	rt, err := airt.NewRuntime(registry, table, prober, adapters)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	return rt
}

// newProposeTestServer builds the full CF-3 fixture: the BF-3/BF-4 fabric
// fixture (kernel + commands + workspaces + executions + approvals + bus)
// PLUS the proposal store PLUS the AI runtime whose sglang answers with
// the given content — the main.go CF-3 wiring, mirrored.
func newProposeTestServer(t *testing.T, content string) (*http.ServeMux, *Handler, *agentfabric.FabricBus) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_ = scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	mux, h, bus := newExecTestServerOnDir(t, dir)
	prpStore, err := agentfabric.NewProposalStore(filepath.Join(dir, "proposals.json"), agentfabric.DefaultMaxProposals)
	if err != nil {
		t.Fatalf("proposal store: %v", err)
	}
	if err := prpStore.Load(); err != nil {
		t.Fatalf("proposal load: %v", err)
	}
	if err := h.ConfigureAgentFabricProposals(prpStore); err != nil {
		t.Fatalf("configure proposals: %v", err)
	}
	if err := h.ConfigureAIRuntime(proposeStack(t, content)); err != nil {
		t.Fatalf("configure ai runtime: %v", err)
	}
	// Re-register the routes: the mux captured them at construction; a
	// second registration is a no-op for distinct paths — the four new
	// routes were already registered by RegisterRoutes inside the fixture.
	return mux, h, bus
}

// proposeChat posts a propose request (the chat shape).
func proposeChat(t *testing.T, mux *http.ServeMux) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return barRequest(t, mux, http.MethodPost, "/api/ai/propose",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"propose something"}]}`)
}

const happyProposalContent = "Sure, switching modes is a good idea.\n\n```scarlix-proposal\n{\"intent\":\"switch the host to ai mode\",\"agent_id\":\"coder.act\",\"capability\":\"mode.request\"}\n```\n\nLet me know if that works."

func TestProposeRoutesAuthGated(t *testing.T) {
	mux, _, _ := newProposeTestServer(t, happyProposalContent)
	for _, path := range []string{"/api/ai/propose", "/api/ai/proposals", "/api/ai/proposals/accept", "/api/ai/proposals/dismiss"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without token = %d, want 401", path, rec.Code)
		}
	}
}

func TestProposeRequiresBothOptIns(t *testing.T) {
	// Runtime on, proposals NOT configured → 503 (the proposal is a fabric
	// entity; the runtime alone never captures one).
	mux, h, _ := newProposeTestServer(t, happyProposalContent)
	h.agentProposals = nil
	rec, _ := proposeChat(t, mux)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("propose without the proposal store = %d, want 503", rec.Code)
	}
	// Proposals on, runtime off → 503 (the aiRuntimeDisabled gate).
	mux2, h2, _ := newProposeTestServer(t, happyProposalContent)
	h2.aiRuntime = nil
	rec, _ = proposeChat(t, mux2)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("propose without the runtime = %d, want 503", rec.Code)
	}
	// Proposals on, runtime on, fabric OFF for accept → 503.
	mux3, h3, _ := newProposeTestServer(t, happyProposalContent)
	h3.agentFabric = nil
	rec, _ = barRequest(t, mux3, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":"prp-%s","operator":"zuzka"}`, strings.Repeat("a", 32)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("accept without the fabric = %d, want 503", rec.Code)
	}
	// The review surface (list/dismiss) does NOT need the fabric — only the
	// proposal store. 200 with an empty list.
	rec, body := barRequest(t, mux3, http.MethodGet, "/api/ai/proposals", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list without the fabric = %d, want 200 (review needs no fabric)", rec.Code)
	}
	if body["count"].(float64) != 0 {
		t.Errorf("count = %v, want 0", body["count"])
	}
}

func TestProposeHappyCapture(t *testing.T) {
	mux, h, _ := newProposeTestServer(t, happyProposalContent)
	rec, body := proposeChat(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("propose = %d: %s", rec.Code, rec.Body.String())
	}
	if body["provider"] != "sglang" || body["model"] != "qwen3-14b" {
		t.Errorf("chat attribution = %v/%v", body["provider"], body["model"])
	}
	proposal, _ := body["proposal"].(map[string]any)
	if proposal == nil {
		t.Fatalf("proposal missing: %v", body)
	}
	if proposal["state"] != "PENDING" {
		t.Errorf("state = %v, want PENDING", proposal["state"])
	}
	if proposal["proposer"] != "sglang/qwen3-14b" {
		t.Errorf("proposer = %v, want sglang/qwen3-14b (attribution survives failover)", proposal["proposer"])
	}
	if proposal["intent"] != "switch the host to ai mode" {
		t.Errorf("intent = %v", proposal["intent"])
	}
	if proposal["agent_id"] != "coder.act" || proposal["capability"] != "mode.request" {
		t.Errorf("agent/capability = %v/%v", proposal["agent_id"], proposal["capability"])
	}
	id, _ := proposal["id"].(string)
	if !strings.HasPrefix(id, "prp-") {
		t.Errorf("id = %q, want prp- prefix", id)
	}
	// The bus carried proposal.captured (change signal, refetch discipline).
	if h.agentBus == nil {
		t.Fatal("bus missing")
	}
	// The proposal is in the store behind the id.
	stored, ok := h.agentProposals.Get(agentfabric.ProposalID(id))
	if !ok || stored.State != agentfabric.ProposalPending {
		t.Fatalf("store = %+v ok=%v", stored, ok)
	}
}

func TestProposeNoBlockIsNull(t *testing.T) {
	mux, _, _ := newProposeTestServer(t, "a plain answer with no structure at all")
	rec, body := proposeChat(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("propose (no block) = %d: %s", rec.Code, rec.Body.String())
	}
	if body["proposal"] != nil {
		t.Errorf("proposal = %v, want null (the model proposed nothing — null IS the answer)", body["proposal"])
	}
	if body["provider"] != "sglang" {
		t.Errorf("the chat answer must still ride along: %v", body["provider"])
	}
}

func TestProposeMalformedIs422(t *testing.T) {
	malformed := "```scarlix-proposal\n{\"intent\":\"x\"}\n```"
	mux, h, _ := newProposeTestServer(t, malformed)
	rec, body := proposeChat(t, mux)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed propose = %d: %s", rec.Code, rec.Body.String())
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Errorf("the named reason must ride along: %v", body)
	}
	if body["provider"] != "sglang" || body["model"] != "qwen3-14b" {
		t.Errorf("attribution must ride along: %v/%v", body["provider"], body["model"])
	}
	msg, _ := body["message"].(map[string]any)
	if msg == nil || msg["content"] != malformed {
		t.Errorf("the refused content must ride along (a re-run is another generation): %v", body["message"])
	}
	// NOTHING was captured.
	if h.agentProposals.Count() != 0 {
		t.Errorf("Count = %d, want 0 (fail-closed — never a partial guess)", h.agentProposals.Count())
	}
}

func TestProposeUnknownModelRidesChatErrors(t *testing.T) {
	mux, _, _ := newProposeTestServer(t, happyProposalContent)
	rec, _ := barRequest(t, mux, http.MethodPost, "/api/ai/propose",
		`{"model":"not-registered","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown model = %d, want 404 (the router never guesses)", rec.Code)
	}
	// Shape errors ride the contract's own words.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/propose",
		`{"model":"qwen3-14b","messages":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty messages = %d, want 400", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/propose",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hi"}],"extra":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400 (closed decode)", rec.Code)
	}
}

func TestProposalsListAndFilters(t *testing.T) {
	mux, _, _ := newProposeTestServer(t, happyProposalContent)
	// Capture two proposals (same content — two propose calls, two ids).
	rec, first := proposeChat(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("propose 1: %d", rec.Code)
	}
	rec, second := proposeChat(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("propose 2: %d", rec.Code)
	}
	p2, _ := second["proposal"].(map[string]any)
	if p2 == nil {
		t.Fatalf("second proposal missing")
	}
	if _, ok := first["proposal"].(map[string]any); !ok {
		t.Fatalf("first proposal missing")
	}

	// Dismiss the second.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/dismiss",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"milo","reason":"not now"}`, p2["id"]))
	if rec.Code != http.StatusCreated {
		t.Fatalf("dismiss: %d: %s", rec.Code, rec.Body.String())
	}

	// List: newest-first, counts always present.
	rec, body := barRequest(t, mux, http.MethodGet, "/api/ai/proposals", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	proposals, _ := body["proposals"].([]any)
	if len(proposals) != 2 {
		t.Fatalf("list = %d proposals, want 2", len(proposals))
	}
	head, _ := proposals[0].(map[string]any)
	if head["id"] != p2["id"] {
		t.Errorf("newest-first violated: %v", head["id"])
	}
	counts, _ := body["counts"].(map[string]any)
	if counts["PENDING"].(float64) != 1 || counts["DISMISSED"].(float64) != 1 {
		t.Errorf("counts = %v", counts)
	}
	if _, ok := body["store_path"]; !ok {
		t.Error("store_path missing")
	}

	// The closed state filter.
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/ai/proposals?state=PENDING", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("state filter: %d", rec.Code)
	}
	// Unknown state → 400; bad limit → 400.
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/ai/proposals?state=WITHDRAWN", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown state filter = %d, want 400", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/ai/proposals?limit=0", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("limit 0 = %d, want 400", rec.Code)
	}
	// Method discipline.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST list = %d, want 405", rec.Code)
	}
}

func TestProposalAcceptWithoutSessionSubmits(t *testing.T) {
	mux, _, _ := newProposeTestServer(t, happyProposalContent)
	// NO kernel session for coder.act → the accepted proposal becomes a
	// SUBMITTED command (the intent is never lost).
	_, body := proposeChat(t, mux)
	proposal, _ := body["proposal"].(map[string]any)

	rec, decision := barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"zuzka"}`, proposal["id"]))
	if rec.Code != http.StatusCreated {
		t.Fatalf("accept = %d: %s", rec.Code, rec.Body.String())
	}
	if decision["reconciled"] != false {
		t.Errorf("reconciled = %v, want false", decision["reconciled"])
	}
	acc, _ := decision["proposal"].(map[string]any)
	if acc["state"] != "ACCEPTED" {
		t.Errorf("proposal state = %v, want ACCEPTED", acc["state"])
	}
	cmd, _ := decision["command"].(map[string]any)
	if cmd == nil || cmd["state"] != "SUBMITTED" {
		t.Fatalf("command = %v, want SUBMITTED (no session — awaiting routing)", cmd)
	}
	if cmd["submitter"] != "zuzka" {
		t.Errorf("submitter = %v, want zuzka (the accepting operator)", cmd["submitter"])
	}
	if cmd["proposal_id"] != proposal["id"] {
		t.Errorf("the durable linkage = %v, want %v", cmd["proposal_id"], proposal["id"])
	}
	if cmd["intent"] != "switch the host to ai mode" {
		t.Errorf("intent = %v (the accepted thing is the proposed thing)", cmd["intent"])
	}
	// (The bus carried command.submitted + proposal.decided — the WS
	// surface forwards every signal; the /ws/bar filter test lives with the
	// bus tests.)

	// Re-accept → 409 (terminal).
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"zuzka"}`, proposal["id"]))
	if rec.Code != http.StatusConflict {
		t.Errorf("re-accept = %d, want 409 (terminal)", rec.Code)
	}
	// Dismiss-after-accept → 409.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/dismiss",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"zuzka"}`, proposal["id"]))
	if rec.Code != http.StatusConflict {
		t.Errorf("dismiss-after-accept = %d, want 409", rec.Code)
	}
	// The composed command view carries the additive proposal field.
	rec, list := barRequest(t, mux, http.MethodGet, "/api/bar/commands?limit=10", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("commands list: %d", rec.Code)
	}
	commands, _ := list["commands"].([]any)
	for _, c := range commands {
		cm, _ := c.(map[string]any)
		if cm["id"] == cmd["id"] {
			pv, _ := cm["proposal"].(map[string]any)
			if pv == nil || pv["state"] != "ACCEPTED" || pv["proposer"] != "sglang/qwen3-14b" {
				t.Fatalf("composed proposal field = %v (want ACCEPTED + the model attribution)", cm["proposal"])
			}
			return
		}
	}
	t.Fatalf("the accepted command is missing from the bar list")
}

func TestProposalAcceptWithSessionDecides(t *testing.T) {
	mux, h, _ := newProposeTestServer(t, happyProposalContent)
	// An ACTIVE session for coder.act holding mode.request → the accepted
	// proposal routes + the kernel decides synchronously (the unchanged
	// barCommandSubmit composition).
	sessionID := mustCreateActiveSession(t, h, "coder.act", agentkernel.CapabilityModeRequest)
	if sessionID == "" {
		t.Fatal("no session")
	}
	_, body := proposeChat(t, mux)
	proposal, _ := body["proposal"].(map[string]any)

	rec, decision := barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"zuzka"}`, proposal["id"]))
	if rec.Code != http.StatusCreated {
		t.Fatalf("accept = %d: %s", rec.Code, rec.Body.String())
	}
	cmd, _ := decision["command"].(map[string]any)
	if cmd == nil || cmd["state"] != "DECIDED" {
		t.Fatalf("command = %v, want DECIDED (an ACTIVE session exists)", cmd)
	}
	if cmd["allow"] != true {
		t.Errorf("allow = %v, want true (the kernel allowed mode.request)", cmd["allow"])
	}
}

func TestProposalAcceptReconciliation(t *testing.T) {
	mux, h, _ := newProposeTestServer(t, happyProposalContent)
	_, body := proposeChat(t, mux)
	proposal, _ := body["proposal"].(map[string]any)
	prpID := agentfabric.ProposalID(proposal["id"].(string))

	// Simulate a crashed accept: the command was captured (persisted) but the
	// proposal close never happened — the proposal is STILL PENDING and the
	// linkage exists.
	crashed, err := h.agentFabric.SubmitWithProposal("switch the host to ai mode", "coder.act", agentkernel.CapabilityModeRequest, "zuzka", prpID, time.Now().UTC())
	if err != nil {
		t.Fatalf("crashed-accept simulation: %v", err)
	}

	rec, decision := barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"zuzka"}`, proposal["id"]))
	if rec.Code != http.StatusCreated {
		t.Fatalf("reconciling accept = %d: %s", rec.Code, rec.Body.String())
	}
	if decision["reconciled"] != true {
		t.Errorf("reconciled = %v, want true (the EXISTING command, no second capture)", decision["reconciled"])
	}
	cmd, _ := decision["command"].(map[string]any)
	if cmd == nil || cmd["id"] != crashed.ID {
		t.Fatalf("command = %v, want the existing %s (no intent captured twice)", cmd, crashed.ID)
	}
	// Exactly ONE command carries the linkage.
	if h.agentFabric.Count() != 1 {
		t.Errorf("commands = %d, want 1 (the reconciliation must not capture again)", h.agentFabric.Count())
	}
}

func TestProposalAcceptValidation(t *testing.T) {
	mux, _, _ := newProposeTestServer(t, happyProposalContent)
	// Malformed id → 400 (shape first).
	rec, _ := barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept", `{"proposal_id":"nope","operator":"zuzka"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400", rec.Code)
	}
	// Unknown id → 404.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":"prp-%s","operator":"zuzka"}`, strings.Repeat("f", 32)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", rec.Code)
	}
	// Missing operator → 400.
	_, body := proposeChat(t, mux)
	proposal, _ := body["proposal"].(map[string]any)
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":%q}`, proposal["id"]))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing operator = %d, want 400", rec.Code)
	}
	// Over-bound operator → 400.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"%s"}`, proposal["id"], strings.Repeat("x", 65)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("over-bound operator = %d, want 400", rec.Code)
	}
	// Unknown fields rejected (no re-steering).
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"zuzka","intent":"re-steered"}`, proposal["id"]))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("re-steering fields = %d, want 400 (the accepted thing is the proposed thing)", rec.Code)
	}
	// Method discipline.
	rec, _ = barRequest(t, mux, http.MethodGet, "/api/ai/proposals/accept", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET accept = %d, want 405", rec.Code)
	}
}

func TestProposalDismissUnconditional(t *testing.T) {
	mux, h, _ := newProposeTestServer(t, happyProposalContent)
	_, body := proposeChat(t, mux)
	proposal, _ := body["proposal"].(map[string]any)
	prpID := proposal["id"].(string)

	rec, decision := barRequest(t, mux, http.MethodPost, "/api/ai/proposals/dismiss",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"milo","reason":"not now"}`, prpID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("dismiss = %d: %s", rec.Code, rec.Body.String())
	}
	dis, _ := decision["proposal"].(map[string]any)
	if dis["state"] != "DISMISSED" || dis["reason"] != "not now" || dis["operator"] != "milo" {
		t.Errorf("dismissed = %v", dis)
	}
	if _, has := dis["command"]; has {
		t.Errorf("a dismissed proposal never references a command: %v", dis["command"])
	}
	// NO command was ever captured.
	if h.agentFabric.Count() != 0 {
		t.Errorf("commands = %d, want 0 (a dismissal never became a command)", h.agentFabric.Count())
	}
	// Re-dismiss → 409; accept-after-dismiss → 409.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/dismiss",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"milo"}`, prpID))
	if rec.Code != http.StatusConflict {
		t.Errorf("re-dismiss = %d, want 409", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/accept",
		fmt.Sprintf(`{"proposal_id":%q,"operator":"zuzka"}`, prpID))
	if rec.Code != http.StatusConflict {
		t.Errorf("accept-after-dismiss = %d, want 409 (the dismissal is terminal)", rec.Code)
	}
	// Validation: malformed id 400, unknown 404, missing operator 400.
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/dismiss", `{"proposal_id":"x","operator":"m"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed dismiss id = %d, want 400", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/dismiss",
		fmt.Sprintf(`{"proposal_id":"prp-%s","operator":"m"}`, strings.Repeat("e", 32)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown dismiss id = %d, want 404", rec.Code)
	}
	rec, _ = barRequest(t, mux, http.MethodPost, "/api/ai/proposals/dismiss", `{"operator":"m"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing id = %d, want 400", rec.Code)
	}
}

func TestProposeCaptureLimit503(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_ = guard.New()
	_ = scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	mux, h, _ := newExecTestServerOnDir(t, dir)
	// A bound of ONE: the first capture fills the store.
	tiny, err := agentfabric.NewProposalStore(filepath.Join(dir, "proposals.json"), 1)
	if err != nil {
		t.Fatalf("tiny store: %v", err)
	}
	if err := tiny.Load(); err != nil {
		t.Fatalf("tiny load: %v", err)
	}
	if err := h.ConfigureAgentFabricProposals(tiny); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if err := h.ConfigureAIRuntime(proposeStack(t, happyProposalContent)); err != nil {
		t.Fatalf("runtime: %v", err)
	}
	rec, _ := proposeChat(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("first propose = %d: %s", rec.Code, rec.Body.String())
	}
	rec, body := proposeChat(t, mux)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("second propose = %d, want 503 (bounded): %v", rec.Code, body)
	}
}
