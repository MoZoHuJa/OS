// Package inventory defines normalized data structures for system observability.
//
// These are read-only representations of GPU, runtime, model, service, and health
// state. They do NOT control anything — they are for discovery and monitoring.
//
// v19.0.7 — introduced as preparation for the ScarliHQ resource view (v19.1.6)
// and the compute fabric (v19.2.x). The bash `scarlix` CLI (built in parallel
// for v19.0.7) must emit JSON that matches these struct tags exactly. See
// docs/SCARLIX_DATA_CONTRACTS.md for the canonical schema.
//
// This package complements (does NOT replace) the existing internal/status
// package, which reads /var/lib/scarlix/host-status.json (the untrusted-host
// → dashboard bridge written by scarlix-host-bridge). The inventory package
// is the higher-level, normalized representation that downstream code will
// compose from multiple sources: host-status.json, nvidia-smi, docker ps,
// models.yaml, and direct healthcheck probes.
//
// STABILITY: Field names in this file are FROZEN in v19.0.7. Future versions
// may ADD fields but MUST NOT rename or remove existing ones without a v2
// migration. New optional fields should use `omitempty` and a meaningful name.
//
// (go:generate hint is OPTIONAL — left as a comment for future enum generation
//
//	if Vendor / Protocol / Status values ever need type-safe enumeration.)
//
//go:generate go run github.com/alvaroloes/enumer -type=Vendor -transform=snake -output=vendor_enumer.go
package inventory

// GPU represents the normalized state of a single GPU.
// (ScaRgeN master guide section 8 — GPU discovery)
type GPU struct {
	ID             string  `json:"id"`     // e.g. "gpu.nvidia.0"
	Index          int     `json:"index"`  // 0, 1, ...
	Vendor         string  `json:"vendor"` // "nvidia", "amd", "intel"
	Name           string  `json:"name"`   // "RTX 5060 Ti"
	VRAMTotalMB    int     `json:"vram_total_mb"`
	VRAMUsedMB     int     `json:"vram_used_mb"`
	VRAMFreeMB     int     `json:"vram_free_mb"`
	UtilizationPct int     `json:"utilization_percent"`
	TemperatureC   int     `json:"temperature_c"`
	PowerW         float64 `json:"power_w"`
	Driver         string  `json:"driver"`      // "570. ..."
	CUDA           string  `json:"cuda"`        // "12.8"
	ComputeCap     string  `json:"compute_cap"` // "12.0" (sm_120)
	Healthy        bool    `json:"healthy"`
}

// Runtime represents the normalized state of an inference runtime.
// (ScaRgeN master guide section 12 — runtime discovery)
//
// ID values currently used in ScarLiX v19: "sglang", "vllm", "beellama",
// "ollama", "litellm", "smg". Protocol is one of: "openai-compatible",
// "ollama", "litellm-gateway", "smg-gateway".
type Runtime struct {
	ID           string   `json:"id"`           // "sglang", "vllm", "beellama", "ollama", "litellm"
	Version      string   `json:"version"`      // image tag or version
	Enabled      bool     `json:"enabled"`      // configured to run?
	Running      bool     `json:"running"`      // container actually up?
	Healthy      bool     `json:"healthy"`      // healthcheck passing?
	Protocol     string   `json:"protocol"`     // "openai-compatible", "ollama"
	Port         int      `json:"port"`         // 30000, 8089, 11438, 11435, 4001
	GPUIDs       []string `json:"gpu_ids"`      // which GPUs assigned
	Image        string   `json:"image"`        // full image:tag
	Capabilities []string `json:"capabilities"` // "chat", "embed", "vision", "tools"
}

// Model represents the normalized metadata of a model.
// (ScaRgeN master guide section 9 — model catalog)
//
// Path is the on-disk location (e.g. "/models/Qwen3-14B-AWQ" for safetensors
// or "/models/Qwen3-14B-Q4_K_M.gguf" for GGUF). EstimatedVRAM is the
// approximate VRAM footprint at the model's quantization + default context.
type Model struct {
	ID                string   `json:"id"`             // "qwen3-14b-awq"
	Path              string   `json:"path"`           // "/models/Qwen3-14B-AWQ"
	Format            string   `json:"format"`         // "safetensors", "gguf"
	Quantization      string   `json:"quantization"`   // "awq", "q4_k_m", "fp16"
	Parameters        string   `json:"parameters"`     // "14B"
	ContextLength     int      `json:"context_length"` // 32768
	EstimatedVRAM     int      `json:"estimated_vram_mb"`
	Capabilities      []string `json:"capabilities"`       // "coding", "reasoning", "chat"
	SupportedRuntimes []string `json:"supported_runtimes"` // "sglang", "vllm", "llamacpp"
	Present           bool     `json:"present"`            // file actually exists on disk?
}

// Service represents a ScarLiX service (container or systemd unit).
//
// Type is one of: "docker" (managed via docker compose), "systemd"
// (managed via systemctl), "binary" (a long-running process not under
// docker or systemd). Status is one of: "running", "stopped", "failed",
// "unknown".
type Service struct {
	ID          string `json:"id"` // "scarlihq", "sglang", "scarlix-host-bridge"
	Name        string `json:"name"`
	Type        string `json:"type"`   // "docker", "systemd", "binary"
	Status      string `json:"status"` // "running", "stopped", "failed", "unknown"
	Port        int    `json:"port,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
	Uptime      string `json:"uptime,omitempty"`
}

// Health represents the health summary of a single component.
//
// Component is the same identifier used elsewhere (e.g. Runtime.ID or
// "gpu.0"). State is one of: "healthy", "unhealthy", "starting", "down",
// "unknown". CheckedAt is ISO 8601 (RFC 3339) UTC.
type Health struct {
	Component string `json:"component"` // "sglang", "scarlihq", "gpu.0"
	State     string `json:"state"`     // "healthy", "unhealthy", "starting", "down", "unknown"
	Message   string `json:"message,omitempty"`
	CheckedAt string `json:"checked_at"` // ISO 8601 timestamp
}

// SystemStatus is the top-level normalized system snapshot.
//
// This is the root object the `scarlix` CLI emits with `--json` and what
// the ScarliHQ resource view will surface at GET /api/inventory (planned
// v19.1.6). All nested slices are non-nil in emitted JSON (use empty
// slices, not nil, when no entries exist — see types_test.go).
type SystemStatus struct {
	Version       string    `json:"version"` // SCARLIX OS version (e.g. "19.0.7")
	Mode          string    `json:"mode"`    // current scarlix-mode (ai, turbo, creative, ...)
	UptimeSeconds int64     `json:"uptime_seconds"`
	Timestamp     string    `json:"timestamp"` // ISO 8601
	GPUs          []GPU     `json:"gpus"`
	Runtimes      []Runtime `json:"runtimes"`
	Models        []Model   `json:"models"`
	Services      []Service `json:"services"`
	Health        []Health  `json:"health"`
}
