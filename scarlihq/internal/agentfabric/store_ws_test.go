package agentfabric

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

// BF-2: workspace-scoped commands — the scope is persisted, filtered and
// counted; unscoped commands keep the exact BF-1 behavior.
func TestCommandStoreWorkspaceScopedCommands(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "commands.json")
	s, err := NewCommandStore(path, DefaultMaxCommands)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	base := time.Now().UTC()
	wsA := WorkspaceID("ws-" + strings.Repeat("a", 32))
	wsB := WorkspaceID("ws-" + strings.Repeat("b", 32))

	// Scoped + unscoped submissions.
	if _, err := s.Submit("in a", "coder.agent-one", agentkernel.CapabilityStatusRead, wsA, base); err != nil {
		t.Fatalf("submit a: %v", err)
	}
	if _, err := s.Submit("in b", "coder.agent-one", agentkernel.CapabilityStatusRead, wsB, base.Add(time.Second)); err != nil {
		t.Fatalf("submit b: %v", err)
	}
	if _, err := s.Submit("unscoped", "coder.agent-one", agentkernel.CapabilityStatusRead, "", base.Add(2*time.Second)); err != nil {
		t.Fatalf("submit unscoped: %v", err)
	}
	if _, err := s.Submit("also in a", "coder.agent-two", agentkernel.CapabilityGamingRead, wsA, base.Add(3*time.Second)); err != nil {
		t.Fatalf("submit a2: %v", err)
	}

	// A malformed scope is rejected (defense in depth — the API validates
	// early; the store is the authority).
	if _, err := s.Submit("bad scope", "coder.agent-one", agentkernel.CapabilityStatusRead, WorkspaceID("ws-nope"), base.Add(4*time.Second)); err == nil {
		t.Fatal("submit with a malformed workspace id = nil, want error")
	}

	// ListLast sees everything (the BF-1 surface is unchanged).
	if got := len(s.ListLast(10)); got != 4 {
		t.Errorf("ListLast(10) = %d commands, want 4", got)
	}

	// Scope filters.
	if got := s.ListLastWorkspace(10, wsA); len(got) != 2 {
		t.Errorf("ListLastWorkspace(a) = %d, want 2", len(got))
	}
	if got := s.ListLastWorkspace(10, wsB); len(got) != 1 {
		t.Errorf("ListLastWorkspace(b) = %d, want 1", len(got))
	}
	if got := s.ListLastWorkspace(10, ""); len(got) != 1 {
		t.Errorf("ListLastWorkspace(unscoped) = %d, want 1", len(got))
	}
	// Newest-first inside the scope.
	scoped := s.ListLastWorkspace(10, wsA)
	if scoped[0].Intent != "also in a" || scoped[1].Intent != "in a" {
		t.Errorf("ListLastWorkspace(a) order = [%s, %s], want newest-first [also in a, in a]", scoped[0].Intent, scoped[1].Intent)
	}
	if scoped[0].WorkspaceID != string(wsA) {
		t.Errorf("scoped record carries workspace_id %q, want %q", scoped[0].WorkspaceID, wsA)
	}

	// Counts per workspace.
	counts := s.WorkspaceCounts()
	if counts[string(wsA)] != 2 || counts[string(wsB)] != 1 || counts[""] != 1 {
		t.Errorf("WorkspaceCounts() = %v, want a:2 b:1 unscoped:1", counts)
	}

	// Scoped state counts + totals.
	if got := s.CountWorkspace(wsA); got != 2 {
		t.Errorf("CountWorkspace(a) = %d, want 2", got)
	}
	// Route + decide one scoped command; the scoped counts must reflect it.
	cmds := s.ListLastWorkspace(10, wsA)
	target := cmds[1] // "in a" (oldest)
	if _, err := s.Route(CommandID(target.ID), "aks-"+strings.Repeat("1", 32), base.Add(5*time.Second)); err != nil {
		t.Fatalf("route scoped: %v", err)
	}
	states := s.CountsWorkspace(wsA)
	if states[string(CommandRouted)] != 1 || states[string(CommandSubmitted)] != 1 {
		t.Errorf("CountsWorkspace(a) = %v, want ROUTED:1 SUBMITTED:1", states)
	}
	if unscoped := s.CountsWorkspace(""); unscoped[string(CommandSubmitted)] != 1 {
		t.Errorf("CountsWorkspace(unscoped) = %v, want SUBMITTED:1", unscoped)
	}

	// Reload: the scope survives (the schema stays 1 — the field is purely
	// additive; BF-1 files load and BF-2 files keep the scope).
	s2, err := NewCommandStore(path, DefaultMaxCommands)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := len(s2.ListLastWorkspace(10, wsA)); got != 2 {
		t.Errorf("ListLastWorkspace(a) after reload = %d, want 2", got)
	}
	if got := len(s2.ListLastWorkspace(10, "")); got != 1 {
		t.Errorf("ListLastWorkspace(unscoped) after reload = %d, want 1", got)
	}
	routed := s2.ListLastWorkspace(10, wsA)
	found := false
	for _, c := range routed {
		if c.ID == target.ID && c.State == CommandRouted && c.WorkspaceID == string(wsA) {
			found = true
		}
	}
	if !found {
		t.Error("routed scoped command lost its scope or state across reload")
	}
}

// A BF-1-format commands file (no workspace_id fields at all) must load
// unchanged into the BF-2 store — the additive-field contract.
func TestCommandStoreLoadsBF1FormatFile(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "commands.json")
	bf1 := `{"version":1,"commands":[` +
		`{"id":"cmd-` + strings.Repeat("1", 32) + `","intent":"legacy","agent_id":"coder.agent-one","capability":"status.read","state":"SUBMITTED","submitted_at":"2026-10-24T00:00:00Z","updated_at":"2026-10-24T00:00:00Z"}` +
		`]}`
	if err := os.WriteFile(path, []byte(bf1), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := NewCommandStore(path, DefaultMaxCommands)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("BF-1 file must load unchanged: %v", err)
	}
	if s.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", s.Count())
	}
	cmds := s.ListLast(1)
	if cmds[0].WorkspaceID != "" {
		t.Errorf("legacy command carries workspace_id %q, want empty", cmds[0].WorkspaceID)
	}
}
