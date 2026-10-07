# SCARLIX OS v19.1.9 — Resource Foundation Release

> **Status:** FROZEN. v19.1.9 is the Resource Foundation release. No new
> architecture added — this version freezes the v19.1.x generation and runs
> full regression. Per ScaRgeN master guide section 19.

## Frozen artifacts (v19.1.0–v19.1.9)

### Go packages (10)
| Package | Version | Tests |
|---------|---------|-------|
| `internal/api` | v19.0.6+ | PASS |
| `internal/compat` | v19.1.8 (GPU×Runtime×Model matrix) | 11 PASS |
| `internal/contract` | v19.1.0 (Resource Contract v1) + v19.1.1 (validation) | 27 PASS |
| `internal/inventory` | v19.0.7 (types) + v19.0.8 (GPU) + v19.0.9 (runtime/model) | 39 PASS |
| `internal/monitor` | v19.1.4 (ScarliMonitor Foundation) | 8 PASS |
| `internal/registry` | v19.1.2 (Runtime + Model registries) | 20 PASS |
| `internal/scheduler` | v19.1.7 (dry-run, deterministic scoring) | 9 PASS |
| `internal/status` | v19.0.6+ | PASS |
| `internal/telemetry` | v19.1.5 (JSON Lines persistence) | 10 PASS |
| `internal/scarlix_mode` | v19.0.6+ | PASS |

**Total: 145 tests PASS / 0 FAIL / 0 SKIP**

### Go binaries (7)
| Binary | Purpose | Version |
|--------|---------|---------|
| `scarlihq` | Dashboard + REST API + WebSocket + MCP | v19.0.6+ |
| `scarlix-bridge-reader` | Host-Bridge atomic file reader | v19.0.6+ |
| `scarlix-gpu` | GPU telemetry (normalized JSON) | v19.0.8 |
| `scarlix-inventory` | Full system inventory (GPUs+runtimes+models+health) | v19.0.9 |
| `scarlix-contract` | Resource Contract v1 (example/validate/parse) | v19.1.0 |
| `scarlix-monitor` | ScarliMonitor (snapshot + HTTP server + telemetry) | v19.1.6 |
| `scarlix-scheduler` | Dry-run scheduler (compute plan, deterministic scoring) | v19.1.7 |

### Bash CLIs (8)
| Script | Purpose |
|--------|---------|
| `scarlix` | Unified CLI (system status, gpu/runtime/model list, --json) |
| `scarlix-mode` | GPU mode switcher (ai/turbo/creative/tv/game/offline/stop) |
| `scarlix-doctor` | Self-diagnostic (--fix, --strict) |
| `scarlix-wizard` | Setup wizard |
| `scarlix-host-bridge` | Root timer (host-status.json every 5s) |
| `scarlix-smoke-test.sh` | 10-check regression validation (13/0/0 green) |
| `model-manager.sh` | Model manager (weekly HF auto-pull) |
| `download-models.sh` | Model downloader |
| `generate-env.sh` | .env generator (atomic, fail-closed) |
| `generate-litellm-config.sh` | LiteLLM config from models.yaml |

## Full regression results (v19.1.9)
```
bash -n (all scripts)              → PASS
shellcheck -S warning (CI scripts) → CLEAN
YAML lint (26 compose + smg)       → 26/26 OK
systemd-analyze verify (5 units)   → PASS
VERSION consistency                → PASS (19.1.9 everywhere)
go.mod module path                 → PASS (github.com/MoZoHuJa/OS/scarlihq)
go vet (all 10 packages)           → CLEAN
go test (all 10 packages)          → 145 PASS / 0 FAIL
go build (all 7 binaries)         → OK
image tags (5 via registry API)    → HTTP 200
ollama-main active refs           → 0
v12 doc headers in active docs     → 0
scarlix-smoke-test.sh             → 13 passed, 0 failed, 0 warned
```

## What is frozen (must NOT change without version bump)
- Resource Contract v1 schema (field names FROZEN in v19.1.0)
- Validation rules (6 reject categories, v19.1.1)
- RuntimeRegistry + ModelRegistry APIs (v19.1.2/v19.1.3)
- ScarliMonitor Snapshot schema (v19.1.4)
- Telemetry Measurement fields (10, FROZEN in v19.1.5)
- HTTP API endpoints (GET /, /health, /telemetry — v19.1.6)
- Scheduler scoring formula (deterministic, v19.1.7)
- Compatibility rules (5, v19.1.8)

## Next generation: v19.2.x Compute Fabric
- v19.2.0: Real scheduler (request→inspect→score→allocate→execute→release)
- v19.2.1: Deterministic scoring (refined from v19.1.7 dry-run)
- v19.2.2: Manual GPU selection
- v19.2.3: Preferred GPU
- v19.2.4: Automatic GPU selection
- v19.2.5: Priority queues
- v19.2.6: Safe fallback
- v19.2.7: Resource leases
- v19.2.8: ScarliHQ compute control
- v19.2.9: Compute Fabric release (freeze + benchmark)
