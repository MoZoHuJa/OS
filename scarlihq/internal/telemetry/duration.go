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

	return time.Duration(hours * float64(time.Hour)), nil
}
