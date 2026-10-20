package reservationops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/reservation"
)

// --- HostInventoryProvider ---

func TestNewHostInventoryProvider_PortValidation(t *testing.T) {
	if _, err := NewHostInventoryProvider([]int{0}); err == nil {
		t.Fatal("port 0 must be rejected")
	}
	if _, err := NewHostInventoryProvider([]int{4000, 4000}); err == nil {
		t.Fatal("duplicate ports must be rejected")
	}
	if _, err := NewHostInventoryProvider(nil); err != nil {
		t.Fatalf("nil ports must be accepted: %v", err)
	}
	if _, err := NewHostInventoryProvider([]int{30000}); err != nil {
		t.Fatalf("valid ports must be accepted: %v", err)
	}
}

func TestHostInventoryProvider_GetSnapshot_ManagedPortsReservable(t *testing.T) {
	// In a sandbox without nvidia-smi/docker/models.yaml, the collectors
	// degrade to empty slices — the managed ports must STILL be reservable,
	// and the snapshot must be coherent (non-zero generation + timestamp).
	p, err := NewHostInventoryProvider([]int{30000, 8089})
	if err != nil {
		t.Fatalf("provider rejected: %v", err)
	}
	snap := p.GetSnapshot()
	if snap.Generation == "" {
		t.Fatal("snapshot generation must never be empty")
	}
	if snap.AsOf.IsZero() {
		t.Fatal("snapshot timestamp must never be zero")
	}
	if !snap.ResourceExists("port.30000") || !snap.ResourceExists("port.8089") {
		t.Fatal("managed ports must exist in the snapshot")
	}
	if snap.ResourceExists("port.11434") {
		t.Fatal("an unmanaged port must NOT be reservable")
	}
}

// --- DockerCleanupVerifier ---

func fixedNow() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }

func TestDockerCleanupVerifier_Port(t *testing.T) {
	running := []ManagedContainer{{
		Labels: map[string]string{"scarlix.managed": "true", "scarlix.port": "30000"},
	}}
	empty := []ManagedContainer{}

	verifier := newCleanupVerifierForTests(func(ctx context.Context) ([]ManagedContainer, error) { return running, nil }, fixedNow)
	evidence, err := verifier.VerifyCleanup("port.30000", reservation.NewUserIdentity("system.reservation-governor"))
	if err != nil || evidence.Verified() {
		t.Fatalf("occupied port must NOT verify clean (err=%v verified=%t)", err, evidence.Verified())
	}

	verifier = newCleanupVerifierForTests(func(ctx context.Context) ([]ManagedContainer, error) { return empty, nil }, fixedNow)
	evidence, err = verifier.VerifyCleanup("port.30000", reservation.NewUserIdentity("system.reservation-governor"))
	if err != nil || !evidence.Verified() {
		t.Fatalf("free port must verify clean (err=%v verified=%t)", err, evidence.Verified())
	}
	if evidence.ResourceID() != "port.30000" {
		t.Fatalf("evidence must carry the resource ID, got %q", evidence.ResourceID())
	}
	if evidence.ObservedAt() != fixedNow() {
		t.Fatalf("evidence timestamp mismatch: %v", evidence.ObservedAt())
	}
}

func TestDockerCleanupVerifier_RuntimeAndModel(t *testing.T) {
	running := []ManagedContainer{{
		Labels: map[string]string{"scarlix.runtime_id": "sglang", "scarlix.model_id": "qwen-coder"},
	}}
	list := func(ctx context.Context) ([]ManagedContainer, error) { return running, nil }
	verifier := newCleanupVerifierForTests(list, fixedNow)

	for _, id := range []string{"runtime.sglang", "model.qwen-coder"} {
		evidence, err := verifier.VerifyCleanup(id, reservation.NewUserIdentity("r"))
		if err != nil || evidence.Verified() {
			t.Fatalf("occupied %s must NOT verify clean (err=%v)", id, err)
		}
	}
	for _, id := range []string{"runtime.vllm", "model.llama"} {
		evidence, err := verifier.VerifyCleanup(id, reservation.NewUserIdentity("r"))
		if err != nil || !evidence.Verified() {
			t.Fatalf("free %s must verify clean (err=%v)", id, err)
		}
	}
}

func TestDockerCleanupVerifier_UnknownResourceFailsClosed(t *testing.T) {
	verifier := newCleanupVerifierForTests(func(ctx context.Context) ([]ManagedContainer, error) { return nil, nil }, fixedNow)
	for _, id := range []string{"disk.sda", "gpu.amd.1234", "port.abc", "gpu.nvidia."} {
		evidence, err := verifier.VerifyCleanup(id, reservation.NewUserIdentity("r"))
		if err != nil {
			t.Fatalf("classification must not error for %q (got %v) — fail-closed evidence instead", id, err)
		}
		if evidence.Verified() {
			t.Fatalf("unknown/unmappable resource %q must fail closed (verified=false)", id)
		}
	}
}

func TestDockerCleanupVerifier_DockerUnavailableIsError(t *testing.T) {
	verifier := newCleanupVerifierForTests(func(ctx context.Context) ([]ManagedContainer, error) {
		return nil, errors.New("docker: command not found")
	}, fixedNow)
	if _, err := verifier.VerifyCleanup("port.30000", reservation.NewUserIdentity("r")); err == nil {
		t.Fatal("docker unavailability must surface as an error (retried next tick), never as clean evidence")
	}
}

func TestDockerCleanupVerifier_EvidenceCarriesReconcilerPrincipal(t *testing.T) {
	verifier := newCleanupVerifierForTests(func(ctx context.Context) ([]ManagedContainer, error) { return nil, nil }, fixedNow)
	evidence, err := verifier.VerifyCleanup("port.30000", reservation.NewUserIdentity("system.reservation-governor"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evidence.ReconcilerID() != "system.reservation-governor" {
		t.Fatalf("evidence must carry the reconciler principal, got %q", evidence.ReconcilerID())
	}
	if evidence.OperationID() == "" {
		t.Fatal("evidence must carry a non-empty operation ID")
	}
}

// GPU classification (gpu.nvidia.<uuid>) depends on the live inventory, so
// it is exercised indirectly: on a GPU-less host the UUID is unmappable and
// must fail closed. This is asserted via the unknown-resource path above; a
// full GPU mapping test would need a collector seam, which the production
// code deliberately does not have (the collectors ARE the seam).
func TestDockerCleanupVerifier_GPUCanonicalShapeFailsClosedWithoutGPUs(t *testing.T) {
	verifier := newCleanupVerifierForTests(func(ctx context.Context) ([]ManagedContainer, error) { return nil, nil }, fixedNow)
	evidence, err := verifier.VerifyCleanup("gpu.nvidia.GPU-00000000-0000-0000-0000-000000000000", reservation.NewUserIdentity("r"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evidence.Verified() {
		t.Fatal("an unmappable GPU UUID must fail closed (verified=false) — sandbox has no nvidia-smi, nothing can be proven free")
	}
}
