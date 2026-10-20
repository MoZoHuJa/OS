package reservation

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/allocation"
)

// NewFileBackedManager wires the Phase 3 reservation engine to the durable
// allocation store. Inventory, cleanup verification, and token identity are
// injected because they must reflect the actual host and deployment secrets.
//
// Startup contract (KPK-19.2.12-P1-02, KPK-19.2.13-P1-01): call Recover (or
// ReconcileAllPending, its compatibility wrapper) with a trusted reconciler
// token during service startup BEFORE accepting new allocations. The full
// sequence repairs:
//
//  1. RESERVING allocations (finalize complete HELD bundles, abort orphaned
//     claims, durably route expired/blocked bundles to cleanup),
//  2. expired-lease reaping (mark, verify cleanup evidence, release — which
//     also converges allocation lifecycles),
//  3. interrupted allocation releases (RELEASING/CLEANUP_REQUIRED, plus
//     FAILED/CANCELLED with release-progress bundles — actively completed,
//     not only inspected),
//  4. stale lease metadata on RESERVED allocations, including discovery of
//     interrupted user-initiated releases stranded under a still-RESERVED
//     allocation record.
//
// For long-running services, RunLeaseReaper provides the controlled periodic
// repair path (expiry reaping + reserved-lease repair + interrupted-release
// completion per tick).
func NewFileBackedManager(
	reservationPath string,
	allocationPath string,
	policy ReservationPolicy,
	clock Clock,
	inventory InventoryProvider,
	cleanup CleanupVerifier,
	identities TrustedIdentityProvider,
) (*Manager, error) {
	if strings.TrimSpace(reservationPath) == "" || strings.TrimSpace(allocationPath) == "" {
		return nil, fmt.Errorf("reservation: reservation and allocation store paths are required")
	}
	reservationPath, err := filepath.Abs(filepath.Clean(reservationPath))
	if err != nil {
		return nil, fmt.Errorf("reservation: resolve reservation store path: %w", err)
	}
	allocationPath, err = filepath.Abs(filepath.Clean(allocationPath))
	if err != nil {
		return nil, fmt.Errorf("reservation: resolve allocation store path: %w", err)
	}
	if reservationPath == allocationPath {
		return nil, fmt.Errorf("reservation: reservation and allocation stores must use different files")
	}
	if inventory == nil || cleanup == nil || identities == nil {
		return nil, fmt.Errorf("reservation: inventory, cleanup verifier, and trusted identity provider are required")
	}
	allocationStore := allocation.NewStore(allocationPath)
	validator, err := NewAllocationReservationAdapter(allocationStore)
	if err != nil {
		return nil, err
	}
	return NewManager(NewStore(reservationPath), policy, clock, inventory, validator, cleanup, identities)
}
