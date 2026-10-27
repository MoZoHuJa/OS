package agentfabric

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestExecutionStore(t *testing.T, dir string, maxExecutions int) *ExecutionStore {
	t.Helper()
	s, err := NewExecutionStore(filepath.Join(dir, "executions.json"), maxExecutions)
	if err != nil {
		t.Fatalf("NewExecutionStore: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestNewExecutionStoreValidation(t *testing.T) {
	if _, err := NewExecutionStore("", 10); err == nil {
		t.Error("empty path accepted, want construction error")
	}
	if _, err := NewExecutionStore("/tmp/x.json", 0); err == nil {
		t.Error("maxExecutions=0 accepted, want construction error")
	}
	if _, err := NewExecutionStore("/tmp/x.json", -1); err == nil {
		t.Error("maxExecutions=-1 accepted, want construction error")
	}
}

func TestExecutionStoreLoadInitializesEmptyFile(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "executions.json")
	s, err := NewExecutionStore(path, 16)
	if err != nil {
		t.Fatalf("NewExecutionStore: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("initialized file mode %o, want 0600", info.Mode().Perm())
	}
	if s.Count() != 0 {
		t.Errorf("Count = %d, want 0", s.Count())
	}
}

func TestExecutionStoreRecordAndPersistence(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestExecutionStore(t, dir, 16)
	rec := baseExecution()
	saved, err := s.Record(rec)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if saved.ID != rec.ID {
		t.Errorf("saved id %q != %q", saved.ID, rec.ID)
	}
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1", s.Count())
	}
	// One execution per command, ever.
	if _, err := s.Record(rec); !errors.Is(err, ErrAlreadyExecuted) {
		t.Errorf("second Record for the same command = %v, want ErrAlreadyExecuted", err)
	}
	// A DIFFERENT command records fine.
	second := baseExecution()
	second.ID = "afx-" + strings.Repeat("d", 32)
	second.CommandID = "cmd-" + strings.Repeat("e", 32)
	if _, err := s.Record(second); err != nil {
		t.Fatalf("Record(second): %v", err)
	}

	// Restart persistence: a fresh store over the same file sees both.
	s2, err := NewExecutionStore(filepath.Join(dir, "executions.json"), 16)
	if err != nil {
		t.Fatalf("NewExecutionStore(restart): %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("Load(restart): %v", err)
	}
	if s2.Count() != 2 {
		t.Fatalf("restart Count = %d, want 2 (the file is the history)", s2.Count())
	}
	got, ok := s2.GetByCommand(CommandID(rec.CommandID))
	if !ok || got.ID != rec.ID || got.State != ExecutionCompleted {
		t.Fatalf("GetByCommand after restart = %+v ok=%v", got, ok)
	}
	// The one-execution-per-command invariant survives the restart.
	if _, err := s2.Record(rec); !errors.Is(err, ErrAlreadyExecuted) {
		t.Errorf("post-restart duplicate Record = %v, want ErrAlreadyExecuted", err)
	}
}

func TestExecutionStoreGetByCommandAndList(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestExecutionStore(t, dir, 16)
	cmdA := "cmd-" + strings.Repeat("a", 32)
	cmdB := "cmd-" + strings.Repeat("b", 32)
	a := baseExecution()
	a.CommandID = cmdA
	b := baseExecution()
	b.ID = "afx-" + strings.Repeat("f", 32)
	b.CommandID = cmdB
	b.State = ExecutionSkipped
	b.Allow = false
	b.Outcome = ""
	b.Reason = "rule/session-state: session is SUSPENDED, must be ACTIVE"
	for _, rec := range []ExecutionRecord{a, b} {
		if _, err := s.Record(rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if got, ok := s.GetByCommand(CommandID(cmdA)); !ok || got.ID != a.ID {
		t.Errorf("GetByCommand(cmdA) = %+v ok=%v", got, ok)
	}
	if _, ok := s.GetByCommand(CommandID("cmd-" + strings.Repeat("9", 32))); ok {
		t.Error("GetByCommand(unknown) = ok — want false")
	}
	if _, ok := s.Get(ExecutionID(a.ID)); !ok {
		t.Error("Get(known id) = false")
	}
	// Newest-first.
	last := s.ListLast(10)
	if len(last) != 2 || last[0].ID != b.ID || last[1].ID != a.ID {
		t.Errorf("ListLast order = [%s, %s], want newest-first [b, a]", last[0].ID, last[1].ID)
	}
	// State filter.
	skipped := s.ListLastState(10, ExecutionSkipped)
	if len(skipped) != 1 || skipped[0].ID != b.ID {
		t.Errorf("ListLastState(SKIPPED) = %d records, want 1 (b)", len(skipped))
	}
	if got := s.ListLastState(10, ExecutionFailed); len(got) != 0 {
		t.Errorf("ListLastState(FAILED) = %d records, want 0", len(got))
	}
	// Counts: all states always present.
	counts := s.Counts()
	if counts[string(ExecutionCompleted)] != 1 || counts[string(ExecutionSkipped)] != 1 || counts[string(ExecutionFailed)] != 0 {
		t.Errorf("Counts = %v — want COMPLETED=1 SKIPPED=1 FAILED=0", counts)
	}
}

func TestExecutionStoreBound(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestExecutionStore(t, dir, 2)
	for i := 0; i < 2; i++ {
		rec := baseExecution()
		rec.ID = "afx-" + strings.Repeat(string(rune('a'+i)), 32)
		rec.CommandID = "cmd-" + strings.Repeat(string(rune('a'+i)), 32)
		if _, err := s.Record(rec); err != nil {
			t.Fatalf("Record(%d): %v", i, err)
		}
	}
	third := baseExecution()
	third.ID = "afx-" + strings.Repeat("c", 32)
	third.CommandID = "cmd-" + strings.Repeat("c", 32)
	if _, err := s.Record(third); !errors.Is(err, ErrExecutionLimit) {
		t.Errorf("Record over bound = %v, want ErrExecutionLimit", err)
	}
}

func TestExecutionStoreRNGFailClosed(t *testing.T) {
	saved := rngExecutionID
	rngExecutionID = func() (string, error) { return "", ErrExecutionRNG }
	defer func() { rngExecutionID = saved }()
	if _, err := NewExecutionID(); !errors.Is(err, ErrExecutionRNG) {
		t.Errorf("NewExecutionID on RNG failure = %v, want ErrExecutionRNG", err)
	}
}

func TestExecutionStoreLoadFailClosed(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "executions.json")
	validID := validExecutionID()

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write executions file: %v", err)
		}
	}
	cmdID := string(validCommandIDForExec())
	base := `"id":"` + validID + `","command_id":"` + cmdID + `","agent_id":"research.agent-one","capability":"status.read","session_id":"` + validSessionIDForExec() + `","runtime":"research","intent":"read the host status","executed_at":"2026-10-27T12:00:00Z"`
	badPayloads := map[string]string{
		"not JSON":               "not json at all",
		"wrong schema version":   `{"version":2,"executions":[]}`,
		"missing version":        `{"executions":[]}`,
		"unknown state":          `{"version":1,"executions":[{` + base + `,"state":"RUNNING","allow":true,"outcome":"x"}]}`,
		"skipped with allow":     `{"version":1,"executions":[{` + base + `,"state":"SKIPPED","allow":true,"reason":"x"}]}`,
		"completed no outcome":   `{"version":1,"executions":[{` + base + `,"state":"COMPLETED","allow":true}]}`,
		"failed no reason":       `{"version":1,"executions":[{` + base + `,"state":"FAILED","allow":true}]}`,
		"bad runtime label":      `{"version":1,"executions":[{` + base + `,"state":"COMPLETED","allow":true,"outcome":"x","runtime":"shell"}]}`,
		"wrong runtime mapping":  `{"version":1,"executions":[{` + base + `,"state":"COMPLETED","allow":true,"outcome":"x","runtime":"coding"}]}`,
		"duplicate execution id": `{"version":1,"executions":[{` + base + `,"state":"COMPLETED","allow":true,"outcome":"x"},{` + base + `,"state":"COMPLETED","allow":true,"outcome":"x"}]}`,
		"command executed twice": `{"version":1,"executions":[{` + base + `,"state":"COMPLETED","allow":true,"outcome":"x"},{"id":"afx-` + strings.Repeat("z", 32) + `","command_id":"` + cmdID + `","agent_id":"research.agent-one","capability":"status.read","session_id":"` + validSessionIDForExec() + `","runtime":"research","intent":"read the host status","executed_at":"2026-10-27T12:01:00Z","state":"FAILED","allow":true,"reason":"act error"}]}`,
		"over bound":             `{"version":1,"executions":[` + strings.Repeat(`{`+base+`,"state":"COMPLETED","allow":true,"outcome":"x"},`, 3) + `{` + base + `,"state":"COMPLETED","allow":true,"outcome":"x"}]}`,
	}
	for why, payload := range badPayloads {
		write(payload)
		s, err := NewExecutionStore(path, 3)
		if err != nil {
			t.Fatalf("NewExecutionStore: %v", err)
		}
		if err := s.Load(); err == nil {
			t.Errorf("corrupt file (%s) loaded — fail-closed violated", why)
		}
	}

	// Loose file permissions abort the load (tamper discipline).
	write(`{"version":1,"executions":[]}`)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	s, err := NewExecutionStore(path, 3)
	if err != nil {
		t.Fatalf("NewExecutionStore: %v", err)
	}
	if err := s.Load(); !errors.Is(err, ErrExecutionFilePermissions) {
		t.Errorf("loose file mode = %v, want ErrExecutionFilePermissions", err)
	}
	_ = os.Chmod(path, 0o600)
	// Loose DIRECTORY aborts too.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	if err := s.Load(); !errors.Is(err, ErrExecutionFilePermissions) {
		t.Errorf("loose dir mode = %v, want ErrExecutionFilePermissions", err)
	}
}

func TestExecutionStoreAtomicPersistRollback(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestExecutionStore(t, dir, 4)
	// Corrupt the directory discipline: make the temp-file creation fail by
	// replacing the directory with a FILE after the first record.
	rec := baseExecution()
	if _, err := s.Record(rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	path := filepath.Join(dir, "executions.json")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"executions":[]}`), 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	// Reload a fresh store on the (valid) file, record one more while the
	// directory is still writable, then break persistability by removing
	// write permission from the DIRECTORY.
	s2, err := NewExecutionStore(path, 4)
	if err != nil {
		t.Fatalf("NewExecutionStore: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	extra := baseExecution()
	extra.ID = "afx-" + strings.Repeat("1", 32)
	extra.CommandID = "cmd-" + strings.Repeat("1", 32)
	if _, err := s2.Record(extra); err != nil {
		t.Fatalf("Record(extra) while writable: %v", err)
	}
	if s2.Count() != 1 {
		t.Fatalf("Count = %d, want 1 before the persist-failure probe", s2.Count())
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir ro: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	second := baseExecution()
	second.ID = "afx-" + strings.Repeat("0", 32)
	second.CommandID = "cmd-" + strings.Repeat("0", 32)
	if _, err := s2.Record(second); err == nil {
		t.Fatal("Record succeeded against an unwritable directory — persist discipline violated")
	}
	// Rollback: the in-memory state must NOT include the failed append.
	if s2.Count() != 1 {
		t.Errorf("Count after failed persist = %d, want 1 (rollback — state and file stay in sync)", s2.Count())
	}
	if _, ok := s2.GetByCommand(CommandID(second.CommandID)); ok {
		t.Error("failed record is visible in memory — rollback violated")
	}
}

func TestExecutionStoreFileShape(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestExecutionStore(t, dir, 4)
	rec := baseExecution()
	if _, err := s.Record(rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "executions.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("file is not valid JSON: %v", err)
	}
	if v, ok := f["version"].(float64); !ok || int(v) != 1 {
		t.Errorf("file version = %v, want 1 (explicit schema)", f["version"])
	}
	execs, ok := f["executions"].([]any)
	if !ok || len(execs) != 1 {
		t.Fatalf("file executions = %v, want 1 record", f["executions"])
	}
	first, _ := execs[0].(map[string]any)
	for _, key := range []string{"id", "command_id", "agent_id", "capability", "session_id", "runtime", "state", "allow", "outcome", "intent", "executed_at"} {
		if _, present := first[key]; !present {
			t.Errorf("record misses key %q", key)
		}
	}
	if _, present := first["workspace_id"]; present {
		t.Error("unscoped record carries workspace_id — omitempty violated")
	}
}
