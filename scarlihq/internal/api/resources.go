// Package api — the v20.0.0 control-plane RESOURCE ACCOUNTING surface
// (roadmap §10.2, "the UI must label values clearly"):
//
//	GET /api/compute/resources — per-node, per-dimension accounting with
//	    the CLOSED value-label vocabulary:
//
//	      OBSERVED    — read from hardware/runtime (the inventory snapshot,
//	                    with its freshness + per-source provenance).
//	      ESTIMATED   — derived from model metadata (manifest DECLARATIONS
//	                    — never conflated with observed capacity).
//	      RESERVED    — held by an allocation (the admission ledger's
//	                    HELD/ACTIVE records, per node).
//	      ALLOCATABLE — capacity remaining after policy headroom AND
//	                    reserved — COMPUTED, never observed.
//	      UNKNOWN/STALE — not safe for automatic admission (the node-level
//	                    labels; allocatable is NULL there, never a guess).
//
// The route is READ-gated (observation grade — like the inventory: the
// view reserves nothing). The local node's observed view rides the same
// snapshot seam as /api/inventory/v2; the fabric nodes ride the
// registry's RETAINED observations (§9.5: unreachable = capacity
// UNKNOWN — the last snapshot stays visible under its own honest
// labels, reservations stay HELD). The reserved view is ZERO with the
// honest reservations_configured=false when the admission gate is not
// enabled (nothing CAN reserve without the gate — zero is a fact, not
// a guess).
package api

import (
	"net/http"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/admission"
	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// The CLOSED value-label vocabulary (§10.2 verbatim — the UI contract).
const (
	LabelObserved    = "observed"    // read from hardware/runtime
	LabelEstimated   = "estimated"   // derived from model metadata
	LabelReserved    = "reserved"    // held by an allocation
	LabelAllocatable = "allocatable" // capacity remaining after policy headroom
	LabelUnknown     = "unknown"     // not safe for automatic admission
	LabelStale       = "stale"       // not safe for automatic admission
)

// resourcesResponse — the GET /api/compute/resources answer.
type resourcesResponse struct {
	Version string `json:"version"`
	// GeneratedAt — serve time (the freshness ages are computed AT
	// EVALUATION TIME — the reader's now, not the builder's).
	GeneratedAt time.Time `json:"generated_at"`
	// Labels — the closed vocabulary (machine-readable contract).
	Labels []string `json:"labels"`
	// Policy — the headroom + windows the allocatable math used (the
	// numbers behind the label, never hidden).
	Policy resourcesPolicyView `json:"policy"`
	// ReservationsConfigured — false = the admission gate is not enabled
	// (reserved is honestly ZERO: nothing can reserve without the gate).
	ReservationsConfigured bool `json:"reservations_configured"`
	// Nodes — the local node FIRST, then the fabric nodes sorted by id
	// (deterministic; every view is a snapshot at GeneratedAt).
	Nodes []nodeAccountingView `json:"nodes"`
}

// resourcesPolicyView — the allocatable math's inputs.
type resourcesPolicyView struct {
	// Source — "active" (the admission gate's live policy) or "default"
	// (DefaultPolicy — the gate is not enabled; the math is still shown
	// with the default headroom, labeled).
	Source string `json:"source"`
	// Version — the policy's provenance string.
	Version string `json:"version"`
	// VRAMSafetyFraction / RAMSafetyFraction / StorageSafetyFraction /
	// CPUSafetyThreads — the headroom subtracted from OBSERVED before
	// reserved is subtracted (allocatable = observed_free − safety −
	// reserved, per dimension).
	VRAMSafetyFraction    float64 `json:"vram_safety_fraction"`
	RAMSafetyFraction     float64 `json:"ram_safety_fraction"`
	StorageSafetyFraction float64 `json:"storage_safety_fraction"`
	CPUSafetyThreads      float64 `json:"cpu_safety_threads"`
	// LocalFreshnessSeconds — the local admission freshness bound.
	LocalFreshnessSeconds float64 `json:"local_freshness_seconds"`
	// RemoteFreshnessSeconds — the STRICTER remote bound (decision #6).
	RemoteFreshnessSeconds float64 `json:"remote_freshness_seconds"`
}

// nodeAccountingView — one node's full accounting.
type nodeAccountingView struct {
	// NodeID — "local" or the enrolled node identity.
	NodeID string `json:"node_id"`
	// NodeState — "local" | "active" | "unreachable" | "quarantined" |
	// "unknown" (the identity layer's answer; the registry's retained
	// observation may still render under its own labels).
	NodeState string `json:"node_state"`
	// Labels — the node-level safety labels: "observed" when fresh;
	// "stale" when the snapshot ages past the node's window; "unknown"
	// when there is no usable snapshot at all. NEVER absent.
	Labels []string `json:"labels"`
	// Freshness — the snapshot's serve-time evaluation.
	Freshness freshnessView `json:"freshness"`
	// Observed — the hardware/runtime facts (label: observed).
	Observed observedView `json:"observed"`
	// Reserved — the ledger's per-node holds (label: reserved).
	Reserved reservedView `json:"reserved"`
	// Allocatable — the computed remaining capacity (label:
	// allocatable); NULL (nil) when the node is stale/unknown — never a
	// guess from aged numbers.
	Allocatable *allocatableView `json:"allocatable,omitempty"`
	// Estimated — the node's model manifest DECLARATIONS (label:
	// estimated — metadata, never observed capacity).
	Estimated []modelEstimateView `json:"estimated"`
	// LatencyClass — the operator-declared class ("A"/"B"/"C"; "" for
	// local — the fabric's declaration, never a probe guess).
	LatencyClass string `json:"latency_class,omitempty"`
	// Note — the honest absent notes (e.g. "no inventory observed yet").
	Note string `json:"note,omitempty"`
}

// freshnessView — the serve-time staleness evaluation (the inventory
// v2 contract's shape).
type freshnessView struct {
	ObservedAt    time.Time `json:"observed_at"`
	AgeSeconds    float64   `json:"age_seconds"`
	MaxAgeSeconds float64   `json:"max_age_seconds"`
	Stale         bool      `json:"stale"`
	// Window — "local" or "remote" (WHICH bound the age was evaluated
	// against — the stricter remote window is the §9.5/#6 decision).
	Window string `json:"window"`
}

// observedView — the OBSERVED label's facts (hardware/runtime).
type observedView struct {
	Host     hostObservedView      `json:"host"`
	GPUs     []gpuObservedView     `json:"gpus"`
	Storage  []storageObservedView `json:"storage"`
	Runtimes []runtimeObservedView `json:"runtimes"`
}

type hostObservedView struct {
	CPUThreads        int    `json:"cpu_threads"`
	RAMTotalBytes     uint64 `json:"ram_total_bytes"`
	RAMAvailableBytes uint64 `json:"ram_available_bytes"`
}

type gpuObservedView struct {
	UUID           string `json:"uuid"`
	DeviceID       string `json:"device_id"`
	VRAMTotalBytes uint64 `json:"vram_total_bytes"`
	VRAMFreeBytes  uint64 `json:"vram_free_bytes"`
	Healthy        bool   `json:"healthy"`
}

type storageObservedView struct {
	DeviceID       string `json:"device_id"`
	Kind           string `json:"kind"`
	CapacityBytes  uint64 `json:"capacity_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	Healthy        bool   `json:"healthy"`
	// ReadProfileConfigured — whether the device carries a RECORDED
	// benchmark (the measured-floors prerequisite; the honest absence).
	ReadProfileConfigured bool `json:"read_profile_configured"`
}

type runtimeObservedView struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
	Healthy bool   `json:"healthy"`
}

// reservedView — the RESERVED label's facts (the ledger).
type reservedView struct {
	// Holds — the HELD+ACTIVE record count against this node.
	Holds int `json:"holds"`
	// GPUs — per-UUID reserved VRAM (incl. each hold's KV portion — the
	// admission gate's accounting) + the exclusive flag.
	GPUs []gpuReservedView `json:"gpus"`
	// RAMBytes / PinnedRAMBytes / CPUThreads — the host dimensions.
	RAMBytes       uint64  `json:"ram_bytes"`
	PinnedRAMBytes uint64  `json:"pinned_ram_bytes"`
	CPUThreads     float64 `json:"cpu_threads"`
	// Storage — per-device reserved bytes.
	Storage []storageReservedView `json:"storage"`
	// Ports — the held host-side ports.
	Ports []int `json:"ports"`
}

type gpuReservedView struct {
	UUID         string `json:"uuid"`
	VRAMBytes    uint64 `json:"vram_bytes"`
	KVCacheBytes uint64 `json:"kv_cache_bytes"`
	Exclusive    bool   `json:"exclusive"`
}

type storageReservedView struct {
	DeviceID      string `json:"device_id"`
	ReservedBytes uint64 `json:"reserved_bytes"`
}

// allocatableView — the ALLOCATABLE label's facts (COMPUTED: observed
// free − policy safety − reserved, per dimension).
type allocatableView struct {
	// GPUs — per-UUID allocatable VRAM (free − safety − reserved).
	GPUs []gpuAllocatableView `json:"gpus"`
	// RAMBytes — MemAvailable − safety − reserved (saturating).
	RAMBytes uint64 `json:"ram_bytes"`
	// CPUThreads — threads − OS safety − reserved (floored at 0).
	CPUThreads float64 `json:"cpu_threads"`
	// Storage — per-device allocatable bytes.
	Storage []storageAllocatableView `json:"storage"`
}

type gpuAllocatableView struct {
	UUID      string `json:"uuid"`
	VRAMBytes uint64 `json:"vram_bytes"`
}

type storageAllocatableView struct {
	DeviceID string `json:"device_id"`
	Bytes    uint64 `json:"bytes"`
}

// modelEstimateView — the ESTIMATED label's facts (one model's manifest
// DECLARATIONS — the placement gate's own vocabulary, never conflated
// with observed capacity or verified bytes).
type modelEstimateView struct {
	ModelID string `json:"model_id"`
	// ManifestStatus — "declared" | "none" | "invalid:<path>" (the
	// provenance of the estimate itself).
	ManifestStatus string `json:"manifest_status"`
	// PlacementEligible — metadata completeness ONLY (eligible ≠ the
	// backend runs — that is a runtime observation, a different label).
	PlacementEligible bool `json:"placement_eligible"`
	// KVCacheBytesPerToken / DenseFootprintBytes / ExpertCount /
	// ExpertsPerToken / TotalParameters / ActiveParameters /
	// ContextLimit — the DECLARED numbers behind the estimate.
	KVCacheBytesPerToken uint64 `json:"kv_cache_bytes_per_token,omitempty"`
	DenseFootprintBytes  uint64 `json:"dense_footprint_bytes,omitempty"`
	ExpertCount          int    `json:"expert_count,omitempty"`
	ExpertsPerToken      int    `json:"experts_per_token,omitempty"`
	TotalParameters      string `json:"total_parameters,omitempty"`
	ActiveParameters     string `json:"active_parameters,omitempty"`
	ContextLimit         int    `json:"context_limit,omitempty"`
	// Verified — whether a RECORDED artifact verification exists for the
	// model (the admission gate's own prerequisite; absent = the honest
	// "declared digests are declarations").
	Verified bool `json:"verified"`
}

// resourcesRoute serves the GET (READ-gated; the view reserves nothing).
func (h *Handler) resourcesRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "resources is read-only (GET)")
		return
	}
	if h.inventoryV2 == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "inventory source not configured (the resources view rides the same snapshot seam as /api/inventory/v2)")
		return
	}

	now := time.Now().UTC()

	// The policy: the ACTIVE admission policy when the gate is enabled;
	// the DEFAULT headroom labeled "default" when not (the math stays
	// visible and honest either way).
	policy := admission.DefaultPolicy()
	policySource := "default"
	reservationsConfigured := false
	if h.admission != nil {
		policy = h.admission.Policy()
		policySource = "active"
		reservationsConfigured = true
	}
	remoteWindow := policy.MaxSnapshotAge
	if h.fabricRegistry != nil {
		remoteWindow = h.fabricRegistry.RemoteSnapshotMaxAge()
	}

	resp := resourcesResponse{
		Version:                h.version,
		GeneratedAt:            now,
		Labels:                 []string{LabelObserved, LabelEstimated, LabelReserved, LabelAllocatable, LabelUnknown, LabelStale},
		ReservationsConfigured: reservationsConfigured,
		Policy: resourcesPolicyView{
			Source:                 policySource,
			Version:                policy.PolicyVersion,
			VRAMSafetyFraction:     policy.VRAMSafetyFraction,
			RAMSafetyFraction:      policy.RAMSafetyFraction,
			StorageSafetyFraction:  policy.StorageSafetyFraction,
			CPUSafetyThreads:       policy.CPUSafetyThreads,
			LocalFreshnessSeconds:  policy.MaxSnapshotAge.Seconds(),
			RemoteFreshnessSeconds: remoteWindow.Seconds(),
		},
		Nodes: []nodeAccountingView{},
	}

	// The LOCAL node first.
	localSnap, err := h.inventoryV2()
	if err != nil {
		resp.Nodes = append(resp.Nodes, nodeAccountingView{
			NodeID:    "local",
			NodeState: "local",
			Labels:    []string{LabelUnknown},
			Freshness: freshnessView{Window: "local"},
			Note:      "the local inventory snapshot could not be built: " + err.Error(),
			Estimated: []modelEstimateView{},
		})
	} else {
		resp.Nodes = append(resp.Nodes, h.buildNodeAccounting("local", "local", "", "", localSnap, now, policy.MaxSnapshotAge, "local"))
	}

	// The fabric nodes: the registry's retained observations + the
	// identity layer's states (sorted by node id — deterministic).
	if h.fabricRegistry != nil {
		observations := h.fabricRegistry.Observations()
		for _, obs := range observations {
			state := "active"
			labels := []string{}
			note := ""
			if h.fabricNodes != nil {
				if view, err := h.fabricNodes.Record(obs.NodeID); err == nil {
					switch view.Status {
					case "QUARANTINED":
						state = "quarantined"
						note = "credential revoked — cleanup only through trusted recovery (new work refused)"
					}
				}
			}
			if !obs.Reachable {
				state = "unreachable"
				note = "capacity UNKNOWN — the last snapshot is retained under its own labels; reservations stay HELD (never recycled)"
			}
			if obs.Snapshot == nil {
				labels = append(labels, LabelUnknown)
				note = "no inventory observed yet (the honest absence)"
				resp.Nodes = append(resp.Nodes, nodeAccountingView{
					NodeID:       obs.NodeID,
					NodeState:    state,
					Labels:       labels,
					Freshness:    freshnessView{Window: "remote"},
					Estimated:    []modelEstimateView{},
					Note:         note,
					LatencyClass: string(obs.LatencyClass),
				})
				continue
			}
			resp.Nodes = append(resp.Nodes, h.buildNodeAccounting(obs.NodeID, state, string(obs.LatencyClass), note, obs.Snapshot, now, remoteWindow, "remote"))
		}
	}

	writeJSON(w, resp)
}

// buildNodeAccounting assembles one node's full accounting (observed +
// reserved + allocatable + estimated) from its snapshot. The allocatable
// is NULL when the snapshot is stale (a stale snapshot's free figures
// describe a machine that may have changed — never reused as capacity).
func (h *Handler) buildNodeAccounting(nodeID, state, latencyClass, note string, snap *inventory.InventorySnapshotV2, now time.Time, maxAge time.Duration, window string) nodeAccountingView {
	age := now.Sub(snap.ObservedAt)
	stale := maxAge <= 0 || age > maxAge
	labels := []string{LabelObserved}
	if stale {
		labels = append(labels, LabelStale)
	}

	node := nodeAccountingView{
		NodeID:       nodeID,
		NodeState:    state,
		Labels:       labels,
		Note:         note,
		LatencyClass: latencyClass,
		Freshness: freshnessView{
			ObservedAt:    snap.ObservedAt,
			AgeSeconds:    age.Seconds(),
			MaxAgeSeconds: maxAge.Seconds(),
			Stale:         stale,
			Window:        window,
		},
		Estimated: []modelEstimateView{},
	}

	// ---- OBSERVED ---------------------------------------------------------
	obs := observedView{
		Host: hostObservedView{
			CPUThreads:        snap.Host.CPUThreads,
			RAMTotalBytes:     snap.Host.RAMTotalBytes,
			RAMAvailableBytes: snap.Host.RAMAvailableBytes,
		},
		GPUs:     []gpuObservedView{},
		Storage:  []storageObservedView{},
		Runtimes: []runtimeObservedView{},
	}
	for _, g := range snap.GPUs {
		obs.GPUs = append(obs.GPUs, gpuObservedView{
			UUID:           g.UUID,
			DeviceID:       g.ID,
			VRAMTotalBytes: uint64(g.VRAMTotalMB) << 20,
			VRAMFreeBytes:  uint64(g.VRAMFreeMB) << 20,
			Healthy:        g.Healthy,
		})
	}
	for _, d := range snap.Storage {
		obs.Storage = append(obs.Storage, storageObservedView{
			DeviceID:              d.ID,
			Kind:                  string(d.Kind),
			CapacityBytes:         d.CapacityBytes,
			AvailableBytes:        d.AvailableBytes,
			Healthy:               d.Healthy,
			ReadProfileConfigured: d.ReadProfile != nil,
		})
	}
	for _, rt := range snap.Runtimes {
		obs.Runtimes = append(obs.Runtimes, runtimeObservedView{ID: rt.ID, Running: rt.Running, Healthy: rt.Healthy})
	}
	node.Observed = obs

	// ---- RESERVED (the ledger's per-node holds) ----------------------------
	res := reservedView{GPUs: []gpuReservedView{}, Storage: []storageReservedView{}, Ports: []int{}}
	if h.admission != nil {
		for _, rec := range h.admission.Held() {
			if fabricNodeKey(rec.NodeID) != fabricNodeKey(nodeID) {
				continue
			}
			res.Holds++
			res.RAMBytes += rec.Assignment.RAMBytes
			res.PinnedRAMBytes += rec.Assignment.PinnedRAMBytes
			res.CPUThreads += rec.Assignment.CPUThreads
			for _, g := range rec.Assignment.GPUs {
				res.GPUs = append(res.GPUs, gpuReservedView{
					UUID:         g.GPUUUID,
					VRAMBytes:    g.VRAMBytes,
					KVCacheBytes: g.KVCacheBytes,
					Exclusive:    g.Exclusive,
				})
			}
			for _, s := range rec.Assignment.Storage {
				res.Storage = append(res.Storage, storageReservedView{DeviceID: s.DeviceID, ReservedBytes: s.ReservedBytes})
			}
			res.Ports = append(res.Ports, rec.Assignment.Ports...)
		}
	}
	node.Reserved = res

	// ---- ALLOCATABLE (COMPUTED — null on stale/unreachable/quarantined,
	// never a guess) ---------------------------------------------------------
	if !stale && state != "unreachable" && state != "quarantined" {
		alloc := &allocatableView{GPUs: []gpuAllocatableView{}, Storage: []storageAllocatableView{}}
		policy := admission.DefaultPolicy()
		if h.admission != nil {
			policy = h.admission.Policy()
		}
		// Per-GPU: free − safety(total) − reserved (saturating). The
		// KV cache is included in each hold's VRAMBytes EXACTLY ONCE (the
		// admission gate's accounting — KPK-19.8.0-P2-03: VRAMBytes =
		// shared + reserve + the KV charged to this GPU; KVCacheBytes is
		// the ledger's own tracked signal, never added a second time here).
		reservedVRAM := map[string]uint64{}
		for _, g := range res.GPUs {
			reservedVRAM[g.UUID] += g.VRAMBytes
		}
		for _, g := range obs.GPUs {
			safety := uint64(float64(g.VRAMTotalBytes) * policy.VRAMSafetyFraction)
			alloc.GPUs = append(alloc.GPUs, gpuAllocatableView{
				UUID:      g.UUID,
				VRAMBytes: saturatingSub64(g.VRAMFreeBytes, safety+reservedVRAM[g.UUID]),
			})
		}
		// RAM: available − safety(available) − reserved.
		ramSafety := uint64(float64(obs.Host.RAMAvailableBytes) * policy.RAMSafetyFraction)
		alloc.RAMBytes = saturatingSub64(obs.Host.RAMAvailableBytes, ramSafety+res.RAMBytes+res.PinnedRAMBytes)
		// CPU: threads − OS safety − reserved (floored at 0).
		alloc.CPUThreads = float64(obs.Host.CPUThreads) - policy.CPUSafetyThreads - res.CPUThreads
		if alloc.CPUThreads < 0 {
			alloc.CPUThreads = 0
		}
		// Storage: available − safety(capacity) − reserved.
		reservedStorage := map[string]uint64{}
		for _, s := range res.Storage {
			reservedStorage[s.DeviceID] += s.ReservedBytes
		}
		for _, d := range obs.Storage {
			safety := uint64(float64(d.CapacityBytes) * policy.StorageSafetyFraction)
			alloc.Storage = append(alloc.Storage, storageAllocatableView{
				DeviceID: d.DeviceID,
				Bytes:    saturatingSub64(d.AvailableBytes, safety+reservedStorage[d.DeviceID]),
			})
		}
		node.Allocatable = alloc
	}

	// ---- ESTIMATED (the manifest DECLARATIONS) ------------------------------
	for _, mv := range snap.Models {
		est := modelEstimateView{
			ModelID:           mv.Catalog.ID,
			ManifestStatus:    mv.ManifestStatus,
			PlacementEligible: mv.Placement.Eligible,
			Verified:          mv.ArtifactVerification != nil,
			TotalParameters:   mv.Catalog.Parameters,
			ContextLimit:      mv.Catalog.ContextLength,
		}
		if mv.Execution != nil {
			est.KVCacheBytesPerToken = mv.Execution.KVCacheBytesPerToken
			est.DenseFootprintBytes = mv.Execution.DenseFootprintBytes
			est.ExpertCount = mv.Execution.ExpertCount
			est.ExpertsPerToken = mv.Execution.ExpertsPerToken
			est.TotalParameters = mv.Execution.TotalParameters
			est.ActiveParameters = mv.Execution.ActiveParameters
			est.ContextLimit = mv.Execution.ContextLimit
		}
		node.Estimated = append(node.Estimated, est)
	}

	return node
}

// saturatingSub64 — a−b clamped at 0 (the allocatable math never goes
// negative; an over-subscription reads as zero remaining).
func saturatingSub64(a, b uint64) uint64 {
	if b >= a {
		return 0
	}
	return a - b
}
