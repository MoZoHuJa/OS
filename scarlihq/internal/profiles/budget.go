package profiles

import (
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

// budgetLedgerJSON is the durable on-disk shape (schema-versioned like
// every persisted store in this codebase).
type budgetLedgerJSON struct {
	SchemaVersion int                         `json:"schema_version"`
	Used          map[string]map[string]int64 `json:"used"`     // profile → month → tokens
	ByModel       map[string]map[string]int64 `json:"by_model"` // profile → model → tokens (report only)
	Reserved      map[string]int64            `json:"reserved"` // profile → in-flight reservation total
}

// BudgetLedger is the durable, mutex-guarded usage ledger. A nil-path
// ledger is MEMORY-ONLY (tests; production always passes a path — the
// memory mode is loudly labeled in its construction error contract).
type BudgetLedger struct {
	mu   sync.Mutex
	path string // "" → memory-only (tests)
	data budgetLedgerJSON
}

// NewBudgetLedger constructs the ledger and loads the durable file when a
// path is given. Construction fail-closes on a tampered/corrupt file (the
// grants.json discipline: never run on an unaccountable ledger); the
// directory is created 0700, the file is written 0600.
func NewBudgetLedger(path string) (*BudgetLedger, error) {
	l := &BudgetLedger{path: path, data: budgetLedgerJSON{
		SchemaVersion: 1,
		Used:          map[string]map[string]int64{},
		ByModel:       map[string]map[string]int64{},
		Reserved:      map[string]int64{},
	}}
	if path == "" {
		return l, nil // memory-only — tests
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("budget ledger: %v (fail closed)", err)
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if werr := l.persistLocked(); werr != nil { // write the initial empty ledger
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
	if err := json.Unmarshal(raw, &l.data); err != nil {
		return nil, fmt.Errorf("budget ledger: %s is corrupt: %v (fix or archive it deliberately — never run on a tampered ledger)", path, err)
	}
	if l.data.SchemaVersion != 1 {
		return nil, fmt.Errorf("budget ledger: %s carries schema_version %d, expected 1 (a future migration, not a silent reinterpretation)", path, l.data.SchemaVersion)
	}
	if l.data.Used == nil {
		l.data.Used = map[string]map[string]int64{}
	}
	if l.data.ByModel == nil {
		l.data.ByModel = map[string]map[string]int64{}
	}
	if l.data.Reserved == nil {
		l.data.Reserved = map[string]int64{}
	}
	return l, nil
}

// monthKey is the UTC calendar-month key ("2006-01") — the budget window.
func monthKey(now time.Time) string { return now.UTC().Format("2006-01") }

// Reservation is one in-flight budget hold. Settle replaces it with the
// actual usage; Release returns it whole (failed attempts are not billed).
// A reservation is single-shot: after Settle/Release it is spent.
type Reservation struct {
	ledger  *BudgetLedger
	profile string
	month   string
	amount  int64
	spent   bool
}

// Reserve checks and holds `estimate` tokens against the profile's
// `monthly` budget. The check counts used + already-reserved + this
// estimate — concurrent requests cannot collectively overspend. The
// current month is the window; older months are pruned on write.
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
	reserved := l.data.Reserved[profile]
	if used+reserved+estimate > monthly {
		return nil, fmt.Errorf("%w: profile %s — monthly %d, used %d, reserved %d, this request ~%d (month %s UTC)", ErrQuotaExceeded, profile, monthly, used, reserved, estimate, month)
	}
	l.data.Reserved[profile] = reserved + estimate
	if err := l.persistLocked(); err != nil {
		// Fail closed: the hold is NOT granted when it cannot be recorded.
		l.data.Reserved[profile] = reserved
		return nil, err
	}
	return &Reservation{ledger: l, profile: profile, month: month, amount: estimate}, nil
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
	l.data.Reserved[r.profile] -= r.amount
	if l.data.Reserved[r.profile] < 0 {
		l.data.Reserved[r.profile] = 0
	}
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
	l.data.Reserved[r.profile] -= r.amount
	if l.data.Reserved[r.profile] < 0 {
		l.data.Reserved[r.profile] = 0
	}
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
	reserved := l.data.Reserved[profile]
	remaining := monthly - used - reserved
	if remaining < 0 {
		remaining = 0
	}
	return BudgetState{Profile: profile, Month: month, Monthly: monthly, Used: used, Reserved: reserved, Remaining: remaining}
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
