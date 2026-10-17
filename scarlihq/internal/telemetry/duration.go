package telemetry

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseRetentionDuration parses a duration string that supports Go's standard
// time.Duration units (ns, us, ms, s, m, h) plus 'd' (days) and 'w' (weeks),
// which Go's time.ParseDuration does NOT support.
//
// This is needed because the systemd timer calls `scarlix-monitor --prune 30d`
// and Go's time.ParseDuration("30d") returns an error.
//
// v19.1.17 P1 (A-01): Was: time.ParseDuration which rejected "30d" → prune never ran.
// Now: supports "30d", "2w", "12h", "720h" etc.
// v19.1.18 P1 (A-08): Reject explicit +/- signs in the d/w branch. Was: "+1d" passed
//
//	through (time.ParseDuration fails on 'd', then numPart="+1" → ParseFloat ok →
//	accepted). Now: reject any sign prefix for consistency with standard Go duration
//	which also rejects "+1h" (actually Go accepts +1h, but we reject for d/w strictness).
//
// v19.1.18 P1 (A-09): Check result > 0 AFTER conversion to time.Duration. Was: a
//
//	sub-nanosecond value like "0.0000000000001d" passed the num > 0 float check but
//	truncated to 0 when cast to time.Duration (int64 nanoseconds). Now: reject result ≤ 0.
func ParseRetentionDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Try standard Go ParseDuration first (handles ns, us, ms, s, m, h)
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, fmt.Errorf("duration must be positive, got %v", d)
		}
		return d, nil
	}

	// Handle 'd' (days) and 'w' (weeks) suffixes
	// Find the suffix part
	var numPart, suffixPart string
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] >= '0' && s[i] <= '9' || s[i] == '.' {
			numPart = s[:i+1]
			suffixPart = s[i+1:]
			break
		}
	}

	if numPart == "" || suffixPart == "" {
		return 0, fmt.Errorf("invalid duration format: %q", s)
	}

	// v19.1.18 P1 (A-08): Reject explicit sign prefixes in the d/w branch.
	// time.ParseDuration already handles "+1h"/"-1h" (rejects negative via the d<=0
	// check above). For the d/w branch, reject signs explicitly so "+1d"/"-1d"
	// can't slip through with unexpected semantics.
	if numPart[0] == '+' || numPart[0] == '-' {
		return 0, fmt.Errorf("duration must not have explicit sign: %q", s)
	}

	num, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q in duration: %v", numPart, err)
	}

	if num <= 0 {
		return 0, fmt.Errorf("duration must be positive, got %v", num)
	}

	var hours float64
	switch suffixPart {
	case "d":
		hours = num * 24
	case "w":
		hours = num * 24 * 7
	default:
		return 0, fmt.Errorf("unsupported unit %q (use ns, us, ms, s, m, h, d, or w)", suffixPart)
	}

	// Check for overflow
	if hours > float64(^uint64(0)>>1)/float64(time.Hour) {
		return 0, fmt.Errorf("duration overflow: %q", s)
	}

	result := time.Duration(hours * float64(time.Hour))

	// v19.1.18 P1 (A-09): A very small positive float (e.g. "0.0000000000001d")
	// passes the num > 0 check but truncates to 0 when cast to time.Duration
	// (int64 nanoseconds). Reject it explicitly.
	if result <= 0 {
		return 0, fmt.Errorf("duration rounds to zero or negative: %q (num=%v, result=%v)", s, num, result)
	}

	return result, nil
}
