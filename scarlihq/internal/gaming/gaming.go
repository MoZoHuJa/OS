// Package gaming defines the typed read-only contract for the ScarLiXoS
// gaming-stack inventory. It is a pure data contract: no detection, no
// command execution, no host access happens in this package.
//
// v19.3.1 (Gaming Stack Integration Plan, Phase 4): the authoritative
// detection runs host-side in scarlix-host-bridge (pacman database,
// systemctl, docker ps output, optional flatpak list) and is published in
// host-status.json. ScarliHQ only reads and serves it — it must never become
// a second source of truth, and it must never gain launch or install powers.
package gaming

import "encoding/json"

// Kind classifies what a gaming-stack entry is. Values are part of the
// /api/gaming contract and must not be renamed without a contract version
// bump.
type Kind string

const (
	KindLauncher      Kind = "launcher"      // Steam, Lutris, Heroic, Bottles, itch, RetroArch
	KindCompatibility Kind = "compatibility" // Wine, Wine Gecko/Mono, Vulkan loaders
	KindPerformance   Kind = "performance"   // Gamescope, MangoHud, GameMode
	KindStreaming     Kind = "streaming"     // native Sunshine, sunshine-tv
	KindServer        Kind = "server"        // Jellyfin, Minecraft (not launchers)
)

// Source records which channel an entry is managed through.
type Source string

const (
	SourcePacman  Source = "pacman"  // official Arch repositories
	SourceAUR     Source = "aur"     // AUR, installed via the non-root yay flow
	SourceFlatpak Source = "flatpak" // optional alternative channel (never default)
	SourceSystemd Source = "systemd" // host systemd unit (e.g. native Sunshine)
	SourceCompose Source = "compose" // docker compose service (e.g. sunshine-tv)
)

// Health is the honest lifecycle state of an entry. "unknown" is a valid,
// expected value — the dashboard must never guess.
type Health string

const (
	HealthNotInstalled  Health = "not_installed"  // not present on the host
	HealthNotApplicable Health = "not_applicable" // libraries/assets have no runtime
	HealthStopped       Health = "stopped"        // installed but not running
	HealthRunning       Health = "running"        // installed and running
	HealthUnhealthy     Health = "unhealthy"      // running but reporting failure
	HealthUnknown       Health = "unknown"        // detection unavailable
)

// ValidKind reports whether k is a known Kind.
func ValidKind(k Kind) bool {
	switch k {
	case KindLauncher, KindCompatibility, KindPerformance, KindStreaming, KindServer:
		return true
	}
	return false
}

// ValidSource reports whether s is a known Source.
func ValidSource(s Source) bool {
	switch s {
	case SourcePacman, SourceAUR, SourceFlatpak, SourceSystemd, SourceCompose:
		return true
	}
	return false
}

// ValidHealth reports whether h is a known Health.
func ValidHealth(h Health) bool {
	switch h {
	case HealthNotInstalled, HealthNotApplicable, HealthStopped,
		HealthRunning, HealthUnhealthy, HealthUnknown:
		return true
	}
	return false
}

// Entry is one gaming-stack inventory row as published by the host bridge.
// A launcher being installed never implies any game is installed.
type Entry struct {
	ID          string `json:"id"`                // package name / unit / compose service
	Name        string `json:"name"`              // human-readable display name
	Kind        Kind   `json:"kind"`              // launcher|compatibility|performance|streaming|server
	Source      Source `json:"source"`            // pacman|aur|flatpak|systemd|compose
	Installed   bool   `json:"installed"`         // detected on the host
	Version     string `json:"version,omitempty"` // pacman/flatpak version when installed
	Running     bool   `json:"running"`           // services/containers only
	Health      Health `json:"health"`            // honest lifecycle state
	Optional    bool   `json:"optional"`          // true unless part of the base gaming-minimal set
	LastChecked string `json:"last_checked"`      // ISO timestamp of the host-side detection
}

// MarshalJSONEntries guarantees the wire shape stays a JSON array even when
// the slice is empty, so /api/gaming clients never need a null branch.
func MarshalJSONEntries(entries []Entry) ([]byte, error) {
	if entries == nil {
		return json.Marshal([]Entry{})
	}
	return json.Marshal(entries)
}
