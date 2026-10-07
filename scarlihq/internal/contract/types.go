// Package contract defines the Resource Contract v1 — the canonical schema
// for compute resource requests that agents submit to the scheduler.
//
// This is the REQUEST side of the resource model. The RESPONSE side
// (allocations, granted GPU IDs, port assignments, etc.) comes in v19.2.x
// (Compute Fabric). For v19.1.0 the contract is defined + parseable +
// serializable, but NOT yet enforced by a scheduler.
//
// Relationship to package inventory (v19.0.7–v19.0.9):
//   - inventory = STATE  (what the system HAS  — read-only, collected from
//     nvidia-smi / docker ps / models.yaml / health probes).
//   - contract = REQUEST (what the agent WANTS — declared by the agent).
//
// The future scheduler (v19.2.x Compute Fabric) will match contract
// requests against inventory state and emit allocation responses.
//
// STABILITY CONTRACT (frozen in v19.1.0):
//   - Field names in JSON + YAML tags are FROZEN. Future versions may ADD
//     fields (with omitempty for optional ones) but MUST NOT rename or
//     remove existing ones without a v2 migration.
//   - Enum value sets (TaskType*, Priority*, Accelerator*, *Scope*) may be
//     EXTENDED additively. Consumers MUST tolerate unknown enum values
//     (forward compat — same rule as inventory package).
//   - All integer fields use int (mirrors inventory conventions).
//   - All `[]string` slices must serialize as `[]` when empty, never null.
//     Producers must initialize non-nil slices.
package contract

// ContractVersion is the schema version. v1 is frozen in v19.1.0.
type ContractVersion string

const (
	// VersionV1 is the Resource Contract schema version introduced in
	// SCARLIX OS v19.1.0. Frozen.
	VersionV1 ContractVersion = "v1"
)

// ResourceContract is the top-level request an agent submits to obtain
// compute resources for a task.
//
// JSON shape:
//
//	{
//	  "version": "v1",
//	  "id": "550e8400-...",
//	  "agent_id": "agent.coder",
//	  "task":     { "type": "coding", "priority": "interactive" },
//	  "compute":  { "accelerator": "cuda", "vram_mb": 12000, ... },
//	  "runtime":  { "preferred": ["sglang","vllm"] },
//	  "model":    { "capabilities": ["coding","reasoning"] },
//	  "security": { "filesystem": "workspace", "network": "restricted", "shell": "sandbox" }
//	}
type ResourceContract struct {
	Version  ContractVersion `json:"version"  yaml:"version"`  // "v1"
	ID       string          `json:"id"       yaml:"id"`       // UUID
	AgentID  string          `json:"agent_id" yaml:"agent_id"` // e.g. "agent.coder"
	Task     TaskSpec        `json:"task"     yaml:"task"`
	Compute  ComputeSpec     `json:"compute"  yaml:"compute"`
	Runtime  RuntimeSpec     `json:"runtime"   yaml:"runtime"`
	Model    ModelSpec       `json:"model"     yaml:"model"`
	Security SecuritySpec    `json:"security"  yaml:"security"`
}

// TaskSpec describes what the agent wants to do.
//
// Type is one of the TaskType* constants (extensible). Priority is one of
// the Priority* constants (extensible). The scheduler (v19.2.x) uses
// Priority to order pending contracts when GPU/RAM is contended.
type TaskSpec struct {
	Type     string `json:"type"     yaml:"type"`     // TaskTypeCoding, TaskTypeChat, ...
	Priority string `json:"priority" yaml:"priority"` // PriorityRealtime, PriorityInteractive, ...
}

// ComputeSpec describes the hardware resources requested.
//
// Accelerator is one of the Accelerator* constants (extensible). VRAMMB,
// CPUCores, RAMMB are non-negative integers. Zero is a valid request
// (means "do not reserve this resource").
type ComputeSpec struct {
	Accelerator string `json:"accelerator" yaml:"accelerator"` // AcceleratorCUDA, AcceleratorCPU, AcceleratorROCM
	VRAMMB      int    `json:"vram_mb"      yaml:"vram_mb"`    // requested VRAM in MiB
	CPUCores    int    `json:"cpu_cores"    yaml:"cpu_cores"`  // requested CPU cores
	RAMMB       int    `json:"ram_mb"       yaml:"ram_mb"`     // requested RAM in MiB
}

// RuntimeSpec describes which inference runtimes the agent prefers.
//
// Preferred is an ordered list of runtime IDs (e.g. "sglang", "vllm",
// "beellama", "ollama", "litellm", "smg"). The scheduler tries them in
// order. Empty list means "no preference". Must serialize as [] not null.
type RuntimeSpec struct {
	Preferred []string `json:"preferred" yaml:"preferred"`
}

// ModelSpec describes what model capabilities the task needs.
//
// Capabilities is an unordered list of capability tags (e.g. "coding",
// "reasoning", "chat", "embed", "vision", "tools"). The scheduler matches
// these against inventory.Model.Capabilities. Must serialize as [] not null.
type ModelSpec struct {
	Capabilities []string `json:"capabilities" yaml:"capabilities"`
}

// SecuritySpec describes the security scope the agent needs.
//
// Filesystem is one of FilesystemScope* (extensible). Network is one of
// NetworkScope* (extensible). Shell is one of ShellScope* (extensible).
// The scheduler (v19.2.x) and the guard package enforce these at runtime.
type SecuritySpec struct {
	Filesystem string `json:"filesystem" yaml:"filesystem"` // FilesystemScopeWorkspace, FilesystemScopeNone
	Network    string `json:"network"    yaml:"network"`    // NetworkScopeRestricted, NetworkScopeNone
	Shell      string `json:"shell"      yaml:"shell"`      // ShellScopeSandbox, ShellScopeNone
}

// ---------------------------------------------------------------------------
// Enum value constants (extensible — consumers MUST tolerate unknown values).
// ---------------------------------------------------------------------------

// Task types. The scheduler matches these against the task handler
// registered for the agent. Unknown values are tolerated for forward
// compatibility (e.g. a future "vision" task type added in v19.3.x).
const (
	TaskTypeCoding    = "coding"
	TaskTypeChat      = "chat"
	TaskTypeResearch  = "research"
	TaskTypeEmbedding = "embedding"
	TaskTypeIndexing  = "indexing"
)

// Priorities. The scheduler uses these to order pending contracts when
// GPU/RAM is contended. Lower textual priority = higher scheduling weight:
// "realtime" preempts "interactive", "interactive" preempts "normal",
// etc. Unknown values are tolerated.
const (
	PriorityRealtime    = "realtime"
	PriorityInteractive = "interactive"
	PriorityNormal      = "normal"
	PriorityBackground  = "background"
	PriorityBatch       = "batch"
)

// Accelerators. The scheduler matches these against inventory.GPU.Vendor
// (nvidia → cuda, amd → rocm, intel → cpu/intel). "cpu" means "no GPU
// requested". Unknown values are tolerated.
const (
	AcceleratorCUDA = "cuda"
	AcceleratorCPU  = "cpu"
	AcceleratorROCM = "rocm"
)

// Filesystem scopes. "workspace" = read/write to the agent's workspace
// directory only. "none" = no filesystem access. Unknown values tolerated.
const (
	FilesystemScopeWorkspace = "workspace"
	FilesystemScopeNone      = "none"
)

// Network scopes. "restricted" = outbound only to whitelisted hosts
// (litellm gateway, package mirrors, etc.). "none" = fully offline.
// Unknown values tolerated.
const (
	NetworkScopeRestricted = "restricted"
	NetworkScopeNone       = "none"
)

// Shell scopes. "sandbox" = shell available inside the agent's sandbox
// (bwrap/firejail). "none" = no shell access. Unknown values tolerated.
const (
	ShellScopeSandbox = "sandbox"
	ShellScopeNone    = "none"
)
