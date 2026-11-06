package profiles

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// The typed, policy-bearing profile schema (v19.6.6, KPK-19.6.5-P1-03).
//
// The v19.6.5 defect: the Profile struct modeled five display fields and a
// STRING token_budget — but the repository's four family profiles declare
// token_budget as a MAPPING (monthly + model) in three of four files, so
// yaml.Unmarshal FAILED for hugo/mon/xox and the loader SILENTLY fell back
// to a filename-only profile (DisplayName=name, HudTheme=default), discarding
// the display name, role, theme and every policy field; the permissions,
// scarlix_mode policy and XOX's content filter were not represented in the
// Go schema AT ALL and were enforced NOWHERE. A policy-bearing file that
// fails to parse must NEVER become a successful permissive-looking profile.
//
// The v19.6.6 schema models the FULL declared policy: permissions, mode
// policy, the dual-shape token budget, and the content filter. Loading is
// STRICT: a parse or validation failure returns a named error (Issue), the
// profile is excluded from List() and reported in the issues slice — never
// silently converted into a fallback. Enforcement helpers (AllowsMode,
// HasPermission, ContentBlocked) make the declared policy DECIDABLE at the
// action boundary (REST /api/mode/switch + /api/ai/chat + MCP — see
// internal/api/rest.go), server-side, not a dashboard hiding controls.
// ---------------------------------------------------------------------------

// Profile represents a user profile (parsed from /etc/scarlix/profiles/*.yaml)
// — the FULL policy-bearing schema, not just the display fields.
type Profile struct {
	Name          string         `json:"name" yaml:"name"`
	DisplayName   string         `json:"display_name" yaml:"display_name"`
	Role          string         `json:"role" yaml:"role"`
	Permissions   []string       `json:"permissions" yaml:"permissions"`
	Mode          ModePolicy     `json:"scarlix_mode" yaml:"scarlix_mode"`
	HudTheme      string         `json:"hud_theme" yaml:"hud_theme"`
	TokenBudget   TokenBudget    `json:"token_budget" yaml:"token_budget"`
	ContentFilter *ContentFilter `json:"content_filter,omitempty" yaml:"content_filter"`
}

// ModePolicy is the scarlix_mode section: the default mode, the closed set
// of allowed modes, and whether the profile may request a switch at all.
type ModePolicy struct {
	Default   string   `json:"default" yaml:"default"`
	Allowed   []string `json:"allowed" yaml:"allowed"`
	CanSwitch bool     `json:"can_switch" yaml:"can_switch"`
}

// TokenBudget is the dual-shape budget field. The repository's profiles
// declare BOTH honest shapes and both are valid:
//
//	token_budget: unlimited                (zmor — the admin)
//	token_budget: {monthly: 100000, model: qwen2.5:3b}   (family members)
//
// The custom unmarshaler accepts exactly these two shapes and rejects
// everything else BY NAME (a number, a list, a negative monthly — an
// explicit error, never a zero-value guess).
type TokenBudget struct {
	Unlimited bool   `json:"unlimited,omitempty"`
	Monthly   int64  `json:"monthly,omitempty"`
	Model     string `json:"model,omitempty"`
}

// UnmarshalYAML implements the dual-shape contract above.
func (b *TokenBudget) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return fmt.Errorf("profiles: token_budget scalar must be the string \"unlimited\", got %q", node.Value)
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "unlimited":
			*b = TokenBudget{Unlimited: true}
			return nil
		case "", "null":
			return fmt.Errorf("profiles: token_budget is empty — declare \"unlimited\" or {monthly: N, model: name}")
		}
		return fmt.Errorf("profiles: token_budget scalar must be \"unlimited\" (got %q); a numeric budget is declared as {monthly: N, model: name}", s)
	case yaml.MappingNode:
		var raw struct {
			Monthly *int64 `yaml:"monthly"`
			Model   string `yaml:"model"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("profiles: token_budget mapping must be {monthly: N, model: name}: %v", err)
		}
		if raw.Monthly == nil {
			return fmt.Errorf("profiles: token_budget mapping requires a monthly field ({monthly: N, model: name})")
		}
		if *raw.Monthly <= 0 {
			return fmt.Errorf("profiles: token_budget.monthly must be a positive number, got %d", *raw.Monthly)
		}
		model := strings.TrimSpace(raw.Model)
		if model == "" {
			return fmt.Errorf("profiles: token_budget.mapping requires a model field ({monthly: N, model: name}) — the budget is per-model")
		}
		if !validModelRef(model) {
			return fmt.Errorf("profiles: token_budget.model %q is not a model reference shape (registry id like qwen2.5:3b)", model)
		}
		*b = TokenBudget{Monthly: *raw.Monthly, Model: model}
		return nil
	}
	return fmt.Errorf("profiles: token_budget must be \"unlimited\" or {monthly: N, model: name} (got a %s node)", node.Tag)
}

// ContentFilter is the child-safe policy section (XOX). Enabled requires a
// non-empty blocklist; the blocklist is enforced SERVER-SIDE on the chat
// input (ContentBlocked) — keyword-level, honestly documented as such
// (semantic classification is a CF-4+ item with the local model, never a
// silent claim).
type ContentFilter struct {
	Enabled   bool     `json:"enabled" yaml:"enabled"`
	Blocklist []string `json:"blocklist" yaml:"blocklist"`
}

// Issue is one invalid profile file — surfaced to the API caller, never
// swallowed (the audit's requirement: an explicit error, not a silent
// permissive fallback).
type Issue struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// validProfileName is the closed shape of a profile name (and its file
// name): lowercase letters, digits, dashes/underscores — the set the
// filename-derived fallback used to accept, now validated, not assumed.
func validProfileName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// validPermissionName is the closed shape of a declared permission:
// lowercase snake_case words (read_files, ai_chat, kids_content, all).
func validPermissionName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// validModeName is the closed shape of a declared scarlix mode (the
// runtime's own closed enumeration: ai, stop, game, creative, turbo,
// offline, tv — enforced shape-wise here; membership in the runtime's
// enumeration is the mode package's own concern at switch time).
func validModeName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// validModelRef is the closed shape of a model reference: an Ollama-style
// tag (qwen2.5:3b) or a HuggingFace-ish id segment (Qwen3-14B-AWQ).
func validModelRef(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == ':', r == '-', r == '_', r == '/':
		default:
			return false
		}
	}
	return true
}

// Validate checks the WHOLE policy-bearing schema (the audit's negative
// tests made deterministic): closed role enumeration, permission shapes
// and the all-wildcard rule, mode policy coherence, budget coherence,
// content-filter coherence, and the display basics. A policy file that
// fails validation is an Issue — never a silently reshaped profile.
func (p *Profile) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("profiles: name is required (the file's profile identity)")
	}
	if !validProfileName(p.Name) {
		return fmt.Errorf("profiles: name %q is not the closed shape (lowercase letters, digits, - and _)", p.Name)
	}
	switch p.Role {
	case "admin", "family":
	default:
		return fmt.Errorf("profiles: role %q is not in the closed enumeration (admin, family)", p.Role)
	}
	if p.DisplayName == "" {
		return fmt.Errorf("profiles: display_name is required for %s (the dashboard's label)", p.Name)
	}
	if p.HudTheme == "" {
		return fmt.Errorf("profiles: hud_theme is required for %s (the HUD's theme id)", p.Name)
	}
	if len(p.Permissions) == 0 {
		return fmt.Errorf("profiles: permissions are required for %s (an EMPTY permission set must be declared explicitly, not omitted into permissiveness)", p.Name)
	}
	seen := map[string]bool{}
	for _, perm := range p.Permissions {
		if !validPermissionName(perm) {
			return fmt.Errorf("profiles: permission %q is not the closed shape (lowercase snake_case)", perm)
		}
		if perm == "all" && p.Role != "admin" {
			return fmt.Errorf("profiles: the \"all\" wildcard is ADMIN-only, profile %s has role %q", p.Name, p.Role)
		}
		if seen[perm] {
			return fmt.Errorf("profiles: duplicate permission %q for %s", perm, p.Name)
		}
		seen[perm] = true
	}
	if p.Mode.Default == "" {
		return fmt.Errorf("profiles: scarlix_mode.default is required for %s", p.Name)
	}
	if !validModeName(p.Mode.Default) {
		return fmt.Errorf("profiles: scarlix_mode.default %q is not the closed mode shape", p.Mode.Default)
	}
	if len(p.Mode.Allowed) == 0 {
		return fmt.Errorf("profiles: scarlix_mode.allowed is required for %s (the closed set of modes this profile may operate in)", p.Name)
	}
	allowed := map[string]bool{}
	for _, m := range p.Mode.Allowed {
		if !validModeName(m) {
			return fmt.Errorf("profiles: scarlix_mode.allowed entry %q is not the closed mode shape", m)
		}
		if allowed[m] {
			return fmt.Errorf("profiles: duplicate scarlix_mode.allowed entry %q for %s", m, p.Name)
		}
		allowed[m] = true
	}
	if !allowed[p.Mode.Default] {
		return fmt.Errorf("profiles: scarlix_mode.default %q must be a member of scarlix_mode.allowed %v for %s (a default outside the allowed set is an incoherent policy)", p.Mode.Default, p.Mode.Allowed, p.Name)
	}
	if !p.Mode.CanSwitch && len(p.Mode.Allowed) != 1 {
		return fmt.Errorf("profiles: scarlix_mode.can_switch=false with %d allowed modes is incoherent for %s — a profile that cannot switch must declare exactly ONE allowed mode", len(p.Mode.Allowed), p.Name)
	}
	if !p.TokenBudget.Unlimited && p.TokenBudget.Monthly == 0 {
		return fmt.Errorf("profiles: token_budget for %s is neither \"unlimited\" nor a typed mapping (monthly + model)", p.Name)
	}
	if p.ContentFilter != nil && p.ContentFilter.Enabled {
		if len(p.ContentFilter.Blocklist) == 0 {
			return fmt.Errorf("profiles: content_filter.enabled=true requires a non-empty blocklist for %s", p.Name)
		}
		for _, b := range p.ContentFilter.Blocklist {
			if strings.TrimSpace(b) == "" || len(b) > 64 {
				return fmt.Errorf("profiles: content_filter.blocklist entry %q is not a category shape for %s", b, p.Name)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Enforcement helpers — the action-boundary policy (server-side).
//
// Identity is the CALLER'S DECLARATION until per-profile authentication
// lands (a CF-4+ roadmap item, stated honestly in the SECURITY_MAP): the
// household Bearer token still gates every route; the profile reference
// (X-Scarlix-Profile header / profile field) selects WHICH policy applies.
// A declared policy REFUSES its forbidden actions — the dashboard hiding
// controls was never enforcement.
// ---------------------------------------------------------------------------

// ErrModeSwitchForbidden is the named refusal for a mode switch the
// profile's policy does not allow.
var ErrModeSwitchForbidden = fmt.Errorf("profiles: mode switch refused by profile policy")

// AllowsMode decides a mode-switch request against the declared policy:
// can_switch=false refuses EVERY switch (XOX stays in her default mode);
// a target outside the allowed set is refused BY NAME with the allowed
// set in the message. The default mode itself is always "allowed" (it is
// a member of the allowed set by validation).
func (p *Profile) AllowsMode(target string) error {
	if !p.Mode.CanSwitch {
		return fmt.Errorf("%w: profile %s cannot switch modes (scarlix_mode.can_switch=false, default %s is the only mode)", ErrModeSwitchForbidden, p.Name, p.Mode.Default)
	}
	for _, m := range p.Mode.Allowed {
		if m == target {
			return nil
		}
	}
	return fmt.Errorf("%w: mode %q is not in profile %s's allowed set %v", ErrModeSwitchForbidden, target, p.Name, p.Mode.Allowed)
}

// ErrPermissionDenied is the named refusal for a capability the profile's
// permission set does not carry.
var ErrPermissionDenied = fmt.Errorf("profiles: permission denied by profile policy")

// HasPermission decides a capability request against the declared
// permission set: the admin's "all" wildcard carries everything; a family
// member carries exactly the declared words.
func (p *Profile) HasPermission(perm string) bool {
	for _, declared := range p.Permissions {
		if declared == "all" || declared == perm {
			return true
		}
	}
	return false
}

// RequirePermission is HasPermission with the named refusal error.
func (p *Profile) RequirePermission(perm string) error {
	if p.HasPermission(perm) {
		return nil
	}
	return fmt.Errorf("%w: profile %s does not carry the %q permission (declared: %v)", ErrPermissionDenied, p.Name, perm, p.Permissions)
}

// ErrContentBlocked is the named refusal for chat input the content
// filter's blocklist hits.
var ErrContentBlocked = fmt.Errorf("profiles: chat input blocked by profile content filter")

// ContentBlocked checks the chat input against the profile's blocklist
// (case-insensitive substring, keyword-level — honestly documented: a
// semantic classifier is a CF-4+ item, never a silent claim). Returns the
// matched category for the refusal message.
func (p *Profile) ContentBlocked(text string) (string, bool) {
	if p.ContentFilter == nil || !p.ContentFilter.Enabled {
		return "", false
	}
	lower := strings.ToLower(text)
	for _, category := range p.ContentFilter.Blocklist {
		if strings.Contains(lower, strings.ToLower(strings.TrimSpace(category))) {
			return category, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// The manager: strict loading, explicit issues.
// ---------------------------------------------------------------------------

// Manager manages user profiles.
type Manager struct {
	dir string
}

// New creates a new profile manager.
func New(dir string) *Manager {
	return &Manager{dir: dir}
}

// Load reads, parses and validates ONE profile STRICTLY (the audit's
// remediation: no fallback — a read error, a YAML error or a validation
// error returns a named error; a policy-bearing file never becomes a
// filename-only permissive-looking profile).
func (m *Manager) Load(name string) (*Profile, error) {
	if !validProfileName(name) {
		return nil, fmt.Errorf("profiles: %q is not a profile name shape (lowercase letters, digits, - and _)", name)
	}
	path := filepath.Join(m.dir, name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("profiles: %q does not exist (%s)", name, path)
		}
		return nil, fmt.Errorf("profiles: reading %s failed: %v", path, err)
	}
	var p Profile
	// STRICT decoding: unknown keys are ERRORS for policy files (a typo like
	// "permissons:" silently dropping a permission is exactly the class of
	// accident the audit demands eliminated — the schema is closed).
	if err := unmarshalStrictProfile(data, &p); err != nil {
		return nil, fmt.Errorf("profiles: %s is not valid profile YAML (strict schema — unknown keys are refused): %v (KPK-19.6.5-P1-03: a policy-bearing file that fails to parse is an ERROR, never a silent fallback)", path, err)
	}
	if p.Name == "" {
		p.Name = name // the filename is the identity when the YAML omits it — still validated
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("profiles: %s: %v (KPK-19.6.5-P1-03)", path, err)
	}
	return &p, nil
}

// List returns the VALID profiles plus the issues of the INVALID ones
// (v19.6.6, KPK-19.6.5-P1-03: the old List() silently converted every
// parse failure into a filename-only fallback profile — the display name,
// role, theme and ALL policy fields discarded, the API caller none the
// wiser; now an invalid file is EXCLUDED from the list and SURFACED as an
// Issue, explicitly). Both slices are non-nil for a stable JSON shape.
func (m *Manager) List() ([]Profile, []Issue) {
	profiles := []Profile{}
	issues := []Issue{}

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return profiles, issues
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		p, err := m.Load(name)
		if err != nil {
			issues = append(issues, Issue{File: entry.Name(), Error: err.Error()})
			continue
		}
		profiles = append(profiles, *p)
	}
	return profiles, issues
}

// unmarshalStrictProfile decodes with KnownFields — the yaml.v3 strict
// surface: unknown keys in a policy file are ERRORS, not silently dropped
// declarations.
func unmarshalStrictProfile(data []byte, p *Profile) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(p)
}
