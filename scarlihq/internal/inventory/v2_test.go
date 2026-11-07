package inventory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/modelmeta"
)

// TestSnapshotV2Shape — the envelope contract (roadmap §5.6): schema
// version, snapshot ID, observation timestamp, per-component sources,
// and NON-NIL slices everywhere.
func TestSnapshotV2Shape(t *testing.T) {
	snap, err := BuildSnapshotV2()
	if err != nil {
		t.Fatalf("BuildSnapshotV2 failed: %v", err)
	}
	if snap.SchemaVersion != "scarlix.inventory/v2" {
		t.Errorf("schema_version=%q", snap.SchemaVersion)
	}
	if len(snap.SnapshotID) != 32 {
		t.Errorf("snapshot_id=%q, want 32 hex chars", snap.SnapshotID)
	}
	if snap.ObservedAt.IsZero() {
		t.Error("observed_at zero — the freshness anchor is required")
	}
	for name, slice := range map[string]interface{}{
		"gpus":     snap.GPUs,
		"storage":  snap.Storage,
		"runtimes": snap.Runtimes,
		"models":   snap.Models,
		"health":   snap.Health,
		"sources":  snap.Sources,
	} {
		if slice == nil {
			t.Errorf("%s is nil — the []-not-null contract is absolute", name)
		}
	}
	// Sources cover the components the roadmap names.
	wantComponents := map[string]bool{
		"storage": false, "storage.host": false, "gpus": false,
		"runtimes": false, "models": false, "models.manifests": false,
	}
	for _, s := range snap.Sources {
		if _, ok := wantComponents[s.Component]; ok {
			wantComponents[s.Component] = true
		}
	}
	for c, seen := range wantComponents {
		if !seen {
			t.Errorf("sources missing component %q", c)
		}
	}
}

func TestSnapshotV2UniqueIDs(t *testing.T) {
	a, _ := BuildSnapshotV2()
	b, _ := BuildSnapshotV2()
	if a.SnapshotID == b.SnapshotID {
		t.Error("two snapshots share an ID — every build mints a fresh identity")
	}
}

func TestFreshnessMath(t *testing.T) {
	snap := &InventorySnapshotV2{
		SchemaVersion: SchemaVersionV2,
		SnapshotID:    "x",
		ObservedAt:    time.Now().UTC().Add(-2 * time.Minute),
	}
	if snap.FreshWithin(time.Minute) {
		t.Error("a 2-minute-old snapshot is stale under a 1-minute policy")
	}
	if !snap.FreshWithin(5 * time.Minute) {
		t.Error("a 2-minute-old snapshot is fresh under a 5-minute policy")
	}
	// Zero/negative policy = always stale (fail-closed: an unset policy
	// must not bless data).
	if snap.FreshWithin(0) || snap.FreshWithin(-time.Second) {
		t.Error("a non-positive max age must always evaluate stale")
	}

	resp := snap.Response(time.Minute)
	if !resp.Freshness.Stale {
		t.Error("response freshness must mark the stale snapshot")
	}
	if resp.Freshness.MaxAgeSeconds != 60 {
		t.Errorf("max_age_seconds=%v", resp.Freshness.MaxAgeSeconds)
	}
	if resp.Freshness.AgeSeconds < 119 || resp.Freshness.AgeSeconds > 121 {
		t.Errorf("age_seconds=%v, want ~120", resp.Freshness.AgeSeconds)
	}
}

func TestFreshnessServeTime(t *testing.T) {
	// A JUST-built snapshot is fresh; the response carries the serve-time
	// evaluation (age ~0, stale false).
	snap, _ := BuildSnapshotV2()
	resp := snap.Response(time.Minute)
	if resp.Freshness.Stale {
		t.Error("a fresh snapshot must not be marked stale")
	}
	if resp.Freshness.AgeSeconds > 1 {
		t.Errorf("age_seconds=%v, want ~0", resp.Freshness.AgeSeconds)
	}
}

// TestV1FrozenContractIntact — v2 is additive: the frozen v1 JSON keys
// still marshal exactly as the v19.0.7 contract froze them (the v1
// compatibility gate the roadmap's exit criteria demand).
func TestV1FrozenContractIntact(t *testing.T) {
	g := GPU{ID: "gpu.nvidia.0", Index: 0, Vendor: "nvidia", Name: "RTX 5060 Ti",
		VRAMTotalMB: 16384, VRAMUsedMB: 100, VRAMFreeMB: 16284,
		UtilizationPct: 5, TemperatureC: 40, PowerW: 12.5,
		Driver: "570.0", CUDA: "12.8", ComputeCap: "12.0", Healthy: true}
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "index", "vendor", "name", "vram_total_mb",
		"vram_used_mb", "vram_free_mb", "utilization_percent", "temperature_c",
		"power_w", "driver", "cuda", "compute_cap", "healthy"} {
		if _, ok := m[key]; !ok {
			t.Errorf("frozen v1 GPU key %q missing from %s", key, raw)
		}
	}
	// The internal UUID is deliberately excluded from public JSON.
	if _, ok := m["uuid"]; ok {
		t.Errorf("uuid leaked into public GPU JSON: %s", raw)
	}

	st := SystemStatus{
		Version: "19.7.0", Mode: "ai", UptimeSeconds: 1, Timestamp: "now",
		GPUs: []GPU{}, Runtimes: []Runtime{}, Models: []Model{},
		Services: []Service{}, Health: []Health{},
	}
	rawS, _ := json.Marshal(st)
	if !strings.Contains(string(rawS), `"gpus":[]`) {
		t.Errorf("v1 non-nil slice contract: %s", rawS)
	}
}

// TestModelExecutionViewJSON — the merged model view marshals with the
// honest-absence shape (no execution → no execution key; placement
// reasons always present).
func TestModelExecutionViewJSON(t *testing.T) {
	v := ModelExecutionView{
		Catalog:        Model{ID: "sglang", Path: "/models/Qwen3-14B-AWQ"},
		ManifestStatus: "none",
		Placement: modelmeta.PlacementEligibility{
			Eligible: false,
			Reasons:  []string{"no verified manifest declares this model"},
		},
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["execution"]; ok {
		t.Errorf("execution key present with nil execution — omitempty required: %s", raw)
	}
	if _, ok := m["placement"]; !ok {
		t.Errorf("placement gate missing: %s", raw)
	}
	if _, ok := m["manifest_status"]; !ok {
		t.Errorf("manifest_status missing: %s", raw)
	}
}
