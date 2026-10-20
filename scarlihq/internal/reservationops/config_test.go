package reservationops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a governor config fixture with controlled permissions.
// The umask-independence lesson from v19.3.1 hostbridge tests applies: chmod
// explicitly, never rely on ambient umask — including the PARENT directory
// (sandbox umask 0002 makes t.TempDir() group-writable, which the trust
// check must rightly reject, so tests tighten it to 0700 first).
func writeConfig(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("tighten temp dir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod config fixture: %v", err)
	}
	return path
}

const validConfigJSON = `{
  "reconciler_token": "0123456789abcdef0123456789abcdef0123456789abcdef",
  "user_tokens": {},
  "managed_ports": [30000, 8089, 11434]
}`

func TestLoadGovernorConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "reservation.json", validConfigJSON, 0o640)

	cfg, err := LoadGovernorConfig(path)
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.ReconcilerToken == "" || len(cfg.ManagedPorts) != 3 || cfg.ManagedPorts[0] != 30000 {
		t.Fatalf("unexpected config content: %+v", cfg)
	}
}

func TestLoadGovernorConfig_RejectsWorldReadable(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "reservation.json", validConfigJSON, 0o644)

	if _, err := LoadGovernorConfig(path); err == nil {
		t.Fatal("world-readable config with tokens must be rejected")
	}
}

func TestLoadGovernorConfig_RejectsGroupWritable(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "reservation.json", validConfigJSON, 0o660)

	if _, err := LoadGovernorConfig(path); err == nil {
		t.Fatal("group-writable config must be rejected")
	}
}

func TestLoadGovernorConfig_RejectsRelativePath(t *testing.T) {
	if _, err := LoadGovernorConfig("reservation.json"); err == nil {
		t.Fatal("relative path must be rejected")
	}
}

func TestLoadGovernorConfig_RejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	bad := strings.Replace(validConfigJSON, `"managed_ports"`, `"managed_portz"`, 1)
	path := writeConfig(t, dir, "reservation.json", bad, 0o640)

	if _, err := LoadGovernorConfig(path); err == nil {
		t.Fatal("unknown fields must be rejected (strict decode)")
	}
}

func TestLoadGovernorConfig_RejectsShortReconcilerToken(t *testing.T) {
	dir := t.TempDir()
	bad := strings.Replace(validConfigJSON,
		"0123456789abcdef0123456789abcdef0123456789abcdef", "short", 1)
	path := writeConfig(t, dir, "reservation.json", bad, 0o640)

	if _, err := LoadGovernorConfig(path); err == nil {
		t.Fatal("short reconciler token must be rejected")
	}
}

func TestLoadGovernorConfig_RejectsReconcilerPrincipalClaim(t *testing.T) {
	dir := t.TempDir()
	bad := `{
  "reconciler_token": "0123456789abcdef0123456789abcdef0123456789abcdef",
  "user_tokens": {"0123456789abcdef0123": "system.reservation-governor"},
  "managed_ports": []
}`
	path := writeConfig(t, dir, "reservation.json", bad, 0o640)

	if _, err := LoadGovernorConfig(path); err == nil {
		t.Fatal("a user token claiming the reconciler principal must be rejected")
	}
}

func TestLoadGovernorConfig_RejectsDuplicatePrincipal(t *testing.T) {
	dir := t.TempDir()
	bad := `{
  "reconciler_token": "0123456789abcdef0123456789abcdef0123456789abcdef",
  "user_tokens": {
    "0123456789abcdef0123": "scarlihq.compute",
    "0123456789abcdef4567": "scarlihq.compute"
  },
  "managed_ports": []
}`
	path := writeConfig(t, dir, "reservation.json", bad, 0o640)

	if _, err := LoadGovernorConfig(path); err == nil {
		t.Fatal("two tokens mapping to the same principal must be rejected")
	}
}

func TestLoadGovernorConfig_RejectsUserTokenEqualToReconcilerToken(t *testing.T) {
	dir := t.TempDir()
	bad := `{
  "reconciler_token": "0123456789abcdef0123456789abcdef0123456789abcdef",
  "user_tokens": {"0123456789abcdef0123456789abcdef0123456789abcdef": "some.principal"},
  "managed_ports": []
}`
	path := writeConfig(t, dir, "reservation.json", bad, 0o640)

	if _, err := LoadGovernorConfig(path); err == nil {
		t.Fatal("user token equal to the reconciler token must be rejected")
	}
}

func TestGovernorConfigValidate_Ports(t *testing.T) {
	cases := []struct {
		name  string
		ports []int
		ok    bool
	}{
		{"empty ok", nil, true},
		{"valid", []int{1, 65535}, true},
		{"zero rejected", []int{0}, false},
		{"too high rejected", []int{65536}, false},
		{"negative rejected", []int{-1}, false},
		{"duplicate rejected", []int{4000, 4000}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := GovernorConfig{
				ReconcilerToken: "0123456789abcdef0123456789abcdef0123456789abcdef",
				ManagedPorts:    tc.ports,
			}
			err := cfg.validate()
			if tc.ok && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
