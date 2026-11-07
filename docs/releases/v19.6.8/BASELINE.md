# ScarLiX OS v19.6.8 — Frozen Baseline Record

> **Status:** the Phase 0 baseline freeze (roadmap §4.1, Step 0.1) —
> recorded at v19.6.8 packaging. This file records the exact
> repository/toolchain facts the release was verified against.
> **Honesty rule:** facts not verifiable from this environment are marked
> `TARGET-HOST — record at acceptance`, never guessed.

------------------------------------------------------------------------

## 1. Repository baseline

| Fact | Value |
|---|---|
| Baseline commit (v19.6.7 line, pre-v19.6.8 changes) | `292b3d877b4f1f0b727f6286b7972096b07af512` (2026-11-07) |
| v19.6.7 remediation commit | `8a783cf221f5e9c346474e4dd308aec0e17e21c1` |
| v19.6.6 baseline (the roadmap's reference) | `96bfb2328ea759c74d4b093bf61d4ed00ded9d3d`, tag `v19.6.6` |
| v19.6.8 candidate | the commit carrying this file (record the hash in the release notes at tag time, the release-notes genre) |
| Branch | `main` (`MoZoHuJa/OS`) |

## 2. Toolchain (verified in the release environment)

| Fact | Value |
|---|---|
| Go | go1.23.4 linux/amd64 |
| go.mod directive | `go 1.23` |
| Module deps | `github.com/gorilla/websocket v1.5.3`, `gopkg.in/yaml.v3 v3.0.1` (go.sum pinned) |
| Python (media-tools compile check) | python3 py_compile PASS |

## 3. Active configuration set (frozen for this release)

- `VERSION` → `19.6.8`
- Compose set (the active dependency inventory,
  `docs/DEPENDENCY_INVENTORY_2026-11-06.md`, drift-guarded by
  `tests/test_dependency_inventory.sh`): ScarliHQ (local build,
  `golang:1.23-alpine` → `alpine:3.20`), SGLang `v0.4.9.post6-cu128-b200`
  (tag-pin), SMG `v1.4.1.post1-sglang-v0.5.10` (tag-pin), Ollama
  `0.40.7@sha256:2e6cc9…` (digest-pin), llama.cpp
  `@sha256:6d6076…` (digest-pin), LiteLLM `main-v1.23.9` (tag-pin),
  Hermes `v0.22.2@sha256:36099…` (digest-pin), + the edge stacks per the
  inventory.
- `models.yaml` — model-agnostic, no release identity in the header
  (KPK-19.6.5-P3-06); the compose files are the source of truth for
  model bindings.
- `profiles/{zmor,hugo,xox,mon}.yaml` — the identity-bound family
  profiles (v19.6.7); token index at `/etc/scarlix/profile-tokens.conf`
  (deployment-side, not in-repo).
- `docker/image-pin-allowlist.conf` — the four experimental `:latest`
  images, review-by 2027-01-31.

## 4. Target-host facts — record at acceptance

The following are NOT verifiable from the release environment and must
be recorded by the operator on the target host at hardware acceptance
(still OPEN, KPK-06): kernel version, Docker Engine version, NVIDIA
driver + CUDA runtime version, GPU UUIDs, SSD devices/filesystems,
`docker/daemon.json` live state (`iptables: false`, `bridge: none`).

## 5. Verification evidence (this environment, v19.6.8 candidate)

```
cd scarlihq
gofmt -l .            # clean (the v19.6.7 space-indented files normalized)
go vet ./...          # PASS
go build ./...        # PASS
go test ./...         # PASS — all packages
go test -race ./internal/allocation ./internal/reservation \
  ./internal/workload ./internal/hostbridge ./internal/compute \
  ./internal/api ./internal/agentkernel   # PASS
registry classification + dependency inventory + media-tools suites   # re-run at packaging
smoke --offline       # re-run at packaging (check count → 36)
```

Exact per-suite results are recorded in
`docs/RELEASE_NOTES_v19.6.8.md` §Verification.

## 6. What this baseline freezes (Phase 0 scope)

1. **Lifecycle vocabulary** — the agent compute intake actions are
   `start`/`release`; `"reserve"` is rejected by name (roadmap §4.3).
2. **State machine** — the frozen v19.2.2 table + the v19.6.8 additive
   states `STOP_REQUESTED` and `RECONCILE_REQUIRED` (roadmap §4.4), one
   lifecycle authority (`internal/allocation`).
3. **Canonical architecture docs** — `docs/architecture/` trio; stale
   architecture documents carry historical-snapshot banners (roadmap
   §4.2).
4. **Conservative admission guard** — one allocation per GPU/port until
   v19.7.1 (roadmap §4.6); Host Bridge acceptance stays OPEN and the
   workload bridge service stays disabled (roadmap §4.5).
5. **The roadmap itself** —
   `docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md`
   is now an in-repo planning document (release labels ≥ v19.7.0 are
   planning labels, not commitments).
