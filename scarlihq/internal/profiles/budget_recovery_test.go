package profiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The v19.7.1 crash-safe reservation proofs (KPK-19.6.7-carry-forward-
// P2-06): reservation records carry identity (ID, month, boot ID);
// a restart reconciles orphaned holds VISIBLY (the audit trail), never
// as a silent permanent quota loss and never as an invisible carry-
// forward; month-specific accounting; v1 aggregate migration.

// bootIdentityForTest exposes the process boot identity for the
// restart-simulation tests (same package — the identity is private by
// design; a test in this package may read it).
func bootIdentityForTest() string { return processBootID() }

// TestBudgetCrashRecoveryReleasesOrphanedHold (KPK-19.6.7-carry-forward
// acceptance): restart after Reserve but before Settle/Release → the
// orphaned hold is RELEASED with an explicit, recorded recovery
// decision — the profile's quota is not silently lost forever.
func TestBudgetCrashRecoveryReleasesOrphanedHold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token-budget.json")

	// The "old process" reserves 400 of a 1000 monthly budget, then dies
	// (no Settle, no Release — the durable file keeps the record).
	l1, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l1.Reserve("hugo", 1000, 400, budgetNow()); err != nil {
		t.Fatal(err)
	}

	// Simulate the restart: a DIFFERENT boot identity reconstructing the
	// ledger from the durable file. The production construction path
	// reconciles at NewBudgetLedger — to simulate a foreign boot, forge
	// the record's boot ID on disk (the honest way a dead process's hold
	// looks to the next one).
	overwriteBootID(t, path, "boot-deadbeef")

	l2, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatalf("restart reconstruction failed: %v", err)
	}

	// No silent permanent loss: the full 1000 is available again.
	state := l2.State("hugo", 1000, budgetNow())
	if state.Reserved != 0 || state.Remaining != 1000 {
		t.Fatalf("after recovery reserved=%d remaining=%d, want 0/1000 (the orphaned hold is released, not immortal)", state.Reserved, state.Remaining)
	}
	// The release is EXPLICIT and recorded (the audit trail).
	rec := l2.Recovered()
	if len(rec) != 1 {
		t.Fatalf("recovery trail=%d entries, want 1 (the explicit decision)", len(rec))
	}
	if rec[0].Profile != "hugo" || rec[0].Amount != 400 || rec[0].Reason == "" {
		t.Fatalf("recovery record incomplete: %+v", rec[0])
	}
	// A new reservation succeeds against the recovered quota.
	if _, err := l2.Reserve("hugo", 1000, 900, budgetNow()); err != nil {
		t.Fatalf("post-recovery reserve failed (quota was not truly returned): %v", err)
	}
}

// TestBudgetSameBootReservationSurvives: a reservation from THIS process
// is NOT released by reconciliation (it may still settle) — recovery
// distinguishes boots, it does not wipe live holds.
func TestBudgetSameBootReservationSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token-budget.json")
	l1, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l1.Reserve("hugo", 1000, 400, budgetNow())
	if err != nil {
		t.Fatal(err)
	}
	// Same process → same boot identity → reconstructing the ledger from
	// the file KEEPS the hold (a second ledger instance in one process
	// must not eat the first one's in-flight reservation).
	overwriteBootID(t, path, bootIdentityForTest())
	l2, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	state := l2.State("hugo", 1000, budgetNow())
	if state.Reserved != 400 {
		t.Fatalf("same-boot hold released by reconciliation: reserved=%d, want 400", state.Reserved)
	}
	if len(l2.Recovered()) != 0 {
		t.Fatalf("same-boot hold must not enter the recovery trail: %+v", l2.Recovered())
	}
	// The original handle still settles cleanly against the record.
	if err := r.Settle(350, "qwen", budgetNow()); err != nil {
		t.Fatal(err)
	}
}

// TestBudgetPreviousMonthHoldDoesNotConsumeCurrentMonth (KPK acceptance):
// an abandoned reservation from the PREVIOUS month does not reduce the
// current month's available budget.
func TestBudgetPreviousMonthHoldDoesNotConsumeCurrentMonth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token-budget.json")
	l, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	// Reserve in October with the OLD process's boot identity.
	october := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if _, err := l.Reserve("hugo", 1000, 400, october); err != nil {
		t.Fatal(err)
	}
	overwriteBootID(t, path, "boot-deadbeef")

	// November restart: the October hold cannot consume November's quota
	// (released: boot mismatch AND out-of-window).
	l2, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	november := budgetNow() // 2026-11-07
	if _, err := l2.Reserve("hugo", 1000, 1000, november); err != nil {
		t.Fatalf("a previous-month abandoned hold must not consume the current month: %v", err)
	}
}

// TestBudgetRecoveryIsIdempotent (KPK acceptance): reconstructing the
// ledger twice produces the same reconciled state (no double recovery,
// no resurrection).
func TestBudgetRecoveryIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token-budget.json")
	l1, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l1.Reserve("hugo", 1000, 400, budgetNow()); err != nil {
		t.Fatal(err)
	}
	overwriteBootID(t, path, "boot-deadbeef")

	l2, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	l3, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(l2.Recovered()) != 1 || len(l3.Recovered()) != 1 {
		t.Fatalf("recovery is not idempotent: %d / %d trail entries, want 1/1", len(l2.Recovered()), len(l3.Recovered()))
	}
	if s2, s3 := l2.State("hugo", 1000, budgetNow()), l3.State("hugo", 1000, budgetNow()); s2 != s3 {
		t.Fatalf("repeated reconstruction diverged: %+v vs %+v", s2, s3)
	}
}

// TestBudgetV1LedgerMigration: a v19.6.x ledger (schema 1, aggregate
// reserved map) migrates to records — the anonymous aggregate becomes a
// named legacy record which the first reconciliation RELEASES on the
// record (the documented upgrade policy: no invisible carry-forward of
// an unverifiable hold).
func TestBudgetV1LedgerMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token-budget.json")
	v1 := `{
  "schema_version": 1,
  "used": {"hugo": {"2026-11": 250}},
  "by_model": {"hugo": {"qwen": 250}},
  "reserved": {"hugo": 300, "zmor": 0}
}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := NewBudgetLedger(path)
	if err != nil {
		t.Fatalf("v1 migration failed: %v", err)
	}
	// Settled usage survives the migration untouched.
	state := l.State("hugo", 1000, budgetNow())
	if state.Used != 250 {
		t.Fatalf("used=%d, want 250 (settled history survives migration)", state.Used)
	}
	// The orphaned v1 aggregate is RELEASED (visible decision), not
	// carried forward as an immortal anonymous hold.
	if state.Reserved != 0 {
		t.Fatalf("reserved=%d, want 0 (the v1 aggregate is recovered at migration)", state.Reserved)
	}
	rec := l.Recovered()
	if len(rec) != 1 || rec[0].Amount != 300 || rec[0].Profile != "hugo" {
		t.Fatalf("migration recovery trail=%+v, want one 300-token hugo entry", rec)
	}
	// The store is now schema 2 on disk.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var peek struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil || peek.SchemaVersion != 2 {
		t.Fatalf("migrated file schema=%d err=%v, want 2", peek.SchemaVersion, err)
	}
}

// overwriteBootID rewrites every reservation record's boot_id in the
// durable file — the test stand-in for "a hold created by another
// process".
func overwriteBootID(t *testing.T, path, bootID string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	reservations, ok := doc["reservations"]
	if !ok {
		return // no reservations — nothing to rewrite
	}
	var recs []map[string]interface{}
	if err := json.Unmarshal(reservations, &recs); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		r["boot_id"] = bootID
	}
	updated, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	doc["reservations"] = updated
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}
