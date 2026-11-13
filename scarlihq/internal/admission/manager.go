// Package admission — the Manager: the deterministic, atomic aggregate
// admission gate (roadmap §6.4's pattern, steps 1-9).
package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// Policy is the admission policy snapshot (deterministic per version).
type Policy struct {
	// PolicyVersion — carried in every decision (provenance).
	PolicyVersion string
	// MaxSnapshotAge — the freshness bound; a staler snapshot is HARD
	// refused (KPK-19.7.0-P2-01).
	MaxSnapshotAge time.Duration
	// VerificationMaxAge — how old an artifact verification record may
	// be before admission refuses the model (KPK-19.7.0-P2-04: stale
	// verification is absent verification).
	VerificationMaxAge time.Duration
	// VRAMSafetyFraction — the headroom fraction of a GPU's TOTAL VRAM
	// subtracted before allocation (fragmentation + runtime overhead).
	VRAMSafetyFraction float64
	// RAMSafetyFraction — the headroom fraction of TOTAL RAM.
	RAMSafetyFraction float64
	// StorageSafetyFraction — the headroom fraction of a device's
	// aggregate filesystem capacity.
	StorageSafetyFraction float64
	// CPUSafetyThreads — absolute threads reserved for the OS/housekeeping.
	CPUSafetyThreads float64
	// MaxPinnableRAMBytes — the pinned-RAM sub-budget. 0 = NONE ADMITTED
	// (the honest v19.7.1 default: no pinned-RAM observation or ledger
	// exists yet — pinning is refused, not guessed).
	MaxPinnableRAMBytes uint64
	// LeaseTTL — how long an admission holds before expiry sweeps it
	// (a HELD record only; an ACTIVE record holds until released or
	// crash-recovered, never swept by the timer — v19.8.1).
	LeaseTTL time.Duration
	// MaxBenchmarkAge — how old a device's recorded benchmark may be
	// when a measured floor is demanded (v19.8.1, the SK audit's P2-04: an
	// arbitrarily old benchmark describes a device it may no longer be; the
	// admission gate owns the clock exactly like it owns verification age).
	MaxBenchmarkAge time.Duration
}

// DefaultPolicy is the documented v19.8.1 policy (the v19.7.1 values +
// the v19.8.1 remediations: the measured random-read contract enforced at
// the gate, verification records bound to the manifest identity, the
// idempotency replay bound to the request fingerprint, and the ACTIVE
// lifecycle after a confirmed start).
func DefaultPolicy() Policy {
	return Policy{
		PolicyVersion:         "admission/v19.8.1",
		MaxSnapshotAge:        60 * time.Second,
		VerificationMaxAge:    7 * 24 * time.Hour,
		VRAMSafetyFraction:    0.05,
		RAMSafetyFraction:     0.10,
		StorageSafetyFraction: 0.10,
		CPUSafetyThreads:      2,
		MaxPinnableRAMBytes:   0,
		LeaseTTL:              10 * time.Minute,
		MaxBenchmarkAge:       30 * 24 * time.Hour,
	}
}

// SnapshotSource produces the inventory snapshot admission decides
// against (production: inventory.BuildSnapshotV2; tests: fixtures).
type SnapshotSource interface {
	Current() (*inventory.InventorySnapshotV2, error)
}

// ModelSource resolves manifests and RECORDED artifact verifications
// (production: the models.d manifests + the verification store; tests:
// fixtures).
type ModelSource interface {
	// Manifest returns the schema-valid manifest for id (false when none
	// or invalid — the loader already group-rejects duplicates).
	Manifest(id string) (*modelmeta.Manifest, bool)
	// Verification returns the recorded artifact verification for id
	// (false when no record exists).
	Verification(id string) (*modelmeta.ArtifactVerification, bool)
}

// Manager is the admission gate. Construction is cheap; every Admit call
// is deterministic for identical (request, snapshot, ledger, policy)
// inputs: candidate GPUs iterate in UUID order, explanations name the
// exact numbers, and the reserve is one atomic ledger transaction.
type Manager struct {
	policy    Policy
	snapshots SnapshotSource
	models    ModelSource
	ledger    *Ledger
	now       func() time.Time
}

// NewManager wires the manager. The ledger must be constructed through
// NewLedger (which reconciles crashes/expiry before serving).
func NewManager(policy Policy, snapshots SnapshotSource, models ModelSource, ledger *Ledger) *Manager {
	if policy.PolicyVersion == "" {
		policy = DefaultPolicy()
	}
	return &Manager{policy: policy, snapshots: snapshots, models: models, ledger: ledger, now: time.Now}
}

// SetNow overrides the clock (tests).
func (m *Manager) SetNow(now func() time.Time) { m.now = now }

// Policy returns the active policy (operator surface).
func (m *Manager) Policy() Policy { return m.policy }

// Admit decides + reserves. The full §6.4 sequence:
//
//  1. validate identity and policy      → request-invalid
//  2. verify snapshot freshness         → snapshot-stale (HARD)
//  3. verify model/backend compatibility→ model-*/backend-* (incl. the
//     VERIFIED-artifact gate and the
//     placement × runtime join)
//     4-7. compute + check + persist the assignment atomically (one ledger
//     transaction; failure reserves
//     NOTHING)
//  8. return the assignment + lease
//  9. any dimension failure → reserve nothing, name the dimension
func (m *Manager) Admit(req AdmissionRequest) AdmissionDecision {
	now := m.now().UTC()
	base := AdmissionDecision{
		ReasonCode:    ReasonOK,
		SnapshotID:    "",
		PolicyVersion: m.policy.PolicyVersion,
	}

	// ---- 1. request validation -------------------------------------------
	if strings.TrimSpace(req.RequestID) == "" {
		return refuse(base, ReasonRequestInvalid, "request_id is required")
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return refuse(base, ReasonRequestInvalid, "model_id is required")
	}
	if strings.TrimSpace(req.BackendID) == "" {
		return refuse(base, ReasonRequestInvalid, "backend_id is required")
	}
	if reasons := req.Requirements.Validate(); len(reasons) > 0 {
		return refuse(base, ReasonRequestInvalid, "invalid resource vector: "+strings.Join(reasons, "; "))
	}
	for i, g := range req.Requirements.GPU {
		if !g.Exclusive {
			return refuse(base, ReasonRequestInvalid, fmt.Sprintf(
				"gpu[%d]: v19.7.1 admits EXCLUSIVE GPU assignment only (roadmap §6: \"exclusive GPU mode remains default\") — sharing without compute-share enforcement is a phantom capability; set exclusive=true", i))
		}
		if g.MinimumComputeShare > 0 {
			return refuse(base, ReasonRequestInvalid, fmt.Sprintf(
				"gpu[%d]: minimum_compute_share is meaningful only for shared assignment, which v19.7.1 does not admit", i))
		}
	}
	if req.Requirements.PinnedRAMBytes > m.policy.MaxPinnableRAMBytes {
		return refuse(base, ReasonPinnedRAMUnavailable, fmt.Sprintf(
			"pinned_ram_bytes %d exceeds the admissible pinned budget %d — v19.7.1 observes no pinned-RAM figure (ram_pinned_bytes stays the honest zero until a Phase 2 ledger owns it); pinning is refused, never guessed",
			req.Requirements.PinnedRAMBytes, m.policy.MaxPinnableRAMBytes))
	}

	// ---- 2. snapshot + freshness (HARD GATE) ------------------------------
	// (v19.8.1, KPK-19.8.0-P2-04: the idempotency replay moved INSIDE the
	// atomic ledger transaction below — the pre-lock check was a
	// TOCTOU (two concurrent identical admits could both pass it and
	// double-admit), and the replay now requires an EXACT principal +
	// request-fingerprint match, a mismatch being the NAMED conflict.)
	snap, err := m.snapshots.Current()
	if err != nil {
		return refuse(base, ReasonSnapshotUnavailable, "inventory snapshot unavailable: "+err.Error())
	}
	base.SnapshotID = snap.SnapshotID
	if !snap.FreshWithin(m.policy.MaxSnapshotAge) {
		age := time.Since(snap.ObservedAt)
		return refuse(base, ReasonSnapshotStale, fmt.Sprintf(
			"snapshot %s is %s old (policy max %s) — a stale snapshot is HARD-REFUSED: its free-VRAM/free-space figures describe a machine that may have changed (KPK-19.7.0-P2-01)",
			snap.SnapshotID, age.Round(time.Millisecond), m.policy.MaxSnapshotAge))
	}

	// ---- 3 + 4-7. the atomic dimension check + reserve (the replay check
	// AND the model gates ride the SAME ledger transaction — the v19.8.1
	// post-release ordering remediation: the transaction opens with the
	// idempotency probe (KPK-19.8.0-P2-04: principal + request-fingerprint
	// bound, inside the lock, never a TOCTOU), and the model gates run AFTER
	// it, so a mismatched replay surfaces the NAMED idempotency-conflict
	// instead of being masked by an incidental model-unknown, and an EXACT
	// replay returns the ORIGINAL decision even if the manifest was removed
	// after the first admission) --------------------------------------------
	replayed, assignment, admID, leaseExpiry, failCode, failExpl := m.reserveAtomic(req, snap, now)
	if failCode != ReasonOK {
		return refuse(base, failCode, failExpl)
	}
	if replayed != nil {
		// An EXACT replay (same principal + same canonical request): the
		// ORIGINAL decision stands — one hold, not two.
		return AdmissionDecision{
			Allowed:        true,
			ReasonCode:     ReasonOK,
			Explanation:    fmt.Sprintf("idempotent replay — admission %s (created %s) stands; the request fingerprint matches exactly", replayed.AdmissionID, replayed.CreatedAt.Format(time.RFC3339)),
			AdmissionID:    replayed.AdmissionID,
			Assignment:     &replayed.Assignment,
			SnapshotID:     replayed.SnapshotID,
			PolicyVersion:  m.policy.PolicyVersion,
			LeaseExpiresAt: replayed.LeaseExpiresAt,
		}
	}

	// ---- 8. the decision ---------------------------------------------------
	return AdmissionDecision{
		Allowed:        true,
		ReasonCode:     ReasonOK,
		Explanation:    fmt.Sprintf("reserved: %s", describeAssignment(assignment)),
		AdmissionID:    admID,
		Assignment:     assignment,
		SnapshotID:     snap.SnapshotID,
		PolicyVersion:  m.policy.PolicyVersion,
		LeaseExpiresAt: leaseExpiry,
	}
}

// checkModel enforces the model-side gates: manifest existence, schema
// validity, placement eligibility, VERIFIED artifacts, backend
// declaration, and the live backend join.
func (m *Manager) checkModel(modelID, backendID string, snap *inventory.InventorySnapshotV2) (string, string, *modelmeta.Manifest) {
	manifest, ok := m.models.Manifest(modelID)
	if !ok {
		return ReasonModelUnknown, fmt.Sprintf("no schema-valid manifest declares model %q (invalid/duplicate manifests never reach admission)", modelID), nil
	}
	if reasons := modelmeta.ValidateManifest(manifest); len(reasons) > 0 {
		return ReasonModelIneligible, fmt.Sprintf("manifest for %q fails validation: %s", modelID, strings.Join(reasons, "; ")), nil
	}
	if pe := modelmeta.PlacementEligible(manifest); !pe.Eligible {
		return ReasonModelIneligible, fmt.Sprintf("manifest for %q is placement-ineligible: %s", modelID, strings.Join(pe.Reasons, "; ")), nil
	}

	// The VERIFIED-artifact gate (KPK-19.7.0-P2-04): declared digests are
	// declarations; admission consumes verification RECORDS.
	verif, ok := m.models.Verification(modelID)
	if !ok {
		return ReasonModelUnverified, fmt.Sprintf(
			"model %q carries no artifact verification record — run scarlix-inventory --verify-manifests; a declared checkpoint digest is a declaration, not a verified fingerprint", modelID), nil
	}
	if !verif.AllVerified {
		why := "verification record is not fully verified"
		if verif.Checkpoint != nil && verif.Checkpoint.Error != "" {
			why = "checkpoint verification error: " + verif.Checkpoint.Error
		} else if verif.Checkpoint != nil && !verif.Checkpoint.Match {
			why = fmt.Sprintf("computed checkpoint digest %s does not match the declaration", verif.Checkpoint.ComputedSHA256)
		}
		return ReasonModelUnverified, fmt.Sprintf("model %q: %s", modelID, why), nil
	}
	if age := m.now().UTC().Sub(verif.VerifiedAt); age > m.policy.VerificationMaxAge {
		return ReasonModelUnverified, fmt.Sprintf(
			"model %q's artifact verification is %s old (policy max %s) — a stale verification is absent verification (KPK-19.7.0-P2-04)",
			modelID, age.Round(time.Second), m.policy.VerificationMaxAge), nil
	}

	// The manifest-identity binding (v19.8.1, KPK-19.8.0-P1-02): the record
	// must be bound to the CURRENT manifest's canonical fingerprint. A
	// record keyed by model ID alone is a stale authorization token for
	// whatever manifest later reuses that ID; the seven-day freshness
	// window is not a substitute for binding the verification to the exact
	// manifest + artifacts it verified.
	if verif.ManifestFingerprint == "" {
		return ReasonModelUnverified, fmt.Sprintf(
			"model %q's verification record carries NO manifest fingerprint (a pre-v19.8.1 record) — re-run scarlix-inventory --verify-manifests so the record binds to the exact manifest identity",
			modelID), nil
	}
	if current := modelmeta.ManifestFingerprint(manifest); current != verif.ManifestFingerprint {
		return ReasonModelVerificationIdentityMismatch, fmt.Sprintf(
			"model %q's verification record (fingerprint %s) is not bound to the current manifest (fingerprint %s) — the manifest's identity-bearing fields changed after verification (revision/checkpoint path+digest/expert-pack declaration); re-run scarlix-inventory --verify-manifests (KPK-19.8.0-P1-02)",
			modelID, verif.ManifestFingerprint, current), nil
	}

	// Backend declaration (the manifest declares an OPEN set — never
	// asserting presence; presence is checked next).
	declared := false
	for _, b := range manifest.Runtime.SupportedBackends {
		if b == backendID {
			declared = true
			break
		}
	}
	if !declared {
		return ReasonBackendNotDeclared, fmt.Sprintf(
			"manifest for %q declares backends %v — %q is not among them (the manifest declares, the snapshot observes; admission needs both)", modelID, manifest.Runtime.SupportedBackends, backendID), nil
	}

	// The placement × runtime join (KPK-19.7.0-P2-04 of the KpK audit):
	// eligible metadata + a RUNNING, HEALTHY backend.
	for _, rt := range snap.Runtimes {
		if rt.ID != backendID {
			continue
		}
		if !rt.Running || !rt.Healthy {
			return ReasonBackendUnavailable, fmt.Sprintf(
				"backend %q is %s (running=%v healthy=%v) — declared AND eligible metadata does not start a runtime; admission joins placement with live observation",
				backendID, backendState(rt.Running, rt.Healthy), rt.Running, rt.Healthy), nil
		}
		return ReasonOK, "", manifest
	}
	return ReasonBackendUnavailable, fmt.Sprintf(
		"backend %q is not a configured runtime in this snapshot — declared, but not observed at all", backendID), nil
}

func backendState(running, healthy bool) string {
	switch {
	case running && healthy:
		return "serving"
	case running:
		return "running but not healthy"
	default:
		return "stopped"
	}
}

// gpuCandidate is one snapshot GPU in deterministic order.
type gpuCandidate struct {
	uuid      string
	deviceID  string
	vramTotal uint64
	vramFree  uint64
}

// sortedGPUCandidates returns the snapshot's GPUs in UUID order
// (deterministic candidate iteration — never snapshot order).
func sortedGPUCandidates(snap *inventory.InventorySnapshotV2) []gpuCandidate {
	cands := make([]gpuCandidate, 0, len(snap.GPUs))
	for _, g := range snap.GPUs {
		cands = append(cands, gpuCandidate{
			uuid:      g.UUID,
			deviceID:  g.ID,
			vramTotal: uint64(g.VRAMTotalMB) << 20,
			vramFree:  uint64(g.VRAMFreeMB) << 20,
		})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].uuid < cands[j].uuid })
	return cands
}

// heldTotals aggregates the ledger's HELD records per dimension.
type heldTotals struct {
	vramByUUID   map[string]uint64 // incl. each hold's KV portion
	ram          uint64
	pinned       uint64
	cpu          float64
	storageByDev map[string]uint64
	ports        map[int]bool
	exclusiveGPU map[string]bool
}

func totalsFromHeld(held []Record) heldTotals {
	t := heldTotals{
		vramByUUID:   map[string]uint64{},
		storageByDev: map[string]uint64{},
		ports:        map[int]bool{},
		exclusiveGPU: map[string]bool{},
	}
	for _, r := range held {
		t.ram += r.Assignment.RAMBytes
		t.pinned += r.Assignment.PinnedRAMBytes
		t.cpu += r.Assignment.CPUThreads
		for _, g := range r.Assignment.GPUs {
			t.vramByUUID[g.GPUUUID] += g.VRAMBytes
			if g.Exclusive {
				t.exclusiveGPU[g.GPUUUID] = true
			}
		}
		for _, s := range r.Assignment.Storage {
			t.storageByDev[s.DeviceID] += s.ReservedBytes
		}
		for _, p := range r.Assignment.Ports {
			t.ports[p] = true
		}
	}
	return t
}

// reserveAtomic performs the whole dimension check + insert + persist in
// ONE ledger transaction (any failure reserves NOTHING). v19.8.1: the
// transaction opens with the idempotency replay check (inside the lock —
// concurrent identical admits serialize; a mismatched principal or request
// fingerprint is the NAMED conflict, never a silently-inherited hold), a
// successful replay returns the prior record (replayed != nil) without
// reserving anything new, and the model/backend gates run INSIDE the same
// transaction AFTER the idempotency probe (the v19.8.1 post-release
// ordering remediation: a key reused by a different request is the
// idempotency conflict even when the replaying request itself is invalid —
// an unknown model must never mask the key-misuse event — and an exact
// replay returns the original decision even if the manifest was removed
// afterwards).
func (m *Manager) reserveAtomic(req AdmissionRequest, snap *inventory.InventorySnapshotV2, now time.Time) (*Record, *ResourceAssignment, string, time.Time, string, string) {
	var (
		assignment  ResourceAssignment
		admID       string
		leaseExpiry time.Time
		failCode    string
		failExpl    string
		replayed    *Record
	)

	err := m.ledger.withLock(func(view *ledgerView) error {
		held := totalsFromHeld(view.held())
		assignment = ResourceAssignment{}
		failCode, failExpl = ReasonOK, ""

		// ---- 0. the idempotency replay (INSIDE the transaction) ------
		if prior := view.byIdempotency(req.IdempotencyKey); prior != nil {
			if prior.Principal != req.Principal || prior.RequestFingerprint != requestFingerprint(req) {
				failCode, failExpl = ReasonIdempotencyConflict, fmt.Sprintf(
					"idempotency key %q is held by admission %s (principal %q, request fingerprint %s) — the replaying request differs (principal %q, fingerprint %s): the same key returns the ORIGINAL decision or a NAMED conflict, never another request's allocation (KPK-19.8.0-P2-04)",
					req.IdempotencyKey, prior.AdmissionID, prior.Principal, prior.RequestFingerprint, req.Principal, requestFingerprint(req))
				return errAbort
			}
			replayed = prior
			return nil
		}

		// ---- 0.5 the model + backend gates (INSIDE the transaction, AFTER --
		// the idempotency probe — the v19.8.1 post-release ordering fix): a
		// mismatched replay must surface idempotency-conflict (an unknown
		// model in the replaying request must never MASK the key misuse),
		// and an exact replay has already returned above, so a manifest
		// removed after the first admission cannot void the replay either.
		if modelReason, modelExpl, _ := m.checkModel(req.ModelID, req.BackendID, snap); modelReason != ReasonOK {
			failCode, failExpl = modelReason, modelExpl
			return errAbort
		}

		// ---- GPUs (exclusive per UUID; KV counts against the GPU) ------
		cands := sortedGPUCandidates(snap)
		kvLeft := req.Requirements.KVCacheBytes
		for i, g := range req.Requirements.GPU {
			demand := g.VRAMBytes
			kvHere := uint64(0)
			if kvLeft > 0 {
				// The KV cache is charged to the first GPU request
				// (deterministic; the engines allocate KV from VRAM).
				kvHere = kvLeft
				kvLeft = 0
				demand += kvHere
			}

			cand := pickGPU(cands, g.GPUUUID, demand, held, m.policy)
			if cand == nil {
				if g.GPUUUID != "" {
					if named := findGPU(cands, g.GPUUUID); named == nil {
						failCode, failExpl = ReasonGPUUnknownUUID, fmt.Sprintf("gpu[%d]: uuid %q is not in snapshot %s", i, g.GPUUUID, snap.SnapshotID)
					} else if held.exclusiveGPU[g.GPUUUID] {
						failCode, failExpl = ReasonGPUExclusiveConflict, fmt.Sprintf("gpu[%d]: uuid %q is already exclusively held", i, g.GPUUUID)
					} else {
						failCode, failExpl = ReasonGPUVRAMExhausted, fmt.Sprintf("gpu[%d]: uuid %q allocatable %d bytes < demand %d (free %d − reserved %d − safety %d)", i, g.GPUUUID, allocatableVRAM(named, held, m.policy), demand, named.vramFree, held.vramByUUID[g.GPUUUID], vramSafety(named, m.policy))
					}
				} else {
					failCode, failExpl = ReasonGPUNoCandidates, fmt.Sprintf("gpu[%d]: no GPU in %s has %d bytes allocatable (candidates checked in UUID order: %s)", i, snap.SnapshotID, demand, uuidList(cands))
				}
				return errAbort
			}

			assignment.GPUs = append(assignment.GPUs, GPUAssignment{
				GPUUUID:      cand.uuid,
				DeviceID:     cand.deviceID,
				VRAMBytes:    demand,
				KVCacheBytes: kvHere,
				Exclusive:    true,
			})
			held.vramByUUID[cand.uuid] += demand
			held.exclusiveGPU[cand.uuid] = true
		}
		assignment.KVCacheBytes = req.Requirements.KVCacheBytes

		// ---- host RAM --------------------------------------------------
		ramAlloc := saturatingSub(snap.Host.RAMAvailableBytes, held.ram+m.ramSafety(snap))
		if req.Requirements.RAMBytes > ramAlloc {
			failCode, failExpl = ReasonRAMExhausted, fmt.Sprintf(
				"ram_bytes %d > allocatable %d (observed MemAvailable %d − reserved %d − safety %d)",
				req.Requirements.RAMBytes, ramAlloc, snap.Host.RAMAvailableBytes, held.ram, m.ramSafety(snap))
			return errAbort
		}
		assignment.RAMBytes = req.Requirements.RAMBytes
		assignment.PinnedRAMBytes = req.Requirements.PinnedRAMBytes

		// ---- CPU threads ------------------------------------------------
		cpuAlloc := float64(snap.Host.CPUThreads) - m.policy.CPUSafetyThreads - held.cpu
		if cpuAlloc < 0 {
			cpuAlloc = 0
		}
		if req.Requirements.CPUThreads > cpuAlloc {
			failCode, failExpl = ReasonCPUExhausted, fmt.Sprintf(
				"cpu_threads %v > allocatable %v (observed %d − OS safety %v − reserved %v)",
				req.Requirements.CPUThreads, cpuAlloc, snap.Host.CPUThreads, m.policy.CPUSafetyThreads, held.cpu)
			return errAbort
		}
		assignment.CPUThreads = req.Requirements.CPUThreads

		// ---- storage ------------------------------------------------------
		for i, s := range req.Requirements.Storage {
			dev := findDevice(snap, s.DeviceID)
			if dev == nil {
				failCode, failExpl = ReasonStorageUnknownDevice, fmt.Sprintf("storage[%d]: device %q is not in snapshot %s", i, s.DeviceID, snap.SnapshotID)
				return errAbort
			}
			// Measured-performance floors: NEVER schedule from unverified
			// estimates — a floor without a measurement is refused. v19.8.1
			// (KPK-19.8.0-P1-01): the FULL measured contract — sequential MB/s +
			// random-read IOPS + p95 latency ceiling + the benchmark age bound
			// (SK P2-04) — enforced through the ONE shared predicate
			// (storage.CheckMeasuredFloors) so the planner-side qualification
			// and this final gate CANNOT drift apart.
			outcome, detail := storage.CheckMeasuredFloors(
				s.MinimumReadMBps, s.MinimumRandomReadIOPS, s.MaximumP95LatencyMs,
				dev.ReadProfile, now, m.policy.MaxBenchmarkAge)
			switch outcome {
			case storage.FloorsAbsent:
				failCode, failExpl = ReasonStorageNoReadProfile, fmt.Sprintf(
					"storage[%d]: device %q demands a measured floor but carries NO benchmark record — %s (run the opt-in --storage-benchmark with the random-read phase)",
					i, s.DeviceID, detail)
				return errAbort
			case storage.FloorsStale:
				failCode, failExpl = ReasonStorageReadProfileStale, fmt.Sprintf(
					"storage[%d]: device %q — %s",
					i, s.DeviceID, detail)
				return errAbort
			case storage.FloorsUnmet:
				failCode, failExpl = ReasonStorageReadProfileInsufficient, fmt.Sprintf(
					"storage[%d]: device %q — %s",
					i, s.DeviceID, detail)
				return errAbort
			}
			reserved := held.storageByDev[s.DeviceID]
			safety := uint64(float64(dev.CapacityBytes) * m.policy.StorageSafetyFraction)
			alloc := saturatingSub(dev.AvailableBytes, reserved+safety)
			if s.MinimumFreeBytes > alloc {
				failCode, failExpl = ReasonStorageExhausted, fmt.Sprintf(
					"storage[%d]: device %q free demand %d > allocatable %d (aggregate available %d − reserved %d − safety %d)",
					i, s.DeviceID, s.MinimumFreeBytes, alloc, dev.AvailableBytes, reserved, safety)
				return errAbort
			}
			assignment.Storage = append(assignment.Storage, StorageAssignment{
				DeviceID:      s.DeviceID,
				ReservedBytes: s.MinimumFreeBytes,
			})
			held.storageByDev[s.DeviceID] += s.MinimumFreeBytes
		}

		// ---- ports --------------------------------------------------------
		for i, p := range req.Requirements.Ports {
			if held.ports[p] {
				failCode, failExpl = ReasonPortConflict, fmt.Sprintf("ports[%d]: %d is already held by another admission", i, p)
				return errAbort
			}
			assignment.Ports = append(assignment.Ports, p)
			held.ports[p] = true
		}

		// ---- persist (the atomic reserve's step 7) -------------------------
		id, err := newAdmissionID()
		if err != nil {
			failCode, failExpl = ReasonLedgerUnavailable, "admission id unavailable: "+err.Error()
			return errAbort
		}
		leaseExpiry = now.Add(m.policy.LeaseTTL)
		view.insert(Record{
			AdmissionID:        id,
			RequestID:          req.RequestID,
			IdempotencyKey:     req.IdempotencyKey,
			Principal:          req.Principal,
			ModelID:            req.ModelID,
			BackendID:          req.BackendID,
			SnapshotID:         snap.SnapshotID,
			Requirements:       req.Requirements,
			Assignment:         assignment,
			Status:             StatusHeld,
			CreatedAt:          now,
			LeaseExpiresAt:     leaseExpiry,
			BootID:             bootIdentity(),
			RequestFingerprint: requestFingerprint(req),
		})
		admID = id
		return nil
	})

	if err != nil {
		if failCode == ReasonOK {
			failCode, failExpl = ReasonLedgerUnavailable, err.Error()
		}
		return nil, nil, "", time.Time{}, failCode, failExpl
	}
	if replayed != nil {
		// The replay path: nothing new was reserved; the prior record stands.
		return replayed, nil, "", time.Time{}, ReasonOK, ""
	}
	return nil, &assignment, admID, leaseExpiry, ReasonOK, ""
}

// errAbort marks a dimension failure inside the ledger transaction (the
// withLock wrapper rolls the append back; nothing was reserved).
var errAbort = fmt.Errorf("admission dimension check failed")

// pickGPU selects the candidate for one GPU request: the NAMED uuid, or
// the lowest-uuid candidate with capacity (deterministic).
func pickGPU(cands []gpuCandidate, uuid string, demand uint64, held heldTotals, policy Policy) *gpuCandidate {
	if uuid != "" {
		c := findGPU(cands, uuid)
		if c == nil || held.exclusiveGPU[uuid] || demand > allocatableVRAM(c, held, policy) {
			return nil
		}
		return c
	}
	for i := range cands {
		if held.exclusiveGPU[cands[i].uuid] {
			continue
		}
		if demand <= allocatableVRAM(&cands[i], held, policy) {
			return &cands[i]
		}
	}
	return nil
}

func findGPU(cands []gpuCandidate, uuid string) *gpuCandidate {
	for i := range cands {
		if cands[i].uuid == uuid {
			return &cands[i]
		}
	}
	return nil
}

func allocatableVRAM(c *gpuCandidate, held heldTotals, policy Policy) uint64 {
	if c == nil {
		return 0
	}
	return saturatingSub(c.vramFree, held.vramByUUID[c.uuid]+vramSafety(c, policy))
}

func vramSafety(c *gpuCandidate, policy Policy) uint64 {
	return uint64(float64(c.vramTotal) * policy.VRAMSafetyFraction)
}

func (m *Manager) ramSafety(snap *inventory.InventorySnapshotV2) uint64 {
	return uint64(float64(snap.Host.RAMTotalBytes) * m.policy.RAMSafetyFraction)
}

func findDevice(snap *inventory.InventorySnapshotV2, deviceID string) *storage.Device {
	for i := range snap.Storage {
		if snap.Storage[i].ID == deviceID {
			return &snap.Storage[i]
		}
	}
	return nil
}

func saturatingSub(v, sub uint64) uint64 {
	if sub > v {
		return 0
	}
	return v - sub
}

func uuidList(cands []gpuCandidate) string {
	parts := make([]string, 0, len(cands))
	for _, c := range cands {
		parts = append(parts, c.uuid)
	}
	return strings.Join(parts, ",")
}

func describeAssignment(a *ResourceAssignment) string {
	var parts []string
	for _, g := range a.GPUs {
		parts = append(parts, fmt.Sprintf("gpu %s exclusive %d MiB", g.GPUUUID, g.VRAMBytes>>20))
	}
	if a.RAMBytes > 0 {
		parts = append(parts, fmt.Sprintf("ram %d MiB", a.RAMBytes>>20))
	}
	if a.PinnedRAMBytes > 0 {
		parts = append(parts, fmt.Sprintf("pinned %d MiB", a.PinnedRAMBytes>>20))
	}
	if a.CPUThreads > 0 {
		parts = append(parts, fmt.Sprintf("cpu %v threads", a.CPUThreads))
	}
	for _, s := range a.Storage {
		parts = append(parts, fmt.Sprintf("storage %s %d MiB", s.DeviceID, s.ReservedBytes>>20))
	}
	if len(a.Ports) > 0 {
		ports := make([]string, 0, len(a.Ports))
		for _, p := range a.Ports {
			ports = append(ports, fmt.Sprintf("%d", p))
		}
		parts = append(parts, "ports "+strings.Join(ports, ","))
	}
	return strings.Join(parts, "; ")
}

// Release closes a HELD or ACTIVE admission (verified cleanup: the
// transition is persisted atomically; a double release is a NAMED error).
func (m *Manager) Release(admissionID string) (Record, error) {
	return m.ledger.release(admissionID, "explicit release by operator/planner", m.now().UTC())
}

// Renew extends a HELD admission's lease by the policy TTL (v19.8.1,
// KPK-19.8.0-P2-05: a hold whose workload is not yet started but whose
// preparation is longer than one lease gets a principal-bound, durable
// extension instead of silently losing its capacity). Renewing an ACTIVE
// record is a NAMED error (the lease no longer governs it).
func (m *Manager) Renew(admissionID, principal string) (Record, error) {
	return m.ledger.renew(admissionID, principal, m.policy.LeaseTTL, m.now().UTC())
}

// ConfirmStarted transitions a HELD admission to ACTIVE (v19.8.1,
// KPK-19.8.0-P2-05: the workload owner's explicit, principal-bound,
// idempotent act). From ACTIVE on, the lease expiry NEVER frees the hold
// (only explicit release or crash recovery do) — a timer alone must
// not free capacity a confirmed-start workload still consumes.
func (m *Manager) ConfirmStarted(admissionID, principal string) (Record, error) {
	return m.ledger.confirmStarted(admissionID, principal, m.now().UTC())
}

// Reconcile sweeps lease expiry (and, at construction, crash recovery):
// expired HELD records become EXPIRED with their capacity freed (ACTIVE
// records are never swept — v19.8.1). Returns the number of records
// closed.
func (m *Manager) Reconcile() int {
	return m.ledger.sweepExpired(m.now().UTC())
}

// Held returns the live capacity holds — HELD and ACTIVE alike
// (v19.8.1: a confirmed-start workload still consumes its reservation)
// in deterministic order.
func (m *Manager) Held() []Record {
	all := m.ledger.Records()
	out := make([]Record, 0, len(all))
	for _, r := range all {
		if r.Status == StatusHeld || r.Status == StatusActive {
			out = append(out, r)
		}
	}
	return out
}

// Records returns every record (live + the bounded audit trail).
func (m *Manager) Records() []Record { return m.ledger.Records() }

// requestFingerprint computes the canonical SHA-256 of an admission
// request's identity-bearing content (v19.8.1, KPK-19.8.0-P2-04): the
// principal, the request id, the model, the backend, and the FULL
// resource vector. json.Marshal of these structs is deterministic (fixed
// struct field order; no maps in the vector), so the fingerprint is
// stable for identical requests and differs for ANY identity-bearing
// change — exactly what an idempotency replay must prove.
func requestFingerprint(req AdmissionRequest) string {
	canon, err := json.Marshal(struct {
		Principal    string         `json:"principal"`
		RequestID    string         `json:"request_id"`
		ModelID      string         `json:"model_id"`
		BackendID    string         `json:"backend_id"`
		Requirements ResourceVector `json:"requirements"`
	}{
		Principal:    req.Principal,
		RequestID:    req.RequestID,
		ModelID:      req.ModelID,
		BackendID:    req.BackendID,
		Requirements: req.Requirements,
	})
	if err != nil {
		// Unreachable for these value types (no channels/functions/maps);
		// a marshal failure still fails CLOSED: the empty fingerprint
		// matches nothing and every replay is the NAMED conflict.
		return ""
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}
