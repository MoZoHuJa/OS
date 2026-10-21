# Hermes Agent UPGRADE REPORT — v2026.9.24 (v0.21.5) → v0.22.2

**Task:** ScarLiXOS Task 12 — Hermes Agent migration (in-place, current version line).
**Date of audit/verification:** 2026-10-21
**Repo files changed:** `agents/hermes/docker-compose.yml`, `agents/hermes/config.yaml`,
`agents/hermes/.env.example` (new), `agents/hermes/migrate-to-v0.22.2.sh` (new),
`docs/ARCHITECTURE.md`, `.github/workflows/ci.yml`.
**Not changed:** no version bump, no tag — the deployment ships as part of the
current version line (v19.4.1) per operator instruction.

---

## 1. Image verification (Phase A)

| Item | Value | Proof |
|---|---|---|
| Old tag | `nousresearch/hermes-agent:v2026.9.24` | prior `docker-compose.yml` pin |
| Old manifest digest | `sha256:fca358f12efd65bfaaca05884166f15c0e2788375ca30d77061ac1ebc96452b7` (OCI index) | registry-1.docker.io manifest HEAD |
| Old amd64 image | `sha256:2fd023efbb8d…` | per-arch manifest entry |
| Old upstream release | v0.21.5, tag `v2026.9.24`, image label `org.opencontainers.image.revision=f97608f178d1ffeca59860195ab7da295f7c8e5f` | image config blob + GitHub releases list |
| New tag | `nousresearch/hermes-agent:v0.22.2` | Docker Hub tags API (`count=184`) |
| New manifest-list digest | `sha256:36099926da316742423544e055d4734777105ffe6341873af5459b957d0c2919` | registry-1.docker.io `Docker-Content-Digest` |
| New amd64 image | `sha256:a11574b7fe8e1631d7d88d556677dac693bc058abf558cdeb899155688c1bf05`, pushed 2026-10-15, size 1 121 639 598 B | per-arch manifest entry + Docker Hub tags API |
| New arm64 image | `sha256:6793b8c89a44872fc9a5ef135ac0c28bf0a7b9caf4968278195b14e43919aef5` | per-arch manifest entry |
| Upstream `stable` tag | **same digest as v0.22.2** (`sha256:36099926…`) — release-gate image | registry manifest HEAD |
| Upstream release | `v0.22.2` — official, NOT prerelease, published 2026-10-15, "Hermes Agent v0.22.2" | GitHub releases API |
| Newer stable? | None. `v0.22.3` exists **only** as `rc.1`/`rc.2` tags (release candidates). `v0.22.1` was "built from the wrong commit" per upstream notes — v0.22.2 is the release 0.22.1 was meant to be. | Docker Hub tags API + release notes |
| Image entrypoint | `["/opt/hermes/docker/entrypoint-dispatch.sh"]`, CMD `[]`, User `root`, WorkingDir `/opt/hermes`, Volume `/opt/data` | image config blob (both versions) |
| Runtime | s6-overlay in **both** images (PID 1 → `/init` → `main-wrapper.sh`); dispatch script is byte-identical between old/new tags | `diff` of `docker/entrypoint-dispatch.sh` between the two upstream checkouts |

**Pin used in compose:** `nousresearch/hermes-agent:v0.22.2@sha256:36099926da316742423544e055d4734777105ffe6341873af5459b957d0c2919`
(tag for humans, digest for immutability). No `latest`, no unverified tag.

---

## 2. Compatibility table (old → new)

| Contract | v2026.9.24 (v0.21.5) | v0.22.2 | Action taken |
|---|---|---|---|
| Image pin | tag only | tag + manifest digest | pinned by digest |
| Container command | none (default `hermes` → interactive chat) | `command: ["gateway","run"]` is the documented persistent-gateway form; s6 supervises the gateway process | **added explicit `command: ["gateway","run"]`** |
| User data path | `./config:/root/.hermes` + `HERMES_HOME=/root/.hermes` env override | `/opt/data` is the only supported home (image default `HERMES_HOME=/opt/data`, volume `/opt/data`; docs: "single source of truth") | **mount changed to `./config:/opt/data`; `HERMES_HOME` env removed** |
| UID model | container root entrypoint, agent drops to internal `hermes` (10000) via s6-setuidgid; stage2 chowns `$HERMES_HOME` | same + explicit `HERMES_UID`/`HERMES_GID` remap API (1–65534, non-root; PUID/PGID aliases) | env added with default 10000 (documented in `.env.example`) |
| Health port | `127.0.0.1:7999:7999`, `curl localhost:7999/health` | **7999 no longer exists.** API server (OpenAI-compatible) + `GET /health` (no auth, JSON status) on **8642**; gated on `API_SERVER_ENABLED=true` + `API_SERVER_KEY` (required, min 8 chars). Dashboard is a separate supervised service on 9119 (`HERMES_DASHBOARD=1`) — not used here. | **healthcheck → `curl -sf http://localhost:8642/health`**; API server enabled, bound to **container loopback only**; host port publishing REMOVED (nothing in ScarLiXOS consumed 7999 — verified by repo-wide grep) |
| `OPENAI_API_BASE` env | supported | **removed** — renamed `OPENAI_BASE_URL` | env renamed in compose |
| `OPENAI_API_KEY` env | supported | still supported (auth fallback) | kept |
| LiteLLM endpoint | `config.yaml: custom_provider.base_url` | **`custom_provider` (singular) key no longer exists in the codebase** — universal pattern is top-level `base_url` + `api_key` (with `base_url` set, provider routing is bypassed and the endpoint is called directly) | config rewritten to top-level `base_url`/`api_key` |
| `provider: custom` | supported | supported (runtime_provider_custom) | kept |
| `model: qwen3.6:14b` | supported | supported (model string passes to endpoint) | kept — **operator must confirm the LiteLLM alias still resolves** (deploy step 4 in §5) |
| `memory.enabled` | supported | **renamed `memory.memory_enabled`** (old key silently ignored) | key renamed |
| `memory.nudge_interval: 300` | supported | supported (default 10) | kept |
| `skills.enabled` / `skills.auto_create` | supported | **both keys gone** — skills are always available; managed via `skills.disabled` lists | removed from config; documented |
| `cron.enabled` | supported | **key gone** — built-in cron scheduler runs with the gateway | removed from config; documented |
| `gateway.telegram.enabled/bot_token/allowed_users` (config.yaml) | supported | **gone** — enablement is env-driven (`TELEGRAM_BOT_TOKEN`), allowlist is `TELEGRAM_ALLOWED_USERS` (user IDs, comma-separated; fail-closed when unset) | removed from config; wired via compose env |
| `gateway.discord.enabled: false` (config.yaml) | supported | **gone** — Discord off unless `DISCORD_BOT_TOKEN` set | removed (default already off) |
| `TELEGRAM_CHAT_ID` env | supported | **no longer read** — allowlist is `TELEGRAM_ALLOWED_USERS` | compose env rewires `.env:TELEGRAM_ZMOR_CHAT_ID` → `TELEGRAM_ALLOWED_USERS`. For private chats chat id == peer user id, so the existing value stays valid; it is an allowlist of USER IDs now (documented) |
| MCP config | `mcp.servers.<name>.url` + `transport: http` | **replaced by top-level `mcp_servers:`**; `transport` key is only used for the SSE exception — HTTP is implied by `url` | config rewritten |
| MCP tool filtering | (none available in old config) | `mcp_servers.<name>.tools.include/exclude` (exact + glob) | **read-only allowlist added** (see §4) |
| `TZ=Europe/Bratislava` | supported | supported (base image `debian:13.4` full → tzdata present) | kept |
| `scarlix-net` | external network | external network | kept (CI enforces the name) |
| `/opt/scarlix:ro` | read-only mount | read-only mount | kept; read-only verified by script + CI review |
| `./workspace:/workspace` | special-ish path | plain directory (no special runtime support; agent cwd is `/opt/hermes`, `terminal.cwd` configurable) | mount kept as a plain bind; documented |
| Approvals | defaults (interactive prompts) | `approvals.mode: smart\|manual\|off` (default `smart`), hardline blocklist always on, `approvals.deny` globs, unattended tool-loop hard stops default-on | `approvals.mode: smart` written explicitly |
| Healthcheck tooling | `curl` in image | `curl` still in image (Dockerfile runtime deps) | kept |
| `docker run --user` | n/a | **explicitly unsupported** (fails fast) | not used; UID remap via env instead |

**Telegram authorization semantics (verified in `plugins/platforms/telegram/adapter.py`):**
allowlist decisions use the sender's **user id** (from `from_user`), `TELEGRAM_ALLOWED_USERS`
is comma-separated user IDs (or `*`), and the fallback is **fail-closed** — no allowlist
means deny (unless `GATEWAY_ALLOW_ALL_USERS` is set, which we do not set).

---

## 3. What the migration deliberately does NOT change

- No new OS version, no git tag (operator instruction — edits ship in the current line).
- `restart: unless-stopped`, container name `hermes`, external `scarlix-net`.
- LiteLLM stays at `http://litellm:4000/v1` over `scarlix-net` (SMG/LiteLLM contract unchanged).
- ScarliHQ MCP endpoint stays `http://scarlihq:8091/mcp` over `scarlix-net`.
- No privileged mode, no docker.sock, no `network_mode: host`, no extra capabilities.
- No dashboard service (`HERMES_DASHBOARD` unset → dashboard does not start).
- No secrets in git: `.env` is runtime-only; `.env.example` carries placeholders.

---

## 4. Security posture (Phase H)

| Control | State in the new deployment |
|---|---|
| `/opt/scarlix` read-only | mount unchanged; `migrate --verify` asserts the mount is RO and fails otherwise |
| Ports published to host | **none** (the old `127.0.0.1:7999:7999` publish is removed — no consumer existed; API server binds container loopback 127.0.0.1:8642 for the healthcheck only) |
| API server auth | `API_SERVER_ENABLED=true` requires `API_SERVER_KEY` (upstream refuses to start the adapter without it); the migration script auto-generates a 32-byte hex key into `.env` |
| Telegram auth | token via env; allowlist = user IDs (fail-closed default) |
| MCP allowlist | **read-only ScarliHQ tools only**: `scarlix_mode_get`, `scarlix_gpu_status`, `scarlix_container_list`, `scarlix_profile_list`, `scarlix_full_status`. The mutating `scarlix_mode_set` is deliberately **not** registered — OS mode switches remain behind ScarLiXOS/Host-Bridge authorization |
| Approvals | `approvals.mode: smart` (dangerous commands need an in-chat approval from the allowlisted admin); hardline blocklist always on; unattended gateway/cron tool-loop hard stops default-on |
| Secrets in logs | `migrate --verify` greps the last 200 log lines for all three secrets and fails on any hit |
| Entry point | image default (s6 `/init` chain) — never overridden (overriding breaks UID remap + supervision) |

**Known-issue note (upstream):** sessions that ran under the 0.22.1 gateway cannot be
deleted in 0.22.2. We migrate **from v0.21.5**, never through 0.22.1, so this does not
apply — recorded for completeness.

---

## 5. Deployment runbook (on the ScarLiXOS host)

```bash
# 0) Pull the updated repo (compose/config/script arrive together)
cd /path/to/OS-repo && git pull            # main @ current version line
cd agents/hermes

# 1) Dry-run (changes nothing; validates env + compose pin)
./migrate-to-v0.22.2.sh

# 2) Apply: stop → backup (config+workspace+.env, SHA-256 manifest) →
#    ensure API key → pull pinned image → up → wait for /health
./migrate-to-v0.22.2.sh --apply

# 3) Post-deploy verification (image pin, health, log errors, secret leak, RO mount)
./migrate-to-v0.22.2.sh --verify

# 4) Operator checks that need a human:
#    - send the bot a Telegram message from the ALLOWED account  → it must answer
#    - send from a non-allowed account                            → it must refuse
#    - confirm the LiteLLM alias: docker exec hermes curl -s http://litellm:4000/v1/models \
#        -H "Authorization: Bearer $LITELLM_MASTER_KEY" | grep qwen3.6
```

**Diagnostics:**

```bash
docker inspect --format='{{.State.Health.Status}}' hermes   # healthy | unhealthy
docker logs --tail 100 hermes
docker exec hermes curl -sf http://localhost:8642/health    # JSON status
docker inspect hermes | grep -A3 '"Image"'                  # verify the digest
ls migration-backups/                                       # backup + manifest
```

**Rollback (practically scripted, see §7 for limits):**

```bash
./migrate-to-v0.22.2.sh --rollback
# = stop+remove new container (single-writer rule) → restore config/workspace/.env
#   from the newest backup → restore the v19.4.1 compose+config from git →
#   docker compose up -d on the OLD image → verify Telegram/LiteLLM/MCP manually
```

Rollback keeps the backup archive (`migration-backups/`) — original data is never
deleted during the rollback window. Never run the old and the new agent over the same
data directory at the same time (the script enforces stop-before-restore).

---

## 6. Exposing the API server later (if ever needed)

Not enabled now. If a host-side consumer for the OpenAI-compatible API is ever added:
set `API_SERVER_HOST=0.0.0.1`… use `0.0.0.0` **inside the container only together with**
a strong `API_SERVER_KEY`, publish as `127.0.0.1:8642:8642` (host loopback), and put
anything remote behind an authenticated reverse proxy/VPN. `HERMES_DASHBOARD_INSECURE`
must never be used.

---

## 7. Verification results — what was and was not tested

**Verified with evidence (this audit, sandbox + registry/upstream sources):**

| # | Check | Result |
|---|---|---|
| A1 | Docker Hub tag `v0.22.2` exists, amd64+arm64, pushed 2026-10-15 | PASS (tags API) |
| A2 | Manifest digest pinned; `stable` tag == same digest | PASS (registry API) |
| A3 | v0.22.2 is the newest stable (0.22.3 = RC only; 0.22.1 = bad build) | PASS (tags API + release notes) |
| A4 | Upstream release v0.22.2 official, not prerelease | PASS (GitHub releases API) |
| A5 | Image entrypoint/user/volumes extracted and compared old↔new | PASS (config blobs) |
| C1 | Compose: pinned tag+digest, `scarlix-net` kept, `/opt/scarlix:ro` kept, no privileged/docker.sock/host network, ports: none published, healthcheck on 8642 with start_period | PASS (static review + CI compose-validation locally) |
| C2 | `command: ["gateway","run"]` matches upstream docker docs | PASS (docker.md @ v0.22.2) |
| D1 | `custom_provider` (singular) absent from v0.22.2 code; top-level `base_url`+`api_key` verified in `runtime_provider_custom.py` + configuration.md | PASS (code grep) |
| D2 | `${VAR}` env expansion in config.yaml verified (`_expand_env_vars`) | PASS (code) |
| D3 | `OPENAI_API_BASE` gone; `OPENAI_BASE_URL` used by runtime provider resolution | PASS (code grep) |
| E1 | `TELEGRAM_CHAT_ID` not read anywhere; `TELEGRAM_ALLOWED_USERS` = user-ID CSV, fail-closed | PASS (config_defaults.py, adapter code) |
| E2 | Telegram allowlist uses sender user id | PASS (adapter `_env_allowlist_decision`) |
| F1 | `mcp.servers` → `mcp_servers` migration verified in `mcp_config.py` (only `mcp_servers` key is read) | PASS (code) |
| F2 | `tools.include` allowlist supported (exact + glob) | PASS (mcp.md + mcp_config.py) |
| G1 | `memory.memory_enabled` replaces `memory.enabled` | PASS (config_defaults.py) |
| G2 | `skills.enabled`/`auto_create`/`cron.enabled` keys absent in v0.22.2 | PASS (code grep) |
| H1 | approvals `smart` default + hardline blocklist + unattended hard stops | PASS (security.md) |
| H2 | `curl` present in image runtime deps | PASS (Dockerfile) |
| I1 | `bash -n` + shellcheck on the migration script | PASS |
| I2 | Compose/config YAML parse + `docker compose config` interpolation check (local, no docker → YAML + interpolation matrix simulated with the CI env file) | PASS |
| I3 | Repo-wide grep: nothing consumes host port 7999 | PASS |

**NOT verifiable in this sandbox (requires the ScarLiXOS Docker host — run during deploy):**

| # | Check | Where |
|---|---|---|
| S1 | `docker compose config` against the real docker CLI | deploy step 1 (dry-run preflight) |
| S2 | Live image pull by digest + container boot (s6 supervision tree) | deploy step 2 |
| S3 | Readiness via real `GET /health` | deploy step 2 (script waits, no bare sleep) |
| S4 | LiteLLM completion + streaming through the new runtime | operator step 4 + bot test |
| S5 | Telegram allowed-user allow / non-allowed refuse | operator step 4 |
| S6 | ScarliHQ MCP handshake + allowlisted tools only | operator (ask the bot for mode/status) |
| S7 | Memory/skills/cron persistence across restart | operator (restart container, re-check) |
| S8 | `scarlix_mode_set` refusal (not in allowlist) | operator (ask the bot to switch modes) |
| S9 | Rollback rehearsal on the host | `--rollback` on the staging copy first |

This split is deliberate and honest: the sandbox has no Docker daemon, no LiteLLM,
no ScarliHQ bridge and no Telegram account, so the staged smoke test of the guide's
§10 cannot be executed here. Every statically decidable fact above is backed by the
quoted upstream source; everything else is an explicit, scripted deploy-time check.

---

## 8. Open risks / follow-ups

1. **LiteLLM alias drift** — `qwen3.6:14b` must still be a valid alias on the LiteLLM/SMG
   gateway (deploy step 4 checks it; if the alias changed, update `model:` in config.yaml).
2. **First boot after upgrade** — v0.22.2's s6 boot does profile/config reconciliation on
   the migrated `/opt/data`; the `start_period: 90s` covers it, but the first boot can be
   slower than the old image's (1.1 GB image, cont-init chown pass over the data tree).
3. **Sessions/DB schema** — v0.22.2 rolls up v0.21.6→v0.22.2 including gateway state
   changes; the backup archive (state.db + -wal/-shm) is the pre-migration snapshot and
   `--rollback` restores it wholesale. Do not open the migrated DB with the old image
   after the new one has run on it (schema may have moved) — restore from backup instead.
4. **`scarlix_mode_set` intentionally locked out** — if the Telegram agent should be able
   to request mode switches again, add it to `tools.include` only after a security review
   (it bypasses nothing by itself — Host-Bridge still authorizes — but the agent gains an
   unattended mutation request path).
5. **Upstream cadence** — the repo tracks `stable`-gated releases; re-run this report's
   Phase A checks (tags API + digest) whenever a future bump is planned. Never `latest`.

---

## 9. Definition of Done (guide §14) — status

- [x] Image tag and digest verified and pinned (tag+digest in compose)
- [x] Configuration valid per the real runtime (keys verified against v0.22.2 source)
- [x] Backup and migration reproducible (scripted, idempotent, SHA-256 manifest)
- [ ] LiteLLM completion/streaming — **deploy-time check S4**
- [ ] Telegram allowed/refused — **deploy-time check S5**
- [x] ScarliHQ MCP configured with a minimal allowed toolset (read-only)
- [ ] Memory/skills/sessions/cron survive restart — **deploy-time check S7**
- [x] `/opt/scarlix` read-only boundary asserted by script + static review
- [ ] Gateway + healthcheck after cold start and restart — **deploy-time checks S2/S3/S7**
- [x] Dashboard/API not exposed unauthenticated (no host ports; key required; loopback bind)
- [x] Secrets not in git (`.env.example` placeholders only); log-leak check scripted
- [ ] Critical regression tests — **deploy-time checks S1–S9 bundle**
- [x] Rollback procedure scripted and documented (practical host rehearsal = S9)
- [x] This report contains results, not claims (evidence column per check)
