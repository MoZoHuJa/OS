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

// The v19.6.7 identity-bound profile authorization proofs
// (KPK-19.6.6-P1-01) — the audit's acceptance matrix, executed against
// the LIVE handler stack with a per-profile token index (multi-user
// mode):
//
//   - a family token cannot select the zmor admin profile (body field,
//     query field, or X-Scarlix-Profile header — no entry path overrides
//     the authenticated principal);
//   - a family token cannot select ANOTHER family profile;
//   - the zmor token selects zmor and is allowed; the household token
//     (the operator) may act-as a family profile — an act-as that can
//     only REDUCE privileges (xox's can_switch=false still refuses);
//   - an OMITTED profile reference is DERIVED from the principal — the
//     family policy applies (content filter, mode policy, budget) with
//     no policy-free path;
//   - a broken principal profile fails CLOSED (403), never unrestricted;
//   - operator surfaces (probe, model registration) refuse family
//     tokens (403) and serve the admin principal;
//   - the enforced monthly budget refuses a request that cannot fit the
//     remaining quota with a stable 429 carrying the quota state.
const (
	pZmorYAML = `name: zmor
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
	pMonYAML = `name: mon
display_name: "Mon (manzelka)"
role: family
permissions: [read_files, read_photos, write_files, voice_assistant, ai_chat]
scarlix_mode:
  default: ai
  allowed: [ai, game]
  can_switch: true
hud_theme: elegant
token_budget:
  monthly: 50000
  model: qwen2.5:3b
`
	// pTinyXoxYAML carries a deliberately tiny budget — the 429 proof.
	pTinyXoxYAML = `name: xox
display_name: "XOX (tiny budget)"
role: family
permissions: [kids_content, ai_chat]
scarlix_mode:
  default: ai
  allowed: [ai]
  can_switch: false
hud_theme: creative
token_budget:
  monthly: 10
  model: qwen2.5:3b
`
	// pBrokenHugoYAML fails the typed schema — the derived-profile
	// fail-closed proof (the principal's own policy is unloadable).
	pBrokenHugoYAML = `name: hugo
display_name: "Hugo (broken)"
role: family
permissions: [ai_chat]
scarlix_mode:
  default: ai
  allowed: [ai]
  can_switch: false
hud_theme: gaming
token_budget:
  - 100
`
)

// principalMux builds the multi-user stack: the four repository-shaped
// profiles, a per-profile token index (hugo/mon/xox/zmor), the household
// (admin) token, the AI runtime with the memory ledger — the full
// identity-bound decision path.
func principalMux(t *testing.T, withRuntime bool) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"hugo":   enfHugoYAML,
		"mon":    pMonYAML,
		"xox":    enfXoxYAML,
		"zmor":   pZmorYAML,
		"gamer":  enfGamerYAML,
		"broken": enfBrokenYAML,
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
	}
	mode := scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	h := NewHandler(guard.New(), mode, profiles.New(dir), "secret", "19.6.7")
	index, err := profiles.NewTokenIndex(map[string]string{
		"hugo":  "hugo-tok",
		"mon":   "mon-tok",
		"xox":   "xox-tok",
		"zmor":  "zmor-tok",
		"gamer": "gamer-tok",
	}, "secret")
	if err != nil {
		t.Fatalf("token index: %v", err)
	}
	if err := h.ConfigureProfileAuthz(index); err != nil {
		t.Fatalf("profile authz: %v", err)
	}
	if withRuntime {
		if err := h.ConfigureAIRuntime(aiStack(t, http.StatusOK, http.StatusOK)); err != nil {
			t.Fatal(err)
		}
		ledger, err := profiles.NewBudgetLedger("") // memory-only — the accounting proof
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

// authedRequest mirrors enfRequest with the EXPLICIT bearer token — the
// identity under test.
func authedRequest(t *testing.T, mux *http.ServeMux, token, method, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestFamilyTokenCannotSelectAdminProfile: the P1 core — hugo's and xox's
// tokens are refused 403 when they declare the zmor admin profile, via the
// body field AND the X-Scarlix-Profile header (no entry path overrides
// the authenticated principal). The zmor token selecting zmor is allowed.
func TestFamilyTokenCannotSelectAdminProfile(t *testing.T) {
	mux := principalMux(t, true)

	// Hugo token + profile=zmor (body) — 403, the mismatch named.
	code, body := authedRequest(t, mux, "hugo-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}],"profile":"zmor"}`, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "cannot select profile") {
		t.Fatalf("hugo's token + profile=zmor must refuse 403 naming the mismatch, got %d: %s", code, body)
	}

	// XOX token + profile=zmor via the HEADER — 403 identically.
	code, body = authedRequest(t, mux, "xox-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}]}`,
		map[string]string{"X-Scarlix-Profile": "zmor"})
	if code != http.StatusForbidden || !strings.Contains(body, "cannot select profile") {
		t.Fatalf("xox's token + header profile=zmor must refuse 403 (the header cannot override the principal), got %d: %s", code, body)
	}

	// The zmor token selecting zmor chats (its own policy: ai_chat via 'all').
	code, body = authedRequest(t, mux, "zmor-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}],"profile":"zmor"}`, nil)
	if code != http.StatusOK || !strings.Contains(body, "answer from") {
		t.Fatalf("the zmor token + profile=zmor must chat (its own policy), got %d: %s", code, body)
	}
}

// TestFamilyTokenCannotSelectAnotherFamilyProfile: hugo's token cannot
// act as mon or xox — family identities are mutually isolated behind the
// shared household surface.
func TestFamilyTokenCannotSelectAnotherFamilyProfile(t *testing.T) {
	mux := principalMux(t, true)
	code, body := authedRequest(t, mux, "hugo-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}],"profile":"mon"}`, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "cannot select profile") {
		t.Fatalf("hugo's token + profile=mon must refuse 403, got %d: %s", code, body)
	}
	code, _ = authedRequest(t, mux, "hugo-tok", http.MethodPost, "/api/mode?set=ai&profile=xox", "", nil)
	if code != http.StatusForbidden {
		t.Fatalf("hugo's token + profile=xox on the mode boundary must refuse 403, got %d", code)
	}
}

// TestOmittedProfileIsDerivedFromPrincipal: the no-reference path is
// DERIVED, never unrestricted — xox's token with NO profile reference
// still gets HER content filter (blocked category 403) and HER mode
// policy (can_switch=false 403); clean input chats through her policy.
func TestOmittedProfileIsDerivedFromPrincipal(t *testing.T) {
	mux := principalMux(t, true)

	// XOX token, NO profile reference, blocked category → 403 (her filter).
	code, body := authedRequest(t, mux, "xox-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"a story full of violence"}]}`, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "violence") {
		t.Fatalf("xox's token with NO profile reference must still get HER content filter (derived), got %d: %s", code, body)
	}

	// XOX token, NO profile reference, mode switch → 403 (can_switch=false).
	code, body = authedRequest(t, mux, "xox-tok", http.MethodPost, "/api/mode?set=game", "", nil)
	if code != http.StatusForbidden || !strings.Contains(body, "can_switch=false") {
		t.Fatalf("xox's token with NO profile reference must still get HER mode policy (derived), got %d: %s", code, body)
	}

	// The counterweight: clean input under her DERIVED policy chats.
	code, body = authedRequest(t, mux, "xox-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"rozprávka o drakovi"}]}`, nil)
	if code != http.StatusOK || !strings.Contains(body, "answer from") {
		t.Fatalf("clean input under xox's DERIVED policy must chat (200), got %d: %s", code, body)
	}

	// Hugo token, NO reference — his own derived policy (ai_chat, game default).
	code, body = authedRequest(t, mux, "hugo-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}]}`, nil)
	if code != http.StatusOK {
		t.Fatalf("hugo's token with no reference must chat through HIS derived policy, got %d: %s", code, body)
	}

	// gamer has NO ai_chat permission — the derived refusal (403, named).
	code, body = authedRequest(t, mux, "gamer-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}]}`, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "ai_chat") {
		t.Fatalf("gamer's token with no reference must be refused by HIS derived policy (no ai_chat), got %d: %s", code, body)
	}
}

// TestHouseholdTokenActAsOnlyReduces: the household (admin) token may
// act-as xox — but that is a privilege REDUCTION: xox's can_switch=false
// still refuses the mode switch.
func TestHouseholdTokenActAsOnlyReduces(t *testing.T) {
	mux := principalMux(t, false)
	code, body := authedRequest(t, mux, "secret", http.MethodPost, "/api/mode?set=game&profile=xox", "", nil)
	if code != http.StatusForbidden || !strings.Contains(body, "can_switch=false") {
		t.Fatalf("the household token act-as xox must still honor xox's policy (reduction only), got %d: %s", code, body)
	}
}

// TestAdminPrincipalDerivesFullModeSet: the zmor token with NO profile
// reference derives the role:admin profile — whose allowed set carries
// the FULL mode enumeration (stop included; the fixture states it) — and
// the switch is accepted. The derived admin path is DECIDABLE, not
// unrestricted-but-invisible.
func TestAdminPrincipalDerivesFullModeSet(t *testing.T) {
	mux := principalMux(t, false)
	code, body := authedRequest(t, mux, "zmor-tok", http.MethodPost, "/api/mode?set=stop", "", nil)
	if code != http.StatusAccepted {
		t.Fatalf("the zmor token's derived admin policy must allow the stop switch (202), got %d: %s", code, body)
	}
}

// TestBrokenPrincipalProfileFailsClosed: a principal whose OWN profile
// file is broken is refused 403 on the policy boundary — the action is
// not decided without a policy (never unrestricted).
func TestBrokenPrincipalProfileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hugo.yaml"), []byte(pBrokenHugoYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zmor.yaml"), []byte(pZmorYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	h := NewHandler(guard.New(), mode, profiles.New(dir), "secret", "19.6.7")
	index, err := profiles.NewTokenIndex(map[string]string{"hugo": "hugo-tok"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureProfileAuthz(index); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureAIRuntime(aiStack(t, http.StatusOK, http.StatusOK)); err != nil {
		t.Fatal(err)
	}
	ledger, err := profiles.NewBudgetLedger("")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureBudgetLedger(ledger); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Hugo's derived profile is unloadable → 403, the unavailability named.
	code, body := authedRequest(t, mux, "hugo-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}]}`, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "unavailable") {
		t.Fatalf("a principal with a broken profile must fail CLOSED 403 (never unrestricted), got %d: %s", code, body)
	}
}

// TestOperatorSurfacesRefuseFamilyTokens: the probe round and model
// registration are operator acts — a family token is refused 403; the
// household token is served.
func TestOperatorSurfacesRefuseFamilyTokens(t *testing.T) {
	mux := principalMux(t, true)

	code, body := authedRequest(t, mux, "xox-tok", http.MethodPost, "/api/ai/probe", "", nil)
	if code != http.StatusForbidden || !strings.Contains(body, "admin") {
		t.Fatalf("the probe round must refuse a family token 403, got %d: %s", code, body)
	}
	code, body = authedRequest(t, mux, "hugo-tok", http.MethodPost, "/api/ai/models",
		`{"provider":"ollama","model":"qwen3:4b"}`, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "admin") {
		t.Fatalf("model registration must refuse a family token 403, got %d: %s", code, body)
	}
	// The household token is served on the probe (200 — the runtime probes).
	code, _ = authedRequest(t, mux, "secret", http.MethodPost, "/api/ai/probe", "", nil)
	if code != http.StatusOK {
		t.Fatalf("the household (admin) token must be served on the probe, got %d", code)
	}
	// The read-only family surface stays open to the family token.
	code, _ = authedRequest(t, mux, "xox-tok", http.MethodGet, "/api/ai/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("the read-only AI health view must serve the family token, got %d", code)
	}
}

// TestBudgetQuotaExceededIsStable429: a profile whose monthly budget
// cannot cover the request estimate is refused with a STABLE 429 whose
// body carries the quota state (monthly/used/reserved) — the enforced
// budget, not descriptive metadata.
func TestBudgetQuotaExceededIsStable429(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "xox.yaml"), []byte(pTinyXoxYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zmor.yaml"), []byte(pZmorYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	h := NewHandler(guard.New(), mode, profiles.New(dir), "secret", "19.6.7")
	index, err := profiles.NewTokenIndex(map[string]string{"xox": "xox-tok"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureProfileAuthz(index); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureAIRuntime(aiStack(t, http.StatusOK, http.StatusOK)); err != nil {
		t.Fatal(err)
	}
	ledger, err := profiles.NewBudgetLedger("")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureBudgetLedger(ledger); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// The estimate (input + the output reserve) exceeds monthly=10 → 429
	// with the quota state in the body.
	code, body := authedRequest(t, mux, "xox-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}]}`, nil)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "quota") || !strings.Contains(body, `"monthly":10`) {
		t.Fatalf("an exhausted monthly budget must refuse with a stable 429 carrying the quota state, got %d: %s", code, body)
	}
}

// TestBudgetSettlesProviderReportedUsage: a successful chat under a
// budgeted profile settles the PROVIDER-REPORTED usage (not the
// estimate) into the ledger — the accounting proof.
func TestBudgetSettlesProviderReportedUsage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hugo.yaml"), []byte(enfHugoYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zmor.yaml"), []byte(pZmorYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	h := NewHandler(guard.New(), mode, profiles.New(dir), "secret", "19.6.7")
	index, err := profiles.NewTokenIndex(map[string]string{"hugo": "hugo-tok"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureProfileAuthz(index); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureAIRuntime(aiStack(t, http.StatusOK, http.StatusOK)); err != nil {
		t.Fatal(err)
	}
	ledger, err := profiles.NewBudgetLedger("")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureBudgetLedger(ledger); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	code, body := authedRequest(t, mux, "hugo-tok", http.MethodPost, "/api/ai/chat",
		`{"model":"qwen3-14b","messages":[{"role":"user","content":"hello"}]}`, nil)
	if code != http.StatusOK || !strings.Contains(body, "answer from") {
		t.Fatalf("hugo's budgeted chat must succeed, got %d: %s", code, body)
	}
	// sglang reports usage 3+2=5 tokens — the settled amount.
	state := ledger.State("hugo", 100000, nowUTC())
	if state.Used != 5 {
		t.Fatalf("the ledger must carry the provider-reported usage (3+2=5), got used=%d reserved=%d", state.Used, state.Reserved)
	}
	if state.Reserved != 0 {
		t.Fatalf("a settled reservation must release its hold, reserved=%d", state.Reserved)
	}
}

// TestSingleUserDerivedAdminPolicy: without a token index (single-user),
// the household token's omitted reference derives the role:admin profile
// when one exists — the DECIDABLE admin path with the full mode set.
func TestSingleUserDerivedAdminPolicy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zmor.yaml"), []byte(pZmorYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	h := NewHandler(guard.New(), mode, profiles.New(dir), "secret", "19.6.7")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	code, body := authedRequest(t, mux, "secret", http.MethodPost, "/api/mode?set=stop", "", nil)
	if code != http.StatusAccepted {
		t.Fatalf("single-user: the derived admin policy must allow the stop switch (the full mode set), got %d: %s", code, body)
	}
}

// TestSingleUserNoProfilesKeepsLegacyPath: a deployment with NO profile
// files at all keeps the legacy single-user behavior on the policy
// boundary — the explicit compatibility contract (named, never mistaken
// for household isolation).
func TestSingleUserNoProfilesKeepsLegacyPath(t *testing.T) {
	dir := t.TempDir()
	mode := scarlix_mode.NewForTest(filepath.Join(dir, "desired-mode"))
	h := NewHandler(guard.New(), mode, profiles.New(dir), "secret", "19.6.7")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	code, _ := authedRequest(t, mux, "secret", http.MethodPost, "/api/mode?set=stop", "", nil)
	if code != http.StatusAccepted {
		t.Fatalf("single-user with NO profiles keeps the legacy mode-switch path, got %d", code)
	}
}

// TestInvalidFamilyTokenIs401: an unknown token authenticates nobody —
// the 401 semantics are preserved for every surface.
func TestInvalidFamilyTokenIs401(t *testing.T) {
	mux := principalMux(t, false)
	code, _ := authedRequest(t, mux, "intruder-tok", http.MethodGet, "/api/health", "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("an unknown token must be 401, got %d", code)
	}
}
