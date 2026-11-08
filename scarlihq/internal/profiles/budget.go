package profiles

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// The enforced monthly token budget (v19.6.7, KPK-19.6.6-P2-02).
//
// The v19.6.6 defect: the profile schema parsed and VALIDATED
// token_budget {monthly: N, model: m} — and nothing in the runtime ever
// read it. The budget was descriptive metadata displayed to the family
// while actual usage was unbounded.
//
// v19.6.7 makes the budget an ENFORCED quota on the /api/ai/chat path:
//
//   - a DURABLE usage ledger (JSON file, atomic tmp+rename persist, 0600
//     file / 0700 dir — the grants.json discipline): used tokens per
//     profile per month per model, plus in-flight reservations;
//   - RESERVE-BEFORE-FORWARD: the request reserves an estimate (input
//     estimate + a fixed output reserve) BEFORE the model is called —
//     concurrent requests cannot collectively overspend the month;
//   - SETTLE-AFTER-RESPONSE: the reservation is replaced by the
//     provider-reported usage (input + output); when the provider
//     reports none, the estimate stands (the documented fallback);
//   - FAILED attempts are NOT billed: an error path releases the
//     reservation whole (failover attempts, refusals, transport errors);
//   - the MONTHLY window is a UTC calendar month key ("2006-01") —
//     explicit, no local-time ambiguity; older months are pruned;
//   - accounting is per profile ACROSS MODELS (the declared model is the
//     expected/default model — routing a chat through a different model
//     must not escape the budget; per-model usage is tracked for the
//     report, the limit is the profile's monthly total);
//   - quota exhaustion is a STABLE 429 response carrying the quota state;
//   - ledger unavailability fails CLOSED (503): when a budget applies and
//     the ledger cannot account for it, the chat is refused — an
//     unaccountable budget is not a budget.
//
// v19.7.1 (KPK-19.6.7-carry-forward-P2-06) — CRASH-SAFE RESERVATIONS:
//
//   - every reservation is now a durable RECORD (unique ID, profile,
//     month, amount, created-at, BOOT IDENTITY), not an anonymous
//     aggregate counter;
//   - at STARTUP the ledger RECONCILES: reservations whose boot identity
//     is not this process's are RELEASED (a provider request from a dead
//     process cannot still be holding quota — the chat HTTP path is
//     synchronous; if the process died, the request died with it) and
//     the release is RECORDED in a bounded audit trail (Recovered) — an
//     explicit, visible recovery decision, never a silent permanent
//     quota loss and never an invisible carry-forward;
//   - reservation accounting is MONTH-SPECIFIC: an abandoned hold from a
//     previous month cannot consume the current month's budget (holds
//     beyond the retained window are dropped to the recovery trail);
//   - a v19.6.x ledger (schema 1, aggregate `reserved` map with no
//     identity) is MIGRATED: each profile's aggregate becomes a legacy
//     record which the very first reconciliation then releases (boot
//     mismatch by construction) — the old orphaned aggregates are
//     explicitly recovered, on the record, at upgrade time.
// ---------------------------------------------------------------------------

// ErrQuotaExceeded is the named refusal when the profile's monthly budget
// cannot cover the reservation.
var ErrQuotaExceeded = errors.New("profiles: token budget exhausted for the month")

// ErrLedgerUnavailable is the named fail-closed error when the durable
// ledger cannot account usage (I/O or persistence failure).
var ErrLedgerUnavailable = errors.New("profiles: token budget ledger unavailable (fail closed)")

// BudgetOutputReserveTokens is the per-request output reservation added
// to the input estimate: the amount a single answer may cost before the
// provider reports the true number. Deliberately generous for family
// usage, small against a monthly budget (default 4096 output tokens).
const BudgetOutputReserveTokens = 4096

// BudgetInputCharsPerToken is the documented input estimate heuristic:
// ~4 characters per token for the mixed Slovak/English family usage.
const BudgetInputCharsPerToken = 4

// budgetMonthsRetained is how many month keys the durable ledger keeps
// (the current month + the previous one — a rolling audit window).
const budgetMonthsRetained = 2

// budgetRecoveredRetained bounds the crash-recovery audit trail (the
// most recent releases — the trail is evidence, not an infinite log).
const budgetRecoveredRetained = 64

// budgetLegacyBootID marks migrated v1 aggregate reservations: they carry
// no process identity, so the first reconciliation releases them by
// construction (boot mismatch) — the release reason names the migration.
const budgetLegacyBootID = "legacy-v1-migration"

// budgetLedgerSchemaV2 is the current durable schema: reservation
// RECORDS with identity instead of the v1 aggregate counter.
const budgetLedgerSchemaV2 = 2

// EstimateChatTokens estimates the worst-case token cost of one chat
// request before it is forwarded: the input estimate (content runes /
// BudgetInputCharsPerToken, per message) plus the fixed output reserve.
// When the provider later reports real usage, the estimate is settled.
func EstimateChatTokens(messages []string) int64 {
	var chars int64
	for _, m := range messages {
		chars += int64(len([]rune(m)))
	}
	est := chars / BudgetInputCharsPerToken
	if est < 1 {
		est = 1
	}
	return est + BudgetOutputReserveTokens
}

// budgetReservationJSON is ONE durable in-flight hold (v19.7.1,
// KPK-19.6.7-carry-forward-P2-06): identity, month, amount, and the boot
// identity that created it — everything reconciliation needs to make an
// explicit release decision after a crash.
type budgetReservationJSON struct {
	ID        string    `json:"id"` // "res-" + 16 hex, unique
	Profile   string    `json:"profile"`
	Month     string    `json:"month"` // "2006-01" (UTC)
	Amount    int64     `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
	BootID    string    `json:"boot_id"` // the creating process's identity
}

// recoveredReservationJSON is the audit-trail entry for a reservation
// released by reconciliation (never silently dropped).
type recoveredReservationJSON struct {
	budgetReservationJSON
	RecoveredAt time.Time `json:"recovered_at"`
	Reason      string    `json:"reason"`
}

// budgetLedgerJSON is the durable on-disk shape (schema-versioned like
// every persisted store in this codebase).
type budgetLedgerJSON struct {
	SchemaVersion int                         `json:"schema_version"`
	Used          map[string]map[string]int64 `json:"used"`     // profile → month → tokens
	ByModel       map[string]map[string]int64 `json:"by_model"` // profile → model → tokens (report only)
	Reservations  []budgetReservationJSON     `json:"reservations"`
	Recovered     []recoveredReservationJSON  `json:"recovered,omitempty"` // bounded audit trail
}

// budgetLedgerV1JSON is the v19.6.x shape (aggregate reserved map, no
// reservation identity) — read ONLY for migration.
type budgetLedgerV1JSON struct {
	SchemaVersion int                         `json:"schema_version"`
	Used          map[string]map[string]int64 `json:"used"`
	ByModel       map[string]map[string]int64 `json:"by_model"`
	Reserved      map[string]int64            `json:"reserved"`
}

// bootIDOnce guards the per-process boot identity (crypto/rand — a new
// identity per process start; same process → same ID for every
// reservation it creates).
var (
	bootIDOnce  sync.Once
	bootIDValue string
)

func processBootID() string {
	bootIDOnce.Do(func() {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			// Deterministic fallback: an identity that differs per process
			// with overwhelming probability even on rand failure.
			bootIDValue = fmt.Sprintf("boot-%d", time.Now().UnixNano())
			return
		}
		bootIDValue = "boot-" + hex.EncodeToString(b)
	})
	return bootIDValue
}

// newReservationID mints a unique reservation identity.
func newReservationID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reservation id unavailable (crypto/rand): %w", err)
	}
	return "res-" + hex.EncodeToString(b), nil
}

// BudgetLedger is the durable, mutex-guarded usage ledger. A nil-path
// ledger is MEMORY-ONLY (tests; production always passes a path — the
// memory mode is loudly labeled in its construction error contract).
type BudgetLedger struct {
	mu   sync.Mutex
	path string // "" → memory-only (tests)
	data budgetLedgerJSON
}

// NewBudgetLedger constructs the ledger, loads the durable file when a
// path is given, migrates a v1 file, and RECONCILES stale reservations
// (KPK-19.6.7-carry-forward-P2-06). Construction fail-closes on a
// tampered/corrupt file (the grants.json discipline: never run on an
// unaccountable ledger); the directory is created 0700, the file is
// written 0600.
func NewBudgetLedger(path string) (*BudgetLedger, error) {
	l := &BudgetLedger{path: path, data: budgetLedgerJSON{
		SchemaVersion: budgetLedgerSchemaV2,
		Used:          map[string]map[string]int64{},
		ByModel:       map[string]map[string]int64{},
		Reservations:  []budgetReservationJSON{},
	}}
	if path == "" {
		return l, nil // memory-only — tests
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("budget ledger: %v (fail closed)", err)
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// A fresh ledger still reconciles (no-op) and persists the v2 shape.
		if werr := l.reconcileAndPersist(time.Now().UTC()); werr != nil {
			return nil, werr
		}
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("budget ledger: reading %s failed: %v (fail closed)", path, err)
	}
	if info, serr := os.Stat(path); serr == nil && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("budget ledger: %s is group/world-accessible (%v) — chmod 600; usage accounting is not ambient", path, info.Mode().Perm())
	}

	// Peek the schema version, then decode the matching shape.
	var peek struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return nil, fmt.Errorf("budget ledger: %s is corrupt: %v (fix or archive it deliberately — never run on a tampered ledger)", path, err)
	}
	switch peek.SchemaVersion {
	case budgetLedgerSchemaV2:
		if err := json.Unmarshal(raw, &l.data); err != nil {
			return nil, fmt.Errorf("budget ledger: %s is corrupt: %v (fix or archive it deliberately — never run on a tampered ledger)", path, err)
		}
		if l.data.Used == nil {
			l.data.Used = map[string]map[string]int64{}
		}
		if l.data.ByModel == nil {
			l.data.ByModel = map[string]map[string]int64{}
		}
		if l.data.Reservations == nil {
			l.data.Reservations = []budgetReservationJSON{}
		}
	case 1:
		// v19.6.x aggregate shape → migrate to records (no identity — the
		// legacy boot ID makes the first reconciliation release them, on
		// the record, instead of carrying an anonymous hold forward).
		var v1 budgetLedgerV1JSON
		if err := json.Unmarshal(raw, &v1); err != nil {
			return nil, fmt.Errorf("budget ledger: %s is corrupt: %v (fix or archive it deliberately — never run on a tampered ledger)", path, err)
		}
		now := time.Now().UTC()
		month := monthKey(now)
		l.data.Used = v1.Used
		if l.data.Used == nil {
			l.data.Used = map[string]map[string]int64{}
		}
		l.data.ByModel = v1.ByModel
		if l.data.ByModel == nil {
			l.data.ByModel = map[string]map[string]int64{}
		}
		l.data.Reservations = []budgetReservationJSON{}
		profiles := make([]string, 0, len(v1.Reserved))
		for p := range v1.Reserved {
			profiles = append(profiles, p)
		}
		sort.Strings(profiles)
		for _, p := range profiles {
			if v1.Reserved[p] <= 0 {
				continue
			}
			l.data.Reservations = append(l.data.Reservations, budgetReservationJSON{
				ID:        fmt.Sprintf("res-legacy-%s", p),
				Profile:   p,
				Month:     month,
				Amount:    v1.Reserved[p],
				CreatedAt: now,
				BootID:    budgetLegacyBootID,
			})
		}
	default:
		return nil, fmt.Errorf("budget ledger: %s carries schema_version %d, expected %d (a future migration, not a silent reinterpretation)", path, peek.SchemaVersion, budgetLedgerSchemaV2)
	}

	// Startup reconciliation (KPK-19.6.7-carry-forward-P2-06): boot-mismatched
	// and beyond-window reservations are released INTO THE AUDIT TRAIL —
	// no silent permanent quota loss, no invisible carry-forward. The
	// reconciled state is persisted BEFORE the ledger serves.
	if err := l.reconcileAndPersist(time.Now().UTC()); err != nil {
		return nil, err
	}
	return l, nil
}

// reconcileAndPersistLocked-shaped helper (locks internally): releases
// stale reservations and persists. Called at construction.
func (l *BudgetLedger) reconcileAndPersist(now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reconcileLocked(now)
	return l.persistLocked()
}

// reconcileLocked implements the documented recovery policy (caller
// holds the mutex):
//
//   - a reservation whose BootID is not THIS process's boot identity is
//     released: the /api/ai/chat request path is synchronous, so a
//     reservation held by a dead process can never settle — after a
//     restart the hold is orphaned by construction (the audit's option
//     1: "if no provider request could still be running after process
//     restart, release the stale hold");
//   - a reservation whose month fell out of the retained window is
//     dropped (it can no longer settle into any kept month);
//   - every release is recorded in the bounded Recovered trail with its
//     reason — the operator can audit what was returned and why.
func (l *BudgetLedger) reconcileLocked(now time.Time) {
	boot := processBootID()
	current := monthKey(now)
	cutoff := now.UTC().AddDate(0, -(budgetMonthsRetained - 1), 0).Format("2006-01")

	kept := l.data.Reservations[:0]
	for _, r := range l.data.Reservations {
		switch {
		case r.BootID != boot && r.BootID != "":
			l.recordRecoveryLocked(r, now, fmt.Sprintf(
				"boot identity mismatch (held by %s, this process is %s) — the synchronous chat request that held it died with its process; released at startup, not carried forward", r.BootID, boot))
		case r.BootID == "":
			l.recordRecoveryLocked(r, now, "reservation carried no boot identity (pre-v19.7.1 record) — unverifiable hold released at startup")
		case r.Month < cutoff || r.Month > current:
			l.recordRecoveryLocked(r, now, fmt.Sprintf(
				"month %s fell outside the retained window [%s, %s] — the hold can no longer settle; released", r.Month, cutoff, current))
		default:
			kept = append(kept, r)
		}
	}
	l.data.Reservations = kept
}

// recordRecoveryLocked appends a release to the bounded audit trail.
func (l *BudgetLedger) recordRecoveryLocked(r budgetReservationJSON, now time.Time, reason string) {
	l.data.Recovered = append(l.data.Recovered, recoveredReservationJSON{
		budgetReservationJSON: r,
		RecoveredAt:           now,
		Reason:                reason,
	})
	if len(l.data.Recovered) > budgetRecoveredRetained {
		l.data.Recovered = l.data.Recovered[len(l.data.Recovered)-budgetRecoveredRetained:]
	}
}

// monthKey is the UTC calendar-month key ("2006-01") — the budget window.
func monthKey(now time.Time) string { return now.UTC().Format("2006-01") }

// Reservation is one in-flight budget hold. Settle replaces it with the
// actual usage; Release returns it whole (failed attempts are not
// billed). A reservation is single-shot: after Settle/Release it is
// spent.
type Reservation struct {
	ledger   *BudgetLedger
	recordID string
	profile  string
	month    string
	amount   int64
	spent    bool
}

// Reserve checks and holds `estimate` tokens against the profile's
// `monthly` budget. The check counts used + already-reserved-in-THIS-
// month + this estimate — concurrent requests cannot collectively
// overspend, and an abandoned hold from a previous month cannot consume
// the current one (KPK-19.6.7-carry-forward-P2-06). The hold is a
// DURABLE RECORD (ID + boot identity) so a crash reconciles it visibly.
func (l *BudgetLedger) Reserve(profile string, monthly, estimate int64, now time.Time) (*Reservation, error) {
	if monthly <= 0 {
		return nil, fmt.Errorf("budget ledger: Reserve requires a positive monthly budget, got %d", monthly)
	}
	if estimate <= 0 {
		estimate = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	month := monthKey(now)
	l.pruneMonthsLocked(now)

	used := l.data.Used[profile][month]
	reserved := l.reservedLocked(profile, month)
	if used+reserved+estimate > monthly {
		return nil, fmt.Errorf("%w: profile %s — monthly %d, used %d, reserved %d, this request ~%d (month %s UTC)", ErrQuotaExceeded, profile, monthly, used, reserved, estimate, month)
	}

	id, err := newReservationID()
	if err != nil {
		return nil, fmt.Errorf("budget ledger: %v (%w)", err, ErrLedgerUnavailable)
	}
	record := budgetReservationJSON{
		ID:        id,
		Profile:   profile,
		Month:     month,
		Amount:    estimate,
		CreatedAt: now.UTC(),
		BootID:    processBootID(),
	}
	before := len(l.data.Reservations)
	l.data.Reservations = append(l.data.Reservations, record)
	if err := l.persistLocked(); err != nil {
		// Fail closed: the hold is NOT granted when it cannot be recorded.
		l.data.Reservations = l.data.Reservations[:before]
		return nil, err
	}
	return &Reservation{ledger: l, recordID: id, profile: profile, month: month, amount: estimate}, nil
}

// reservedLocked sums the CURRENT-month reservation records for one
// profile (month-specific accounting — the caller holds the mutex).
func (l *BudgetLedger) reservedLocked(profile, month string) int64 {
	var total int64
	for _, r := range l.data.Reservations {
		if r.Profile == profile && r.Month == month {
			total += r.Amount
		}
	}
	return total
}

// removeReservationLocked deletes one record by ID (caller holds the
// mutex); reports whether it existed.
func (l *BudgetLedger) removeReservationLocked(id string) bool {
	for i, r := range l.data.Reservations {
		if r.ID == id {
			l.data.Reservations = append(l.data.Reservations[:i], l.data.Reservations[i+1:]...)
			return true
		}
	}
	return false
}

// Settle replaces the reservation with the ACTUAL usage (provider-reported
// input + output tokens; when the provider reported nothing, pass the
// estimate — the documented fallback) and records it per model for the
// report. Settle on a spent reservation is a no-op (defensive).
func (r *Reservation) Settle(actual int64, model string, now time.Time) error {
	if r == nil || r.spent {
		return nil
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	r.spent = true
	l.removeReservationLocked(r.recordID)
	if actual < 0 {
		actual = 0
	}
	if l.data.Used[r.profile] == nil {
		l.data.Used[r.profile] = map[string]int64{}
	}
	l.data.Used[r.profile][r.month] += actual
	if model != "" {
		if l.data.ByModel[r.profile] == nil {
			l.data.ByModel[r.profile] = map[string]int64{}
		}
		l.data.ByModel[r.profile][model] += actual
	}
	return l.persistLocked()
}

// Release returns the reservation whole — a failed attempt is not billed.
func (r *Reservation) Release() error {
	if r == nil || r.spent {
		return nil
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	r.spent = true
	l.removeReservationLocked(r.recordID)
	return l.persistLocked()
}

// BudgetState is the reported quota state for one profile in one month.
type BudgetState struct {
	Profile   string `json:"profile"`
	Month     string `json:"month"`
	Monthly   int64  `json:"monthly"`
	Used      int64  `json:"used"`
	Reserved  int64  `json:"reserved"`
	Remaining int64  `json:"remaining"`
}

// State reports the profile's current-month accounting (used + reserved
// against the declared monthly budget).
func (l *BudgetLedger) State(profile string, monthly int64, now time.Time) BudgetState {
	l.mu.Lock()
	defer l.mu.Unlock()
	month := monthKey(now)
	used := l.data.Used[profile][month]
	reserved := l.reservedLocked(profile, month)
	remaining := monthly - used - reserved
	if remaining < 0 {
		remaining = 0
	}
	return BudgetState{Profile: profile, Month: month, Monthly: monthly, Used: used, Reserved: reserved, Remaining: remaining}
}

// RecoveryReport is one audit-trail entry surfaced for operators.
type RecoveryReport struct {
	ID          string    `json:"id"`
	Profile     string    `json:"profile"`
	Month       string    `json:"month"`
	Amount      int64     `json:"amount"`
	RecoveredAt time.Time `json:"recovered_at"`
	Reason      string    `json:"reason"`
}

// Recovered lists the crash-recovery audit trail (most recent first) —
// the explicit record of every reservation reconciliation released
// (KPK-19.6.7-carry-forward-P2-06: "no silent permanent quota loss;
// explicit recovery decision").
func (l *BudgetLedger) Recovered() []RecoveryReport {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]RecoveryReport, 0, len(l.data.Recovered))
	for i := len(l.data.Recovered) - 1; i >= 0; i-- {
		rec := l.data.Recovered[i]
		out = append(out, RecoveryReport{
			ID:          rec.ID,
			Profile:     rec.Profile,
			Month:       rec.Month,
			Amount:      rec.Amount,
			RecoveredAt: rec.RecoveredAt,
			Reason:      rec.Reason,
		})
	}
	return out
}

// pruneMonthsLocked drops month keys older than the retained window. The
// caller holds the mutex.
func (l *BudgetLedger) pruneMonthsLocked(now time.Time) {
	current := monthKey(now)
	cutoff := now.UTC().AddDate(0, -(budgetMonthsRetained - 1), 0).Format("2006-01")
	for profile, months := range l.data.Used {
		for month := range months {
			if month < cutoff || month > current {
				delete(months, month)
			}
		}
		if len(months) == 0 {
			delete(l.data.Used, profile)
		}
	}
}

// persistLocked writes the ledger atomically (tmp + rename, 0600). The
// caller holds the mutex. Memory-only ledgers (tests) skip the write.
func (l *BudgetLedger) persistLocked() error {
	if l.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(&l.data, "", "  ")
	if err != nil {
		return fmt.Errorf("budget ledger: %v (%w)", err, ErrLedgerUnavailable)
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("budget ledger: writing %s failed: %v (%w)", tmp, err, ErrLedgerUnavailable)
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return fmt.Errorf("budget ledger: renaming %s into place failed: %v (%w)", l.path, err, ErrLedgerUnavailable)
	}
	return nil
}

// Months lists the retained month keys (sorted) — the audit/report view.
func (l *BudgetLedger) Months() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := map[string]bool{}
	for _, months := range l.data.Used {
		for month := range months {
			seen[month] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
