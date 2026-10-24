package agentfabric

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

// fabricTestDir returns a 0700 temp dir (t.TempDir may be looser — the
// store fail-closes on loose directories by design).
func fabricTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // restore for cleanup
	return dir
}

func newTestStore(t *testing.T, dir string, maxCommands int) *CommandStore {
	t.Helper()
	s, err := NewCommandStore(filepath.Join(dir, "commands.json"), maxCommands)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestNewCommandStoreValidation(t *testing.T) {
	if _, err := NewCommandStore("", 10); err == nil {
		t.Error("empty path accepted, want construction error")
	}
	if _, err := NewCommandStore("/tmp/x.json", 0); err == nil {
		t.Error("maxCommands=0 accepted, want construction error")
	}
	if _, err := NewCommandStore("/tmp/x.json", -1); err == nil {
		t.Error("maxCommands=-1 accepted, want construction error")
	}
}

func TestLoadInitializesEmptyFile(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "commands.json")
	s, err := NewCommandStore(path, 16)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat commands file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("commands file mode = %o, want 0600", info.Mode().Perm())
	}
	if s.Count() != 0 {
		t.Errorf("Count() = %d, want 0", s.Count())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read commands file: %v", err)
	}
	var f commandsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("initialized file is not valid JSON: %v", err)
	}
	if f.Version != commandsFileVersion {
		t.Errorf("initialized version = %d, want %d", f.Version, commandsFileVersion)
	}
	if len(f.Commands) != 0 {
		t.Errorf("initialized commands = %d, want 0", len(f.Commands))
	}
}

func TestSubmitValidationMatrix(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 16)
	now := time.Now().UTC()
	cases := []struct {
		name       string
		intent     string
		agent      agentkernel.AgentID
		capability agentkernel.Capability
		at         time.Time
		wantErr    bool
	}{
		{"happy path", "read the status", "coder.agent-one", agentkernel.CapabilityStatusRead, now, false},
		{"intent trimmed", "   ", "coder.agent-one", agentkernel.CapabilityStatusRead, now, true},
		{"intent control char", "bad\nintent", "coder.agent-one", agentkernel.CapabilityStatusRead, now, true},
		{"bad agent id", "intent", "nope", agentkernel.CapabilityStatusRead, now, true},
		{"unknown capability", "intent", "coder.agent-one", agentkernel.Capability("shell.exec"), now, true},
		{"zero time", "intent", "coder.agent-one", agentkernel.CapabilityStatusRead, time.Time{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Submit(tc.intent, tc.agent, tc.capability, tc.at)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Submit() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestSubmitRNGFailClosed(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 16)
	orig := rngCommandID
	rngCommandID = func() (string, error) { return "", ErrRNG }
	defer func() { rngCommandID = orig }()
	if _, err := s.Submit("intent", "coder.agent-one", agentkernel.CapabilityStatusRead, time.Now().UTC()); !errors.Is(err, ErrRNG) {
		t.Fatalf("Submit with dead RNG = %v, want ErrRNG", err)
	}
	if s.Count() != 0 {
		t.Errorf("Count() = %d after fail-closed RNG, want 0 (nothing captured)", s.Count())
	}
}

func TestSubmitCommandLimit(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 3)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if _, err := s.Submit("intent", "coder.agent-one", agentkernel.CapabilityStatusRead, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}
	if _, err := s.Submit("one too many", "coder.agent-one", agentkernel.CapabilityStatusRead, now.Add(10*time.Second)); !errors.Is(err, ErrCommandLimit) {
		t.Fatalf("Submit over bound = %v, want ErrCommandLimit", err)
	}
	if s.Count() != 3 {
		t.Errorf("Count() = %d, want 3", s.Count())
	}
}

func TestCommandLifecycleWithPersistence(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestStore(t, dir, 16)
	base := time.Now().UTC()

	// 1. Submit with no session available: the intent is captured, never lost.
	cmd, err := s.Submit("check the gaming inventory", "research.agent-two", agentkernel.CapabilityGamingRead, base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if cmd.State != CommandSubmitted {
		t.Fatalf("fresh command state = %s, want SUBMITTED", cmd.State)
	}

	// 2. Route to a session.
	id := CommandID(cmd.ID)
	routed, err := s.Route(id, "aks-one", base.Add(time.Minute))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if routed.State != CommandRouted || routed.SessionID != "aks-one" {
		t.Fatalf("routed = %+v", routed)
	}

	// 3. Decide (allow AND deny are both valid decisions).
	allow := agentkernel.Decision{Allow: true, Reason: "grant: agent research.agent-two holds capability gaming.read"}
	decided, err := s.Decide(id, allow, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decided.State != CommandDecided || decided.Decision == nil || !decided.Decision.Allow {
		t.Fatalf("decided = %+v", decided)
	}

	// 4. Terminal discipline.
	if _, err := s.Cancel(id, base.Add(3*time.Minute)); !errors.Is(err, ErrCommandState) {
		t.Fatalf("Cancel on DECIDED = %v, want ErrCommandState (a kernel decision is a fact)", err)
	}
	if _, err := s.Route(id, "aks-two", base.Add(3*time.Minute)); !errors.Is(err, ErrCommandState) {
		t.Fatalf("Route on DECIDED = %v, want ErrCommandState", err)
	}
	if _, err := s.Decide(id, allow, base.Add(3*time.Minute)); !errors.Is(err, ErrCommandState) {
		t.Fatalf("Decide on DECIDED = %v, want ErrCommandState", err)
	}

	// 5. Unknown command.
	if _, err := s.Cancel(CommandID("cmd-00000000000000000000000000000000"), base); !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("Cancel unknown = %v, want ErrUnknownCommand", err)
	}

	// 6. Decide without reason is rejected (kernel decisions name their rule).
	cmd2, err := s.Submit("second intent", "coder.agent-one", agentkernel.CapabilityStatusRead, base.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	if _, err := s.Route(CommandID(cmd2.ID), "aks-one", base.Add(5*time.Minute)); err != nil {
		t.Fatalf("Route 2: %v", err)
	}
	if _, err := s.Decide(CommandID(cmd2.ID), agentkernel.Decision{Allow: false}, base.Add(6*time.Minute)); !errors.Is(err, ErrEmptyDecisionReason) {
		t.Fatalf("Decide without reason = %v, want ErrEmptyDecisionReason", err)
	}

	// 7. Persistence round-trip: a NEW store instance sees the exact state.
	s2, err := NewCommandStore(s.Path(), 16)
	if err != nil {
		t.Fatalf("NewCommandStore reload: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("Load reload: %v", err)
	}
	reloaded, ok := s2.Get(id)
	if !ok {
		t.Fatal("decided command missing after reload")
	}
	if reloaded.State != CommandDecided || reloaded.Decision == nil || reloaded.Decision.Reason != allow.Reason {
		t.Fatalf("reloaded decided = %+v", reloaded)
	}
}

func TestRebindRoutedCommand(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 16)
	base := time.Now().UTC()
	cmd, err := s.Submit("intent", "coder.agent-one", agentkernel.CapabilityStatusRead, base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	id := CommandID(cmd.ID)
	if _, err := s.Route(id, "aks-one", base.Add(time.Minute)); err != nil {
		t.Fatalf("Route: %v", err)
	}
	// Re-bind to a newer session (ROUTED → ROUTED).
	rerouted, err := s.Route(id, "aks-two", base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("re-Route: %v", err)
	}
	if rerouted.State != CommandRouted || rerouted.SessionID != "aks-two" {
		t.Fatalf("re-routed = %+v, want session aks-two", rerouted)
	}
	// The binding is visible after reload (history is the audit trail's job).
	s2, err := NewCommandStore(s.Path(), 16)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := s2.Get(id)
	if !ok || got.SessionID != "aks-two" {
		t.Fatalf("reloaded re-bind = %+v", got)
	}
}

func TestCancelLifecycle(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 16)
	base := time.Now().UTC()
	sub, err := s.Submit("cancel me early", "coder.agent-one", agentkernel.CapabilityStatusRead, base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	routed, err := s.Submit("cancel me late", "coder.agent-one", agentkernel.CapabilityStatusRead, base.Add(time.Second))
	if err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	if _, err := s.Route(CommandID(routed.ID), "aks-one", base.Add(time.Minute)); err != nil {
		t.Fatalf("Route: %v", err)
	}
	// Cancel from SUBMITTED: no session fields.
	c1, err := s.Cancel(CommandID(sub.ID), base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Cancel SUBMITTED: %v", err)
	}
	if c1.State != CommandCancelled || c1.SessionID != "" || c1.RoutedAt != nil {
		t.Fatalf("cancelled-from-submitted = %+v", c1)
	}
	// Cancel from ROUTED: session/routing history stays.
	c2, err := s.Cancel(CommandID(routed.ID), base.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("Cancel ROUTED: %v", err)
	}
	if c2.State != CommandCancelled || c2.SessionID != "aks-one" || c2.RoutedAt == nil {
		t.Fatalf("cancelled-from-routed = %+v (routing history must stay)", c2)
	}
	// Terminal.
	if _, err := s.Cancel(CommandID(sub.ID), base.Add(4*time.Minute)); !errors.Is(err, ErrCommandState) {
		t.Fatalf("double Cancel = %v, want ErrCommandState", err)
	}
}

func TestListLastNewestFirst(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 16)
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if _, err := s.Submit("intent", "coder.agent-one", agentkernel.CapabilityStatusRead, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}
	all := s.ListLast(200)
	if len(all) != 5 {
		t.Fatalf("ListLast(200) = %d, want 5", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].SubmittedAt.Before(all[i].SubmittedAt) {
			t.Fatalf("ListLast not newest-first: [%d]=%v < [%d]=%v", i-1, all[i-1].SubmittedAt, i, all[i].SubmittedAt)
		}
	}
	// Deterministic 20× repetition.
	first := all[0].ID
	for i := 0; i < 20; i++ {
		got := s.ListLast(1)
		if len(got) != 1 || got[0].ID != first {
			t.Fatalf("ListLast(1) repetition %d = %+v, want id %s", i, got, first)
		}
	}
	// Clamp: n < 1 → 1.
	if got := s.ListLast(0); len(got) != 1 {
		t.Errorf("ListLast(0) = %d, want clamp to 1", len(got))
	}
}

func TestCounts(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 16)
	base := time.Now().UTC()
	a, _ := s.Submit("a", "coder.agent-one", agentkernel.CapabilityStatusRead, base)
	b, _ := s.Submit("b", "coder.agent-one", agentkernel.CapabilityStatusRead, base.Add(time.Second))
	c, _ := s.Submit("c", "coder.agent-one", agentkernel.CapabilityStatusRead, base.Add(2*time.Second))
	_, _ = s.Route(CommandID(b.ID), "aks-one", base.Add(time.Minute))
	_, _ = s.Route(CommandID(c.ID), "aks-one", base.Add(time.Minute))
	_, _ = s.Decide(CommandID(c.ID), agentkernel.Decision{Allow: true, Reason: "grant: x"}, base.Add(2*time.Minute))
	_, _ = s.Cancel(CommandID(a.ID), base.Add(3*time.Minute))
	counts := s.Counts()
	want := map[string]int{
		string(CommandSubmitted): 0,
		string(CommandRouted):    1,
		string(CommandDecided):   1,
		string(CommandCancelled): 1,
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("Counts()[%s] = %d, want %d", k, counts[k], v)
		}
	}
}

func TestGetReturnsCopies(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 16)
	base := time.Now().UTC()
	cmd, err := s.Submit("intent", "coder.agent-one", agentkernel.CapabilityStatusRead, base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := s.Route(CommandID(cmd.ID), "aks-one", base.Add(time.Minute)); err != nil {
		t.Fatalf("Route: %v", err)
	}
	got, ok := s.Get(CommandID(cmd.ID))
	if !ok {
		t.Fatal("Get: missing")
	}
	// Mutate the returned copy (through its pointer fields) — the store must not see it.
	if got.RoutedAt != nil {
		*got.RoutedAt = base.Add(-time.Hour)
	}
	got.Intent = "tampered"
	again, _ := s.Get(CommandID(cmd.ID))
	if again.Intent != "intent" {
		t.Errorf("store intent mutated through returned copy: %q", again.Intent)
	}
	if again.RoutedAt != nil && again.RoutedAt.Before(base) {
		t.Errorf("store routed_at mutated through returned copy")
	}
}

func TestLoadFailClosedMalformed(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "commands.json")
	validID := string(validCommandID())

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write commands file: %v", err)
		}
	}
	badPayloads := map[string]string{
		"not JSON":                 "not json at all",
		"wrong schema version":     `{"version":2,"commands":[]}`,
		"missing version":          `{"commands":[]}`,
		"bad record id":            `{"version":1,"commands":[{"id":"nope","intent":"i","agent_id":"coder.agent-one","capability":"status.read","state":"SUBMITTED","submitted_at":"2026-10-23T12:00:00Z","updated_at":"2026-10-23T12:00:00Z"}]}`,
		"unknown capability":       `{"version":1,"commands":[{"id":"` + validID + `","intent":"i","agent_id":"coder.agent-one","capability":"shell.exec","state":"SUBMITTED","submitted_at":"2026-10-23T12:00:00Z","updated_at":"2026-10-23T12:00:00Z"}]}`,
		"unknown state":            `{"version":1,"commands":[{"id":"` + validID + `","intent":"i","agent_id":"coder.agent-one","capability":"status.read","state":"HELD","submitted_at":"2026-10-23T12:00:00Z","updated_at":"2026-10-23T12:00:00Z"}]}`,
		"submitted with session":   `{"version":1,"commands":[{"id":"` + validID + `","intent":"i","agent_id":"coder.agent-one","capability":"status.read","state":"SUBMITTED","session_id":"aks-x","submitted_at":"2026-10-23T12:00:00Z","updated_at":"2026-10-23T12:00:00Z"}]}`,
		"decided without decision": `{"version":1,"commands":[{"id":"` + validID + `","intent":"i","agent_id":"coder.agent-one","capability":"status.read","state":"DECIDED","session_id":"aks-x","routed_at":"2026-10-23T12:01:00Z","decided_at":"2026-10-23T12:02:00Z","submitted_at":"2026-10-23T12:00:00Z","updated_at":"2026-10-23T12:02:00Z"}]}`,
		"zero submitted_at":        `{"version":1,"commands":[{"id":"` + validID + `","intent":"i","agent_id":"coder.agent-one","capability":"status.read","state":"SUBMITTED","submitted_at":"0001-01-01T00:00:00Z","updated_at":"2026-10-23T12:00:00Z"}]}`,
		"control char intent":      `{"version":1,"commands":[{"id":"` + validID + `","intent":"bad\nintent","agent_id":"coder.agent-one","capability":"status.read","state":"SUBMITTED","submitted_at":"2026-10-23T12:00:00Z","updated_at":"2026-10-23T12:00:00Z"}]}`,
	}
	for why, payload := range badPayloads {
		write(payload)
		s, err := NewCommandStore(path, 16)
		if err != nil {
			t.Fatalf("NewCommandStore: %v", err)
		}
		if err := s.Load(); err == nil {
			t.Errorf("Load accepted %s, want fail-closed rejection", why)
		}
	}

	// Duplicate IDs.
	dup := `{"version":1,"commands":[` +
		`{"id":"` + validID + `","intent":"i","agent_id":"coder.agent-one","capability":"status.read","state":"SUBMITTED","submitted_at":"2026-10-23T12:00:00Z","updated_at":"2026-10-23T12:00:00Z"},` +
		`{"id":"` + validID + `","intent":"i2","agent_id":"coder.agent-two","capability":"gaming.read","state":"SUBMITTED","submitted_at":"2026-10-23T12:00:01Z","updated_at":"2026-10-23T12:00:01Z"}]}`
	write(dup)
	s, err := NewCommandStore(path, 16)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s.Load(); err == nil {
		t.Error("Load accepted duplicate command ids, want fail-closed rejection")
	}

	// Over-bound file.
	var recs string
	for i := 0; i < 3; i++ {
		id := commandIDPrefix + fmt.Sprintf("%032d", i) // 32-char numeric hex-shaped id
		ts := fmt.Sprintf("2026-10-23T12:00:%02dZ", i)
		if i > 0 {
			recs += ","
		}
		recs += fmt.Sprintf(`{"id":%q,"intent":"i","agent_id":"coder.agent-one","capability":"status.read","state":"SUBMITTED","submitted_at":%q,"updated_at":%q}`, id, ts, ts)
	}
	write(`{"version":1,"commands":[` + recs + `]}`)
	s, err = NewCommandStore(path, 2)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s.Load(); err == nil {
		t.Error("Load accepted over-bound record count, want fail-closed rejection")
	}
}

func TestLoadFailClosedPermissions(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "commands.json")
	s, err := NewCommandStore(path, 16)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s.Submit("intent", "coder.agent-one", agentkernel.CapabilityStatusRead, time.Now().UTC()); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Loose FILE modes: tampering until an operator fixes it deliberately.
	for _, mode := range []os.FileMode{0o640, 0o644, 0o604, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod %o: %v", mode, err)
		}
		s2, err := NewCommandStore(path, 16)
		if err != nil {
			t.Fatalf("NewCommandStore: %v", err)
		}
		if err := s2.Load(); !errors.Is(err, ErrCommandFilePermissions) {
			t.Errorf("Load with file mode %o = %v, want ErrCommandFilePermissions", mode, err)
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("restore 0600: %v", err)
	}

	// Loose DIRECTORY: same discipline.
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	s3, err := NewCommandStore(path, 16)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s3.Load(); !errors.Is(err, ErrCommandFilePermissions) {
		t.Errorf("Load with loose dir = %v, want ErrCommandFilePermissions", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore dir: %v", err)
	}

	// After restoration the same file loads clean.
	s4, err := NewCommandStore(path, 16)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s4.Load(); err != nil {
		t.Fatalf("Load after restore: %v", err)
	}
	if s4.Count() != 1 {
		t.Errorf("Count after restore = %d, want 1", s4.Count())
	}
}

func TestPersistFailureRollsBack(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestStore(t, dir, 16)
	base := time.Now().UTC()
	cmd, err := s.Submit("intent", "coder.agent-one", agentkernel.CapabilityStatusRead, base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Make the directory read-only (0500: passes the 0700-perm check, but
	// CreateTemp fails) — every persist now fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir 0500: %v", err)
	}
	if _, err := s.Cancel(CommandID(cmd.ID), base.Add(time.Minute)); err == nil {
		t.Fatal("Cancel with unwritable store succeeded, want persist failure")
	}
	// Rollback: the command is still SUBMITTED, state and file stay in sync.
	got, ok := s.Get(CommandID(cmd.ID))
	if !ok || got.State != CommandSubmitted {
		t.Fatalf("after failed persist: %+v — want SUBMITTED (rollback)", got)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore dir: %v", err)
	}
	// The rollback survived: reload shows SUBMITTED.
	s2, err := NewCommandStore(s.Path(), 16)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	reloaded, _ := s2.Get(CommandID(cmd.ID))
	if reloaded.State != CommandSubmitted {
		t.Fatalf("reloaded state = %s, want SUBMITTED (persist rollback held)", reloaded.State)
	}
}

func TestRouteRejectsEmptySession(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 16)
	base := time.Now().UTC()
	cmd, err := s.Submit("intent", "coder.agent-one", agentkernel.CapabilityStatusRead, base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := s.Route(CommandID(cmd.ID), "", base.Add(time.Minute)); err == nil {
		t.Fatal("Route with empty session accepted, want rejection")
	}
}

func TestBackwardsClockRejected(t *testing.T) {
	s := newTestStore(t, fabricTestDir(t), 16)
	base := time.Now().UTC()
	cmd, err := s.Submit("intent", "coder.agent-one", agentkernel.CapabilityStatusRead, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := s.Route(CommandID(cmd.ID), "aks-one", base); !errors.Is(err, ErrCommandState) {
		t.Fatalf("Route backwards in time = %v, want ErrCommandState", err)
	}
}
