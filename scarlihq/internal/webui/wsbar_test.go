package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/agentfabric"
)

// BF-3 (v19.5.2): /ws/bar negative contracts — the upgrade is refused
// BEFORE it happens when the surface cannot honestly serve:
//
//   - nil bus (fabric off) → 503 (never a silently-dead push surface);
//   - missing ticket → 401;
//   - invalid/expired ticket → 401 (single-use reservation discipline).
//
// The full push flow (fabric.subscribed frame + command.*/execution.*
// delivery + single-use rejection after a consumed ticket) is covered by
// the E2E suite on the real binary.
func TestBarWSNilBusIs503BeforeUpgrade(t *testing.T) {
	mux := http.NewServeMux()
	RegisterBarWS(mux, nil)
	req := httptest.NewRequest(http.MethodGet, "/ws/bar?ticket=anything", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("nil bus: %d, want 503 before the upgrade (the fabric is off)", rec.Code)
	}
}

func TestBarWSMissingTicketIs401(t *testing.T) {
	mux := http.NewServeMux()
	RegisterBarWS(mux, agentfabric.NewFabricBus())
	req := httptest.NewRequest(http.MethodGet, "/ws/bar", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing ticket: %d, want 401", rec.Code)
	}
}

func TestBarWSInvalidTicketIs401(t *testing.T) {
	mux := http.NewServeMux()
	RegisterBarWS(mux, agentfabric.NewFabricBus())
	req := httptest.NewRequest(http.MethodGet, "/ws/bar?ticket=not-a-real-ticket", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid ticket: %d, want 401 (single-use reservation discipline)", rec.Code)
	}
}
