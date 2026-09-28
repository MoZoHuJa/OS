// Package status provides shared types + reader for /var/lib/scarlix/host-status.json
// (written by scarlix-host-bridge systemd timer on the host).
//
// v17.9.8: ScarliHQ container reads this file (read-only mount) instead of running
// nvidia-smi / docker / scarlix-mode inside the container. This is the privilege
// boundary between the (untrusted, LAN-facing) dashboard and the (root) host.
package status

import (
	"encoding/json"
	"os"
)

// HostStatus is the JSON shape written by scarlix-host-bridge every 5s.
type HostStatus struct {
	Timestamp      string      `json:"timestamp"`
	Version        string      `json:"version"`
	ScarlixVersion string      `json:"scarlix_version"`
	Mode           string      `json:"mode"`
	Experimental   bool        `json:"experimental"`
	ModeApplied    string      `json:"mode_applied"`
	GPUs           []GPU       `json:"gpus"`
	Containers     []Container `json:"containers"`
	Disk           Disk        `json:"disk"`
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

// statusFile is the path scarlix-host-bridge writes to (mounted read-only in ScarliHQ).
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
