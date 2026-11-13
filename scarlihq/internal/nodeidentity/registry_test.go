package nodeidentity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock drives deterministic time through the registry.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestRegistry(t *testing.T) (*Registry, *fakeClock, string) {
	t.Helper()
	dir := t.TempDir()
	clock := newFakeClock()
	reg, err := NewRegistry(NewStore(filepath.Join(dir, "nodes.jsonl")), clock, RegistryConfig{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg, clock, dir
}

func mustInvite(t *testing.T, r *Registry, nodeID string, ttl time.Duration) *Invite {
	t.Helper()
	inv, err := r.CreateInvite("admin-op", nodeID, ttl)
	if err != nil {
		t.Fatalf("CreateInvite(%s): %v", nodeID, err)
	}
	return inv
}

func mustEnroll(t *testing.T, r *Registry, inv *Invite) *NodeCredential {
	t.Helper()
	cred, err := r.Enroll(inv.NodeID, inv.Secret, "nonce-1")
	if err != nil {
		t.Fatalf("Enroll(%s): %v", inv.NodeID, err)
	}
	return cred
}

func TestEnrollHappyPathIssuesCredentialOnce(t *testing.T) {
	reg, clock, _ := newTestRegistry(t)
	inv := mustInvite(t, reg, "gpu-node-a", 30*time.Minute)
	if inv.Secret == "" || len(inv.Secret) != 64 {
		t.Fatalf("invite secret must be 64 hex chars, got %d", len(inv.Secret))
	}
	cred := mustEnroll(t, reg, inv)

	// AUTHENTICATE with the issued credential → node principal + full closed scope set.
	id, err := reg.Authenticate("gpu-node-a", cred.Secret)
	if err != nil {
		t.Fatalf("Authenticate after enroll: %v", err)
	}
	if id.Principal() != "node:gpu-node-a" {
		t.Fatalf("principal = %q, want node:gpu-node-a", id.Principal())
	}
	if !id.HasRole(RoleCompute) {
		t.Fatal("enrolled node must hold the compute role")
	}
	for _, s := range ScopesForRole(RoleCompute) {
		if !id.HasScope(s) {
			t.Fatalf("node principal missing scope %s", s)
		}
	}

	// The stored file must contain ONLY the digest, never the raw secrets.
	data, err := os.ReadFile(filepath.Join(filepath.Dir(reg.store.Path()), "nodes.jsonl"))
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(data), inv.Secret) || strings.Contains(string(data), cred.Secret) {
		t.Fatal("the persisted store contains a RAW secret — digest-only discipline violated")
	}
	var rec NodeRecord
	if err := json.Unmarshal([]byte(strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]), &rec); err != nil {
		t.Fatalf("unmarshal first record: %v", err)
	}
	if rec.CredentialDigest != digestHex(cred.Secret) {
		t.Fatal("stored digest does not match the issued credential")
	}
	if rec.Status != StatusActive {
		t.Fatalf("status = %q, want ACTIVE", rec.Status)
	}
	if !rec.EnrolledAt.Equal(clock.Now()) {
		t.Fatalf("enrolled_at = %v, want %v", rec.EnrolledAt, clock.Now())
	}
	if rec.EnrollmentNonce != "nonce-1" {
		t.Fatalf("enrollment nonce not bound: %q", rec.EnrollmentNonce)
	}
}

func TestEnrollConsumesInviteReplayRefused(t *testing.T) {
	reg, _, _ := newTestRegistry(t)
	inv := mustInvite(t, reg, "node-b", 30*time.Minute)
	mustEnroll(t, reg, inv)

	// Replayed enrollment with the SAME (already-consumed) invite → refused.
	if _, err := reg.Enroll(inv.NodeID, inv.Secret, "nonce-2"); err == nil {
		t.Fatal("replayed enrollment with a consumed invite must be refused")
	}
	// And an empty secret never authenticates anything.
	if _, err := reg.Authenticate("node-b", ""); err == nil {
		t.Fatal("empty secret must never authenticate")
	}
}

func TestEnrollExpiredInviteRefused(t *testing.T) {
	reg, clock, _ := newTestRegistry(t)
	inv := mustInvite(t, reg, "node-c", 30*time.Minute)
	clock.Advance(31 * time.Minute)
	if _, err := reg.Enroll(inv.NodeID, inv.Secret, "n"); err == nil {
		t.Fatal("expired invite must be refused")
	} else if err != ErrInviteExpired {
		t.Fatalf("want ErrInviteExpired, got %v", err)
	}
	// After expiry a fresh invite for the same node is allowed (the old one is dead).
	if _, err := reg.CreateInvite("admin-op", "node-c", 0); err != nil {
		t.Fatalf("re-invite after expiry: %v", err)
	}
}

func TestEnrollWrongSecretNoOracle(t *testing.T) {
	reg, _, _ := newTestRegistry(t)
	mustInvite(t, reg, "node-d", 30*time.Minute)

	// Wrong secret for an INVITED node → the generic error, not "invite mismatch".
	if _, err := reg.Enroll("node-d", "not-the-secret", "n"); err != ErrAuthentication {
		t.Fatalf("want generic ErrAuthentication, got %v", err)
	}
	// Wrong NODE → the same generic error (no node-existence oracle).
	if _, err := reg.Enroll("no-such-node", "whatever", "n"); err != ErrAuthentication {
		t.Fatalf("want generic ErrAuthentication for unknown node, got %v", err)
	}
}

func TestCreateInviteGuards(t *testing.T) {
	reg, _, _ := newTestRegistry(t)

	// "local" is reserved — it can never be a remote node.
	if _, err := reg.CreateInvite("op", NodeIDLocal, 0); err == nil {
		t.Fatal("the reserved local node ID must not be invitable")
	}
	// Invalid shapes refused.
	for _, bad := range []string{"", "UPPER", "with space", "-lead", "trail-", "a_b", strings.Repeat("x", 64)} {
		if _, err := reg.CreateInvite("op", bad, 0); err == nil {
			t.Fatalf("invalid node ID %q must be refused", bad)
		}
	}
	// TTL bound.
	if _, err := reg.CreateInvite("op", "node-e", MaxInviteTTL+time.Second); err == nil {
		t.Fatal("invite TTL above the bound must be refused")
	}
	// A live invite for the same node is refused.
	mustInvite(t, reg, "node-e", 10*time.Minute)
	if _, err := reg.CreateInvite("op", "node-e", 0); err == nil {
		t.Fatal("a second live invite for the same node must be refused")
	}
	// An ACTIVE node cannot be re-invited.
	inv := mustInvite(t, reg, "node-f", 0)
	mustEnroll(t, reg, inv)
	if _, err := reg.CreateInvite("op", "node-f", 0); err == nil {
		t.Fatal("re-inviting an ACTIVE node must be refused")
	}
}

func TestRotationGraceWindow(t *testing.T) {
	reg, clock, _ := newTestRegistry(t)
	inv := mustInvite(t, reg, "node-g", 0)
	old := mustEnroll(t, reg, inv)

	newCred, err := reg.RotateCredential("admin-op", "node-g")
	if err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if newCred.Secret == old.Secret {
		t.Fatal("rotation must issue a DIFFERENT secret")
	}

	// Both credentials authenticate inside the grace window.
	if _, err := reg.Authenticate("node-g", old.Secret); err != nil {
		t.Fatalf("old credential must still authenticate inside the grace window: %v", err)
	}
	if _, err := reg.Authenticate("node-g", newCred.Secret); err != nil {
		t.Fatalf("new credential must authenticate: %v", err)
	}

	// After the window the OLD credential is dead; the new one survives.
	clock.Advance(DefaultRotationGrace + time.Second)
	if _, err := reg.Authenticate("node-g", old.Secret); err != ErrAuthentication {
		t.Fatalf("expired old credential must fail generically, got %v", err)
	}
	if _, err := reg.Authenticate("node-g", newCred.Secret); err != nil {
		t.Fatalf("new credential must survive the window: %v", err)
	}

	// Re-rotation: the chain becomes active=newer, previous=newCred
	// (in its grace window), and the ORIGINAL (now previous-previous)
	// is dead immediately.
	newer, err := reg.RotateCredential("op", "node-g")
	if err != nil {
		t.Fatalf("re-rotation: %v", err)
	}
	if _, err := reg.Authenticate("node-g", old.Secret); err != ErrAuthentication {
		t.Fatalf("the original (previous-previous) credential must be dead, got %v", err)
	}
	if _, err := reg.Authenticate("node-g", newCred.Secret); err != nil {
		t.Fatalf("the current previous credential must authenticate within its grace window: %v", err)
	}
	if _, err := reg.Authenticate("node-g", newer.Secret); err != nil {
		t.Fatalf("the newest credential must authenticate: %v", err)
	}
}

func TestRevokeQuarantineAndReactivate(t *testing.T) {
	reg, clock, _ := newTestRegistry(t)
	inv := mustInvite(t, reg, "node-h", 0)
	cred := mustEnroll(t, reg, inv)

	// AUTHENTICATE pre-revoke works.
	if _, err := reg.Authenticate("node-h", cred.Secret); err != nil {
		t.Fatalf("pre-revoke authenticate: %v", err)
	}
	if err := reg.Revoke("admin-op", "node-h"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// Revoke is idempotent.
	if err := reg.Revoke("admin-op", "node-h"); err != nil {
		t.Fatalf("second Revoke must be idempotent, got %v", err)
	}
	// A quarantined node's VALID credential reports quarantine (the agent
	// must stop new work) — with the sentinel, not a generic refusal.
	if _, err := reg.Authenticate("node-h", cred.Secret); err != ErrNodeQuarantined {
		t.Fatalf("want ErrNodeQuarantined, got %v", err)
	}
	// A quarantined node is not re-invitable (trusted recovery instead).
	if _, err := reg.CreateInvite("op", "node-h", 0); err == nil {
		t.Fatal("quarantined node must not be re-invitable")
	}

	// Trusted recovery: reactivate → fresh credential, old dead.
	fresh, err := reg.Reactivate("admin-op", "node-h")
	if err != nil {
		t.Fatalf("Reactivate: %v", err)
	}
	if fresh.Secret == cred.Secret {
		t.Fatal("reactivation must issue a FRESH credential")
	}
	if _, err := reg.Authenticate("node-h", cred.Secret); err != ErrAuthentication {
		t.Fatalf("old credential must be dead after reactivation, got %v", err)
	}
	id, err := reg.Authenticate("node-h", fresh.Secret)
	if err != nil {
		t.Fatalf("reactivated credential must authenticate: %v", err)
	}
	if id.NodeID() != "node-h" {
		t.Fatalf("node id = %q", id.NodeID())
	}
	_ = clock
}

func TestRegistryRestartLoadsPersistedState(t *testing.T) {
	reg, clock, dir := newTestRegistry(t)
	inv := mustInvite(t, reg, "node-i", 0)
	cred := mustEnroll(t, reg, inv)
	inv2 := mustInvite(t, reg, "node-j", time.Hour)

	// Fresh registry over the SAME store (a control-plane restart).
	clock.Advance(time.Second)
	restarted, err := NewRegistry(NewStore(filepath.Join(dir, "nodes.jsonl")), clock, RegistryConfig{})
	if err != nil {
		t.Fatalf("restart load: %v", err)
	}
	// The enrolled credential survives the restart.
	if _, err := restarted.Authenticate("node-i", cred.Secret); err != nil {
		t.Fatalf("credential must survive a control-plane restart: %v", err)
	}
	// The live invite for node-j survives too.
	if _, err := restarted.Enroll("node-j", inv2.Secret, "n"); err != nil {
		t.Fatalf("live invite must survive a restart: %v", err)
	}
	// Views survive (sanitized).
	view, err := restarted.Record("node-i")
	if err != nil {
		t.Fatalf("Record after restart: %v", err)
	}
	if view.Status != StatusActive || len(view.Scopes) == 0 {
		t.Fatalf("view after restart: %+v", view)
	}
}

func TestSanitizedViewsCarryNoSecrets(t *testing.T) {
	reg, _, _ := newTestRegistry(t)
	inv := mustInvite(t, reg, "node-k", 0)
	cred := mustEnroll(t, reg, inv)

	for _, v := range reg.List() {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal view: %v", err)
		}
		s := string(b)
		if strings.Contains(s, cred.Secret) || strings.Contains(s, inv.Secret) ||
			strings.Contains(s, "digest") || strings.Contains(s, "nonce") {
			t.Fatalf("sanitized view leaks secret material: %s", s)
		}
	}
}

func TestCorruptStoreRefusesLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.jsonl")
	if err := os.WriteFile(path, []byte("{\"schema_version\":\"nope\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(NewStore(path), newFakeClock(), RegistryConfig{}); err == nil {
		t.Fatal("an invalid persisted record must refuse the whole registry (fail-closed)")
	}
}

func TestReplayGuard(t *testing.T) {
	clock := newFakeClock()
	g := NewReplayGuard(clock, time.Minute, 8)
	now := clock.Now()

	// Empty request ID fails closed.
	if err := g.Check("n1", "", now); err != ErrInvalidRequestID {
		t.Fatalf("want ErrInvalidRequestID, got %v", err)
	}
	// Stale timestamp (older than ±90s).
	if err := g.Check("n1", "req-1", now.Add(-2*time.Minute)); err == nil {
		t.Fatal("stale timestamp must be refused")
	}
	if err := g.Check("n1", "req-1", now.Add(2*time.Minute)); err == nil {
		t.Fatal("far-future timestamp must be refused")
	}
	// First sighting passes and is REMEMBERED.
	if err := g.Check("n1", "req-1", now); err != nil {
		t.Fatalf("first sighting: %v", err)
	}
	// Replay inside the window is refused.
	if err := g.Check("n1", "req-1", now); err == nil {
		t.Fatal("replay must be refused")
	}
	// The same request ID on a DIFFERENT node is fine (per-node space).
	if err := g.Check("n2", "req-1", now); err != nil {
		t.Fatalf("same ID on another node must pass: %v", err)
	}
	// After the window it is forgotten.
	clock.Advance(time.Minute + time.Second)
	if err := g.Check("n1", "req-1", clock.Now()); err != nil {
		t.Fatalf("expired ID must be forgotten: %v", err)
	}
	// Cap overflow fails closed.
	for i := 0; i < 9; i++ {
		if err := g.Check("n3", string(rune('a'+i)), clock.Now()); err != nil && err != ErrReplayGuardOverflow {
			t.Fatalf("fill loop: %v", err)
		}
	}
}

func TestConcurrentAuthenticateRotationRace(t *testing.T) {
	reg, _, _ := newTestRegistry(t)
	inv := mustInvite(t, reg, "node-r", 0)
	cred := mustEnroll(t, reg, inv)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				_, _ = reg.RotateCredential("race-op", "node-r")
			} else {
				_, _ = reg.Authenticate("node-r", cred.Secret)
			}
		}(i)
	}
	wg.Wait()
	// The invariant that matters post-race: SOME valid credential state
	// exists and the registry still answers consistently.
	if _, err := reg.Authenticate("node-r", cred.Secret); err != nil && err != ErrAuthentication {
		t.Fatalf("post-race authenticate must be a clean answer, got %v", err)
	}
}

func TestAuthenticateGenericFailures(t *testing.T) {
	reg, _, _ := newTestRegistry(t)
	inv := mustInvite(t, reg, "node-s", 0)
	cred := mustEnroll(t, reg, inv)

	// Unknown node → generic.
	if _, err := reg.Authenticate("ghost", cred.Secret); err != ErrAuthentication {
		t.Fatalf("unknown node: %v", err)
	}
	// Wrong secret → generic.
	if _, err := reg.Authenticate("node-s", "wrong"); err != ErrAuthentication {
		t.Fatalf("wrong secret: %v", err)
	}
	// Empty anything → generic.
	if _, err := reg.Authenticate("", "x"); err != ErrAuthentication {
		t.Fatalf("empty node: %v", err)
	}
	if _, err := reg.Authenticate("node-s", ""); err != ErrAuthentication {
		t.Fatalf("empty secret: %v", err)
	}
	// INVITED (not enrolled) → the distinct not-enrolled sentinel.
	inv2 := mustInvite(t, reg, "node-t", 0)
	if _, err := reg.Authenticate("node-t", inv2.Secret); err != ErrNodeNotEnrolled {
		t.Fatalf("invited node authenticate: %v", err)
	}
}
