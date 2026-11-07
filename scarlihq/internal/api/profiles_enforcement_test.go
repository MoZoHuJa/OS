package api

import (
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

// The v19.6.6 profile-policy enforcement proofs (KPK-19.6.5-P1-03): the
// declared family policy is enforced SERVER-SIDE at the action boundaries
// — /api/mode/switch and /api/ai/chat — not by a dashboard hiding
// controls. Fixtures mirror the repository's real profiles.

const (
	enfXoxYAML = `name: xox
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
	enfHugoYAML = `name: hugo
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
	// gamer carries NO ai_chat permission — the chat refusal proof.
	enfGamerYAML = `name: gamer
display_name: "Gamer (guest)"
role: family
permissions: [play_games]
scarlix_mode:
  default: game
  allowed: [game]
  can_switch: false
hud_theme: gaming
token_budget: unlimited
`
	// broken is a policy-bearing file that fails the typed schema — the
	// fail-closed proof (a broken policy file is never an unenforced bypass).
	enfBrokenYAML = `name: broken
display_name: "Broken"
role: family
permissions: [ai_chat]
scarlix_mode:
  default: ai
  allowed: [ai]
  can_switch: false
hud_theme: default
token_budget:
  - 100
`
)

func writeEnforcementProfiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"xox":    enfXoxYAML,
		"hugo":   enfHugoYAML,
		"gamer":  enfGamerYAML,
		"broken": enfBrokenYAML,
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
	}
	return dir
}

func newEnforcementMux(t *testing.T, withRuntime bool) *http.ServeMux {
	t.Helper()
	dir := writeEnforcementProfiles(t)
	// The desired-mode bridge points INSIDE the temp dir (no /var/lib writes).
	mode := scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	h := NewHandler(guard.New(), mode, profiles.New(dir), "secret", "19.6.7")
	if withRuntime {
		if err := h.ConfigureAIRuntime(aiStack(t, http.StatusOK, http.StatusOK)); err != nil {
			t.Fatal(err)
		}
		// v19.6.7 (KPK-19.6.6-P2-02): the enforcement tests run with the
		// budget ledger CONFIGURED — the declared monthly budgets are the
		// ENFORCED quotas on these chats, not informational metadata.
		ledger, err := profiles.NewBudgetLedger("") // memory-only
		if err != nil {
			t.Fatal(err)
		}
		if err := h.ConfigureBudgetLedger(ledger); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func enfRequest(t *testing.T, mux *http.ServeMux, method, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestProfilesAPIReturnsPolicyAndIssues: /api/profiles returns the FULL
// policy-bearing set (permissions, scarlix_mode, token_budget,
// content_filter — the audit's acceptance: the exact configured data) and
// the EXPLICIT issues of invalid files (never silent filename fallbacks).
func TestProfilesAPIReturnsPolicyAndIssues(t *testing.T) {
	mux := newEnforcementMux(t, false)
	code, body := enfRequest(t, mux, http.MethodGet, "/api/profiles", "", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", code, body)
	}
	for _, want := range []string{
		`"display_name":"XOX (dcera)"`,      // the exact configured display name
		`"permissions":["read_photos_only"`, // the declared permissions
		`"scarlix_mode"`,                    // the mode policy
		`"monthly":10000`,                   // the typed token budget
		`"content_filter"`,                  // XOX's child-safe policy
		`"invalid":[{`,                      // the broken file surfaced explicitly
		`"broken.yaml"`,                     // by file name
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the profile API must carry %s — the full declared policy, no silent fallbacks; got: %s", want, body)
		}
	}
	// The v19.6.5 defect shape MUST NOT appear: a filename-only fallback
	// serialized DisplayName == name with no policy fields.
	if strings.Contains(body, `"display_name":"xox"`) {
		t.Fatalf("the filename-only fallback shape leaked — the v19.6.5 silent-fallback defect: %s", body)
	}
}

// TestModeSwitchProfilePolicyEnforced: the scarlix_mode policy decides the
// switch SERVER-SIDE — XOX (can_switch=false) is refused 403 with the
// policy named; Hugo switching OUTSIDE his allowed set is refused 403 with
// the allowed set in the message; Hugo switching INSIDE it is accepted.
func TestModeSwitchProfilePolicyEnforced(t *testing.T) {
	mux := newEnforcementMux(t, false)

	// XOX cannot switch AT ALL — 403, the refusal names the policy.
	code, body := enfRequest(t, mux, http.MethodPost, "/api/mode?set=game&profile=xox", "", nil)
	if code != http.StatusForbidden || !strings.Contains(body, "can_switch=false") {
		t.Fatalf("XOX's can_switch=false must refuse the switch 403 with the policy named, got %d: %s", code, body)
	}

	// Hugo may not switch to turbo (outside [game, ai]) — 403 by name.
	code, body = enfRequest(t, mux, http.MethodPost, "/api/mode?set=turbo&profile=hugo", "", nil)
	if code != http.StatusForbidden || !strings.Contains(body, "allowed set") {
		t.Fatalf("a target outside Hugo's allowed set must refuse 403 naming the allowed set, got %d: %s", code, body)
	}

	// A profile that fails the typed schema FAILS CLOSED — 400, never an
	// unenforced bypass through a broken policy file.
	code, body = enfRequest(t, mux, http.MethodPost, "/api/mode?set=ai&profile=broken", "", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("an unloadable profile must fail closed 400, got %d: %s", code, body)
	}

	// A nonexistent profile reference is 400 — explicit, not silent.
	code, _ = enfRequest(t, mux, http.MethodPost, "/api/mode?set=ai&profile=ghost", "", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("a nonexistent profile reference must be 400, got %d", code)
	}

	// The counterweight: Hugo switching INSIDE his allowed set is accepted
	// (202 — the host bridge applies it within 5s).
	code, body = enfRequest(t, mux, http.MethodPost, "/api/mode?set=ai&profile=hugo", "", nil)
	if code != http.StatusAccepted {
		t.Fatalf("Hugo may switch to ai (in the allowed set) — expected 202, got %d: %s", code, body)
	}
}

// TestModeSwitchProfileViaHeaderAndBody: the profile reference rides the
// X-Scarlix-Profile header and the JSON body field too — every entry path
// to the action boundary is policy-decided.
func TestModeSwitchProfileViaHeaderAndBody(t *testing.T) {
	mux := newEnforcementMux(t, false)
	code, body := enfRequest(t, mux, http.MethodPost, "/api/mode?set=game", "", map[string]string{"X-Scarlix-Profile": "xox"})
	if code != http.StatusForbidden || !strings.Contains(body, "can_switch=false") {
		t.Fatalf("the header-carried profile must enforce identically, got %d: %s", code, body)
	}
	code, body = enfRequest(t, mux, http.MethodPost, "/api/mode", `{"mode":"game","profile":"xox"}`, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "can_switch=false") {
		t.Fatalf("the body-carried profile must enforce identically, got %d: %s", code, body)
	}
}

// TestChatProfilePolicyEnforced: the chat boundary is policy-decided — a
// profile without ai_chat is 403; XOX's content filter blocks a blocklisted
// category 403 server-side; clean input under the same profile chats; a
// broken profile reference fails closed 400.
func TestChatProfilePolicyEnforced(t *testing.T) {
	mux := newEnforcementMux(t, true)

	// gamer carries no ai_chat permission — 403, named.
	code, body := enfRequest(t, mux, http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}],"profile":"gamer"}`, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "ai_chat") {
		t.Fatalf("a profile without ai_chat must refuse the chat 403 by name, got %d: %s", code, body)
	}

	// XOX's content filter is enforced ON THE INPUT — 403, the category named.
	code, body = enfRequest(t, mux, http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"tell me a story full of violence"}],"profile":"xox"}`, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "violence") {
		t.Fatalf("XOX's content filter must block the input 403 with the category named, got %d: %s", code, body)
	}

	// The header entry path enforces identically.
	code, body = enfRequest(t, mux, http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"gambling tips?"}]}`,
		map[string]string{"X-Scarlix-Profile": "xox"})
	if code != http.StatusForbidden || !strings.Contains(body, "gambling") {
		t.Fatalf("the header-carried profile must enforce the content filter identically, got %d: %s", code, body)
	}

	// A broken profile reference fails CLOSED — 400, never a bypass.
	code, _ = enfRequest(t, mux, http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}],"profile":"broken"}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("an unloadable profile must fail the chat closed 400, got %d", code)
	}

	// The counterweight: clean input under XOX's policy chats normally.
	code, body = enfRequest(t, mux, http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"rozprávka o drakovi a princezne"}],"profile":"xox"}`, nil)
	if code != http.StatusOK || !strings.Contains(body, "answer from") {
		t.Fatalf("clean input under XOX's policy must chat normally (200 with an answer), got %d: %s", code, body)
	}

	// No profile reference: the household token alone — unchanged behavior.
	code, body = enfRequest(t, mux, http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}]}`, nil)
	if code != http.StatusOK || !strings.Contains(body, "answer from") {
		t.Fatalf("a token-only chat (no profile reference) keeps the pre-v19.6.6 behavior, got %d: %s", code, body)
	}
}
