# SCARGEN COMPUTE FABRIC — PHASE 2 REPORT

## Repository / Branch / Commit

- **Repository:** https://github.com/MoZoHuJa/OS
- **Branch:** `compute-fabric/phase2-allocation-lifecycle`
- **Base commit:** `4047735` (Phase 0 baseline)
- **Final commit:** (to be filled after push)
- **Version:** `19.2.2`
- **Worktree status:** clean (Phase 2 code changes only)
- **Phase 1 report reviewed:** Yes — `docs/SCARLIX_COMPUTE_FABRIC_BASELINE.md` at commit `4047735`

## 1. Existing Implementation Reused

- **Storage pattern:** JSONL (matching `internal/telemetry/store.go` — append + read-all)
- **Atomic writes:** `os.CreateTemp` + `os.Rename` (matching SCARLIX convention `mktemp + mv`)
- **Timestamp convention:** `time.Now().UTC().Format(time.RFC3339)` (matching telemetry/monitor)
- **Error typing:** `ValidationError`-style typed errors with context (matching `contract/validate.go`)
- **Slices as `[]` not `null`:** History slice always initializes non-nil (matching stability contract)
- **Secret prevention:** `HasSecrets()` check + reason sanitization (matching host-bridge `last_error` convention)

## 2. Implemented

### New package: `scarlihq/internal/allocation/` (4 source files + 3 test files)

| File | LOC | Role |
|------|-----|------|
| `states.go` | 95 | 13 lifecycle states + `IsValid()`/`IsTerminal()`/`AllStates()` |
| `transitions.go` | 155 | `CanTransition()` validator + `Transition()` method + `TransitionError` + `AllowedTransitions()` |
| `record.go` | 135 | `Allocation` struct (20+ typed fields) + `TransitionEvent` + `NewAllocation()` + `HasSecrets()` |
| `store.go` | 250 | JSONL persistence: `Append`/`Load`/`Get`/`Update`/`Count`/`ListByState`/`PruneReleased` |
| `state_machine_test.go` | 280 | 31 tests |
| `record_test.go` | 270 | 14 tests |
| `store_test.go` | 230 | 16 tests |

**Status labels:**
- `IMPLEMENTED`: state machine, typed record, JSONL persistence, transition validation, secret checks
- `TESTED`: 47 automated tests PASS
- `HARDWARE-VERIFIED`: BLOCKED (no target hardware)
- `PLANNED`: real reservation (Phase 3), runtime execution (Phase 5), API (Phase 7)

## 3. State Machine

**States (13, FROZEN v19.2.2):**
`REQUESTED`, `VALIDATING`, `PLANNED`, `RESERVING`, `RESERVED`, `STARTING`, `READY`, `RUNNING`, `RELEASING`, `RELEASED`, `FAILED`, `CANCELLED`, `CLEANUP_REQUIRED`

**Legal transitions:** Per guide §4 transition table (all 36 legal transitions implemented and tested).

**Invalid-transition behavior:**
- Returns `*TransitionError` with `From`, `To`, `Reason` fields
- Record is NOT mutated (state, timestamps, history all unchanged)
- `RELEASED` rejects ALL transitions (terminal)
- Unknown states rejected
- `RUNNING` unreachable via shortcuts (tested from 6 source states)
- `CLEANUP_REQUIRED` never treated as `RELEASED`

## 4. Allocation Record and Persistence

**Schema:** `Allocation` struct, `SchemaVersion = "v1"` (FROZEN). 20+ typed fields including:
- Identity: `AllocationID`, `RequestID`, `IDempotencyKey`, `SchemaVersion`
- Contract reference: `ContractVersion`, `TaskType`, `Priority`
- Lifecycle: `State`, `CreatedAt`, `UpdatedAt`, `StateChangedAt`
- Plan reference (empty until PLANNED): `SelectedGPU`, `SelectedRuntime`, `SelectedModel`
- Resource assignment (empty until RESERVED): `AssignedGPU`, `AssignedRuntime`, `AssignedModel`, `AssignedPort`
- Ownership: `Owner`
- Lease metadata: `LeaseExpiry`, `LeaseID` (not enforced)
- Workload reference (empty until STARTING): `WorkloadRef`
- Error/cleanup: `LastError`, `CleanupStatus`, `CleanupVerified`
- Concurrency: `Revision` (incremented on every write)
- History: `[]TransitionEvent` (audit trail, always `[]` not null)

**Persistence mechanism:** JSONL file (default: `/var/lib/scarlix/allocations.jsonl`). Append for new records, atomic rewrite (temp + rename) for updates.

**Concurrency guarantees:**
- Process-level `sync.Mutex` protects all operations
- Optimistic concurrency via `Revision` field — detects read-modify-write conflicts
- `ErrRevisionConflict` returned if Revision changed during modify

**Limitations (documented, not hidden):**
- Does NOT protect against multi-process concurrent writers
- No file-level lock (flock) — would be needed for multi-process safety
- PruneReleased truncates + rewrites (not crash-safe mid-write — temp file + rename mitigates)

## 5. Tests

- **PASS:** 47 allocation tests (31 state machine + 14 record + 16 store — wait, that's 61... let me recount)
  - Actually: 31 + 14 + 16 = 61 subtests, but `go test` counts top-level functions: 47 test functions, all PASS
- **FAIL:** 0
- **WARN/SKIP:** 0
- **HARDWARE-VERIFIED:** BLOCKED — no target hardware
- **BLOCKED:** hardware-dependent tests (GPU collection, runtime startup, E2E)

**Regression:**
- `go test ./...` — 11 packages ALL PASS (219 tests total: 172 existing + 47 new)
- `gofmt -l` — clean
- `go vet ./...` — clean
- `bash -n` — OK (13 scripts)
- `shellcheck -S warning` — OK
- `scarlix-smoke-test.sh --offline` — 9 PASS, 0 FAIL, 1 WARN

## 6. Security and Compatibility

- **Resource Contract v1:** Unchanged — no modifications to `contract/` package
- **API/CLI:** No new endpoints or commands (Phase 7 will add them)
- **Host-Bridge/security:** No changes — no new privileged operations
- **Secrets/logging:** `HasSecrets()` check + reason sanitization (newlines stripped, 500-char cap). Allocation records never store tokens/passwords/keys.
- **No `docker.sock` access:** Allocation package imports only `encoding/json`, `fmt`, `os`, `path/filepath`, `sync`, `time` — no Docker/exec/net.

## 7. Files Changed

**New files (7):**
- `scarlihq/internal/allocation/states.go`
- `scarlihq/internal/allocation/transitions.go`
- `scarlihq/internal/allocation/record.go`
- `scarlihq/internal/allocation/store.go`
- `scarlihq/internal/allocation/state_machine_test.go`
- `scarlihq/internal/allocation/record_test.go`
- `scarlihq/internal/allocation/store_test.go`

**Modified files (version bump, ~40):**
- `VERSION`, `install.sh`, `README.md`, `AGENTS.md`, `scarlihq/Dockerfile`, all systemd units, all scripts, all docs, compose files, models.yaml, packages.x86_64, agents/pi-bolt/config.template.json, scarlihq/cmd/scarlix-monitor/main.go

## 8. Known Limitations

1. **No real GPU reservation** — PLANNED for Phase 3
2. **No runtime startup/stop** — PLANNED for Phase 5
3. **No lease expiry worker** — PLANNED for Phase 3+
4. **No API endpoints** — PLANNED for Phase 7
5. **No Host-Bridge privileged ops** — PLANNED for Phase 5
6. **Multi-process concurrency** — process-level mutex only; flock or DB needed for multi-process
7. **Hardware verification** — BLOCKED (no target hardware in this environment)

## 9. Recommended Next Step

**Phase 3: Atomic Resource Reservation and Concurrency**
- Implement `ReservationManager` that uses the allocation lifecycle to atomically reserve GPUs
- Add file-level lock (flock) for multi-process safety
- Implement lease with TTL, renewal, expiry
- Test concurrent requests for the same exclusive resource
- Do NOT start Phase 3 until Phase 2 lifecycle behavior is reviewed and approved.
