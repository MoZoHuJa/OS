// AK-4 (v19.4.5): append-only audit sink — the persistence half of the
// kernel's "every decision is auditable" rule (strategy page 24). Until
// AK-3, audit events lived only in memory (returned with the evaluation);
// AK-4 appends every kernel decision and every admin act to an append-only
// JSONL file under /var/lib/scarlix-agentkernel/:
//
//   - evaluate events: one line per permission evaluation (allow AND deny —
//     a denial is a successful, auditable evaluation, the denial-is-200
//     contract's persistence twin);
//   - admin acts: grant / revoke / disable / enable — the acts that change
//     what the kernel may decide;
//   - the file is 0600 in a 0700 directory, append-only (no rewrites, no
//     deletes — the history is the artifact), flock-serialized for
//     multi-process writers (the same store discipline as the reservation
//     governor's stores);
//   - writes are fail-closed for EVALUATIONS: if the audit line cannot be
//     written, the decision is not served — an unauditable allow/deny is a
//     construction error, never a silent degradation. For admin acts the
//     registry state is the source of truth; the audit line is attempted
//     after a successful persist and a write failure is REPORTED (the
//     caller returns 500) even though the state change stands — honest
//     ordering, no rollback theater.
//
// The sink is optional (nil = the AK-2/AK-3 in-memory behavior — opt-in
// via SCARLIHQ_AGENT_AUDIT_FILE). Reads are bounded (last N lines, newest
// first) — an unbounded read endpoint would just be a file dump.

package agentkernel

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Sentinel errors for the audit sink.
var (
	// ErrAuditSinkClosed: the sink was already closed.
	ErrAuditSinkClosed = errors.New("agentkernel: audit sink is closed")
	// ErrAuditEvent: the event failed validation before the write.
	ErrAuditEvent = errors.New("agentkernel: invalid audit event")
	// ErrAuditFilePermissions: audit file/dir looser than 0600/0700.
	ErrAuditFilePermissions = errors.New("agentkernel: audit file permissions too loose")
)

// DefaultMaxAuditRead bounds GET /api/agent/audit (the last N lines). The
// audit file itself is append-only and unbounded in time (it IS the
// history); only the READ surface is bounded.
const DefaultMaxAuditRead = 200

// auditFileSchemaVersion is the per-line envelope version.
const auditFileSchemaVersion = 1

// AuditLineType discriminates what happened.
type AuditLineType string

const (
	AuditTypeEvaluate AuditLineType = "evaluate"
	AuditTypeGrant    AuditLineType = "grant"
	AuditTypeRevoke   AuditLineType = "revoke"
	AuditTypeDisable  AuditLineType = "disable"
	AuditTypeEnable   AuditLineType = "enable"
)

// AuditLine is one append-only JSONL record. One struct for all event
// types: evaluation fields are set for "evaluate", admin-act fields for
// grant/revoke/disable/enable — omitempty keeps the lines small.
type AuditLine struct {
	Schema     int           `json:"schema"`
	Type       AuditLineType `json:"type"`
	Timestamp  time.Time     `json:"timestamp"`
	SessionID  string        `json:"session_id,omitempty"`
	AgentID    AgentID       `json:"agent_id,omitempty"`
	Capability Capability    `json:"capability,omitempty"`
	// Decision (evaluate lines).
	Allow  *bool  `json:"allow,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Admin acts (grant/revoke/disable/enable lines).
	Owner  string `json:"owner,omitempty"`
	Admin  string `json:"admin,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// AuditSink is the append-only JSONL audit store. Safe for concurrent use;
// writes are flock-serialized across processes (LOCK_EX around the append,
// the reservation-store discipline) and mutex-serialized in-process.
type AuditSink struct {
	mu      sync.Mutex
	path    string
	lockPth string
	file    *os.File
	w       *bufio.Writer
	closed  bool
}

// NewAuditSink opens (or initializes) the audit sink at path. The file is
// created 0600 in a 0700 directory; EXISTING looser permissions are a
// construction failure (fail-closed, same tamper discipline as the grant
// registry — no auto-fix). The sink keeps ONE long-lived append fd +
// bufio.Writer; every line is flushed (and the fd fsynced) before Append
// returns, so a crash can lose at most the line being written, never a
// torn line.
func NewAuditSink(path string) (*AuditSink, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("agentkernel: audit sink path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("agentkernel: cannot create audit directory %s: %w", dir, err)
	}
	dInfo, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("agentkernel: cannot stat audit directory: %w", err)
	}
	if dInfo.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%w: directory %s has mode %o (want 0700)", ErrAuditFilePermissions, dir, dInfo.Mode().Perm())
	}
	fInfo, err := os.Stat(path)
	switch {
	case err == nil:
		if fInfo.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("%w: %s has mode %o (want 0600)", ErrAuditFilePermissions, path, fInfo.Mode().Perm())
		}
	case errors.Is(err, os.ErrNotExist):
		// first run — created below with 0600
	default:
		return nil, fmt.Errorf("agentkernel: cannot stat audit file %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("agentkernel: cannot open audit file %s: %w", path, err)
	}
	// Re-stat after create: OpenFile's mode is masked by umask on some
	// systems — verify what actually landed on disk (fail-closed honesty).
	vInfo, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("agentkernel: cannot stat opened audit file: %w", err)
	}
	if vInfo.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, fmt.Errorf("%w: %s has mode %o (want 0600)", ErrAuditFilePermissions, path, vInfo.Mode().Perm())
	}
	lockPth := path + ".lock"
	lf, err := os.OpenFile(lockPth, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("agentkernel: cannot open audit lock file %s: %w", lockPth, err)
	}
	defer lf.Close()
	lInfo, err := lf.Stat()
	if err == nil && lInfo.Mode().Perm() != 0600 {
		if err := os.Chmod(lockPth, 0600); err != nil {
			f.Close()
			return nil, fmt.Errorf("agentkernel: cannot tighten audit lock file mode: %w", err)
		}
	}
	return &AuditSink{
		path:    path,
		lockPth: lockPth,
		file:    f,
		w:       bufio.NewWriter(f),
	}, nil
}

// NewAuditLineFromEvent converts a kernel AuditEvent (evaluation) into the
// persisted line shape.
func NewAuditLineFromEvent(ev AuditEvent) AuditLine {
	allow := ev.Decision.Allow
	return AuditLine{
		Schema:     auditFileSchemaVersion,
		Type:       AuditTypeEvaluate,
		Timestamp:  ev.Timestamp,
		SessionID:  ev.SessionID,
		AgentID:    ev.AgentID,
		Capability: ev.Capability,
		Allow:      &allow,
		Reason:     ev.Decision.Reason,
	}
}

// validate checks one line before it is allowed into the artifact.
func (l AuditLine) validate() error {
	if l.Schema != auditFileSchemaVersion {
		return fmt.Errorf("%w: schema %d (want %d)", ErrAuditEvent, l.Schema, auditFileSchemaVersion)
	}
	switch l.Type {
	case AuditTypeEvaluate:
		if l.SessionID == "" {
			return fmt.Errorf("%w: evaluate line without session id", ErrAuditEvent)
		}
		if l.AgentID == "" {
			return fmt.Errorf("%w: evaluate line without agent id", ErrAuditEvent)
		}
		if l.Capability == "" {
			return fmt.Errorf("%w: evaluate line without capability", ErrAuditEvent)
		}
		if l.Allow == nil {
			return fmt.Errorf("%w: evaluate line without decision", ErrAuditEvent)
		}
		if l.Reason == "" {
			return fmt.Errorf("%w: evaluate line without reason (every decision names its rule/grant)", ErrAuditEvent)
		}
	case AuditTypeGrant, AuditTypeRevoke:
		if l.AgentID == "" {
			return fmt.Errorf("%w: %s line without agent id", ErrAuditEvent, l.Type)
		}
		if l.Capability == "" {
			return fmt.Errorf("%w: %s line without capability", ErrAuditEvent, l.Type)
		}
		if l.Admin == "" {
			return fmt.Errorf("%w: %s line without admin (admin acts name their admin)", ErrAuditEvent, l.Type)
		}
	case AuditTypeDisable, AuditTypeEnable:
		if l.Capability == "" {
			return fmt.Errorf("%w: %s line without capability", ErrAuditEvent, l.Type)
		}
		if l.Admin == "" {
			return fmt.Errorf("%w: %s line without admin", ErrAuditEvent, l.Type)
		}
	default:
		return fmt.Errorf("%w: unknown line type %q", ErrAuditEvent, l.Type)
	}
	if l.Timestamp.IsZero() {
		return fmt.Errorf("%w: zero timestamp", ErrAuditEvent)
	}
	return nil
}

// Append validates and appends one line under the cross-process flock,
// then flushes + fsyncs — the line is on disk (not in a buffer) when
// Append returns nil. Callers treat an error as failure (evaluate: the
// decision is NOT served; admin acts: the error is reported).
func (s *AuditSink) Append(line AuditLine) error {
	if err := line.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("agentkernel: audit line marshal: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrAuditSinkClosed
	}

	// Cross-process append discipline (reservation-store precedent): take
	// LOCK_EX on the sidecar lock file, write, flush, fsync, unlock.
	lf, err := os.OpenFile(s.lockPth, os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("agentkernel: audit lock: %w", err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("agentkernel: audit flock: %w", err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	if _, err := s.w.Write(data); err != nil {
		return fmt.Errorf("agentkernel: audit write: %w", err)
	}
	if err := s.w.Flush(); err != nil {
		return fmt.Errorf("agentkernel: audit flush: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("agentkernel: audit fsync: %w", err)
	}
	return nil
}

// ReadLast returns the last n lines (newest first), validated. Malformed
// lines are reported as errors but do not abort the read — an audit file
// with a bad line must be visible to the operator, not silently skipped.
// n <= 0 or > DefaultMaxAuditRead is clamped to DefaultMaxAuditRead.
func (s *AuditSink) ReadLast(n int) ([]AuditLine, []error, error) {
	if n <= 0 || n > DefaultMaxAuditRead {
		n = DefaultMaxAuditRead
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, ErrAuditSinkClosed
	}
	// Flush any buffered tail so the read sees everything appended.
	if err := s.w.Flush(); err != nil {
		return nil, nil, fmt.Errorf("agentkernel: audit flush before read: %w", err)
	}

	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []AuditLine{}, nil, nil // fresh sink, no events yet
		}
		return nil, nil, fmt.Errorf("agentkernel: audit read: %w", err)
	}
	defer f.Close()

	// Read the tail: scan the whole file (bounded by the line cap below),
	// keep a ring of the last n valid candidates.
	var (
		lines []AuditLine
		errs  []error
		sc    = bufio.NewScanner(f)
		count int
	)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB line cap — audit lines are small; a huge "line" is corruption
	for sc.Scan() {
		count++
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var l AuditLine
		if err := json.Unmarshal(raw, &l); err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", count, err))
			continue
		}
		if err := l.validate(); err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", count, err))
			continue
		}
		lines = append(lines, l)
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, errs, fmt.Errorf("agentkernel: audit scan: %w", err)
	}
	// Newest first.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines, errs, nil
}

// Count returns the number of validated lines in the file (bounded read
// of the whole file; the audit surface is small by contract).
func (s *AuditSink) Count() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrAuditSinkClosed
	}
	if err := s.w.Flush(); err != nil {
		return 0, fmt.Errorf("agentkernel: audit flush before count: %w", err)
	}
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("agentkernel: audit count: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		if len(strings.TrimSpace(sc.Text())) > 0 {
			n++
		}
	}
	return n, sc.Err()
}

// Path returns the audit file path.
func (s *AuditSink) Path() string { return s.path }

// Close flushes and closes the sink (idempotent).
func (s *AuditSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.w.Flush(); err != nil {
		s.file.Close()
		return fmt.Errorf("agentkernel: audit close flush: %w", err)
	}
	return s.file.Close()
}
