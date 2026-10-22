// AK-3 (v19.4.4): persisted, admin-owned capability grants + admin disable
// switches — the policy engine v1. Until AK-2, capability grants were
// request-supplied at session creation (documented transitional contract);
// AK-3 makes every grant a separate, persisted, admin-owned act:
//
//   - GrantRegistry is a JSON file (default /var/lib/scarlix-agentkernel/
//     grants.json) that records one GrantRecord per (agent, capability)
//     grant, each with the granting admin, the owner principal and — when a
//     grant is revoked — a tombstone (revoked_at/revoked_by). Grants are
//     append-only in spirit: they are never silently deleted, only revoked,
//     so the file itself is the reviewable history of who granted what.
//   - DisableRecord is the admin kill-switch per capability (e.g. during an
//     incident): while a disable record exists, Policy denies the capability
//     with rule/capability-disabled. Enable removes the switch; the enable
//     act itself becomes an audit event in AK-4's sink.
//   - The owner principals come from the reservation governor's user_tokens
//     principal space: the operator declares the allowed principal names
//     (SCARLIHQ_AGENT_PRINCIPALS, copied from the governor config values)
//     and every grant/session owner must be part of that set. Fail-closed:
//     an unknown principal in the file aborts startup, an empty allowlist is
//     a construction error, an owner mismatch is rejected at grant time AND
//     at session-creation time.
//
// Store discipline copied from the allocation store (KPK-02/04): the file
// and its directory are 0600/0700 (checked at load, never silently widened),
// writes go to a temp file in the same directory followed by rename (atomic
// on POSIX), and the in-memory state is guarded by a mutex so a decision can
// never be attributed to a registry state that no longer exists.

package agentkernel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sentinel errors for the grant registry.
var (
	// ErrGrantNotFound: no grant record for the (agent, capability) pair.
	ErrGrantNotFound = errors.New("agentkernel: grant not found")
	// ErrGrantInactive: the grant exists but is revoked.
	ErrGrantInactive = errors.New("agentkernel: grant is revoked")
	// ErrDuplicateGrant: an ACTIVE grant for the pair already exists.
	ErrDuplicateGrant = errors.New("agentkernel: active grant already exists")
	// ErrAgentOwnerMismatch: the grant's owner differs from the agent's
	// declared owner (one agent has exactly one owner principal).
	ErrAgentOwnerMismatch = errors.New("agentkernel: agent owner mismatch")
	// ErrUnknownPrincipal: the owner principal is not in the declared
	// user_tokens principal space.
	ErrUnknownPrincipal = errors.New("agentkernel: owner principal not in the declared principal space")
	// ErrAgentLimit / ErrGrantLimit: bounded registry (no unbounded growth).
	ErrAgentLimit = errors.New("agentkernel: grant registry at agent capacity")
	ErrGrantLimit = errors.New("agentkernel: grant registry at grant capacity")
	// ErrGrantFilePermissions: the grants file (or its directory) has looser
	// permissions than 0600/0700 — fail-closed, operator must fix it.
	ErrGrantFilePermissions = errors.New("agentkernel: grants file permissions too loose")
)

// DefaultMaxGrants bounds the total number of grant records (INCLUDING
// revoked tombstones — the history is real state, not free). Deliberately
// small: grants are a reviewed admin act, not a bulk import.
const DefaultMaxGrants = 256

// DefaultMaxAgents bounds the number of distinct agent IDs with grant
// records (active or revoked).
const DefaultMaxAgents = 64

// grantsFileVersion is the on-disk schema version. Load rejects anything
// else — a future schema change must be an explicit migration, not a guess.
const grantsFileVersion = 1

// GrantRecord is one persisted, admin-owned grant. A record with
// RevokedAt == nil is ACTIVE; otherwise it is a tombstone (the history of
// the grant stays in the file).
type GrantRecord struct {
	AgentID    AgentID    `json:"agent_id"`
	Capability Capability `json:"capability"`
	Owner      string     `json:"owner"`
	GrantedBy  string     `json:"granted_by"`
	GrantedAt  time.Time  `json:"granted_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	RevokedBy  string     `json:"revoked_by,omitempty"`
}

// Active reports whether the grant is currently in force.
func (g GrantRecord) Active() bool { return g.RevokedAt == nil }

// DisableRecord is the admin kill-switch for one capability.
type DisableRecord struct {
	Capability Capability `json:"capability"`
	DisabledBy string     `json:"disabled_by"`
	DisabledAt time.Time  `json:"disabled_at"`
	Reason     string     `json:"reason"`
}

// grantsFile is the on-disk representation.
type grantsFile struct {
	Version  int             `json:"version"`
	Grants   []GrantRecord   `json:"grants"`
	Disables []DisableRecord `json:"disables"`
}

// GrantRegistry is the persisted, admin-owned grant + disable store.
type GrantRegistry struct {
	mu         sync.Mutex
	path       string
	principals map[string]bool
	grants     []GrantRecord
	disables   map[Capability]DisableRecord
	maxGrants  int
	maxAgents  int
}

// NewGrantRegistry constructs an empty in-memory registry bound to a file
// path and a declared principal space. It does NOT touch the file — call
// Load to read (or initialize) it. Construction is fail-closed: empty path,
// empty principal space, or non-positive bounds are construction errors.
func NewGrantRegistry(path string, principals []string, maxGrants, maxAgents int) (*GrantRegistry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("agentkernel: grants file path is required")
	}
	if len(principals) == 0 {
		return nil, fmt.Errorf("agentkernel: principal space is required (copy the principal names from the governor user_tokens config)")
	}
	if maxGrants <= 0 || maxAgents <= 0 {
		return nil, fmt.Errorf("agentkernel: maxGrants and maxAgents must be > 0 (got %d/%d)", maxGrants, maxAgents)
	}
	if maxAgents > maxGrants {
		return nil, fmt.Errorf("agentkernel: maxAgents (%d) cannot exceed maxGrants (%d) — nonsensical bounds", maxAgents, maxGrants)
	}
	set := make(map[string]bool, len(principals))
	for _, p := range principals {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("agentkernel: principal space contains an empty principal name")
		}
		if set[p] {
			return nil, fmt.Errorf("agentkernel: duplicate principal %q in the declared principal space", p)
		}
		set[p] = true
	}
	return &GrantRegistry{
		path:       path,
		principals: set,
		grants:     make([]GrantRecord, 0),
		disables:   make(map[Capability]DisableRecord),
		maxGrants:  maxGrants,
		maxAgents:  maxAgents,
	}, nil
}

// Load reads and validates the grants file, or initializes it empty on
// first run. Validation is fail-closed: ANY malformed record, ANY unknown
// capability (closed enumeration), ANY owner outside the declared principal
// space, ANY owner mismatch within one agent, ANY revoked-without-revoker
// tombstone, or looser-than-0600/0700 file permissions abort the load. A
// corrupt or tampered grants file must never degrade into "some grants
// work" — the kernel refuses to start on it.
func (r *GrantRegistry) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, err := os.Stat(r.path)
	switch {
	case err == nil:
		// exists — permissions are checked below
	case errors.Is(err, os.ErrNotExist):
		// First run: initialize an empty, valid, 0600 file.
		if err := r.ensureDirLocked(); err != nil {
			return err
		}
		empty := grantsFile{Version: grantsFileVersion, Grants: []GrantRecord{}, Disables: []DisableRecord{}}
		if err := r.persistLocked(empty); err != nil {
			return fmt.Errorf("agentkernel: cannot initialize grants file %s: %w", r.path, err)
		}
		r.grants = make([]GrantRecord, 0)
		r.disables = make(map[Capability]DisableRecord)
		return nil
	default:
		return fmt.Errorf("agentkernel: cannot stat grants file %s: %w", r.path, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: %s has mode %o (want 0600)", ErrGrantFilePermissions, r.path, info.Mode().Perm())
	}
	if err := r.checkDirPermsLocked(); err != nil {
		return err
	}

	raw, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("agentkernel: cannot read grants file %s: %w", r.path, err)
	}
	var f grantsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("agentkernel: grants file %s is not valid JSON: %w", r.path, err)
	}
	if f.Version != grantsFileVersion {
		return fmt.Errorf("agentkernel: grants file %s has schema version %d (want %d) — explicit migration required", r.path, f.Version, grantsFileVersion)
	}

	// Grants: full validation, fail-closed on every rule.
	agents := make(map[AgentID]string) // agent -> declared owner
	seenActive := make(map[AgentID]map[Capability]bool)
	counted := make(map[AgentID]bool)
	for i, g := range f.Grants {
		if err := g.AgentID.Validate(); err != nil {
			return fmt.Errorf("agentkernel: grants file %s record %d: %w", r.path, i, err)
		}
		if !g.Capability.IsValid() {
			return fmt.Errorf("agentkernel: grants file %s record %d: unknown capability %q (closed enumeration)", r.path, i, g.Capability)
		}
		if strings.TrimSpace(g.Owner) == "" {
			return fmt.Errorf("agentkernel: grants file %s record %d: empty owner principal", r.path, i)
		}
		if !r.principals[g.Owner] {
			return fmt.Errorf("%w: %q (record %d)", ErrUnknownPrincipal, g.Owner, i)
		}
		if strings.TrimSpace(g.GrantedBy) == "" {
			return fmt.Errorf("agentkernel: grants file %s record %d: empty granted_by (grants are admin-owned — name the admin)", r.path, i)
		}
		if g.GrantedAt.IsZero() {
			return fmt.Errorf("agentkernel: grants file %s record %d: zero granted_at", r.path, i)
		}
		if g.RevokedAt != nil {
			if strings.TrimSpace(g.RevokedBy) == "" {
				return fmt.Errorf("agentkernel: grants file %s record %d: revoked grant without revoked_by", r.path, i)
			}
			if g.RevokedAt.Before(g.GrantedAt) {
				return fmt.Errorf("agentkernel: grants file %s record %d: revoked_at before granted_at", r.path, i)
			}
		}
		if prev, ok := agents[g.AgentID]; ok && prev != g.Owner {
			return fmt.Errorf("%w: agent %s is declared with owners %q and %q", ErrAgentOwnerMismatch, g.AgentID, prev, g.Owner)
		}
		agents[g.AgentID] = g.Owner
		if g.Active() {
			if seenActive[g.AgentID][g.Capability] {
				return fmt.Errorf("%w: agent %s already has an active grant for %q", ErrDuplicateGrant, g.AgentID, g.Capability)
			}
			if seenActive[g.AgentID] == nil {
				seenActive[g.AgentID] = make(map[Capability]bool)
			}
			seenActive[g.AgentID][g.Capability] = true
		}
		counted[g.AgentID] = true
	}
	if len(f.Grants) > r.maxGrants {
		return fmt.Errorf("%w: %d records (bound %d)", ErrGrantLimit, len(f.Grants), r.maxGrants)
	}
	if len(counted) > r.maxAgents {
		return fmt.Errorf("%w: %d agents (bound %d)", ErrAgentLimit, len(counted), r.maxAgents)
	}

	// Disables: validated with the same discipline.
	disables := make(map[Capability]DisableRecord)
	for i, d := range f.Disables {
		if !d.Capability.IsValid() {
			return fmt.Errorf("agentkernel: grants file %s disable %d: unknown capability %q", r.path, i, d.Capability)
		}
		if strings.TrimSpace(d.DisabledBy) == "" {
			return fmt.Errorf("agentkernel: grants file %s disable %d: empty disabled_by (disables are admin-owned — name the admin)", r.path, i)
		}
		if d.DisabledAt.IsZero() {
			return fmt.Errorf("agentkernel: grants file %s disable %d: zero disabled_at", r.path, i)
		}
		if _, dup := disables[d.Capability]; dup {
			return fmt.Errorf("agentkernel: grants file %s disable %d: duplicate disable for %q", r.path, i, d.Capability)
		}
		disables[d.Capability] = d
	}

	r.grants = append([]GrantRecord(nil), f.Grants...)
	r.disables = disables
	return nil
}

// Grant appends a new active grant and persists it. The act is admin-owned:
// grantedBy names the admin; owner is the principal the agent acts for and
// must already be (or become) the agent's single declared owner. Re-granting
// a previously revoked capability is allowed — it appends a NEW record, the
// tombstone stays (audit history is append-only in spirit).
func (r *GrantRegistry) Grant(agent AgentID, capability Capability, owner, grantedBy string, now time.Time) (GrantRecord, error) {
	if err := agent.Validate(); err != nil {
		return GrantRecord{}, err
	}
	if !capability.IsValid() {
		return GrantRecord{}, fmt.Errorf("agentkernel: unknown capability %q (closed enumeration — extend AllCapabilities deliberately)", capability)
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return GrantRecord{}, fmt.Errorf("agentkernel: grant owner principal is required")
	}
	if !r.principals[owner] {
		return GrantRecord{}, fmt.Errorf("%w: %q", ErrUnknownPrincipal, owner)
	}
	if strings.TrimSpace(grantedBy) == "" {
		return GrantRecord{}, fmt.Errorf("agentkernel: granted_by is required (grants are admin-owned — name the admin)")
	}
	if now.IsZero() {
		return GrantRecord{}, fmt.Errorf("agentkernel: grant timestamp is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// One agent, one owner — enforced across ALL records (active + revoked).
	for _, g := range r.grants {
		if g.AgentID == agent && g.Owner != owner {
			return GrantRecord{}, fmt.Errorf("%w: agent %s is declared with owner %q, refusing %q", ErrAgentOwnerMismatch, agent, g.Owner, owner)
		}
		if g.AgentID == agent && g.Capability == capability && g.Active() {
			return GrantRecord{}, fmt.Errorf("%w: agent %s already holds %q (revoke first if you mean to re-grant)", ErrDuplicateGrant, agent, capability)
		}
	}
	if len(r.grants) >= r.maxGrants {
		return GrantRecord{}, fmt.Errorf("%w: %d records (bound %d)", ErrGrantLimit, len(r.grants), r.maxGrants)
	}
	distinct := 0
	seenAgents := make(map[AgentID]bool, len(r.grants))
	for _, g := range r.grants {
		if !seenAgents[g.AgentID] {
			seenAgents[g.AgentID] = true
			distinct++
		}
	}
	if !seenAgents[agent] && distinct >= r.maxAgents {
		return GrantRecord{}, fmt.Errorf("%w: %d agents (bound %d)", ErrAgentLimit, distinct, r.maxAgents)
	}

	rec := GrantRecord{
		AgentID:    agent,
		Capability: capability,
		Owner:      owner,
		GrantedBy:  strings.TrimSpace(grantedBy),
		GrantedAt:  now.UTC(),
	}
	if err := r.appendLocked(rec); err != nil {
		return GrantRecord{}, err
	}
	return rec, nil
}

// Revoke tombstones an active grant. Revocation is the only way a grant
// stops being in force — records are never deleted, so the file remains the
// full reviewable history.
func (r *GrantRegistry) Revoke(agent AgentID, capability Capability, revokedBy string, now time.Time) (GrantRecord, error) {
	if strings.TrimSpace(revokedBy) == "" {
		return GrantRecord{}, fmt.Errorf("agentkernel: revoked_by is required (revocations are admin-owned — name the admin)")
	}
	if now.IsZero() {
		return GrantRecord{}, fmt.Errorf("agentkernel: revocation timestamp is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	idx := -1
	for i, g := range r.grants {
		if g.AgentID == agent && g.Capability == capability {
			idx = i
			break
		}
	}
	if idx == -1 {
		return GrantRecord{}, fmt.Errorf("%w: agent %s / %q", ErrGrantNotFound, agent, capability)
	}
	if !r.grants[idx].Active() {
		return GrantRecord{}, fmt.Errorf("%w: agent %s / %q (revoked by %s)", ErrGrantInactive, agent, capability, r.grants[idx].RevokedBy)
	}
	ts := now.UTC()
	r.grants[idx].RevokedAt = &ts
	r.grants[idx].RevokedBy = strings.TrimSpace(revokedBy)
	if err := r.persistLocked(r.snapshotLocked()); err != nil {
		// Roll back the in-memory tombstone so state and file stay in sync.
		r.grants[idx].RevokedAt = nil
		r.grants[idx].RevokedBy = ""
		return GrantRecord{}, fmt.Errorf("agentkernel: revocation persisted out of sync: %w", err)
	}
	return r.grants[idx], nil
}

// Disable turns the admin kill-switch for a capability ON.
func (r *GrantRegistry) Disable(capability Capability, disabledBy, reason string, now time.Time) (DisableRecord, error) {
	if !capability.IsValid() {
		return DisableRecord{}, fmt.Errorf("agentkernel: unknown capability %q (closed enumeration)", capability)
	}
	if strings.TrimSpace(disabledBy) == "" {
		return DisableRecord{}, fmt.Errorf("agentkernel: disabled_by is required (disables are admin-owned — name the admin)")
	}
	if now.IsZero() {
		return DisableRecord{}, fmt.Errorf("agentkernel: disable timestamp is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.disables[capability]; exists {
		return DisableRecord{}, fmt.Errorf("agentkernel: capability %q is already disabled", capability)
	}
	rec := DisableRecord{
		Capability: capability,
		DisabledBy: strings.TrimSpace(disabledBy),
		DisabledAt: now.UTC(),
		Reason:     strings.TrimSpace(reason),
	}
	r.disables[capability] = rec
	if err := r.persistLocked(r.snapshotLocked()); err != nil {
		delete(r.disables, capability)
		return DisableRecord{}, fmt.Errorf("agentkernel: disable persisted out of sync: %w", err)
	}
	return rec, nil
}

// Enable turns the admin kill-switch for a capability OFF (the record is
// removed; the act itself becomes an audit event in AK-4's sink).
func (r *GrantRegistry) Enable(capability Capability, enabledBy string, now time.Time) error {
	if !capability.IsValid() {
		return fmt.Errorf("agentkernel: unknown capability %q (closed enumeration)", capability)
	}
	if strings.TrimSpace(enabledBy) == "" {
		return fmt.Errorf("agentkernel: enabled_by is required (enables are admin-owned — name the admin)")
	}
	_ = now // the timestamp lands in the AK-4 audit event, not the state file

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.disables[capability]; !exists {
		return fmt.Errorf("agentkernel: capability %q is not disabled", capability)
	}
	delete(r.disables, capability)
	if err := r.persistLocked(r.snapshotLocked()); err != nil {
		// Roll back so state and file stay in sync.
		r.disables[capability] = DisableRecord{}
		delete(r.disables, capability)
		return fmt.Errorf("agentkernel: enable persisted out of sync: %w", err)
	}
	return nil
}

// GrantsFor returns the ACTIVE capability set of one agent (the set a new
// session is constructed from). Nil/empty means the agent has nothing in
// force — nothing is granted implicitly.
func (r *GrantRegistry) GrantsFor(agent AgentID) CapabilitySet {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := CapabilitySet{}
	for _, g := range r.grants {
		if g.AgentID == agent && g.Active() {
			set[g.Capability] = true
		}
	}
	return set
}

// AgentOwner returns the declared owner principal of one agent (from its
// grant records — active or revoked, the owner declaration is permanent).
func (r *GrantRegistry) AgentOwner(agent AgentID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range r.grants {
		if g.AgentID == agent {
			return g.Owner, true
		}
	}
	return "", false
}

// Policy builds the current kernel policy from the disable switches. This
// is what evaluation consults in registry mode — runtime toggles take
// effect on the NEXT evaluation, no restart needed.
func (r *GrantRegistry) Policy() Policy {
	r.mu.Lock()
	defer r.mu.Unlock()
	disabled := make(map[Capability]bool, len(r.disables))
	for c := range r.disables {
		disabled[c] = true
	}
	return Policy{Disabled: disabled}
}

// ListGrants returns a copy of all grant records (active + tombstones),
// deterministically sorted by agent, then capability, then granted_at.
func (r *GrantRegistry) ListGrants() []GrantRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]GrantRecord(nil), r.grants...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AgentID != out[j].AgentID {
			return out[i].AgentID < out[j].AgentID
		}
		if out[i].Capability != out[j].Capability {
			return out[i].Capability < out[j].Capability
		}
		return out[i].GrantedAt.Before(out[j].GrantedAt)
	})
	return out
}

// ListDisables returns the current disable switches in closed-enumeration
// order (deterministic output, no map-iteration order leaks).
func (r *GrantRegistry) ListDisables() []DisableRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listDisablesLocked()
}

// listDisablesLocked is the unlocked core of ListDisables — usable from
// code paths that already hold r.mu (snapshotLocked). Caller holds r.mu.
func (r *GrantRegistry) listDisablesLocked() []DisableRecord {
	out := make([]DisableRecord, 0, len(r.disables))
	for _, c := range AllCapabilities() {
		if rec, ok := r.disables[c]; ok {
			out = append(out, rec)
		}
	}
	return out
}

// Counts returns the registry sizes for the API surface and logs (active
// grants, total records including tombstones, distinct agents, disables).
func (r *GrantRegistry) Counts() (active, total, agents, disables int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[AgentID]bool)
	for _, g := range r.grants {
		if g.Active() {
			active++
		}
		seen[g.AgentID] = true
	}
	return active, len(r.grants), len(seen), len(r.disables)
}

// Path returns the grants file path (for logs and the API surface).
func (r *GrantRegistry) Path() string { return r.path }

// ---------------------------------------------------------------------------
// persistence internals
// ---------------------------------------------------------------------------

// appendLocked appends one record and persists. Caller holds r.mu.
func (r *GrantRegistry) appendLocked(rec GrantRecord) error {
	r.grants = append(r.grants, rec)
	if err := r.persistLocked(r.snapshotLocked()); err != nil {
		r.grants = r.grants[:len(r.grants)-1] // roll back on failed write
		return err
	}
	return nil
}

// snapshotLocked builds the on-disk representation. Caller holds r.mu —
// it must NOT call any public locking method (the classic self-deadlock).
func (r *GrantRegistry) snapshotLocked() grantsFile {
	f := grantsFile{
		Version:  grantsFileVersion,
		Grants:   append([]GrantRecord(nil), r.grants...),
		Disables: r.listDisablesLocked(),
	}
	return f
}

// persistLocked atomically writes the file: JSON is marshalled to a temp
// file in the SAME directory (same filesystem → rename is atomic on POSIX)
// with mode 0600, then renamed over the real path. A crash mid-write can
// leave a temp file behind but never a half-written grants file.
func (r *GrantRegistry) persistLocked(f grantsFile) error {
	if err := r.ensureDirLocked(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".grants-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, r.path)
}

// ensureDirLocked makes sure the parent directory exists with 0700.
// Caller holds r.mu (or is Load, which holds it too).
func (r *GrantRegistry) ensureDirLocked() error {
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("agentkernel: cannot create grants directory %s: %w", dir, err)
	}
	return r.checkDirPermsLocked()
}

// checkDirPermsLocked fail-closes on a grants directory looser than 0700.
// Unlike the allocation store we do NOT auto-fix: the grants file is an
// admin-owned security artifact — a loosened mode is treated as tampering
// until an operator fixes it deliberately.
func (r *GrantRegistry) checkDirPermsLocked() error {
	info, err := os.Stat(filepath.Dir(r.path))
	if err != nil {
		return fmt.Errorf("agentkernel: cannot stat grants directory: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: directory %s has mode %o (want 0700)", ErrGrantFilePermissions, filepath.Dir(r.path), info.Mode().Perm())
	}
	return nil
}
