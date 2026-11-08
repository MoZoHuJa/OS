// Package admission — the Manager: the deterministic, atomic aggregate
// admission gate (roadmap §6.4's pattern, steps 1-9).
package admission

import (
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
	// LeaseTTL — how long an admission holds before expiry sweeps it.
	LeaseTTL time.Duration
}

// DefaultPolicy is the documented v19.7.1 policy.
func DefaultPolicy() Policy {
	return Policy{
		PolicyVersion:         "admission/v19.7.1",
		MaxSnapshotAge:        60 * time.Second,
		VerificationMaxAge:    7 * 24 * time.Hour,
		VRAMSafetyFraction:    0.05,
		RAMSafetyFraction:     0.10,
		StorageSafetyFraction: 0.10,
		CPUSafetyThreads:      2,
		MaxPinnableRAMBytes:   0,
		LeaseTTL:              10 * time.Minute,
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

	// ---- idempotency ------------------------------------------------------
	if prior := m.ledger.byIdempotency(req.IdempotencyKey); prior != nil {
		return AdmissionDecision{
			Allowed:        true,
			ReasonCode:     ReasonOK,
			Explanation:    fmt.Sprintf("idempotent replay — admission %s (created %s) stands", prior.AdmissionID, prior.CreatedAt.Format(time.RFC3339)),
			AdmissionID:    prior.AdmissionID,
			Assignment:     &prior.Assignment,
			SnapshotID:     prior.SnapshotID,
			PolicyVersion:  m.policy.PolicyVersion,
			LeaseExpiresAt: prior.LeaseExpiresAt,
		}
	}

	// ---- 2. snapshot + freshness (HARD GATE) ------------------------------
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

	// ---- 3. model + backend compatibility ---------------------------------
	modelReason, modelExpl, manifest := m.checkModel(req.ModelID, req.BackendID, snap)
	if modelReason != ReasonOK {
		return refuse(base, modelReason, modelExpl)
	}
	_ = manifest // placement metadata already enforced by checkModel

	// ---- 4-7. the atomic dimension check + reserve -------------------------
	assignment, admID, leaseExpiry, failCode, failExpl := m.reserveAtomic(req, snap, now)
	if failCode != ReasonOK {
		return refuse(base, failCode, failExpl)
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
// ONE ledger transaction (any failure reserves NOTHING).
func (m *Manager) reserveAtomic(req AdmissionRequest, snap *inventory.InventorySnapshotV2, now time.Time) (*ResourceAssignment, string, time.Time, string, string) {
	var (
		assignment  ResourceAssignment
		admID       string
		leaseExpiry time.Time
		failCode    string
		failExpl    string
	)

	err := m.ledger.withLock(func(view *ledgerView) error {
		held := totalsFromHeld(view.held())
		assignment = ResourceAssignment{}
		failCode, failExpl = ReasonOK, ""

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
			// estimates — a floor without a measurement is refused.
			if s.MinimumReadMBps > 0 {
				if dev.ReadProfile == nil || dev.ReadProfile.MeasuredAt.IsZero() {
					failCode, failExpl = ReasonStorageNoReadProfile, fmt.Sprintf(
						"storage[%d]: device %q demands ≥%.0f MB/s measured read but carries NO benchmark record — an unmeasured floor is never assumed (run the opt-in --storage-benchmark)",
						i, s.DeviceID, s.MinimumReadMBps)
					return errAbort
				}
				if dev.ReadProfile.SequentialReadMBps < s.MinimumReadMBps {
					failCode, failExpl = ReasonStorageReadProfileInsufficient, fmt.Sprintf(
						"storage[%d]: device %q measured %.0f MB/s < required %.0f MB/s (profile %s)",
						i, s.DeviceID, dev.ReadProfile.SequentialReadMBps, s.MinimumReadMBps, dev.ReadProfile.ProfileID)
					return errAbort
				}
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
			AdmissionID:    id,
			RequestID:      req.RequestID,
			IdempotencyKey: req.IdempotencyKey,
			Principal:      req.Principal,
			ModelID:        req.ModelID,
			BackendID:      req.BackendID,
			SnapshotID:     snap.SnapshotID,
			Requirements:   req.Requirements,
			Assignment:     assignment,
			Status:         StatusHeld,
			CreatedAt:      now,
			LeaseExpiresAt: leaseExpiry,
			BootID:         bootIdentity(),
		})
		admID = id
		return nil
	})

	if err != nil {
		if failCode == ReasonOK {
			failCode, failExpl = ReasonLedgerUnavailable, err.Error()
		}
		return nil, "", time.Time{}, failCode, failExpl
	}
	return &assignment, admID, leaseExpiry, ReasonOK, ""
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

// Release closes a HELD admission (verified cleanup: the transition is
// persisted atomically; a double release is a NAMED error).
func (m *Manager) Release(admissionID string) (Record, error) {
	return m.ledger.release(admissionID, "explicit release by operator/planner", m.now().UTC())
}

// Reconcile sweeps lease expiry (and, at construction, crash recovery):
// expired HELD records become EXPIRED with their capacity freed. Returns
// the number of records closed.
func (m *Manager) Reconcile() int {
	return m.ledger.sweepExpired(m.now().UTC())
}

// Held returns the live holds (deterministic order).
func (m *Manager) Held() []Record {
	all := m.ledger.Records()
	out := make([]Record, 0, len(all))
	for _, r := range all {
		if r.Status == StatusHeld {
			out = append(out, r)
		}
	}
	return out
}

// Records returns every record (live + the bounded audit trail).
func (m *Manager) Records() []Record { return m.ledger.Records() }
