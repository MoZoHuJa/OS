// Package admission — the resource vector and aggregate admission gate
// (v19.7.1, Compute Fabric Phase 2, roadmap §6 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).
//
// CHARTER (the package's non-negotiable contract):
//
//   - The resource VECTOR is multi-dimensional (roadmap §6.1: "Do not
//     model the whole machine as one scalar score"): GPU VRAM per UUID,
//     host RAM, pinned RAM, CPU threads, KV-cache bytes, storage free
//     bytes (+ measured read profile when demanded), and ports.
//   - Admission is DETERMINISTIC for identical inputs and identical
//     inventory/policy snapshots (§6.4) — candidate iteration is sorted,
//     never map-ordered.
//   - A STALE snapshot is HARD-REFUSED (KPK-19.7.0-P2-01 of the KpK
//     audit: "admission odmietne freshness.stale=true" — never reuse an
//     old free-VRAM/free-space value as current capacity).
//   - A model without a VERIFIED artifact record is refused
//     (KPK-19.7.0-P2-04: a 64-hex digest in a manifest is a DECLARATION;
//     the admission gate consumes verification records only).
//   - A declared-but-not-running backend is refused (the placement ×
//     runtime join — eligibility is metadata completeness, presence is
//     live observation; admission needs BOTH).
//   - A measured-performance requirement (minimum read MB/s) is refused
//     when no measurement exists — "do not schedule from unverified
//     estimates" is enforced here, not just documented.
//   - GPU assignment is EXCLUSIVE per UUID in v19.7.1 (roadmap §6
//     checklist: "Exclusive GPU mode remains default") — sharing without
//     compute-share enforcement would be a phantom capability.
//   - The reserve is ATOMIC and DURABLE: all dimensions are checked and
//     held under one lock, persisted before the decision is returned
//     (§6.4's atomic reserve pattern, steps 5-9), with a LEASE that
//     expires and a boot identity that reconciles crashes.
//   - Safety headroom is subtracted BEFORE any allocation (§6.3:
//     allocatable = max(0, observed − reserved − safety_headroom)).
package admission

import "fmt"

// sprintf is fmt.Sprintf (kept as a tiny indirection so vector.go's
// Validate reads without an import block).
func sprintf(format string, args ...interface{}) string { return fmt.Sprintf(format, args...) }

// ResourceVector describes a workload's requirements across every
// planning dimension (roadmap §6.2 — the field set is the contract).
// Units are integer BYTES for memory/storage dimensions (never MB/MiB
// mixes — conversion helpers live in the collectors); CPU threads are a
// float share; ports are host-side integers.
type ResourceVector struct {
	// GPU — per-GPU requests (VRAM per UUID; exclusive by default).
	GPU []GPURequest `json:"gpu,omitempty"`
	// RAMBytes — host RAM the workload requires.
	RAMBytes uint64 `json:"ram_bytes,omitempty"`
	// PinnedRAMBytes — page-locked RAM (a separate sub-budget: pinned
	// memory cannot be reclaimed under pressure — admitting it as plain
	// RAM would be the silent substitution the honest-accounting rules
	// forbid).
	PinnedRAMBytes uint64 `json:"pinned_ram_bytes,omitempty"`
	// CPUThreads — logical CPU threads demanded.
	CPUThreads float64 `json:"cpu_threads,omitempty"`
	// KVCacheBytes — the KV-cache footprint demanded. Accounted against
	// the ASSIGNED GPU's VRAM (the engines allocate KV from VRAM) and
	// tracked in the ledger as its own demand signal. Counted against the
	// GPU's allocatable EXACTLY ONCE (the admission gate adds it to the
	// first GPU request's demand; a GPU request's VRAMBytes never includes
	// it — KPK-19.8.0-P2-03).
	KVCacheBytes uint64 `json:"kv_cache_bytes,omitempty"`
	// Storage — per-device storage requirements.
	Storage []StorageRequest `json:"storage,omitempty"`
	// Ports — host-side ports the workload binds.
	Ports []int `json:"ports,omitempty"`
}

// GPURequest is one GPU dimension of a resource vector.
type GPURequest struct {
	// GPUUUID — the target GPU's hardware UUID. Empty = any GPU
	// (deterministic pick: the lowest UUID with capacity).
	GPUUUID string `json:"gpu_uuid,omitempty"`
	// VRAMBytes — VRAM demanded for weights + activations EXCLUDING the
	// KV cache (v19.8.1, KPK-19.8.0-P2-03's single-count contract: the KV
	// cache is declared ONCE in ResourceVector.KVCacheBytes and is added to
	// the assigned GPU's demand exactly ONCE by the admission gate itself
	// — a producer that bakes KV into VRAMBytes made the gate count it
	// twice and refuse workloads that fit).
	VRAMBytes uint64 `json:"vram_bytes"`
	// Exclusive — v19.7.1 admits EXCLUSIVE GPU assignment only (the
	// default); a request with Exclusive=false is refused BY NAME unless
	// the policy explicitly enables sharing (which no v19.7.1 policy
	// does — sharing without compute-share enforcement is a phantom).
	Exclusive bool `json:"exclusive"`
	// MinimumComputeShare — fractional GPU compute share [0, 1]. Only
	// meaningful for shared assignment (refused in v19.7.1).
	MinimumComputeShare float64 `json:"minimum_compute_share,omitempty"`
}

// StorageRequest is one storage-device requirement.
type StorageRequest struct {
	// DeviceID — the inventory storage device ID ("storage.nvme.0").
	DeviceID string `json:"device_id,omitempty"`
	// MinimumFreeBytes — free bytes demanded on the device (checked
	// against the AGGREGATE of the device's distinct filesystems —
	// KPK-19.7.0-P2-01's corrected accounting).
	MinimumFreeBytes uint64 `json:"minimum_free_bytes"`
	// MinimumReadMBps — a MEASURED sequential-read floor. Refused when
	// the device carries no benchmark record (never schedule from
	// unverified estimates — the release's must-not-do, enforced).
	MinimumReadMBps float64 `json:"minimum_read_mbps,omitempty"`
	// MinimumRandomReadIOPS — measured random-read floor (same rule).
	MinimumRandomReadIOPS float64 `json:"minimum_random_read_iops,omitempty"`
	// MaximumP95LatencyMs — measured p95 latency ceiling (same rule).
	MaximumP95LatencyMs float64 `json:"maximum_p95_latency_ms,omitempty"`
	// PathClass — the storage class the workload needs ("model",
	// "scratch", ...). v19.7.1 carries it as DECLARED metadata only (no
	// class registry exists yet — an honest pass-through, documented).
	PathClass string `json:"path_class,omitempty"`
}

// ResourceAssignment is what admission actually granted — the concrete
// resources bound to the admitted request (immutable once decided; the
// record persists it with the request fingerprint).
type ResourceAssignment struct {
	// GPUs — the assigned GPUs (UUID + VRAM hold incl. the KV bytes).
	GPUs []GPUAssignment `json:"gpus,omitempty"`
	// RAMBytes — the host-RAM hold.
	RAMBytes uint64 `json:"ram_bytes,omitempty"`
	// PinnedRAMBytes — the pinned-RAM hold (its own sub-budget).
	PinnedRAMBytes uint64 `json:"pinned_ram_bytes,omitempty"`
	// CPUThreads — the CPU-thread hold.
	CPUThreads float64 `json:"cpu_threads,omitempty"`
	// KVCacheBytes — the KV-cache demand (also inside the GPU holds).
	KVCacheBytes uint64 `json:"kv_cache_bytes,omitempty"`
	// Storage — per-device free-byte holds.
	Storage []StorageAssignment `json:"storage,omitempty"`
	// Ports — the bound ports.
	Ports []int `json:"ports,omitempty"`
}

// GPUAssignment is one granted GPU hold.
type GPUAssignment struct {
	GPUUUID   string `json:"gpu_uuid"`
	DeviceID  string `json:"device_id"` // inventory ID ("gpu.nvidia.0")
	VRAMBytes uint64 `json:"vram_bytes"`
	// KVCacheBytes — the portion of the hold reserved for KV cache.
	KVCacheBytes uint64 `json:"kv_cache_bytes,omitempty"`
	Exclusive    bool   `json:"exclusive"`
}

// StorageAssignment is one granted storage hold.
type StorageAssignment struct {
	DeviceID      string `json:"device_id"`
	ReservedBytes uint64 `json:"reserved_bytes"`
}

// Validate checks the vector's internal consistency (a named reason per
// violation; empty = valid). Deterministic order: GPU → RAM → pinned →
// CPU → KV → storage → ports.
func (v ResourceVector) Validate() []string {
	var reasons []string
	add := func(format string, args ...interface{}) {
		reasons = append(reasons, sprintf(format, args...))
	}

	seenGPU := map[string]bool{}
	for i, g := range v.GPU {
		if g.VRAMBytes == 0 {
			add("gpu[%d]: vram_bytes must be > 0", i)
		}
		if g.MinimumComputeShare < 0 || g.MinimumComputeShare > 1 {
			add("gpu[%d]: minimum_compute_share %v outside [0,1]", i, g.MinimumComputeShare)
		}
		if seenGPU[g.GPUUUID] {
			add("gpu[%d]: duplicate gpu_uuid %q — one request per GPU", i, g.GPUUUID)
		}
		seenGPU[g.GPUUUID] = true
	}
	if v.PinnedRAMBytes > v.RAMBytes && v.RAMBytes > 0 {
		add("pinned_ram_bytes %d exceeds ram_bytes %d — pinned memory is a subset of the RAM demand", v.PinnedRAMBytes, v.RAMBytes)
	}
	if v.KVCacheBytes > 0 && len(v.GPU) == 0 {
		add("kv_cache_bytes %d with no gpu request — the KV cache is charged against the ASSIGNED GPU's VRAM (KPK-19.8.0-P2-03's single-count contract: KV rides the GPU demand, it cannot stand alone)", v.KVCacheBytes)
	}
	if v.CPUThreads < 0 {
		add("cpu_threads %v must be >= 0", v.CPUThreads)
	}
	seenDev := map[string]bool{}
	for i, s := range v.Storage {
		if s.MinimumFreeBytes == 0 && s.MinimumReadMBps == 0 && s.MinimumRandomReadIOPS == 0 && s.MaximumP95LatencyMs == 0 {
			add("storage[%d]: an all-zero requirement is not a requirement (omit the entry)", i)
		}
		if s.MinimumRandomReadIOPS < 0 || s.MaximumP95LatencyMs < 0 || s.MinimumReadMBps < 0 {
			add("storage[%d]: negative measured-performance floors are meaningless", i)
		}
		if seenDev[s.DeviceID] {
			add("storage[%d]: duplicate device_id %q — consolidate the per-device demand", i, s.DeviceID)
		}
		seenDev[s.DeviceID] = true
	}
	seenPort := map[int]bool{}
	for i, p := range v.Ports {
		if p < 1 || p > 65535 {
			add("ports[%d]: %d outside the valid host-port range", i, p)
		}
		if seenPort[p] {
			add("ports[%d]: duplicate port %d", i, p)
		}
		seenPort[p] = true
	}
	return reasons
}
