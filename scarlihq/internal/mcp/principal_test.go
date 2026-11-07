package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/guard"
	"github.com/MoZoHuJa/OS/scarlihq/internal/profiles"
	"github.com/MoZoHuJa/OS/scarlihq/internal/scarlix_mode"
)

// The v19.6.7 MCP identity-binding proofs (KPK-19.6.6-P1-01): the SAME
// shared authorization function decides /rpc's scarlix_mode_set as the
// REST boundaries — the acceptance matrix through the JSON-RPC surface.

const mcpZmorYAML = `name: zmor
display_name: "Zmor (admin)"
role: admin
permissions: [all]
scarlix_mode:
  default: ai
  allowed: [ai, stop, game, creative, turbo, offline, tv]
  can_switch: true
hud_theme: iron-man
token_budget: unlimited
`

const mcpHugoYAML = `name: hugo
display_name: "Hugo (syn)"
role: family
permissions: [read_files, read_photos, play_games, coding_tutor, ai_chat]
scarlix_mode:
  default: game
  allowed: [game, ai]
  can_switch: true
hud_theme: gaming
token_budget:
  monthly: 100000
  model: qwen2.5:3b
`

const mcpXoxYAML = `name: xox
display_name: "XOX (dcera)"
role: family
permissions: [read_photos_only, kids_content, ai_chat]
scarlix_mode:
  default: ai
  allowed: [ai]
  can_switch: false
hud_theme: creative
token_budget:
  monthly: 10000
  model: qwen2.5:3b
content_filter:
  enabled: true
  blocklist: [violence, adult, drugs, gambling]
`

// mcpStack builds the JSON-RPC server with the multi-user token index.
func mcpStack(t *testing.T) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"hugo": mcpHugoYAML,
		"xox":  mcpXoxYAML,
		"zmor": mcpZmorYAML,
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
	}
	mode := scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	s := NewServer(guard.New(), mode, profiles.New(dir), "secret", "19.6.7")
	index, err := profiles.NewTokenIndex(map[string]string{
		"hugo": "hugo-tok",
		"xox":  "xox-tok",
		"zmor": "zmor-tok",
	}, "secret")
	if err != nil {
		t.Fatalf("token index: %v", err)
	}
	if err := s.ConfigureProfileAuthz(index); err != nil {
		t.Fatalf("profile authz: %v", err)
	}
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	return mux
}

// rpcCall posts one tools/call with the given bearer token.
func rpcCall(t *testing.T, mux *http.ServeMux, token, tool string, args map[string]any) (int, map[string]any) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("rpc response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return rec.Code, out
}

func rpcErrCode(out map[string]any) (int, bool) {
	e, ok := out["error"].(map[string]any)
	if !ok {
		return 0, false
	}
	code, _ := e["code"].(float64)
	return int(code), true
}

// TestMCPFamilyTokenCannotSelectAdminProfile: hugo's token declaring
// profile=zmor on scarlix_mode_set is refused (-32006, the identity
// mismatch) — the same decision the REST boundary makes, through the SAME
// shared Authorize.
func TestMCPFamilyTokenCannotSelectAdminProfile(t *testing.T) {
	mux := mcpStack(t)
	_, out := rpcCall(t, mux, "hugo-tok", "scarlix_mode_set", map[string]any{"mode": "ai", "profile": "zmor"})
	code, ok := rpcErrCode(out)
	if !ok || code != -32006 {
		t.Fatalf("hugo's token + profile=zmor must be the -32006 identity refusal, got %+v", out)
	}
}

// TestMCPXoxDerivedPolicyRefusesSwitch: xox's token with NO profile
// argument is DERIVED — her can_switch=false refuses every switch
// (-32005, the mode policy named).
func TestMCPXoxDerivedPolicyRefusesSwitch(t *testing.T) {
	mux := mcpStack(t)
	_, out := rpcCall(t, mux, "xox-tok", "scarlix_mode_set", map[string]any{"mode": "game"})
	code, ok := rpcErrCode(out)
	if !ok || code != -32005 {
		t.Fatalf("xox's DERIVED policy must refuse the switch (-32005 can_switch=false), got %+v", out)
	}
}

// TestMCPZmorTokenModeSetAllowed: zmor's own token switches within the
// admin policy — accepted (the accepted write lands in the test bridge).
func TestMCPZmorTokenModeSetAllowed(t *testing.T) {
	mux := mcpStack(t)
	_, out := rpcCall(t, mux, "zmor-tok", "scarlix_mode_set", map[string]any{"mode": "ai"})
	if _, isErr := rpcErrCode(out); isErr {
		t.Fatalf("zmor's token switching within his policy must be accepted, got %+v", out)
	}
}

// TestMCPHouseholdActAsStillPolicyBound: the household (admin) token
// act-as xox still honors xox's can_switch=false — the act-as only
// reduces.
func TestMCPHouseholdActAsStillPolicyBound(t *testing.T) {
	mux := mcpStack(t)
	_, out := rpcCall(t, mux, "secret", "scarlix_mode_set", map[string]any{"mode": "game", "profile": "xox"})
	code, ok := rpcErrCode(out)
	if !ok || code != -32005 {
		t.Fatalf("the household act-as xox must still honor her policy, got %+v", out)
	}
}

// TestMCPUnknownTokenRefused: an unknown Bearer authenticates nobody.
func TestMCPUnknownTokenRefused(t *testing.T) {
	mux := mcpStack(t)
	code, _ := rpcCall(t, mux, "intruder-tok", "scarlix_mode_get", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("an unknown token must be 401 on /rpc, got %d", code)
	}
}

// TestMCPReadonlyToolsServeFamilyTokens: the read-only tools serve the
// family principal (mode_get) — the family surface stays open.
func TestMCPReadonlyToolsServeFamilyTokens(t *testing.T) {
	mux := mcpStack(t)
	code, out := rpcCall(t, mux, "xox-tok", "scarlix_mode_get", nil)
	if code != http.StatusOK {
		t.Fatalf("mode_get must serve the family token, got %d", code)
	}
	if _, isErr := rpcErrCode(out); isErr {
		t.Fatalf("mode_get must not error, got %+v", out)
	}
}
