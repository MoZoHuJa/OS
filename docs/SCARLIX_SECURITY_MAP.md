# SCARLIX OS v19.3.1 — Security Map

> Purpose: Catalog every security boundary in the v19.0.6 tree — the privilege separation model, atomic file operations, the TOCTOU-safe reader, the fail-closed env loader, lock file topology, and the systemd hardening matrix. All values cited from files on branch `fix-v19.0.6`.
>
> **v19.2.12 update:** added section (h) — experimental image supply-chain residual risk (Deep Audit F-08) and section (i) — Pi-Bolt installer SHA-256 pin (Deep Audit F-01).
>
> **v19.2.13 update:** section (i) extended with the user-owned verified-copy execution handoff (KPK-19.2.12-P2-01) and the scheduled pin-freshness CI job; new section (j) — reservation-layer CleanupVerified semantics (KpK audit P2-02).
>
> **v19.4.2 update:** dependency pin hygiene per `docs/DEPENDENCY_AUDIT_2026-10-21.md` — P0 (pruned upstream tags: coolify, sunshine, buzz/minio) and P1 (stale security pins: crowdsec, ollama, gitea, nextcloud, headscale) fixed; 5 pins now digest-pinned; smoke check 07 existence-checks all fixed pins in CI (section h extended).

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

| Service | Image | Scope | Risk accepted because |
|---|---|---|---|
| needle | `ghcr.io/cactus-compute/needle:latest` | `profiles: [experimental]` — never started by the default mode path | experimental opt-in only; NO_ACCESS note in tree (image unverifiable offline) |
| freetoken | `freetoken/serving:latest` | experimental profile | same |
| browser-mcp | `mcp/playwright-server:latest` | experimental profile | same |
| laya | `laya/decision-engine:latest` | experimental profile + single-GPU gate applies (device_ids 1) | same |

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
| **v19.2.13 pin freshness** (KpK audit P2-01) | scheduled CI job (`.github/workflows/pin-freshness.yml`, weekly) re-downloads the installer and compares the hash with the shipped pin — drift fails the job. Pi-Bolt pin last verified: **2026-10-20**. |

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

## (n) Agent Kernel foundation + sessions (v19.4.x, AK-1 + AK-2)

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

## Other security properties

- **REST/MCP/WS auth:** Bearer token only (no `?token=` URL — v18.6 P2 removed the URL fallback that leaked tokens into logs/referrer/proxy). `crypto/subtle.ConstantTimeCompare` (v18.0.0 P1 — was: custom `secureCompare`).
- **WS ticket:** 30s single-use, 32-byte `crypto/rand` hex, fail-closed on RNG failure (v18.7.4 P0 — was: ignored RNG failure → predictable all-zeros ticket). `ReserveWSTicket` atomically deletes before `upgrader.Upgrade()` (v18.7.5 P0 — was: `PeekWSTicket` didn't delete → two concurrent WS connects with same ticket both upgraded). No `ReleaseWSTicket` on failure (v18.7.6 P0 — was: re-added with fresh 30s TTL → attacker-renewable lifetime). Max 1024 outstanding tickets (v18.7.7 P1 rate limit).
- **HTTP server timeouts:** `ReadHeaderTimeout 5s`, `ReadTimeout 15s`, `WriteTimeout 30s`, `IdleTimeout 60s`, `MaxHeaderBytes 64KB` (v18.6 P1 / v18.7.6 P1).
- **WS origin check:** `net.IPNet.Contains` against `127.0.0.1/32`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (v17.9.9 P1 — was: `strings.HasPrefix`; v18.7.5 P1 — was: manual string ops broke IPv6 `http://[::1]:8090`).
- **WS concurrency:** max 16 concurrent clients (`wsSlots = make(chan struct{}, 16)`, v18.5 P1).

> **Baseline:** This document is part of the v19.0.6 release baseline freeze. Do not modify content without a version bump.
