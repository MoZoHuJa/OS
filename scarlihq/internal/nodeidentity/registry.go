package nodeidentity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Sentinel errors. Admin-surface operations carry specific errors (the admin
// is trusted); Authenticate returns ONLY the generic ErrAuthentication for
// unknown-node / bad-secret cases — no node-existence oracle for outsiders.
var (
	ErrNodeNotFound    = errors.New("nodeidentity: node not found")
	ErrNodeExists      = errors.New("nodeidentity: node already exists")
	ErrInviteNotUsable = errors.New("nodeidentity: no usable invite for this node")
	ErrInviteExpired   = errors.New("nodeidentity: invite expired")
	ErrInviteLive      = errors.New("nodeidentity: an invite is already live for this node")
	ErrNodeQuarantined = errors.New("nodeidentity: node is quarantined — new work refused; cleanup only via trusted recovery")
	ErrNodeNotEnrolled = errors.New("nodeidentity: node has no live credential (not enrolled)")
	ErrAuthentication  = errors.New("nodeidentity: authentication failed")
	ErrInvalidNodeID   = errors.New("nodeidentity: invalid node ID")
	ErrRegistryClosed  = errors.New("nodeidentity: registry closed")
)

// DefaultRotationGrace is the default credential-rotation grace window
// (design §2.3: both digests accepted for 15 minutes, then old is dead).
const DefaultRotationGrace = 15 * time.Minute

// DefaultInviteTTL is the default enrollment invite lifetime (design §2.5).
const DefaultInviteTTL = 30 * time.Minute

// MaxInviteTTL bounds invite lifetimes (fail-closed bound: an admin cannot
// mint a practically-immortal invite).
const MaxInviteTTL = 24 * time.Hour

// RegistryConfig carries the operator-tunable bounds (validated at
// construction — no per-request surprises).
type RegistryConfig struct {
	// RotationGrace — how long a rotated-away credential stays valid.
	RotationGrace time.Duration
	// DefaultInviteTTL — used when CreateInvite is called with ttl <= 0.
	DefaultInviteTTL time.Duration
}

func (c RegistryConfig) withDefaults() RegistryConfig {
	out := c
	if out.RotationGrace <= 0 {
		out.RotationGrace = DefaultRotationGrace
	}
	if out.DefaultInviteTTL <= 0 {
		out.DefaultInviteTTL = DefaultInviteTTL
	}
	return out
}

// Registry is the authoritative node-identity registry. All mutations go
// through here and persist through the JSONL store; all reads are
// sanitized (NodeView). The registry is safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	store *Store
	clock Clock
	cfg   RegistryConfig
	nodes map[string]*NodeRecord
}

// NewRegistry loads the persisted node records and validates every one
// (fail-closed: a corrupt or invalid record refuses the whole registry —
// an identity store that half-loads is not an identity store).
func NewRegistry(store *Store, clock Clock, cfg RegistryConfig) (*Registry, error) {
	if store == nil {
		return nil, fmt.Errorf("nodeidentity: store is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("nodeidentity: clock is required")
	}
	cfg = cfg.withDefaults()
	if cfg.RotationGrace > 24*time.Hour {
		return nil, fmt.Errorf("nodeidentity: rotation grace window must be <= 24h")
	}
	records, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("nodeidentity: cannot load node records: %w", err)
	}
	r := &Registry{
		store: store,
		clock: clock,
		cfg:   cfg,
		nodes: make(map[string]*NodeRecord, len(records)),
	}
	for i := range records {
		rec := &records[i]
		if err := validateRecord(rec); err != nil {
			return nil, fmt.Errorf("nodeidentity: persisted record %q invalid: %w", rec.NodeID, err)
		}
		if _, dup := r.nodes[rec.NodeID]; dup {
			return nil, fmt.Errorf("nodeidentity: duplicate persisted record for node %q", rec.NodeID)
		}
		r.nodes[rec.NodeID] = rec
	}
	return r, nil
}

// validateRecord enforces the persisted invariants (schema, status shape,
// credential/invite presence per status).
func validateRecord(rec *NodeRecord) error {
	if rec == nil {
		return fmt.Errorf("nil record")
	}
	if rec.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version %q is not %q", rec.SchemaVersion, SchemaVersion)
	}
	if err := ValidateNodeID(rec.NodeID); err != nil {
		return err
	}
	if !rec.Status.IsValid() {
		return fmt.Errorf("unknown status %q", rec.Status)
	}
	for _, role := range rec.Roles {
		if !role.IsValid() {
			return fmt.Errorf("unknown role %q", role)
		}
	}
	if len(rec.Roles) == 0 {
		return fmt.Errorf("no roles assigned")
	}
	switch rec.Status {
	case StatusActive:
		if rec.CredentialDigest == "" {
			return fmt.Errorf("ACTIVE without a credential digest")
		}
		if rec.InviteDigest != "" {
			return fmt.Errorf("ACTIVE must not carry an invite digest")
		}
	case StatusInvited:
		if rec.InviteDigest == "" || rec.InviteExpiresAt.IsZero() {
			return fmt.Errorf("INVITED without a live invite digest + expiry")
		}
		if rec.CredentialDigest != "" {
			return fmt.Errorf("INVITED must not carry a credential digest")
		}
	case StatusQuarantined:
		if rec.RevokedAt.IsZero() {
			return fmt.Errorf("QUARANTINED without revoked_at")
		}
	}
	return nil
}

// digestHex is the single digest discipline: sha256 → hex.
func digestHex(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// randomSecret generates a fresh 64-hex-char secret (32 random bytes).
func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("nodeidentity: cannot read randomness: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// constantTimeEqualDigest compares two hex digests in constant time.
func constantTimeEqualDigest(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// persistLocked rewrites the whole registry state durably (small N; atomic
// rewrite under flock — the store's discipline). Called with mu held.
func (r *Registry) persistLocked() error {
	out := make([]NodeRecord, 0, len(r.nodes))
	for _, rec := range r.nodes {
		out = append(out, *rec)
	}
	if err := r.store.SaveAll(out); err != nil {
		return fmt.Errorf("nodeidentity: persist failed: %w", err)
	}
	return nil
}

// CreateInvite mints a one-time enrollment invite for nodeID (design §2.5
// step 1). The returned Invite carries the raw secret EXACTLY ONCE — the
// registry persists only its digest. A still-live invite for the same node
// is refused; an expired or consumed one may be re-invited. Admin-surface
// operation — the caller must have verified the ADMIN principal.
func (r *Registry) CreateInvite(actor, nodeID string, ttl time.Duration) (*Invite, error) {
	if err := ValidateNodeID(nodeID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidNodeID, err)
	}
	if ttl < 0 {
		return nil, fmt.Errorf("nodeidentity: invite TTL must be >= 0")
	}
	if ttl == 0 {
		ttl = r.cfg.DefaultInviteTTL
	}
	if ttl > MaxInviteTTL {
		return nil, fmt.Errorf("nodeidentity: invite TTL %s exceeds the %s bound", ttl, MaxInviteTTL)
	}
	now := r.clock.Now()
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	existing, exists := r.nodes[nodeID]
	if exists && existing.Status == StatusActive {
		return nil, fmt.Errorf("%w (node %s is already ACTIVE)", ErrNodeExists, nodeID)
	}
	if exists && existing.Status == StatusQuarantined {
		return nil, fmt.Errorf("%w (node %s is QUARANTINED — reactivate via trusted recovery instead)", ErrNodeQuarantined, nodeID)
	}
	if exists && existing.Status == StatusInvited && inviteStillLive(existing, now) {
		return nil, fmt.Errorf("%w (expires %s)", ErrInviteLive, existing.InviteExpiresAt.UTC().Format(time.RFC3339))
	}
	rec := &NodeRecord{
		SchemaVersion:   SchemaVersion,
		NodeID:          nodeID,
		Status:          StatusInvited,
		Roles:           []NodeRole{RoleCompute},
		InviteDigest:    digestHex(secret),
		InviteExpiresAt: now.Add(ttl),
		CreatedAt:       now,
		UpdatedAt:       now,
		LastModifiedBy:  actor,
	}
	r.nodes[nodeID] = rec
	if err := r.persistLocked(); err != nil {
		return nil, err
	}
	return &Invite{NodeID: nodeID, Secret: secret, ExpiresAt: rec.InviteExpiresAt}, nil
}

// inviteStillLive reports whether an INVITED record's invite is usable.
func inviteStillLive(rec *NodeRecord, now time.Time) bool {
	return rec != nil &&
		rec.Status == StatusInvited &&
		rec.InviteDigest != "" &&
		!rec.InviteExpiresAt.IsZero() &&
		now.Before(rec.InviteExpiresAt)
}

// Enroll completes enrollment (design §2.5 steps 2–3): a node presenting the
// matching, unexpired, unconsumed invite secret receives its per-node
// credential EXACTLY ONCE. The invite is consumed; the raw credential is
// never persisted. The nonce is recorded bound to this enrollment event.
func (r *Registry) Enroll(nodeID, inviteSecret, nonce string) (*NodeCredential, error) {
	if nodeID == "" || strings.TrimSpace(inviteSecret) == "" {
		return nil, ErrAuthentication
	}
	now := r.clock.Now()
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	rec, exists := r.nodes[nodeID]
	if !exists || rec.Status != StatusInvited {
		// Generic refusal: no node-existence oracle (a wrong-node probe
		// gets the same answer as a wrong secret).
		return nil, ErrAuthentication
	}
	if !inviteStillLive(rec, now) {
		return nil, ErrInviteExpired
	}
	if !constantTimeEqualDigest(digestHex(inviteSecret), rec.InviteDigest) {
		return nil, ErrAuthentication
	}
	// Consume the invite: flip to ACTIVE with the fresh credential digest.
	rec.Status = StatusActive
	rec.CredentialDigest = digestHex(secret)
	rec.PreviousCredentialDigest = ""
	rec.RotationGraceUntil = time.Time{}
	rec.InviteDigest = ""
	rec.InviteExpiresAt = time.Time{}
	rec.EnrolledAt = now
	rec.EnrollmentNonce = nonce
	rec.UpdatedAt = now
	rec.LastModifiedBy = "enroll:" + nodeID
	if err := validateRecord(rec); err != nil {
		// Invariant break — fail closed WITHOUT mutating the store.
		return nil, fmt.Errorf("nodeidentity: enrollment produced an invalid record (refusing to persist): %w", err)
	}
	if err := r.persistLocked(); err != nil {
		return nil, err
	}
	return &NodeCredential{NodeID: nodeID, Secret: secret}, nil
}

// Authenticate resolves a node credential (design §2.3). Returns the
// unforgeable NodeIdentity or a sentinel error:
//   - ErrAuthentication — unknown node or wrong secret (no oracle),
//   - ErrNodeQuarantined — valid credential, revoked node (the holder
//     legitimately learns the quarantine — the agent must stop new work),
//   - ErrNodeNotEnrolled — INVITED node (invite not consumed).
func (r *Registry) Authenticate(nodeID, secret string) (NodeIdentity, error) {
	if nodeID == "" || strings.TrimSpace(secret) == "" {
		return NodeIdentity{}, ErrAuthentication
	}
	digest := digestHex(secret)

	// Everything the decision needs is COPIED under the read lock — the
	// record itself is concurrently mutable (rotations); reading a field
	// after unlock would be a data race.
	type nodeSnapshot struct {
		status NodeStatus
		roles  []NodeRole
		active bool
		prev   bool
	}
	var snap nodeSnapshot
	r.mu.RLock()
	rec, exists := r.nodes[nodeID]
	if exists {
		snap.status = rec.Status
		snap.roles = append([]NodeRole(nil), rec.Roles...)
		now := r.clock.Now()
		snap.active = constantTimeEqualDigest(digest, rec.CredentialDigest)
		snap.prev = rec.PreviousCredentialDigest != "" &&
			constantTimeEqualDigest(digest, rec.PreviousCredentialDigest) &&
			now.Before(rec.RotationGraceUntil)
	}
	r.mu.RUnlock()

	if !exists {
		return NodeIdentity{}, ErrAuthentication
	}
	switch snap.status {
	case StatusQuarantined:
		if snap.active || snap.prev {
			return NodeIdentity{}, ErrNodeQuarantined
		}
		return NodeIdentity{}, ErrAuthentication
	case StatusInvited:
		return NodeIdentity{}, ErrNodeNotEnrolled
	case StatusActive:
		if !snap.active && !snap.prev {
			return NodeIdentity{}, ErrAuthentication
		}
	default:
		return NodeIdentity{}, ErrAuthentication
	}
	return identityFromRoles(nodeID, snap.roles), nil
}

// identityFromRoles builds the principal from a validated role list (the
// role→scope mapping is the closed in-code table, not configuration).
func identityFromRoles(nodeID string, roles []NodeRole) NodeIdentity {
	roleSet := make(map[NodeRole]bool, len(roles))
	scopes := make(map[NodeScope]bool)
	for _, role := range roles {
		roleSet[role] = true
		for _, s := range ScopesForRole(role) {
			scopes[s] = true
		}
	}
	return NodeIdentity{nodeID: nodeID, roles: roleSet, scopes: scopes}
}

// RotateCredential issues a fresh credential for an ACTIVE node (design
// §2.3). The old digest stays accepted until the grace window expires.
// Re-rotation replaces the grace window (an already-rotated-away secret
// dies immediately). Admin-surface operation.
func (r *Registry) RotateCredential(actor, nodeID string) (*NodeCredential, error) {
	now := r.clock.Now()
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	rec, exists := r.nodes[nodeID]
	if !exists {
		return nil, ErrNodeNotFound
	}
	if rec.Status == StatusQuarantined {
		return nil, ErrNodeQuarantined
	}
	if rec.Status != StatusActive {
		return nil, ErrNodeNotEnrolled
	}
	rec.PreviousCredentialDigest = rec.CredentialDigest
	rec.RotationGraceUntil = now.Add(r.cfg.RotationGrace)
	rec.CredentialDigest = digestHex(secret)
	rec.UpdatedAt = now
	rec.LastModifiedBy = actor
	if err := validateRecord(rec); err != nil {
		return nil, fmt.Errorf("nodeidentity: rotation produced an invalid record (refusing to persist): %w", err)
	}
	if err := r.persistLocked(); err != nil {
		return nil, err
	}
	return &NodeCredential{NodeID: nodeID, Secret: secret}, nil
}

// Revoke quarantines a node (design §2.2). Existing reservations are NOT
// auto-released — the fabric registry treats the node's capacity as
// unproven until trusted recovery. Admin-surface operation.
func (r *Registry) Revoke(actor, nodeID string) error {
	now := r.clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, exists := r.nodes[nodeID]
	if !exists {
		return ErrNodeNotFound
	}
	if rec.Status == StatusQuarantined {
		return nil // idempotent
	}
	rec.Status = StatusQuarantined
	rec.RevokedAt = now
	rec.UpdatedAt = now
	rec.LastModifiedBy = actor
	if err := validateRecord(rec); err != nil {
		return fmt.Errorf("nodeidentity: revocation produced an invalid record (refusing to persist): %w", err)
	}
	return r.persistLocked()
}

// Reactivate returns a QUARANTINED node to ACTIVE with a FRESH credential —
// the trusted-recovery path (design §2.2: an operator confirms cleanup and
// re-admits the node; the old credential is dead). Admin-surface operation.
func (r *Registry) Reactivate(actor, nodeID string) (*NodeCredential, error) {
	now := r.clock.Now()
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, exists := r.nodes[nodeID]
	if !exists {
		return nil, ErrNodeNotFound
	}
	if rec.Status != StatusQuarantined {
		return nil, fmt.Errorf("nodeidentity: node %s is not quarantined (status %s)", nodeID, rec.Status)
	}
	rec.Status = StatusActive
	rec.CredentialDigest = digestHex(secret)
	rec.PreviousCredentialDigest = ""
	rec.RotationGraceUntil = time.Time{}
	rec.UpdatedAt = now
	rec.LastModifiedBy = actor
	if err := validateRecord(rec); err != nil {
		return nil, fmt.Errorf("nodeidentity: reactivation produced an invalid record (refusing to persist): %w", err)
	}
	if err := r.persistLocked(); err != nil {
		return nil, err
	}
	return &NodeCredential{NodeID: nodeID, Secret: secret}, nil
}

// Record returns the sanitized view of one node.
func (r *Registry) Record(nodeID string) (NodeView, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, exists := r.nodes[nodeID]
	if !exists {
		return NodeView{}, ErrNodeNotFound
	}
	return r.viewLocked(rec), nil
}

// List returns sanitized views of all nodes (sorted by NodeID for
// deterministic operator output).
func (r *Registry) List() []NodeView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NodeView, 0, len(r.nodes))
	ids := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		ids = append(ids, id)
	}
	sortStrings(ids)
	for _, id := range ids {
		out = append(out, r.viewLocked(r.nodes[id]))
	}
	return out
}

func (r *Registry) viewLocked(rec *NodeRecord) NodeView {
	v := NodeView{
		NodeID:             rec.NodeID,
		Status:             rec.Status,
		Roles:              append([]NodeRole(nil), rec.Roles...),
		EnrolledAt:         rec.EnrolledAt,
		RevokedAt:          rec.RevokedAt,
		CreatedAt:          rec.CreatedAt,
		UpdatedAt:          rec.UpdatedAt,
		RotationGraceUntil: rec.RotationGraceUntil,
	}
	for _, role := range rec.Roles {
		for _, s := range ScopesForRole(role) {
			v.Scopes = append(v.Scopes, s)
		}
	}
	if rec.Status == StatusInvited {
		v.InviteExpiresAt = rec.InviteExpiresAt
		v.InviteLive = inviteStillLive(rec, r.clock.Now())
	}
	return v
}

// sortStrings is a tiny deterministic in-package sort (keeps the dependency
// surface of this package at stdlib-only, no import churn).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
