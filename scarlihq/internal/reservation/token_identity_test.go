package reservation

import (
	"errors"
	"testing"
)

func TestTokenIdentityProvider_ResolvesUserAndReconciler(t *testing.T) {
	provider, err := NewTokenIdentityProvider(
		map[string]string{"user-secret": "agent.coder"},
		map[string]string{"reconciler-secret": "system.reconciler"},
	)
	if err != nil {
		t.Fatalf("NewTokenIdentityProvider: %v", err)
	}
	user, err := provider.ResolveIdentity("user-secret")
	if err != nil || user.Principal() != "agent.coder" || user.IsReconciler() {
		t.Fatalf("unexpected user identity: %+v err=%v", user, err)
	}
	reconciler, err := provider.ResolveIdentity("reconciler-secret")
	if err != nil || reconciler.Principal() != "system.reconciler" || !reconciler.IsReconciler() {
		t.Fatalf("unexpected reconciler identity: %+v err=%v", reconciler, err)
	}
}

func TestTokenIdentityProvider_RejectsUnknownAndDuplicateTokens(t *testing.T) {
	provider, err := NewTokenIdentityProvider(map[string]string{"known": "agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ResolveIdentity("unknown-secret"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown token error=%v, want ErrUnauthenticated", err)
	}
	if _, err := provider.ResolveIdentity(""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("empty token error=%v, want ErrUnauthenticated", err)
	}
	if _, err := NewTokenIdentityProvider(map[string]string{"same": "agent"}, map[string]string{"same": "reconciler"}); err == nil {
		t.Fatal("duplicate token across roles must be rejected")
	}
}

func TestTokenIdentityProvider_DoesNotRetainRawTokens(t *testing.T) {
	provider, err := NewTokenIdentityProvider(map[string]string{"secret-do-not-store": "agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.entries) != 1 {
		t.Fatalf("entries=%d, want 1", len(provider.entries))
	}
	if provider.entries[0].digest == [32]byte{} {
		t.Fatal("token digest was not stored")
	}
}
