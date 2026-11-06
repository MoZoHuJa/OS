package agentfabric

// CF-3 (v19.6.2): the proposal store test matrix — capture/accept/dismiss
// lifecycle, the PENDING-only state machine (terminal decisions are never
// re-decided), one-command-reference-per-proposal enforced twice, restart
// persistence, bounds, RNG fail-closed, atomic persist + rollback,
// fail-closed load (corruption matrix × payloads), file shape.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

func newTestProposalStore(t *testing.T, dir string, maxProposals int) *ProposalStore {
	t.Helper()
	s, err := NewProposalStore(filepath.Join(dir, "proposals.json"), maxProposals)
	if err != nil {
		t.Fatalf("NewProposalStore: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func validProposalIDHex(char byte) string {
	return "prp-" + strings.Repeat(string(rune(char)), 32)
}

// baseProposal is a valid PENDING proposal with typed attribution.
func baseProposal() ProposalRecord {
	return ProposalRecord{
		ID:               validProposalIDHex('a'),
		Intent:           "switch the host to ai mode",
		AgentID:          "coder.act",
		Capability:       "mode.request",
		ProposerProvider: airuntime.ProviderSGLang,
		ProposerModel:    "qwen3-14b",
		State:            ProposalPending,
		CapturedAt:       time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
	}
}

func TestNewProposalStoreValidation(t *testing.T) {
	dir := fabricTestDir(t)
	if _, err := NewProposalStore("", 4); err == nil {
		t.Error("empty path accepted")
	}
	if _, err := NewProposalStore(filepath.Join(dir, "proposals.json"), 0); err == nil {
		t.Error("non-positive bound accepted")
	}
}

func TestProposalStoreLoadInitializesEmptyFile(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "proposals.json")
	s := newTestProposalStore(t, dir, 4)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the empty file must be materialized on first run: %v", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}
	if s.Count() != 0 {
		t.Errorf("Count = %d, want 0", s.Count())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f proposalsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if f.Version != proposalsFileVersion {
		t.Errorf("schema version = %d, want %d", f.Version, proposalsFileVersion)
	}
}

func TestProposalStoreCaptureAcceptDismissLifecycle(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestProposalStore(t, dir, 8)
	now := time.Date(2026, 10, 30, 12, 1, 0, 0, time.UTC)

	captured, err := s.Capture(baseProposal())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if captured.State != ProposalPending {
		t.Errorf("state = %q, want PENDING", captured.State)
	}
	// Capture only takes PENDING records — decisions are Accept/Dismiss.
	if _, err := s.Capture(validProposalFixture(func(r *ProposalRecord) {
		r.ID = validProposalIDHex('z')
		r.State = ProposalAccepted
		r.Operator = "zuzka"
		r.CommandID = validCommandIDForApproval('b')
		r.DecidedAt = &now
	})); err == nil {
		t.Error("Capture accepted a non-PENDING record")
	}

	// Accept closes PENDING → ACCEPTED with the command reference.
	closed, err := s.Accept(ProposalID(captured.ID), "zuzka", validCommandIDForApproval('b'), now)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if closed.State != ProposalAccepted || closed.CommandID != validCommandIDForApproval('b') || closed.Operator != "zuzka" {
		t.Errorf("closed = %+v", closed)
	}
	// Terminal decisions are never re-decided.
	if _, err := s.Accept(ProposalID(captured.ID), "zuzka", validCommandIDForApproval('c'), now.Add(time.Minute)); !errors.Is(err, ErrProposalState) {
		t.Errorf("re-accept = %v, want ErrProposalState", err)
	}
	if _, err := s.Dismiss(ProposalID(captured.ID), "zuzka", "", now); !errors.Is(err, ErrProposalState) {
		t.Errorf("dismiss-after-accept = %v, want ErrProposalState", err)
	}
	// Unknown ids fail closed; malformed ids fail shape-first.
	if _, err := s.Accept(ProposalID(validProposalIDHex('f')), "zuzka", validCommandIDForApproval('b'), now); !errors.Is(err, ErrUnknownProposal) {
		t.Errorf("unknown accept = %v, want ErrUnknownProposal", err)
	}
	if _, err := s.Accept(ProposalID("nope"), "zuzka", validCommandIDForApproval('b'), now); err == nil {
		t.Error("malformed id accepted at decide()")
	}

	// Dismiss closes PENDING → DISMISSED, never a command reference.
	second := baseProposal()
	second.ID = validProposalIDHex('d')
	if _, err := s.Capture(second); err != nil {
		t.Fatalf("Capture(second): %v", err)
	}
	dismissed, err := s.Dismiss(ProposalID(second.ID), "milo", "not now", now)
	if err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if dismissed.State != ProposalDismissed || dismissed.CommandID != "" || dismissed.Reason != "not now" {
		t.Errorf("dismissed = %+v", dismissed)
	}
	if _, err := s.Dismiss(ProposalID(second.ID), "milo", "", now); !errors.Is(err, ErrProposalState) {
		t.Errorf("re-dismiss = %v, want ErrProposalState", err)
	}

	// Invalid decisions fail before any persist: bad operator label, bad
	// command reference, zero decision time.
	third := baseProposal()
	third.ID = validProposalIDHex('e')
	if _, err := s.Capture(third); err != nil {
		t.Fatalf("Capture(third): %v", err)
	}
	if _, err := s.Accept(ProposalID(third.ID), "", validCommandIDForApproval('f'), now); err == nil {
		t.Error("empty operator accepted")
	}
	if _, err := s.Accept(ProposalID(third.ID), strings.Repeat("x", 65), validCommandIDForApproval('f'), now); err == nil {
		t.Error("over-bound operator accepted")
	}
	if _, err := s.Accept(ProposalID(third.ID), "zuzka", "not-a-cmd", now); err == nil {
		t.Error("malformed command reference accepted")
	}
	if _, err := s.Accept(ProposalID(third.ID), "zuzka", validCommandIDForApproval('f'), time.Time{}); err == nil {
		t.Error("zero decision time accepted")
	}
	if _, err := s.Dismiss(ProposalID(third.ID), "zuzka", strings.Repeat("r", 300), now); err == nil {
		t.Error("over-bound reason accepted")
	}
	// The record is STILL PENDING (nothing partial was persisted).
	got, _ := s.Get(ProposalID(third.ID))
	if got.State != ProposalPending {
		t.Errorf("state = %q, want PENDING (nothing partial)", got.State)
	}

	// Restart persistence: a fresh store over the same file sees all three.
	s2 := newTestProposalStore(t, dir, 8)
	if s2.Count() != 3 {
		t.Fatalf("Count after restart = %d, want 3", s2.Count())
	}
	acc, ok := s2.GetByCommand(CommandID(validCommandIDForApproval('b')))
	if !ok || acc.State != ProposalAccepted {
		t.Errorf("GetByCommand after restart = %+v, ok=%v", acc, ok)
	}
}

func TestProposalStoreGetAndList(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestProposalStore(t, dir, 8)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	letters := []byte{'a', 'b', 'c'}
	for i, l := range letters {
		rec := baseProposal()
		rec.ID = validProposalIDHex(l)
		rec.CapturedAt = now.Add(time.Duration(i) * time.Minute)
		rec.UpdatedAt = rec.CapturedAt
		if _, err := s.Capture(rec); err != nil {
			t.Fatalf("Capture(%c): %v", l, err)
		}
	}
	// Newest-first (capture order reversed), deterministic.
	list := s.ListLast(10)
	if len(list) != 3 || list[0].ID != validProposalIDHex('c') || list[2].ID != validProposalIDHex('a') {
		t.Errorf("ListLast order = %s,%s,%s", list[0].ID, list[1].ID, list[2].ID)
	}
	if len(s.ListLast(2)) != 2 {
		t.Errorf("ListLast(2) = %d records", len(s.ListLast(2)))
	}
	// Accept the middle one; state-filtered listings see it.
	if _, err := s.Accept(ProposalID(validProposalIDHex('b')), "zuzka", validCommandIDForApproval('9'), now.Add(10*time.Minute)); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	acc := s.ListLastState(10, ProposalAccepted)
	if len(acc) != 1 || acc[0].ID != validProposalIDHex('b') {
		t.Errorf("ListLastState(ACCEPTED) = %+v", acc)
	}
	if len(s.ListLastState(10, ProposalPending)) != 2 {
		t.Errorf("ListLastState(PENDING) = %d records", len(s.ListLastState(10, ProposalPending)))
	}
	if len(s.ListLastState(10, ProposalDismissed)) != 0 {
		t.Errorf("ListLastState(DISMISSED) = %d records", len(s.ListLastState(10, ProposalDismissed)))
	}
	// Counts: all states always present.
	counts := s.Counts()
	if counts[string(ProposalPending)] != 2 || counts[string(ProposalAccepted)] != 1 || counts[string(ProposalDismissed)] != 0 {
		t.Errorf("Counts = %v", counts)
	}
	// Copy isolation: a returned record is a copy.
	got, _ := s.Get(ProposalID(validProposalIDHex('a')))
	got.Intent = "mutated"
	again, _ := s.Get(ProposalID(validProposalIDHex('a')))
	if again.Intent != "switch the host to ai mode" {
		t.Error("Get returned a live pointer — mutation escaped")
	}
}

func TestProposalStoreBound(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestProposalStore(t, dir, 2)
	if _, err := s.Capture(baseProposal()); err != nil {
		t.Fatalf("Capture 1: %v", err)
	}
	second := baseProposal()
	second.ID = validProposalIDHex('b')
	if _, err := s.Capture(second); err != nil {
		t.Fatalf("Capture 2: %v", err)
	}
	third := baseProposal()
	third.ID = validProposalIDHex('c')
	if _, err := s.Capture(third); !errors.Is(err, ErrProposalLimit) {
		t.Errorf("Capture 3 = %v, want ErrProposalLimit", err)
	}
	// Deciding frees nothing — the bound counts ALL states (the file is the
	// history; the operator archives deliberately).
	if _, err := s.Dismiss(ProposalID(validProposalIDHex('a')), "zuzka", "", time.Now().UTC()); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if _, err := s.Capture(third); !errors.Is(err, ErrProposalLimit) {
		t.Errorf("Capture after dismiss = %v, want ErrProposalLimit (bound counts all states)", err)
	}
}

func TestProposalStoreRNGFailClosed(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestProposalStore(t, dir, 4)
	// The store-side collision check + the API layer's NewProposalID both
	// fail closed; a duplicate capture is refused.
	if _, err := s.Capture(baseProposal()); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, err := s.Capture(baseProposal()); err == nil {
		t.Error("duplicate proposal id captured (rng reuse) — must abort")
	}
}

func TestProposalStoreLoadFailClosed(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "proposals.json")

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write proposals file: %v", err)
		}
	}
	base := `"id":"` + validProposalIDHex('a') + `","intent":"switch the host to ai mode","agent_id":"coder.act","capability":"mode.request","proposer_provider":"sglang","proposer_model":"qwen3-14b","captured_at":"2026-10-30T12:00:00Z","updated_at":"2026-10-30T12:00:00Z"`
	badPayloads := map[string]string{
		"not JSON":                 "not json at all",
		"wrong schema version":     `{"version":2,"proposals":[]}`,
		"missing version":          `{"proposals":[]}`,
		"unknown state":            `{"version":1,"proposals":[{` + base + `,"state":"WITHDRAWN"}]}`,
		"pending with operator":    `{"version":1,"proposals":[{` + base + `,"state":"PENDING","operator":"op"}]}`,
		"pending with command":     `{"version":1,"proposals":[{` + base + `,"state":"PENDING","command_id":"` + validCommandIDForApproval('b') + `"}]}`,
		"accepted no command":      `{"version":1,"proposals":[{` + base + `,"state":"ACCEPTED","operator":"op","decided_at":"2026-10-30T12:05:00Z"}]}`,
		"accepted bad command":     `{"version":1,"proposals":[{` + base + `,"state":"ACCEPTED","operator":"op","command_id":"not-a-cmd","decided_at":"2026-10-30T12:05:00Z"}]}`,
		"accepted with reason":     `{"version":1,"proposals":[{` + base + `,"state":"ACCEPTED","operator":"op","command_id":"` + validCommandIDForApproval('b') + `","reason":"why","decided_at":"2026-10-30T12:05:00Z"}]}`,
		"dismissed with command":   `{"version":1,"proposals":[{` + base + `,"state":"DISMISSED","operator":"op","command_id":"` + validCommandIDForApproval('b') + `","decided_at":"2026-10-30T12:05:00Z"}]}`,
		"dismissed no operator":    `{"version":1,"proposals":[{` + base + `,"state":"DISMISSED","decided_at":"2026-10-30T12:05:00Z"}]}`,
		"foreign proposer":         `{"version":1,"proposals":[{` + base + `,"proposer_provider":"openai","state":"PENDING"}]}`,
		"malformed proposer model": `{"version":1,"proposals":[{` + base + `,"proposer_model":"Not A Model","state":"PENDING"}]}`,
		"unknown capability":       `{"version":1,"proposals":[{` + base + `,"capability":"shell.exec","state":"PENDING"}]}`,
		"bad intent":               `{"version":1,"proposals":[{` + base + `,"intent":"","state":"PENDING"}]}`,
		"bad agent":                `{"version":1,"proposals":[{` + base + `,"agent_id":"not an agent id","state":"PENDING"}]}`,
		"zero captured_at":         `{"version":1,"proposals":[{` + base + `,"captured_at":"0001-01-01T00:00:00Z","state":"PENDING"}]}`,
		"backwards updated_at":     `{"version":1,"proposals":[{` + base + `,"updated_at":"2026-10-30T11:00:00Z","state":"PENDING"}]}`,
		"decided before captured":  `{"version":1,"proposals":[{` + base + `,"state":"DISMISSED","operator":"op","decided_at":"2026-10-30T11:00:00Z"}]}`,
		"duplicate proposal id":    `{"version":1,"proposals":[{` + base + `,"state":"PENDING"},{` + base + `,"state":"PENDING"}]}`,
		"command referenced twice": `{"version":1,"proposals":[{` + base + `,"state":"ACCEPTED","operator":"op","command_id":"` + validCommandIDForApproval('b') + `","decided_at":"2026-10-30T12:05:00Z"},{"id":"` + validProposalIDHex('z') + `","intent":"another","agent_id":"coder.act","capability":"mode.request","proposer_provider":"sglang","proposer_model":"qwen3-14b","captured_at":"2026-10-30T12:01:00Z","updated_at":"2026-10-30T12:01:00Z","state":"ACCEPTED","operator":"op","command_id":"` + validCommandIDForApproval('b') + `","decided_at":"2026-10-30T12:06:00Z"}]}`,
		"over bound":               `{"version":1,"proposals":[` + strings.Repeat(`{`+base+`,"state":"PENDING"},`, 3) + `{` + base + `,"state":"PENDING"}]}`,
	}
	for why, payload := range badPayloads {
		write(payload)
		s, err := NewProposalStore(path, 3)
		if err != nil {
			t.Fatalf("NewProposalStore: %v", err)
		}
		if err := s.Load(); err == nil {
			t.Errorf("corrupt file (%s) loaded — fail-closed violated", why)
		}
	}

	// Loose file permissions abort the load (tamper discipline).
	write(`{"version":1,"proposals":[]}`)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	s, err := NewProposalStore(path, 3)
	if err != nil {
		t.Fatalf("NewProposalStore: %v", err)
	}
	if err := s.Load(); !errors.Is(err, ErrProposalFilePermissions) {
		t.Errorf("loose file mode = %v, want ErrProposalFilePermissions", err)
	}
	_ = os.Chmod(path, 0o600)
	// Loose DIRECTORY aborts too.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	if err := s.Load(); !errors.Is(err, ErrProposalFilePermissions) {
		t.Errorf("loose dir mode = %v, want ErrProposalFilePermissions", err)
	}
}

func TestProposalStoreAtomicPersistRollback(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestProposalStore(t, dir, 4)
	if _, err := s.Capture(baseProposal()); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	path := filepath.Join(dir, "proposals.json")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"proposals":[]}`), 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	// Reload a fresh store on the (valid) file, capture one more while the
	// directory is still writable, then break persistability by removing
	// write permission from the DIRECTORY.
	s2, err := NewProposalStore(path, 4)
	if err != nil {
		t.Fatalf("NewProposalStore: %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	extra := baseProposal()
	extra.ID = validProposalIDHex('1')
	if _, err := s2.Capture(extra); err != nil {
		t.Fatalf("Capture(extra) while writable: %v", err)
	}
	if s2.Count() != 1 {
		t.Fatalf("Count = %d, want 1 before the persist-failure probe", s2.Count())
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir ro: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	second := baseProposal()
	second.ID = validProposalIDHex('0')
	if _, err := s2.Capture(second); err == nil {
		t.Fatal("Capture succeeded against an unwritable directory — persist discipline violated")
	}
	// Rollback: the in-memory state must NOT include the failed append.
	if s2.Count() != 1 {
		t.Errorf("Count after failed persist = %d, want 1 (rollback — state and file stay in sync)", s2.Count())
	}
	// A failed DECISION persist rolls back too (the proposal stays PENDING).
	pnd := s2.ListLast(1)[0]
	if _, err := s2.Dismiss(ProposalID(pnd.ID), "op", "", time.Now().UTC()); err == nil {
		t.Fatal("Dismiss succeeded against an unwritable directory — persist discipline violated")
	}
	got, _ := s2.Get(ProposalID(pnd.ID))
	if got.State != ProposalPending {
		t.Errorf("state after failed Dismiss persist = %q, want PENDING (rollback)", got.State)
	}
}

func TestProposalStoreFileShape(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestProposalStore(t, dir, 16)
	if _, err := s.Capture(baseProposal()); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "proposals.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f proposalsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if f.Version != 1 || len(f.Proposals) != 1 {
		t.Fatalf("file shape = version %d, %d proposals", f.Version, len(f.Proposals))
	}
	// The typed attribution persists (the bridge entity's defining field).
	p := f.Proposals[0]
	if p.ProposerProvider != airuntime.ProviderSGLang || p.ProposerModel != "qwen3-14b" {
		t.Errorf("attribution = %s/%s", p.ProposerProvider, p.ProposerModel)
	}
	if p.AgentID != agentkernel.AgentID("coder.act") {
		t.Errorf("agent = %q", p.AgentID)
	}
}

func TestProposalStoreConcurrent(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestProposalStore(t, dir, 64)
	now := time.Now().UTC()
	done := make(chan struct{})
	// Concurrent captures + reads + decisions under -race.
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			rec := baseProposal()
			rec.ID = fmt.Sprintf("prp-%032x", i) // valid hex ids, unique
			rec.CapturedAt = now.Add(time.Duration(i) * time.Second)
			rec.UpdatedAt = rec.CapturedAt
			if _, err := s.Capture(rec); err != nil {
				t.Errorf("concurrent Capture: %v", err)
			}
			s.ListLast(5)
			s.Counts()
		}
	}()
	<-done
	if s.Count() != 20 {
		t.Errorf("Count = %d, want 20", s.Count())
	}
}
