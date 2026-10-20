// Package agentkernel is the Agent Kernel foundation (AK-1, v19.4.x line).
//
// Strategy (docs/ScaRgeN_CuLT_Strategy_Obsidian_Lattice_SK_2026-10.pdf,
// page 16): "Agenti až po bezpečnom vykonávacom jadre" — an agent is NEVER
// a synonym for an unbounded shell or a privileged process. The kernel is
// the deterministic core that owns:
//
//   - Agent identity: who the agent is and who owns it
//   - Capability model: the enumerated tools the agent may use
//   - Session lifecycle: bounded, observable conversations/interactions
//   - Permission engine: deterministic policy — "LLM navrhuje, politika
//     rozhoduje" (the LLM proposes; policy decides)
//   - Audit trail: what happened, why, and with what result
//
// AK-1 scope (this file set): the typed foundations — identity, capability
// allowlist, session state machine, permission evaluation and audit events.
// It is deliberately NOT yet wired into any HTTP/MCP surface: the contract
// is established and tested first; wiring is AK-2+ (compute intake API and
// agent sessions arrive together, per the roadmap).
//
// Hard rules encoded here (strategy page 22, "Bezpečnostné mantinely"):
//   - no free root shell: capabilities are a closed enumeration, never "exec"
//   - no AI-decided permissions: Evaluate is pure rule evaluation, no LLM
//   - every decision is auditable: Evaluate returns the matched rule
package agentkernel

import (
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Agent identity
// ---------------------------------------------------------------------------

// AgentID is a typed, validated agent identifier: lowercase, dot/hyphen
// separated, "kind.name" shape (e.g. "coder.agent-one"). Empty or malformed
// IDs are rejected — no anonymous agents.
type AgentID string

const maxAgentIDLen = 64

// Validate checks the canonical agent ID shape.
func (a AgentID) Validate() error {
	s := string(a)
	if s == "" {
		return fmt.Errorf("agentkernel: agent id is empty")
	}
	if len(s) > maxAgentIDLen {
		return fmt.Errorf("agentkernel: agent id longer than %d chars", maxAgentIDLen)
	}
	if strings.ContainsAny(s, " \t\n/\\@#$%&*()+=[]{}<>|~`\"'") {
		return fmt.Errorf("agentkernel: agent id %q contains forbidden characters", s)
	}
	parts := strings.SplitN(s, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("agentkernel: agent id %q must be kind.name (e.g. coder.agent-one)", s)
	}
	for _, part := range parts {
		if !isAgentIDPart(part) {
			return fmt.Errorf("agentkernel: agent id %q segments must be [a-z0-9-]", s)
		}
	}
	return nil
}

func isAgentIDPart(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

// AgentIdentity is who the agent is and who owns it. Owner is the principal
// (from the reservation/governor identity space or the ScarliHQ principal)
// accountable for the agent's actions. An agent without an owner does not
// exist.
type AgentIdentity struct {
	ID        AgentID
	Owner     string
	CreatedAt time.Time
}

// NewAgentIdentity validates and constructs an identity.
func NewAgentIdentity(id AgentID, owner string, createdAt time.Time) (AgentIdentity, error) {
	if err := id.Validate(); err != nil {
		return AgentIdentity{}, err
	}
	if strings.TrimSpace(owner) == "" {
		return AgentIdentity{}, fmt.Errorf("agentkernel: agent %q has no owner principal", id)
	}
	if createdAt.IsZero() {
		return AgentIdentity{}, fmt.Errorf("agentkernel: agent %q has no creation time", id)
	}
	return AgentIdentity{ID: id, Owner: owner, CreatedAt: createdAt.UTC()}, nil
}

// ---------------------------------------------------------------------------
// Capability model (closed enumeration — never "exec")
// ---------------------------------------------------------------------------

// Capability is a named, enumerated tool an agent may be granted. The set
// mirrors existing, already-allowlisted surfaces (ScarliHQ read APIs,
// compute lifecycle, mode requests). There is deliberately NO shell, no
// docker passthrough, no arbitrary file access — those live behind the
// Host Bridge and are not agent-grantable.
type Capability string

const (
	// CapabilityStatusRead: read system status/inventory (read-only).
	CapabilityStatusRead Capability = "status.read"
	// CapabilityGamingRead: read the gaming inventory (read-only).
	CapabilityGamingRead Capability = "gaming.read"
	// CapabilityModeRequest: request an operational mode change (goes
	// through the existing mode bridge — request, not execute).
	CapabilityModeRequest Capability = "mode.request"
	// CapabilityComputePlan: ask the planner for a dry-run plan
	// (side-effect free by construction).
	CapabilityComputePlan Capability = "compute.plan"
	// CapabilityComputeReserve: reserve resources through the future
	// compute intake API (governor stores, flock-serialized).
	CapabilityComputeReserve Capability = "compute.reserve"
	// CapabilityComputeRelease: request release of resources the agent's
	// owner owns (reconciliation-gated, never silent).
	CapabilityComputeRelease Capability = "compute.release"
)

// AllCapabilities is the closed allowlist.
func AllCapabilities() []Capability {
	return []Capability{
		CapabilityStatusRead,
		CapabilityGamingRead,
		CapabilityModeRequest,
		CapabilityComputePlan,
		CapabilityComputeReserve,
		CapabilityComputeRelease,
	}
}

// IsValid reports whether c is part of the closed enumeration.
func (c Capability) IsValid() bool {
	switch c {
	case CapabilityStatusRead, CapabilityGamingRead, CapabilityModeRequest,
		CapabilityComputePlan, CapabilityComputeReserve, CapabilityComputeRelease:
		return true
	}
	return false
}

// CapabilitySet is a validated set of capabilities granted to one agent.
type CapabilitySet map[Capability]bool

// NewCapabilitySet validates that every capability is part of the closed
// enumeration and rejects duplicates in the input slice.
func NewCapabilitySet(caps ...Capability) (CapabilitySet, error) {
	set := make(CapabilitySet, len(caps))
	for _, c := range caps {
		if !c.IsValid() {
			return nil, fmt.Errorf("agentkernel: unknown capability %q (closed enumeration — extend AllCapabilities deliberately)", c)
		}
		if set[c] {
			return nil, fmt.Errorf("agentkernel: duplicate capability %q", c)
		}
		set[c] = true
	}
	return set, nil
}

// Has reports membership.
func (s CapabilitySet) Has(c Capability) bool { return s[c] }

// ---------------------------------------------------------------------------
// Session lifecycle (bounded, observable)
// ---------------------------------------------------------------------------

// SessionState is the session lifecycle. CLOSED is terminal.
type SessionState string

const (
	SessionCreated   SessionState = "CREATED"
	SessionActive    SessionState = "ACTIVE"
	SessionSuspended SessionState = "SUSPENDED"
	SessionClosed    SessionState = "CLOSED"
)

// sessionTransitions is the legal-transition table. CLOSED is terminal.
var sessionTransitions = map[SessionState]map[SessionState]bool{
	SessionCreated:   {SessionActive: true, SessionClosed: true},
	SessionActive:    {SessionSuspended: true, SessionClosed: true},
	SessionSuspended: {SessionActive: true, SessionClosed: true},
	SessionClosed:    {}, // terminal — no transitions
}

// LegalSessionTransitions returns the allowed next states (empty for
// terminal states).
func LegalSessionTransitions(from SessionState) []SessionState {
	allowed := sessionTransitions[from]
	out := make([]SessionState, 0, len(allowed))
	for s := range allowed {
		out = append(out, s)
	}
	return out
}

// Session is one bounded agent interaction context.
type Session struct {
	ID        string
	AgentID   AgentID
	State     SessionState
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Transition applies a legal state transition, returning the updated
// session and an error for illegal moves (fail-closed — unknown states and
// terminal-state violations are rejected, mirroring the allocation and
// reservation state machines).
func (s *Session) Transition(to SessionState, now time.Time) error {
	if s == nil {
		return fmt.Errorf("agentkernel: nil session")
	}
	if _, known := sessionTransitions[s.State]; !known {
		return fmt.Errorf("agentkernel: unknown session state %q", s.State)
	}
	if s.State == SessionClosed {
		return fmt.Errorf("agentkernel: CLOSED is terminal — no transitions allowed")
	}
	if !sessionTransitions[s.State][to] {
		return fmt.Errorf("agentkernel: illegal transition %s → %s", s.State, to)
	}
	if now.Before(s.UpdatedAt) {
		return fmt.Errorf("agentkernel: transition timestamp before UpdatedAt")
	}
	s.State = to
	s.UpdatedAt = now.UTC()
	return nil
}

// ---------------------------------------------------------------------------
// Permission engine (deterministic — no LLM in the decision path)
// ---------------------------------------------------------------------------

// Decision is the outcome of a permission evaluation.
type Decision struct {
	Allow  bool
	Reason string
}

// Policy is the deterministic rule set. Rules, in evaluation order:
//
//  1. the session must exist and be ACTIVE (CREATED/SUSPENDED/CLOSED
//     sessions cannot act — an agent that is not live cannot do anything)
//  2. the session must belong to the acting agent
//  3. the capability must be part of the closed enumeration
//  4. the agent's capability set must contain the capability (the grant
//     itself — fail-closed: a nil or empty set grants nothing)
//  5. the capability must not be administratively disabled
//
// Every denial names the rule that fired; every allow names the grant.
// This is the audit-ready explanation the strategy document demands
// ("dôvod výberu aj odmietnutia").
type Policy struct {
	// Disabled lists capabilities administratively turned off (e.g. during
	// an incident). Denials cite the disabled rule.
	Disabled map[Capability]bool
}

// Evaluate decides whether agent/session may use capability at time now,
// given the agent's granted capability set. It is pure: same inputs, same
// decision, same reason — no LLM, no clock dependence beyond audit fields.
// The grant check is fail-closed: a nil or empty CapabilitySet denies
// everything (nothing is granted by default).
func (p Policy) Evaluate(agent AgentIdentity, session *Session, caps CapabilitySet, capability Capability) Decision {
	if session == nil {
		return Decision{Allow: false, Reason: "rule/session-required: no session"}
	}
	if session.AgentID != agent.ID {
		return Decision{Allow: false, Reason: "rule/session-owner: session does not belong to agent"}
	}
	if session.State != SessionActive {
		return Decision{Allow: false, Reason: fmt.Sprintf("rule/session-state: session is %s, must be ACTIVE", session.State)}
	}
	if !capability.IsValid() {
		return Decision{Allow: false, Reason: fmt.Sprintf("rule/capability-unknown: %q is not in the closed enumeration", capability)}
	}
	if !caps.Has(capability) {
		return Decision{Allow: false, Reason: fmt.Sprintf("rule/capability-not-granted: agent %s does not hold %q", agent.ID, capability)}
	}
	if p.Disabled[capability] {
		return Decision{Allow: false, Reason: fmt.Sprintf("rule/capability-disabled: %q is administratively disabled", capability)}
	}
	return Decision{Allow: true, Reason: fmt.Sprintf("grant: agent %s holds capability %s", agent.ID, capability)}
}

// ---------------------------------------------------------------------------
// Audit trail
// ---------------------------------------------------------------------------

// AuditEvent records one permission evaluation (allow OR deny) — the
// traceable-lifecycle building block (strategy page 24: every action has an
// identifier, state, reason, and audit events).
type AuditEvent struct {
	Timestamp  time.Time
	AgentID    AgentID
	SessionID  string
	Capability Capability
	Decision   Decision
}

// NewAuditEvent stamps an evaluation.
func NewAuditEvent(now time.Time, agent AgentIdentity, session *Session, capability Capability, decision Decision) (AuditEvent, error) {
	if now.IsZero() {
		return AuditEvent{}, fmt.Errorf("agentkernel: audit event timestamp is required")
	}
	if session != nil {
		if session.ID == "" {
			return AuditEvent{}, fmt.Errorf("agentkernel: audit event session id is required")
		}
	}
	return AuditEvent{
		Timestamp:  now.UTC(),
		AgentID:    agent.ID,
		SessionID:  sessionIDOrEmpty(session),
		Capability: capability,
		Decision:   decision,
	}, nil
}

func sessionIDOrEmpty(s *Session) string {
	if s == nil {
		return ""
	}
	return s.ID
}
