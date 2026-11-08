// Package inventory — the v2 unified snapshot (v19.7.0, Compute Fabric
// Phase 1, roadmap §5.3/§5.6 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).
//
// The v2 snapshot is ONE normalized, READ-ONLY view of every resource
// planning needs: host resources, GPUs, storage, runtimes, model
// execution metadata, and health — composed from the v1 collectors
// (CollectGPUs/CollectGPUHealth/CollectModels — the FROZEN v1 contract
// is unchanged and keeps passing its compatibility tests) plus the new
// storage and modelmeta packages.
//
// FRESHNESS CONTRACT (roadmap §5.6): every snapshot carries its schema
// version, snapshot ID, observation timestamp, per-component source/probe
// status, and the stale marker. The snapshot is point-in-time: it goes
// stale the moment it is built. FreshWithin(maxAge) is the policy hook —
// a PLANNER must reject or downgrade stale data, never reuse an old
// free-VRAM or free-space value as current capacity ("Do not schedule
// from unverified estimates" — the release's must-not-do).
//
// The v1 types (GPU, Runtime, Model, Service, Health, SystemStatus) are
// FROZEN since v19.0.7; v2 is ADDITIVE and versioned — no v1 JSON key is
// renamed or repurposed (roadmap §5.3: "Preserve frozen public fields").
package inventory

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// SchemaVersionV2 identifies the v2 snapshot schema.
const SchemaVersionV2 = "scarlix.inventory/v2"

// InventorySnapshotV2 is the unified read-only resource snapshot.
// All slices are NON-NIL (the v1 []-not-null contract carries into v2).
type InventorySnapshotV2 struct {
	// SchemaVersion — always "scarlix.inventory/v2".
	SchemaVersion string `json:"schema_version"`
	// SnapshotID — a fresh 32-hex-char random identity per snapshot
	// (unique per build; the freshness marker refers to THIS ID).
	SnapshotID string `json:"snapshot_id"`
	// ObservedAt — when the snapshot was built (UTC). The age a
	// freshness policy evaluates is now-ObservedAt — evaluated at
	// CONSUMPTION time, so the stale marker answers "how old is this
	// when read", not "how old was it when built".
	ObservedAt time.Time `json:"observed_at"`
	// Host — the host-level resources (CPU, RAM, swap-reported-never-
	// admitted).
	Host storage.HostResources `json:"host"`
	// GPUs — the frozen v1 GPU view (nvidia-smi collected).
	GPUs []GPU `json:"gpus"`
	// Storage — the storage devices + network mounts (read-only sources).
	Storage []storage.Device `json:"storage"`
	// Runtimes — the frozen v1 runtime view.
	Runtimes []Runtime `json:"runtimes"`
	// Models — the model execution metadata (v1 catalog + manifest
	// metadata merged; models without a manifest carry their v1 fields
	// with execution unknowns explicit).
	Models []ModelExecutionView `json:"models"`
	// Health — the per-component health summary (GPUs + runtimes +
	// storage entries as the collectors derived them).
	Health []Health `json:"health"`
	// Sources — the per-component source/probe provenance (roadmap
	// §5.6: every snapshot carries how each component was observed —
	// ok/partial/empty/unavailable, never silence).
	Sources []storage.SourceStatus `json:"sources"`
}

// ModelExecutionView is one model's merged view: the v1 catalog record
// (what models.yaml + the disk scan see) + the manifest execution
// metadata when a schema-valid manifest declares it + the placement
// eligibility gate (the roadmap's "must not be used for automatic
// placement" answer, carried per-model with named reasons).
type ModelExecutionView struct {
	// Catalog — the frozen v1 Model record (ID, path, format, quant,
	// context, presence). Always present (the v1 catalog is the join
	// base).
	Catalog Model `json:"catalog"`
	// Execution — the manifest execution metadata; nil when NO schema-
	// valid manifest declares this model (the honest absence — the v1
	// fields above are all that is known).
	Execution *modelmeta.ModelExecution `json:"execution,omitempty"`
	// ManifestStatus — the provenance of the manifest join for THIS
	// model: "declared" (a schema-valid manifest matched — its metadata
	// is trusted AS A DECLARATION; v19.7.1 renamed this from "verified",
	// which conflated schema validity with byte verification,
	// KPK-19.7.0-P2-04), "none" (no manifest found), or
	// "invalid:<path>" (a manifest for this ID failed validation — its
	// reasons are in Placement.Reasons).
	ManifestStatus string `json:"manifest_status"`
	// Placement — the placement eligibility gate + named reasons
	// (METADATA completeness only — "eligible" never means "the backend
	// runs"; that is BackendPresence's separate answer).
	Placement modelmeta.PlacementEligibility `json:"placement"`
	// BackendPresence — the declared-vs-observed join for every manifest-
	// declared backend (v19.7.1, KPK-19.7.0-P2-04: an operator seeing
	// placement.eligible=true must not conclude the backend runs — the
	// join with live runtime observation travels separately). Non-nil;
	// empty for models without a manifest.
	BackendPresence []BackendPresence `json:"backend_presence"`
	// ArtifactVerification — the RECORDED artifact verification for this
	// model (from the operator-triggered `--verify-manifests` pass). nil
	// when no verification record exists — an honest absent, never a
	// fabricated "verified" (KPK-19.7.0-P2-04). The admission gate
	// refuses models whose verification is absent.
	ArtifactVerification *modelmeta.ArtifactVerification `json:"artifact_verification,omitempty"`
}

// Freshness is the serve-time staleness evaluation (roadmap §5.6: the
// stale marker is part of the snapshot's response).
type Freshness struct {
	// ObservedAt — copied from the snapshot (the anchor).
	ObservedAt time.Time `json:"observed_at"`
	// AgeSeconds — the snapshot's age AT EVALUATION TIME.
	AgeSeconds float64 `json:"age_seconds"`
	// MaxAgeSeconds — the policy threshold the age was evaluated against.
	MaxAgeSeconds float64 `json:"max_age_seconds"`
	// Stale — true iff AgeSeconds > MaxAgeSeconds. A stale snapshot must
	// be rejected or downgraded by planners — never used as current
	// capacity.
	Stale bool `json:"stale"`
}

// SnapshotV2Response is what the API and CLI actually emit: the full
// snapshot (embedded, flattened) + the serve-time freshness evaluation.
// Building it never mutates the snapshot.
type SnapshotV2Response struct {
	InventorySnapshotV2
	Freshness Freshness `json:"freshness"`
}

// FreshWithin reports whether the snapshot is younger than maxAge at
// evaluation time. Zero/negative maxAge is treated as "always stale"
// (fail-closed: an unset policy must not bless data).
func (s *InventorySnapshotV2) FreshWithin(maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	return time.Since(s.ObservedAt) <= maxAge
}

// Response evaluates freshness against maxAge at CALL time and returns
// the serve envelope. This is the read path's last step — the stale
// marker is computed at serve time, so it answers for the reader's now.
func (s *InventorySnapshotV2) Response(maxAge time.Duration) SnapshotV2Response {
	age := time.Since(s.ObservedAt)
	return SnapshotV2Response{
		InventorySnapshotV2: *s,
		Freshness: Freshness{
			ObservedAt:    s.ObservedAt,
			AgeSeconds:    age.Seconds(),
			MaxAgeSeconds: maxAge.Seconds(),
			Stale:         !s.FreshWithin(maxAge),
		},
	}
}

// newSnapshotID mints a per-build random identity (crypto/rand — the
// same source the API's WS tickets use; a snapshot ID is not a secret,
// but unpredictability costs nothing and makes snapshot references
// unforgeable in logs).
func newSnapshotID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("snapshot id unavailable (crypto/rand): %w", err)
	}
	return hex.EncodeToString(b), nil
}

// BuildSnapshotV2 composes the unified read-only snapshot: the frozen v1
// collectors (GPUs, GPU health, runtimes, models) + the new storage and
// modelmeta collectors, with per-source provenance for every component.
// It READS ONLY — no workload execution, no capacity reservation, no
// mutation of any kind (the roadmap's inventory boundary: "Inventory
// must not execute workloads or reserve capacity").
//
// Failure semantics: every component degrades independently to its empty
// shape + an "unavailable" source status; the snapshot itself is always
// built (a partial inventory with honest markers beats a 500 with none).
func BuildSnapshotV2() (*InventorySnapshotV2, error) {
	id, err := newSnapshotID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	snap := &InventorySnapshotV2{
		SchemaVersion: SchemaVersionV2,
		SnapshotID:    id,
		ObservedAt:    now,
		GPUs:          []GPU{},
		Storage:       []storage.Device{},
		Runtimes:      []Runtime{},
		Models:        []ModelExecutionView{},
		Health:        []Health{},
		Sources:       []storage.SourceStatus{},
	}

	// ---- host resources ------------------------------------------------------
	host, hostStatuses := storage.CollectHostResources()
	snap.Host = host
	snap.Sources = append(snap.Sources, hostStatuses...)

	// ---- storage devices -----------------------------------------------------
	devices, storageStatuses := storage.CollectDevices()
	snap.Storage = devices
	snap.Sources = append(snap.Sources, storageStatuses...)

	// ---- GPUs (v19.7.1: the provenance-aware collector — unavailable ≠
	// empty; KPK-19.7.0-P2-02) ---------------------------------------------
	gpus, gpuStatus := CollectGPUsWithStatus()
	gpuHealth := CollectGPUHealth(gpus)
	snap.GPUs = gpus
	snap.Health = append(snap.Health, gpuHealth...)
	snap.Sources = append(snap.Sources, gpuStatus)

	// ---- runtimes (v19.7.1: the static catalog and the live docker
	// observation are separate facts; KPK-19.7.0-P2-02) ---------------------
	runtimes, rtStatus := CollectRuntimesWithStatus()
	snap.Runtimes = runtimes
	snap.Sources = append(snap.Sources, rtStatus)
	runtimeHealth := CollectRuntimeHealth(runtimes)
	snap.Health = append(snap.Health, runtimeHealth...)

	// ---- models: v1 catalog + manifest join -----------------------------------
	models := CollectModels()
	manifests := modelmeta.LoadManifests(modelmeta.ResolveManifestsDir())

	// The recorded artifact verifications (operator-triggered pass; an
	// absent store is the honest default — KPK-19.7.0-P2-04).
	verifications := map[string]modelmeta.ArtifactVerification{}
	verifPath := modelmeta.ResolveVerificationStorePath(manifests.Dir)
	verifStatus := storage.SourceStatus{
		Component: "models.verification",
		Source:    "verification store",
		Status:    "empty",
		Detail:    "no artifact verification recorded — run scarlix-inventory --verify-manifests (declared digests are NOT byte-verified until then)",
	}
	if verifPath != "" {
		verifStatus.Source = "verification store: " + verifPath
		if v, err := modelmeta.LoadVerificationStore(verifPath); err != nil {
			verifStatus.Status = "unavailable"
			verifStatus.Detail = fmt.Sprintf("verification store unusable: %v (fail-closed — declared digests stay unverified)", err)
		} else if len(v) > 0 {
			verifications = v
			verifiedCount := 0
			for _, rec := range v {
				if rec.AllVerified {
					verifiedCount++
				}
			}
			verifStatus.Status = "ok"
			verifStatus.Detail = fmt.Sprintf("%d verification records (%d fully verified) — recorded digests, timestamps, and paths; admission refuses absent/stale records", len(v), verifiedCount)
		}
	}
	snap.Sources = append(snap.Sources, verifStatus)

	byID := make(map[string]modelmeta.Manifest, len(manifests.Manifests))
	for _, m := range manifests.Manifests {
		byID[m.ID] = m // duplicate IDs were group-rejected at load (KPK-19.7.0-P2-04)
	}
	invalidByID := make(map[string]string, len(manifests.Invalid)) // id → "invalid:<path>"
	for _, inv := range manifests.Invalid {
		// The invalid manifest's model ID is not trusted (it failed
		// validation) — match by FILE STEM as the best-effort join key,
		// purely for REPORTING.
		key := strings.TrimSuffix(filepath.Base(inv.Path), ".manifest.yaml")
		if key != "" {
			invalidByID[key] = "invalid:" + inv.Path
		}
	}

	for _, m := range models {
		view := ModelExecutionView{
			Catalog:         m,
			ManifestStatus:  "none",
			BackendPresence: []BackendPresence{},
			Placement: modelmeta.PlacementEligibility{
				Eligible: false,
				Reasons:  []string{"no verified manifest declares this model — execution metadata unknown, not defaulted"},
			},
		}
		if mm, ok := byID[m.ID]; ok {
			exec := mm.Execution(filepath.Join(manifests.Dir, m.ID+".manifest.yaml"))
			view.Execution = &exec
			view.ManifestStatus = "declared"
			view.Placement = modelmeta.PlacementEligible(&mm)
			// The declared-vs-observed backend join (KPK-19.7.0-P2-04 of the
			// KpK audit: eligible ≠ running — presence is a separate fact).
			view.BackendPresence = backendPresenceFor(mm.Runtime.SupportedBackends, runtimes, runtimeHealth)
			if rec, ok := verifications[m.ID]; ok {
				vcopy := rec
				view.ArtifactVerification = &vcopy
			}
		} else if status, ok := invalidByID[m.ID]; ok {
			view.ManifestStatus = status
			view.Placement = modelmeta.PlacementEligibility{
				Eligible: false,
				Reasons:  []string{"the manifest for this model failed schema validation — see the load report; metadata is NOT trusted"},
			}
		}
		snap.Models = append(snap.Models, view)
	}

	modelStatus := storage.SourceStatus{
		Component: "models",
		Source:    "models.yaml+disk (v1 catalog)",
		Status:    "ok",
		Detail:    fmt.Sprintf("%d models in the v1 catalog", len(models)),
	}
	if len(models) == 0 {
		modelStatus.Status = "empty"
		modelStatus.Detail = "v1 catalog empty (models.yaml missing/unparseable — honest empty per the v1 contract)"
	}
	snap.Sources = append(snap.Sources, modelStatus)

	manifestStatus := storage.SourceStatus{
		Component: "models.manifests",
		Source:    "manifests dir: " + manifests.Dir,
		Status:    "ok",
		Detail:    fmt.Sprintf("%d declared (schema-valid), %d invalid", len(manifests.Manifests), len(manifests.Invalid)),
	}
	switch {
	case manifests.DirError != "":
		manifestStatus.Status = "unavailable"
		manifestStatus.Detail = manifests.DirError
	case len(manifests.Manifests) == 0 && len(manifests.Invalid) == 0:
		manifestStatus.Status = "empty"
		manifestStatus.Detail = "no *.manifest.yaml files — execution metadata unknown for every model (the honest default)"
	case len(manifests.Invalid) > 0:
		manifestStatus.Status = "partial"
		for _, inv := range manifests.Invalid {
			manifestStatus.Detail += "; invalid: " + inv.Path
		}
	}
	snap.Sources = append(snap.Sources, manifestStatus)

	return snap, nil
}
