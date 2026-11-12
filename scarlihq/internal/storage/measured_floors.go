// Package storage — measured_floors.go: the ONE measured-storage floor
// predicate (v19.8.1, KPK-19.8.0-P1-01).
//
// The v19.8.0 defect: the InferSSD qualification (infer_ssd.QualifySSD)
// checked sequential MB/s + random IOPS + p95 latency, but the aggregate
// ADMISSION gate checked only the sequential floor and free bytes — the
// same contract enforced twice, differently, and the weaker copy guarded
// the final reserve. For SSD-backed expert paging, random I/O and tail
// latency are the performance the strategy EXISTS to protect.
//
// The fix is structural, not patchy: the numeric predicate lives HERE,
// in the leaf package both consumers already import, as ONE pure
// function. The planner-side qualification (infer_ssd) and the
// admission-side revalidation now call the same code — they cannot
// drift apart again without deleting this call.
//
// The freshness (max-age) dimension: admission owns the clock (it
// already owns verification age — the same discipline), so the age bound
// rides the policy and is checked HERE at call time; QualifySSD passes a
// zero bound (its verdict stays deterministic over its inputs — the
// measured-at timestamp itself is a snapshot fact the caller can reason
// about, the wall clock is not).
package storage

import (
	"fmt"
	"time"
)

// FloorsOutcome — the closed verdict vocabulary of the measured-floor
// predicate. The callers map these onto their own named refusal codes
// (admission's Reason* / infer_ssd's Qualification*); the NUMBERS behind
// each verdict are computed once, here.
type FloorsOutcome int

const (
	// FloorsOK — every requested floor is measured and met.
	FloorsOK FloorsOutcome = iota
	// FloorsAbsent — no measured read profile exists (never schedule
	// from unverified estimates — the floors are meaningless without
	// their measurement).
	FloorsAbsent
	// FloorsStale — the profile exists but is older than the caller's
	// max-age bound (a benchmark describes the device it ran on, at the
	// time it ran; an arbitrarily old one is a different device's story).
	FloorsStale
	// FloorsUnmet — the profile is fresh but at least one requested
	// floor is unmeasured or below/above the demanded bound.
	FloorsUnmet
)

// CheckMeasuredFloors evaluates the measured-storage contract for ONE
// device's recorded ReadProfile against the requested floors:
//
//   - minSequentialMBps > 0 → SequentialReadMBps must be ≥ it;
//   - minRandomIOPS > 0     → RandomReadIOPS must be measured AND ≥ it
//     (an unmeasured random phase — 0 — is NOT "passes any floor");
//   - maxP95Ms > 0          → RandomReadP95Ms must be measured, > 0,
//     and ≤ it (tail latency is a ceiling, not a floor);
//   - maxAge > 0            → MeasuredAt must be within maxAge of now.
//
// A floor of 0 means "not requested" (the entry's other dimensions may
// still bind). The returned detail names every unmet bound with its
// numbers (measured vs demanded) — never prose over math. The detail is
// empty when the verdict is FloorsOK.
func CheckMeasuredFloors(minSequentialMBps, minRandomIOPS, maxP95Ms float64, rp *ReadProfile, now time.Time, maxAge time.Duration) (FloorsOutcome, string) {
	if rp == nil || rp.MeasuredAt.IsZero() {
		return FloorsAbsent, "the device carries NO measured read profile — a measured-performance floor without a measurement is a named refusal (run the opt-in storage benchmark with the random-read phase)"
	}
	if maxAge > 0 {
		if age := now.Sub(rp.MeasuredAt); age > maxAge {
			return FloorsStale, fmt.Sprintf(
				"the recorded benchmark is %s old (bound %s, measured %s, profile %s) — a benchmark older than the policy bound describes a device it may no longer be (re-run the operator benchmark)",
				age.Round(time.Second), maxAge, rp.MeasuredAt.UTC().Format(time.RFC3339), rp.ProfileID)
		}
	}
	var unmet []string
	if minSequentialMBps > 0 && rp.SequentialReadMBps < minSequentialMBps {
		unmet = append(unmet, fmt.Sprintf("sequential %.1f MB/s < the demanded floor %.1f MB/s", rp.SequentialReadMBps, minSequentialMBps))
	}
	if minRandomIOPS > 0 {
		if rp.RandomReadIOPS <= 0 {
			unmet = append(unmet, fmt.Sprintf("random-read IOPS is UNMEASURED (profile %s measured without the random-read phase) while the demand requires ≥ %.1f IOPS", rp.ProfileID, minRandomIOPS))
		} else if rp.RandomReadIOPS < minRandomIOPS {
			unmet = append(unmet, fmt.Sprintf("random-read %.1f IOPS < the demanded floor %.1f IOPS", rp.RandomReadIOPS, minRandomIOPS))
		}
	}
	if maxP95Ms > 0 {
		if rp.RandomReadP95Ms <= 0 {
			unmet = append(unmet, fmt.Sprintf("random-read p95 is UNMEASURED (profile %s measured without the random-read phase) while the demand requires ≤ %.3f ms", rp.ProfileID, maxP95Ms))
		} else if rp.RandomReadP95Ms > maxP95Ms {
			unmet = append(unmet, fmt.Sprintf("random-read p95 %.3f ms exceeds the demanded ceiling %.3f ms", rp.RandomReadP95Ms, maxP95Ms))
		}
	}
	if len(unmet) > 0 {
		return FloorsUnmet, fmt.Sprintf("measured floors unmet: %s", joinFloors(unmet))
	}
	return FloorsOK, ""
}

// joinFloors joins the unmet bounds deterministically (declaration order
// — the function above appends sequentially; no map iteration).
func joinFloors(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "; "
		}
		out += p
	}
	return out
}
