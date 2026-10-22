package agentkernel

// AK-4 (v19.4.5): append-only audit sink tests — construction fail-closed
// (loose file/dir permissions), line validation matrices per event type,
// append+read round-trips, append-only semantics, read bounds, malformed
// line reporting, count, close discipline, and persistence across sinks.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func auditTestTime() time.Time {
	return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
}

func auditTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newTestSink(t *testing.T) *AuditSink {
	t.Helper()
	sink, err := NewAuditSink(filepath.Join(auditTestDir(t), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.Close() })
	return sink
}

func validEvaluateLine() AuditLine {
	allow := true
	return AuditLine{
		Schema:     auditFileSchemaVersion,
		Type:       AuditTypeEvaluate,
		Timestamp:  auditTestTime(),
		SessionID:  "aks-0123456789abcdef",
		AgentID:    "coder.agent-one",
		Capability: CapabilityStatusRead,
		Allow:      &allow,
		Reason:     "grant: agent coder.agent-one holds capability status.read",
	}
}

func TestNewAuditSink_Construction(t *testing.T) {
	if _, err := NewAuditSink(""); err == nil {
		t.Error("empty path must be a construction error")
	}
	// Loose directory fail-closed.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if _, err := NewAuditSink(filepath.Join(dir, "audit.jsonl")); !errors.Is(err, ErrAuditFilePermissions) {
		t.Errorf("loose dir must fail-closed with ErrAuditFilePermissions, got %v", err)
	}
	// Loose existing file fail-closed.
	dir2 := auditTestDir(t)
	path := filepath.Join(dir2, "audit.jsonl")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAuditSink(path); !errors.Is(err, ErrAuditFilePermissions) {
		t.Errorf("0644 file must fail-closed with ErrAuditFilePermissions, got %v", err)
	}
	// Happy path: fresh file created 0600, lock file 0600.
	dir3 := auditTestDir(t)
	path3 := filepath.Join(dir3, "audit.jsonl")
	sink, err := NewAuditSink(path3)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	fi, err := os.Stat(path3)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("audit file mode = %o, want 0600", fi.Mode().Perm())
	}
	fl, err := os.Stat(path3 + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if fl.Mode().Perm() != 0600 {
		t.Errorf("audit lock mode = %o, want 0600", fl.Mode().Perm())
	}
}

func TestAuditSink_AppendReadRoundTrip(t *testing.T) {
	sink := newTestSink(t)

	// Evaluate lines: allow + deny.
	allowLine := validEvaluateLine()
	if err := sink.Append(allowLine); err != nil {
		t.Fatal(err)
	}
	deny := false
	denyLine := AuditLine{
		Schema:     auditFileSchemaVersion,
		Type:       AuditTypeEvaluate,
		Timestamp:  auditTestTime().Add(time.Second),
		SessionID:  "aks-0123456789abcdef",
		AgentID:    "coder.agent-one",
		Capability: CapabilityComputeReserve,
		Allow:      &deny,
		Reason:     "rule/capability-not-granted: agent coder.agent-one does not hold \"compute.reserve\"",
	}
	if err := sink.Append(denyLine); err != nil {
		t.Fatal(err)
	}
	// Admin acts.
	grantLine := AuditLine{
		Schema:     auditFileSchemaVersion,
		Type:       AuditTypeGrant,
		Timestamp:  auditTestTime().Add(2 * time.Second),
		AgentID:    "coder.agent-one",
		Capability: CapabilityStatusRead,
		Owner:      "zmor",
		Admin:      "admin.zmor",
	}
	if err := sink.Append(grantLine); err != nil {
		t.Fatal(err)
	}
	revokeLine := AuditLine{
		Schema:     auditFileSchemaVersion,
		Type:       AuditTypeRevoke,
		Timestamp:  auditTestTime().Add(3 * time.Second),
		AgentID:    "coder.agent-one",
		Capability: CapabilityStatusRead,
		Admin:      "admin.zmor",
	}
	if err := sink.Append(revokeLine); err != nil {
		t.Fatal(err)
	}
	disableLine := AuditLine{
		Schema:     auditFileSchemaVersion,
		Type:       AuditTypeDisable,
		Timestamp:  auditTestTime().Add(4 * time.Second),
		Capability: CapabilityComputeReserve,
		Admin:      "admin.zmor",
		Detail:     "capacity freeze",
	}
	if err := sink.Append(disableLine); err != nil {
		t.Fatal(err)
	}
	enableLine := AuditLine{
		Schema:     auditFileSchemaVersion,
		Type:       AuditTypeEnable,
		Timestamp:  auditTestTime().Add(5 * time.Second),
		Capability: CapabilityComputeReserve,
		Admin:      "admin.zmor",
	}
	if err := sink.Append(enableLine); err != nil {
		t.Fatal(err)
	}

	// Read: newest first, all 6.
	lines, errs, err := sink.ReadLast(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected malformed lines: %v", errs)
	}
	if len(lines) != 6 {
		t.Fatalf("ReadLast = %d lines, want 6", len(lines))
	}
	if lines[0].Type != AuditTypeEnable || lines[5].Type != AuditTypeEvaluate {
		t.Fatalf("order must be newest-first: first=%s last=%s", lines[0].Type, lines[5].Type)
	}
	if lines[5].Allow == nil || !*lines[5].Allow || lines[5].Reason == "" {
		t.Fatalf("evaluate line lost its decision: %+v", lines[5])
	}

	// Count.
	n, err := sink.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("Count = %d, want 6", n)
	}
}

func TestAuditSink_AppendOnlySemantics(t *testing.T) {
	path := filepath.Join(auditTestDir(t), "audit.jsonl")
	sink, err := NewAuditSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append(validEvaluateLine()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	// A NEW sink over the same file appends — history preserved.
	sink2, err := NewAuditSink(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sink2.Close()
	allow := false
	if err := sink2.Append(AuditLine{
		Schema:     auditFileSchemaVersion,
		Type:       AuditTypeEvaluate,
		Timestamp:  auditTestTime().Add(time.Minute),
		SessionID:  "aks-0123456789abcdef",
		AgentID:    "coder.agent-one",
		Capability: CapabilityStatusRead,
		Allow:      &allow,
		Reason:     "rule/capability-disabled: \"status.read\" is administratively disabled",
	}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) <= len(before) {
		t.Fatal("audit file must only grow (append-only)")
	}
	if string(after[:len(before)]) != string(before) {
		t.Fatal("existing audit bytes must never be rewritten")
	}
	lines, _, err := sink2.ReadLast(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("history lost across sinks: %d lines, want 2", len(lines))
	}
}

func TestAuditSink_LineValidation(t *testing.T) {
	sink := newTestSink(t)
	cases := []struct {
		name string
		line AuditLine
	}{
		{"bad schema", AuditLine{Schema: 2, Type: AuditTypeEnable, Timestamp: auditTestTime(), Capability: CapabilityStatusRead, Admin: "a"}},
		{"unknown type", AuditLine{Schema: 1, Type: AuditLineType("nuke"), Timestamp: auditTestTime()}},
		{"evaluate without session", AuditLine{Schema: 1, Type: AuditTypeEvaluate, Timestamp: auditTestTime(), AgentID: "a.b", Capability: CapabilityStatusRead, Allow: boolPtr(true), Reason: "r"}},
		{"evaluate without agent", AuditLine{Schema: 1, Type: AuditTypeEvaluate, Timestamp: auditTestTime(), SessionID: "aks-x", Capability: CapabilityStatusRead, Allow: boolPtr(true), Reason: "r"}},
		{"evaluate without capability", AuditLine{Schema: 1, Type: AuditTypeEvaluate, Timestamp: auditTestTime(), SessionID: "aks-x", AgentID: "a.b", Allow: boolPtr(true), Reason: "r"}},
		{"evaluate without decision", AuditLine{Schema: 1, Type: AuditTypeEvaluate, Timestamp: auditTestTime(), SessionID: "aks-x", AgentID: "a.b", Capability: CapabilityStatusRead, Reason: "r"}},
		{"evaluate without reason", AuditLine{Schema: 1, Type: AuditTypeEvaluate, Timestamp: auditTestTime(), SessionID: "aks-x", AgentID: "a.b", Capability: CapabilityStatusRead, Allow: boolPtr(true)}},
		{"grant without admin", AuditLine{Schema: 1, Type: AuditTypeGrant, Timestamp: auditTestTime(), AgentID: "a.b", Capability: CapabilityStatusRead, Owner: "zmor"}},
		{"grant without agent", AuditLine{Schema: 1, Type: AuditTypeGrant, Timestamp: auditTestTime(), Capability: CapabilityStatusRead, Admin: "a"}},
		{"revoke without capability", AuditLine{Schema: 1, Type: AuditTypeRevoke, Timestamp: auditTestTime(), AgentID: "a.b", Admin: "a"}},
		{"disable without admin", AuditLine{Schema: 1, Type: AuditTypeDisable, Timestamp: auditTestTime(), Capability: CapabilityStatusRead}},
		{"enable without capability", AuditLine{Schema: 1, Type: AuditTypeEnable, Timestamp: auditTestTime(), Admin: "a"}},
		{"zero timestamp", AuditLine{Schema: 1, Type: AuditTypeEnable, Capability: CapabilityStatusRead, Admin: "a"}},
	}
	for _, tc := range cases {
		if err := sink.Append(tc.line); !errors.Is(err, ErrAuditEvent) {
			t.Errorf("line %q must fail validation with ErrAuditEvent, got %v", tc.name, err)
		}
	}
	// Nothing invalid landed in the file.
	n, err := sink.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("invalid lines must not be written: Count = %d", n)
	}
}

func TestAuditSink_ReadLastBounds(t *testing.T) {
	sink := newTestSink(t)
	for i := 0; i < 25; i++ {
		l := validEvaluateLine()
		l.Timestamp = auditTestTime().Add(time.Duration(i) * time.Second)
		l.Reason = "grant: iteration " + string(rune('a'+i))
		if err := sink.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	// n=10 → last 10, newest first.
	lines, _, err := sink.ReadLast(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 10 {
		t.Fatalf("ReadLast(10) = %d, want 10", len(lines))
	}
	if lines[0].Reason != "grant: iteration y" { // i=24
		t.Fatalf("newest first violated: %q", lines[0].Reason)
	}
	// n=0 and n>max clamp to DefaultMaxAuditRead (200 > 25 → all).
	lines, _, err = sink.ReadLast(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 25 {
		t.Fatalf("ReadLast(0) must clamp to default and return all 25, got %d", len(lines))
	}
}

func TestAuditSink_MalformedLinesReportedNotHidden(t *testing.T) {
	path := filepath.Join(auditTestDir(t), "audit.jsonl")
	sink, err := NewAuditSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append(validEvaluateLine()); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	// Tamper: append garbage directly to the file.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"schema\":1,\"type\":\"nuke\"}\nnot json at all\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	sink2, err := NewAuditSink(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sink2.Close()
	lines, errs, err := sink2.ReadLast(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("valid lines must still be returned: %d", len(lines))
	}
	if len(errs) != 2 {
		t.Fatalf("both malformed lines must be REPORTED: %v", errs)
	}
}

func TestAuditSink_CloseDiscipline(t *testing.T) {
	sink := newTestSink(t)
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal("Close must be idempotent")
	}
	if err := sink.Append(validEvaluateLine()); !errors.Is(err, ErrAuditSinkClosed) {
		t.Fatalf("Append after Close must be ErrAuditSinkClosed, got %v", err)
	}
	if _, _, err := sink.ReadLast(10); !errors.Is(err, ErrAuditSinkClosed) {
		t.Fatalf("ReadLast after Close must be ErrAuditSinkClosed, got %v", err)
	}
}

func TestNewAuditLineFromEvent(t *testing.T) {
	ev := AuditEvent{
		Timestamp:  auditTestTime(),
		AgentID:    "coder.agent-one",
		SessionID:  "aks-abc",
		Capability: CapabilityStatusRead,
		Decision:   Decision{Allow: true, Reason: "grant: agent coder.agent-one holds capability status.read"},
	}
	l := NewAuditLineFromEvent(ev)
	if l.Type != AuditTypeEvaluate || l.SessionID != "aks-abc" || l.Allow == nil || !*l.Allow {
		t.Fatalf("conversion lost fields: %+v", l)
	}
	if err := l.validate(); err != nil {
		t.Fatalf("converted line must validate: %v", err)
	}
	// JSON round-trip keeps the decision pointer distinct (allow vs deny).
	data, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	var back AuditLine
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Allow == nil || *back.Allow != true {
		t.Fatal("allow flag lost in JSON round-trip")
	}
}

func boolPtr(b bool) *bool { return &b }
