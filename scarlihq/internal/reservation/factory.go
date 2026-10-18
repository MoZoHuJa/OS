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
// Call ReconcileAllPending with a trusted reconciler token during service
// startup before accepting new allocations.
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
	reservationPath = filepath.Clean(reservationPath)
	allocationPath = filepath.Clean(allocationPath)
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
