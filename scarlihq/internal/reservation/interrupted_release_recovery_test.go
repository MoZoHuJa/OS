package reservation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
)

// =============================================================================
// KPK-19.2.13-P1-01: crash recovery must complete a user-initiated release
// interrupted between RequestRelease() and ConfirmRelease().
// =============================================================================

// RequestRelease persists the allocation release intent together with the
// first member's RELEASING transition — the allocation is discoverable by the
// release recovery scan BEFORE any ConfirmRelease happens.
func TestRequestRelease_PersistsAllocationReleasingIntent(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	member := result.Reservations[0]
	if _, err := h.manager.RequestRelease(member.ReservationID, "user-token"); err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleasing {
		t.Fatalf("allocation state after RequestRelease=%s, want RELEASING (release intent persisted, KPK-19.2.13-P1-01)", got)
	}
}

// Crash after RequestRelease() commits but before ConfirmRelease(): restart
// and Recover() converge WITHOUT waiting for lease expiry (no clock advance).
func TestRecover_CrashAfterRequestRelease_ConvergesWithoutExpiry(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	member := result.Reservations[0]
	if _, err := h.manager.RequestRelease(member.ReservationID, "user-token"); err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}
	// Simulate the crash: no ConfirmRelease, no lease expiry (clock frozen).

	summary, err := h.manager.Recover("reconciler-token")
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
		t.Fatalf("allocation state=%s, want RELEASED (recovery must complete the interrupted release without lease expiry)", got)
	}
	for _, r := range result.Reservations {
		got, err := h.manager.Get(r.ReservationID)
		if err != nil {
			t.Fatalf("Get reservation: %v", err)
		}
		if got.Status != StatusReleased {
			t.Fatalf("reservation %s status=%s, want RELEASED (bundle-release contract)", r.ReservationID, got.Status)
		}
	}
	found := false
	for _, id := range summary.Released {
		if id == "alloc-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("summary.Released=%v, want alloc-1 reported", summary.Released)
	}
}

// The pre-19.2.13 stranded state: the reservation is RELEASING but the
// allocation-side intent write never persisted (allocation still RESERVED).
// Recovery must discover it from the reservation bundle and converge it.
func TestRecover_StrandedReleasingUnderReservedAllocation_Converges(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	member := result.Reservations[0]
	if _, err := h.manager.RequestRelease(member.ReservationID, "user-token"); err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}
	// Simulate the intent write failure / pre-19.2.13 persisted state.
	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		record.State = allocation.StateReserved
		return nil
	}); err != nil {
		t.Fatalf("regress allocation to RESERVED: %v", err)
	}

	if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
		t.Fatalf("allocation state=%s, want RELEASED (stranded RELEASING member under RESERVED must converge)", got)
	}
	for _, r := range result.Reservations {
		got, err := h.manager.Get(r.ReservationID)
		if err != nil {
			t.Fatalf("Get reservation: %v", err)
		}
		if got.Status != StatusReleased {
			t.Fatalf("reservation %s status=%s, want RELEASED", r.ReservationID, got.Status)
		}
	}
}

// Crash after ONE member is RELEASING and siblings remain HELD: recovery
// completes the documented bundle-release contract (all members released).
func TestRecover_PartialReleasingWithHeldSiblings_CompletesBundleContract(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	if _, err := h.manager.RequestRelease(result.Reservations[0].ReservationID, "user-token"); err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}

	if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
		t.Fatalf("allocation state=%s, want RELEASED", got)
	}
	for i, r := range result.Reservations {
		got, err := h.manager.Get(r.ReservationID)
		if err != nil {
			t.Fatalf("Get reservation %d: %v", i, err)
		}
		if got.Status != StatusReleased {
			t.Fatalf("reservation %d (%s) status=%s, want RELEASED — releasing any member releases the whole allocation bundle", i, r.ReservationID, got.Status)
		}
	}
}

// Repeated Recover() and ConfirmRelease() calls are idempotent: terminal
// states are preserved and no spurious errors are produced.
func TestRecover_RepeatedRecoverAndConfirmRelease_Idempotent(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	member := result.Reservations[0]
	if _, err := h.manager.RequestRelease(member.ReservationID, "user-token"); err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}
	if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
		t.Fatalf("first recovery: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
		t.Fatalf("allocation state=%s, want RELEASED", got)
	}

	// Repeated recovery passes must not regress the terminal state.
	for i := 0; i < 3; i++ {
		if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
			t.Fatalf("repeated recovery pass %d: %v", i+1, err)
		}
		if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
			t.Fatalf("repeated recovery pass %d: state=%s, want RELEASED", i+1, got)
		}
	}
	// Repeated ConfirmRelease on the already-RELEASED reservation converges
	// (no-op) instead of skipping the allocation reconciliation.
	for i := 0; i < 2; i++ {
		if _, err := h.manager.ConfirmRelease(member.ReservationID, "reconciler-token"); err != nil {
			t.Fatalf("repeated ConfirmRelease %d: %v", i+1, err)
		}
		if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
			t.Fatalf("repeated ConfirmRelease %d: allocation state=%s, want RELEASED", i+1, got)
		}
	}
}

// Cleanup-verifier failure during recovery leaves the allocation visibly
// blocked (CLEANUP_REQUIRED), the reservations blocking (CONFLICT blocks the
// resource), and the summary reporting the blocked work.
func TestRecover_CleanupVerifierFailure_BlocksVisiblyAndResourceBlocking(t *testing.T) {
	h := newRealAdapterHarness(t, false) // cleanup verifier reports failure
	result := h.reserveFullBundle(t, "alloc-1")

	if _, err := h.manager.RequestRelease(result.Reservations[0].ReservationID, "user-token"); err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}
	// Recover itself reports blocked release work through the structured
	// summary (the v19.2.12 contract: CleanupBlocked is operator-visible, not
	// a scanner failure); the typed no-false-success errors are surfaced by
	// the individual release operations (ConfirmRelease/ReapExpiredOnce).
	summary, err := h.manager.Recover("reconciler-token")
	if err != nil {
		t.Fatalf("Recover: %v (blocked work is summary-reported, not a recovery failure)", err)
	}

	if got := h.allocationState(t, "alloc-1"); got != allocation.StateCleanupRequired {
		t.Fatalf("allocation state=%s, want CLEANUP_REQUIRED (operator-visible blocked state)", got)
	}
	blocked := false
	for _, id := range summary.CleanupBlocked {
		if id == "alloc-1" {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("summary.CleanupBlocked=%v, want alloc-1", summary.CleanupBlocked)
	}
	// The conflicted reservations still block the resources (fail-closed).
	bundle, err := h.manager.ListByAllocation("alloc-1")
	if err != nil {
		t.Fatalf("ListByAllocation: %v", err)
	}
	if len(bundle) == 0 {
		t.Fatal("bundle must still exist for the blocked allocation")
	}
	for _, r := range bundle {
		if r.Status != StatusConflict {
			t.Fatalf("reservation %s status=%s, want CONFLICT (cleanup could not be verified)", r.ReservationID, r.Status)
		}
		if !r.Status.BlocksResource() {
			t.Fatalf("reservation %s CONFLICT must block the resource", r.ReservationID)
		}
	}
	if DefaultPolicy().CanReserveGPU(testGPUUUID0, bundle) {
		t.Fatal("GPU must remain blocked while the release is conflicted")
	}
}

// Allocation-store write failure at the RequestRelease boundary is surfaced
// as a typed error; the reservation IS durably RELEASING; restart repair
// converges it.
func TestRequestRelease_AllocationWriteFailure_TypedErrorAndRestartRepair(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	failing := &failingAllocationStore{inner: h.allocStore, fail: true}
	failingAdapter, err := NewAllocationReservationAdapter(failing)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}
	h.manager.allocVal = failingAdapter

	member := result.Reservations[0]
	_, err = h.manager.RequestRelease(member.ReservationID, "user-token")
	if err == nil {
		t.Fatal("allocation intent write failure must surface a typed error, not success")
	}
	if !errors.Is(err, ErrAllocationReconciliationPending) {
		t.Fatalf("error=%v, want ErrAllocationReconciliationPending", err)
	}
	// The reservation store is authoritative: the member IS durably RELEASING.
	got, err := h.manager.Get(member.ReservationID)
	if err != nil {
		t.Fatalf("Get reservation: %v", err)
	}
	if got.Status != StatusReleasing {
		t.Fatalf("reservation status=%s, want RELEASING (durable outcome)", got.Status)
	}

	// Restart with a healthy adapter: recovery converges.
	h.manager.allocVal = h.adapter
	if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
		t.Fatalf("restart repair: %v", err)
	}
	if state := h.allocationState(t, "alloc-1"); state != allocation.StateReleased {
		t.Fatalf("post-repair state=%s, want RELEASED", state)
	}
}

// The periodic lease reaper also completes interrupted releases — a committed
// RELEASING reservation must not wait for the next restart.
func TestRunLeaseReaper_CompletesInterruptedRelease(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	if _, err := h.manager.RequestRelease(result.Reservations[0].ReservationID, "user-token"); err != nil {
		t.Fatalf("RequestRelease: %v", err)
	}
	// Regress the allocation to RESERVED so only the reaper's periodic
	// interrupted-release scan can find it (phase-3-equivalent coverage
	// without a restart).
	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		record.State = allocation.StateReserved
		return nil
	}); err != nil {
		t.Fatalf("regress allocation: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reportErrs := make(chan error, 64)
	go func() {
		reportErrs <- h.manager.RunLeaseReaper(ctx, "reconciler-token", 10*time.Millisecond, func(err error) {})
	}()
	defer func() { cancel() }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.allocationState(t, "alloc-1"); got == allocation.StateReleased {
			return // converged by the periodic scan
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("lease reaper did not complete the interrupted release within the deadline")
}

// =============================================================================
// KPK-19.2.13-P1-02: CompleteAllocationRelease must follow the frozen
// transition table for every source state; FAILED/CANCELLED walk through
// RELEASING instead of jumping directly to RELEASED.
// =============================================================================

// walkToReserved builds an allocation and walks it REQUESTED → … → RESERVED.
func walkToReserved(t *testing.T, store *allocation.Store, id string) {
	t.Helper()
	record := allocation.NewAllocation(id, "v1", "coding", "interactive", "agent")
	if err := store.Append(record); err != nil {
		t.Fatalf("Append allocation: %v", err)
	}
	for _, step := range []struct {
		to     allocation.State
		reason string
	}{
		{allocation.StateValidating, "test validation"},
		{allocation.StatePlanned, "test plan"},
		{allocation.StateReserving, "test reservation"},
		{allocation.StateReserved, "test bundle committed"},
	} {
		if err := store.Update(id, func(record *allocation.Allocation) error {
			_, err := record.Transition(step.to, step.reason)
			return err
		}); err != nil {
			t.Fatalf("walk to %s: %v", step.to, err)
		}
	}
}

// Table-driven transition test for EVERY supported source state of
// CompleteAllocationRelease: RESERVED, RELEASING, CLEANUP_REQUIRED, FAILED,
// CANCELLED → RELEASING (walked where required) → RELEASED with assignments
// and lease metadata cleared. Every path must be frozen-table-legal.
func TestCompleteAllocationRelease_TableDriven_AllSourceStates(t *testing.T) {
	for _, tc := range []struct {
		source allocation.State
		walk   func(t *testing.T, store *allocation.Store, id string)
	}{
		{
			source: allocation.StateReserved,
			walk: func(t *testing.T, store *allocation.Store, id string) {
				walkToReserved(t, store, id)
			},
		},
		{
			source: allocation.StateReleasing,
			walk: func(t *testing.T, store *allocation.Store, id string) {
				walkToReserved(t, store, id)
				if err := store.Update(id, func(record *allocation.Allocation) error {
					_, err := record.Transition(allocation.StateReleasing, "test release started")
					return err
				}); err != nil {
					t.Fatalf("walk to RELEASING: %v", err)
				}
			},
		},
		{
			source: allocation.StateCleanupRequired,
			walk: func(t *testing.T, store *allocation.Store, id string) {
				walkToReserved(t, store, id)
				for _, step := range []allocation.State{allocation.StateReleasing, allocation.StateCleanupRequired} {
					if err := store.Update(id, func(record *allocation.Allocation) error {
						_, err := record.Transition(step, "test walk")
						return err
					}); err != nil {
						t.Fatalf("walk to %s: %v", step, err)
					}
				}
			},
		},
		{
			source: allocation.StateFailed,
			walk: func(t *testing.T, store *allocation.Store, id string) {
				walkToReserved(t, store, id)
				if err := store.Update(id, func(record *allocation.Allocation) error {
					_, err := record.Transition(allocation.StateFailed, "test failure")
					return err
				}); err != nil {
					t.Fatalf("walk to FAILED: %v", err)
				}
			},
		},
		{
			source: allocation.StateCancelled,
			walk: func(t *testing.T, store *allocation.Store, id string) {
				walkToReserved(t, store, id)
				if err := store.Update(id, func(record *allocation.Allocation) error {
					_, err := record.Transition(allocation.StateCancelled, "test cancellation")
					return err
				}); err != nil {
					t.Fatalf("walk to CANCELLED: %v", err)
				}
			},
		},
	} {
		t.Run(string(tc.source), func(t *testing.T) {
			store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
			tc.walk(t, store, "alloc-1")
			// Simulate the assignments a committed bundle leaves behind.
			if err := store.Update("alloc-1", func(record *allocation.Allocation) error {
				record.AssignedGPU = testGPUUUID0
				record.AssignedRuntime = "sglang"
				record.AssignedModel = "qwen3-14b"
				record.AssignedPort = 30000
				record.LeaseID = "lease-1"
				record.LeaseExpiry = time.Now().Add(time.Hour)
				return nil
			}); err != nil {
				t.Fatalf("set assignments: %v", err)
			}
			adapter, err := NewAllocationReservationAdapter(store)
			if err != nil {
				t.Fatalf("NewAllocationReservationAdapter: %v", err)
			}
			if err := adapter.CompleteAllocationRelease("alloc-1"); err != nil {
				t.Fatalf("CompleteAllocationRelease from %s: %v (the frozen table must be followed, never violated)", tc.source, err)
			}
			record, err := store.Get("alloc-1")
			if err != nil {
				t.Fatalf("Get allocation: %v", err)
			}
			if record.State != allocation.StateReleased {
				t.Fatalf("state=%s, want RELEASED (from %s)", record.State, tc.source)
			}
			if record.AssignedGPU != "" || record.AssignedRuntime != "" || record.AssignedModel != "" || record.AssignedPort != 0 {
				t.Fatalf("assignments not cleared from %s: %+v", tc.source, record)
			}
			if record.LeaseID != "" || !record.LeaseExpiry.IsZero() {
				t.Fatalf("lease metadata not cleared from %s: %q %v", tc.source, record.LeaseID, record.LeaseExpiry)
			}
			if !record.CleanupVerified || record.CleanupStatus != "verified" {
				t.Fatalf("cleanup evidence not recorded from %s: verified=%v status=%q", tc.source, record.CleanupVerified, record.CleanupStatus)
			}
			// Idempotent replay repairs stale fields and stays terminal.
			if err := adapter.CompleteAllocationRelease("alloc-1"); err != nil {
				t.Fatalf("idempotent replay from RELEASED: %v", err)
			}
		})
	}

	// Unsupported source states must be rejected, not guessed.
	t.Run("invalid source", func(t *testing.T) {
		store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
		record := allocation.NewAllocation("alloc-x", "v1", "coding", "interactive", "agent")
		if err := store.Append(record); err != nil {
			t.Fatalf("Append: %v", err)
		}
		adapter, err := NewAllocationReservationAdapter(store)
		if err != nil {
			t.Fatalf("NewAllocationReservationAdapter: %v", err)
		}
		if err := adapter.CompleteAllocationRelease("alloc-x"); err == nil {
			t.Fatal("REQUESTED is not a legal release source — must be rejected")
		}
	})
}

// FAILED allocation + all reservations released with verified cleanup →
// allocation reaches RELEASED with assignments cleared (restart converges).
func TestRecover_FailedAllocationWithReleasedBundle_Converges(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	// The allocation failed while its reservations lived on.
	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		_, err := record.Transition(allocation.StateFailed, "start failed but bundle leaked")
		return err
	}); err != nil {
		t.Fatalf("walk to FAILED: %v", err)
	}
	// Every member was durably released (verified cleanup).
	for _, res := range result.Reservations {
		if err := h.manager.store.update(res.ReservationID, func(r *Reservation) error {
			if err := r.transition(StatusReleasing, h.clock.Now(), "test release"); err != nil {
				return err
			}
			return r.transition(StatusReleased, h.clock.Now(), "test cleanup confirmed")
		}); err != nil {
			t.Fatalf("release member %s: %v", res.ReservationID, err)
		}
	}

	summary, err := h.manager.Recover("reconciler-token")
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	record, err := h.allocStore.Get("alloc-1")
	if err != nil {
		t.Fatalf("Get allocation: %v", err)
	}
	if record.State != allocation.StateReleased {
		t.Fatalf("allocation state=%s, want RELEASED (FAILED must walk FAILED → RELEASING → RELEASED)", record.State)
	}
	if record.AssignedGPU != "" || record.AssignedRuntime != "" || record.AssignedModel != "" {
		t.Fatalf("assignments not cleared: %+v", record)
	}
	found := false
	for _, id := range summary.Released {
		if id == "alloc-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("summary.Released=%v, want alloc-1", summary.Released)
	}
}

// CANCELLED allocation + all reservations released with verified cleanup →
// same convergence.
func TestRecover_CancelledAllocationWithReleasedBundle_Converges(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		_, err := record.Transition(allocation.StateCancelled, "cancelled with bundle")
		return err
	}); err != nil {
		t.Fatalf("walk to CANCELLED: %v", err)
	}
	for _, res := range result.Reservations {
		if err := h.manager.store.update(res.ReservationID, func(r *Reservation) error {
			if err := r.transition(StatusReleasing, h.clock.Now(), "test release"); err != nil {
				return err
			}
			return r.transition(StatusReleased, h.clock.Now(), "test cleanup confirmed")
		}); err != nil {
			t.Fatalf("release member %s: %v", res.ReservationID, err)
		}
	}

	if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateReleased {
		t.Fatalf("allocation state=%s, want RELEASED (CANCELLED must walk CANCELLED → RELEASING → RELEASED)", got)
	}
}

// FAILED/CANCELLED with any CONFLICT member → CLEANUP_REQUIRED, never
// RELEASED (no false success).
func TestRecover_FailedWithConflictMember_CleanupRequiredNeverReleased(t *testing.T) {
	for _, source := range []allocation.State{allocation.StateFailed, allocation.StateCancelled} {
		t.Run(string(source), func(t *testing.T) {
			h := newRealAdapterHarness(t, true)
			result := h.reserveFullBundle(t, "alloc-1")

			if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
				_, err := record.Transition(source, "test source state")
				return err
			}); err != nil {
				t.Fatalf("walk to %s: %v", source, err)
			}
			// One member is CONFLICT, the others released.
			first := result.Reservations[0]
			if err := h.manager.store.update(first.ReservationID, func(r *Reservation) error {
				if err := r.transition(StatusExpiredPendingReconciliation, h.clock.Now(), "test expiry"); err != nil {
					return err
				}
				return r.transition(StatusConflict, h.clock.Now(), "test ambiguity")
			}); err != nil {
				t.Fatalf("conflict member: %v", err)
			}
			for _, res := range result.Reservations[1:] {
				if err := h.manager.store.update(res.ReservationID, func(r *Reservation) error {
					if err := r.transition(StatusReleasing, h.clock.Now(), "test release"); err != nil {
						return err
					}
					return r.transition(StatusReleased, h.clock.Now(), "test cleanup confirmed")
				}); err != nil {
					t.Fatalf("release member %s: %v", res.ReservationID, err)
				}
			}

			if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if got := h.allocationState(t, "alloc-1"); got != allocation.StateCleanupRequired {
				t.Fatalf("allocation state=%s, want CLEANUP_REQUIRED (CONFLICT member must block, never RELEASED)", got)
			}
		})
	}
}

// FAILED/CANCELLED with a HELD-only bundle is NOT release work: recovery must
// not force-release it (the expiry reaper / user release path owns it).
func TestRecover_FailedHeldOnlyBundle_NotForceReleased(t *testing.T) {
	h := newRealAdapterHarness(t, true)
	result := h.reserveFullBundle(t, "alloc-1")

	if err := h.allocStore.Update("alloc-1", func(record *allocation.Allocation) error {
		_, err := record.Transition(allocation.StateFailed, "failed with live bundle")
		return err
	}); err != nil {
		t.Fatalf("walk to FAILED: %v", err)
	}

	if err := h.manager.ReconcileAllPending("reconciler-token"); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := h.allocationState(t, "alloc-1"); got != allocation.StateFailed {
		t.Fatalf("allocation state=%s, want FAILED (HELD-only bundle must not be force-released by recovery)", got)
	}
	for _, r := range result.Reservations {
		got, err := h.manager.Get(r.ReservationID)
		if err != nil {
			t.Fatalf("Get reservation: %v", err)
		}
		if got.Status != StatusHeld {
			t.Fatalf("reservation %s status=%s, want HELD (recovery must not release HELD members of a non-release allocation)", r.ReservationID, got.Status)
		}
	}
}

// The release scan enumerates every legal source state of the frozen release
// lifecycle: RELEASING, CLEANUP_REQUIRED, FAILED, CANCELLED (not RESERVED /
// RELEASED / others).
func TestListReleasingAllocations_IncludesFailedCancelled(t *testing.T) {
	store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
	adapter, err := NewAllocationReservationAdapter(store)
	if err != nil {
		t.Fatalf("NewAllocationReservationAdapter: %v", err)
	}

	prepare := func(id string, steps ...allocation.State) {
		t.Helper()
		walkToReserved(t, store, id)
		for _, step := range steps {
			if err := store.Update(id, func(record *allocation.Allocation) error {
				_, err := record.Transition(step, "test walk")
				return err
			}); err != nil {
				t.Fatalf("%s: walk to %s: %v", id, step, err)
			}
		}
	}
	prepare("alloc-releasing", allocation.StateReleasing)
	prepare("alloc-cleanup", allocation.StateReleasing, allocation.StateCleanupRequired)
	prepare("alloc-failed", allocation.StateFailed)
	prepare("alloc-cancelled", allocation.StateCancelled)
	prepare("alloc-reserved")                            // stays RESERVED
	prepare("alloc-released", allocation.StateReleasing) // → RELEASED below

	// RELEASING → RELEASED requires CleanupVerified before the terminal jump.
	if err := store.Update("alloc-released", func(record *allocation.Allocation) error {
		record.CleanupVerified = true
		_, err := record.Transition(allocation.StateReleased, "test released")
		return err
	}); err != nil {
		t.Fatalf("walk to RELEASED: %v", err)
	}

	ids, err := adapter.ListReleasingAllocations()
	if err != nil {
		t.Fatalf("ListReleasingAllocations: %v", err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for _, want := range []string{"alloc-releasing", "alloc-cleanup", "alloc-failed", "alloc-cancelled"} {
		if !got[want] {
			t.Errorf("ListReleasingAllocations missing %s (got %v) — interrupted-release discovery must cover every legal source state", want, ids)
		}
	}
	for _, notWant := range []string{"alloc-reserved", "alloc-released"} {
		if got[notWant] {
			t.Errorf("ListReleasingAllocations must not include %s (got %v)", notWant, ids)
		}
	}
}

// =============================================================================
// KPK-19.2.13-P2-04: the bundle validator fails closed on nil members
// (first, middle, last) — never panics.
// =============================================================================

func validBundleMember(id string) *Reservation {
	return &Reservation{
		ReservationID:      id,
		AllocationID:       "alloc-1",
		SchemaVersion:      ReservationSchemaVersion,
		ResourceID:         "runtime.sglang",
		ResourceType:       ResourceTypeRuntimeSlot,
		Owner:              "agent",
		Status:             StatusHeld,
		LeaseID:            "lease-1",
		LeaseExpiry:        time.Now().Add(5 * time.Minute),
		BundleSize:         2,
		IdempotencyKey:     "idem-1",
		RequestFingerprint: "fp-1",
	}
}

func TestBundleValidator_NilMembers_FailClosedNoPanic(t *testing.T) {
	cases := map[string][]*Reservation{
		"nil first":  {nil, validBundleMember("res-b")},
		"nil middle": {validBundleMember("res-a"), nil, validBundleMember("res-c")},
		"nil last":   {validBundleMember("res-a"), nil},
		"nil only":   {nil},
	}
	for name, bundle := range cases {
		t.Run(name, func(t *testing.T) {
			// Direct validator boundary.
			err := validateBundleConsistency(bundle, "alloc-1", activeBundleStatuses)
			if err == nil || !strings.Contains(err.Error(), "nil record") {
				t.Fatalf("validateBundleConsistency error=%v, want a typed nil-record rejection", err)
			}
			// validateActiveBundle boundary (includes the sort path).
			err = validateActiveBundle(bundle, "alloc-1")
			if err == nil || !strings.Contains(err.Error(), "nil record") {
				t.Fatalf("validateActiveBundle error=%v, want a typed nil-record rejection", err)
			}
		})
	}

	// FinalizeReservation boundary: a bundle with a nil first member must be
	// rejected with an error, not panic, and must not mutate the allocation.
	t.Run("FinalizeReservation nil first", func(t *testing.T) {
		store := allocation.NewStore(filepath.Join(t.TempDir(), "allocations.jsonl"))
		newPlannedAllocation(t, store, "alloc-1")
		if err := store.Update("alloc-1", func(record *allocation.Allocation) error {
			_, err := record.Transition(allocation.StateReserving, "test")
			return err
		}); err != nil {
			t.Fatalf("transition RESERVING: %v", err)
		}
		adapter, err := NewAllocationReservationAdapter(store)
		if err != nil {
			t.Fatalf("NewAllocationReservationAdapter: %v", err)
		}
		if err := adapter.FinalizeReservation("alloc-1", []*Reservation{nil, validBundleMember("res-b")}); err == nil {
			t.Fatal("nil first member must be rejected by FinalizeReservation")
		}
		record, err := store.Get("alloc-1")
		if err != nil {
			t.Fatalf("Get allocation: %v", err)
		}
		if record.State != allocation.StateReserving || record.AssignedGPU != "" {
			t.Fatalf("allocation mutated from nil-member bundle: %+v", record)
		}
	})
}
