// Package nodeidentity — the distributed fabric's node identity and trust
// model (v19.9.0, Compute Fabric Phase 5, roadmap §9.2 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md,
// design: docs/SCARLIX_COMPUTE_FABRIC_PHASE_5_DISTRIBUTED_DESIGN.md §2).
//
// CHARTER (the non-negotiable contract):
//
//   - A node is NOT trusted because it is reachable on LAN or Tailscale
//     (roadmap §9.2). Network membership is transport, never authorization.
//     Every node principal is minted ONLY through admin-approved enrollment
//     and authenticated with a per-node credential whose sha256 digest is
//     the only stored form.
//   - Enrollment is invite-based and one-time: an ADMIN creates a TTL-bounded
//     invite; the enrolling agent presents the invite secret once over the
//     encrypted transport; the control plane consumes the invite and issues
//     a per-node credential returned exactly ONCE (the raw secret is never
//     persisted, never logged — the same discipline as
//     reservation.TokenIdentityProvider).
//   - Credentials rotate with a bounded grace window (old digest accepted
//     until it expires, then dead). Revocation is QUARANTINE: no new work,
//     and existing reservations are NOT auto-released — capacity is not
//     proven free (roadmap §9.5's "cleanup only through trusted recovery").
//   - Node principals are strictly narrower than humans/agents: the closed
//     scope set below is the whole authorization surface; a node principal
//     can never be an admin and never a reconciler-of-record.
//   - Replay protection: every authenticated mutating node request carries
//     a unique request ID and a timestamp; the ReplayGuard rejects stale
//     timestamps and (node, request-ID) replays within a bounded window —
//     fail-closed throughout.
//
// This package is pure domain + persistence; it does no network I/O.
package nodeidentity

import (
	"fmt"
	"time"
)

// Clock is the injected time source (tests drive determinism through it).
type Clock interface {
	Now() time.Time
}

// SystemClock is the production clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// NodeStatus is the node lifecycle state (design §2.2).
//
// UNREACHABLE is deliberately NOT a status here: reachability is an
// annotation the fabric registry (internal/fabric) keeps per node — it
// degrades admission, never identity.
type NodeStatus string

const (
	// StatusInvited — an enrollment invite exists (TTL-bounded, one-time).
	StatusInvited NodeStatus = "INVITED"
	// StatusActive — enrolled; holds a live credential.
	StatusActive NodeStatus = "ACTIVE"
	// StatusQuarantined — revoked; new work refused; cleanup only via
	// trusted (operator-driven) recovery.
	StatusQuarantined NodeStatus = "QUARANTINED"
)

// IsValid reports whether s is a known node status.
func (s NodeStatus) IsValid() bool {
	switch s {
	case StatusInvited, StatusActive, StatusQuarantined:
		return true
	}
	return false
}

// NodeRole is an enrolled node's role. v19.9.0 ships exactly one closed role;
// additions are additive-only and must map to a closed scope set.
type NodeRole string

const (
	// RoleCompute — a fabric executor node: it may publish inventory/health,
	// report executions, and receive start/stop/reconcile for allocations the
	// ledger placed on it.
	RoleCompute NodeRole = "compute"
)

// IsValid reports whether r is a known node role.
func (r NodeRole) IsValid() bool {
	return r == RoleCompute
}

// NodeScope is one authorization scope a node principal may hold. The closed
// set is the WHOLE surface — "receive" scopes are inbound commands from the
// control plane, outbound ones are the node's own reports/pushes.
type NodeScope string

const (
	ScopeInventoryPush       NodeScope = "inventory:push"
	ScopeHealthPush          NodeScope = "health:push"
	ScopeExecutionReport     NodeScope = "execution:report"
	ScopeAllocationStart     NodeScope = "allocation:receive-start"
	ScopeAllocationStop      NodeScope = "allocation:receive-stop"
	ScopeAllocationReconcile NodeScope = "allocation:receive-reconcile"
)

// IsValid reports whether s is a known node scope.
func (s NodeScope) IsValid() bool {
	switch s {
	case ScopeInventoryPush, ScopeHealthPush, ScopeExecutionReport,
		ScopeAllocationStart, ScopeAllocationStop, ScopeAllocationReconcile:
		return true
	}
	return false
}

// ScopesForRole returns the closed scope set for a role (the role-to-scope
// mapping is decided HERE, not by configuration — a role can never widen
// itself through a config edit).
func ScopesForRole(role NodeRole) []NodeScope {
	switch role {
	case RoleCompute:
		return []NodeScope{
			ScopeInventoryPush,
			ScopeHealthPush,
			ScopeExecutionReport,
			ScopeAllocationStart,
			ScopeAllocationStop,
			ScopeAllocationReconcile,
		}
	default:
		return nil
	}
}

// NodePrincipalPrefix is how a node principal renders: "node:<id>". The
// reservation/admission layers treat these principals as non-admin,
// non-reconciler (design §2.4).
const NodePrincipalPrefix = "node:"

// NodeIDLocal is the reserved node ID of the control-plane host itself.
// It can NEVER be enrolled as a remote node (it is the planner's NodeLocal).
const NodeIDLocal = "local"

// SchemaVersion is the persisted record schema (additive-only evolution).
const SchemaVersion = "scarlix.nodeidentity/v1"

// NodeRecord is the persisted state of one node (digests only — the raw
// secrets exist only in the one-time return values of Enroll/Rotate).
type NodeRecord struct {
	SchemaVersion string     `json:"schema_version"`
	NodeID        string     `json:"node_id"`
	Status        NodeStatus `json:"status"`
	Roles         []NodeRole `json:"roles"`
	// CredentialDigest — sha256 hex of the ACTIVE credential secret.
	CredentialDigest string `json:"credential_digest,omitempty"`
	// PreviousCredentialDigest — sha256 hex of the rotated-away secret,
	// accepted only until RotationGraceUntil.
	PreviousCredentialDigest string    `json:"previous_credential_digest,omitempty"`
	RotationGraceUntil       time.Time `json:"rotation_grace_until,omitempty"`
	// InviteDigest — sha256 hex of the one-time invite secret (INVITED only).
	InviteDigest    string    `json:"invite_digest,omitempty"`
	InviteExpiresAt time.Time `json:"invite_expires_at,omitempty"`
	EnrolledAt      time.Time `json:"enrolled_at,omitempty"`
	RevokedAt       time.Time `json:"revoked_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// LastModifiedBy — the actor that last mutated the record (operator
	// audit trail; "enroll:<nodeID>" for self-enrollment).
	LastModifiedBy string `json:"last_modified_by,omitempty"`
	// EnrollmentNonce — the nonce bound to the enrollment event that issued
	// the live credential (design §2.5 step 2).
	EnrollmentNonce string `json:"enrollment_nonce,omitempty"`
}

// NodeView is the sanitized public view of a NodeRecord — no digests, no
// nonces. This is what the API surfaces (roadmap §11.5: never leak secrets).
type NodeView struct {
	NodeID             string      `json:"node_id"`
	Status             NodeStatus  `json:"status"`
	Roles              []NodeRole  `json:"roles,omitempty"`
	Scopes             []NodeScope `json:"scopes,omitempty"`
	EnrolledAt         time.Time   `json:"enrolled_at,omitempty"`
	RevokedAt          time.Time   `json:"revoked_at,omitempty"`
	CreatedAt          time.Time   `json:"created_at"`
	UpdatedAt          time.Time   `json:"updated_at"`
	RotationGraceUntil time.Time   `json:"rotation_grace_until,omitempty"`
	InviteLive         bool        `json:"invite_live,omitempty"`
	InviteExpiresAt    time.Time   `json:"invite_expires_at,omitempty"`
}

// NodeIdentity is the authenticated principal resolved from a valid node
// credential. Unforgeable from outside the package's Authenticate (the
// constructor is package-private, mirroring reservation.Identity).
type NodeIdentity struct {
	nodeID string
	roles  map[NodeRole]bool
	scopes map[NodeScope]bool
}

// NodeID returns the node's ID.
func (i NodeIdentity) NodeID() string { return i.nodeID }

// Principal returns the principal string ("node:<id>").
func (i NodeIdentity) Principal() string { return NodePrincipalPrefix + i.nodeID }

// HasRole reports whether the node holds role r.
func (i NodeIdentity) HasRole(r NodeRole) bool { return i.roles[r] }

// HasScope reports whether the node holds scope s (the whole authorization
// surface — design §2.4).
func (i NodeIdentity) HasScope(s NodeScope) bool { return i.scopes[s] }

// Scopes returns the held scopes (sorted by the caller if displayed).
func (i NodeIdentity) Scopes() []NodeScope {
	out := make([]NodeScope, 0, len(i.scopes))
	for s := range i.scopes {
		out = append(out, s)
	}
	return out
}

// String renders the principal (never the credential).
func (i NodeIdentity) String() string { return i.Principal() }

// NodeCredential is the one-time return value of Enroll/Rotate: the raw
// secret exists HERE and in the agent's own secure storage only — the
// registry persists the digest.
type NodeCredential struct {
	NodeID string
	Secret string
}

// Invite is the one-time return value of CreateInvite: the raw secret exists
// HERE and wherever the admin transports it to the node operator — the
// registry persists the digest only.
type Invite struct {
	NodeID    string
	Secret    string
	ExpiresAt time.Time
}

func (c *NodeCredential) String() string {
	if c == nil {
		return "NodeCredential(nil)"
	}
	return fmt.Sprintf("NodeCredential{node=%s secret=<redacted>}", c.NodeID)
}

func (i *Invite) String() string {
	if i == nil {
		return "Invite(nil)"
	}
	return fmt.Sprintf("Invite{node=%s secret=<redacted> expires=%s}", i.NodeID, i.ExpiresAt.UTC().Format(time.RFC3339))
}

// ValidateNodeID enforces the closed node-ID shape: lowercase alphanumerics
// and hyphens, 1-63 chars, and not the reserved "local" (the control-plane
// host) or anything that renders ambiguously as a principal.
func ValidateNodeID(id string) error {
	if id == "" || len(id) > 63 {
		return fmt.Errorf("nodeidentity: node ID must be 1-63 characters")
	}
	if id == NodeIDLocal {
		return fmt.Errorf("nodeidentity: node ID %q is reserved for the control-plane host", NodeIDLocal)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return fmt.Errorf("nodeidentity: node ID must be lowercase alphanumeric or hyphen (got %q)", string(c))
		}
	}
	if id[0] == '-' || id[len(id)-1] == '-' {
		return fmt.Errorf("nodeidentity: node ID must not start or end with a hyphen")
	}
	return nil
}
