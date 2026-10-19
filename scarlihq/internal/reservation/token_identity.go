package reservation

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"strings"
)

// tokenIdentityEntry stores only a token digest, never the bearer token.
type tokenIdentityEntry struct {
	digest   [32]byte
	identity Identity
}

// TokenIdentityProvider resolves configured bearer tokens to trusted
// principals. Configure tokens from a secret manager/environment at startup;
// raw token values are hashed immediately and are not retained or logged.
type TokenIdentityProvider struct {
	entries []tokenIdentityEntry
}

// NewTokenIdentityProvider builds a fail-closed identity provider.
// userTokens maps raw bearer token -> principal; reconcilerTokens maps raw
// bearer token -> trusted reconciler principal. A token cannot have both roles.
func NewTokenIdentityProvider(userTokens, reconcilerTokens map[string]string) (*TokenIdentityProvider, error) {
	provider := &TokenIdentityProvider{entries: make([]tokenIdentityEntry, 0, len(userTokens)+len(reconcilerTokens))}
	seen := make(map[[32]byte]bool, len(userTokens)+len(reconcilerTokens))
	add := func(token, principal string, reconciler bool) error {
		if strings.TrimSpace(token) == "" || strings.TrimSpace(principal) == "" {
			return fmt.Errorf("reservation: token and principal must be non-empty")
		}
		digest := sha256.Sum256([]byte(token))
		if seen[digest] {
			return fmt.Errorf("reservation: a bearer token is configured more than once")
		}
		seen[digest] = true
		identity := NewUserIdentity(principal)
		if reconciler {
			identity = newReconcilerIdentity(principal)
		}
		provider.entries = append(provider.entries, tokenIdentityEntry{digest: digest, identity: identity})
		return nil
	}
	for token, principal := range userTokens {
		if err := add(token, principal, false); err != nil {
			return nil, err
		}
	}
	for token, principal := range reconcilerTokens {
		if err := add(token, principal, true); err != nil {
			return nil, err
		}
	}
	if len(provider.entries) == 0 {
		return nil, fmt.Errorf("reservation: at least one trusted token must be configured")
	}
	return provider, nil
}

// ResolveIdentity uses constant-time digest comparisons and returns a generic
// authentication error. It never includes the supplied token in error text.
func (p *TokenIdentityProvider) ResolveIdentity(token string) (Identity, error) {
	if p == nil || strings.TrimSpace(token) == "" {
		return Identity{}, ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(token))
	var found *Identity
	for i := range p.entries {
		if subtle.ConstantTimeCompare(digest[:], p.entries[i].digest[:]) == 1 {
			found = &p.entries[i].identity
		}
	}
	if found == nil {
		return Identity{}, ErrUnauthenticated
	}
	return *found, nil
}

var _ TrustedIdentityProvider = (*TokenIdentityProvider)(nil)
