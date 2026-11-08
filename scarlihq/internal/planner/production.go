// Production seams for the deterministic planner (v19.7.2, Compute
// Fabric Phase 3): the ONLY file in internal/planner allowed to touch
// the environment. The planning path (planner.go) is pure — no
// network, no exec, no file reads — and these seams are how the real
// world gets in:
//
//   - the snapshot seam reuses the admission production source
//     (inventory.BuildSnapshotV2 — the same freshness envelope + the
//     per-source provenance the plan cites);
//   - the model seam reuses the admission production model source
//     (models.d manifests + the RECORDED verification store,
//     re-loaded at call time — no cache by design; a manifest edited
//     between two plans is seen by the second one);
//   - the image registry seam: the operator declares digest-pinned
//     image references through SCARLIHQ_PLANNER_IMAGES (a
//     comma-separated backend=reference list; whitespace-tolerant;
//     parsed fail-closed at construction — a malformed entry is a
//     startup error, never a per-request surprise). The honest
//     default is EMPTY: no digest-pinned references ship with the
//     repository, so every plan refuses plan-image-unconfigured until
//     the operator declares them — the same honesty as v19.7.1's
//     "no verified manifests ship yet".
package planner

import (
	"fmt"
	"os"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/admission"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
)

// PlannerImagesEnv is the operator surface for the image registry:
// SCARLIHQ_PLANNER_IMAGES="sglang=repo/sglang:v0.4@sha256:...,vllm=..."
const PlannerImagesEnv = "SCARLIHQ_PLANNER_IMAGES"

// ParseImagesEnv parses the operator's image declarations
// (backend=reference, comma-separated). Fail-closed: an unknown
// backend slug, a missing '=', or an empty reference is a NAMED error
// — the planner never silently drops a declaration.
func ParseImagesEnv(value string) (map[airuntime.BackendID]string, error) {
	out := map[airuntime.BackendID]string{}
	value = strings.TrimSpace(value)
	if value == "" {
		return out, nil
	}
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		eq := strings.Index(entry, "=")
		if eq <= 0 {
			return nil, fmt.Errorf("planner: malformed %s entry %q (want backend=reference)", PlannerImagesEnv, entry)
		}
		b := airuntime.BackendID(strings.TrimSpace(entry[:eq]))
		ref := strings.TrimSpace(entry[eq+1:])
		if err := b.Validate(); err != nil {
			return nil, fmt.Errorf("planner: malformed %s entry %q: %w", PlannerImagesEnv, entry, err)
		}
		if ref == "" {
			return nil, fmt.Errorf("planner: malformed %s entry %q (empty reference)", PlannerImagesEnv, entry)
		}
		if _, dup := out[b]; dup {
			return nil, fmt.Errorf("planner: %s declares backend %q twice", PlannerImagesEnv, b)
		}
		out[b] = ref
	}
	return out, nil
}

// ProductionPolicy builds the production planner policy: the
// documented defaults + the operator's image declarations from
// SCARLIHQ_PLANNER_IMAGES (parsed fail-closed).
func ProductionPolicy() (PlannerPolicy, error) {
	policy := DefaultPlannerPolicy()
	images, err := ParseImagesEnv(os.Getenv(PlannerImagesEnv))
	if err != nil {
		return PlannerPolicy{}, err
	}
	if len(images) > 0 {
		policy.Images = images
	}
	return policy, nil
}

// NewProductionPlanner wires the production planner: the admission
// production snapshot + model seams (the same sources the admission
// gate consumes — one truth about the machine, two gates reading it)
// and the operator's policy. Construction is fail-closed on a
// malformed image declaration or an inconsistent capability registry.
func NewProductionPlanner() (*Planner, error) {
	policy, err := ProductionPolicy()
	if err != nil {
		return nil, err
	}
	models := admission.NewProductionModelSource()
	return NewPlanner(policy, admission.ProductionSnapshotSource{}, models)
}
