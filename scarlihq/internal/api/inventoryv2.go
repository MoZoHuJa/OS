// Package api — the inventory v2 read surface (v19.7.0, Compute Fabric
// Phase 1, roadmap §5.6).
//
// Three READ-ONLY endpoints, behind the standard Bearer auth (the same
// read gate as /api/status — any authenticated principal may OBSERVE;
// observation grants no control):
//
//	GET /api/inventory/v2          — the full unified snapshot + freshness
//	GET /api/inventory/v2/storage  — storage devices + host resources + freshness
//	GET /api/inventory/v2/models   — model execution views + freshness
//
// Non-GET is 405 BY NAME ("inventory is read-only") — the gaming-
// inventory precedent. There is NO write surface here, no benchmark
// trigger, no reservation: the roadmap's boundary — "Inventory must not
// execute workloads or reserve capacity" — is enforced by the route set
// itself (nothing but GET handlers exists to call).
//
// Freshness: every response carries the serve-time staleness evaluation
// (observed_at, age_seconds, max_age_seconds, stale) against
// SCARLIHQ_INVENTORY_MAX_AGE (default 60s; an unparseable value falls
// back to the default — this is an informational marker, not an auth
// boundary; the PLANNER must still reject or downgrade stale data, never
// reuse an old free-VRAM/free-space value as current capacity).
package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/storage"
)

// inventoryMaxAgeEnv is the freshness policy knob (seconds). Default 60s:
// a snapshot older than a minute describes a machine that may have
// changed — the operator narrows or widens the window explicitly.
const (
	inventoryMaxAgeEnv = "SCARLIHQ_INVENTORY_MAX_AGE"
	inventoryMaxAgeDef = 60 * time.Second
)

// inventoryV2MaxAge resolves the freshness threshold per request. Parse
// failure falls back to the default (documented above — informational
// marker semantics, fail-visible through the returned fallback flag).
func inventoryV2MaxAge() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(inventoryMaxAgeEnv))
	if raw == "" {
		return inventoryMaxAgeDef, true
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return inventoryMaxAgeDef, false
	}
	return time.Duration(secs) * time.Second, true
}

// ConfigureInventoryV2 injects the snapshot collector seam (tests pass a
// fixed snapshot; production leaves it nil for the real read-only
// collector). The collector returns the BUILT snapshot — freshness is
// evaluated at serve time by the handlers.
func (h *Handler) ConfigureInventoryV2(collect func() (*inventory.InventorySnapshotV2, error)) {
	h.inventoryV2 = collect
}

// inventoryV2Collect runs the collector (the seam, or the real one).
func (h *Handler) inventoryV2Collect() (*inventory.InventorySnapshotV2, error) {
	if h.inventoryV2 != nil {
		return h.inventoryV2()
	}
	return inventory.BuildSnapshotV2()
}

// inventoryV2Full serves the complete unified snapshot.
func (h *Handler) inventoryV2Full(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "inventory is read-only (GET)")
		return
	}
	snap, err := h.inventoryV2Collect()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "inventory snapshot unavailable: "+err.Error())
		return
	}
	maxAge, _ := inventoryV2MaxAge()
	writeJSON(w, snap.Response(maxAge))
}

// inventoryV2StorageView is the storage-section response: the freshness
// evaluation + the devices + the host resources + the storage-relevant
// source statuses (the FULL snapshot is not serialized for a section
// request — the section view is its own honest shape).
type inventoryV2StorageView struct {
	Freshness inventory.Freshness    `json:"freshness"`
	Host      storage.HostResources  `json:"host"`
	Storage   []storage.Device       `json:"storage"`
	Sources   []storage.SourceStatus `json:"sources"`
}

// inventoryV2Storage serves the storage + host section.
func (h *Handler) inventoryV2Storage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "inventory is read-only (GET)")
		return
	}
	snap, err := h.inventoryV2Collect()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "inventory snapshot unavailable: "+err.Error())
		return
	}
	maxAge, _ := inventoryV2MaxAge()
	resp := snap.Response(maxAge)
	view := inventoryV2StorageView{
		Freshness: resp.Freshness,
		Host:      snap.Host,
		Storage:   snap.Storage,
		Sources:   filterSources(snap.Sources, "storage", "storage.host"),
	}
	writeJSON(w, view)
}

// inventoryV2ModelsView is the models-section response.
type inventoryV2ModelsView struct {
	Freshness inventory.Freshness            `json:"freshness"`
	Models    []inventory.ModelExecutionView `json:"models"`
	Sources   []storage.SourceStatus         `json:"sources"`
}

// inventoryV2Models serves the model execution views.
func (h *Handler) inventoryV2Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "inventory is read-only (GET)")
		return
	}
	snap, err := h.inventoryV2Collect()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "inventory snapshot unavailable: "+err.Error())
		return
	}
	maxAge, _ := inventoryV2MaxAge()
	resp := snap.Response(maxAge)
	view := inventoryV2ModelsView{
		Freshness: resp.Freshness,
		Models:    snap.Models,
		Sources:   filterSources(snap.Sources, "models", "models.manifests"),
	}
	writeJSON(w, view)
}

// filterSources keeps the source statuses whose Component matches one of
// the wanted components (never nil — the []-not-null contract).
func filterSources(srcs []storage.SourceStatus, want ...string) []storage.SourceStatus {
	set := make(map[string]bool, len(want))
	for _, w := range want {
		set[w] = true
	}
	out := make([]storage.SourceStatus, 0, len(srcs))
	for _, s := range srcs {
		if set[s.Component] {
			out = append(out, s)
		}
	}
	return out
}
