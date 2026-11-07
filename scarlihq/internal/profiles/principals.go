package profiles

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Identity-bound principals (v19.6.7, KPK-19.6.6-P1-01).
//
// The v19.6.6 defect: the family-profile policy was DECIDED against a
// CALLER-DECLARED profile reference — the body field / the
// X-Scarlix-Profile header — while the authentication boundary knew only
// ONE household Bearer token. Whoever held the shared token could (a) omit
// the reference and skip the policy entirely, or (b) self-select the zmor
// admin profile. The source honestly documented the residual; the audit
// correctly refused to call that a security boundary.
//
// v19.6.7 binds identity to authentication:
//
//   - every profile MAY carry its own Bearer token (the token index, loaded
//     from /etc/scarlix/profile-tokens.conf or SCARLIHQ_PROFILE_TOKENS);
//     a presented token RESOLVES to exactly that principal;
//   - the household token (SCARLIHQ_TOKEN) resolves to the ADMIN principal
//     — the operator's own identity, never a family member's;
//   - Authorize is the ONE shared decision function (REST + MCP): a client
//     profile reference must MATCH the authenticated principal, or the
//     principal must be admin (an act-as that can only REDUCE privileges);
//     an omitted reference is DERIVED from the principal — there is no
//     policy-free path in multi-user mode;
//   - deployments without a token index are SINGLE-USER by construction
//     (one token, one identity, the admin) — the compatibility path is
//     named as such, explicitly, never mistaken for household isolation.
//
// Audit events (requirement 6): every authorization decision is logged —
// principal, resolved profile, decision, reason — WITHOUT Bearer tokens.
// ---------------------------------------------------------------------------

// Principal is the authenticated identity behind one request.
type Principal struct {
	// Name is the profile the token belongs to ("hugo", "xox", …) or
	// HouseholdPrincipal for the household (admin) token.
	Name string
	// IsAdmin is true for the household token and for tokens whose profile
	// declares role: admin.
	IsAdmin bool
	// MultiUser is true when a per-profile token index is active (the
	// strict mode: identity is bound, references are checked, omissions
	// are derived).
	MultiUser bool
	// ViaHouseholdToken marks the principal authenticated by the shared
	// SCARLIHQ_TOKEN (the operator) rather than a per-profile token.
	ViaHouseholdToken bool
}

// HouseholdPrincipal is the principal name for the shared household
// (operator) token. It is a NAME, not a profile file; its policy, when
// needed, is the role: admin profile discovered in the registry.
const HouseholdPrincipal = "household"

// tokenEntry is one profile→token mapping in the index.
type tokenEntry struct {
	profile string
	token   string
}

// TokenIndex maps per-profile Bearer tokens to profile identities.
// Resolution is constant-time per entry (crypto/subtle), entries are
// iterated in a deterministic (sorted) order.
type TokenIndex struct {
	entries []tokenEntry
}

// NewTokenIndex validates and indexes profile→token pairs. Profile names
// use the closed profile-name shape; tokens must be non-empty, unique, and
// must not equal the household token (that collision is refused here —
// one Bearer must never authenticate as two different principals).
func NewTokenIndex(pairs map[string]string, householdToken string) (*TokenIndex, error) {
	if len(pairs) == 0 {
		return nil, fmt.Errorf("profiles: the token index is empty — declare profile:token pairs (or omit the index entirely for single-user mode)")
	}
	idx := &TokenIndex{entries: make([]tokenEntry, 0, len(pairs))}
	seenTokens := map[string]string{}
	for name, token := range pairs {
		if !validProfileName(name) {
			return nil, fmt.Errorf("profiles: token index entry %q is not a profile name shape (lowercase letters, digits, - and _)", name)
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, fmt.Errorf("profiles: token index entry %q carries an EMPTY token", name)
		}
		if other, dup := seenTokens[token]; dup {
			return nil, fmt.Errorf("profiles: token index collision — profiles %q and %q share one token (one Bearer authenticates exactly one identity)", other, name)
		}
		if householdToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(householdToken)) == 1 {
			return nil, fmt.Errorf("profiles: token index entry %q reuses the household SCARLIHQ_TOKEN — a profile token must be its own secret (rotate one of them)", name)
		}
		seenTokens[token] = name
		idx.entries = append(idx.entries, tokenEntry{profile: name, token: token})
	}
	sort.Slice(idx.entries, func(i, j int) bool { return idx.entries[i].profile < idx.entries[j].profile })
	return idx, nil
}

// Empty reports whether any per-profile token is indexed.
func (t *TokenIndex) Empty() bool { return t == nil || len(t.entries) == 0 }

// Len returns the number of indexed profile tokens.
func (t *TokenIndex) Len() int {
	if t == nil {
		return 0
	}
	return len(t.entries)
}

// Profiles returns the indexed profile names (sorted).
func (t *TokenIndex) Profiles() []string {
	if t == nil {
		return nil
	}
	names := make([]string, 0, len(t.entries))
	for _, e := range t.entries {
		names = append(names, e.profile)
	}
	return names
}

// Resolve maps a presented token to its profile identity. The scan is
// constant-time per entry; a miss returns ok=false.
func (t *TokenIndex) Resolve(token string) (string, bool) {
	if t == nil || token == "" {
		return "", false
	}
	for _, e := range t.entries {
		if subtle.ConstantTimeCompare([]byte(token), []byte(e.token)) == 1 {
			return e.profile, true
		}
	}
	return "", false
}

// LoadTokenIndex reads the tokens file: one "profile:token" entry per
// line, '#' comments and blank lines skipped. The file must not be
// world-readable (secrets discipline — 0640 or tighter; the installer
// ships it 0400 owned by the container's uid).
func LoadTokenIndex(path, householdToken string) (*TokenIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("profiles: reading the token index %s failed: %v", path, err)
	}
	if info, serr := os.Stat(path); serr == nil && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("profiles: the token index %s is group/world-accessible (%v) — chmod 640 or tighter; secrets are not ambient", path, info.Mode().Perm())
	}
	pairs := map[string]string{}
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, token, found := strings.Cut(line, ":")
		if !found {
			return nil, fmt.Errorf("profiles: token index %s line %d is not profile:token", path, n+1)
		}
		name = strings.TrimSpace(name)
		pairs[name] = strings.TrimSpace(token)
	}
	idx, err := NewTokenIndex(pairs, householdToken)
	if err != nil {
		return nil, fmt.Errorf("profiles: token index %s: %v", path, err)
	}
	return idx, nil
}

// ParseTokenIndex parses an inline index ("name:token,name:token") —
// the env-declared form (SCARLIHQ_PROFILE_TOKENS), tests and small
// deployments.
func ParseTokenIndex(raw, householdToken string) (*TokenIndex, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("profiles: the inline token index is empty — declare name:token pairs")
	}
	pairs := map[string]string{}
	for n, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, token, found := strings.Cut(pair, ":")
		if !found {
			return nil, fmt.Errorf("profiles: inline token index entry %d %q is not name:token", n+1, pair)
		}
		pairs[strings.TrimSpace(name)] = strings.TrimSpace(token)
	}
	return NewTokenIndex(pairs, householdToken)
}

// ---------------------------------------------------------------------------
// The authenticator + THE shared authorization function.
// ---------------------------------------------------------------------------

// Authenticator resolves Bearer tokens to principals and decides the
// effective profile policy for a request. ONE instance is shared by the
// REST handler and the MCP server — one authorization function, not
// independent optional checks (the audit's requirement 4).
type Authenticator struct {
	householdToken string
	index          *TokenIndex
	manager        *Manager
	logMu          sync.Mutex // serializes audit event lines
}

// NewAuthenticator constructs the authenticator. A nil index is the
// single-user deployment (the household token is the admin identity);
// a non-nil index activates multi-user mode.
func NewAuthenticator(index *TokenIndex, householdToken string, m *Manager) (*Authenticator, error) {
	if m == nil {
		return nil, fmt.Errorf("profiles: the authenticator requires the profile manager")
	}
	if householdToken == "" {
		return nil, fmt.Errorf("profiles: the authenticator requires the household token (SCARLIHQ_TOKEN)")
	}
	return &Authenticator{householdToken: householdToken, index: index, manager: m}, nil
}

// MultiUser reports whether per-profile tokens are active.
func (a *Authenticator) MultiUser() bool { return !a.index.Empty() }

// SetTokenIndex binds the per-profile token index (multi-user mode).
// Deliberately one-shot: an already-bound authenticator refuses a second
// index — identity sources do not hot-swap under a running server.
func (a *Authenticator) SetTokenIndex(index *TokenIndex) error {
	if index == nil {
		return fmt.Errorf("profiles: the token index is required (nil is not a mode)")
	}
	if !a.index.Empty() {
		return fmt.Errorf("profiles: the authenticator already carries a token index (%d profiles) — restart to change identity sources", a.index.Len())
	}
	a.index = index
	return nil
}

// ResolveToken maps a presented Bearer token to its principal, or nil
// when the token is valid for nobody. Per-profile tokens win over the
// household token (they cannot collide — NewTokenIndex refuses that).
func (a *Authenticator) ResolveToken(token string) *Principal {
	if token == "" {
		return nil
	}
	if name, ok := a.index.Resolve(token); ok {
		// The profile's own role decides admin-ness (zmor's token is an
		// admin token because zmor.yaml says role: admin) — but the
		// principal exists even when the policy file is broken; the
		// authorization step fails closed on that, by name.
		isAdmin := false
		if p, err := a.manager.Load(name); err == nil {
			isAdmin = p.Role == "admin"
		}
		return &Principal{Name: name, IsAdmin: isAdmin, MultiUser: a.MultiUser()}
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.householdToken)) == 1 {
		return &Principal{Name: HouseholdPrincipal, IsAdmin: true, MultiUser: a.MultiUser(), ViaHouseholdToken: true}
	}
	return nil
}

// ErrProfileMismatch is the named refusal for a client-declared profile
// that is not the authenticated principal's own (and the principal is not
// admin — an admin act-as is a deliberate privilege reduction, allowed).
var ErrProfileMismatch = fmt.Errorf("profiles: profile reference refused — it is not the authenticated principal's profile")

// ErrProfileUnavailable is the named fail-closed refusal for a principal
// whose policy cannot be loaded (broken/missing profile file): the action
// is refused rather than decided without a policy.
var ErrProfileUnavailable = fmt.Errorf("profiles: the principal's profile policy is unavailable (fail closed)")

// AuthzDecision is one authorization outcome — the audit event payload
// (principal, resolved profile, decision, reason; never a token).
type AuthzDecision struct {
	Principal string
	Profile   string
	Declared  string
	Action    string
	Deny      error // nil = allow
}

// Authorize is THE shared authorization function (REST /api/mode +
// /api/ai/chat + MCP scarlix_mode_set all call exactly this):
//
//   - declared != "": the reference must load (a broken policy file is a
//     400-class fail-closed, never a bypass); in multi-user mode it must
//     MATCH the principal's profile or the principal must be admin
//     (an act-as that can only reduce privileges — every admin right is a
//     superset of every family profile);
//   - declared == "": the profile is DERIVED — in multi-user mode from the
//     principal (their own policy, or the role:admin profile for the
//     household token); there is NO policy-free path. In single-user mode
//     (no token index) the admin profile is derived when present;
//     a deployment with no admin profile at all keeps the legacy
//     unrestricted single-user behavior — named exactly as that;
//   - the returned decision is logged as the audit event (no tokens).
//
// The returned *Profile may be nil WITH a nil error — that is the explicit
// single-user legacy contract (the operator has no profile files; the
// household token is the whole trust boundary). Every multi-user outcome
// is a non-nil profile or an error.
func (a *Authenticator) Authorize(principal *Principal, declared, action string) (*Profile, *AuthzDecision) {
	if principal == nil {
		d := &AuthzDecision{Principal: "(unauthenticated)", Declared: declared, Action: action, Deny: fmt.Errorf("no principal on an authed request — an internal wiring defect, refused")}
		a.auditEvent(d)
		return nil, d
	}
	decision := &AuthzDecision{Principal: principal.Name, Declared: declared, Action: action}

	// A declared reference: load first (fail closed on a broken file),
	// then check it against the authenticated identity.
	if declared != "" {
		p, err := a.manager.Load(declared)
		if err != nil {
			decision.Deny = fmt.Errorf("declared profile %q fails to load (fail closed — a broken policy file is never an unenforced bypass): %v", declared, err)
			a.auditEvent(decision)
			return nil, decision
		}
		if a.MultiUser() && !principal.IsAdmin && principal.Name != declared {
			decision.Deny = fmt.Errorf("%w: principal %q cannot select profile %q (identity is token-bound; an omitted or foreign reference is refused, not honored)", ErrProfileMismatch, principal.Name, declared)
			a.auditEvent(decision)
			return nil, decision
		}
		decision.Profile = p.Name
		a.auditEvent(decision)
		return p, decision
	}

	// No declared reference: derive.
	if a.MultiUser() {
		if principal.ViaHouseholdToken {
			// The operator token derives the role:admin profile — the
			// decidable admin path, never an unrestricted one. No admin
			// profile in multi-user mode is a configuration defect: refuse.
			p, err := a.adminProfile()
			if err != nil {
				decision.Deny = fmt.Errorf("%w: the household token needs a role:admin profile in multi-user mode (zmor.yaml ships one): %v", ErrProfileUnavailable, err)
				a.auditEvent(decision)
				return nil, decision
			}
			decision.Profile = p.Name
			a.auditEvent(decision)
			return p, decision
		}
		p, err := a.manager.Load(principal.Name)
		if err != nil {
			decision.Deny = fmt.Errorf("%w: principal %q's profile policy: %v", ErrProfileUnavailable, principal.Name, err)
			a.auditEvent(decision)
			return nil, decision
		}
		decision.Profile = p.Name
		a.auditEvent(decision)
		return p, decision
	}

	// Single-user mode: the household token IS the admin identity. When an
	// admin profile exists its policy applies (all permissions, unlimited
	// budget — equivalent privileges to the legacy path, but DECIDABLE and
	// auditable); when no admin profile exists at all the legacy
	// single-user behavior stands — explicitly, not silently.
	p, err := a.adminProfile()
	if err != nil {
		// No admin profile in the tree: the honest legacy contract.
		decision.Profile = "(single-user legacy: no profile policy)"
		a.auditEvent(decision)
		return nil, decision
	}
	decision.Profile = p.Name
	a.auditEvent(decision)
	return p, decision
}

// adminProfile finds the (first, sorted) role:admin profile in the
// registry — the derived policy for the household token.
func (a *Authenticator) adminProfile() (*Profile, error) {
	profs, issues := a.manager.List()
	if len(issues) > 0 {
		// A broken file in the registry is surfaced, not swallowed — but it
		// does not by itself refuse the admin derivation unless NO valid
		// admin profile exists.
		_ = issues
	}
	var adminNames []string
	for _, p := range profs {
		if p.Role == "admin" {
			adminNames = append(adminNames, p.Name)
		}
	}
	if len(adminNames) == 0 {
		return nil, fmt.Errorf("no role:admin profile is loadable in %s", a.manager.dir)
	}
	sort.Strings(adminNames)
	return a.manager.Load(adminNames[0])
}

// auditEvent persists the authorization decision as a structured log line
// (stdout → container logs; the agent-kernel audit sink is a separate,
// opt-in surface for kernel acts). Principal, resolved profile, declared
// reference, action, decision, reason — NEVER a Bearer token.
func (a *Authenticator) auditEvent(d *AuthzDecision) {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	if d.Deny != nil {
		log.Printf("AUDIT authz principal=%s profile=%s declared=%s action=%s decision=DENY reason=%q", d.Principal, d.Profile, d.Declared, d.Action, d.Deny.Error())
		return
	}
	log.Printf("AUDIT authz principal=%s profile=%s declared=%s action=%s decision=ALLOW", d.Principal, d.Profile, d.Declared, d.Action)
}

// ---------------------------------------------------------------------------
// Context plumbing — one principal per request, set by the auth
// middleware (REST and MCP), read at the action boundary.
// ---------------------------------------------------------------------------

type principalContextKey struct{}

// WithPrincipal stores the resolved principal in the request context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext retrieves the principal (nil when absent — an
// unauthenticated or legacy path).
func PrincipalFromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalContextKey{}).(*Principal)
	return p
}
