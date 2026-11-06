package agentfabric

// CF-3 (v19.6.2): the command store's proposal linkage tests — the
// additive proposal_id field, SubmitWithProposal (the accept path's
// capture), GetByProposal (the reconciliation lookup), one-command-per-
// proposal enforced on the mutation side AND at load.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentkernel"
)

func TestSubmitWithProposalLinkage(t *testing.T) {
	dir := fabricTestDir(t)
	s := newTestStore(t, dir, 16)
	now := time.Now().UTC()

	prp := ProposalID(validProposalIDHex('a'))
	rec, err := s.SubmitWithProposal("switch the host to ai mode", "coder.act", agentkernel.CapabilityModeRequest, "zuzka", prp, now)
	if err != nil {
		t.Fatalf("SubmitWithProposal: %v", err)
	}
	if rec.ProposalID != string(prp) {
		t.Errorf("proposal linkage = %q, want %q", rec.ProposalID, prp)
	}
	if rec.Submitter != "zuzka" {
		t.Errorf("submitter = %q, want zuzka (the accepting operator)", rec.Submitter)
	}
	if rec.WorkspaceID != "" {
		t.Errorf("workspace = %q, want empty — proposals are unscoped by design", rec.WorkspaceID)
	}
	if rec.State != CommandSubmitted {
		t.Errorf("state = %q, want SUBMITTED", rec.State)
	}

	// GetByProposal finds it (the reconciliation lookup).
	got, ok := s.GetByProposal(prp)
	if !ok || got.ID != rec.ID {
		t.Errorf("GetByProposal = %+v, ok=%v (want the captured command)", got, ok)
	}
	// An unknown (but well-formed) proposal has no command.
	if _, ok := s.GetByProposal(ProposalID(validProposalIDHex('f'))); ok {
		t.Error("GetByProposal answered for an unlinked proposal")
	}
	// A malformed proposal id never matches.
	if _, ok := s.GetByProposal(ProposalID("nope")); ok {
		t.Error("GetByProposal answered for a malformed id")
	}

	// ONE COMMAND PER PROPOSAL, mutation side: a second capture for the
	// same linkage is refused.
	if _, err := s.SubmitWithProposal("another intent", "coder.act", agentkernel.CapabilityModeRequest, "zuzka", prp, now.Add(time.Minute)); err == nil {
		t.Error("a second command captured for one proposal — the invariant is one command per proposal, ever")
	}
	// A malformed linkage is refused at submit.
	if _, err := s.SubmitWithProposal("x", "coder.act", agentkernel.CapabilityModeRequest, "zuzka", ProposalID("bad"), now); err == nil {
		t.Error("malformed proposal id accepted at SubmitWithProposal")
	}
	// Unlinked submissions are unaffected (the BF-1 path).
	if _, err := s.Submit("plain intent", "coder.act", agentkernel.CapabilityStatusRead, "", now); err != nil {
		t.Fatalf("unlinked Submit: %v", err)
	}

	// Restart: the linkage round-trips through the file.
	s2 := newTestStore(t, dir, 16)
	got2, ok := s2.GetByProposal(prp)
	if !ok || got2.ID != rec.ID || got2.ProposalID != string(prp) {
		t.Errorf("linkage lost across restart: %+v ok=%v", got2, ok)
	}

	// The commands file carries the additive field and stays schema v1.
	raw, err := os.ReadFile(filepath.Join(dir, "commands.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f commandsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if f.Version != commandsFileVersion {
		t.Errorf("schema version = %d (the additive field must NOT bump it)", f.Version)
	}
	found := false
	for _, c := range f.Commands {
		if c.ID == rec.ID {
			found = true
			if c.ProposalID != string(prp) {
				t.Errorf("file record linkage = %q", c.ProposalID)
			}
		}
	}
	if !found {
		t.Fatal("the linked command is missing from the file")
	}
}

func TestCommandLoadRejectsDuplicateProposalLinkage(t *testing.T) {
	dir := fabricTestDir(t)
	path := filepath.Join(dir, "commands.json")
	prp := validProposalIDHex('a')
	base := `"agent_id":"coder.act","capability":"mode.request","state":"SUBMITTED","submitted_at":"2026-10-30T12:00:00Z","updated_at":"2026-10-30T12:00:00Z","intent":"switch the host to ai mode","proposal_id":"` + prp + `"`
	payload := `{"version":1,"commands":[{"id":"` + validCommandIDForApproval('b') + `",` + base + `},{"id":"` + validCommandIDForApproval('c') + `",` + base + `}]}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := NewCommandStore(path, 16)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s.Load(); err == nil {
		t.Fatal("a file with TWO commands claiming one proposal loaded — one command per proposal, ever (fail-closed)")
	}

	// A malformed proposal_id on a record is refused too.
	bad := `{"version":1,"commands":[{"id":"` + validCommandIDForApproval('b') + `","agent_id":"coder.act","capability":"mode.request","state":"SUBMITTED","submitted_at":"2026-10-30T12:00:00Z","updated_at":"2026-10-30T12:00:00Z","intent":"x","proposal_id":"not-a-prp"}]}`
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s2, err := NewCommandStore(path, 16)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s2.Load(); err == nil {
		t.Fatal("a malformed proposal_id record loaded (corrupt)")
	}

	// Distinct linkages are fine.
	good := `{"version":1,"commands":[{"id":"` + validCommandIDForApproval('b') + `","agent_id":"coder.act","capability":"mode.request","state":"SUBMITTED","submitted_at":"2026-10-30T12:00:00Z","updated_at":"2026-10-30T12:00:00Z","intent":"a","proposal_id":"` + validProposalIDHex('a') + `"},{"id":"` + validCommandIDForApproval('c') + `","agent_id":"coder.act","capability":"mode.request","state":"SUBMITTED","submitted_at":"2026-10-30T12:01:00Z","updated_at":"2026-10-30T12:01:00Z","intent":"b","proposal_id":"` + validProposalIDHex('d') + `"}]}`
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s3, err := NewCommandStore(path, 16)
	if err != nil {
		t.Fatalf("NewCommandStore: %v", err)
	}
	if err := s3.Load(); err != nil {
		t.Fatalf("distinct linkages must load: %v", err)
	}
	if _, ok := s3.GetByProposal(ProposalID(validProposalIDHex('a'))); !ok {
		t.Error("first linkage lost")
	}
	if _, ok := s3.GetByProposal(ProposalID(validProposalIDHex('d'))); !ok {
		t.Error("second linkage lost")
	}
}

func TestCommandRecordValidateProposalLinkage(t *testing.T) {
	now := time.Now().UTC()
	// A malformed linkage fails record validation (load AND persist paths).
	bad := CommandRecord{
		ID:          validCommandIDForApproval('b'),
		Intent:      "x",
		AgentID:     "coder.act",
		Capability:  agentkernel.CapabilityModeRequest,
		ProposalID:  strings.Repeat("p", 20), // malformed
		State:       CommandSubmitted,
		SubmittedAt: now,
		UpdatedAt:   now,
	}
	if err := bad.validate(); err == nil {
		t.Error("malformed proposal linkage accepted by validate()")
	}
}
