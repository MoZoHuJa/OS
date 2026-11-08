package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The v19.6.7 enforced-monthly-budget proofs (KPK-19.6.6-P2-02): the
// durable ledger's accounting semantics — reserve/settle/release, the
// concurrency sum, the month window, the persistence round-trip, the
// tamper discipline.

// budgetNow is a fixed clock for deterministic month-window tests.
func budgetNow() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) }

func TestBudgetReserveSettleAccounting(t *testing.T) {
	l, err := NewBudgetLedger("")
	if err != nil {
		t.Fatal(err)
	}
	// Reserve 100 against 1000 → settle the actual 60 → used=60, reserved=0.
	r, err := l.Reserve("hugo", 1000, 100, budgetNow())
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := r.Settle(60, "qwen2.5:3b", budgetNow()); err != nil {
		t.Fatalf("settle: %v", err)
	}
	state := l.State("hugo", 1000, budgetNow())
	if state.Used != 60 || state.Reserved != 0 || state.Remaining != 940 {
		t.Fatalf("settle accounting: used=%d reserved=%d remaining=%d (want 60/0/940)", state.Used, state.Reserved, state.Remaining)
	}
	// Settling a spent reservation is a no-op (defensive).
	if err := r.Settle(999, "x", budgetNow()); err != nil {
		t.Fatalf("double settle: %v", err)
	}
	if s := l.State("hugo", 1000, budgetNow()); s.Used != 60 {
		t.Fatalf("double settle must not change accounting, used=%d", s.Used)
	}
}

func TestBudgetReserveReleaseNotBilled(t *testing.T) {
	l, _ := NewBudgetLedger("")
	r, err := l.Reserve("hugo", 1000, 100, budgetNow())
	if err != nil {
		t.Fatal(err)
	}
	// A failed attempt releases the hold whole — not billed.
	if err := r.Release(); err != nil {
		t.Fatal(err)
	}
	state := l.State("hugo", 1000, budgetNow())
	if state.Used != 0 || state.Reserved != 0 {
		t.Fatalf("release must return the hold whole, used=%d reserved=%d", state.Used, state.Reserved)
	}
}

func TestBudgetQuotaExceeded(t *testing.T) {
	l, _ := NewBudgetLedger("")
	// Fill the month: used=950.
	r, err := l.Reserve("hugo", 1000, 100, budgetNow())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Settle(950, "m", budgetNow()); err != nil {
		t.Fatal(err)
	}
	// 950 + 100 > 1000 → the named refusal.
	_, err = l.Reserve("hugo", 1000, 100, budgetNow())
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}
	// A small request still fits: 950 + 40 ≤ 1000.
	if _, err := l.Reserve("hugo", 1000, 40, budgetNow()); err != nil {
		t.Fatalf("a fitting request must reserve: %v", err)
	}
}

func TestBudgetConcurrentReservationsCannotOverspend(t *testing.T) {
	l, _ := NewBudgetLedger("")
	// 10 concurrent reservations of 100 against 1000: exactly 10 fit —
	// the 11th (whenever it lands) must be refused; collectively they can
	// never exceed the month.
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted, refused := 0, 0
	for i := 0; i < 11; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := l.Reserve("hugo", 1000, 100, budgetNow())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if errors.Is(err, ErrQuotaExceeded) {
					refused++
				}
				return
			}
			granted++
			_ = r
		}()
	}
	wg.Wait()
	if granted != 10 || refused != 1 {
		t.Fatalf("exactly 10 reservations must fit and 1 must be refused, got granted=%d refused=%d", granted, refused)
	}
	state := l.State("hugo", 1000, budgetNow())
	if state.Reserved != 1000 {
		t.Fatalf("the reserved total must equal the monthly budget exactly, reserved=%d", state.Reserved)
	}
}

func TestBudgetMonthWindowIsUTCCalendarMonth(t *testing.T) {
	l, _ := NewBudgetLedger("")
	// November usage settles; December starts a FRESH window (the UTC
	// calendar-month key).
	r, _ := l.Reserve("hugo", 1000, 100, budgetNow())
	if err := r.Settle(950, "m", budgetNow()); err != nil {
		t.Fatal(err)
	}
	december := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	r2, err := l.Reserve("hugo", 1000, 100, december)
	if err != nil {
		t.Fatalf("a new month must start a fresh window: %v", err)
	}
	stateNov := l.State("hugo", 1000, budgetNow())
	stateDec := l.State("hugo", 1000, december)
	if stateNov.Month == stateDec.Month {
		t.Fatalf("the month keys must differ across the boundary: %s vs %s", stateNov.Month, stateDec.Month)
	}
	_ = r2
}

func TestBudgetLedgerPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger", "token-budget.json")
	l, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Reserve("hugo", 1000, 100, budgetNow())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Settle(123, "qwen2.5:3b", budgetNow()); err != nil {
		t.Fatal(err)
	}
	// A second ledger over the SAME file sees the settled usage — the
	// durable accounting survives restarts.
	l2, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	state := l2.State("hugo", 1000, budgetNow())
	if state.Used != 123 || state.Reserved != 0 {
		t.Fatalf("the reloaded ledger must carry the settled usage, used=%d reserved=%d", state.Used, state.Reserved)
	}
	// The file is 0600 (secrets discipline).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the ledger file must be 0600, got %v", info.Mode().Perm())
	}
}

func TestBudgetLedgerTamperDiscipline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token-budget.json")
	// A corrupt ledger fails construction BY NAME (never run on a tampered
	// ledger).
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBudgetLedger(path); err == nil {
		t.Fatal("a corrupt ledger must fail construction")
	}
	// A world-readable ledger fails construction (secrets are not ambient).
	path2 := filepath.Join(dir, "loose.json")
	if err := os.WriteFile(path2, []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBudgetLedger(path2); err == nil {
		t.Fatal("a group/world-accessible ledger must fail construction")
	}
	// A foreign schema version fails construction (a migration, not a
	// silent reinterpretation). v19.7.1 made schema 2 the CURRENT
	// shape (reservation records, KPK-19.6.7-carry-forward-P2-06) —
	// the future sentinel is now 3.
	path3 := filepath.Join(dir, "future.json")
	if err := os.WriteFile(path3, []byte(`{"schema_version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBudgetLedger(path3); err == nil {
		t.Fatal("a future schema version must fail construction")
	}
}

func TestEstimateChatTokens(t *testing.T) {
	// The estimate is the input heuristic plus the fixed output reserve.
	est := EstimateChatTokens([]string{"hello"}) // 5 runes → 1 token + reserve
	if est != 1+BudgetOutputReserveTokens {
		t.Fatalf("estimate for 'hello': got %d, want %d", est, 1+BudgetOutputReserveTokens)
	}
	// Empty input still reserves (the model call itself costs).
	if est := EstimateChatTokens(nil); est != 1+BudgetOutputReserveTokens {
		t.Fatalf("estimate for empty input: got %d", est)
	}
}
