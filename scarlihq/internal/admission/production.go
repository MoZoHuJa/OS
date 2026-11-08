// Package admission — the PRODUCTION source adapters (main.go's wiring).
//
// The Manager consumes two seams (SnapshotSource + ModelSource); the
// tests carry fixtures. This file is the ONLY production
// implementation: the snapshot seam wraps inventory.BuildSnapshotV2
// (the v19.7.0 unified collector — same freshness envelope, same
// per-source provenance the decision cites), and the model seam wraps
// the models.d manifests + the recorded verification store
// (modelmeta's own resolution: SCARLIX_MODELS_D → /etc/scarlix/models.d
// → <cwd>/models.d, verification at SCARLIX_MODELS_VERIFICATION →
// /var/lib/scarlix/models-verification.json → beside the manifests).
//
// The adapters are deliberately THIN: no caching, no re-reading, no
// clock. Every Admit call re-loads the manifests and the verification
// store — admission is a rare, high-stakes act, and a stale manifest
// set is exactly the class of bug the fail-closed policy refuses to
// paper over. The snapshot collector owns its own freshness math; the
// manager owns the staleness REFUSAL.
package admission

import (
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
)

// ProductionSnapshotSource is the live inventory seam.
type ProductionSnapshotSource struct{}

// Current builds the unified v2 snapshot (the real collector).
func (ProductionSnapshotSource) Current() (*inventory.InventorySnapshotV2, error) {
	return inventory.BuildSnapshotV2()
}

// ProductionModelSource resolves manifests + recorded verifications at
// CALL time (no cache — a manifest edited between two admissions is
// seen by the second one, not masked by the first).
type ProductionModelSource struct{}

// NewProductionModelSource returns the resolving model source.
func NewProductionModelSource() *ProductionModelSource { return &ProductionModelSource{} }

// Manifest returns the schema-valid manifest for id (false when absent,
// invalid, or duplicate-collided — the loader's group rejection means a
// collided ID resolves to NOTHING, and admission sees the honest no).
func (ProductionModelSource) Manifest(id string) (*modelmeta.Manifest, bool) {
	dir := modelmeta.ResolveManifestsDir()
	loaded := modelmeta.LoadManifests(dir)
	for i := range loaded.Manifests {
		if loaded.Manifests[i].ID == id {
			if errs := modelmeta.ValidateManifest(&loaded.Manifests[i]); len(errs) > 0 {
				return nil, false
			}
			m := loaded.Manifests[i]
			return &m, true
		}
	}
	return nil, false
}

// Verification returns the RECORDED artifact verification for id (false
// when no store exists or no record is present — the gate treats an
// absent record as absent verification, which is the refusal).
func (ProductionModelSource) Verification(id string) (*modelmeta.ArtifactVerification, bool) {
	dir := modelmeta.ResolveManifestsDir()
	path := modelmeta.ResolveVerificationStorePath(dir)
	if path == "" {
		return nil, false
	}
	store, err := modelmeta.LoadVerificationStore(path)
	if err != nil {
		return nil, false // unreadable store = unverified (fail closed)
	}
	rec, ok := store[id]
	if !ok {
		return nil, false
	}
	return &rec, true
}

// NewProductionManager is main.go's one-liner: default policy, the live
// snapshot seam, the resolving model seam, and the durable ledger at
// path (fail-closed construction — a corrupt ledger refuses to boot the
// surface, never to silently re-zero capacity accounting).
func NewProductionManager(ledgerPath string) (*Manager, error) {
	ledger, err := NewLedger(ledgerPath)
	if err != nil {
		return nil, err
	}
	return NewManager(DefaultPolicy(), ProductionSnapshotSource{}, NewProductionModelSource(), ledger), nil
}
