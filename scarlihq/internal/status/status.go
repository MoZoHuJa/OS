// Package status provides shared types + reader for /var/lib/scarlix/host-status.json
// (written by scarlix-host-bridge systemd timer on the host).
//
// v18.0.0: ScarliHQ container reads this file (read-only mount) instead of running
// nvidia-smi / docker / scarlix-mode inside the container. This is the privilege
// boundary between the (untrusted, LAN-facing) dashboard and the (root) host.
package status

import (
	"encoding/json"
	"os"
)

// HostStatus is the JSON shape written by scarlix-host-bridge every 5s.
type HostStatus struct {
	Timestamp      string          `json:"timestamp"`
	Version        string          `json:"version"`
	ScarlixVersion string          `json:"scarlix_version"`
	Mode           string          `json:"mode"`
	Experimental   bool            `json:"experimental"`
	ModeTransition ModeTransition  `json:"mode_transition"`
	GPUs           []GPU           `json:"gpus"`
	Containers     []Container     `json:"containers"`
	Disk           Disk            `json:"disk"`
}

// ModeTransition holds the state of the most recent mode switch request.
// v18.0.0: full state machine (was: just "mode_applied" string in v17.9.8).
// GET /api/mode now returns this so dashboard can show retry/failed state.
type ModeTransition struct {
	Requested    string `json:"requested"`     // mode that was requested (e.g. "ai")
	State        string `json:"state"`        // none|applied|retrying|failed|rejected
	RetryCount   int    `json:"retry_count"`   // 0-3 (MAX_RETRIES)
	LastError    string `json:"last_error"`    // error message (truncated to 500 chars)
	LastTimestamp string `json:"last_timestamp"` // when the last transition was attempted
}

// GPU is one nvidia-smi entry.
type GPU struct {
	Index      int     `json:"index"`
	Name       string  `json:"name"`
	Temp       float64 `json:"temp"`
	Util       float64 `json:"util"`
	MemUsed    float64 `json:"mem_used"`
	MemTotal   float64 `json:"mem_total"`
	Power      float64 `json:"power"`
	ComputeCap string  `json:"compute_cap"`
}

// Container is one docker ps entry.
type Container struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Ports  string `json:"ports"`
}

// Disk holds /models filesystem stats (MB).
type Disk struct {
	ModelsFreeMB  int `json:"models_free_mb"`
	ModelsTotalMB int `json:"models_total_mb"`
}

const statusFile = "/var/lib/scarlix/host-status.json"

// Read loads host-status.json. Returns zero-value HostStatus if missing/unparseable.
func Read() HostStatus {
	var s HostStatus
	data, err := os.ReadFile(statusFile)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s)
	return s
}
