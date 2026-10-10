# SCARLIX OS v19.2.1 — Security Map

> Purpose: Catalog every security boundary in the v19.0.6 tree — the privilege separation model, atomic file operations, the TOCTOU-safe reader, the fail-closed env loader, lock file topology, and the systemd hardening matrix. All values cited from files on branch `fix-v19.0.6`.

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

## Other security properties

- **REST/MCP/WS auth:** Bearer token only (no `?token=` URL — v18.6 P2 removed the URL fallback that leaked tokens into logs/referrer/proxy). `crypto/subtle.ConstantTimeCompare` (v18.0.0 P1 — was: custom `secureCompare`).
- **WS ticket:** 30s single-use, 32-byte `crypto/rand` hex, fail-closed on RNG failure (v18.7.4 P0 — was: ignored RNG failure → predictable all-zeros ticket). `ReserveWSTicket` atomically deletes before `upgrader.Upgrade()` (v18.7.5 P0 — was: `PeekWSTicket` didn't delete → two concurrent WS connects with same ticket both upgraded). No `ReleaseWSTicket` on failure (v18.7.6 P0 — was: re-added with fresh 30s TTL → attacker-renewable lifetime). Max 1024 outstanding tickets (v18.7.7 P1 rate limit).
- **HTTP server timeouts:** `ReadHeaderTimeout 5s`, `ReadTimeout 15s`, `WriteTimeout 30s`, `IdleTimeout 60s`, `MaxHeaderBytes 64KB` (v18.6 P1 / v18.7.6 P1).
- **WS origin check:** `net.IPNet.Contains` against `127.0.0.1/32`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (v17.9.9 P1 — was: `strings.HasPrefix`; v18.7.5 P1 — was: manual string ops broke IPv6 `http://[::1]:8090`).
- **WS concurrency:** max 16 concurrent clients (`wsSlots = make(chan struct{}, 16)`, v18.5 P1).

> **Baseline:** This document is part of the v19.0.6 release baseline freeze. Do not modify content without a version bump.
