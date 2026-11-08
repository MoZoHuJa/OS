// Production seams for the deterministic planner (v19.7.2, Compute
// Fabric Phase 3; v19.8.0 Phase 4 evolution): the ONLY files in
// internal/planner allowed to touch the environment. The planning path
// (planner.go) is pure — no network, no exec, no file reads — and
// these seams are how the real world gets in:
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
//   - the v19.8.0 InferSSD runtime profile seam (roadmap §8.4/§8.8):
//     SCARLIHQ_INFERSSD_PROFILE points at ONE runtime profile file —
//     loaded, validated, and placeholder-refused fail-closed at
//     construction; a DISABLED profile is the honest not-configured
//     (§8.8's initial status: the planner stays nil-profiled and every
//     infer_ssd plan refuses plan-ssd-profile-unconfigured); an
//     ENABLED profile rides the policy as the ssd-backed-moe
//     derivation's evidence record. Unset = not configured.
package planner

import (
	"fmt"
	"os"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/admission"
	"github.com/MoZoHuJa/OS/scarlihq/internal/airuntime"
	"github.com/MoZoHuJa/OS/scarlihq/internal/backends/infer_ssd"
)

// PlannerImagesEnv is the operator surface for the image registry:
// SCARLIHQ_PLANNER_IMAGES="sglang=repo/sglang:v0.4@sha256:...,vllm=..."
const PlannerImagesEnv = "SCARLIHQ_PLANNER_IMAGES"

// InferSSDProfileEnv is the v19.8.0 operator surface for the InferSSD
// runtime profile (roadmap §8.4): SCARLIHQ_INFERSSD_PROFILE=<path> —
// one file, loaded + validated + placeholder-refused fail-closed; the
// ENABLED flag is the §8.8 enablement act.
const InferSSDProfileEnv = "SCARLIHQ_INFERSSD_PROFILE"

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

// LoadInferSSDProfile loads the operator's InferSSD runtime profile
// (roadmap §8.4) when SCARLIHQ_INFERSSD_PROFILE is set. Fail-closed:
// an unreadable, unparsable, invalid, or placeholder-carrying profile
// is a NAMED error (a startup error, never a per-request surprise);
// a DISABLED profile returns (nil, nil) — the honest not-configured
// (§8.8's initial status; the caller's log distinguishes unset from
// disabled). Unset returns (nil, nil).
func LoadInferSSDProfile() (*infer_ssd.RuntimeProfile, error) {
	path := strings.TrimSpace(os.Getenv(InferSSDProfileEnv))
	if path == "" {
		return nil, nil
	}
	profile, err := infer_ssd.LoadRuntimeProfile(path)
	if err != nil {
		return nil, fmt.Errorf("planner: %s is malformed (fail-closed — a broken runtime profile is a startup error, never a silent default): %w", InferSSDProfileEnv, err)
	}
	if !profile.Enabled {
		return nil, nil // §8.8: disabled is documentation — the planner stays nil-profiled
	}
	return profile, nil
}

// ProductionPolicy builds the production planner policy: the
// documented defaults + the operator's image declarations from
// SCARLIHQ_PLANNER_IMAGES (parsed fail-closed) + the enabled InferSSD
// runtime profile from SCARLIHQ_INFERSSD_PROFILE (loaded + validated
// fail-closed).
func ProductionPolicy() (PlannerPolicy, error) {
	policy := DefaultPlannerPolicy()
	images, err := ParseImagesEnv(os.Getenv(PlannerImagesEnv))
	if err != nil {
		return PlannerPolicy{}, err
	}
	if len(images) > 0 {
		policy.Images = images
	}
	profile, err := LoadInferSSDProfile()
	if err != nil {
		return PlannerPolicy{}, err
	}
	policy.SSDProfile = profile
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
