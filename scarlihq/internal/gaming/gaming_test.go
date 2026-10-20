package gaming

import (
	"encoding/json"
	"testing"
)

// v19.3.1: contract tests for the typed gaming inventory (Gaming Stack
// Integration Plan, Phase 4). The wire contract is what dashboards and any
// future MCP surface consume, so the enum validation and JSON shape are
// regression-protected here.

func TestValidKindMatrix(t *testing.T) {
	valid := []Kind{KindLauncher, KindCompatibility, KindPerformance, KindStreaming, KindServer}
	for _, k := range valid {
		if !ValidKind(k) {
			t.Errorf("ValidKind(%q) = false, want true", k)
		}
	}
	for _, k := range []Kind{"", "Launcher", "game", "unknown"} {
		if ValidKind(k) {
			t.Errorf("ValidKind(%q) = true, want false", k)
		}
	}
}

func TestValidSourceMatrix(t *testing.T) {
	valid := []Source{SourcePacman, SourceAUR, SourceFlatpak, SourceSystemd, SourceCompose}
	for _, s := range valid {
		if !ValidSource(s) {
			t.Errorf("ValidSource(%q) = false, want true", s)
		}
	}
	for _, s := range []Source{"", "PACMAN", "docker", "shell"} {
		if ValidSource(s) {
			t.Errorf("ValidSource(%q) = true, want false", s)
		}
	}
}

func TestValidHealthMatrix(t *testing.T) {
	valid := []Health{HealthNotInstalled, HealthNotApplicable, HealthStopped,
		HealthRunning, HealthUnhealthy, HealthUnknown}
	for _, h := range valid {
		if !ValidHealth(h) {
			t.Errorf("ValidHealth(%q) = false, want true", h)
		}
	}
	for _, h := range []Health{"", "ok", "RUNNING"} {
		if ValidHealth(h) {
			t.Errorf("ValidHealth(%q) = true, want false", h)
		}
	}
}

func TestMarshalJSONEntriesNilIsArray(t *testing.T) {
	b, err := MarshalJSONEntries(nil)
	if err != nil {
		t.Fatalf("marshal nil: %v", err)
	}
	if string(b) != "[]" {
		t.Fatalf("nil entries marshalled to %s, want []", b)
	}
}

func TestEntryWireShape(t *testing.T) {
	e := Entry{
		ID:          "steam",
		Name:        "Steam",
		Kind:        KindLauncher,
		Source:      SourcePacman,
		Installed:   true,
		Version:     "1.0.0.87-3",
		Running:     false,
		Health:      HealthNotApplicable,
		Optional:    false,
		LastChecked: "2026-10-20T15:00:00+02:00",
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round Entry
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round != e {
		t.Fatalf("round-trip mismatch: %+v != %+v", round, e)
	}
	// A not-installed entry must serialize without an empty version key.
	b, _ = json.Marshal(Entry{ID: "heroic-games-launcher-bin", Kind: KindLauncher, Source: SourceAUR, Health: HealthNotInstalled})
	if jsonFieldExists(string(b), "version") {
		t.Fatalf("not-installed entry should omit version, got %s", b)
	}
}

func jsonFieldExists(s, field string) bool {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return false
	}
	_, ok := m[field]
	return ok
}
