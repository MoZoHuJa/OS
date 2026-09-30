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
        "time"
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
        Stale          bool            `json:"stale,omitempty"` // v18.3: true if file missing/corrupt/old
        Error          string          `json:"error,omitempty"` // v18.5 P1: present when status generation failed
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

// v18.8.2 P2: testStatusFile overrides statusFile for unit tests (empty = use default).
var testStatusFile string

// getStatusFile returns testStatusFile if set, else the production const.
func getStatusFile() string {
        if testStatusFile != "" {
                return testStatusFile
        }
        return statusFile
}

// Read loads host-status.json. Returns zero-value HostStatus + nil error on success.
// v18.3 P1: Returns error on corrupt JSON (was: silent `_ = json.Unmarshal` → API got zero values).
// API handlers can now report "stale" or "corrupt" status to dashboard.
func Read() (HostStatus, error) {
        var s HostStatus
        data, err := os.ReadFile(getStatusFile())
        if err != nil {
                return s, err // file missing — caller checks os.IsNotExist
        }
        if err := json.Unmarshal(data, &s); err != nil {
                // v18.3: return empty struct + error so API can report corruption
                s.Stale = true
                s.Timestamp = "" // signal "not valid"
                return s, err
        }
        // v18.3: mark stale if timestamp is empty or > 60s old
        if s.Timestamp == "" {
                s.Stale = true
        }
        // v18.5 P1: Real stale check (was: only checked empty timestamp, not age)
        if s.Timestamp != "" {
                if t, err := time.Parse(time.RFC3339, s.Timestamp); err == nil {
                        if time.Since(t) > 60*time.Second {
                                s.Stale = true
                        }
                        // v18.5: also reject future timestamps (clock skew protection)
                        if t.After(time.Now().Add(30 * time.Second)) {
                                s.Stale = true
                        }
                } else {
                        // Can't parse timestamp → mark stale
                        s.Stale = true
                }
        }
        return s, nil
}

// ReadOrStale is a convenience wrapper that never fails — returns Stale=true on error.
// Use this in handlers where you want to return SOMETHING to the dashboard.
func ReadOrStale() HostStatus {
        s, err := Read()
        if err != nil {
                s.Stale = true
        }
        return s
}
