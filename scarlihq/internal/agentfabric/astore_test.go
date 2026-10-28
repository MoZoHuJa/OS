package agentfabric

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// BF-4 (v19.5.3): the approval store test matrix — the estore discipline,
// mirrored: fail-closed load (corruption payloads, permissions, bound),
// atomic persist + rollback, restart persistence, the PENDING-only state
// machine (terminal decisions are never re-decided), one-request-per-
// command enforced twice, RNG fail-closed, file shape.

func newTestApprovalStore(t *testing.T, dir string, maxApprovals int) *ApprovalStore {
	t.Helper()
	s, err := NewApprovalStore(filepath.Join(dir, "approvals.json"), maxApprovals)
	if err != nil {
		t.Fatalf("NewApprovalStore: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func validApprovalIDHex(char byte) string {
	return "apv-" + strings.Repeat(string(rune(char)), 32)
}

func validCommandIDForApproval(char byte) string {
	return "cmd-" + strings.Repeat(string(rune(char)), 32)
}

func validSessionIDForApproval() string {
	return "aks-" + strings.Repeat("1", 32)
}

// baseApproval is a valid PENDING mode.request approval request.
func baseApproval() ApprovalRecord {
	return ApprovalRecord{
		ID:          validApprovalIDHex('a'),
		CommandID:   validCommandIDForApproval('b'),
		AgentID:     "coder.act",
		Capability:  "mode.request",
		SessionID:   validSessionIDForApproval(),
		Runtime:     RuntimeCoding,
		Submitter:   "zuzka",
		Params:      ApprovalParams{Mode: "ai"},
		State:       ApprovalPending,
		Intent:      "switch the host to ai mode",
		RequestedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
	}
}

func TestNewApprovalStoreValidation(t *testing.T) {
	if _, err := NewApprovalStore("", 10); err == nil {
		t.Error("empty path accepted, want construction error")
	}
	if _, err := NewApprovalStore("/tmp/x.json", 0); err == nil {
		t.Error("maxApprovals=0 accepted, want construction error")
	}
	if _, err := NewApprovalStore("/tmp/x.json", -1); err == nil {
		t.Error("maxApprovals=-1 accepted, want construction error")
	}
}

func TestApprovalStoreLoadInitializesEmptyFile(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "approvals.json")
	s, err := NewApprovalStore(path, 16)
	if err != nil {
		t.Fatalf("NewApprovalStore: %v", err)
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

func TestApprovalStoreRequestAndPersistence(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestApprovalStore(t, dir, 16)
	rec := baseApproval()
	saved, err := s.Request(rec)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if saved.ID != rec.ID {
		t.Errorf("saved id %q != %q", saved.ID, rec.ID)
	}
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1", s.Count())
	}
	// One request per command, ever.
	if _, err := s.Request(rec); !errors.Is(err, ErrAlreadyRequested) {
		t.Errorf("second Request for the same command = %v, want ErrAlreadyRequested", err)
	}
	// A DIFFERENT command requests fine.
	second := baseApproval()
	second.ID = validApprovalIDHex('d')
	second.CommandID = validCommandIDForApproval('e')
	if _, err := s.Request(second); err != nil {
		t.Fatalf("Request(second): %v", err)
	}

	// Restart persistence: a fresh store over the same file sees both.
	s2, err := NewApprovalStore(filepath.Join(dir, "approvals.json"), 16)
	if err != nil {
		t.Fatalf("NewApprovalStore(restart): %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("Load(restart): %v", err)
	}
	if s2.Count() != 2 {
		t.Fatalf("restart Count = %d, want 2 (the file is the history)", s2.Count())
	}
	got, ok := s2.GetByCommand(CommandID(rec.CommandID))
	if !ok || got.ID != rec.ID || got.State != ApprovalPending {
		t.Fatalf("GetByCommand after restart = %+v ok=%v", got, ok)
	}
	// The one-request-per-command invariant survives the restart.
	if _, err := s2.Request(rec); !errors.Is(err, ErrAlreadyRequested) {
		t.Errorf("post-restart duplicate Request = %v, want ErrAlreadyRequested", err)
	}
}

func TestApprovalStoreStateMachine(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestApprovalStore(t, dir, 16)
	decidedAt := time.Date(2026, 10, 28, 12, 5, 0, 0, time.UTC)

	// APPROVE closes PENDING with the execution reference.
	rec := baseApproval()
	if _, err := s.Request(rec); err != nil {
		t.Fatalf("Request: %v", err)
	}
	execID := "afx-" + strings.Repeat("a", 32)
	approved, err := s.Approve(ApprovalID(rec.ID), "op-xena", execID, decidedAt)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.State != ApprovalApproved || approved.Approver != "op-xena" || approved.ExecutionID != execID {
		t.Fatalf("approved = %+v", approved)
	}
	// Terminal decisions are never re-decided.
	if _, err := s.Approve(ApprovalID(rec.ID), "op-xena", execID, decidedAt.Add(time.Minute)); !errors.Is(err, ErrApprovalState) {
		t.Errorf("re-Approve on APPROVED = %v, want ErrApprovalState", err)
	}
	if _, err := s.Reject(ApprovalID(rec.ID), "op-xena", "no", decidedAt.Add(time.Minute)); !errors.Is(err, ErrApprovalState) {
		t.Errorf("Reject on APPROVED = %v, want ErrApprovalState", err)
	}
	// Unknown ids fail closed.
	if _, err := s.Approve(ApprovalID(validApprovalIDHex('9')), "op", execID, decidedAt); !errors.Is(err, ErrUnknownApproval) {
		t.Errorf("Approve(unknown) = %v, want ErrUnknownApproval", err)
	}

	// REJECT closes PENDING with the approver + optional reason.
	rej := baseApproval()
	rej.ID = validApprovalIDHex('e')
	rej.CommandID = validCommandIDForApproval('e')
	if _, err := s.Request(rej); err != nil {
		t.Fatalf("Request(rej): %v", err)
	}
	rejected, err := s.Reject(ApprovalID(rej.ID), "op-mira", "not on this host", decidedAt)
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.State != ApprovalRejected || rejected.Approver != "op-mira" || rejected.Reason != "not on this host" || rejected.ExecutionID != "" {
		t.Fatalf("rejected = %+v", rejected)
	}
	if _, err := s.Reject(ApprovalID(rej.ID), "op-mira", "", decidedAt.Add(time.Minute)); !errors.Is(err, ErrApprovalState) {
		t.Errorf("re-Reject on REJECTED = %v, want ErrApprovalState", err)
	}

	// Invalid decisions fail before any persist: bad approver, bad
	// execution id, zero time.
	pnd := baseApproval()
	pnd.ID = validApprovalIDHex('b')
	pnd.CommandID = validCommandIDForApproval('c')
	if _, err := s.Request(pnd); err != nil {
		t.Fatalf("Request(pnd): %v", err)
	}
	if _, err := s.Approve(ApprovalID(pnd.ID), " ", execID, decidedAt); err == nil {
		t.Error("Approve with whitespace approver accepted")
	}
	if _, err := s.Approve(ApprovalID(pnd.ID), "op", "not-afx", decidedAt); err == nil {
		t.Error("Approve with malformed execution id accepted")
	}
	if _, err := s.Approve(ApprovalID(pnd.ID), "op", execID, time.Time{}); err == nil {
		t.Error("Approve with zero time accepted")
	}
	if _, err := s.Reject(ApprovalID(pnd.ID), "op", strings.Repeat("x", 300), decidedAt); err == nil {
		t.Error("Reject with over-bound reason accepted")
	}
	// The request is STILL PENDING (nothing partial was persisted).
	got, _ := s.Get(ApprovalID(pnd.ID))
	if got.State != ApprovalPending {
		t.Errorf("state after failed decisions = %q, want PENDING (no partial writes)", got.State)
	}

	// Counts: all states always present (the closed enumeration is
	// PENDING/APPROVED/REJECTED — there is deliberately no withdrawn
	// state: a request's command is DECIDED-terminal, it can never be
	// cancelled out from under the operator).
	counts := s.Counts()
	if counts[string(ApprovalApproved)] != 1 || counts[string(ApprovalRejected)] != 1 || counts[string(ApprovalPending)] != 1 {
		t.Errorf("Counts = %v — want APPROVED=1 REJECTED=1 PENDING=1", counts)
	}
	if _, hasWithdrawn := counts[string(ApprovalState("WITHDRAWN"))]; hasWithdrawn {
		t.Errorf("Counts carry WITHDRAWN — the enumeration is closed at 3 states")
	}
}

func TestApprovalStoreGetAndList(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestApprovalStore(t, dir, 16)
	cmdA := validCommandIDForApproval('a')
	cmdB := validCommandIDForApproval('b')
	a := baseApproval()
	a.CommandID = string(cmdA)
	b := baseApproval()
	b.ID = validApprovalIDHex('f')
	b.CommandID = string(cmdB)
	for _, rec := range []ApprovalRecord{a, b} {
		if _, err := s.Request(rec); err != nil {
			t.Fatalf("Request: %v", err)
		}
	}
	if _, err := s.Approve(ApprovalID(b.ID), "op", "afx-"+strings.Repeat("a", 32), time.Date(2026, 10, 28, 12, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Approve(b): %v", err)
	}
	if got, ok := s.GetByCommand(CommandID(cmdA)); !ok || got.ID != a.ID {
		t.Errorf("GetByCommand(cmdA) = %+v ok=%v", got, ok)
	}
	if _, ok := s.GetByCommand(CommandID(validCommandIDForApproval('9'))); ok {
		t.Error("GetByCommand(unknown) = ok — want false")
	}
	if _, ok := s.Get(ApprovalID(a.ID)); !ok {
		t.Error("Get(known id) = false")
	}
	// Newest-first.
	last := s.ListLast(10)
	if len(last) != 2 || last[0].ID != b.ID || last[1].ID != a.ID {
		t.Errorf("ListLast order = [%s, %s], want newest-first [b, a]", last[0].ID, last[1].ID)
	}
	// State filter.
	approved := s.ListLastState(10, ApprovalApproved)
	if len(approved) != 1 || approved[0].ID != b.ID {
		t.Errorf("ListLastState(APPROVED) = %d records, want 1 (b)", len(approved))
	}
	if got := s.ListLastState(10, ApprovalRejected); len(got) != 0 {
		t.Errorf("ListLastState(REJECTED) = %d records, want 0", len(got))
	}
}

func TestApprovalStoreBound(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestApprovalStore(t, dir, 2)
	for i := 0; i < 2; i++ {
		rec := baseApproval()
		rec.ID = validApprovalIDHex(byte('a' + i))
		rec.CommandID = validCommandIDForApproval(byte('a' + i))
		if _, err := s.Request(rec); err != nil {
			t.Fatalf("Request(%d): %v", i, err)
		}
	}
	third := baseApproval()
	third.ID = validApprovalIDHex('c')
	third.CommandID = validCommandIDForApproval('c')
	if _, err := s.Request(third); !errors.Is(err, ErrApprovalLimit) {
		t.Errorf("Request over bound = %v, want ErrApprovalLimit", err)
	}
}

func TestApprovalStoreRNGFailClosed(t *testing.T) {
	saved := rngApprovalID
	rngApprovalID = func() (string, error) { return "", ErrApprovalRNG }
	defer func() { rngApprovalID = saved }()
	if _, err := NewApprovalID(); !errors.Is(err, ErrApprovalRNG) {
		t.Errorf("NewApprovalID on RNG failure = %v, want ErrApprovalRNG", err)
	}
}

func TestApprovalStoreLoadFailClosed(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "approvals.json")

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write approvals file: %v", err)
		}
	}
	cmdID := string(validCommandIDForApproval('b'))
	base := `"id":"` + validApprovalIDHex('a') + `","command_id":"` + cmdID + `","agent_id":"coder.act","capability":"mode.request","session_id":"` + validSessionIDForApproval() + `","runtime":"coding","submitter":"zuzka","params":{"mode":"ai"},"intent":"switch the host to ai mode","requested_at":"2026-10-28T12:00:00Z"`
	badPayloads := map[string]string{
		"not JSON":                "not json at all",
		"wrong schema version":    `{"version":2,"approvals":[]}`,
		"missing version":         `{"approvals":[]}`,
		"unknown state":           `{"version":1,"approvals":[{` + base + `,"state":"APPROVING"}]}`, // (WITHDRAWN-shaped files land here too — the enumeration is closed at 3 states)
		"pending with approver":   `{"version":1,"approvals":[{` + base + `,"state":"PENDING","approver":"op"}]}`,
		"approved no execution":   `{"version":1,"approvals":[{` + base + `,"state":"APPROVED","approver":"op","decided_at":"2026-10-28T12:05:00Z"}]}`,
		"rejected with execution": `{"version":1,"approvals":[{` + base + `,"state":"REJECTED","approver":"op","decided_at":"2026-10-28T12:05:00Z","execution_id":"afx-` + strings.Repeat("a", 32) + `"}]}`,
		"withdrawn with approver": `{"version":1,"approvals":[{` + base + `,"state":"WITHDRAWN","approver":"op","decided_at":"2026-10-28T12:05:00Z"}]}`,
		"research capability":     `{"version":1,"approvals":[{` + base + `,"capability":"status.read","runtime":"research","params":{},"state":"PENDING"}]}`,
		"wrong runtime mapping":   `{"version":1,"approvals":[{` + base + `,"runtime":"research","state":"PENDING"}]}`,
		"params mismatch":         `{"version":1,"approvals":[{` + base + `,"params":{"allocation_id":"x"},"state":"PENDING"}]}`,
		"duplicate approval id":   `{"version":1,"approvals":[{` + base + `,"state":"PENDING"},{` + base + `,"state":"PENDING"}]}`,
		"command requested twice": `{"version":1,"approvals":[{` + base + `,"state":"PENDING"},{"id":"apv-` + strings.Repeat("z", 32) + `","command_id":"` + cmdID + `","agent_id":"coder.act","capability":"mode.request","session_id":"` + validSessionIDForApproval() + `","runtime":"coding","params":{"mode":"ai"},"intent":"switch the host to ai mode","requested_at":"2026-10-28T12:01:00Z","state":"PENDING"}]}`,
		"over bound":              `{"version":1,"approvals":[` + strings.Repeat(`{`+base+`,"state":"PENDING"},`, 3) + `{` + base + `,"state":"PENDING"}]}`,
	}
	for why, payload := range badPayloads {
		write(payload)
		s, err := NewApprovalStore(path, 3)
		if err != nil {
			t.Fatalf("NewApprovalStore: %v", err)
		}
		if err := s.Load(); err == nil {
			t.Errorf("corrupt file (%s) loaded — fail-closed violated", why)
		}
	}

	// Loose file permissions abort the load (tamper discipline).
	write(`{"version":1,"approvals":[]}`)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	s, err := NewApprovalStore(path, 3)
	if err != nil {
		t.Fatalf("NewApprovalStore: %v", err)
	}
	if err := s.Load(); !errors.Is(err, ErrApprovalFilePermissions) {
		t.Errorf("loose file mode = %v, want ErrApprovalFilePermissions", err)
	}
	_ = os.Chmod(path, 0o600)
	// Loose DIRECTORY aborts too.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	if err := s.Load(); !errors.Is(err, ErrApprovalFilePermissions) {
		t.Errorf("loose dir mode = %v, want ErrApprovalFilePermissions", err)
	}
}

func TestApprovalStoreAtomicPersistRollback(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestApprovalStore(t, dir, 4)
	rec := baseApproval()
	if _, err := s.Request(rec); err != nil {
		t.Fatalf("Request: %v", err)
	}
	path := filepath.Join(dir, "approvals.json")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"approvals":[]}`), 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	// Reload a fresh store on the (valid) file, request one more while the
	// directory is still writable, then break persistability by removing
	// write permission from the DIRECTORY.
	s2, err := NewApprovalStore(path, 4)
	if err != nil {
		t.Fatalf("NewApprovalStore: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	extra := baseApproval()
	extra.ID = validApprovalIDHex('1')
	extra.CommandID = validCommandIDForApproval('1')
	if _, err := s2.Request(extra); err != nil {
		t.Fatalf("Request(extra) while writable: %v", err)
	}
	if s2.Count() != 1 {
		t.Fatalf("Count = %d, want 1 before the persist-failure probe", s2.Count())
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir ro: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	second := baseApproval()
	second.ID = validApprovalIDHex('0')
	second.CommandID = validCommandIDForApproval('0')
	if _, err := s2.Request(second); err == nil {
		t.Fatal("Request succeeded against an unwritable directory — persist discipline violated")
	}
	// Rollback: the in-memory state must NOT include the failed append.
	if s2.Count() != 1 {
		t.Errorf("Count after failed persist = %d, want 1 (rollback — state and file stay in sync)", s2.Count())
	}
	if _, ok := s2.GetByCommand(CommandID(second.CommandID)); ok {
		t.Error("failed record is visible in memory — rollback violated")
	}
	// A failed DECISION persist rolls back too (the request stays PENDING).
	pnd := s2.ListLast(1)[0]
	if _, err := s2.Reject(ApprovalID(pnd.ID), "op", "no", time.Now().UTC()); err == nil {
		t.Fatal("Reject succeeded against an unwritable directory — persist discipline violated")
	}
	got, _ := s2.Get(ApprovalID(pnd.ID))
	if got.State != ApprovalPending {
		t.Errorf("state after failed Reject persist = %q, want PENDING (rollback)", got.State)
	}
}

func TestApprovalStoreFileShape(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestApprovalStore(t, dir, 16)
	rec := baseApproval()
	if _, err := s.Request(rec); err != nil {
		t.Fatalf("Request: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "approvals.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f struct {
		Version   int              `json:"version"`
		Approvals []ApprovalRecord `json:"approvals"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Version != 1 {
		t.Errorf("schema version = %d, want 1 (explicit)", f.Version)
	}
	if len(f.Approvals) != 1 || f.Approvals[0].ID != rec.ID {
		t.Errorf("file carries %+v, want the single record", f.Approvals)
	}
	if f.Approvals[0].State != ApprovalPending || f.Approvals[0].Params.Mode != "ai" {
		t.Errorf("record shape = %+v (params must be captured)", f.Approvals[0])
	}
}
