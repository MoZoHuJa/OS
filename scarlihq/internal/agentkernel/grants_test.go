package agentkernel

// AK-3 (v19.4.4): persisted admin-owned grant registry tests — load
// validation (fail-closed on every malformed shape), grant/revoke/disable/
// enable lifecycle with persistence round-trips, bounds, owner/principal
// discipline, and policy building.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func grantTestTime() time.Time {
	return time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
}

// grantTestDir returns a TempDir tightened to 0700 — the grants registry
// fail-closes on looser directories (tamper discipline), so tests must set
// up the exact discipline a real /var/lib/scarlix-agentkernel has.
func grantTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newTestRegistry(t *testing.T) *GrantRegistry {
	t.Helper()
	reg, err := NewGrantRegistry(filepath.Join(grantTestDir(t), "grants.json"), []string{"zmor", "system.reservation-governor"}, DefaultMaxGrants, DefaultMaxAgents)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); err != nil {
		t.Fatal(err)
	}
	return reg
}

func mustGrant(t *testing.T, r *GrantRegistry, agent AgentID, cap_ Capability, owner, by string) GrantRecord {
	t.Helper()
	rec, err := r.Grant(agent, cap_, owner, by, grantTestTime())
	if err != nil {
		t.Fatalf("Grant(%s, %s): %v", agent, cap_, err)
	}
	return rec
}

func TestNewGrantRegistry_ConstructionFailClosed(t *testing.T) {
	if _, err := NewGrantRegistry("", []string{"zmor"}, 8, 4); err == nil {
		t.Error("empty path must be a construction error")
	}
	if _, err := NewGrantRegistry("/tmp/g.json", nil, 8, 4); err == nil {
		t.Error("empty principal space must be a construction error")
	}
	if _, err := NewGrantRegistry("/tmp/g.json", []string{"zmor", " "}, 8, 4); err == nil {
		t.Error("empty principal name must be a construction error")
	}
	if _, err := NewGrantRegistry("/tmp/g.json", []string{"zmor", "zmor"}, 8, 4); err == nil {
		t.Error("duplicate principal must be a construction error")
	}
	if _, err := NewGrantRegistry("/tmp/g.json", []string{"zmor"}, 0, 4); err == nil {
		t.Error("maxGrants=0 must be a construction error")
	}
	if _, err := NewGrantRegistry("/tmp/g.json", []string{"zmor"}, 8, 0); err == nil {
		t.Error("maxAgents=0 must be a construction error")
	}
	if _, err := NewGrantRegistry("/tmp/g.json", []string{"zmor"}, 4, 8); err == nil {
		t.Error("maxAgents>maxGrants must be a construction error (nonsensical bounds)")
	}
}

func TestGrantRegistry_LoadInitializesEmptyFile(t *testing.T) {
	path := filepath.Join(grantTestDir(t), "grants.json")
	reg, err := NewGrantRegistry(path, []string{"zmor"}, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("initialized grants file mode = %o, want 0600", info.Mode().Perm())
	}
	a, tot, ag, d := reg.Counts()
	if a != 0 || tot != 0 || ag != 0 || d != 0 {
		t.Errorf("empty registry counts = %d/%d/%d/%d, want all 0", a, tot, ag, d)
	}
	// Reload must be idempotent.
	if err := reg.Load(); err != nil {
		t.Fatalf("reload of empty file: %v", err)
	}
}

func TestGrantRegistry_GrantLifecycleAndPersistence(t *testing.T) {
	path := filepath.Join(grantTestDir(t), "grants.json")
	reg, err := NewGrantRegistry(path, []string{"zmor"}, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); err != nil {
		t.Fatal(err)
	}
	mustGrant(t, reg, "coder.agent-one", CapabilityStatusRead, "zmor", "admin.zmor")
	mustGrant(t, reg, "coder.agent-one", CapabilityComputePlan, "zmor", "admin.zmor")

	caps := reg.GrantsFor("coder.agent-one")
	if len(caps) != 2 || !caps.Has(CapabilityStatusRead) || !caps.Has(CapabilityComputePlan) {
		t.Fatalf("GrantsFor = %v, want status.read + compute.plan", caps)
	}
	owner, ok := reg.AgentOwner("coder.agent-one")
	if !ok || owner != "zmor" {
		t.Fatalf("AgentOwner = %q/%v, want zmor", owner, ok)
	}

	// Round-trip: a fresh registry over the same file sees the same state.
	reg2, err := NewGrantRegistry(path, []string{"zmor"}, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := reg2.GrantsFor("coder.agent-one"); len(got) != 2 {
		t.Fatalf("reload lost grants: %v", got)
	}
	if o, ok := reg2.AgentOwner("coder.agent-one"); !ok || o != "zmor" {
		t.Fatalf("reload lost owner: %q/%v", o, ok)
	}

	// Revoke → capability leaves the active set, tombstone persists.
	if _, err := reg.Revoke("coder.agent-one", CapabilityStatusRead, "admin.zmor", grantTestTime().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := reg.GrantsFor("coder.agent-one"); len(got) != 1 || !got.Has(CapabilityComputePlan) {
		t.Fatalf("post-revoke GrantsFor = %v, want only compute.plan", got)
	}
	grants := reg.ListGrants()
	if len(grants) != 2 {
		t.Fatalf("ListGrants after revoke = %d records, want 2 (tombstone stays)", len(grants))
	}
	for _, g := range grants {
		if g.AgentID == "coder.agent-one" && g.Capability == CapabilityStatusRead {
			if g.Active() {
				t.Error("revoked grant must not be Active()")
			}
			if g.RevokedBy != "admin.zmor" || g.RevokedAt == nil {
				t.Errorf("tombstone missing revoker data: %+v", g)
			}
		}
	}

	// Re-grant after revoke appends a NEW active record; tombstone survives.
	mustGrant(t, reg, "coder.agent-one", CapabilityStatusRead, "zmor", "admin.zmor")
	if got := reg.GrantsFor("coder.agent-one"); len(got) != 2 {
		t.Fatalf("post-regrant GrantsFor = %v, want 2", got)
	}
	if len(reg.ListGrants()) != 3 {
		t.Fatalf("ListGrants after regrant = %d, want 3 (audit history is append-only)", len(reg.ListGrants()))
	}

	// Round-trip again: revoke + re-grant history survives reload.
	reg3, err := NewGrantRegistry(path, []string{"zmor"}, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg3.Load(); err != nil {
		t.Fatal(err)
	}
	if len(reg3.ListGrants()) != 3 {
		t.Fatalf("reload lost history: %d records, want 3", len(reg3.ListGrants()))
	}
	if got := reg3.GrantsFor("coder.agent-one"); len(got) != 2 {
		t.Fatalf("reload lost re-granted capability: %v", got)
	}
}

func TestGrantRegistry_GrantValidation(t *testing.T) {
	reg := newTestRegistry(t)
	if _, err := reg.Grant("bad id", CapabilityStatusRead, "zmor", "admin", grantTestTime()); err == nil {
		t.Error("invalid agent id must be rejected")
	}
	if _, err := reg.Grant("coder.agent-one", Capability("shell.exec"), "zmor", "admin", grantTestTime()); err == nil {
		t.Error("unknown capability must be rejected (closed enumeration)")
	}
	if _, err := reg.Grant("coder.agent-one", CapabilityStatusRead, "", "admin", grantTestTime()); err == nil {
		t.Error("empty owner must be rejected")
	}
	if _, err := reg.Grant("coder.agent-one", CapabilityStatusRead, "stranger", "admin", grantTestTime()); !errors.Is(err, ErrUnknownPrincipal) {
		t.Errorf("owner outside principal space must be ErrUnknownPrincipal, got %v", err)
	}
	if _, err := reg.Grant("coder.agent-one", CapabilityStatusRead, "zmor", "", grantTestTime()); err == nil {
		t.Error("empty granted_by must be rejected (admin-owned act)")
	}
	if _, err := reg.Grant("coder.agent-one", CapabilityStatusRead, "zmor", "admin", time.Time{}); err == nil {
		t.Error("zero timestamp must be rejected")
	}
	// First valid grant declares the owner.
	mustGrant(t, reg, "coder.agent-one", CapabilityStatusRead, "zmor", "admin.zmor")
	// Same agent, different owner → mismatch.
	if _, err := reg.Grant("coder.agent-one", CapabilityGamingRead, "system.reservation-governor", "admin.zmor", grantTestTime()); !errors.Is(err, ErrAgentOwnerMismatch) {
		t.Errorf("owner mismatch must be ErrAgentOwnerMismatch, got %v", err)
	}
	// Duplicate ACTIVE grant.
	if _, err := reg.Grant("coder.agent-one", CapabilityStatusRead, "zmor", "admin.zmor", grantTestTime()); !errors.Is(err, ErrDuplicateGrant) {
		t.Errorf("duplicate active grant must be ErrDuplicateGrant, got %v", err)
	}
}

func TestGrantRegistry_RevokeValidation(t *testing.T) {
	reg := newTestRegistry(t)
	mustGrant(t, reg, "coder.agent-one", CapabilityStatusRead, "zmor", "admin.zmor")
	if _, err := reg.Revoke("coder.agent-one", CapabilityStatusRead, "", grantTestTime()); err == nil {
		t.Error("empty revoked_by must be rejected")
	}
	if _, err := reg.Revoke("coder.agent-two", CapabilityStatusRead, "admin", grantTestTime()); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("unknown grant must be ErrGrantNotFound, got %v", err)
	}
	if _, err := reg.Revoke("coder.agent-one", CapabilityGamingRead, "admin", grantTestTime()); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("unknown capability pair must be ErrGrantNotFound, got %v", err)
	}
	if _, err := reg.Revoke("coder.agent-one", CapabilityStatusRead, "admin.zmor", grantTestTime().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Revoke("coder.agent-one", CapabilityStatusRead, "admin.zmor", grantTestTime().Add(2*time.Minute)); !errors.Is(err, ErrGrantInactive) {
		t.Errorf("double revoke must be ErrGrantInactive, got %v", err)
	}
}

func TestGrantRegistry_DisableEnablePolicy(t *testing.T) {
	reg := newTestRegistry(t)
	mustGrant(t, reg, "coder.agent-one", CapabilityStatusRead, "zmor", "admin.zmor")

	// Before disable: policy allows (session mock not needed — Disabled map only).
	pol := reg.Policy()
	if pol.Disabled[CapabilityStatusRead] {
		t.Fatal("capability must not be disabled before Disable()")
	}
	if _, err := reg.Disable(Capability("shell.exec"), "admin", "incident", grantTestTime()); err == nil {
		t.Error("disable of unknown capability must be rejected")
	}
	if _, err := reg.Disable(CapabilityStatusRead, "", "incident", grantTestTime()); err == nil {
		t.Error("empty disabled_by must be rejected")
	}
	if _, err := reg.Disable(CapabilityStatusRead, "admin.zmor", "incident: probe noise", grantTestTime()); err != nil {
		t.Fatal(err)
	}
	pol = reg.Policy()
	if !pol.Disabled[CapabilityStatusRead] {
		t.Fatal("capability must be disabled after Disable()")
	}
	if len(reg.ListDisables()) != 1 || reg.ListDisables()[0].Reason != "incident: probe noise" {
		t.Fatalf("ListDisables = %+v", reg.ListDisables())
	}
	// Duplicate disable.
	if _, err := reg.Disable(CapabilityStatusRead, "admin.zmor", "again", grantTestTime()); err == nil {
		t.Error("duplicate disable must be rejected")
	}
	// Enable validation + effect.
	if err := reg.Enable(CapabilityGamingRead, "admin.zmor", grantTestTime()); err == nil {
		t.Error("enable of a not-disabled capability must be rejected")
	}
	if err := reg.Enable(CapabilityStatusRead, "", grantTestTime()); err == nil {
		t.Error("empty enabled_by must be rejected")
	}
	if err := reg.Enable(CapabilityStatusRead, "admin.zmor", grantTestTime()); err != nil {
		t.Fatal(err)
	}
	pol = reg.Policy()
	if pol.Disabled[CapabilityStatusRead] {
		t.Fatal("capability must be enabled after Enable()")
	}

	// Persistence round-trip of the disable switch.
	if _, err := reg.Disable(CapabilityComputeReserve, "admin.zmor", "capacity freeze", grantTestTime()); err != nil {
		t.Fatal(err)
	}
	reg2, err := NewGrantRegistry(reg.Path(), []string{"zmor"}, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg2.Load(); err != nil {
		t.Fatal(err)
	}
	pol2 := reg2.Policy()
	if !pol2.Disabled[CapabilityComputeReserve] || pol2.Disabled[CapabilityStatusRead] {
		t.Fatalf("disable switches did not round-trip: %+v", pol2.Disabled)
	}
}

func TestGrantRegistry_LoadFailClosedOnMalformedRecords(t *testing.T) {
	base := func() string {
		return `{"version":1,"grants":[{"agent_id":"coder.agent-one","capability":"status.read","owner":"zmor","granted_by":"admin.zmor","granted_at":"2026-10-24T10:00:00Z"}],"disables":[]}`
	}
	cases := []struct {
		name string
		body string
	}{
		{"unknown capability", `{"version":1,"grants":[{"agent_id":"a.b","capability":"shell.exec","owner":"zmor","granted_by":"admin","granted_at":"2026-10-24T10:00:00Z"}],"disables":[]}`},
		{"owner outside principal space", `{"version":1,"grants":[{"agent_id":"a.b","capability":"status.read","owner":"stranger","granted_by":"admin","granted_at":"2026-10-24T10:00:00Z"}],"disables":[]}`},
		{"empty granted_by", `{"version":1,"grants":[{"agent_id":"a.b","capability":"status.read","owner":"zmor","granted_by":"","granted_at":"2026-10-24T10:00:00Z"}],"disables":[]}`},
		{"zero granted_at", `{"version":1,"grants":[{"agent_id":"a.b","capability":"status.read","owner":"zmor","granted_by":"admin","granted_at":"0001-01-01T00:00:00Z"}],"disables":[]}`},
		{"revoked without revoker", `{"version":1,"grants":[{"agent_id":"a.b","capability":"status.read","owner":"zmor","granted_by":"admin","granted_at":"2026-10-24T10:00:00Z","revoked_at":"2026-10-25T10:00:00Z"}],"disables":[]}`},
		{"duplicate active grant", `{"version":1,"grants":[{"agent_id":"a.b","capability":"status.read","owner":"zmor","granted_by":"admin","granted_at":"2026-10-24T10:00:00Z"},{"agent_id":"a.b","capability":"status.read","owner":"zmor","granted_by":"admin","granted_at":"2026-10-24T11:00:00Z"}],"disables":[]}`},
		{"owner mismatch within agent", `{"version":1,"grants":[{"agent_id":"a.b","capability":"status.read","owner":"zmor","granted_by":"admin","granted_at":"2026-10-24T10:00:00Z"},{"agent_id":"a.b","capability":"gaming.read","owner":"other","granted_by":"admin","granted_at":"2026-10-24T11:00:00Z"}],"disables":[]}`},
		{"bad schema version", `{"version":2,"grants":[],"disables":[]}`},
		{"not json", `nope`},
		{"unknown disable capability", `{"version":1,"grants":[],"disables":[{"capability":"shell.exec","disabled_by":"admin","disabled_at":"2026-10-24T10:00:00Z","reason":"x"}]}`},
		{"disable without admin", `{"version":1,"grants":[],"disables":[{"capability":"status.read","disabled_by":"","disabled_at":"2026-10-24T10:00:00Z","reason":"x"}]}`},
		{"duplicate disable", `{"version":1,"grants":[],"disables":[{"capability":"status.read","disabled_by":"a","disabled_at":"2026-10-24T10:00:00Z","reason":"x"},{"capability":"status.read","disabled_by":"b","disabled_at":"2026-10-24T11:00:00Z","reason":"y"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(grantTestDir(t), "grants.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			reg, err := NewGrantRegistry(path, []string{"zmor"}, 8, 4)
			if err != nil {
				t.Fatal(err)
			}
			if err := reg.Load(); err == nil {
				t.Fatalf("Load must fail-closed on: %s", tc.name)
			}
		})
	}
	// Sanity: the valid base file loads.
	path := filepath.Join(grantTestDir(t), "grants.json")
	if err := os.WriteFile(path, []byte(base()), 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := NewGrantRegistry(path, []string{"zmor"}, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); err != nil {
		t.Fatalf("valid file must load: %v", err)
	}
}

func TestGrantRegistry_LoadFailClosedOnPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0640, 0644, 0604, 0666} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(grantTestDir(t), "grants.json")
			if err := os.WriteFile(path, []byte(`{"version":1,"grants":[],"disables":[]}`), mode); err != nil {
				t.Fatal(err)
			}
			reg, err := NewGrantRegistry(path, []string{"zmor"}, 8, 4)
			if err != nil {
				t.Fatal(err)
			}
			if err := reg.Load(); !errors.Is(err, ErrGrantFilePermissions) {
				t.Fatalf("mode %o must fail-closed with ErrGrantFilePermissions, got %v", mode, err)
			}
		})
	}
	// Loose directory (0777) must also fail-closed even with a 0600 file.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	// t.TempDir auto-cleans even after chmod; restore for the cleanup path.
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	path := filepath.Join(dir, "grants.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"grants":[],"disables":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := NewGrantRegistry(path, []string{"zmor"}, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); !errors.Is(err, ErrGrantFilePermissions) {
		t.Fatalf("loose directory must fail-closed with ErrGrantFilePermissions, got %v", err)
	}
}

func TestGrantRegistry_Bounds(t *testing.T) {
	// Agent bound: maxAgents=2 with plenty of grant headroom (8 records).
	reg, err := NewGrantRegistry(filepath.Join(grantTestDir(t), "grants.json"), []string{"zmor"}, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); err != nil {
		t.Fatal(err)
	}
	mustGrant(t, reg, "coder.a", CapabilityStatusRead, "zmor", "admin")
	mustGrant(t, reg, "coder.a", CapabilityGamingRead, "zmor", "admin")
	mustGrant(t, reg, "coder.b", CapabilityStatusRead, "zmor", "admin")
	if _, err := reg.Grant("coder.c", CapabilityStatusRead, "zmor", "admin", grantTestTime()); !errors.Is(err, ErrAgentLimit) {
		t.Errorf("3rd agent must hit ErrAgentLimit, got %v", err)
	}
	// Revocation frees nothing — the agent tombstones still count (honest bound).
	if _, err := reg.Revoke("coder.b", CapabilityStatusRead, "admin", grantTestTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Grant("coder.c", CapabilityStatusRead, "zmor", "admin", grantTestTime()); !errors.Is(err, ErrAgentLimit) {
		t.Errorf("revoked agent still counts — ErrAgentLimit expected, got %v", err)
	}

	// Grant-record bound (maxGrants=3): tombstones count, re-grant appends.
	reg2, err := NewGrantRegistry(filepath.Join(grantTestDir(t), "grants.json"), []string{"zmor"}, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg2.Load(); err != nil {
		t.Fatal(err)
	}
	mustGrant(t, reg2, "coder.x", CapabilityStatusRead, "zmor", "admin")
	mustGrant(t, reg2, "coder.x", CapabilityGamingRead, "zmor", "admin")
	mustGrant(t, reg2, "coder.x", CapabilityModeRequest, "zmor", "admin")
	if _, err := reg2.Grant("coder.x", CapabilityComputePlan, "zmor", "admin", grantTestTime()); !errors.Is(err, ErrGrantLimit) {
		t.Errorf("4th record must hit ErrGrantLimit, got %v", err)
	}
	if _, err := reg2.Revoke("coder.x", CapabilityModeRequest, "admin", grantTestTime()); err != nil {
		t.Fatal(err)
	}
	// Re-grant of the revoked pair appends a NEW record → 4th record again.
	if _, err := reg2.Grant("coder.x", CapabilityModeRequest, "zmor", "admin", grantTestTime()); !errors.Is(err, ErrGrantLimit) {
		t.Errorf("re-grant must append (tombstones count) — ErrGrantLimit expected, got %v", err)
	}

	// Load-side bound: a file with more records than the bound fails.
	path := filepath.Join(grantTestDir(t), "grants.json")
	big := `{"version":1,"grants":[` +
		`{"agent_id":"a.b","capability":"status.read","owner":"zmor","granted_by":"admin","granted_at":"2026-10-24T10:00:00Z"},` +
		`{"agent_id":"a.b","capability":"gaming.read","owner":"zmor","granted_by":"admin","granted_at":"2026-10-24T10:01:00Z"},` +
		`{"agent_id":"c.d","capability":"status.read","owner":"zmor","granted_by":"admin","granted_at":"2026-10-24T10:02:00Z"}],"disables":[]}`
	if err := os.WriteFile(path, []byte(big), 0600); err != nil {
		t.Fatal(err)
	}
	reg3, err := NewGrantRegistry(path, []string{"zmor"}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg3.Load(); !errors.Is(err, ErrGrantLimit) {
		t.Errorf("load over grants bound must fail with ErrGrantLimit, got %v", err)
	}
}

func TestGrantRegistry_ListDeterministic(t *testing.T) {
	reg := newTestRegistry(t)
	mustGrant(t, reg, "coder.b-two", CapabilityGamingRead, "zmor", "admin")
	mustGrant(t, reg, "coder.a-one", CapabilityStatusRead, "zmor", "admin")
	mustGrant(t, reg, "coder.a-one", CapabilityComputePlan, "zmor", "admin")
	list := reg.ListGrants()
	want := []AgentID{"coder.a-one", "coder.a-one", "coder.b-two"}
	for i, g := range list {
		if g.AgentID != want[i] {
			t.Fatalf("ListGrants order = %v, want %v", list, want)
		}
	}
	// 20 consecutive lists are identical (no map-order leak — the
	// check_15/check_18 determinism discipline).
	var prev string
	for i := 0; i < 20; i++ {
		l := reg.ListGrants()
		s := ""
		for _, g := range l {
			s += string(g.AgentID) + "|" + string(g.Capability) + ";"
		}
		if i > 0 && s != prev {
			t.Fatalf("ListGrants is not deterministic across calls: %q vs %q", prev, s)
		}
		prev = s
	}
}

func TestGrantRegistry_ReturnedRecordsAreCopies(t *testing.T) {
	reg := newTestRegistry(t)
	rec := mustGrant(t, reg, "coder.agent-one", CapabilityStatusRead, "zmor", "admin.zmor")
	// Mutating a returned record must not affect the registry state.
	rec.Owner = "mutated"
	if o, _ := reg.AgentOwner("coder.agent-one"); o != "zmor" {
		t.Fatalf("returned record is not a copy — owner mutated to %q", o)
	}
	list := reg.ListGrants()
	list[0].GrantedBy = "forged"
	again := reg.ListGrants()
	if again[0].GrantedBy != "admin.zmor" {
		t.Fatal("ListGrants must return copies — mutation leaked")
	}
}

func TestGrantRegistry_DisableTakesEffectOnNextEvaluation(t *testing.T) {
	// End-to-end (kernel level): the registry policy + grants drive the
	// deterministic kernel decision, and a runtime toggle flips it without
	// a restart.
	reg := newTestRegistry(t)
	mustGrant(t, reg, "coder.agent-one", CapabilityStatusRead, "zmor", "admin.zmor")

	agent := AgentIdentity{ID: "coder.agent-one", Owner: "zmor", CreatedAt: grantTestTime()}
	sess := Session{ID: "aks-test", AgentID: "coder.agent-one", State: SessionActive, CreatedAt: grantTestTime(), UpdatedAt: grantTestTime()}

	pol := reg.Policy()
	d := pol.Evaluate(agent, &sess, reg.GrantsFor("coder.agent-one"), CapabilityStatusRead)
	if !d.Allow {
		t.Fatalf("granted capability must be allowed, got %q", d.Reason)
	}
	if _, err := reg.Disable(CapabilityStatusRead, "admin.zmor", "incident drill", grantTestTime()); err != nil {
		t.Fatal(err)
	}
	pol = reg.Policy() // next evaluation picks the toggle up
	d = pol.Evaluate(agent, &sess, reg.GrantsFor("coder.agent-one"), CapabilityStatusRead)
	if d.Allow {
		t.Fatal("disabled capability must be denied")
	}
	if d.Reason == "" || !strings.Contains(d.Reason, "rule/capability-disabled") {
		t.Fatalf("denial must cite rule/capability-disabled, got %q", d.Reason)
	}
}
