// Package inventory — Runtime discovery and health-check collector (v19.0.9).
//
// This file implements the runtime population of the Runtime struct defined in
// types.go (v19.0.7). It is the Go-side counterpart of the bash `scarlix runtime
// list` subcommand — both invoke `docker ps --filter name=<container>` with the
// same five-runtime table, but the Go collector emits the normalized
// `inventory.Runtime` JSON shape that downstream consumers (ScarliHQ resource
// view v19.1.6, future compute fabric v19.2.x, and `scarlix --json runtime
// list`) require.
//
// Scope rules (v19.0.9 task 3-b):
//
//   - READ-ONLY. No container start/stop, no env rebuild, no mode switch.
//   - Graceful degradation: if Docker is missing/hangs/returns garbage, the
//     collector returns an empty-but-non-nil []Runtime where every runtime
//     has Running=false, so callers can always JSON-marshal to `[]` safely
//     (stability contract §5: "all arrays must serialize as [], never null").
//   - Static runtime metadata (image, port, protocol, GPU assignment,
//     capabilities) is hard-coded from the compose files in ai/*/docker-
//     compose.yml — these are the config source of truth, not derived at
//     runtime. The dynamic part (Running, Healthy) is queried live.
//   - Health is populated by CollectRuntimeHealth via HTTP probes to each
//     runtime's healthcheck endpoint with a 2s timeout.
//
// Complements (does NOT replace) the existing internal/status package, which
// reads /var/lib/scarlix/host-status.json (the host-bridge → dashboard pipe).
// The status package is the lower-level read; inventory is the higher-level
// normalized view that v19.1.6+ will compose from multiple sources.
package inventory

import (
        "context"
        "fmt"
        "net/http"
        "os"
        "os/exec"
        "strings"
        "time"
)

// dockerTimeout bounds any `docker ps` invocation. 5s matches the v19.0.8
// nvidia-smi timeout — generous for a healthy Docker daemon, tight enough
// to surface a stuck dockerd without hanging the caller.
const dockerTimeout = 5 * time.Second

// healthTimeout bounds each runtime HTTP health probe. 2s matches the bash
// CLI's curl --max-time 2 (scarlix-mode health probe pattern).
const healthTimeout = 2 * time.Second

// runtimeMeta captures static per-runtime metadata extracted from the
// docker-compose.yml files in ai/{sglang,vllm,llamacpp,ollama,litellm}/.
// These are the config source of truth — image tags, ports, GPU assignments,
// and capability claims all come from the compose definitions, not from
// runtime probing. GPUIndex -1 means CPU-only (no NVIDIA_VISIBLE_DEVICES).
//
// Fields:
//   - ID           — Runtime.ID ("sglang", "vllm", "beellama", "ollama", "litellm")
//   - Container    — docker container_name (used for `docker ps --filter name=`)
//   - Image        — full image:tag (or @sha256 digest for pinned images)
//   - Protocol     — Runtime.Protocol ("openai-compatible", "ollama")
//   - Port         — host-side port (30000, 8089, 11438, 11435, 4001)
//   - GPUIndex     — NVIDIA_VISIBLE_DEVICES value (-1 = CPU)
//   - Capabilities — Runtime.Capabilities (chat/completion/tools/embed/gateway)
//   - HealthURL    — HTTP healthcheck endpoint (http://127.0.0.1:<port>/...)
type runtimeMeta struct {
        ID           string
        Container    string
        Image        string
        Protocol     string
        Port         int
        GPUIndex     int
        Capabilities []string
        HealthURL    string
}

// runtimeMetadata is the canonical runtime table for v19.0.x. Sourced from:
//   - ai/sglang/docker-compose.yml     (NVIDIA_VISIBLE_DEVICES=0,  :30000)
//   - ai/vllm/docker-compose.yml      (NVIDIA_VISIBLE_DEVICES=1,  :8089)
//   - ai/llamacpp/docker-compose.yml  (CPU, :11438, pinned by sha256)
//   - ai/ollama/docker-compose.yml    (CPU, :11435, container=ollama-agent)
//   - ai/litellm/docker-compose.yml   (CPU, :4001)
//
// HealthURL endpoints verified against each compose's `healthcheck:` block:
//   - sglang   → /health (port 30000)
//   - vllm     → /v1/models (port 8089; vLLM exposes /health and /v1/models;
//                /v1/models is the standard OpenAI-compatible probe)
//   - beellama → /health (port 11438 → container :8080)
//   - ollama   → /api/tags (port 11435; ollama healthcheck uses `ollama list`
//                internally but the HTTP endpoint is /api/tags)
//   - litellm  → /health/liveliness (port 4001; auth-free liveness probe —
//                /health requires master_key and would 401)
var runtimeMetadata = []runtimeMeta{
        {
                ID:           "sglang",
                Container:    "sglang",
                Image:        "lmsysorg/sglang:v0.4.9.post6-cu128-b200",
                Protocol:     "openai-compatible",
                Port:         30000,
                GPUIndex:     0,
                Capabilities: []string{"chat", "completion", "tools"},
                HealthURL:    "http://127.0.0.1:30000/health",
        },
        {
                ID:           "vllm",
                Container:    "vllm",
                Image:        "vllm/vllm-openai:v0.8.5",
                Protocol:     "openai-compatible",
                Port:         8089,
                GPUIndex:     1,
                Capabilities: []string{"chat", "completion"},
                HealthURL:    "http://127.0.0.1:8089/v1/models",
        },
        {
                ID:           "beellama",
                Container:    "beellama",
                Image:        "ghcr.io/ggml-org/llama.cpp@sha256:6d607629e3dd5e85f45c43d1494648126cb3f93f2122c9cd53f43242c94cde14",
                Protocol:     "openai-compatible",
                Port:         11438,
                GPUIndex:     -1,
                Capabilities: []string{"chat", "completion"},
                HealthURL:    "http://127.0.0.1:11438/health",
        },
        {
                ID:           "ollama",
                Container:    "ollama-agent",
                Image:        "ollama/ollama:0.5.4",
                Protocol:     "ollama",
                Port:         11435,
                GPUIndex:     -1,
                Capabilities: []string{"chat", "completion", "embed"},
                HealthURL:    "http://127.0.0.1:11435/api/tags",
        },
        {
                ID:           "litellm",
                Container:    "litellm",
                Image:        "ghcr.io/berriai/litellm:main-v1.21.7",
                Protocol:     "openai-compatible",
                Port:         4001,
                GPUIndex:     -1,
                Capabilities: []string{"chat", "completion", "gateway"},
                HealthURL:    "http://127.0.0.1:4001/health/liveliness",
        },
}

// CollectRuntimes queries Docker for inference runtime status + returns
// normalized Runtime structs. Returns empty []Runtime{} (never nil) —
// when Docker is unavailable every runtime has Running=false but the
// metadata (image, port, protocol) is still populated, so callers can
// always JSON-marshal to a non-null array of runtimes.
//
// Field population rules (see docs/SCARLIX_DATA_CONTRACTS.md §3):
//   - ID           = runtimeMeta.ID (stable identifier)
//   - Version      = the image tag (e.g. "v0.4.9.post6-cu128-b200" extracted
//                    from "lmsysorg/sglang:v0.4.9.post6-cu128-b200"). For
//                    digest-pinned images (beellama), Version="" since the
//                    digest is not a meaningful "version".
//   - Enabled      = true (all five runtimes are configured in compose files;
//                    whether they actually start depends on scarlix-mode).
//   - Running      = true iff `docker ps --filter name=<container>` returns
//                    a Status line starting with "Up".
//   - Healthy      = false (placeholder; CollectRuntimeHealth refines).
//   - Protocol     = runtimeMeta.Protocol.
//   - Port         = runtimeMeta.Port.
//   - GPUIDs       = ["gpu.nvidia.0"] for GPUIndex=0, ["gpu.nvidia.1"] for
//                    GPUIndex=1, [] for GPUIndex=-1 (CPU). Format matches
//                    GPU.ID so cross-refs work.
//   - Image        = runtimeMeta.Image (full image:tag).
//   - Capabilities = runtimeMeta.Capabilities (chat/completion/tools/embed/gateway).
func CollectRuntimes() []Runtime {
        out := make([]Runtime, 0, len(runtimeMetadata))
        for _, m := range runtimeMetadata {
                gpuIDs := make([]string, 0, 1)
                if m.GPUIndex >= 0 {
                        gpuIDs = append(gpuIDs, fmt.Sprintf("gpu.nvidia.%d", m.GPUIndex))
                }
                // Capabilities is a []string — always non-nil so JSON marshals as
                // [...] not null. Build a fresh copy so the caller can mutate freely.
                caps := make([]string, len(m.Capabilities))
                copy(caps, m.Capabilities)

                runningStatus, _ := dockerInspect(m.Container)
                running := runningStatus == "running"

                out = append(out, Runtime{
                        ID:           m.ID,
                        Version:      imageTag(m.Image),
                        Enabled:      true,
                        Running:      running,
                        Healthy:      false, // refined by CollectRuntimeHealth
                        Protocol:     m.Protocol,
                        Port:         m.Port,
                        GPUIDs:       gpuIDs,
                        Image:        m.Image,
                        Capabilities: caps,
                })
        }
        return out
}

// CollectRuntimeHealth probes each runtime's HTTP healthcheck endpoint
// with a 2s timeout and derives a Health entry for each. Mutates the
// input slice's `Healthy` field in place to mirror the derived state
// (same pattern as CollectGPUHealth in gpu.go).
//
// State mapping (see docs/SCARLIX_DATA_CONTRACTS.md §6):
//   - HTTP 200      → "healthy"
//   - conn refused  → "down"     (container not running / port closed)
//   - timeout       → "unhealthy" (container running but not responding)
//   - any other err → "down"     (DNS, malformed URL, etc.)
//
// Returns []Health (never nil) — one entry per input runtime, in the same
// order. Component identifier = Runtime.ID ("sglang", "vllm", ...).
// CheckedAt is RFC 3339 UTC.
//
// Mutation contract: the input slice's backing array IS modified in place
// (Go slice semantics — slice header passed by value, backing array shared).
// Callers that want to preserve the original Healthy values should pass a copy.
func CollectRuntimeHealth(runtimes []Runtime) []Health {
        healths := make([]Health, 0, len(runtimes))
        now := time.Now().UTC().Format(time.RFC3339)
        client := &http.Client{Timeout: healthTimeout}

        for i := range runtimes {
                var state, msg string
                // Look up the HealthURL for this runtime ID. If we don't find it
                // (shouldn't happen — runtimeMetadata is the source of truth —
                // but defensive), mark unknown.
                url := ""
                for _, m := range runtimeMetadata {
                        if m.ID == runtimes[i].ID {
                                url = m.HealthURL
                                break
                        }
                }
                if url == "" {
                        state = "unknown"
                        msg = "no healthcheck URL configured for runtime"
                } else {
                        state, msg = probeHealth(client, url)
                }

                healths = append(healths, Health{
                        Component: runtimes[i].ID,
                        State:     state,
                        Message:   msg,
                        CheckedAt: now,
                })
                // Mirror state back into the Runtime slice so callers using either
                // API see consistent Healthy values.
                runtimes[i].Healthy = (state == "healthy")
        }
        return healths
}

// dockerInspect runs `docker ps --filter name=<container> --format {{.Status}}`
// and returns true iff the output starts with "Up". Returns (false, nil) when:
//   - docker is not on PATH (no error — graceful degradation)
//   - docker exits non-zero (no error — graceful degradation)
//   - docker returns empty output (container doesn't exist)
//   - docker times out (5s context deadline)
//   - docker returns a Status that doesn't start with "Up" (e.g. "Exited",
//     "Restarting", "Created") → container exists but isn't running
//
// The error return is non-nil only for unexpected internal failures (currently
// never used by callers but kept on the signature for future expansion and to
// match the task spec's `dockerInspect(...) (status string, err error)` shape).
func dockerInspect(containerName string) (status string, err error) {
        binary, lookErr := exec.LookPath("docker")
        if lookErr != nil {
                // docker not installed — common in CI / dev sandboxes. Graceful
                // degradation: return "" so caller treats as "not running".
                return "", nil
        }

        ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
        defer cancel()

        cmd := exec.CommandContext(ctx, binary,
                "ps", "--filter", "name="+containerName,
                "--format", "{{.Status}}",
        )
        out, runErr := cmd.Output()
        if runErr != nil {
                // Covers: non-zero exit (dockerd broken), context deadline (hang),
                // signal. In all cases, "not running" is the safe answer.
                return "", nil
        }

        line := strings.TrimSpace(string(out))
        if line == "" {
                // No container matched the name filter — not running.
                return "", nil
        }
        if strings.HasPrefix(line, "Up") {
                return "running", nil
        }
        // "Exited", "Restarting", "Created", "Paused", "Removing", "Dead" —
        // the container exists but isn't serving traffic. Treat as not running.
        return "down", nil
}

// ----- internal helpers ----------------------------------------------------

// imageTag extracts the version tag from a Docker image reference.
//
// Examples:
//   - "lmsysorg/sglang:v0.4.9.post6-cu128-b200" → "v0.4.9.post6-cu128-b200"
//   - "vllm/vllm-openai:v0.8.5"                 → "v0.8.5"
//   - "ollama/ollama:0.5.4"                     → "0.5.4"
//   - "ghcr.io/berriai/litellm:main-v1.21.7"   → "main-v1.21.7"
//   - "ghcr.io/ggml-org/llama.cpp@sha256:..."   → "" (digest-pinned, no tag)
//   - "library/nginx:latest"                    → "latest"
//   - "library/nginx"                           → "" (no tag)
//
// Returns "" when the image has no tag (digest-pinned or untagged) so that
// callers can decide whether to display "unknown" or skip — the Runtime
// struct's Version field accepts an empty string per the data contract.
func imageTag(imageRef string) string {
        // Digest-pinned images (sha256:...) take precedence over any tag —
        // Docker does not allow both, so we check for @ first.
        if at := strings.Index(imageRef, "@"); at >= 0 {
                // Some digest-pinned images also have a tag before the @ (e.g.
                // "repo:tag@sha256:..."). Extract that tag if present.
                repo := imageRef[:at]
                if colon := strings.LastIndex(repo, ":"); colon >= 0 {
                        // Skip the registry-port colon if there's a slash after it.
                        // e.g. "ghcr.io:443/repo" — the last colon is the port, not tag.
                        // But "ghcr.io/repo:tag" — last colon is the tag separator.
                        // Heuristic: if the substring after the last colon contains a
                        // slash, it's a port-qualified registry, not a tag.
                        tag := repo[colon+1:]
                        if !strings.Contains(tag, "/") {
                                return tag
                        }
                }
                return ""
        }
        // No @ digest — look for the last : that's not part of a registry port.
        // Heuristic: a tag separator colon has no slash after it.
        if colon := strings.LastIndex(imageRef, ":"); colon >= 0 {
                tag := imageRef[colon+1:]
                if !strings.Contains(tag, "/") {
                        return tag
                }
        }
        return ""
}

// probeHealth issues a GET to url and maps the result to (state, message).
// 200 → ("healthy", "")
// connection refused → ("down", "connection refused")
// timeout → ("unhealthy", "healthcheck timeout")
// other error (DNS, malformed URL, non-200 status) → ("down", err.Error())
func probeHealth(client *http.Client, url string) (state, msg string) {
        resp, err := client.Get(url)
        if err != nil {
                // Distinguish timeout (a stuck runtime — "unhealthy") from
                // connection refused (nothing listening — "down"). Other errors
                // (DNS, malformed URL) lump into "down" since the runtime isn't
                // responding on its expected endpoint.
                if os.IsTimeout(err) {
                        return "unhealthy", "healthcheck timeout"
                }
                // http.Client errors wrap the underlying net.OpError — check for
                // "connection refused" substring (cross-platform-ish; works on Linux
                // + macOS which is all we support for now).
                errStr := err.Error()
                if strings.Contains(errStr, "connection refused") {
                        return "down", "connection refused"
                }
                return "down", errStr
        }
        defer resp.Body.Close()
        if resp.StatusCode == http.StatusOK {
                return "healthy", ""
        }
        // Non-200 (e.g. 503 from a runtime that's still loading the model).
        // Treat as "unhealthy" — the runtime is responding but not ready.
        return "unhealthy", fmt.Sprintf("HTTP %d", resp.StatusCode)
}
