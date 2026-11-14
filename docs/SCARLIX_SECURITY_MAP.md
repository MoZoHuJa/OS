# SCARLIX OS v19.3.1 — Security Map

> Purpose: Catalog every security boundary in the v19.0.6 tree — the privilege separation model, atomic file operations, the TOCTOU-safe reader, the fail-closed env loader, lock file topology, and the systemd hardening matrix. All values cited from files on branch `fix-v19.0.6`.
>
> **v19.2.12 update:** added section (h) — experimental image supply-chain residual risk (Deep Audit F-08) and section (i) — Pi-Bolt installer SHA-256 pin (Deep Audit F-01).
>
> **v19.2.13 update:** section (i) extended with the user-owned verified-copy execution handoff (KPK-19.2.12-P2-01) and the scheduled pin-freshness CI job; new section (j) — reservation-layer CleanupVerified semantics (KpK audit P2-02).
>
> **v19.4.2 update:** dependency pin hygiene per `docs/DEPENDENCY_AUDIT_2026-10-21.md` — P0 (pruned upstream tags: coolify, sunshine, buzz/minio) and P1 (stale security pins: crowdsec, ollama, gitea, nextcloud, headscale) fixed; 5 pins now digest-pinned; smoke check 07 existence-checks all fixed pins in CI (section h extended).
>
> **v19.4.3 update (KpK review 2026-10-22 remediation):** check_07 now uses the explicit classification VERIFIED_EXISTS / MISSING / UNVERIFIED (transport errors, 401/403/429/5xx and malformed auth never count as existing; only a definitive 404 fails CI) with a deterministic mock-tested suite (`tests/test_registry_classification.sh`, in CI); probes are digest-aware and cover every digest pin in active compose files (14 probes). The buzz workspace is **formally disabled** (KPK-03: minio/minio dead upstream — all channels, source archived; profile renamed `.disabled`, quarantined pin out of every deploy path, guarded by smoke check_19). Registry evidence classes are separated in `docs/RELEASE_GATE_v19.4.3.md` — CI green is never claimed as hardware acceptance (KPK-06 protocol: `docs/HARDWARE_ACCEPTANCE_PROTOCOL.md`).
>
> **v19.6.7 update (the v19.6.6 KpK audit remediation):** section (r) is updated to the IDENTITY-BOUND family-profile contract (KPK-19.6.6-P1-01 CLOSED: per-profile Bearer tokens, one shared `profiles.Authenticator.Authorize`, derivation of omitted references, admin-only operator surfaces — the caller-declaration residual is gone); new section (t) — the ENFORCED monthly token budget (KPK-19.6.6-P2-02 CLOSED: durable ledger, reserve-before-forward, settle/release semantics, stable 429, fail-closed unavailability, the documented degraded policy); section (s) is updated (recursive newest-file mtime, broadened compose discovery, the time-bounded pin allowlist — KPK-19.6.6-P3-05 + SK-audit P2-03); new section (u) — the media-tools request boundary (KPK-19.6.6-P2-03 CLOSED: bearer auth, bounded concurrency, deliberate error answers). Residuals updated: hardware acceptance stays OPEN, the content filter stays keyword-level, the budget covers the chat path only (the propose path is admin-gated), the IPv6 dual-stack note stands.

## (a) Host-Bridge privilege separation

The ScarliHQ container is **untrusted + LAN-facing**. It has been progressively stripped of host capability since v17.9.8.

| Capability | Status | Source |
|---|---|---|
| docker.sock mount | **REMOVED** (v17.9.8 P0) | `scarlihq/docker-compose.yml` volumes — no `/var/run/docker.sock` |
| `nvidia` runtime | **REMOVED** (v17.9.8 P0) | `scarlihq/docker-compose.yml` — no `deploy.resources.reservations.devices` |
| `scarlix-mode` script mount | **REMOVED** (v17.9.8 P0) | compose — no `/usr/local/bin/scarlix-mode` mount |
| Container UID | **nonroot UID 65532** (alpine `adduser -D -u 65532 -H -g "" nonroot`) | `scarlihq/Dockerfile` line ~46 |
| Writable host path | ONLY `/var/lib/scarlix/bridge-input` (rw) | compose volumes |
| Read-only host paths | `/var/lib/scarlix` (parent ro), `/etc/scarlix` (ro) | compose volumes |
| Container command | `/scarlihq` (Go binary, `USER nonroot`, `ENTRYPOINT ["/scarlihq"]`) | Dockerfile |

The container cannot call `docker`, `nvidia-smi`, `systemctl`, or write anywhere except `bridge-input/`.

## (b) `bridge-input/` vs `bridge-state/` (the two-directory split)

| Directory | Owner | Mode | Writable by | Purpose |
|---|---|---|---|---|
| `/var/lib/scarlix/bridge-input/` | `65532:65532` | `700` | ScarliHQ container (nonroot UID 65532) | Holds ONLY `desired-mode` (the file ScarliHQ writes to request a mode switch) |
| `/var/lib/scarlix/bridge-state/` | `root:root` | `700` | Host bridge (root) | Holds `.retry`, `last-transition` — state ScarliHQ must NOT see or modify |
| `/var/lib/scarlix/host-status.json` | `root:root` | `644` | Host bridge (root) | Read by ScarliHQ (ro mount) for `/api/status` + `/ws` |
| `/var/lib/scarlix/current-mode` | `root:root` | (default) | Host bridge via `scarlix-mode` | Read by ScarliHQ (ro mount) for current mode |

The split (v18.0.0 P0) was the fix for v17.9.9's single `bridge/` dir which let ScarliHQ create symlinks in a directory the root host-bridge wrote to — a symlink-attack surface. Now ScarliHQ cannot create symlinks anywhere root reads from.

`scarlix-host-bridge` verifies `bridge-input/` integrity at every timer tick:
```bash
input_owner=$(stat -c '%u:%g' "$INPUT_DIR" 2>/dev/null || echo "?")
input_mode=$(stat -c '%a' "$INPUT_DIR" 2>/dev/null || echo "?")
if [ "$input_owner" != "65532:65532" ] || [ "$input_mode" != "700" ]; then
    echo "FATAL: bridge-input integrity compromised ..." >> "$BRIDGE_LOG"
    exit 1
fi
```
And for `bridge-state/`:
```bash
chown root:root "$STATE_DIR_ROOT" 2>/dev/null || { ...; exit 1; }
chmod 700 "$STATE_DIR_ROOT" 2>/dev/null || { ...; exit 1; }
```
Source: `files/usr/local/bin/scarlix-host-bridge` lines ~73–98.

## (c) `scarlix-bridge-reader` (Go, TOCTOU-safe)

The host bridge does NOT use the shell `validate_input_file && cat && re-validate` pattern (which had a TOCTOU window — ScarliHQ could swap the file between validate and read). Instead it calls a Go binary built from `scarlihq/cmd/scarlix-bridge-reader/main.go`.

| Property | Value | Source |
|---|---|---|
| Open flags | `O_RDONLY \| O_NOFOLLOW \| O_NONBLOCK` | main.go line ~67 |
| Symlink defense | `O_NOFOLLOW` returns `ELOOP` if `path` is a symlink — ScarliHQ cannot create a symlink to `/etc/shadow` and have root read it via the bridge | main.go comment |
| FIFO/pipe defense | `O_NONBLOCK` makes Open return immediately (no block); subsequent `fstat` rejects `S_IFMT != S_IFREG` | main.go |
| Stat method | `syscall.Fstat(fd, &st)` — operates on the open fd, NOT the path | main.go line ~83 |
| UID check | `st.Uid != 65532` → reject (only the ScarliHQ nonroot UID is allowed to write `desired-mode`) | main.go line ~108 |
| Mode whitelist | `mode in {0600, 0640, 0700}` (0600 = owner-only, 0640 = owner+group read, 0700 = owner rwx — all have `0` in the others octet) | main.go line ~120 |
| Size cap | `st.Size > 100` → reject (mode name is max ~20 chars; prevents DoS) | main.go line ~130 |
| Read method | `io.ReadFull(file, buf)` from the SAME fd — atomic with the `fstat` (kernel holds the inode reference via the fd; rename/unlink/recreate of `path` cannot affect this read) | main.go line ~138 |
| Stderr log | `/var/log/scarlix/bridge-reader.log` (v18.7.6 P0: was `/tmp/sbr.err` — predictable path, symlink risk) | `scarlix-host-bridge` |
| Fallback | **NONE** — if the binary is missing, host-bridge logs FATAL, deletes `desired-mode`, marks transition `rejected` | `scarlix-host-bridge` line ~140 |

## (d) `.env` file handling

There are TWO `.env` files — do not confuse them.

### `/etc/scarlix/.env` — secrets (root:root 600)

| Property | Value | Source |
|---|---|---|
| Owner | `root:root` | `files/etc/systemd/system/generate-env.sh` (final `chown root:root`) |
| Mode | `600` | same (final `chmod 600`) |
| Atomic write | mktemp `${ENV_FILE}.XXXXXX` → write → `chmod 600` → `chown root:root` → `mv -f` (no partial-on-crash window) | `generate-env.sh` lines ~210–215 |
| Loaded by | ScarliHQ compose (env: `SCARLIHQ_TOKEN`), LiteLLM compose (`LITELLM_MASTER_KEY`), SMG compose (`SMG_MASTER_KEY`), Buzz compose, monitoring compose | respective compose files |
| Validation | `REQUIRED_KEYS="SCARLIHQ_TOKEN LITELLM_MASTER_KEY SMG_MASTER_KEY RESTIC_PASSWORD"` — non-empty check, fail-closed exit 1 | `generate-env.sh` |
| Dup detection | Only `^[A-Z_][A-Z0-9_]*=` lines counted (v19.0.1 P1-a — was: also counted comments → false positives → scarlix-doctor dead-loop) | `generate-env.sh` |
| Orphan detection | Multi-line base64 fragments flagged; `JWT_SECRET` auto-regenerated if truncated; `STORAGE_ENCRYPTION_KEY` NOT auto-regenerated (rotating it makes existing encrypted data unreadable) | `generate-env.sh` |

### `/opt/scarlix/.env` — engine runtime env (root:root 600)

| Property | Value | Source |
|---|---|---|
| Owner | `root:root` (v18.5 P0 LPE fix — was: `chown $REAL_USER /opt/scarlix` → LPE if user modifies .env and root service `source`s it) | `files/usr/local/bin/scarlix-mode::generate_env_file()` |
| Mode | `600` | `scarlix-mode` (chmod 600 on tmp before mv) |
| Atomic write | mktemp `${ENV_FILE}.XXXXXX` → heredoc → `chmod 600` → `mv -f` (v18.9.0 P2-17) | `scarlix-mode` |
| Generated from | `/etc/scarlix/models.yaml` via `yq` (single source of truth) | `scarlix-mode` |
| Loaded by | `docker compose --env-file /opt/scarlix/.env` (NEVER `source`d by a root script) | `scarlix-mode`, `model-manager.sh`, `download-models.sh` |
| `load_env_safe()` | `grep -E '^[A-Z_][A-Z0-9_]*='` whitelist + `export "$key=$value"` — no shell evaluation | `model-manager.sh::load_env_safe()` (same pattern duplicated in `scarlix-mode::load_model_paths()`) |

## (e) Lock topology (`scarlix-mode`)

| Lock | FD | Path | Mode | Held by | Purpose |
|---|---|---|---|---|---|
| `scarlix-mode.lock` | 200 | `/run/scarlix/scarlix-mode.lock` (v18.7.2 P0: was `/tmp` — symlink attack) | exclusive (`-n`, non-blocking) for write modes; skipped for `status`/`vram`/`vram-check` | `scarlix-mode` | Serialize mode switches; the host-bridge serializes via `/var/lock/scarlix-host-bridge.lock` |
| `.models.lock` | 9 | `/var/lib/scarlix/.models.lock` | exclusive `-x -n` for write ops (ai/turbo/offline); shared `-s -n` for read ops (status/vram) | `scarlix-mode`, `model-manager.sh`, `download-models.sh` (all three share this lock) | Prevent races on `/models/` between mode switch + model pull |
| `scarlix-host-bridge.lock` | 200 | `/var/lock/scarlix-host-bridge.lock` (with `/var/lock` mkdir + `/run/lock` fallback v18.8.5 P1) | exclusive `-n` (exit 0 if already held — oneshot semantics) | `scarlix-host-bridge` | Prevent timer-overlap stacking |

`mkdir -p /run/scarlix` and `mkdir -p $(dirname "$MODELS_LOCK")` are fail-closed (v18.7.7 P1 / v18.7.8 P1) — was `|| true` which masked a missing dir as a cryptic flock failure under `set -e`.

## (f) systemd hardening (`scarlix-host-bridge.service`)

| Directive | Value | Rationale |
|---|---|---|
| `User=` | `root` | Needs nvidia-smi, docker, scarlix-mode |
| `NoNewPrivileges=` | `true` | Prevent setuid escalation from compromised child |
| `PrivateTmp=` | `true` | Isolate /tmp, /var/tmp |
| `ProtectHome=` | `true` | /home, /root, /run/user invisible |
| `ProtectKernelTunables=` | `true` | /sys/kernel invisible |
| `ProtectKernelModules=` | `true` | No `init_module`/`finit_module` |
| `ProtectControlGroups=` | `true` | /sys/fs/cgroup ro |
| `RestrictSUIDSGID=` | `true` | No setuid/setgid bit changes |
| `LockPersonality=` | `true` | No `personality()` change |
| `RestrictRealtime=` | `true` | No realtime scheduling |
| `RestrictNamespaces=` | `true` | No `unshare`/`setns` |
| `RestrictAddressFamilies=` | `AF_UNIX AF_INET AF_INET6` | Docker socket + network pulls only |
| `CapabilityBoundingSet=` | `CAP_SYS_ADMIN CAP_NET_RAW CAP_DAC_OVERRIDE CAP_KILL CAP_CHOWN CAP_FOWNER` | `CAP_CHOWN`/`CAP_FOWNER` added v18.8.6 P0-1 (was: host-bridge couldn't `chown` bridge-state/ → exit 1 → host-status.json stale → dashboard broken) |

Note: `ProtectSystem=strict` and `ReadOnlyPaths=/etc` are NOT used because `scarlix-mode` writes to `/var/lib/scarlix/current-mode` and `docker compose` needs `/opt/scarlix`.

## (g) `generate-env.sh` security

| Property | Value |
|---|---|
| `set -euo pipefail` | Fail on error, undefined var, pipe failure |
| `umask 077` effective | Achieved via `chmod 600 "$SECRETS_TMP"` (not umask — atomic write guarantees 600 even if umask differs) |
| Fail-closed on tmp write | `mktemp ... || exit 1`; `chmod 600 "$SECRETS_TMP" \|\| { rm -f; exit 1; }`; `chown ... \|\| { rm -f; exit 1; }`; `mv -f ... \|\| { rm -f; exit 1; }` |
| Token updates | `update_kv()` uses `awk` + `ENVIRON[]` (v19.0.1 P1-b — was: `awk -v v=...` re-processed backslashes + `$2=v` corrupted tokens containing `=`) |
| Required-key validation | `REQUIRED_KEYS="SCARLIHQ_TOKEN LITELLM_MASTER_KEY SMG_MASTER_KEY RESTIC_PASSWORD"` — exit 1 if any empty |
| Telegram token update | fail-closed (v18.9.5 P2-01 — was `\|\| true` masked failure) |

## (h) Experimental image supply chain — documented residual risk (v19.2.12, Deep Audit F-08)

The **default AI path is tag-pinned** (`sglang`, `vllm`, `ollama`, `llamacpp` compose files reference immutable `:tag` images, not `:latest`; since v19.4.2 `ollama` is additionally **digest-pinned** and all pins fixed in v19.4.2 — coolify, crowdsec, ollama, headscale, sunshine — carry digests, per `docs/DEPENDENCY_AUDIT_2026-10-21.md` §9). The following **experimental-only** services still reference `:latest` and are accepted as a **documented residual risk**:

| Service | Image | Scope | Risk accepted because | Last review |
|---|---|---|---|---|
| needle | `ghcr.io/cactus-compute/needle:latest` | `profiles: [experimental]` — never started by the default mode path | experimental opt-in only; NO_ACCESS note in tree (image unverifiable offline) | 2026-11-06 (v19.6.6 registry pass) |
| freetoken | `freetoken/serving:latest` | experimental profile | same | 2026-11-06 (v19.6.6 registry pass) |
| browser-mcp | `mcp/playwright-server:latest` | experimental profile | same | 2026-11-06 (v19.6.6 registry pass) |
| laya | `laya/decision-engine:latest` | experimental profile + single-GPU gate applies (device_ids 1) | same | 2026-11-06 (v19.6.6 registry pass) |

**v19.6.6 (the SK-audit §5.1 register + automation):** this table is
mirrored in `docs/DEPENDENCY_INVENTORY_2026-11-06.md` §3 (the GENERATED
inventory — drift-guarded by `tests/test_dependency_inventory.sh`) and the
unpinned `:latest` set is re-detected EVERY WEEK by
`scarlix-stale-audit.sh` (the stale-tree automation, §s below) — the
"NOT YET PINNED" comments from v18.8 no longer rely on memory alone. The
register date column is the review SLA: a new experimental image starts
its clock on the day it is added, and the weekly audit names every
unpinned image with its compose path. **v19.6.7 (SK-audit P2-03):** the
four entries below are now ALLOWLISTED with reasons and a review-by date
(`docker/image-pin-allowlist.conf`, §s) — the weekly audit reports them
as `UNPINNED-ALLOWED` (visible debt, not a finding) until **2027-01-31**,
after which an unextended entry reverts to a normal UNPINNED finding.
Pin-or-archive remains the real fix; the allowlist makes the debt
time-bounded instead of evergreen.

**Rule (enforced by review, not yet by CI):** any NEW service added to the DEFAULT path must be tag- or digest-pinned — `:latest` is permitted only under `profiles: [experimental]`. Digest pinning for the four services above is **deferred** (see README roadmap status table) until their registries can be reached from the release pipeline to record immutable digests. **v19.4.2:** tag EXISTENCE for the pins fixed in v19.4.2 is now CI-enforced — smoke check 07 runs online in CI since v19.4.2 (was `--offline`) and fails on a definitive registry 404 (audit §6.1 P0 detector; network errors stay WARNs); the experimental `:latest` set above is unchanged (unverifiable registries).

**Mitigation for operators:** do not enable `.experimental` / experimental profiles on hosts that cannot tolerate an unpinned image pull; Pi-Bolt-style SHA-256 verification does not apply to Docker image tags — only digest pinning does.

## (i) Pi-Bolt installer supply-chain pin (v19.2.12, Deep Audit F-01)

| Property | Value |
|---|---|
| Old behaviour | `curl -fsSL https://pi-bolt.opensec.in/install.sh \| sh` — remote script executed without verification |
| New behaviour | download to temp file → SHA-256 compared against `files/usr/local/share/scarlix/pi-bolt-install.sha256` (shipped with the release, also installed to `/usr/local/share/scarlix/`) → execute only on match |
| Mismatch handling | loud warning + skip (Pi-Bolt is optional); **never** silent continue, **never** execute an unverified installer |
| Pin refresh | deliberate manual action when upgrading to a newer Pi-Bolt installer — recompute the hash and commit the pin file with the release |
| Enforcement | smoke-test check 12 fails the release if the pin or the verification logic is missing |
| **v19.2.13 execution handoff** (KPK-19.2.12-P2-01) | the verified installer was a root-owned `mktemp` file (mode `0600`) executed via `sudo -u "$REAL_USER" bash` — the unprivileged user could never READ it, so the pinned install always failed at execution. Now the verified content is handed to a **user-owned private copy**: user-side `mktemp` as `$REAL_USER` + `install -o $REAL_USER -m 0600` from root (never world-readable; integrity preserved between verification and execution). Both temp copies are cleaned up on every success/failure/interrupt path (`EXIT`/`INT`/`TERM` traps). Smoke-test check 13 verifies the pattern and the runtime mechanic. |
| **v19.2.13 pin freshness** (KpK audit P2-01) | scheduled CI job (`.github/workflows/pin-freshness.yml`, weekly) re-downloads the installer and compares the hash with the shipped pin — drift fails the job. Pi-Bolt pin last verified: **2026-11-14**. |

## (j) Reservation-layer `CleanupVerified` semantics (v19.2.13, KpK audit P2-02)

An allocation reaching `RELEASED` implies **reservation-layer verified cleanup**: every
reservation bundle member's release required positive cleanup evidence at the reservation
layer (`CleanupVerifier.VerifyCleanup`), and `CompleteAllocationRelease` records
`CleanupVerified = true` **before** the terminal transition (frozen KPK-01 invariant).

**Hardware proof is the verifier implementation's responsibility.** In CI and unit tests the
verifier is a stub; a production `CleanupVerifier` (real `nvidia-smi` / port / container
state observation) is required before `RELEASED` can be treated as hardware-proven — that
wiring is part of Phase 5 / target-host E2E acceptance (`docs/PHASE3_G1_ACCEPTANCE.md`).

## (k) Host Bridge workload lifecycle (v19.3.1, P5.1+P6 unified)

The allowlisted privileged-operations path gained a typed workload control
surface (merged from the `19.2.14.P6` line):

- **Unix-domain socket only.** `hostbridge.ListenUnix` refuses TCP by design;
  the socket path must be absolute, the parent directory must be a real
  directory (not a symlink), owned by the service identity, and not writable
  by group/other; the socket is chmod 0600 and a pre-existing path is never
  unlinked (an admin must inspect it instead — an attacker-controlled socket
  cannot be silently replaced).
- **Two typed endpoints, nothing else.** `/v1/workloads/start` and
  `/v1/workloads/stop` accept a fixed JSON shape (unknown fields rejected,
  16 KiB request cap); the executor interface never sees a shell, an
  arbitrary executable, an image, environment or path input.
- **Service hardening:** `scarlix-workload-bridge.service` runs as
  `User=65532` with the docker supplementary group (host-root-equivalent —
  hence the narrow API), `NoNewPrivileges`, `ProtectSystem=strict`,
  `PrivateTmp`, `PrivateDevices`, `ProtectHome`, `CapabilityBoundingSet=`
  (empty), `RestrictAddressFamilies=AF_UNIX AF_INET`, disabled by default
  with only a template config installed.
- **Fail-closed lifecycle:** uncertain workload starts persist into
  `CLEANUP_REQUIRED` / typed `ErrReconciliationRequired` errors — never a
  silent success; idempotency is ledgered (`hostbridge.Ledger`).
- **The ScarliHQ container still has no docker.sock.** All Docker access
  happens host-side inside the bridge binary.

## (l) Gaming stack surface (v19.3.1)

- **Read-only by construction:** `GET /api/gaming` is auth-gated and
  GET-only; `internal/gaming` is a pure data contract with no exec/docker
  imports (enforced by smoke-test check_16). ScarliHQ detects nothing itself
  — the host bridge does the probing and publishes through
  `host-status.json`.
- **Installer safety:** `scarlix-gaming-install` never removes packages
  (smoke-test check_15 rejects removal commands in executed positions),
  never runs AUR builds as root (fails closed with guidance), reports
  per-package failures with a non-zero exit when anything failed, and is
  idempotent (`--needed`).
- **Honest health reporting:** undetectable states surface as `unknown`,
  libraries as `not_applicable` — the dashboard never guesses. A launcher
  being installed never implies any game is installed.

## (m) Reservation governor (v19.3.2, F-04)

- **Off the HTTP plane:** `scarlix-reservation` has no socket, no API, no
  listener (smoke-test check_17 rejects listener patterns in the binary
  source). It reconciles — it cannot be driven remotely at all. Workload
  start/stop stays on the workload bridge (k); future Reserve/Release intake
  is a separately designed, separately reviewed surface.
- **Docker group, read-only consumer:** like the workload bridge, the
  governor runs with `SupplementaryGroups=docker` (host-root-equivalent) but
  only ever executes `docker ps --filter label=scarlix.managed=true` for
  inventory + cleanup verification. It performs **no docker mutations**.
- **Config trust:** `/etc/scarlix/reservation.json` must be root-owned,
  0640-class (group/other-writable rejected, world-readable rejected — it
  carries tokens), non-symlink, with a non-group/world-writable root-owned
  parent. The reconciler token is a **separate secret** from
  `SCARLIHQ_TOKEN` (KPK precedent: reconciler identities are separately
  trusted); the loader rejects user tokens claiming the reconciler principal
  and tokens shorter than policy minimums. The shipped template contains
  only `CHANGE_ME` placeholders that intentionally fail validation.
- **Fail-closed release semantics:** docker unavailability during cleanup
  verification is an **error** (retried next tick — nothing is released on a
  maybe); unmappable/unknown resource IDs produce `Verified=false` evidence
  → the reservation converges to **CONFLICT** (blocks, operator-visible),
  never a silent `RELEASED`. A corrupt store aborts startup; the unit parks
  in failed state after bounded restarts.
- **Store hygiene:** reservations + allocations live under
  `/var/lib/scarlix-reservation/` (StateDirectory 0700, files 0600, atomic
  rewrites, flock). `pruneReleased` stays unwired by choice (audit-trail
  retention). The governor never deletes user data.
- **Enablement is an operator decision:** install.sh copies binary + unit +
  template but never enables the service (smoke-test check_17 asserts the
  absence of any `systemctl enable scarlix-reservation`).

## (n) Agent Kernel — COMPLETE (v19.4.x, AK-1…AK-5)

- **Closed capability enumeration — never "exec":** the only grantable
  capabilities are `status.read`, `gaming.read`, `mode.request`,
  `compute.plan`, `compute.reserve`, `compute.release` (mirrors of existing
  allowlisted surfaces). There is no `shell.exec`, no docker passthrough, no
  arbitrary file access — strategy p. 22 ("Žiadny voľný root shell").
  Unknown capabilities are rejected at `NewCapabilitySet` construction;
  extending the enumeration is a deliberate reviewed act.
- **No AI-decided permissions:** `Policy.Evaluate` is pure deterministic rule
  evaluation (no LLM in the decision path — the LLM layer proposes, policy
  decides). Rules in fixed order: session-required → session-owner →
  session-state → capability-unknown → capability-not-granted →
  capability-disabled → grant. Every decision carries a reason
  ("dôvod výberu aj odmietnutia", strategy p. 13).
- **Grant check is fail-closed:** the agent's `CapabilitySet` is enforced by
  the engine itself — a nil or empty set denies everything. (The AK-1 branch
  draft omitted this check — caught in self-review before merge and fixed;
  recorded in `docs/AGENT_KERNEL_AK1_DESIGN.md`.)
- **No anonymous agents:** every `AgentIdentity` carries an owner principal
  (accountability ties into the governor/ScarliHQ principal space); agent IDs
  are typed and validated (`kind.name`, `[a-z0-9-]`, ≤64 chars).
- **Bounded sessions:** `CLOSED` is terminal; illegal transitions and
  backwards timestamps are rejected — the same fail-closed state-machine
  discipline as allocation/reservation.
- **Not reachable by default (AK-2, v19.4.1):** the REST surface
  (`/api/agent/sessions` create/list/transition/evaluate) is **opt-in**
  (`SCARLIHQ_AGENTKERNEL_ENABLED=true`, 503 until configured, commented
  compose block) — same enablement contract as the compute lifecycle. When
  enabled, every route is behind the standard Bearer auth middleware
  (constant-time compare, no URL-token fallback); the trust boundary is the
  authenticated ScarliHQ principal. Grants are request-supplied in AK-2 —
  AK-3 replaces this with admin-owned persisted grants tied to the
  governor's `user_tokens` principal space.
- **Bounded, fail-closed session store:** default 128 records (the cap
  counts CLOSED records too — honest, leak-free bound); session IDs are
  `aks-` + 16 crypto-random bytes with RNG failure aborting creation
  (v18.7.4-P0 precedent); a nil/empty capability set is rejected at
  creation (nothing granted implicitly); reads return deep copies (no
  grant mutation through returned records); evaluation runs under the store
  lock — a decision can never be attributed to a state that no longer
  exists.
- **Denial-is-200 evaluate contract:** a denial is a successful, auditable
  evaluation — the API returns 200 with `{allow:false, reason}` rather
  than an HTTP error; unknown sessions (404) and unknown capabilities
  (400) are client errors, not kernel decisions. Every decision carries the
  fired rule / cited grant.
- **Admin-owned persisted grants (AK-3, v19.4.4):** in registry mode every
  grant is a separate persisted admin act — `/var/lib/scarlix-agentkernel/
  grants.json` (0600 file / 0700 dir, atomic temp+rename writes, tombstone
  revocations; the file is the reviewable history and re-granting appends,
  never overwrites). Grants name their admin (`granted_by` mandatory — no
  anonymous grants); one agent has exactly one owner principal, enforced
  across all records; owner principals must be part of the declared
  `SCARLIHQ_AGENT_PRINCIPALS` space (copied from the governor `user_tokens`
  values — fail-closed on unknown principals at grant AND load time).
  **Fail-closed load:** any malformed record, unknown capability, owner
  mismatch, anonymous revocation, duplicate active grant, schema drift, or
  loose file/dir permissions aborts startup — a tampered registry never
  degrades into "some grants work". Unlike the allocation store, loose
  permissions are NOT auto-fixed (tamper discipline). The registry is
  bounded (256 records incl. tombstones, 64 agents) and request-supplied
  `capabilities` are rejected in registry mode (grants are never
  client-supplied there).
- **Admin kill-switch (AK-3):** `Disable`/`Enable` per capability (each act
  names its admin); a disabled capability is denied citing
  `rule/capability-disabled` and the toggle takes effect on the NEXT
  evaluation (no restart — evaluate consults the live registry policy).
  The grant surface (`/api/agent/grants`, `/api/agent/grants/revoke`,
  `/api/agent/capabilities/disable|enable`) is behind the standard Bearer
  auth and 503 until the registry is opted in (`SCARLIHQ_AGENT_GRANTS_FILE`
  + `SCARLIHQ_AGENT_PRINCIPALS`, commented compose block).
- **Append-only audit sink (AK-4, v19.4.5):** every kernel decision (allow
  AND deny) and every admin act (grant/revoke/disable/enable) is persisted
  to `/var/lib/scarlix-agentkernel/audit.jsonl` — append-only JSONL, 0600
  file / 0700 dir (construction fail-closes on looser modes, umask-proof
  re-stat), flock-serialized multi-process writes (reservation-store
  discipline), flush + fsync per line (a crash can lose at most the line
  being written, never a torn line). **Evaluations are persisted BEFORE the
  decision is served** — an audit write failure returns 500 and no decision
  (an unauditable allow/deny is never served). Admin acts append after
  their persist; write failures are reported (honest ordering — the registry
  state stands). Lines validate before write (type-specific mandatory
  fields); reads are bounded (last 200 by default) and malformed lines are
  reported in the API response (`audit_read_errors`), never silently
  skipped. Read surface `/api/agent/audit` is read-only behind Bearer auth;
  opt-in via `SCARLIHQ_AGENT_AUDIT_FILE`.
- **Agent-gated compute intake (AK-5, v19.4.6):** `POST /api/agent/sessions/
  compute` chains three independent gates — kernel evaluation (denial-is-200;
  registry kill-switch applies on the next call), principal mapping (the
  agent's registry-declared owner — the registry is REQUIRED for intake: a
  legacy-mode caller-supplied owner would make the mapping spoofable, so
  intake returns 400 without it, never a silent fallback), and workload-
  bridge ownership enforcement (Start/Stop with the agent owner as
  principal; unowned allocations → 403 ErrOwnerMismatch). Every attempt is
  audited with the allocation id and outcome (`compute.reserve` /
  `compute.release` lines, detail = executed / rejected:<kind>); audit
  write failures are surfaced, never silent. Bounded request discipline
  (4KB, one object, unknown fields rejected) + 45s bridge timeout parity
  with the direct lifecycle surface.

## (o) Universal AI Runtime foundation (v19.6.0, CF-1 — Phase C OPENS, unwired)

- **Closed local-stack enumeration:** the provider set in
  `scarlihq/internal/airuntime` is `sglang`/`vllm`/`beellama`/`ollama`
  ONLY, in the v19.0.6-locked 4-tier failover order (`DefaultFailoverOrder`).
  There is deliberately no openai/anthropic/azure — the runtime normalizes
  the LOCAL sovereign stack and is not a cloud-LLM gateway; cloud names,
  ports, paths, whitespace and control characters are rejected at
  validation (tested). Extending the enumeration is a reviewed act
  (`AllProviders` + `Tier` + the order + tests change together).
- **Network/IO-free by construction:** no `net/http`, no sockets, no file
  access, no exec anywhere in the package — runtime AND tests. Smoke check
  28 scans the source **comment-stripped** (prose that says "no net/http"
  cannot mask a real import — the scan fires on code, not prose; verified
  negative control). The cloud-token scan covers the runtime files — the
  tests legitimately assert rejection, they are the proof, not the
  violation.
- **Fail-closed health + routing:** UNKNOWN health never serves (never
  observed ⇒ never routed — trust is earned by observation); observation
  time is monotone (a stale probe cannot overwrite a newer one, rejected
  by name); the router never guesses a model (`ErrUnknownModel`) and
  never returns an empty plan as success (`ErrNoRoute` — the named skips
  return alongside the error: fail-closed, not fail-blind). The router is
  PURE: no clock, no randomness, no network, no LLM — and it never
  executes anything (execution is the CF-2 adapters' job, behind the
  fabric's approvals).
- **Unwired = no new attack surface:** CF-1 deliberately adds no HTTP/MCP
  surface, no persistence, no compose env, no main.go wiring (the AK-1
  precedent) — `main.go` and `docker-compose.yml` carry no `airuntime`
  reference; smoke check 28 enforces the unwired contract. Nothing
  user-facing changes in v19.6.0.
- **Bounded normalized contract:** chat content ≤ 4096 runes, ≤ 32
  messages, closed roles (system/user/assistant — no tool role yet),
  closed finish reasons (stop/length — deliberately no "error" value: a
  failed generation is an adapter error, never a response shape),
  non-negative usage, and every response carries provider attribution +
  a required UTC timestamp (attribution survives failover).

## (p) Universal AI Runtime transport (v19.6.1, CF-2 — Phase C, opt-in)

CF-2 wires the pure CF-1 contract to the real local stack through a
SEPARATE transport package (`internal/airt`) — the network lives behind
the contract, never inside it (smoke check 28 now scans the WHOLE
`internal/airuntime` package comment-stripped; check 29 scans the
transport's own markers). The security posture:

- **The cloud-gateway guard is structural:** endpoints validate http-only
  (no TLS anywhere in the local stack), no userinfo, explicit port, no
  path/query/fragment — and the host must be LOOPBACK or a SINGLE-LABEL
  docker service name. A dotted host is the cloud-gateway shape, rejected
  BY NAME, before any cosmetic rule fires. The runtime cannot be pointed
  at a public endpoint even by a misconfigured env override: an invalid
  override aborts startup (fail-closed, never a silent fall-back).
- **No new listening surface:** /api/ai/* rides the existing
  Bearer-gated ScarliHQ listener (8090) — nothing in `internal/airt`
  listens; the adapters only DIAL OUT to the documented local stack
  (smoke check 29 asserts no listener in the transport). All five routes
  sit behind `h.auth` and answer 503 until
  `SCARLIHQ_AIRUNTIME_ENABLED=true` configures the runtime (the same
  opt-in contract as every surface since v19.3.2).
- **Bounded transport, bounded answers:** 30s per adapter attempt (the
  ai/smg timeout precedent; v19.6.3: operator-tunable through
  `SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT` within the 300s named upper bound),
  5s per probe, the response body read through a 1 MiB limit (a runaway
  response must not eat memory — the content itself is bounded to 4096
  runes by the contract), the chat request body bounded to 1 MiB with
  unknown-fields-rejected decode (the full legitimate 32×4096-rune range
  stays reachable).
- **v19.6.3 (KPK-01) + v19.6.4 (KPK-03) — the local-only client is closed at
  THREE layers, and the dial layer validates DESTINATIONS, not just names:**
  the ONLY `http.Client` this package constructs (`airt/transport.go`,
  asserted by smoke check 31) follows NO redirects (`http.ErrUseLastResponse`
  — a 307/308 is handed back as the response and classified as the failure
  it is: a compromised or misconfigured tier cannot bounce the conversation,
  the POST body is the private prompts, onto any `Location` it answers),
  inherits NO ambient proxy (`Proxy: nil` — `HTTP_PROXY`/`HTTPS_PROXY` never
  steer local AI traffic; proxy support is not a feature of this transport
  and cannot become one by accident), and DIALS only loopback or the
  configured endpoint hosts (the hostname allowlist). Redirect and proxy
  regression tests assert ZERO requests and ZERO prompt bytes at the
  external destination.
  **v19.6.4 (KPK-03) closes the hostname-vs-address gap the v19.6.3 audit
  named:** a configured NAME is RESOLVED through the process's own resolver,
  EVERY answered address is validated against the trusted local networks,
  and the connection is PINNED to a validated address — the IP that is
  dialed is the IP that was validated, no validate-then-resolve TOCTOU gap.
  A MIXED answer set (one approved + one foreign address) is refused WHOLE
  (fail-closed, never filtered); an empty or failed resolution is refused
  fail-closed. Loopback (`127.0.0.0/8`, `::1`) is ALWAYS trusted; the
  non-loopback trust defaults to Docker's DOCUMENTED default address pool
  ENUMERATED as fifteen canonical /16 networks (`172.17.0.0/16` …
  `172.31.0.0/16`, `dockerDefaultPoolCIDRs` — v19.6.6 below) and is
  REPLACED by an operator
  declaration — `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS=172.18.0.0/16,10.0.0.0/8`
  (comma-separated CIDRs, validated fail-closed at startup; deliberately
  NOT the whole RFC1918 space: `10.0.0.0/8` and `192.168.0.0/16` are out
  until declared). A service-name endpoint with loopback-only trust fails
  AT CONSTRUCTION, never per-request. Residual, stated honestly: the
  resolver itself (Docker DNS / host resolv.conf) is trusted only to
  ANSWER — a compromised resolver can no longer steer traffic anywhere it
  wants, only into the declared networks or into a refusal.
  **v19.6.6 (KPK-19.6.5-P2-01) makes the DEFAULT literal-honest:** the
  v19.6.5 shorthand `172.17.0.0/12` was a NON-canonical network address —
  parsing it MASKS to `172.16.0.0/12`, which silently trusted
  `172.16.0.0/16` (not part of Docker's documented default pool, which
  begins at `172.17.0.0`; commonly corporate-VPN/overlay space) — the same
  class of trust-scope mistake the home-LAN removal fixed, in a different
  private range. The default is now the ENUMERATED pool: fifteen CANONICAL
  /16 entries, each parseable unchanged, the trust range stated IS the
  trust range enforced; `172.16.0.1` and `172.32.0.1` are rejected by
  default (regression-tested by name), every user-defined network carved
  from the default pool still works. The SK-audit P2-01 guidance is wired:
  service-name endpoints with UNSET `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS`
  raise a loud STARTUP WARNING carrying the exact subnet-derivation
  command — an operator whose scarlix-net lives outside the pool declares
  it BEFORE production, explicit, visible, auditable.
  **v19.6.5 (KPK-19.6.4-P2-01) narrows the DEFAULT to what it actually is:**
  the implicit trust is the Docker default bridge pool ONLY (v19.6.6
  above: the enumerated canonical /16s) — the home-LAN range
  `192.168.0.0/16` was REMOVED from the default
  (the independent v19.6.4 audit: it is not a per-deployment Docker
  boundary, it covers every `192.168.x.x` destination — home routers, NAS
  devices, workstations — and scarlix-net is an EXTERNAL network whose
  subnet this repository cannot pin, so the code cannot infer that a
  `192.168.x.x` address belongs to this deployment; a poisoned resolver
  mapping an approved name to `192.168.1.1` used to pass the address
  policy). Default trust now rejects `192.168.1.1`, `192.168.0.1` and
  every other unrelated LAN destination (regression-tested); an operator
  whose scarlix-net lives in the LAN range DECLARES it explicitly — obtain
  the actual subnet with `docker network inspect scarlix-net` and set
  `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS=<that subnet>` — explicit, visible,
  auditable, never implicit. A UNIVERSAL route declaration
  (`0.0.0.0/0`, `::/0`) is REFUSED at startup — a declaration that would
  nullify the destination boundary is a configuration error, not a posture
  (the independent audit's hardening note, adopted as policy). The
  Dual-stack note (the SK-audit P2-03 residual, documented): a
  dual-stack service name may answer A + AAAA records; an unapproved
  IPv6 answer refuses the set WHOLE (fail-closed) — on dual-stack
  Docker networks declare the IPv6 pool in
  `SCARLIHQ_AIRUNTIME_TRUSTED_CIDRS` (e.g. `fd00::/64` alongside the
  IPv4 subnet) or prefer IPv4 literals in the endpoints.
  Residual, inherent and accepted (documented at tag): lateral movement
  INSIDE a declared trusted network (a compromised resolver returning a
  different container/host in the SAME declared subnet still passes) —
  CIDR trust cannot distinguish hosts within the declared net; the
  operator narrows the declaration to the smallest actual subnet, and a
  per-service expected-IP pin remains an optional CF-4+ item.
- **v19.6.3 (KPK-02) — one end-to-end timeout contract:** the budget for
  one AI request is per-attempt × the closed four-tier set
  (`airt.ChatBudget` — the complete honest failover walk; sequential
  attempts SHARE it, never an independent per-tier allowance). The API
  layer imposes it as the request context deadline, every adapter and
  probe operation propagates it, its expiry is reported as a DELIBERATE
  504 (`airt.ErrBudgetExpired`, attempts riding along — never a socket
  write deadline artifact, never a generic 502 masquerade), and the
  `/api/ai/chat|propose` write path extends beyond it with a 10s margin
  (`http.ResponseController`) so a legitimate slow local generation is
  WRITTEN, never cut by the server's 30s default WriteTimeout (which keeps
  serving every other surface unchanged).
  **The named maximum wall clock (v19.6.4 documentation, the SK-audit
  P2-03 follow-up):** one `/api/ai/chat` or `/api/ai/propose` request may
  occupy its handler for at most `4 × min(SCARLIHQ_AIRUNTIME_CHAT_TIMEOUT,
  300s)` = **20 minutes** at the widest setting, plus the 10s write margin
  — bounded, but operationally long: concurrent chat occupancy is bounded
  by auth (who may call) and the four-slot sequential walk, not by a
  dedicated concurrency limit; monitor and rate-limit at the operator's
  discretion (a CF-4 audit-dashboard item).
- **v19.6.4 (KPK-04) + v19.6.5 (KPK-19.6.4-P2-02) + v19.6.6
  (KPK-19.6.5-P2-02) — a cancelled probe round is not evidence about the
  providers, the recording is ATOMIC, and the context check lives UNDER
  THE LOCK:** v19.6.6 moved `ctx.Err()` from before `t.mu.Lock()` to
  immediately after acquiring it — a cancellation arriving while
  `ObserveBatch` was BLOCKED on a contended lock used to slip its batch
  through the waiting window; the linearization point is now the successful
  context check under the lock (deterministic lock-contention cancellation
  + deadline tests with the live-context counterweight). the
  operator closing the browser, a reverse proxy aborting the request, or a
  round deadline expiring mid-probe used to convert `context.Canceled` into
  UNAVAILABLE observations and OVERWRITE all four providers'
  last-known-good health — a caller-controlled act could mark the whole
  local AI stack dead and force false no-route responses until the next
  successful round repaired the table. Now: a probe round whose parent
  context is done ABORTS (the context error is returned, `errors.Is`
  identifiable), `ProbeAllAndObserve` records NOTHING, and the health
  table's last-known-good states AND timestamps stand. Genuine
  per-provider failures (refused, timed out, unreachable) are recorded
  exactly when the parent context is live; the background loop's round
  budget carries a 2x margin over the per-probe timeout so a slow tier is
  classified by the probe's OWN timeout (UNAVAILABLE, genuine), never
  ambiguously by the round clock.
  **v19.6.5 (KPK-19.6.4-P2-02) closes the check-to-write race the
  independent v19.6.4 audit found:** the v19.6.4 round-end guard checked
  the context AFTER the probes but then wrote observations one-by-one
  (`table.Observe` in a loop) — a cancellation arriving in the gap between
  `ProbeAll` returning and the first write still landed its observations,
  and a future mid-loop cancellation could land a PARTIAL round. Now the
  whole round commits through `HealthTable.ObserveBatch` — every
  observation validated, the batch monotonicity-checked, and the writes
  applied under ONE lock; the caller's context is re-checked AT THE COMMIT
  BOUNDARY immediately before any write. The linearization rule, stated
  once: cancellation BEFORE the batch commit means ZERO writes; cancellation
  AFTER the commit does not retroactively undo a completed observation
  round. A deterministic test seam (`Prober.preCommit`) pins a cancellation
  into exactly the post-probe/pre-commit window and proves the boundary
  refuses it; concurrent racing rounds are proven monotone and never
  partially applied (a stale round is discarded whole).
- **AI Runtime REST auth ≠ inference port auth:** the Bearer gate protects
  the ScarliHQ REST surface (8090) ONLY — the inference ports themselves
  (30000/8089/11438/11435) authenticate nobody by design (the documented
  local stack). The ports remain localhost / scarlix-net ONLY: keep the
  compose `127.0.0.1:` binds and the host firewall as invariant — anyone
  who reaches `scarlix-net` or the host ports bypasses ScarliHQ's gate and
  talks to the models directly. Never add cloud keys to the adapter "for
  convenience".
- **Fail-closed normalization at the wire boundary:** exactly one choice;
  the response role must be assistant; finish reasons only stop/length —
  unknown finish reasons are adapter ERRORS the plan skips past, never
  coerced values; absent usage is zero accounting, negative usage fails;
  content must survive Message validation on real transport answers.
- **Failover is honest, not silent:** the executor walks the pure CF-1
  router's plan (replanned from live state every request); the first
  answering tier's attribution rides the response, every failed step is
  an Attempt{provider, tier, role, error} — the caller SEES the failover
  (fail-closed, not fail-blind). ErrNoRoute carries the named-rule skips;
  ErrAllAdaptersFailed carries the attempts.
- **Construction is closed:** the adapter set must cover EXACTLY the
  four-provider local stack (a missing tier would be a silent fail-open
  hole in failover — rejected by name); the prober set is complete;
  requests are deep-copied to adapters (copy isolation, tested in both
  directions — an adapter cannot corrupt a caller's request).
- **Chat mutates nothing on the host:** it is transport to local model
  servers, read-only from the host's perspective. An LLM response
  becoming a fabric PROPOSAL is CF-3, behind the fabric's operator
  approvals — the LLM never decides anything here ("LLM navrhuje,
  politika rozhoduje").
- **No persistence in CF-2 — the registry is NOT durable until the
  CF-4/persistence decision:** it is in-memory, rebuilt at boot from
  `SCARLIHQ_AIRUNTIME_MODELS` + operator POSTs — a malformed bootstrap
  declaration aborts startup (the runtime never boots on a guessed
  registry). An operator enabling the runtime must ALWAYS declare the
  models in the env (restart = the registry restores from env + operator
  acts; registrations do not survive). No new files, no new mounts, no new
  env secrets.

## (q) Universal AI Runtime propose surface (v19.6.2, CF-3 — Phase C, both opt-ins)

CF-3 is the bridge the whole Phase C exists for: an LLM response becomes
a fabric PROPOSAL — never an auto-submitted command. The security
posture is the BF-4 genre applied to model-authored intents:

- **The LLM never holds a pen that writes into the fabric.** The propose
  endpoint (`POST /api/ai/propose`) captures a well-formed proposal block
  as a PENDING record and NOTHING else — no routing, no kernel decision,
  no execution, no approval request. The ONLY door from a proposal to a
  command is the operator's explicit accept; everything after it is the
  EXISTING fabric flow (the fresh kernel evaluation, the BF-4 approval
  gate for coding acts, the audit-first ordering — untouched). A
  dismissal never becomes a command at all.
- **The parser is fail-closed, never a guess:** exactly ONE fenced
  `scarlix-proposal` block (column-0 fence — a loose fence would match
  code samples ABOUT proposals), the closed three-field shape with
  unknown fields rejected, every field required (no defaults — the
  parser never fills in a blank agent or guesses a capability), values
  validated with the BAR'S OWN validators (no second, weaker standard
  for model-authored intents). Zero blocks → the honest null; TWO
  blocks → malformed, refused (ambiguity is never resolved by guessing).
  A malformed block captures NOTHING — 422 with the named reason + the
  provider/model attribution + the refused content.
- **The proposal is held to the store discipline:** proposals.json rides
  the fabric opt-in (default next to commands.json, same 0700 directory,
  same tamper discipline — 0600/0700 checked at load AND before every
  persist, fail-closed load, atomic persist + rollback, bounded 256,
  never deleted — the reviewable history). The operator label is
  attribution, never a credential (the bearer token remains the only
  authentication boundary).
- **The linkage is one-way and one-shot:** the durable `proposal_id`
  field on the command is additive (`omitempty` — the commands schema
  stays v1, BF-1..BF-4 files load unchanged); ONE command per proposal,
  enforced on the mutation side AND at load (a hand-edited duplicate is
  corruption, not "some proposals work"); accept's reconciliation
  (GetByProposal) closes the crash window — no intent is captured twice
  through that door. Accept carries NO params — the accepted thing is
  the proposed thing (no re-steering; the BF-4 rule, applied at capture).
- **Both opt-ins, fail-closed:** the propose surface answers 503 without
  the AI runtime (`SCARLIHQ_AIRUNTIME_ENABLED=true`) AND without the
  fabric's proposal store (`SCARLIHQ_AGENTFABRIC_ENABLED=true`) — the
  runtime alone never captures anything; the fabric alone has nothing to
  parse. All four routes sit behind `h.auth` on the existing
  Bearer-gated listener; no new ports, no new mounts.
- **Chat-side bounds carry over unchanged:** the propose request is the
  chat request shape (1 MiB bound, closed decode); the response content
  is bounded to 4096 runes by the CF-1 contract before the parser ever
  sees it; the parse itself is pure text processing — no network, no
  clock, no randomness in `ParseProposal`.

## (r) Family profiles — typed schema, strict load, identity-bound enforcement (v19.6.7, KPK-19.6.5-P1-03 + KPK-19.6.6-P1-01)

The repository's four family profiles (`profiles/{hugo,mon,xox,zmor}.yaml`)
are POLICY-BEARING files: permissions, `scarlix_mode` (default/allowed/
can_switch), the dual-shape `token_budget` (`unlimited` scalar for the
admin, `{monthly, model}` mapping for family members) and XOX's
`content_filter` (enabled + blocklist). The v19.6.5 defect: the Go loader
modeled a STRING `token_budget` and five display fields — the mapping
shape FAILED `yaml.Unmarshal` for three of four profiles and the loader
SILENTLY returned filename-only fallbacks (display name, role, theme and
every policy field discarded), while the policy fields were represented
NOWHERE and enforced NOWHERE — "if these profiles are intended to enforce
user permissions, the current behavior is a security boundary failure,
not just a display bug" (the audit's words).

The v19.6.6 contract (schema + strict load — unchanged, the foundation):

- **The schema is typed and total** (`internal/profiles/loader.go`):
  `Permissions`, `ModePolicy`, `TokenBudget` (custom unmarshaler — the
  two honest shapes load typed, every other shape is refused BY NAME;
  the NESTED keys are validated by hand — unknown/duplicate nested keys
  are named errors, KPK-19.6.6-P3-04 closed in v19.6.7), `ContentFilter`.
  Validation is closed: role ∈ {admin, family}; the `all` wildcard is
  ADMIN-only; `scarlix_mode.default` must be a member of `allowed`;
  `can_switch=false` allows exactly ONE mode; an enabled content filter
  requires a non-empty blocklist; duplicate permissions are refused.
- **The load is STRICT**: `yaml` `KnownFields` — unknown keys are ERRORS
  for policy files (a typo'd `permissons:` silently dropping a permission
  is the accident class the audit demanded eliminated). A parse or
  validation failure is a named `Issue` — the profile is EXCLUDED from
  `List()` and SURFACED in the API response (`/api/profiles` returns
  `{"profiles": [...], "invalid": [...]}`); `Load`/`Get` return the
  error. **Never a silently permissive fallback.**

The v19.6.7 contract (KPK-19.6.6-P1-01 CLOSED — identity is bound to
authentication; `internal/profiles/principals.go` +
`internal/api/principal.go`):

- **Per-profile Bearer tokens.** Every profile MAY carry its own token —
  the index loads from `/etc/scarlix/profile-tokens.conf` (one
  `profile:token` per line) or inline
  `SCARLIHQ_PROFILE_TOKENS=name:token,...`. Load is fail-closed: a
  group/world-readable file is refused (secrets are not ambient — 0640
  or tighter), a malformed line is refused with its line number, token
  COLLISIONS between profiles are refused (one Bearer authenticates
  exactly one identity), and a profile token equal to the household
  `SCARLIHQ_TOKEN` is refused by name. Resolution is constant-time per
  entry (`crypto/subtle`), entries iterated sorted. **The household
  `SCARLIHQ_TOKEN` resolves to the ADMIN principal** — the operator's
  own identity, never a family member's; a `role:admin` profile's own
  token is admin too.
- **ONE shared authorization function.** REST `/api/mode`,
  `/api/ai/chat` AND MCP `scarlix_mode_set` all decide through exactly
  `profiles.Authenticator.Authorize(principal, declared, action)` — one
  decision function, not independent optional checks (the audit's
  requirement 4). The auth middleware (REST and MCP) resolves EVERY
  request's principal from the presented Bearer and stores it in the
  request context; the 401/503 semantics for the household token are
  byte-for-byte the legacy contract.
- **A DECLARED reference must match the principal.** In multi-user mode
  a client profile reference that is not the principal's own profile is
  refused 403 `ErrProfileMismatch` ("principal %q cannot select profile
  %q") — a family token cannot select `zmor`, nor another family
  member's profile; the `X-Scarlix-Profile` header is checked
  identically to the body field (no entry path overrides the
  authenticated principal). An unloadable DECLARED file stays the
  400-class fail-closed (the v19.6.6 contract, preserved).
- **An OMITTED reference is DERIVED from the principal** — there is NO
  policy-free path in multi-user mode: the family member gets HER
  policy (permissions, mode policy, content filter, budget), the
  household token derives the `role:admin` profile (a decidable admin
  path — no admin profile in multi-user mode is a named configuration
  defect, refused 403 `ErrProfileUnavailable`; a principal whose OWN
  file is broken is refused 403 identically — never unrestricted).
- **An admin act-as only REDUCES privileges.** The household (or zmor)
  token may declare a family profile — and the TARGET profile's policy
  still applies in full (the audit's requirement 5: admin access is
  restricted to the admin principal; a family principal never
  self-selects admin).
- **Operator surfaces require the ADMIN principal** (`authAdmin` in
  `rest.go`): compute start/stop, agent sessions/grants/audit (and the
  agent compute intake), bar commands/workspaces/executions/approvals,
  ai probe, ai propose/proposals/accept/dismiss, and the POST model
  registration on `/api/ai/models` (the GET list stays family-readable).
  A family member's per-profile token is refused 403 there — "identity
  is bound, not self-declared." Read-only surfaces (health, gpu,
  containers, status, profiles, ws-ticket, ai health/models-list, MCP
  read-only tools) serve family tokens.
- **Audit events, never tokens** (the audit's requirement 6): every
  authorization decision is logged — `AUDIT authz principal=…
  profile=… declared=… action=… decision=ALLOW|DENY reason=…` —
  principal, resolved profile, declared reference, action, decision,
  reason; NEVER a Bearer token.
- **Single-user deployments are named, not mistaken for isolation**
  (the audit's requirement 7). Without a token index the household
  token is the ONE admin identity: `SCARLIHQ_SINGLE_USER=true` logs the
  explicit single-user mode; the implicit default logs the same truth
  with a pointer to the isolation guide (create
  `/etc/scarlix/profile-tokens.conf`). When an admin profile exists its
  policy applies (equivalent privileges, but decidable and auditable);
  deployments with NO profiles at all keep the legacy unrestricted path
  — named exactly as that in the audit event, never silently.
- **Policy enforcement stays at the ACTION BOUNDARY, now decided
  against the AUTHENTICATED profile** (the v19.6.6 contract carried
  over): mode switching (`/api/mode` + MCP `scarlix_mode_set`)
  enforces `can_switch=false` (XOX cannot switch AT ALL — 403 REST /
  JSON-RPC −32005, the refusal names the policy) and the allowed-set
  membership (refused BY NAME with the allowed set in the message); an
  identity mismatch refuses 403 REST / −32006 MCP (`ErrProfileMismatch`).
  `/api/ai/chat` enforces
  the `ai_chat` permission (403 with the declared set riding along)
  and an ENABLED content filter ON THE CHAT INPUT — a blocklisted
  category hit refuses the request with the category named
  (keyword-level, case-insensitive substring; a SEMANTIC classifier is
  a CF-4+ item with the local model — honestly documented, never a
  silent claim).
- **The derived admin policy must not restrict the operator**:
  `profiles/zmor.yaml` `scarlix_mode.allowed` now carries the FULL
  enumeration (`ai, stop, game, creative, turbo, offline, tv` — was 4).

The v19.6.6 residual — "the household Bearer token remains the only
authentication boundary; the profile reference is the CALLER'S
DECLARATION" — is CLOSED: the declaration is now CHECKED against the
authenticated principal, and the omitted-reference path is DERIVED, not
unrestricted. What remains honest: the content filter is keyword-level
(a semantic classifier is a CF-4+ item with the local model), hardware
acceptance stays OPEN (KPK-06), and the profile policy governs the
surfaces that decide through `Authorize` (mode, chat, the read-only
views) — the token budget covers the chat path only (§t; the propose
path is admin-gated above).

Acceptance matrix — the v19.6.6 audit's own list, now executable proofs
(`internal/api/principal_test.go`, 12 REST tests;
`internal/mcp/principal_test.go`, 6 MCP tests):

| Request | Result |
|---|---|
| hugo token + `profile=zmor` (body or `X-Scarlix-Profile` header) | **403** `ErrProfileMismatch` — the header cannot override the principal |
| xox token + `profile=zmor` | **403** |
| hugo token + `profile=mon` / `profile=xox` | **403** — family identities mutually isolated |
| zmor token + `profile=zmor` | **allowed** (its own policy) |
| family token, reference OMITTED | **derived** — HER filter/mode policy/budget still refuse (xox: blocked category 403, `can_switch=false` 403) |
| household token act-as a family profile | **allowed, policy-bound** — the target's policy applies (reduction only) |
| broken own profile file | **403** `ErrProfileUnavailable` — fail closed, never unrestricted |
| invalid/unknown token | **401** |
| family token on an operator surface (probe, model POST registration) | **403** — admin principal required |
| same matrix through MCP `scarlix_mode_set` | **identical** — one shared `Authorize` |

Regression proofs: `internal/profiles/loader_test.go` (the four
repository profiles parse WITHOUT fallback; the dual-shape budget truth
table; the NESTED strict-validation truth table; the negative schema
matrix; `List` excludes + reports invalid files; the token-index
validation/file discipline; the authenticator identity binding + the
single-user legacy contract) + `internal/api/profiles_enforcement_test.go`
(the v19.6.6 REST enforcement matrix) + `internal/api/principal_test.go`
(the identity matrix above, 12 tests) + `internal/mcp/principal_test.go`
(the MCP half, 6 tests).

## (s) Stale-tree audit — the repo-hygiene automation (v19.6.6, the process remediation; v19.6.7 hardened)

The v19.6.5 audits' process finding: the repository's attention is
ASYMMETRIC — the control plane hardens daily while `profiles/` sat 85
days (where the P1 lived), `media-tools/` carried a broken build context
for 38, and the experimental `:latest` register relied on v18.8 comments.
The remediation is AUTOMATION, not memory:

- **`files/usr/local/bin/scarlix-stale-audit.sh`** — the audit: (1)
  staleness of every REGISTERED path against its classification
  (ACTIVE/OPTIONAL/EXPERIMENTAL/ARCHIVED) and its max-stale threshold
  (git last-touch in a checkout; **v19.6.7, KPK-19.6.6-P3-05 CLOSED:**
  on an installed tree a registered DIRECTORY's last-touch is the NEWEST
  file mtime found RECURSIVELY under it — caches/generated data excluded
  by the find, plain files keep the direct stat — editing an existing
  file inside `profiles/` no longer reads as stale, and the report
  states which mode produced the age); (2) every `:latest`/tagless image
  reference in every compose file — **v19.6.7: discovery broadened to
  `docker-compose*.yml` AND `docker-compose*.yaml` plus the modern
  `compose.yml`/`compose.yaml` short forms** (`.git` still excluded), so
  future compose files are scanned by construction; (3) every compose
  `build:` context missing its Dockerfile (the P2-04 class); (4) every
  UNCLASSIFIED top-level path — the registry IS the audited contract, an
  unregistered path is unaudited by construction.
- **The time-bounded pin allowlist (v19.6.7, SK-audit P2-03):**
  `docker/image-pin-allowlist.conf` (`image|reason|review-by` per line,
  override via `SCARLIX_STALE_AUDIT_PIN_ALLOWLIST` — a MISSING explicit
  override is named on stderr and audited as if no allowlist existed,
  never silently ignored). An experimental image may ride `:latest` ONLY
  with a documented reason AND a review-by date: while today ≤ review-by
  the weekly audit reports it as `UNPINNED-ALLOWED` (an INFO line —
  visible debt, NOT a finding, no exit-code effect); the day after, the
  entry EXPIRES and the image reverts to a normal UNPINNED finding that
  names the expiry. The four known experimental images
  (`mcp/playwright-server:latest`, `freetoken/serving:latest`,
  `laya/decision-engine:latest`, `ghcr.io/cactus-compute/needle:latest`
  — all NO_ACCESS registries, unverifiable offline) are allowlisted
  until **2027-01-31**. "The default is debt, exceptions are borrowed" —
  pin-a-digest or archive the stack stays the real fix; an evergreen
  allowlist would be a policy hole, which is why every entry is dated.
- **`scarlix-stale-audit.timer`** (weekly Mon 06:30, persistent) — the
  schedule; the service treats findings (exit 1) as SUCCESS so a timer
  failure means the audit itself broke. Reports land in
  `/var/log/scarlix/stale-audit/`.
- **`--ai-summary` — the AI Local Model agent, ADVISORY ONLY:** after the
  audit, the report is POSTed to the LOCAL Ollama tier (loopback-only,
  `curl --noproxy '*'` — the airt transport lesson applied to ops
  tooling; model `qwen2.5:3b`, the canonical starter) for triage —
  which findings block release integrity, which are hygiene, what order
  to fix. The triage is saved next to the report and CHANGES NOTHING:
  "LLM navrhuje, politika rozhoduje". The AI step failing is a WARNING,
  never an audit failure.
- **The policy** lives in `docs/STALE_TREE_AUDIT.md`; the drift-guarded
  image inventory lives in `docs/DEPENDENCY_INVENTORY_2026-11-06.md`
  (`tests/test_dependency_inventory.sh`).

Exit codes are CI-gate friendly (0 clean / 1 findings / 2 usage); the
classification registry is edited deliberately, in the script, reviewed
in code review — and the allowlist is edited just as deliberately, few
entries, dated, reviewed.

## (t) Enforced token budgets — the monthly usage quota (v19.6.7, KPK-19.6.6-P2-02 CLOSED)

The v19.6.6 defect: the profile schema parsed and VALIDATED
`token_budget {monthly: N, model: m}` — and nothing in the runtime ever
read it. The budget was descriptive metadata displayed to the family
while actual usage was unbounded. v19.6.7 makes it an ENFORCED quota on
the `/api/ai/chat` path (`internal/profiles/budget.go` +
`internal/api/principal.go`):

- **The ledger is durable and tamper-disciplined** (the grants.json
  discipline): default path
  `/var/lib/scarlihq-budget/token-budget.json` — 0600 file / 0700
  directory, atomic tmp+rename persist, `schema_version 1`, and a
  fail-closed load: corrupt JSON, a foreign schema version, or loose
  permissions abort ("never run on a tampered ledger"). Mount
  `/var/lib/scarlihq-budget` rw in the scarlihq compose, or set
  `SCARLIHQ_AIRUNTIME_BUDGET_LEDGER` to a writable path.
- **RESERVE-BEFORE-FORWARD:** the request's estimate (input runes / 4
  per message + the fixed 4096-token output reserve) is held against the
  profile's `monthly` budget BEFORE the model is called — concurrent
  requests cannot collectively overspend the month (the mutex-guarded
  check counts used + already-reserved + this estimate). A Reserve whose
  persist fails is rolled back — the hold is NOT granted when it cannot
  be recorded.
- **SETTLE-AFTER-RESPONSE:** the reservation is replaced by the
  provider-reported usage (input + output; per-model usage recorded for
  the report). When the provider reports none, the estimate stands —
  the documented fallback. **FAILED attempts are RELEASED whole** — not
  billed (transport errors, refusals, failover attempts that never
  answered; an error path releases the reservation, and a settlement
  failure leaves the hold standing — accounting errs toward
  restriction, never toward overspend).
- **The window is a UTC calendar-month key** (`2006-01` — explicit, no
  local-time ambiguity), older months pruned to a 2-month rolling
  window, and accounting is per profile ACROSS models — routing a chat
  through a different model must not escape the budget (the declared
  model is the expected/default; the per-model split is report-only).
- **Quota exhaustion is a STABLE 429** carrying the quota state
  `{profile, month, monthly, used, reserved, remaining}`; **ledger
  unavailability fails CLOSED (503)** when a budget applies — an
  unaccountable budget is not a budget (`ErrLedgerUnavailable`).
- **The degraded policy is documented, not silent:** without a ledger
  (an unwritable DEFAULT path) the declared budgets degrade to
  loudly-warned INFORMATIONAL metadata — one startup warning + one
  request-stream warning say exactly that. An EXPLICIT
  `SCARLIHQ_AIRUNTIME_BUDGET_LEDGER` that fails to load/validate
  ABORTS STARTUP instead — the explicit path is fail-closed, the
  default path degrades loudly.
- **Honest scope:** the budget covers the `/api/ai/chat` path only (the
  provider-facing family surface). The propose path is admin-gated
  (§r) — a family principal cannot reach it at all; `unlimited`
  budgets (the admin) and profiles without a monthly budget are not
  accounted.
- Regression proofs: `internal/profiles/budget_test.go` (8 tests —
  reserve/settle accounting, release-not-billed, the quota refusal, the
  11-way CONCURRENT reservation race (cannot overspend), the UTC month
  window + pruning, the persistence round-trip, the tamper discipline,
  the estimate) + the REST proofs in `principal_test.go`
  (`TestBudgetQuotaExceededIsStable429` — the 429 shape;
  `TestBudgetSettlesProviderReportedUsage` — the settle path).

## (u) Media-tools request boundary (v19.6.7, KPK-19.6.6-P2-03 CLOSED)

The v19.6.6 posture (fixed argv, no shell, filename shape validation,
64 KiB body limit, 600 s subprocess bound, non-root container,
loopback-published port) left the request plane unauthenticated and
unbounded — every accepted conversion could spawn an ffmpeg/ImageMagick
process for up to 600 s with no cap, no queue bound, no per-client
limit, and a malformed `Content-Length` raised outside the JSON error
handling. v19.6.7 (`media-tools/server.py`):

- **`MEDIA_TOOLS_TOKEN` Bearer authentication** — `hmac.compare_digest`
  (the answer does not leak match timing). WITHOUT the token the
  conversion surface fails CLOSED: `/convert` and `/templates` answer
  503 with a body that says the surface is closed; `/health` stays OPEN
  (liveness — no token, ever). With a token set:
  missing/wrong/non-Bearer Authorization → 401.
- **Bounded concurrency:** a `threading.BoundedSemaphore` sized by
  `MEDIA_TOOLS_MAX_CONCURRENCY` (default 2, clamped 1..16); a request
  that cannot acquire a slot within the 15 s queue wait gets a
  deliberate 503 (the queue is bounded, not infinite). A per-client-IP
  cap of 2 concurrent conversions → 429 beyond.
- **Collision and malformed-input discipline:** an output-name collision
  lock — two parallel conversions writing the SAME output name → one
  200, one 409 "output name busy" (the loser never clobbers the
  winner's file); an invalid `Content-Length` → 400 (a deliberate
  answer, not a `ValueError` traceback); a non-object JSON root
  (`[1,2,3]`, `42`) → 400 (not a 500); a 30 s read timeout.
- Regression proofs: `tests/test_media_tools.sh` — 42 assertions
  against REAL server instances on a random loopback port (env seams:
  `MEDIA_TOOLS_PORT`, `MEDIA_TOOLS_INPUT_DIR`/`_OUTPUT_DIR`/
  `_TEMPLATES_DIR`): the fail-closed/401 matrix, malformed-header and
  non-object-root 400s, a REAL ffmpeg conversion (wav → mp3), the
  saturation storm (5 truly-parallel conversions at
  `MAX_CONCURRENCY=1`: exactly one 200, one 503 queue-full, three 429s,
  and the server SURVIVES — `/health` still 200, no 5xx), and the
  output-name collision twin (200/409). 42 run, 0 failed, 0 skipped at
  tag.

## (v) The v20.0.0 control-plane surfaces — node-tagged admission + the resource accounting (v20.0.0)

| Property | Enforcement |
|---|---|
| Node-tagged admission gate | `POST /api/compute/admission` with `node_id` routes through `admission.NodeSnapshots` — the fabric Registry's §4.1 guards (quarantined → unreachable → absent → stale-remote-window) under ONE lock; the refusal vocabulary is CLOSED and surfaces VERBATIM as reason codes (`node-unknown`/`node-quarantined`/`node-unreachable`/`node-inventory-absent`/`node-inventory-stale` + the generic `node-snapshot-unavailable`). Without the seam (the fabric not enabled): the NAMED `node-source-unconfigured` — node capacity is NEVER guessed from the local host's snapshot. |
| One authoritative ledger | The node tag (`Record.node_id`) is a ROUTING KEY on the single control-plane ledger — no per-node stores, no consensus, no second ledger (the Phase 5 design's §1 hub-and-spoke stands). Per-node capacity totals key on the normalized node id: a local hold never consumes a remote node's allocatable and vice versa. |
| Idempotency across nodes | `node_id` rides the request fingerprint — the same idempotency key against a different node is the NAMED `idempotency-conflict`, never a silently-inherited hold. |
| Resource accounting (READ) | `GET /api/compute/resources` — observation grade (`h.auth`): the view reserves nothing. The closed label vocabulary (observed/estimated/reserved/allocatable/unknown/stale) is the machine contract; **allocatable is NULL for stale/unreachable/quarantined nodes** (never a guess from aged figures); without the admission gate the reserved view is the honest ZERO with `reservations_configured=false`. |
| Capability census (READ) | `GET /api/controlplane` — observation grade; the rows never claim operator-owned acts (hardware acceptance KPK-06, real multi-host acceptance stay in the runbook's matrices). |
| Production ledger view | `api.FabricAdmissionLedger` — the executions push computes REAL reconciliation decisions against the HELD/ACTIVE ledger records (the v19.9.0 report-only fallback closed); an unknown node returns the EMPTY view (every agent-reported execution is foreign work — §4.2). |
| Tailscale = transport, NEVER authorization | unchanged (§9.2): the per-node credential + replay guard gate every push regardless of network reachability. |

## Other security properties

- **REST/MCP/WS auth:** Bearer token only (no `?token=` URL — v18.6 P2 removed the URL fallback that leaked tokens into logs/referrer/proxy). `crypto/subtle.ConstantTimeCompare` (v18.0.0 P1 — was: custom `secureCompare`). **v19.6.7:** the presented Bearer RESOLVES to an identity-bound principal (per-profile token index first, the household token = the admin principal — §r); operator surfaces additionally require the admin principal (`authAdmin`); the 401/503 semantics for the household token are unchanged.
- **WS ticket:** 30s single-use, 32-byte `crypto/rand` hex, fail-closed on RNG failure (v18.7.4 P0 — was: ignored RNG failure → predictable all-zeros ticket). `ReserveWSTicket` atomically deletes before `upgrader.Upgrade()` (v18.7.5 P0 — was: `PeekWSTicket` didn't delete → two concurrent WS connects with same ticket both upgraded). No `ReleaseWSTicket` on failure (v18.7.6 P0 — was: re-added with fresh 30s TTL → attacker-renewable lifetime). Max 1024 outstanding tickets (v18.7.7 P1 rate limit).
- **HTTP server timeouts:** `ReadHeaderTimeout 5s`, `ReadTimeout 15s`, `WriteTimeout 30s`, `IdleTimeout 60s`, `MaxHeaderBytes 64KB` (v18.6 P1 / v18.7.6 P1).
- **WS origin check:** `net.IPNet.Contains` against `127.0.0.1/32`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (v17.9.9 P1 — was: `strings.HasPrefix`; v18.7.5 P1 — was: manual string ops broke IPv6 `http://[::1]:8090`).
- **WS concurrency:** max 16 concurrent clients (`wsSlots = make(chan struct{}, 16)`, v18.5 P1).

> **Baseline:** This document is part of the v19.0.6 release baseline freeze. Do not modify content without a version bump.
