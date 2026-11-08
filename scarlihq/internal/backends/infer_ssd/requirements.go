// Package infer_ssd — requirements.go: the SSD performance
// qualification (roadmap §8.6) + the §8.2 GPU compatibility join —
// PURE functions over (profile, observed device/GPU), consumed by both
// the planner's deterministic evaluation and the adapter's own checks.
package infer_ssd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// QualificationCode — the closed, stable refusal vocabulary of the SSD
// qualification (every hard constraint carries its named refusal — the
// planner surfaces these as plan-* reason codes; a silent constraint is
// a missing constraint).
type QualificationCode string

const (
	// QualificationDeviceUnknown — the profile's storage device is not
	// in the observed inventory (a declared device that is not observed
	// is not a device).
	QualificationDeviceUnknown QualificationCode = "ssd-device-unknown"
	// QualificationNoReadProfile — the device carries NO measured read
	// profile: a measured-performance floor without a measurement is a
	// NAMED refusal (roadmap §8.6/§8.8: SSD I/O admission uses measured
	// conservative capacity — never schedule from unverified estimates).
	QualificationNoReadProfile QualificationCode = "ssd-no-read-profile"
	// QualificationFloorsUnmet — the device IS measured but the
	// measurement does not meet the profile's declared conservative
	// floors (sequential MB/s, random IOPS, or the p95 ceiling).
	QualificationFloorsUnmet QualificationCode = "ssd-floors-unmet"
	// QualificationFreeBytesInsufficient — the device's observed
	// available bytes cannot hold the expert storage demand + the
	// profile's minimum free floor.
	QualificationFreeBytesInsufficient QualificationCode = "ssd-free-bytes-insufficient"
)

// Qualification is the SSD qualification verdict for one (profile,
// device, demand) triple — the named code + the numbers behind it (the
// planner's explanation carries them verbatim: every number named,
// never prose over math).
type Qualification struct {
	Qualified bool
	Code      QualificationCode
	Detail    string
}

// QualifySSD evaluates the §8.6 qualification: the observed device must
// carry a MEASURED read profile whose sequential throughput, random
// IOPS and p95 latency meet the profile's declared conservative floors,
// and whose available bytes hold the demand + the minimum free floor.
// demandBytes is the EXPERT storage demand (the paged experts' bytes —
// the manifest's expert_count × expert_bytes; the shared weights live
// in VRAM under the ssd-backed-moe strategy).
//
// The floors ARE the conservative allocatable I/O budget (§8.6 step 7:
// "Derive a conservative allocatable I/O budget" — the operator derives
// it from their own measured runs and declares it as the floor; the
// qualification compares measured-vs-declared, never
// advertised-vs-hoped).
func QualifySSD(profile *RuntimeProfile, dev *storage.Device, demandBytes uint64) Qualification {
	if profile == nil {
		return Qualification{Code: QualificationNoReadProfile, Detail: "no runtime profile configured — the qualification has nothing to qualify against"}
	}
	if dev == nil {
		return Qualification{Code: QualificationDeviceUnknown, Detail: fmt.Sprintf("the profile's storage device %q is not in the observed inventory", profile.Resources.Storage.DeviceID)}
	}
	rp := dev.ReadProfile
	if rp == nil || rp.MeasuredAt.IsZero() {
		return Qualification{Code: QualificationNoReadProfile, Detail: fmt.Sprintf("device %q carries no measured read profile — a measured-performance floor without a measurement is a named refusal (roadmap §8.6: run the operator benchmark with the random-read phase before enabling SSD-backed scheduling)", dev.ID)}
	}
	floors := profile.Resources.Storage
	var unmet []string
	if rp.SequentialReadMBps < floors.MinimumReadMBps {
		unmet = append(unmet, fmt.Sprintf("sequential %.1f MB/s < the declared floor %.1f MB/s", rp.SequentialReadMBps, floors.MinimumReadMBps))
	}
	if rp.RandomReadIOPS < floors.MinimumRandomReadIOPS {
		unmet = append(unmet, fmt.Sprintf("random-read %.1f IOPS < the declared floor %.1f IOPS (v19.7.0's benchmark measured sequential only — the v19.8.0 random-read phase populates this; an unmeasured random floor refuses the qualification)", rp.RandomReadIOPS, floors.MinimumRandomReadIOPS))
	}
	if rp.RandomReadP95Ms <= 0 || rp.RandomReadP95Ms > floors.MaximumP95LatencyMs {
		unmet = append(unmet, fmt.Sprintf("random-read p95 %.3f ms is unmeasured or exceeds the declared ceiling %.3f ms", rp.RandomReadP95Ms, floors.MaximumP95LatencyMs))
	}
	if len(unmet) > 0 {
		return Qualification{Code: QualificationFloorsUnmet, Detail: fmt.Sprintf("device %q is measured but the floors are unmet: %s", dev.ID, strings.Join(unmet, "; "))}
	}
	need := demandBytes + floors.MinimumFreeBytes
	if dev.AvailableBytes < need {
		return Qualification{Code: QualificationFreeBytesInsufficient, Detail: fmt.Sprintf("device %q: expert demand %d bytes + the minimum free floor %d bytes = %d needed, %d available", dev.ID, demandBytes, floors.MinimumFreeBytes, need, dev.AvailableBytes)}
	}
	return Qualification{Qualified: true, Detail: fmt.Sprintf("device %q qualified: sequential %.1f MB/s ≥ %.1f, random %.1f IOPS ≥ %.1f, p95 %.3f ms ≤ %.3f, free %d ≥ %d bytes", dev.ID, rp.SequentialReadMBps, floors.MinimumReadMBps, rp.RandomReadIOPS, floors.MinimumRandomReadIOPS, rp.RandomReadP95Ms, floors.MaximumP95LatencyMs, dev.AvailableBytes, need)}
}

// ---------------------------------------------------------------------------
// The §8.2 GPU compatibility join
// ---------------------------------------------------------------------------

// GPUCompatibility joins the profile's declared §8.2 GPU dimensions
// (compute capabilities, exact CUDA, minimum driver) against ONE
// observed GPU. Fail-closed at every step: an unparseable observed
// compute capability, a CUDA mismatch, or a driver below the minimum is
// a named refusal — the profile's declared dimensions are the ONLY
// approved combinations (§8.2: "Do not assume compatibility ... for
// each candidate combination, record: GPU vendor, model, compute
// capability, driver").
//
// The observed CUDA must match the declared CUDA EXACTLY (the declared
// combination is the tested combination; "close enough" is an
// untested combination wearing a tested one's name).
func GPUCompatibility(profile *RuntimeProfile, gpu inventory.GPU) (bool, string) {
	if profile == nil {
		return false, "no runtime profile configured — the GPU compatibility join has nothing to join"
	}
	capSlug, ok := normalizeComputeCap(gpu.ComputeCap)
	if !ok {
		return false, fmt.Sprintf("the observed GPU %s reports unparseable compute capability %q — an unknown capability never passes a declared-combination join", gpu.Name, gpu.ComputeCap)
	}
	declared := false
	for _, c := range profile.Compatibility.GPUComputeCapabilities {
		if strings.EqualFold(strings.TrimSpace(c), capSlug) {
			declared = true
			break
		}
	}
	if !declared {
		return false, fmt.Sprintf("the observed GPU %s (%s) is not in the profile's declared capabilities %v (the declared combinations are the TESTED combinations — §8.2: do not assume compatibility)", capSlug, gpu.Name, profile.Compatibility.GPUComputeCapabilities)
	}
	if strings.TrimSpace(gpu.CUDA) != strings.TrimSpace(profile.Compatibility.CUDA) {
		return false, fmt.Sprintf("the observed driver CUDA %q does not exactly match the profile's declared CUDA %q (the tested combination is the approved combination)", gpu.CUDA, profile.Compatibility.CUDA)
	}
	if cmp, ok := compareVersionStrings(gpu.Driver, profile.Compatibility.DriverMinimum); !ok {
		return false, fmt.Sprintf("the observed driver version %q is unparseable against the profile's minimum %q — an unparseable version never passes a minimum-version join", gpu.Driver, profile.Compatibility.DriverMinimum)
	} else if cmp < 0 {
		return false, fmt.Sprintf("the observed driver %q is below the profile's declared minimum %q", gpu.Driver, profile.Compatibility.DriverMinimum)
	}
	return true, fmt.Sprintf("GPU %s: compute capability %s ∈ %v, CUDA %s matches exactly, driver %s ≥ minimum %s", gpu.Name, capSlug, profile.Compatibility.GPUComputeCapabilities, gpu.CUDA, gpu.Driver, profile.Compatibility.DriverMinimum)
}

// normalizeComputeCap converts an observed compute capability ("12.0",
// "9.0a", "8.6") into the capability slug form the profiles declare
// ("sm_120", "sm_90", "sm_86").
func normalizeComputeCap(observed string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(observed))
	s = strings.TrimSuffix(s, "a") // NVIDIA's architectural suffix (9.0a) — the base slug is the join key
	parts := strings.SplitN(s, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return "", false
	}
	if major < 0 || minor < 0 || major > 99 || minor > 99 {
		return "", false
	}
	return fmt.Sprintf("sm_%d", major*10+minor), true
}

// compareVersionStrings compares two dot-separated numeric versions
// (driver-style). Returns (cmp, ok): cmp < 0 when a < b, 0 when equal,
// > 0 when a > b; ok=false when either side has a non-numeric
// component (an unparseable version is a named refusal upstream, never
// a guess here).
func compareVersionStrings(a, b string) (int, bool) {
	pa := strings.Split(strings.TrimSpace(a), ".")
	pb := strings.Split(strings.TrimSpace(b), ".")
	if len(pa) == 0 || len(pb) == 0 {
		return 0, false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var na, nb int
		if i < len(pa) {
			v, err := strconv.Atoi(strings.TrimSpace(pa[i]))
			if err != nil {
				return 0, false
			}
			na = v
		}
		if i < len(pb) {
			v, err := strconv.Atoi(strings.TrimSpace(pb[i]))
			if err != nil {
				return 0, false
			}
			nb = v
		}
		if na != nb {
			if na < nb {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}
