package agentfabric

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestWorkspaceStore(t *testing.T) (*WorkspaceStore, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "workspaces.json")
	s, err := NewWorkspaceStore(path, DefaultMaxWorkspaces)
	if err != nil {
		t.Fatalf("NewWorkspaceStore: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s, path
}

func TestWorkspaceStoreCreateValidation(t *testing.T) {
	s, _ := newTestWorkspaceStore(t)
	now := time.Now().UTC()
	cases := []struct {
		name, desc string
		wantErr    bool
	}{
		{"alpha", "", false},
		{"padded name", "padded desc", false},
		{"", "", true},
		{"   ", "", true},
		{strings.Repeat("x", 65), "", true},
		{"ctrl\nname", "", true},
		{"ok", strings.Repeat("d", 257), true},
		{"ok2", "bad\tdesc", true},
		{"ok3", "", false},
	}
	for _, tc := range cases {
		if _, err := s.Create(tc.name, tc.desc, now); tc.wantErr != (err != nil) {
			t.Errorf("Create(%q, %q) err=%v, wantErr=%v", tc.name, tc.desc, err, tc.wantErr)
		}
	}
	if _, err := s.Create("no time", "", time.Time{}); err == nil {
		t.Error("Create with zero time = nil, want error")
	}
}

func TestWorkspaceStoreCreateRNGFailClosed(t *testing.T) {
	s, _ := newTestWorkspaceStore(t)
	saved := rngWorkspaceID
	rngWorkspaceID = func() (string, error) { return "", ErrWorkspaceRNG }
	defer func() { rngWorkspaceID = saved }()
	if _, err := s.Create("alpha", "", time.Now().UTC()); !errors.Is(err, ErrWorkspaceRNG) {
		t.Fatalf("Create with RNG failure = %v, want ErrWorkspaceRNG", err)
	}
	if s.Count() != 0 {
		t.Errorf("Count() after RNG failure = %d, want 0 (nothing persisted)", s.Count())
	}
}

func TestWorkspaceStoreLifecycle(t *testing.T) {
	s, path := newTestWorkspaceStore(t)
	base := time.Now().UTC()

	ws, err := s.Create("relwork", "coding context", base)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ws.State != WorkspaceActive {
		t.Errorf("new workspace state = %q, want ACTIVE", ws.State)
	}
	if len(ws.Sessions) != 0 {
		t.Errorf("new workspace carries %d sessions, want 0", len(ws.Sessions))
	}
	if ws.Name != "relwork" || ws.Description != "coding context" {
		t.Errorf("name/description not persisted: %q / %q", ws.Name, ws.Description)
	}

	sid := validSessionID()
	joined, err := s.JoinSession(WorkspaceID(ws.ID), sid, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("JoinSession: %v", err)
	}
	if !joined.IsMember(sid) || len(joined.Sessions) != 1 {
		t.Fatalf("membership not recorded: %v", joined.Sessions)
	}

	// Duplicate join is a state conflict.
	if _, err := s.JoinSession(WorkspaceID(ws.ID), sid, base.Add(2*time.Minute)); !errors.Is(err, ErrSessionAlreadyMember) {
		t.Fatalf("duplicate join = %v, want ErrSessionAlreadyMember", err)
	}

	// A second workspace cannot claim the same session.
	other, err := s.Create("beta", "", base.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("Create beta: %v", err)
	}
	if _, err := s.JoinSession(WorkspaceID(other.ID), sid, base.Add(4*time.Minute)); !errors.Is(err, ErrSessionClaimed) {
		t.Fatalf("cross-claim join = %v, want ErrSessionClaimed", err)
	}

	// Leave, then the second workspace CAN claim it.
	left, err := s.LeaveSession(WorkspaceID(ws.ID), sid, base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("LeaveSession: %v", err)
	}
	if left.IsMember(sid) {
		t.Error("session still a member after leave")
	}
	if _, err := s.LeaveSession(WorkspaceID(ws.ID), sid, base.Add(6*time.Minute)); !errors.Is(err, ErrSessionNotMember) {
		t.Fatalf("leave of a non-member = %v, want ErrSessionNotMember", err)
	}
	if _, err := s.JoinSession(WorkspaceID(other.ID), sid, base.Add(7*time.Minute)); err != nil {
		t.Fatalf("join after leave in another workspace: %v", err)
	}

	// Archive: terminal.
	archived, err := s.Archive(WorkspaceID(other.ID), base.Add(8*time.Minute))
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if archived.State != WorkspaceArchived || archived.ArchivedAt == nil {
		t.Fatalf("archive not recorded: %+v", archived)
	}
	if _, err := s.Archive(WorkspaceID(other.ID), base.Add(9*time.Minute)); !errors.Is(err, ErrWorkspaceState) {
		t.Fatalf("double archive = %v, want ErrWorkspaceState", err)
	}
	if _, err := s.JoinSession(WorkspaceID(other.ID), validSessionID(), base.Add(10*time.Minute)); !errors.Is(err, ErrWorkspaceState) {
		t.Fatalf("join into archived = %v, want ErrWorkspaceState", err)
	}
	if _, err := s.LeaveSession(WorkspaceID(other.ID), sid, base.Add(11*time.Minute)); !errors.Is(err, ErrWorkspaceState) {
		t.Fatalf("leave from archived = %v, want ErrWorkspaceState", err)
	}

	// Unknown workspace.
	if _, err := s.Archive(WorkspaceID("ws-"+strings.Repeat("e", 32)), base); !errors.Is(err, ErrUnknownWorkspace) {
		t.Fatalf("archive unknown = %v, want ErrUnknownWorkspace", err)
	}

	// SessionWorkspace lookup.
	found, ok := s.SessionWorkspace(sid)
	if !ok || found.ID != other.ID {
		t.Fatalf("SessionWorkspace(%s) = %v,%v — want the archived other workspace (lookup survives archiving)", sid, found.ID, ok)
	}
	if _, ok := s.SessionWorkspace("aks-" + strings.Repeat("c", 32)); ok {
		t.Error("SessionWorkspace(unknown) = true, want false")
	}

	// Persistence round-trip: reload and verify everything survived.
	s2, err := NewWorkspaceStore(path, DefaultMaxWorkspaces)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if s2.Count() != 2 {
		t.Errorf("Count after reload = %d, want 2", s2.Count())
	}
	got, ok := s2.Get(WorkspaceID(other.ID))
	if !ok {
		t.Fatal("archived workspace missing after reload")
	}
	if got.State != WorkspaceArchived || !got.IsMember(sid) {
		t.Errorf("archived membership/state lost after reload: %+v", got)
	}
}

func TestWorkspaceStoreMembershipBound(t *testing.T) {
	s, _ := newTestWorkspaceStore(t)
	now := time.Now().UTC()
	ws, err := s.Create("full", "", now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < MaxSessionsPerWorkspace; i++ {
		sid := fmt.Sprintf("aks-%032x", i+1) // unique, valid hex shape
		if _, err := s.JoinSession(WorkspaceID(ws.ID), sid, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("JoinSession %d: %v", i, err)
		}
	}
	if _, err := s.JoinSession(WorkspaceID(ws.ID), fmt.Sprintf("aks-%032x", MaxSessionsPerWorkspace+1), now.Add(time.Minute)); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("join over bound = %v, want ErrSessionLimit", err)
	}
}

func TestWorkspaceStoreCapacityBound(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	s, err := NewWorkspaceStore(filepath.Join(dir, "workspaces.json"), 2)
	if err != nil {
		t.Fatalf("NewWorkspaceStore: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		if _, err := s.Create(string(rune('a'+i)), "", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	if _, err := s.Create("third", "", now.Add(10*time.Second)); !errors.Is(err, ErrWorkspaceLimit) {
		t.Fatalf("Create over bound = %v, want ErrWorkspaceLimit", err)
	}
	if s.Count() != 2 {
		t.Errorf("Count() = %d, want 2", s.Count())
	}
}

func TestWorkspaceStoreJoinShapeCheck(t *testing.T) {
	s, _ := newTestWorkspaceStore(t)
	now := time.Now().UTC()
	ws, _ := s.Create("x", "", now)
	if _, err := s.JoinSession(WorkspaceID(ws.ID), "not-a-session", now); !errors.Is(err, ErrSessionShape) {
		t.Fatalf("join with a malformed session id = %v, want ErrSessionShape", err)
	}
	if _, err := s.JoinSession(WorkspaceID(ws.ID), "", now); !errors.Is(err, ErrSessionShape) {
		t.Fatalf("join with an empty session id = %v, want ErrSessionShape", err)
	}
}

func TestWorkspaceStoreLoadFailClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "workspaces.json")

	valid := `{"version":1,"workspaces":[{"id":"` + validWorkspaceID() + `","name":"a","state":"ACTIVE","sessions":[],"created_at":"2026-10-24T00:00:00Z","updated_at":"2026-10-24T00:00:00Z"}]}`

	cases := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{"valid file loads", valid, 0o600},
		{"malformed json", `{"version":1,`, 0o600},
		{"wrong schema version", `{"version":2,"workspaces":[]}`, 0o600},
		{"bad record shape", `{"version":1,"workspaces":[{"id":"ws-nope","name":"a","state":"ACTIVE","sessions":[],"created_at":"2026-10-24T00:00:00Z","updated_at":"2026-10-24T00:00:00Z"}]}`, 0o600},
		{"unknown state", `{"version":1,"workspaces":[{"id":"` + validWorkspaceID() + `","name":"a","state":"DRAFT","sessions":[],"created_at":"2026-10-24T00:00:00Z","updated_at":"2026-10-24T00:00:00Z"}]}`, 0o600},
		{"duplicate workspace id", `{"version":1,"workspaces":[{"id":"` + validWorkspaceID() + `","name":"a","state":"ACTIVE","sessions":[],"created_at":"2026-10-24T00:00:00Z","updated_at":"2026-10-24T00:00:00Z"},{"id":"` + validWorkspaceID() + `","name":"b","state":"ACTIVE","sessions":[],"created_at":"2026-10-24T00:01:00Z","updated_at":"2026-10-24T00:01:00Z"}]}`, 0o600},
		{"session claimed twice", `{"version":1,"workspaces":[{"id":"` + validWorkspaceID() + `","name":"a","state":"ACTIVE","sessions":["` + validSessionID() + `"],"created_at":"2026-10-24T00:00:00Z","updated_at":"2026-10-24T00:00:00Z"},{"id":"` + "ws-" + strings.Repeat("f", 32) + `","name":"b","state":"ACTIVE","sessions":["` + validSessionID() + `"],"created_at":"2026-10-24T00:01:00Z","updated_at":"2026-10-24T00:01:00Z"}]}`, 0o600},
		{"loose file 0640", valid, 0o640},
		{"loose file 0644", valid, 0o644},
		{"loose file 0666", valid, 0o666},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatalf("chmod fixture: %v", err)
			}
			s, err := NewWorkspaceStore(path, DefaultMaxWorkspaces)
			if err != nil {
				t.Fatalf("construct: %v", err)
			}
			loadErr := s.Load()
			if tc.name == "valid file loads" {
				if loadErr != nil {
					t.Fatalf("Load(valid) = %v, want nil", loadErr)
				}
				return
			}
			if loadErr == nil {
				t.Fatalf("Load(%s) = nil, want fail-closed error", tc.name)
			}
			if tc.mode != 0o600 && !errors.Is(loadErr, ErrWorkspaceFilePermissions) {
				t.Errorf("Load(%s) = %v, want ErrWorkspaceFilePermissions", tc.name, loadErr)
			}
		})
	}
}

func TestWorkspaceStoreLoadLooseDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod loose: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "workspaces.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"workspaces":[]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := NewWorkspaceStore(path, DefaultMaxWorkspaces)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if err := s.Load(); !errors.Is(err, ErrWorkspaceFilePermissions) {
		t.Fatalf("Load with a 0755 directory = %v, want ErrWorkspaceFilePermissions", err)
	}
}

func TestWorkspaceStoreLoadOverBound(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "workspaces.json")
	var recs []map[string]any
	for i := 0; i < 3; i++ {
		id := "ws-" + strings.Repeat(string(rune('a'+i)), 32)
		recs = append(recs, map[string]any{
			"id": id, "name": string(rune('a' + i)), "state": "ACTIVE",
			"sessions":   []string{},
			"created_at": "2026-10-24T00:00:00Z", "updated_at": "2026-10-24T00:00:00Z",
		})
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "workspaces": recs})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := NewWorkspaceStore(path, 2)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if err := s.Load(); err == nil {
		t.Fatal("Load over bound = nil, want error")
	}
}

func TestWorkspaceStorePersistFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "workspaces.json")
	s, err := NewWorkspaceStore(path, DefaultMaxWorkspaces)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	ws, err := s.Create("alpha", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Make the directory read-only: persist will fail (temp file creation).
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod ro: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := s.JoinSession(WorkspaceID(ws.ID), validSessionID(), time.Now().UTC()); err == nil {
		t.Fatal("JoinSession with a read-only dir = nil, want persist failure")
	}
	// Rollback: the membership mutation never happened in memory.
	got, ok := s.Get(WorkspaceID(ws.ID))
	if !ok {
		t.Fatal("workspace vanished after a failed persist")
	}
	if len(got.Sessions) != 0 {
		t.Errorf("in-memory state rolled forward despite persist failure: %v", got.Sessions)
	}
	// ...and on disk (reload into a fresh store).
	_ = os.Chmod(dir, 0o700)
	s2, err := NewWorkspaceStore(path, DefaultMaxWorkspaces)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloaded, _ := s2.Get(WorkspaceID(ws.ID))
	if len(reloaded.Sessions) != 0 {
		t.Errorf("disk state rolled forward despite persist failure: %v", reloaded.Sessions)
	}
}

func TestWorkspaceStoreGetReturnsCopies(t *testing.T) {
	s, _ := newTestWorkspaceStore(t)
	now := time.Now().UTC()
	ws, err := s.Create("alpha", "", now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sid := validSessionID()
	if _, err := s.JoinSession(WorkspaceID(ws.ID), sid, now); err != nil {
		t.Fatalf("JoinSession: %v", err)
	}
	got, _ := s.Get(WorkspaceID(ws.ID))
	got.Sessions[0] = "aks-" + strings.Repeat("z", 32)
	again, _ := s.Get(WorkspaceID(ws.ID))
	if again.Sessions[0] != sid {
		t.Error("Get() returned a record sharing the Sessions slice with store state")
	}
}

func TestWorkspaceStoreListOrderAndCopies(t *testing.T) {
	s, _ := newTestWorkspaceStore(t)
	now := time.Now().UTC()
	var names []string
	for i := 0; i < 3; i++ {
		name := string(rune('a' + i))
		if _, err := s.Create(name, "", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		names = append(names, name)
	}
	list := s.List()
	if len(list) != 3 {
		t.Fatalf("List() length = %d, want 3", len(list))
	}
	for i, rec := range list {
		if rec.Name != names[i] {
			t.Errorf("List()[%d].Name = %q, want %q (creation order)", i, rec.Name, names[i])
		}
	}
	// Mutation through the list must not reach the store.
	list[0].Name = "mutated"
	again := s.List()
	if again[0].Name == "mutated" {
		t.Error("List() shares records with store state")
	}
}

func TestWorkspaceStoreConstructionErrors(t *testing.T) {
	if _, err := NewWorkspaceStore("", DefaultMaxWorkspaces); err == nil {
		t.Error("NewWorkspaceStore(\"\") = nil, want error")
	}
	if _, err := NewWorkspaceStore("/tmp/x.json", 0); err == nil {
		t.Error("NewWorkspaceStore(0 bound) = nil, want error")
	}
	if _, err := NewWorkspaceStore("/tmp/x.json", -1); err == nil {
		t.Error("NewWorkspaceStore(-1 bound) = nil, want error")
	}
}

func TestWorkspaceStoreFirstRunInitializesFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "workspaces.json")
	s, err := NewWorkspaceStore(path, DefaultMaxWorkspaces)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load first run: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not created on first run: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("first-run file mode = %o, want 0600", info.Mode().Perm())
	}
	// The initialized file must load back.
	s2, err := NewWorkspaceStore(path, DefaultMaxWorkspaces)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("reload initialized file: %v", err)
	}
}
