// Package reservationops provides the production host-side providers that
// the reservation domain requires but must not implement itself: a trusted
// token identity provider backed by an administrator-owned config file, a
// live inventory provider composing the read-only inventory collectors, and
// a docker-backed cleanup verifier producing typed cleanup evidence.
//
// v19.3.2 — F-04 (production reservation composition root). These types are
// consumed by cmd/scarlix-reservation (the reservation governor service).
// The reservation domain package stays free of docker/nvidia-smi/OS concerns;
// everything host-specific is injected here, at the composition boundary.
package reservationops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// GovernorConfig is the administrator-owned configuration of the reservation
// governor. It lives at /etc/scarlix/reservation.json (root-owned, mode 0640
// root:65532 in production — see reservation.json.example) and is the ONLY
// source of reservation trust material:
//
//   - ReconcilerToken authenticates the governor itself for Recover() and the
//     periodic lease reaper. It is a separate secret from SCARLIHQ_TOKEN by
//     design (KPK precedent: the reconciler is a separately-trusted identity,
//     never the shared dashboard token).
//   - UserTokens maps reserve-capable tokens to principal names. May be empty
//     until the compute intake API exists (nothing can call Reserve yet);
//     an empty map means "no user principals trusted", which is fail-closed.
//   - ManagedPorts are the host ports the reservation engine may own. Ports
//     are only reservable when explicitly supplied by this trusted config
//     (mirrors the reservation inventory adapter contract).
type GovernorConfig struct {
	ReconcilerToken string            `json:"reconciler_token"`
	UserTokens      map[string]string `json:"user_tokens"`
	ManagedPorts    []int             `json:"managed_ports"`
}

// ReconcilerPrincipal is the fixed principal name the governor's own token
// resolves to. It identifies the reconciler identity in reservation records
// and cleanup evidence.
const ReconcilerPrincipal = "system.reservation-governor"

// LoadGovernorConfig reads and validates the governor config.
//
// Trust rules (fail-closed, mirroring hostbridge config policy but stricter
// because this file carries secrets):
//   - path must be absolute, a regular file, not a symlink
//   - the file must not be writable by group/other (mode&0022 == 0)
//   - the file must not be readable by other (mode&0004 == 0) — tokens live here
//   - the file (and its parent dir) must be owned by root or the current euid
//     (tests run unprivileged; production runs as 65532 reading a root:65532 file)
//   - the parent directory must not be group/world-writable
//   - JSON decoding is strict: unknown fields are rejected
//   - the reconciler token must be at least 32 chars (generated via
//     `openssl rand -hex 32` by the operator — see the example config)
//   - user token values (principals) must be non-empty and distinct
//   - managed ports must be within [1,65535] and unique
//   - user tokens must not collide with the reconciler token (the identity
//     provider rejects overlap — checked there too, but we fail earlier with
//     a config-shaped error)
func LoadGovernorConfig(path string) (GovernorConfig, error) {
	var cfg GovernorConfig
	if !filepath.IsAbs(path) {
		return cfg, fmt.Errorf("reservationops: config path must be absolute (got %q)", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return cfg, fmt.Errorf("reservationops: config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return cfg, fmt.Errorf("reservationops: config must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return cfg, fmt.Errorf("reservationops: config must not be writable by group/other (mode %04o)", info.Mode().Perm())
	}
	if info.Mode().Perm()&0o004 != 0 {
		return cfg, fmt.Errorf("reservationops: config is world-readable but contains tokens (use chmod 0640 root:65532)")
	}
	if err := ownedByTrusted(info, path); err != nil {
		return cfg, err
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.Mode().IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return cfg, fmt.Errorf("reservationops: config parent must be a real directory")
	}
	if parent.Mode().Perm()&0o022 != 0 {
		return cfg, fmt.Errorf("reservationops: config parent must not be group/world-writable")
	}
	if err := ownedByTrusted(parent, filepath.Dir(path)); err != nil {
		return cfg, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("reservationops: config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("reservationops: config is not valid GovernorConfig JSON: %w", err)
	}
	if dec.More() {
		return cfg, fmt.Errorf("reservationops: config must contain a single JSON object")
	}
	return cfg, cfg.validate()
}

func (cfg GovernorConfig) validate() error {
	if len(cfg.ReconcilerToken) < 32 {
		return fmt.Errorf("reservationops: reconciler_token must be at least 32 characters (generate with `openssl rand -hex 32`)")
	}
	principals := make(map[string]bool, len(cfg.UserTokens))
	for token, principal := range cfg.UserTokens {
		if len(token) < 16 {
			return fmt.Errorf("reservationops: user tokens must be at least 16 characters")
		}
		if token == cfg.ReconcilerToken {
			return fmt.Errorf("reservationops: a user token equals the reconciler token — reconciler trust must stay separate")
		}
		if principal == "" {
			return fmt.Errorf("reservationops: user token maps to an empty principal")
		}
		if principal == ReconcilerPrincipal {
			return fmt.Errorf("reservationops: user token must not claim the reconciler principal %q", ReconcilerPrincipal)
		}
		if principals[principal] {
			return fmt.Errorf("reservationops: principal %q is mapped by more than one token", principal)
		}
		principals[principal] = true
	}
	seen := make(map[int]bool, len(cfg.ManagedPorts))
	for _, port := range cfg.ManagedPorts {
		if port < 1 || port > 65535 {
			return fmt.Errorf("reservationops: managed port %d outside [1,65535]", port)
		}
		if seen[port] {
			return fmt.Errorf("reservationops: managed port %d listed more than once", port)
		}
		seen[port] = true
	}
	return nil
}

// ownedByTrusted accepts root ownership or ownership by the current effective
// user. Production units run as UID 65532 with a root-owned 0640 root:65532
// config; tests and CI runners may run unprivileged with self-owned fixtures.
func ownedByTrusted(info os.FileInfo, label string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("reservationops: cannot stat %s owner", label)
	}
	if stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("reservationops: %s must be owned by root (or the running user)", label)
	}
	return nil
}
