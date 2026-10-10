---
name: scarlix-os-v19-replica
description: Reconstruct, audit, or set up the SCARLiX OS v19.2.1 local-AI platform from its pinned public source or from this specification. Use when an AI coding agent must reproduce the current v19.2.1 behavior, architecture, installation, runtime stack, security boundaries, tests, and documentation without adding future-roadmap features.
---

# SCARLiX OS v19.2.1 — Replication Skill

## Mission

You are the implementation agent responsible for reproducing **SCARLiX OS v19.2.1**, the current frozen source snapshot described by this skill. Deliver a working, auditable, Linux-first local-AI platform and its repository structure. Preserve existing security boundaries and version-specific behavior.

**Do not implement future roadmap features.** Do not invent features and claim they exist. In particular, the v19.2.1 Compute Fabric scheduler is a **dry-run planner**: it creates a plan and does not perform real GPU allocation, port reservation, leases, process placement, or resource release. Preserve this behavior unless the task explicitly asks to change the version.

This skill describes the target system and the safe reconstruction procedure. It is not a substitute for source code if the goal is byte-for-byte reproduction. When network access is available, the pinned repository commit is the authoritative implementation. If only this file is available, reconstruct equivalent behavior from the specification, clearly document any implementation choices that cannot be verified against source, and never claim source-identical reproduction.

## Canonical source and version lock

- Repository: `https://github.com/MoZoHuJa/OS`
- Target version: `19.2.1`
- Target commit: `406d6929f511b3d00b26a5538ad04096f1fe2d9e`
- Version file: `VERSION` must contain exactly `19.2.1` plus a final newline.
- Bootstrap: `install.sh`
- OS base: EndeavourOS / near-vanilla Arch Linux; there is no custom ISO.
- Expected install flow: clone a specific version/commit, review, then run `bash install.sh` on a compatible EndeavourOS host.
- Do not silently switch to the repository's moving default branch. Always pin to the target commit or a verified tag.

Preferred source retrieval, when permitted:

```bash
git clone https://github.com/MoZoHuJa/OS.git scarlix-os
cd scarlix-os
git checkout 406d6929f511b3d00b26a5538ad04096f1fe2d9e
test "$(cat VERSION)" = "19.2.1"
git rev-parse HEAD
```

If this exact commit cannot be fetched, stop treating the checkout as the canonical snapshot. Record the discrepancy and either use the provided v19.2.1 archive or explicitly label the reconstruction as specification-derived.

## Non-negotiable rules

1. **Reconcile with code, not stale prose.** Some legacy documents in the snapshot contain older version headers, old port references, or diagrams that disagree with current code. Inspect executable scripts, Compose files, Go code, tests, and the frozen release baseline. If code and docs disagree, report the discrepancy and prefer verified runtime code for behavior; do not silently rewrite historical context.
2. **No destructive rewrite.** Do not replace a working repository with a simplified demo. Preserve directory structure, public interfaces, config names, runtime services, installer checkpoints, and security fixes.
3. **Small changes only.** If implementation work is needed, work in a branch, keep diffs focused, and make a rollback point before changes.
4. **Never fake validation.** Distinguish `PASS`, `FAIL`, `BLOCKED`, `NOT RUN`, and `WARNING`. A static check is not an end-to-end test. A healthy container is not proof that inference works.
5. **No unsafe privilege shortcuts.** Do not mount `/var/run/docker.sock` into the LAN-facing dashboard, do not expose inference endpoints on `0.0.0.0` at the host, and do not allow the dashboard to invoke arbitrary root shell commands.
6. **Keep secrets out of source.** Use `.env` / environment variables, restrictive permissions, and `.env.template` placeholders. Never commit actual API tokens, passwords, Hugging Face tokens, or private keys.
7. **Do not add roadmap scope.** The target is v19.2.1 only. Do not implement real scheduler allocation, leases, cross-device orchestration, multi-agent fabric, or any other not-yet-present feature under this skill.
8. **Fail closed.** Invalid configuration, unsafe bridge input, failed critical installation phases, invalid credentials, or failed mode transitions must not be treated as success.
9. **Preserve the tested security fixes.** The environment loader must not evaluate shell code. The host bridge must safely validate input files and privilege boundaries. The installer must abort on critical errors.
10. **Keep user data safe.** Do not run the installer or change host packages, boot configuration, firewall, Docker, systemd, or model directories unless the user explicitly authorizes a real host installation.

## What SCARLiX OS v19.2.1 is

SCARLiX is a Linux-first local-AI platform layered on EndeavourOS/Arch Linux. It is not merely a desktop theme and not a custom ISO. It combines:

- a host operating system and NVIDIA/CUDA/Docker substrate;
- several inference engines with a code-defined failover sequence;
- an OpenAI-compatible model gateway and a separate SMG routing service;
- local AI tools, media/voice services, monitoring, and networking;
- ScarliHQ, a Go control-plane/dashboard with REST, WebSocket, and MCP/JSON-RPC surfaces;
- a secure file-based host bridge for the small set of privileged host actions;
- profiles and policy-oriented controls;
- `scarlix-doctor`, shell utilities, systemd units, Compose files, model configuration, documentation, and smoke tests;
- a Resource Contract v1 and a scheduler that currently produces a dry-run plan only.

The intended product principle is local-first, resource-aware, modular inference with explicit privilege boundaries. Do not describe every service in the repository as always running. Optional services and experimental runtimes must remain optional or gated as defined in their existing configuration.

## System topology

Use the following as a conceptual map. Confirm exact names, ports, environment variables, mount points, and commands in the pinned source before writing or modifying implementation.

### Five logical layers

1. **Inference and base OS** — EndeavourOS/Arch, Linux kernel(s), NVIDIA drivers, CUDA 12.8-compatible containers, Docker Engine/Compose, model storage, inference runtimes.
2. **Workspace and infrastructure** — LiteLLM, SMG, Buzz/Nostr relay, Headscale, Caddy, CrowdSec, monitoring, and supporting services.
3. **Agents** — Hermes gateway/agent and Pi-Bolt coding-agent configuration. The OpenCode directory is deprecated documentation/config only; do not reactivate it as the primary coding agent.
4. **ScarliHQ control plane** — Go service, REST endpoints, WebSocket updates, MCP/JSON-RPC, profile loading, status reading, guard logic, mode request writer, embedded dashboard.
5. **Profiles/policy** — YAML profile definitions for `zmor`, `hugo`, `xox`, and `mon`; token budgets, permitted modes, themes, and any policy fields must be taken from the actual files.

### High-level mode/control flow

- User opens ScarliHQ and authenticates with a bearer token.
- ScarliHQ reads system status from `/var/lib/scarlix/host-status.json` and exposes it through its API/dashboard.
- When the user requests a mode, ScarliHQ writes a narrowly defined `desired-mode` request into `/var/lib/scarlix/bridge-input/`.
- A host-side systemd timer invokes the privileged host bridge.
- The host bridge uses the Go `scarlix-bridge-reader` to read and validate the request safely, then calls the fixed `scarlix-mode` command with a validated mode.
- Host status is gathered and written atomically to a status JSON file. ScarliHQ has read-only access to that status file.
- The dashboard container must not be granted direct Docker or arbitrary host privileges.

Do not replace this pattern with direct shell execution from an HTTP handler.

## Host bridge and security boundary

The bridge is one of the most important parts of the design. Reproduce its exact security posture from source:

- ScarliHQ is an untrusted/LAN-facing, non-root container (expected UID 65532).
- It must not receive Docker socket access, host NVIDIA runtime privileges, or a direct mount of `scarlix-mode`.
- Host status/config mounts should be read-only unless the canonical Compose explicitly requires otherwise.
- The only dashboard-writable bridge area is `/var/lib/scarlix/bridge-input/`, owned/restricted for the dashboard UID and mode `0700`.
- Bridge request creation should be atomic and exclusive (`O_EXCL`) so two requests cannot overwrite each other silently.
- The privileged reader should use `O_NOFOLLOW`, inspect the opened file with `fstat`, and read from the same file descriptor. Do not introduce a symlink or TOCTOU vulnerability by validating a path and then reopening it.
- Validate regular-file type, expected UID, mode, size limit (canonical design: at most 100 bytes), and allowed mode values. Reject unknown values.
- Use a lock to serialize bridge processing and another lock for mode transitions / model operations as in source.
- Write status through a temporary file and atomic rename; never expose a partially written JSON document.
- The HTTP API must require bearer-token authentication for privileged endpoints. Do not put tokens in query strings.
- WebSocket tickets are short-lived and single-use; preserve atomic ticket consumption and connection limits.
- Environment parsing must be a literal `KEY=VALUE` parser with a strict key allow-list. **Never `source` an untrusted `.env` file or use `eval`.**
- All host-facing commands should be fixed, argument-validated operations. Do not pass arbitrary user strings to a shell.
- Root-owned bridge state and dashboard-writable bridge input must remain separate directories with separate ownership/modes.

Security regression test requirement: include test cases for path traversal, symlink input, wrong UID/mode, oversized file, invalid mode, duplicate request, malformed environment keys, and command-substitution strings such as `PWN=$(touch /tmp/...)` and backticks. Verify that malicious strings are rejected and no marker file is created. Do not treat the commit's claimed mutation tests as locally executed unless you actually run them.

## Inference stack — current v19.2.1

The code-defined local mode switcher uses a four-tier failover sequence. Confirm the actual implementation in `files/usr/local/bin/scarlix-mode` before making changes.

| Order | Runtime | Intended role | Important guard |
|---|---|---|---|
| 1 | SGLang | Primary GPU inference on GPU 0; Qwen3-14B-AWQ | Keep `--disable-flashinfer`; use the exact image/config in pinned Compose |
| 2 | vLLM | Experimental GPU inference on GPU 1 | Start only when the `.experimental` gate is enabled; do not silently make it default |
| 3 | BeeLlama / llama.cpp | CPU fallback using GGUF Q4_K_M | Fallback after primary/experimental GPU tiers fail |
| 4 | Ollama | CPU tertiary fallback, starter `qwen2.5:3b` | If this also fails, mode transition must report failure |

Failover is not the same as the LiteLLM external gateway chain. Do not merge these sequences.

### SGLang (tier 1)

Canonical values present in the v19.2.1 Compose configuration:

- image: `lmsysorg/sglang:v0.4.9.post6-cu128-b200`
- container: `sglang`
- host bind: `127.0.0.1:30000`
- GPU visibility/device reservation: GPU `0`
- model default: `/models/Qwen3-14B-AWQ`
- static memory fraction default: `0.80`
- context length default: `32768`
- chunked prefill default: `4096`
- max running requests default: `4`
- shared memory default: `4gb`
- `--disable-flashinfer` must remain enabled for the target setup
- model mount: `/models:/models:ro`
- health endpoint: `/health`
- network: external Compose network `scarlix-net`

Do not blindly change the SGLang image tag, CUDA suffix, model path, or Blackwell flags. Verify image availability and actual CLI compatibility on the target host before changing them. The values above are defaults from the target Compose, not a guarantee that every GPU can fit this model/context configuration.

### vLLM (tier 2)

Canonical Compose defaults include:

- image: `vllm/vllm-openai:v0.8.5`
- host bind: `127.0.0.1:8089` → container port `8000`
- GPU `1`, tensor parallel size `1`
- default model `/models/Qwen3-14B-AWQ`
- GPU memory utilization default `0.80`
- max model length default `32768`
- `VLLM_TRUST_FLAG` is generated by validated configuration logic; do not replace with a truthiness expansion that interprets `"false"` as enabled.
- gated behind the experimental marker for mode-switching.

### BeeLlama / llama.cpp and Ollama

- BeeLlama uses the repository's llama.cpp Compose and a GGUF Q4_K_M model path/config. It is the third mode-switcher fallback.
- Ollama is the tertiary CPU fallback with starter model `qwen2.5:3b`.
- Keep host ports bound to loopback as in the pinned Compose.
- Do not assume that the fallback model is already downloaded. Model presence and readiness must be checked before reporting the runtime healthy.
- Preserve model locking between model manager, download operations, and mode transitions as implemented in source.

### LiteLLM gateway (external OpenAI-compatible clients)

The simplified gateway chain is separate from the local mode-switcher failover:

1. `scarlix-default` → SGLang OpenAI-compatible endpoint, normally `http://sglang:30000/v1`.
2. fallback → Ollama endpoint, normally `http://ollama-agent:11434`.
3. fallback → BeeLlama OpenAI-compatible endpoint, normally `http://beellama:8080/v1`.

vLLM is intentionally excluded from the normal LiteLLM chain because it is gated as experimental. Use the current `ai/litellm/config.yaml` as the authoritative mapping of model names, keys, and retry/fallback settings. Never put real master keys in a committed config.

### SMG

SMG is a separate routing/monitoring gateway with its own configuration and latency-aware fallback policy. Use `ai/smg/config.yaml` and `ai/smg/docker-compose.yml` as source of truth. Some old docs mention different port numbers; use the actual Compose host bind and report doc drift rather than changing ports to match stale text.

## Resource Contract v1 and scheduler behavior

Resource Contract v1 is a request schema. It describes what an agent wants; the inventory describes what the host has. Keep these concepts separate.

Canonical contract fields:

```yaml
version: v1
id: "550e8400-e29b-41d4-a716-446655440000"
agent_id: agent.coder
task:
  type: coding
  priority: interactive
compute:
  accelerator: cuda
  vram_mb: 12000
  cpu_cores: 4
  ram_mb: 8192
runtime:
  preferred: [sglang, vllm]
model:
  capabilities: [coding, reasoning]
security:
  filesystem: workspace
  network: restricted
  shell: sandbox
```

Contract rules:

- Top-level fields: `version`, `id`, `agent_id`, `task`, `compute`, `runtime`, `model`, `security`.
- Task types include `coding`, `chat`, `research`, `embedding`, `indexing`.
- Priorities include `realtime`, `interactive`, `normal`, `background`, `batch`.
- Accelerators include `cuda`, `cpu`, `rocm`.
- `compute.vram_mb`, `cpu_cores`, and `ram_mb` are integer requests; zero means no reservation requested.
- `runtime.preferred` is an ordered runtime-ID list; empty lists serialize as `[]`, not `null`.
- `model.capabilities` is a list; empty lists serialize as `[]`, not `null`.
- Security scopes include filesystem `workspace|none`, network `restricted|none`, shell `sandbox|none`.
- Consumers must tolerate additive unknown enum values for forward compatibility.
- Field names/types must remain stable. Do not rename or remove v1 fields.
- `scarlix-contract` commands, where present: `example`, `example --json`, `validate <file>`, and `parse <file>`.
- Contract v1 is frozen for this target.

**Critical target-version limitation:** the v19.2.1 scheduler is dry-run only. It may inspect contract/inventory and print a plan, but must not claim or implement a granted allocation, GPU lock, port lease, process start, or release operation. Mark all such behavior as not implemented in v19.2.1.

## Installation and host model

The target install is for a compatible EndeavourOS/Arch host, not Ubuntu or a generic cross-platform shell environment. Inspect `install.sh` before any real execution.

The installer is a privileged, phased bootstrap with checkpoints and logs. Broad phases include:

1. preflight and hardware discovery;
2. system package setup and Arch/multilib synchronization;
3. NVIDIA/CUDA/container prerequisites;
4. repository files and services installed into host paths;
5. interactive configuration, environment setup, and ScarliHQ build/deployment.

Preserve the actual phase boundaries and checkpoint validation from the script rather than replacing them with a short generic installer. Critical failures must stop the install immediately. Keep the installer log restricted (`/var/log/scarlix/install.log`, mode `0600`) because it may contain sensitive operational details. Re-running the installer should be deliberate and checkpoint-aware.

Host assumptions to validate:

- EndeavourOS / Arch Linux with `pacman` and systemd;
- UEFI recommended by the hardware guide;
- NVIDIA driver and `nvidia-smi` when NVIDIA GPU runtime is requested;
- Docker Engine and Compose plugin;
- `nvidia-container-toolkit` for GPU containers;
- `/models` as the model storage mount/path;
- external Docker network `scarlix-net`;
- writable system locations such as `/etc/scarlix`, `/var/lib/scarlix`, `/var/log/scarlix`, `/opt/scarlix`, `/usr/local/bin`, and `/etc/systemd/system`.

Never execute privileged installation on an arbitrary development machine just to “test” the skill. For code-only replication, implement/test in a temporary workspace or disposable VM and leave host installation as a separate, user-approved step.

## Hardware reference profile

The repository's documented main AI/gaming host profile is:

- CPU: AMD Ryzen 7 7700X, 8C/16T;
- RAM: 64 GB DDR5-5600;
- GPU 0: NVIDIA RTX 5060 Ti 16 GB (Blackwell);
- GPU 1: NVIDIA RTX 4060 Ti 16 GB (Ada);
- storage described by the repository as 2 TB system NVMe + data NVMe + backup HDD;
- NVIDIA/CUDA 12.8-compatible containers.

The project owner also has a related development machine profile of Ryzen 5, 64 GB RAM, RTX 5060 Ti 16 GB plus RTX 4060 Ti 16 GB, 2 TB + 500 GB NVMe and 1 TB HDD. Do not hard-code the owner’s machine as a requirement for all users. Detect hardware and degrade gracefully. Mixed GPU architectures are why GPU assignments and tensor parallelism must not be casually combined.

## Repository layout to reproduce

Use the exact pinned tree when available. A specification-derived implementation should preserve this functional layout and must not collapse everything into one script:

```text
.
├── .env.template
├── .github/workflows/ci.yml
├── AGENTS.md
├── LICENSE
├── README.md
├── VERSION
├── install.sh
├── packages.x86_64
├── models.yaml
├── agents/
│   ├── hermes/             # config + Compose
│   ├── pi-bolt/            # coding-agent config template + guide
│   └── opencode/           # deprecated migration note/config only
├── ai/
│   ├── sglang/
│   ├── vllm/
│   ├── llamacpp/           # BeeLlama CPU fallback
│   ├── ollama/
│   ├── litellm/            # gateway config + Compose
│   ├── smg/                # separate gateway
│   ├── browser-mcp/
│   ├── comfyui/
│   ├── freetoken/
│   ├── laya/
│   ├── musicgen/
│   ├── needle/
│   └── video/
├── scarlihq/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   ├── go.mod
│   ├── go.sum
│   ├── cmd/
│   └── internal/
├── files/
│   ├── etc/                # host configuration and systemd units
│   └── usr/local/bin/      # host CLI scripts
├── profiles/               # zmor.yaml, hugo.yaml, xox.yaml, mon.yaml
├── docker/                  # daemon config and socket-proxy definition
├── docs/                    # architecture, hardware, network, API/security maps
├── network/                 # Caddy and Headscale
├── monitoring/
├── security/crowdsec/
├── voice/                   # Piper, wakeword, Whisper
├── workspace/buzz/
├── gaming/
├── hp-agent/
├── media-tools/
└── render-banner.mjs
```

This is a functional map, not permission to invent missing source files. Prefer retrieving the exact pinned repository tree instead of recreating hundreds of files from summaries.

## ScarliHQ implementation contract

ScarliHQ is a Go application and Dockerized control plane. Inspect the existing code and preserve its actual packages and route names. Core areas include:

- main HTTP server and configuration;
- REST API;
- WebSocket status stream;
- MCP/JSON-RPC endpoint;
- mode request writer;
- status-file reader and status/API serialization;
- profile YAML loader;
- guard and auth logic;
- host-side `scarlix-bridge-reader` binary;
- embedded frontend/dashboard;
- resource contract and inventory packages;
- dry-run scheduler.

The dashboard container should run non-root (target UID 65532, Alpine runtime image in the canonical Dockerfile) and have only the capabilities/mounts needed for its job. The Go module targets Go 1.23 in the pinned source. Preserve `go.mod`/`go.sum` versions from the target snapshot; do not upgrade dependencies opportunistically.

Expected dashboard/service network surfaces from the target architecture include loopback-bound host endpoints:

| Service | Host bind / port | Purpose |
|---|---:|---|
| ScarliHQ | `127.0.0.1:8090` | REST + WS + MCP/dashboard |
| SGLang | `127.0.0.1:30000` | Primary GPU inference |
| vLLM | `127.0.0.1:8089` | Experimental GPU inference |
| Ollama | `127.0.0.1:11435` | CPU fallback |
| BeeLlama | `127.0.0.1:11438` | CPU GGUF fallback |
| LiteLLM | `127.0.0.1:4001` | OpenAI-compatible gateway |
| SMG | verify Compose file | Separate routing gateway |
| Whisper | `127.0.0.1:8002` | Voice/STT service |
| Grafana | Headscale-only address in Compose | Private monitoring |

These are architectural reference values. Before asserting the final port map, parse the pinned Compose files and report mismatches; do not silently “correct” code to match a stale document.

## Current profiles

Reproduce the actual YAML profiles from the pinned repository. The documented intent is:

- `profiles/zmor.yaml` — administrator profile, unlimited token budget;
- `profiles/hugo.yaml` — 100k token budget;
- `profiles/xox.yaml` — 10k token budget, kid-safe restrictions;
- `profiles/mon.yaml` — 50k token budget.

The mode/theme examples in legacy architecture docs may be stale. Read the current YAML and Go validation logic. Do not invent policy enforcement just because a profile field exists; test that each security-relevant field is actually enforced.

## Model/configuration behavior

- `models.yaml` is the primary configuration map for model/runtime selection and environment generation. Read it before modifying model paths, trust flags, VRAM/context settings, or fallback behavior.
- Keep `.env.template` as placeholder-only; never copy real credentials into it.
- Generated LiteLLM config must obtain the SCARLiX version from the supported version source (repo `VERSION`, `/etc/scarlix/VERSION`, or `/usr/local/share/scarlix/VERSION` as implemented), not from a stale hard-coded release header.
- Model downloads and model manager operations must be serialized using the existing model lock.
- Never claim a model is available solely because a path is configured. Check file existence, expected format, and readiness.
- v19.2.1 does not yet provide the later model SHA-256/revision integrity feature described as a roadmap item. Do not represent that feature as implemented.
- Image digest pinning is not complete across all services in this target. Preserve existing image tags and document the remaining supply-chain limitation; do not imply every image is immutable.

## Runtime, security, and operational behavior

Reproduce the current source behavior for the following utilities and services, found under `files/usr/local/bin/` and `files/etc/`:

- `scarlix-mode`: controlled mode transition and verified AI startup/failover;
- `scarlix-host-bridge`: host status collection and dispatch of validated mode requests;
- `scarlix-bridge-reader`: safe host-side reader;
- `scarlix-doctor`: diagnostics and explicit repair mode;
- model manager / model download scripts and their lock handling;
- generated LiteLLM configuration;
- systemd units and timers for bridge/status/model operations;
- shell environment/config parsing and smoke tests.

Do not guess command-line flags. Read `--help` or source and keep argument validation strict. Any automatic repair must be explicit, logged, and idempotent; `--fix` must not be silently enabled during diagnostics.

## CI and tests

The target repository includes `.github/workflows/ci.yml` with static checks, YAML/Compose checks, and Go checks. Inspect the workflow itself; do not assume a test is executed merely because it exists in the repository.

Reproduction acceptance sequence:

1. **Source identity**
   - `git rev-parse HEAD` equals the target commit if using canonical source.
   - `VERSION` equals `19.2.1`.
   - `git status --short` is clean before changes.
2. **Archive and tree**
   - Verify ZIP integrity if using the archive.
   - Verify required directories and files exist.
   - Compare the resulting tree against the pinned source where available.
3. **Shell**
   - `bash -n` every shipped Bash script.
   - Run ShellCheck if installed; if absent, report `BLOCKED`/`NOT RUN`, never PASS.
4. **YAML and Compose**
   - Parse YAML files.
   - Validate Compose configurations with the installed Docker Compose plugin where available.
   - Note that generic YAML parsing is not equivalent to Compose semantic validation.
5. **systemd**
   - Run `systemd-analyze verify` on shipped unit files when supported.
   - Confirm service/timer dependencies and privilege context.
6. **Go**
   - Run `gofmt -l` across Go source.
   - Run `go vet ./...` and `go test ./...` from the correct module directory.
   - If dependency downloads fail due to no network, report `BLOCKED`; do not claim the tests passed.
7. **Smoke tests**
   - Run the shipped smoke-test script in offline mode where appropriate, using its documented flags and repo-root environment.
   - Current observed v19.2.1 baseline: **9 PASS, 0 FAIL, 1 WARN** in offline mode. Warnings included registry checks skipped offline. ShellCheck now runs in CI as a separate job, and the smoke test itself runs in CI as a new job added in v19.2.1. A new environment may differ.
8. **Security regression**
   - Run explicit mutation tests for the environment loader / heredoc injection guards.
   - Test command substitutions (`$(`), backticks, malformed keys, newlines, unexpected values, symlinks, file permissions, oversize inputs, invalid modes, and duplicate bridge requests.
   - Assert that rejected input causes no side effects.
9. **Integration**
   - In a disposable host/VM with compatible NVIDIA hardware, verify Docker/NVIDIA runtime, each service health endpoint, actual model loading, one real inference request through SGLang, and expected failover behavior.
   - Test CPU fallback separately.
   - Verify loopback binding and confirm there are no unintended LAN-exposed inference/control endpoints.
10. **Recovery**
    - Test a failed image pull, missing model, failed inference, invalid env, service restart, and interrupted mode transition.
    - Verify locks are released, no stale bridge request remains, and the status file remains valid.
11. **Report**
    - Summarize commands, exact outcomes, logs, limitations, and hardware/environment.
    - Separate `PASS`, `FAIL`, `BLOCKED`, `NOT RUN`, `WARNING`.

Do not run destructive or privileged tests on a production host. Use a disposable VM or test machine.

## Known v19.2.1 limitations that must remain explicit

- Compute Fabric scheduler is **dry-run only**; no real GPU allocation, port assignment, leases, reservation manager, or resource release.
- Physical GPU inference and full end-to-end runtime lifecycle are not proven by the offline smoke test alone.
- Go tests/vet can be blocked when dependencies are unavailable offline; that is not a test pass.
- ShellCheck may be unavailable in the environment.
- Image digest pinning and model revision/hash integrity are not complete across the whole stack.
- Some legacy documentation may contain stale version headers, ports, or tier diagrams. Verify source before trusting it.
- Do not assert that Android, Windows, macOS, cross-device orchestration, a universal runtime, or multi-agent fabric is implemented in v19.2.1.
- Do not state that CI is green unless you have fetched and checked the actual workflow run for the target commit.

## Acceptance criteria

The reconstruction is complete only when:

- [ ] It is clearly identified as SCARLiX OS v19.2.1.
- [ ] The repository has the expected directory and component structure, or any deliberate deviations are documented.
- [ ] Installer behavior remains phased, checkpointed, fail-hard, and restricted to supported host assumptions.
- [ ] Inference configurations and mode-switcher failover match the pinned source.
- [ ] ScarliHQ uses the secure file-based host bridge; no direct Docker socket or arbitrary root shell is exposed to the dashboard.
- [ ] Environment parsing never executes values as shell code.
- [ ] Resource Contract v1 remains schema-compatible.
- [ ] Scheduler remains dry-run only for this target version.
- [ ] Optional services stay optional and vLLM remains experimental-gated.
- [ ] Secrets are placeholders only and host-facing ports are checked.
- [ ] Static tests and smoke tests are executed and results accurately reported.
- [ ] Any test blocked by environment/dependencies is labelled as blocked.
- [ ] No future roadmap features have been added or represented as complete.
- [ ] A final `REPLICATION_REPORT.md` records source identity, file tree, tests, limitations, and any deviations from the canonical source.

## Recommended agent execution plan

### Phase A — Discover, do not modify
1. Read this entire skill.
2. Check whether the user supplied a source archive or repository checkout.
3. If available, inspect `VERSION`, Git commit, `AGENTS.md`, `README.md`, `docs/SCARLIX_RELEASE_BASELINE.md`, `docs/SCARLIX_CURRENT_ARCHITECTURE.md`, `docs/SCARLIX_COMPONENT_MAP.md`, `docs/SCARLIX_RESOURCE_CONTRACT.md`, `docs/SCARLIX_SECURITY_MAP.md`, `docs/SCARLIX_RUNTIME_MAP.md`, `docs/SCARLIX_GPU_MAP.md`, `docs/SCARLIX_API_MAP.md`, `docs/HARDWARE.md`, `docs/NETWORK.md`, and the actual source/config files they describe.
4. Generate a manifest of files and compare it to the pinned commit.
5. Record all source/docs mismatches before deciding which values to implement.

### Phase B — Reproduce
1. Prefer copying/building from the pinned source when available; that is the only reliable path to source-identical behavior.
2. If forced to implement from this specification alone, create modules in the repository map above and implement the smallest correct version of each described component. Do not invent undocumented APIs: define them explicitly, document them, and mark them as reconstruction choices.
3. Implement security boundaries before dashboard conveniences.
4. Implement tests alongside each subsystem.
5. Keep scheduler dry-run behavior and preserve all target-version limitations.

### Phase C — Validate
1. Run all locally possible acceptance checks.
2. Run security regression tests.
3. Run integration tests only in a disposable, compatible environment.
4. Inspect network binds, container privileges, file permissions, secrets, and systemd units.
5. Create `REPLICATION_REPORT.md` with evidence and limitations.
6. Do not call the system “production ready” merely because it builds.

### Phase D — Deliver
Return:
- what was reconstructed and from which source;
- target commit/version;
- a component/file-tree summary;
- exact test results;
- hardware and environment limitations;
- known gaps;
- how to run safely on a compatible test host;
- explicit statement that v19.2.1 scheduler is dry-run only.

## Final principle

**Faithful replication beats creative reinterpretation.** Recreate the current v19.2.1 system as it exists, preserve its boundaries, test what can be tested, and label everything else honestly. Do not turn this task into a future product redesign.
