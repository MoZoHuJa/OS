// Package admission — the durable admission ledger (v19.7.1).
//
// The ledger is the CAPACITY AUTHORITY: observed inventory numbers are
// only a bound; what is actually allocatable is observed − reserved −
// safety, and "reserved" lives HERE, durably, with identity (admission
// ID, request fingerprint, boot identity, lease expiry). The atomic
// reserve pattern (roadmap §6.4 steps 5-9) holds the ledger lock across
// the whole check-and-persist; a persist failure rolls the hold back.
//
// Crash recovery (the same discipline the token-budget ledger got in
// v19.7.1, KPK-19.6.7-carry-forward-P2-06): a record whose boot identity
// is not this process's is released at load — a dead process's workload
// start can no longer complete — and the release is RECORDED (bounded
// audit trail). Lease expiry frees holds whose workload never confirmed
// — also on the record.
package admission

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// LedgerSchemaVersion is the durable store's schema identity.
const LedgerSchemaVersion = 1

// ledgerRetained is how many non-HELD records the audit trail keeps
// (most recent first — evidence, not an infinite log).
const ledgerRetained = 64

// bootIdentity is this process's admission-ledger boot ID (a new one per
// process start — the crash-recovery discriminator).
var (
	bootOnce  sync.Once
	bootValue string
)

func bootIdentity() string {
	bootOnce.Do(func() {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			bootValue = fmt.Sprintf("boot-%d", time.Now().UnixNano())
			return
		}
		bootValue = "boot-" + hex.EncodeToString(b)
	})
	return bootValue
}

// Record is one admitted hold (durable, identity-carrying).
type Record struct {
	AdmissionID    string             `json:"admission_id"`
	RequestID      string             `json:"request_id"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
	Principal      string             `json:"principal"`
	ModelID        string             `json:"model_id"`
	BackendID      string             `json:"backend_id"`
	SnapshotID     string             `json:"snapshot_id"`
	Requirements   ResourceVector     `json:"requirements"`
	Assignment     ResourceAssignment `json:"assignment"`
	Status         string             `json:"status"` // HELD | RELEASED | EXPIRED | CRASH_RECOVERED
	CreatedAt      time.Time          `json:"created_at"`
	LeaseExpiresAt time.Time          `json:"lease_expires_at"`
	BootID         string             `json:"boot_id"`
	// ClosedAt/CloseReason — set when the record left HELD.
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	CloseReason string     `json:"close_reason,omitempty"`
}

// LedgerRetained returns the audit-trail retention bound (tests).
func LedgerRetained() int { return ledgerRetained }

// ledgerJSON is the on-disk shape.
type ledgerJSON struct {
	SchemaVersion int      `json:"schema_version"`
	Records       []Record `json:"records"`
}

// Ledger is the durable, mutex-guarded admission ledger. A nil-path
// ledger is MEMORY-ONLY (tests; production always passes a path).
type Ledger struct {
	mu   sync.Mutex
	path string
	recs []Record
}

// NewLedger constructs the ledger, loads the durable file, and
// reconciles: boot-mismatched records are crash-released (on the
// record), expired leases are expired (on the record), and the
// reconciled state persists BEFORE the ledger serves. Construction
// fail-closes on a corrupt/foreign-schema file.
func NewLedger(path string) (*Ledger, error) {
	l := &Ledger{path: path, recs: []Record{}}
	if path == "" {
		l.reconcileLocked(time.Now().UTC()) // memory-only: still coherent
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("admission ledger: %v (fail closed)", err)
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if werr := l.reconcileAndPersist(time.Now().UTC()); werr != nil {
			return nil, werr
		}
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("admission ledger: reading %s failed: %v (fail closed)", path, err)
	}
	if info, serr := os.Stat(path); serr == nil && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("admission ledger: %s is group/world-accessible (%v) — chmod 600; capacity accounting is not ambient", path, info.Mode().Perm())
	}
	var doc ledgerJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("admission ledger: %s is corrupt: %v (fix or archive it deliberately — never run on a tampered capacity ledger)", path, err)
	}
	if doc.SchemaVersion != LedgerSchemaVersion {
		return nil, fmt.Errorf("admission ledger: %s carries schema_version %d, expected %d (a future migration, not a silent reinterpretation)", path, doc.SchemaVersion, LedgerSchemaVersion)
	}
	if doc.Records == nil {
		doc.Records = []Record{}
	}
	l.recs = doc.Records
	if err := l.reconcileAndPersist(time.Now().UTC()); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Ledger) reconcileAndPersist(now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reconcileLocked(now)
	return l.persistLocked()
}

// reconcileLocked (caller holds the mutex): crash + lease recovery.
func (l *Ledger) reconcileLocked(now time.Time) {
	boot := bootIdentity()
	kept := l.recs[:0]
	for _, r := range l.recs {
		if r.Status != StatusHeld {
			kept = append(kept, r)
			continue
		}
		switch {
		case r.BootID != boot:
			closed := now
			r.Status = StatusCrashRecovered
			r.ClosedAt = &closed
			r.CloseReason = fmt.Sprintf("boot identity mismatch (held by %s, this process is %s) — the workload start died with its process; capacity released at reconstruction", r.BootID, boot)
			kept = append(kept, r)
		case !r.LeaseExpiresAt.After(now):
			closed := now
			r.Status = StatusExpired
			r.ClosedAt = &closed
			r.CloseReason = "lease expired before release/renewal — capacity freed by reconciliation"
			kept = append(kept, r)
		default:
			kept = append(kept, r)
		}
	}
	l.recs = trimTrail(kept)
}

// trimTrail bounds the non-HELD audit trail.
func trimTrail(recs []Record) []Record {
	held := 0
	for _, r := range recs {
		if r.Status == StatusHeld {
			held++
		}
	}
	closed := len(recs) - held
	if closed <= ledgerRetained {
		return recs
	}
	drop := closed - ledgerRetained
	out := recs[:0]
	for _, r := range recs {
		if drop > 0 && r.Status != StatusHeld {
			drop--
			continue
		}
		out = append(out, r)
	}
	return out
}

// newAdmissionID mints a record identity.
func newAdmissionID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "adm-" + hex.EncodeToString(b), nil
}

// held returns the HELD records (a stable copy, sorted by AdmissionID —
// deterministic for callers; the caller must not mutate).
func (l *Ledger) held() []Record {
	out := make([]Record, 0, len(l.recs))
	for _, r := range l.recs {
		if r.Status == StatusHeld {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AdmissionID < out[j].AdmissionID })
	return out
}

// byIdempotency finds a HELD record with the same key (idempotent admit).
func (l *Ledger) byIdempotency(key string) *Record {
	if key == "" {
		return nil
	}
	for i := range l.recs {
		if l.recs[i].Status == StatusHeld && l.recs[i].IdempotencyKey == key {
			r := l.recs[i]
			return &r
		}
	}
	return nil
}

// insertLocked appends a new HELD record (caller holds the mutex).
func (l *Ledger) insertLocked(r Record) {
	l.recs = append(l.recs, r)
}

// persistLocked writes atomically (tmp + rename, 0600).
func (l *Ledger) persistLocked() error {
	if l.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(&ledgerJSON{SchemaVersion: LedgerSchemaVersion, Records: l.recs}, "", "  ")
	if err != nil {
		return fmt.Errorf("admission ledger: %v", err)
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("admission ledger: writing %s failed: %v", tmp, err)
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return fmt.Errorf("admission ledger: renaming %s into place failed: %v", l.path, err)
	}
	return nil
}

// release closes a HELD record (verified cleanup: the transition is
// persisted atomically; releasing a non-HELD record is a NAMED error,
// never a silent no-op).
func (l *Ledger) release(admissionID, reason string, now time.Time) (Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.recs {
		if l.recs[i].AdmissionID == admissionID {
			if l.recs[i].Status != StatusHeld {
				return l.recs[i], fmt.Errorf("admission %s is %s, not HELD — nothing to release (verified cleanup refuses a double release)", admissionID, l.recs[i].Status)
			}
			closed := now
			l.recs[i].Status = StatusReleased
			l.recs[i].ClosedAt = &closed
			l.recs[i].CloseReason = reason
			out := l.recs[i]
			if err := l.persistLocked(); err != nil {
				// Roll back the in-memory transition — the durable state wins.
				l.recs[i].Status = StatusHeld
				l.recs[i].ClosedAt = nil
				l.recs[i].CloseReason = ""
				return out, fmt.Errorf("admission ledger: release persist failed, the hold stands: %v", err)
			}
			return out, nil
		}
	}
	return Record{}, fmt.Errorf("admission %s not found", admissionID)
}

// withLock runs fn under the ledger lock and persists on success — the
// atomic check-and-reserve seam the Manager uses (fn mutates the ledger
// through the provided view; a false return aborts without persisting).
func (l *Ledger) withLock(fn func(view *ledgerView) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot := len(l.recs)
	if err := fn(&ledgerView{l: l}); err != nil {
		if len(l.recs) > snapshot {
			l.recs = l.recs[:snapshot] // roll back the append
		}
		return err
	}
	if err := l.persistLocked(); err != nil {
		if len(l.recs) > snapshot {
			l.recs = l.recs[:snapshot] // the hold is NOT granted unpersisted
		}
		return err
	}
	return nil
}

// ledgerView exposes the ledger's HELD state to the Manager's check
// phase (caller of withLock holds the mutex).
type ledgerView struct {
	l *Ledger
}

func (v *ledgerView) held() []Record { return v.l.held() }

func (v *ledgerView) insert(r Record) { v.l.insertLocked(r) }

// Records returns every record (stable copy, sorted by AdmissionID).
func (l *Ledger) Records() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Record, len(l.recs))
	copy(out, l.recs)
	sort.Slice(out, func(i, j int) bool { return out[i].AdmissionID < out[j].AdmissionID })
	return out
}

// sweepExpired closes HELD records whose lease expired at/before now
// (persisted atomically; every closure is on the record). Returns the
// number closed.
func (l *Ledger) sweepExpired(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	closed := 0
	for i := range l.recs {
		if l.recs[i].Status != StatusHeld || l.recs[i].LeaseExpiresAt.After(now) {
			continue
		}
		closedAt := now
		l.recs[i].Status = StatusExpired
		l.recs[i].ClosedAt = &closedAt
		l.recs[i].CloseReason = "lease expired before release/renewal — capacity freed by reconciliation"
		closed++
	}
	if closed > 0 {
		_ = l.persistLocked() // best-effort sweep persist; the next write re-asserts
	}
	return closed
}

// BootIdentity exposes the process boot identity (operator surfaces).
func BootIdentity() string { return bootIdentity() }
