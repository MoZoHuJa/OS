package telemetry

import (
	"testing"
	"time"
)

func TestParseRetentionDuration_Days(t *testing.T) {
	d, err := ParseRetentionDuration("30d")
	if err != nil {
		t.Fatalf("30d should parse: %v", err)
	}
	expected := 30 * 24 * time.Hour
	if d != expected {
		t.Errorf("30d = %v, want %v", d, expected)
	}
}

func TestParseRetentionDuration_Weeks(t *testing.T) {
	d, err := ParseRetentionDuration("2w")
	if err != nil {
		t.Fatalf("2w should parse: %v", err)
	}
	expected := 2 * 7 * 24 * time.Hour
	if d != expected {
		t.Errorf("2w = %v, want %v", d, expected)
	}
}

func TestParseRetentionDuration_Hours(t *testing.T) {
	d, err := ParseRetentionDuration("12h")
	if err != nil {
		t.Fatalf("12h should parse: %v", err)
	}
	expected := 12 * time.Hour
	if d != expected {
		t.Errorf("12h = %v, want %v", d, expected)
	}
}

func TestParseRetentionDuration_StandardDuration(t *testing.T) {
	// "720h" should also work (standard Go duration)
	d, err := ParseRetentionDuration("720h")
	if err != nil {
		t.Fatalf("720h should parse: %v", err)
	}
	expected := 720 * time.Hour
	if d != expected {
		t.Errorf("720h = %v, want %v", d, expected)
	}
}

func TestParseRetentionDuration_Zero(t *testing.T) {
	_, err := ParseRetentionDuration("0d")
	if err == nil {
		t.Error("0d should be rejected (must be positive)")
	}
}

func TestParseRetentionDuration_Negative(t *testing.T) {
	_, err := ParseRetentionDuration("-1h")
	if err == nil {
		t.Error("-1h should be rejected (must be positive)")
	}
}

func TestParseRetentionDuration_Invalid(t *testing.T) {
	_, err := ParseRetentionDuration("invalid")
	if err == nil {
		t.Error("invalid should be rejected")
	}
}

func TestParseRetentionDuration_Empty(t *testing.T) {
	_, err := ParseRetentionDuration("")
	if err == nil {
		t.Error("empty should be rejected")
	}
}

func TestParseRetentionDuration_UnsupportedUnit(t *testing.T) {
	_, err := ParseRetentionDuration("5y")
	if err == nil {
		t.Error("5y should be rejected (unsupported unit)")
	}
}

// v19.1.18 P1 (A-08): explicit sign prefixes in d/w branch must be rejected.
func TestParseRetentionDuration_NegativeDay(t *testing.T) {
	_, err := ParseRetentionDuration("-1d")
	if err == nil {
		t.Error("-1d should be rejected (explicit sign)")
	}
}

func TestParseRetentionDuration_NegativeWeek(t *testing.T) {
	_, err := ParseRetentionDuration("-1w")
	if err == nil {
		t.Error("-1w should be rejected (explicit sign)")
	}
}

func TestParseRetentionDuration_PlusDay(t *testing.T) {
	_, err := ParseRetentionDuration("+1d")
	if err == nil {
		t.Error("+1d should be rejected (explicit sign)")
	}
}

func TestParseRetentionDuration_PlusWeek(t *testing.T) {
	_, err := ParseRetentionDuration("+1w")
	if err == nil {
		t.Error("+1w should be rejected (explicit sign)")
	}
}

// v19.1.18 P1 (A-09): sub-nanosecond result must be rejected (rounds to zero).
func TestParseRetentionDuration_SubNanoSecondResult(t *testing.T) {
	// 1e-15 days ≈ 86 femtoseconds → 0.0864 nanoseconds → truncates to 0
	// time.Duration is int64 nanoseconds, so anything < 0.5ns rounds to 0.
	_, err := ParseRetentionDuration("0.000000000000001d")
	if err == nil {
		t.Error("0.000000000000001d should be rejected (rounds to zero duration)")
	}
}

func TestParseRetentionDuration_VerySmallButValid(t *testing.T) {
	// 1e-10 days ≈ 86.4 nanoseconds → should be > 0 after truncation
	d, err := ParseRetentionDuration("0.0000000001d")
	if err != nil {
		t.Fatalf("0.0000000001d should parse (86ns > 0): %v", err)
	}
	if d <= 0 {
		t.Errorf("0.0000000001d = %v, want > 0", d)
	}
}
