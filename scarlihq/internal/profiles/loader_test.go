package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"strings"

	"gopkg.in/yaml.v3"
)

// The v19.6.5 audit's exact fixtures — the four repository profiles as
// embedded copies (deterministic, no filesystem dependency), plus a walk
// of the REAL repository profiles when the checkout is present.

const hugoYAML = `name: hugo
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

const monYAML = `name: mon
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

const xoxYAML = `name: xox
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

const zmorYAML = `name: zmor
display_name: "Zmor (admin)"
role: admin
permissions: [all]
scarlix_mode:
  default: ai
  allowed: [ai, game, turbo, offline]
  can_switch: true
hud_theme: iron-man
token_budget: unlimited
`

// TestRepositoryProfilesParseWithoutFallback is THE v19.6.5 regression
// proof (KPK-19.6.5-P1-03): all four repository profiles must parse and
// validate against the typed schema — the v19.6.5 loader's STRING
// token_budget failed on the three MAPPING-shaped budgets (hugo, mon,
// xox) and silently returned filename-only fallback profiles, discarding
// every policy field.
func TestRepositoryProfilesParseWithoutFallback(t *testing.T) {
	fixtures := map[string]string{
		"hugo": hugoYAML,
		"mon":  monYAML,
		"xox":  xoxYAML,
		"zmor": zmorYAML,
	}
	dir := t.TempDir()
	for name, yaml := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
	}
	m := New(dir)

	hugo, err := m.Load("hugo")
	if err != nil {
		t.Fatalf("hugo must parse under the typed schema (the v19.6.5 silent-fallback defect): %v", err)
	}
	if hugo.DisplayName != "Hugo (syn)" || hugo.Role != "family" || hugo.HudTheme != "gaming" {
		t.Fatalf("hugo's DISPLAY identity must survive loading, got %+v", hugo)
	}
	if hugo.TokenBudget.Unlimited || hugo.TokenBudget.Monthly != 100000 || hugo.TokenBudget.Model != "qwen2.5:3b" {
		t.Fatalf("hugo's MAPPING-shaped token_budget must load typed, got %+v", hugo.TokenBudget)
	}
	if len(hugo.Permissions) != 5 || hugo.Permissions[0] != "read_files" {
		t.Fatalf("hugo's permissions must load, got %v", hugo.Permissions)
	}
	if hugo.Mode.Default != "game" || len(hugo.Mode.Allowed) != 2 || !hugo.Mode.CanSwitch {
		t.Fatalf("hugo's mode policy must load, got %+v", hugo.Mode)
	}

	zmor, err := m.Load("zmor")
	if err != nil {
		t.Fatalf("zmor must parse (the SCALAR unlimited budget): %v", err)
	}
	if !zmor.TokenBudget.Unlimited || zmor.TokenBudget.Monthly != 0 {
		t.Fatalf("zmor's SCALAR token_budget must load as unlimited, got %+v", zmor.TokenBudget)
	}
	if !zmor.HasPermission("anything_at_all") {
		t.Fatal("the admin's \"all\" wildcard carries every permission")
	}

	xox, err := m.Load("xox")
	if err != nil {
		t.Fatalf("xox must parse (the audit's exact failing shape — mapping budget + content filter): %v", err)
	}
	if xox.ContentFilter == nil || !xox.ContentFilter.Enabled || len(xox.ContentFilter.Blocklist) != 4 {
		t.Fatalf("xox's content filter must load, got %+v", xox.ContentFilter)
	}

	// The audit's acceptance: /api/profiles returns the exact configured
	// identity — verified through List() (what the handler serializes).
	profs, issues := m.List()
	if len(issues) != 0 {
		t.Fatalf("all four repository profiles are valid — no issues, got %v", issues)
	}
	if len(profs) != 4 {
		t.Fatalf("all four repository profiles must be listed, got %d", len(profs))
	}
	byName := map[string]Profile{}
	for _, p := range profs {
		byName[p.Name] = p
	}
	if p := byName["mon"]; p.DisplayName != "Mon (manzelka)" || p.Role != "family" || p.HudTheme != "elegant" {
		t.Fatalf("mon's exact configured identity must be returned, got %+v", p)
	}
}

// TestRepositoryProfilesOnDisk walks the REAL repository profiles/ directory
// when the checkout is present (the audit's acceptance: schema validation
// and tests for all four SUPPLIED profiles — not just embedded copies).
func TestRepositoryProfilesOnDisk(t *testing.T) {
	repoProfiles := filepath.Join("..", "..", "..", "profiles")
	entries, err := os.ReadDir(repoProfiles)
	if err != nil {
		t.Skipf("repository profiles/ not present at %s (embedded fixtures already cover the schema)", repoProfiles)
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		count++
		name := e.Name()[:len(e.Name())-len(".yaml")]
		if _, err := New(repoProfiles).Load(name); err != nil {
			t.Fatalf("repository profile %s must parse and validate against the v19.6.6 typed schema: %v", e.Name(), err)
		}
	}
	if count != 4 {
		t.Fatalf("the repository carries exactly four family profiles, found %d", count)
	}
}

// TestNoSilentFallback: the v19.6.5 defect CLASS — a policy-bearing file
// that fails to parse or validate must return a NAMED ERROR, never a
// filename-only fallback profile that looks permissively fine. Get/Load
// return the error; List() EXCLUDES the file and reports it as an Issue.
func TestNoSilentFallback(t *testing.T) {
	dir := t.TempDir()
	// The v19.6.5 exact failure shape: token_budget as a LIST (neither the
	// scalar nor the mapping) — a type error the old loader swallowed.
	broken := "name: broken\ndisplay_name: Broken\nrole: family\npermissions: [ai_chat]\nscarlix_mode:\n  default: ai\n  allowed: [ai]\n  can_switch: false\nhud_theme: default\ntoken_budget:\n  - 100\n"
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	if _, err := m.Load("broken"); err == nil {
		t.Fatal("a policy-bearing file with a malformed token_budget must be a NAMED ERROR, never a fallback profile (KPK-19.6.5-P1-03)")
	}
	profs, issues := m.List()
	if len(profs) != 0 {
		t.Fatalf("an invalid profile must be EXCLUDED from the list, got %v", profs)
	}
	if len(issues) != 1 || issues[0].File != "broken.yaml" {
		t.Fatalf("the invalid file must be reported as an Issue, got %v", issues)
	}
	if issues[0].Error == "" {
		t.Fatal("the Issue must carry the error text — explicit, surfacing, not swallowed")
	}
}

// TestTokenBudgetDualShape: the dual-shape contract — "unlimited" scalar,
// {monthly, model} mapping, and the honest refusals for every other shape.
func TestTokenBudgetDualShape(t *testing.T) {
	var b TokenBudget
	if err := yamlUnmarshalString("unlimited", &b); err != nil || !b.Unlimited {
		t.Fatalf("the scalar \"unlimited\" must load, got (%+v, %v)", b, err)
	}
	if err := yamlUnmarshalString("monthly: 500\nmodel: qwen2.5:3b", &b); err != nil || b.Unlimited || b.Monthly != 500 || b.Model != "qwen2.5:3b" {
		t.Fatalf("the mapping shape must load typed, got (%+v, %v)", b, err)
	}
	for _, bad := range []string{
		"- 100",                          // a list
		"monthly: -5\nmodel: qwen2.5:3b", // a negative budget
		"monthly: 500",                   // a mapping without a model
		"model: qwen2.5:3b",              // a mapping without a monthly
		"'500'",                          // a numeric scalar
		"'un limited'",                   // internal whitespace garbage
	} {
		if err := yamlUnmarshalString(bad, &b); err == nil {
			t.Fatalf("token_budget %q must be REFUSED by name (an explicit error, never a zero-value guess)", bad)
		}
	}
}

// TestValidationNegativeCases: the schema's own discipline — every
// incoherent policy shape is rejected with a named error.
func TestValidationNegativeCases(t *testing.T) {
	base := func(mutate func(*Profile)) Profile {
		p := Profile{
			Name:        "test",
			DisplayName: "Test",
			Role:        "family",
			Permissions: []string{"ai_chat"},
			Mode:        ModePolicy{Default: "ai", Allowed: []string{"ai"}, CanSwitch: false},
			HudTheme:    "default",
			TokenBudget: TokenBudget{Monthly: 100, Model: "qwen2.5:3b"},
		}
		mutate(&p)
		return p
	}
	cases := []struct {
		name string
		p    Profile
	}{
		{"unknown role", base(func(p *Profile) { p.Role = "guest" })},
		{"all wildcard on family", base(func(p *Profile) { p.Permissions = []string{"all"} })},
		{"bad permission shape", base(func(p *Profile) { p.Permissions = []string{"AI Chat!"} })},
		{"duplicate permissions", base(func(p *Profile) { p.Permissions = []string{"ai_chat", "ai_chat"} })},
		{"no permissions", base(func(p *Profile) { p.Permissions = nil })},
		{"default outside allowed", base(func(p *Profile) { p.Mode = ModePolicy{Default: "game", Allowed: []string{"ai"}, CanSwitch: true} })},
		{"can_switch false with multiple modes", base(func(p *Profile) {
			p.Mode = ModePolicy{Default: "ai", Allowed: []string{"ai", "game"}, CanSwitch: false}
		})},
		{"empty allowed", base(func(p *Profile) { p.Mode = ModePolicy{Default: "ai", Allowed: nil, CanSwitch: true} })},
		{"content filter enabled without blocklist", base(func(p *Profile) { p.ContentFilter = &ContentFilter{Enabled: true} })},
		{"budget neither unlimited nor typed", base(func(p *Profile) { p.TokenBudget = TokenBudget{} })},
		{"bad name shape", base(func(p *Profile) { p.Name = "Test User" })},
		{"missing display name", base(func(p *Profile) { p.DisplayName = "" })},
	}
	for _, tc := range cases {
		if err := tc.p.Validate(); err == nil {
			t.Fatalf("%s must be rejected by the schema validation", tc.name)
		}
	}
	// The counterweight: the base profile itself is valid.
	coherent := base(func(*Profile) {})
	if err := coherent.Validate(); err != nil {
		t.Fatalf("the coherent base profile must validate, got %v", err)
	}
}

// TestAllowsMode: the mode-switch policy decisions — XOX (can_switch=false)
// refuses EVERY switch; Hugo switches within his allowed set and is refused
// by name outside it.
func TestAllowsMode(t *testing.T) {
	xox := Profile{Mode: ModePolicy{Default: "ai", Allowed: []string{"ai"}, CanSwitch: false}}
	if err := xox.AllowsMode("ai"); !errors.Is(err, ErrModeSwitchForbidden) {
		t.Fatalf("XOX cannot switch AT ALL (can_switch=false), got %v", err)
	}
	hugo := Profile{Name: "hugo", Mode: ModePolicy{Default: "game", Allowed: []string{"game", "ai"}, CanSwitch: true}}
	if err := hugo.AllowsMode("ai"); err != nil {
		t.Fatalf("Hugo may switch to ai (in the allowed set), got %v", err)
	}
	if err := hugo.AllowsMode("turbo"); !errors.Is(err, ErrModeSwitchForbidden) {
		t.Fatalf("Hugo may NOT switch to turbo (outside the allowed set) — a named refusal, got %v", err)
	}
}

// TestHasPermission: the permission decisions — the admin wildcard, the
// declared family words, and the honest refusal for everything else.
func TestHasPermission(t *testing.T) {
	admin := Profile{Role: "admin", Permissions: []string{"all"}}
	for _, perm := range []string{"ai_chat", "write_files", "read_files", "anything"} {
		if !admin.HasPermission(perm) {
			t.Fatalf("the admin's \"all\" wildcard carries %q", perm)
		}
	}
	mon := Profile{Role: "family", Permissions: []string{"read_files", "read_photos", "write_files", "voice_assistant", "ai_chat"}}
	if !mon.HasPermission("write_files") {
		t.Fatal("mon carries write_files (declared)")
	}
	if mon.HasPermission("play_games") {
		t.Fatal("mon does NOT carry play_games (undeclared — the declared set is the whole truth)")
	}
	if err := mon.RequirePermission("play_games"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("the refusal must be the named error, got %v", err)
	}
}

// TestContentBlocked: the server-side content filter — a blocked category
// hit refuses with the category named; innocent text passes; a disabled or
// absent filter is a no-op.
func TestContentBlocked(t *testing.T) {
	xox := Profile{Name: "xox", ContentFilter: &ContentFilter{Enabled: true, Blocklist: []string{"violence", "adult", "drugs", "gambling"}}}
	if category, blocked := xox.ContentBlocked("tell me a story with violence in it"); !blocked || category != "violence" {
		t.Fatalf("a blocklisted category hit must refuse WITH the category named, got (%q, %v)", category, blocked)
	}
	if category, blocked := xox.ContentBlocked("Mama, VIOLENCE je zlé?"); !blocked || category != "violence" {
		t.Fatalf("the blocklist is case-insensitive, got (%q, %v)", category, blocked)
	}
	if _, blocked := xox.ContentBlocked("rozprávka o drakovi a princezne"); blocked {
		t.Fatal("innocent text passes the filter")
	}
	off := Profile{ContentFilter: &ContentFilter{Enabled: false, Blocklist: []string{"violence"}}}
	if _, blocked := off.ContentBlocked("violence everywhere"); blocked {
		t.Fatal("a DISABLED filter is a no-op")
	}
	none := Profile{}
	if _, blocked := none.ContentBlocked("violence"); blocked {
		t.Fatal("an ABSENT filter is a no-op")
	}
}

// --- helpers ---

func yamlUnmarshalString(s string, out interface{}) error {
	dec := yaml.NewDecoder(strings.NewReader(s))
	dec.KnownFields(true)
	return dec.Decode(out)
}
