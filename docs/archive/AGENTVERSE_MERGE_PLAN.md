# AgentVerse-OS-ScarLiX-1s → SCARLIX OS Merge Plan

> ⚠️ **DEPRECATED — HISTORICAL DOCUMENT**
> This document describes SCARLIX OS v15/v16.1 (Garuda Linux ISO build).
> SCARLIX OS is now **EndeavourOS + install.sh bootstrap** (no ISO).
> Kept for historical reference only. Current version: see /VERSION + README.md.
> Do NOT follow these instructions — they will not work with v18.x+.



**Document ID:** AV-MERGE-PLAN-1
**Author:** research-agent (Task ID AV-MERGE)
**Date:** 2026-09-27
**Source repo:** https://github.com/MoZoHuJa/AgentVerse-OS-ScarLiX-1s (forked from `agentverse-os/AgentVerse-OS`, Apache-2.0, alpha 0.2)
**Target repo:** SCARLIX OS v17.0 (`scarlix-os-v12-repo/`, EndeavourOS/Arch base, ScarliHQ Go core)
**Status:** Draft for review by MoZoHuJa before v17.2 cycle

---

## 1. Executive Summary

`AgentVerse-OS-ScarLiX-1s` is **MoZoHuJa's own fork** of the upstream `agentverse-os/AgentVerse-OS` project (created 2026-09-18, six days after the upstream's last push). The fork is structurally identical to upstream (same 12 root entries, same 11 312-byte README.md), so for merge purposes the fork and upstream are treated as the same codebase. Upstream carries 981★ / 26 forks, Apache-2.0, and is in active alpha 0.2.

The repo is **not a port of SCARLIX OS to Ubuntu** — it is an **independent, opinionated design** for a "personal cloud OS on a single server" that overlaps SCARLIX's mission but solves a different primary problem:

| Axis | SCARLIX OS v17.0 | AgentVerse-OS-ScarLiX-1s |
|------|-------------------|--------------------------|
| Primary user | Family + AI workloads (4 profiles, GPU gaming) | Solo developer + their AI agents |
| Surface | 2D web dashboard (HTMX, embedded in Go) | Windowed desktop PWA (Svelte 5) |
| Install target | EndeavourOS / Arch (Garuda ISO) | Ubuntu 22.04+ (scripted bootstrap) |
| App model | 21 hand-built `docker-compose.yml` | 944-app catalog (Runtipi+Coolify+Umbrel merged) |
| Provider abstraction | None (containers hardcode endpoints) | Capabilities (`storage.s3`, `llm`, `notify`) |
| Workspace isolation | Docker only | Per-project Incus container with Docker inside |
| Remote access | Headscale + Caddy + Authelia + CrowdSec | Tailscale-only (MagicDNS + tailscaled-issued certs) |
| Backups | BTRFS + Snapper + restic | ZFS snapshots + restic (per-app rollback) |
| Core language | Go (`ScarliHQ`, :8090) | Rust (`cloudd` v0.2.0, axum+rusqlite+bollard) |
| AI stack | 3-tier inference (SGLang→Ollama→llama.cpp), 6 GPU modes | Apps that consume `llm` capability (LiteLLM, Ollama via catalog) |
| Agents | Hermes + OpenCode + gstack (23 manager tools) | Catalog ships Hermes + Pipecat Voice; workspaces run Claude Code + Codex inside Incus |
| State | SQLite + cosine memory | rusqlite (bundled), 7-entity domain model |

**Recommendation in one line:** Do **not** replace SCARLIX OS wholesale; treat AgentVerse-OS as a **design library** — adopt 5 specific patterns (capabilities, 944-app store merger, browser-windowed desktop, Incus workspaces, Rust side-car core) and adapt them onto SCARLIX's existing Go/Arch/BTRFS substrate, while skipping 3 patterns that conflict with SCARLIX's mission (Tailscale-only access, Ubuntu bootstrap, single-user model).

**Expected payoff:** A 50× expansion of the SCARLIX app catalog (21 → 944+), a desktop-grade UI surface, a capabilities-based provider layer that lets Hermes dynamically swap providers, and isolated dev workspaces — all without abandoning Arch, BTRFS+Snapper, Headscale+Authelia+CrowdSec, the 4-profile family model, or the dual-NVIDIA inference stack.

**License note:** AgentVerse-OS is Apache-2.0 (compatible with SCARLIX's sovereign distribution model). The 938 imported catalog manifests retain their upstream licenses (Runtipi = MIT-compatible, Coolify = AGPL-3.0 for some compose stacks, Umbrel = Polyform Non-Commercial for the app store launcher itself). **A license audit of the imported catalog is a blocker for ISO bundling** — see §6 Risk #1.

---

## 2. Component-by-Component Analysis

Each component is rated **🟢 Adopt / 🟡 Adapt / 🔴 Skip** with rationale.

### 2.1 🟢 Capabilities-based provider switching

**Source:** `cloudd/src/model.rs` + `store/garage/manifest.yaml` + `bootstrap/install.sh`

The "capabilities" system is the single highest-value idea in AgentVerse-OS. Apps declare `provides: [storage.s3]` / `requires: [storage.s3]` in their manifest. Projects declare `capabilities: [storage.s3, llm, notify]` in `project.yaml`. The core:

1. Installs the chosen provider app (e.g. `garage`).
2. Calls the provider's `hooks/grant.sh` with the consumer project's name → provider creates per-project credentials (Garage: bucket + access key via Admin API).
3. Connects the project's per-project `gate` (a small Caddy instance) to the provider's network.
4. Drops env vars (`<PROVIDER>_URL`, `STORAGE_S3_ACCESS_KEY`, `STORAGE_S3_SECRET_KEY`, …) into the project's compose `.env`.
5. Workspace reaches the provider at `http://storage.s3.gate` (stable URL on the project's gate network).

Swapping Garage↔MinIO↔SeaweedFS changes the provider app, not the project. Same pattern applies to `llm` (provider = LiteLLM/Ollama), `notify` (ntfy), `git` (Gitea), etc.

**Why adopt in SCARLIX:**
- Hermes/OpenCode agents currently consume LiteLLM via hardcoded `:4000`. With capabilities, the SCARLIX family profiles (Zmor/Hugo/XOX/Mon) can request `llm` and the core resolves it to the best available provider (SGLang first, Ollama fallback, llama.cpp last — already the 3-tier failover model, now expressed as capabilities).
- Media apps (ComfyUI, MusicGen, video) currently hardcode paths to `/models` bind mounts. Capabilities let them request `storage.models` and get it from ZFS dataset, S3 bucket, or HuggingFace mirror interchangeably.
- The 21 existing SCARLIX containers can be retrofitted with manifest files in 1-2 days each.

**Adaptation work:**
- Port the 7-entity Rust domain model (`project, workspace, gate, app, capability, grant, route`) into a SCARLIX-native format. Two options:
  - **(A) Steal the schema, keep Go:** Translate the structs in `cloudd/src/model.rs` to Go structs in `scarlihq/internal/capabilities/`. This keeps ScarliHQ as the single core. **Estimated 1-2 weeks.**
  - **(B) Run cloudd side-car:** Package the Rust `cloudd` binary as a SCARLIX service, expose its `/api/*` over loopback, and let ScarliHQ proxy to it. **Estimated 3 days** for install, but creates a dual-core architecture (see §5 compat notes). **Recommended only if a rapid v17.2 demo is needed.**

**Adopt: 🟢** — capability manifests become the SCARLIX app descriptor format. All 21 existing containers get `manifest.yaml` + `compose.yaml` retrofits.

---

### 2.2 🟢 944-app self-hosted store

**Source:** `store/` (945 entries: 938 imported + 6 hand-written + `recommended.yaml`), `cloudd/src/store.rs` (16 KB), `cloudd/src/importer/{mod.rs(26 KB), runtipi.rs(14 KB), coolify.rs(9 KB), umbrel.rs(10 KB)}`.

The store is implemented as a filesystem of `<app-name>/{manifest.yaml, compose.yaml, hooks/}` directories. The importer pulls catalogs from upstream Runtipi/Coolify/Umbrel git repos at build time, generates the `manifest.yaml` from the upstream metadata, and tags `origin: runtipi | coolify | umbrel | manual`. `manual` apps are never overwritten by re-import.

**Why adopt in SCARLIX:**
- SCARLIX currently has 21 hand-built containers — adding the 22nd requires writing a Dockerfile + compose + ScarliHQ profile. The AgentVerse store pattern makes adding app #22 a single `manifest.yaml` + `compose.yaml` + optional `hooks/grant.sh`.
- 944 pre-vetted manifests dramatically expand family-entertainment catalog (Nextcloud, Immich, Vaultwarden, Audiobookshelf, Jellyfin, Navidrome, Photoprism, Home Assistant, …).
- `recommended.yaml` curator file is a perfect place to mark family-friendly apps for the 4 SCARLIX profiles.

**Adaptation work:**
- Vendor the `store/` directory into `scarlix-os-v12-repo/store/`.
- Re-implement the importer in Go (or run the Rust importer as a build-time tool, generating `manifest.yaml` files committed to the repo).
- Audit licenses per app: **Runtipi** is BSD-3-Clause-compatible, **Coolify** is AGPL-3.0 (but only Coolify's *own* compose snippets are AGPL; the catalog entries describe third-party apps under their own licenses), **Umbrel's catalog** is MIT but the Umbrel *launcher* is Polyform Non-Commercial. Need to verify each app's compose license before bundling into the SCARLIX ISO.
- Map capability names to SCARLIX providers: `storage.s3` → MinIO (already bundled? or add Garage), `llm` → LiteLLM endpoint at `:4000`, `notify` → ntfy (new in SCARLIX).

**Adopt: 🟢** — full catalog vendoring + curated subset preinstalled on family ISO. Curation (which of 944 are default-installed) is a profile decision.

---

### 2.3 🟢 Browser-windowed desktop (Svelte 5 PWA)

**Source:** `desktop/` (Svelte 5 + Vite, `npm run build` → `cloudd/static/`), embedded into Rust binary via `rust-embed`.

The Desktop ships windows, taskbar, start menu, widgets (monitor/clock/news/weather/projects), Files over app data, Passwords & Access, themes/wallpapers, boot screen, offline overlay, screensaver. 4-language i18n (EN/RU/UK/ES) lives in `src/lib/i18n/`. Playwright e2e in `desktop/e2e/`.

**Why adopt in SCARLIX:**
- SCARLIX's current 2D dashboard (HTML/HTMX embedded in Go) is functional but not desktop-grade. The 4 family profiles (Zmor/Hugo/XOX/Mon) cry out for distinct wallpapers + themes — exactly what this provides.
- Mobile/tablet form factor matters for the "family entertainment" use case (wife's tablet, kids' phones) — the AgentVerse PWA gives SCARLIX a phone home screen with no extra work.
- Boot screen + offline overlay + screensaver are needed polish for a "Linux OS for the family" product.

**Adaptation work:**
- The Desktop currently talks to `cloudd`'s `/api/*`. Two integration paths:
  - **(A) Rebuild backend:** Rewrite the `cloudd` API client in the Desktop to hit ScarliHQ's REST + WebSocket endpoints. ScarliHQ's existing `:8090` API surface needs to grow the `cloudd`-shaped routes (projects, workspaces, capabilities, store). **Estimated 2-3 weeks** (Desktop work + ScarliHQ route additions).
  - **(B) Run cloudd side-car:** Run `cloudd` next to ScarliHQ on `:8091`. Desktop hits cloudd, cloudd proxies "Hermes/OpenCode/mode" calls back to ScarliHQ. Quicker to demo, messier long-term.
- Localize to 4 SCARLIX languages (EN/RU/UK/ES — same set).
- Add 4 profile themes already designed in SCARLIX (Iron Man/Gaming/Creative/Elegant) as Svelte theme presets.

**Adopt: 🟢** — Svelte Desktop becomes the SCARLIX primary UI; HTMX 2D dashboard remains as the lightweight fallback for low-resource devices.

---

### 2.4 🟡 Incus workspaces with VS Code + Claude Code + Codex

**Source:** `templates/incus/{main.tf (14 KB), coder-compose.yml (3 KB), .terraform.lock.hcl}`, `cloudd/src/{incus.rs(13 KB), coder.rs(8 KB)}`, `bootstrap/coder/`, `bootstrap/install.sh` (Tailscale + Coder + Komodo section).

Each project provisions an **Incus** container (or VM, or nesting container with Docker inside) via Terraform + Coder. Inside the workspace:
- VS Code (code-server) on `:8444` accessed via the Desktop
- A terminal (web)
- Claude Code + Codex CLI agents, with auth-state persisted across stop/start (systemd unit reads fresh token + init script from Incus guest API `/dev/incus/sock` on every boot)
- `/home/coder` is a separate ZFS volume keyed by an immutable workspace ID — survives instance recreation
- `DISABLE_AUTOUPDATER=1` is set for Claude Code (threat-model decision: explicit version bumps only)

**Why adapt (not adopt wholesale):**
- SCARLIX already runs Hermes + OpenCode + gstack on the host directly inside Docker containers. Adding per-project Incus isolation is a **new capability** (multi-project dev work, OS-level isolation for untrusted agent code), not a replacement.
- Incus on EndeavourOS: Arch's `incus` package works, `incus-nesting` is supported, `incus-vm` works with KVM. The Coder server has Arch packages. No fundamental blocker.
- **Adaptation:** Run Incus + Coder as **optional** SCARLIX services (not default). Profile `dev` (new) gets a per-project workspace; family profiles don't pay the overhead.

**Adaptation work:**
- Port `templates/incus/main.tf` (use Ubuntu image as default, but add an EndeavourOS/Arch image option for SCARLIX-native workspaces).
- Vendor `bootstrap/coder/` and `bootstrap/komodo/` compose files into `scarlix-os-v12-repo/workspaces/{coder,komodo}/`.
- The Coder → Incus integration needs the Incus unix socket mounted into Coder + `incus-admin` group membership. Already documented in `coder-compose.yml`.
- **ZFS dataset for Incus pool:** SCARLIX uses BTRFS — see §5.2 for the ZFS-on-LUKS vs BTRFS subvolume decision. **Recommended:** add an optional second disk (or BTRFS subvolume) for `tank/workspaces` even on the BTRFS root.

**Adopt (adapted): 🟡** — Incus + Coder + Komodo become SCARLIX services behind the new `dev` profile. Optional, off-by-default for non-dev users.

---

### 2.5 🔴 Tailscale-only remote access (skip; SCARLIX keeps Headscale)

**Source:** `bootstrap/install.sh` (Tailscale auth-link/QR section), `bootstrap/cloudd.env.example` (`CLOUDD_EDGE_HOST=<box>.<tailnet>.ts.net` + `CLOUDD_TLS_INTERNAL=false` → tailscaled issues real Let's Encrypt certs), README ("Access only via Tailscale, nothing exposed to the internet").

AgentVerse-OS pins remote access to Tailscale (the SaaS), with MagicDNS + HTTPS Certificates from the tailnet. Real Let's Encrypt certs are issued by `tailscaled` itself, no root CA install on devices. No inbound ports opened to the internet.

**Why skip:**
- SCARLIX uses **Headscale** (self-hosted Tailscale-compatible coordination server) + Caddy auto-HTTPS + Authelia SSO (TOTP+WebAuthn 2FA) + CrowdSec WAF. This is strictly more sovereign than depending on Tailscale's SaaS for coordination. Headscale + Authelia gives SCARLIX 2FA, SSO, and WAF that AgentVerse-OS explicitly lacks (alpha 0.2 status notes: "no user accounts or permissions yet").
- AgentVerse-OS's "no port forwarding" property is identical to Headscale (both use WireGuard outbound dialing).

**However — borrow two ideas:**
- **(a) MagicDNS + HTTPS Certs:** Headscale has MagicDNS support. SCARLIX should configure Headscale + `tailscaled` (or `headscale`-controlled `tailscaled`) to issue Let's Encrypt certs via the ACME DNS-01 challenge against the tailnet domain. This replaces self-signed Caddy internal certs.
- **(b) First-run wizard with QR code:** The AgentVerse install.sh's QR code for Tailscale auth is a great UX. SCARLIX's first-boot wizard (`scarlix-wizard`) should gain a similar QR code for Headscale auth + the family-profile assignment.

**Skip: 🔴** for Tailscale-direct dependency. **Adopt: 🟢** for the QR-code wizard UX and the MagicDNS+Let's-Encrypt integration on Headscale.

---

### 2.6 🔴 ZFS + restic backup (skip; SCARLIX keeps BTRFS + Snapper + restic)

**Source:** `bootstrap/host/zfs-layout.sh` (6.4 KB) + `cloudd/src/backup.rs` (23 KB) + `cloudd/src/appdata.rs` (22 KB, automatic permission repair).

AgentVerse-OS's ZFS layout:
```
tank/core        → /var/lib/cloudos        (cloudd state + secrets + manifest repo; recordsize=128K)
tank/postgres    → /srv/postgres            (recordsize=16K, logbias=throughput)
tank/apps        → /srv/apps                (per-app child-dataset, bind-mount not named volume; recordsize=128K)
tank/workspaces  → none (Incus-managed)    (ZFS volume for Incus pool)
tank/docker      → /var/lib/docker          (recordsize=128K, auto-snapshot=false)
tank/backups     → /srv/backups             (local restic repo; recordsize=1M, auto-snapshot=false)
```
Mirror across two disks if `DISKS` lists ≥2. ARC capped at 6 GB by default. Per-app snapshots → per-app rollback (two clicks in Desktop).

**Why skip:**
- SCARLIX is EndeavourOS/Arch with BTRFS + Snapper already configured (`garuda-scarlix/airootfs/etc/snapper/configs/root`). Replacing BTRFS with ZFS would mean rebuilding the entire install pipeline (Calamares partition config, snapper hooks, btrfs-assistant UI) and re-validating with NVIDIA driver installer (ZFS-on-Root + NVIDIA DKMS is a known friction point on Arch).
- BTRFS subvolumes give SCARLIX most of what AgentVerse-OS gets from ZFS datasets (per-app subvolume, snapshots, transparent compression). The one ZFS feature SCARLIX loses is per-dataset `recordsize` tuning (BTRFS sector size is global per-fs).

**Borrow ideas:**
- **(a) Per-app data subvolumes:** SCARLIX should create `/@/apps/<name>` BTRFS subvolumes for each store app, mirroring `tank/apps/<name>` pattern. Per-app snapshot/rollback works identically.
- **(b) recordsize tuning:** Skip — BTRFS doesn't have per-subvolume sector size; the perf delta on consumer NVMe is <5%.
- **(c) Two-click app rollback UI:** Borrow the Desktop pattern (store.rs + appdata.rs logic). Add to ScarliHQ's REST API + the Svelte Desktop.
- **(d) Auto-permission-repair logic** (`appdata.rs`): SCARLIX Docker containers frequently have permission issues on bind mounts (UID mismatch). Borrow this repair logic into a `scarlix-fix-app-perms <app>` CLI.

**Skip: 🔴** for ZFS migration. **Adopt: 🟢** for per-app subvolumes + rollback UI + permission repair logic.

---

### 2.7 🟡 Rust cloudd core (vs Go ScarliHQ)

**Source:** `cloudd/` (Cargo.toml + src/ 21 files, ~370 KB Rust). Deps: axum 0.8, rusqlite 0.37 (bundled), bollard 0.19 (Docker), hyperlocal 0.9 (Unix socket HTTP for Caddy/Docker), utoipa 5 (OpenAPI), rust-embed 8 (Desktop embedded), age 0.11 (backup encryption), feed-rs (news widget), sysinfo (monitor). 60-route REST API + CLI + first-run wizard + updates + rollback.

ScarliHQ today is a Go binary at `:8090` with go:embed of a 2D dashboard + REST + WebSocket + MCP Server + Guard (destructive_command_guard) + Profile Manager + Memory (SQLite+cosine). Codebase is ~5 Go files (`cmd/scarlihq/main.go`, `internal/api/rest.go`, `internal/guard/guard.go`, `internal/profiles/loader.go`, `internal/scarlix_mode/mode.go`, `internal/mcp/server.go`, `internal/webui/ws.go`).

**Decision:** **Do not replace ScarliHQ with cloudd.** Instead:

- **Adopt cloudd's API contract as a side-car:** package the Rust binary as a SCARLIX service on `:8091`, expose its OpenAPI at `/api/openapi.json`. ScarliHQ proxies `/v2/projects/*`, `/v2/store/*`, `/v2/capabilities/*`, `/v2/workspaces/*` to cloudd. ScarliHQ keeps `/v1/mode`, `/v1/gpu`, `/v1/profiles`, `/v1/agents/*`, `/v1/memory`, `/mcp/*` (the AI-layer surface).
- **Long-term path (v18.0+):** If cloudd stabilizes and proves faster/safer than Go for the orchestration surface, slowly port ScarliHQ's `internal/api/rest.go` and `internal/profiles/loader.go` into Rust modules inside cloudd. Defer the full language consolidation decision to v18.0+.
- **Compatibility:** Both binaries use SQLite. They can share `/var/lib/scarlix/state.db` via separate tables (`scarlix_*` vs `cloudos_*` namespaces) or use separate DBs. Separate DBs recommended.

**Why adapt (not adopt):**
- Replacing ScarliHQ wholesale would lose: ScarliHQ's MCP Server (for Hermes delegation), the destructive_command_guard (critical for family use), the 4-profile family model with token limits, the 6 GPU modes / `scarlix-mode` script integration, and the existing Arch packaging.
- Replacing cloudd wholesale would lose: the 60-route orchestration API, the OpenAPI doc, the Coder/Incus/Komodo adapters, the store importer, the backup subsystem.

**Adopt (side-car): 🟡** — cloudd runs as SCARLIX service `scarlix-cloudd.service` on `:8091`. ScarliHQ proxies orchestration routes to it. Both cores coexist until v18.0+ consolidation review.

---

### 2.8 🟢 Bootstrap + install.sh patterns (adapt for EndeavourOS)

**Source:** `bootstrap/install.sh` (42 KB) + `bootstrap/update.sh` (26 KB, installed as `agentverse-update`) + `bootstrap/host/install-host.sh` (6.4 KB) + `bootstrap/{coder,docker-proxy,edge,komodo,systemd}/`.

Install steps: host checks → ZFS → Docker + Incus → **Tailscale** (auth link + QR + MagicDNS + HTTPS cert verify) → Coder → Komodo → edge (Caddy) → core (cloudd) → catalog (import Runtipi+Coolify+Umbrel) → template → summary. `PACKAGE=` env var lets bootstrap install from a release tarball or local build.

`update.sh` is a self-update channel client: pulls `stable.json` from GitHub Releases, downloads `agentverse-os-<version>.tar.gz`, swaps in via tarball, **automatic rollback if the new version doesn't come up** (smoke-checks after restart, falls back on failure).

**Why adopt (adapt):**
- SCARLIX currently ships via Garuda ISO + Calamares + `first-boot.sh`. There is no over-the-air update channel for SCARLIX today. The `update.sh` + `stable.json` + smoke-check-rollback pattern is exactly what SCARLIX needs for v18.0.
- Tailscale auth via QR code at first boot is a great UX to add to `scarlix-wizard`.

**Adaptation work:**
- Strip ZFS + Tailscale + Coder + Komodo steps; keep Docker + Incus (optional, dev profile) + edge (Caddy) + core + catalog + summary.
- Replace Tailscale step with Headscale enrollment step (similar UX, different API).
- Replace `stable.json` GitHub Releases channel with SCARLIX's own release pipeline (GitHub Releases works the same way).
- `update.sh` becomes `scarlix-update` (already a placeholder concept in SCARLIX). Self-update with rollback becomes a flagship v18.0 feature.

**Adopt (adapted): 🟢** — bootstrap's update.sh + stable.json + smoke-check-rollback pattern is the SCARLIX v18.0 OTA foundation.

---

### 2.9 🟢 Komodo as App Runtime

**Source:** `bootstrap/komodo/` (compose files) + `cloudd/src/komodo.rs` (8 KB).

Komodo is the deploy engine that turns a store app's `compose.yaml` into a running Docker stack. cloudd calls Komodo's API; Komodo handles `docker compose up -d`, secrets, networks, and per-stack lifecycle.

**Why adopt:**
- SCARLIX today runs each container via a directly authored `docker-compose.yml` + `systemctl start docker-compose@<name>`. That pattern doesn't scale to 944 apps — you want a real deploy runtime that handles secrets, restart-on-failure, and stack-per-network isolation.
- Komodo is Apache-2.0 and self-hostable on Arch.

**Adaptation work:**
- Vendor `bootstrap/komodo/` into `scarlix-os-v12-repo/orchestration/komodo/`.
- Wire ScarliHQ's `app install` / `app remove` / `app logs` routes to Komodo's API (cloudd already has this wiring — port or proxy).
- For the existing 21 hand-built containers, **do not** migrate them to Komodo in v17.x — keep them as `systemd`-managed compose stacks. Only new store-app installs go through Komodo in v17.2. Slow migration in v18.0.

**Adopt: 🟢** — Komodo is the App Runtime for catalog apps. Existing 21 hand-built containers stay on the direct compose path for now.

---

### 2.10 🟢 Per-project gate (per-project Caddy)

**Source:** `cloudd/src/gate.rs` (2 KB) + `cloudd/src/caddy.rs` (11 KB) + `bootstrap/edge/`.

Each project gets a `gate-<project>` Caddy instance that is the **only door** into the workspace's Incus container. The gate also routes capability requests: `http://storage.s3.gate` → the installed S3 provider's network. Workspace reaches providers through the gate, never directly.

**Why adopt:**
- SCARLIX's Caddy config (`network/caddy/Caddyfile`) is monolithic. Per-project gates add proper isolation for the multi-project dev scenario (Zmor working on a personal project in workspace `alpha` while Hugo's gaming container doesn't see it).
- The capability routing model requires per-project gates.

**Adaptation work:**
- Add a `gate-<project>` Caddy service per workspace, on the project's own Docker network.
- ScarliHQ's existing `network/caddy/Caddyfile` becomes the "edge" (port :443) that proxies to per-project gates on internal ports.

**Adopt: 🟢** — per-project gates are part of the capabilities system adoption (§2.1).

---

### 2.11 🟡 Two assistants already in catalog: Hermes + Pipecat Voice

**Source:** `store/recommended.yaml` (lists `pipecat-voice` and `hermes-agent` as the first two recommended apps) + README mentions.

AgentVerse-OS ships **Hermes Agent** (with its native dashboard — chat terminal, skills, cron, messengers) and **Pipecat Voice** (a voice assistant in the browser with local Whisper + Piper + LLM through an OpenAI-compatible API) as catalog entries.

**Why adapt (not adopt):**
- SCARLIX already runs Hermes + OpenCode + gstack. Adding Pipecat Voice as the catalog app gives SCARLIX a web-based voice assistant — currently SCARLIX has `voice/whisper`, `voice/piper`, and `voice/wakeword` as separate containers without a unified browser surface.
- The catalog manifest pattern (one `manifest.yaml` + `compose.yaml` + `hooks/`) replaces the ad-hoc SCARLIX `voice/whisper/docker-compose.yml` etc.

**Adaptation work:**
- Convert `voice/{whisper,piper,wakeword}` into a single `pipecat-voice` store app, reusing the existing whisper + piper containers + adding a Pipecat server container.
- Mark `pipecat-voice` and `hermes-agent` as `recommended` in the SCARLIX curated store.
- The "voice control of the system itself" AgentVerse-OS roadmap item is also on SCARLIX's v18.0 roadmap (worklog Task #4).

**Adopt: 🟡** — replace SCARLIX's 3-container voice stack with the `pipecat-voice` catalog app, integrate with ScarliHQ's mode/profile API so voice can switch GPU modes.

---

### 2.12 🔴 Single-user model (skip)

**Source:** README "It works as a personal server for one person; there are no user accounts or permissions yet."

AgentVerse-OS is explicitly single-user. SCARLIX is explicitly multi-user family (4 profiles with token limits + themes). **Skip this design choice entirely** — keep SCARLIX's family model. The capabilities system + per-project workspaces layer cleanly on top of family profiles: each profile gets its own `projects/` directory and project grants.

**Skip: 🔴** — SCARLIX keeps its multi-user family model.

---

### 2.13 🟢 Self-update with rollback

**Source:** `cloudd/src/updates.rs` (45 KB — largest non-service file in cloudd) + `bootstrap/update.sh` + the `stable.json` update channel.

Self-update package → install → smoke-check (does the new core come up? does the API respond? do existing apps still respond?) → if any check fails, **automatic rollback to previous version**. Update channel is a single `stable.json` file on GitHub Releases.

**Why adopt:**
- SCARLIX today has no OTA update mechanism — reinstall from new ISO is the only upgrade path. This is the largest gap in SCARLIX's "sovereign OS" pitch today.
- The rollback-on-failure pattern is essential for a family-targeted OS (non-technical users cannot recover from a broken boot).

**Adaptation work:**
- Port `updates.rs` logic into either Go (in ScarliHQ) or run as part of cloudd side-car.
- Define SCARLIX's smoke-check contract: ScarliHQ responds on :8090, Docker daemon healthy, LiteLLM responds on :4000, SGLang responds on :8088 (or Ollama :11434 in fallback), first profile (Zmor) can log in.
- Set up `stable.json` channel on SCARLIX's GitHub Releases.
- Wire into `scarlix-update` CLI; document in `docs/UPDATE_POLICY.md` (new).

**Adopt: 🟢** — flagship v18.0 feature.

---

## 3. Phased Integration Roadmap

### v17.2 — Foundation (4-6 weeks)

**Theme:** Bring the catalog and capabilities schema into SCARLIX, without changing the runtime yet.

| # | Deliverable | Files added/changed | Risk |
|---|-------------|---------------------|------|
| 1 | Vendor `store/` directory (944 apps) + `recommended.yaml` into `scarlix-os-v12-repo/store/` | `store/` (944 dirs) | License audit must precede ISO bundling |
| 2 | Add `scarlihq/internal/capabilities/` Go package — port the 7-entity domain model (`ProjectSpec`, `WorkspaceSpec`, `AppManifest`, `Runtime`, `RouteSpec`, `Endpoint`, `Hooks`, `LoginSpec`, `Recommended`) from `cloudd/src/model.rs` | `scarlihq/internal/capabilities/{model.go, manifest.go, project.go}` | Low — pure data types |
| 3 | Retrofit the 21 existing SCARLIX containers with `manifest.yaml` files (origin: manual, provides: <capability>) | 21 new manifest.yaml files | Low |
| 4 | Add `scarlix-fix-app-perms <app>` CLI — port of `cloudd/src/appdata.rs` permission repair logic | `scarlihq/cmd/scarlihq/fixperms.go` | Low |
| 5 | Add BTRFS per-app subvolume creation script for new app installs | `scripts/create-app-subvol.sh`, `scripts/rollback-app.sh` | Low |
| 6 | Run license audit script on `store/` — categorize each app as `bundle-ok` / `bundle-block` / `opt-in-only` | `scripts/audit-store-licenses.sh`, `docs/STORE_LICENSE_AUDIT.md` (new) | 🔴 High — blocks v17.2 ISO if any AGPL/Polyform NC apps are bundled |
| 7 | Prototype: install one catalog app (e.g. `vaultwarden`) via `docker compose -f store/vaultwarden/compose.yaml up -d` on a SCARLIX test box, verify it works end-to-end | `docs/V17.2_STORE_PROTOTYPE.md` (new) | Low |

**Exit criteria for v17.2:** Catalog vendored, manifests for 21 SCARLIX containers in place, license audit done, one prototype app installed from the catalog.

---

### v17.3 — Cloudd Side-Car + Desktop Preview (6-8 weeks)

**Theme:** Bring up the Rust cloudd side-car + Svelte Desktop as a parallel UI surface, with ScarliHQ proxying to it.

| # | Deliverable | Files added/changed | Risk |
|---|-------------|---------------------|------|
| 1 | Build cloudd from source on EndeavourOS — package as `scarlix-cloudd` AUR package + `scarlix-cloudd.service` systemd unit on `:8091` | `aur/scarlix-cloudd/`, `garuda-scarlix/airootfs/etc/systemd/system/scarlix-cloudd.service` | 🟠 Medium — Rust compilation on Arch is straightforward but bundling `cloudd` in the ISO adds ~30 MB |
| 2 | Configure cloudd to use SCARLIX's Docker daemon (no separate Komodo/Coder/Incus yet — just the store + capabilities + projects APIs) | `bootstrap/cloudd.env` SCARLIX-flavored | Low |
| 3 | Add ScarliHQ proxy routes: `/v2/projects/*` → `localhost:8091/api/projects/*`, `/v2/store/*`, `/v2/capabilities/*` | `scarlihq/internal/api/rest.go` (add proxy handlers) | Low |
| 4 | Build the Svelte Desktop, target `cloudd/static/` — configure it to use ScarliHQ's `/v1/*` for AI-layer (mode, GPU, profiles, memory) and cloudd's `/v2/*` for orchestration | `desktop/` (vendored from AgentVerse-OS), `desktop/src/lib/api/scarlihq.ts` (new) | 🟠 Medium — Desktop currently has no ScarliHQ client; need to write the TS bindings |
| 5 | Add 4 SCARLIX profile themes (Iron Man / Gaming / Creative / Elegant) as Svelte theme presets in `desktop/src/lib/themes/` | 4 new theme presets | Low |
| 6 | Add Headscale + Caddy config for the Desktop — Desktop served at `https://<host>.<tailnet>/`, MagicDNS + Let's-Encrypt via tailscaled controlled by Headscale | `network/headscale/docker-compose.yml`, `network/caddy/Caddyfile` updates | 🟠 Medium — Headscale's `tailscaled` client doesn't yet issue ACME certs (need to verify; if not, fall back to Caddy's internal CA + DNS-01 challenge) |
| 7 | QR-code enrollment in `scarlix-wizard` for Headscale auth (port from AgentVerse `bootstrap/install.sh`'s Tailscale QR section) | `garuda-scarlix/airootfs/usr/local/bin/scarlix-wizard` | Low |
| 8 | Playwright e2e tests for the Desktop on SCARLIX (smoke: login, open store, install one app, see it running) | `desktop/e2e/scarlix.spec.ts` | Low |

**Exit criteria for v17.3:** Desktop accessible via Headscale URL, can browse 944-app store, can install one app end-to-end with per-app subvolume + rollback working. ScarliHQ HTMX dashboard still functions as fallback.

---

### v18.0 — Workspaces + Self-Update + Voice (10-12 weeks)

**Theme:** Add per-project Incus workspaces, OTA self-update with rollback, and unify the voice stack.

| # | Deliverable | Files added/changed | Risk |
|---|-------------|---------------------|------|
| 1 | Vendor `bootstrap/{coder,komodo}/` + `templates/incus/` into `scarlix-os-v12-repo/workspaces/` | `workspaces/{coder,komodo,incus-template}/` | 🟠 Medium — Incus on EndeavourOS is supported but not yet validated on the SCARLIX ISO |
| 2 | Add `dev` profile (5th profile, alongside Zmor/Hugo/XOX/Mon) — gets per-project Incus workspaces with VS Code + Claude Code + Codex CLI | `profiles/dev.yaml`, `garuda-scarlix/airootfs/etc/scarlix/profiles/dev.yaml` | Low |
| 3 | Port `templates/incus/main.tf` with EndeavourOS image option alongside Ubuntu 24.04 | `workspaces/incus-template/main.tf` | 🟠 Medium — building a custom EndeavourOS cloud-init image is non-trivial; defer to v18.1 if needed, ship with Ubuntu 24.04 image only |
| 4 | Add `scarlix-update` CLI + `stable.json` GitHub Releases channel + smoke-check rollback | `scripts/scarlix-update.sh`, `.github/workflows/release.yml`, `docs/UPDATE_POLICY.md` | 🔴 High — rollback logic must be bulletproof; extensive testing needed |
| 5 | Convert `voice/{whisper,piper,wakeword}` into single `pipecat-voice` store app with Pipecat server + voice-controlled ScarliHQ mode switching | `store/pipecat-voice/{manifest.yaml, compose.yaml, hooks/}` | 🟠 Medium — Pipecat bot framework integration with ScarliHQ's mode API is new work |
| 6 | Migrate the 21 existing hand-built containers from direct `systemd` compose to Komodo-managed stacks (where license allows) | 21 manifest.yaml updates | 🟠 Medium — gradual migration; keep systemd path as fallback |
| 7 | Per-project gates (one Caddy per project's network) wired into ScarliHQ's `/v2/projects/<name>/gate` endpoint | `scarlihq/internal/gate/`, `network/caddy/per-project/` | 🟠 Medium |
| 8 | Long-term cloudd-vs-ScarliHQ consolidation review: based on v17.3 + v18.0 experience, decide whether to (A) keep dual-core, (B) port ScarliHQ's surface into Rust, or (C) port cloudd's orchestration into Go | `docs/V18_CORE_CONSOLIDATION_REVIEW.md` (new) | Decision deferred to v18.1 |

**Exit criteria for v18.0:** Per-project workspaces available via `dev` profile, `scarlix-update` ships an OTA update with rollback, voice is unified, Komodo is the default app runtime.

---

## 4. Component Adoption Matrix

| # | AgentVerse-OS Component | Verdict | Target Version | Owner |
|---|--------------------------|---------|----------------|-------|
| 2.1 | Capabilities-based provider switching | 🟢 Adopt | v17.2 | ScarliHQ Go package |
| 2.2 | 944-app store (Runtipi+Coolify+Umbrel merger) | 🟢 Adopt | v17.2 | Vendor `store/` |
| 2.3 | Browser-windowed desktop (Svelte 5 PWA) | 🟢 Adopt | v17.3 | New `desktop/` directory |
| 2.4 | Incus workspaces (VS Code + Claude Code + Codex) | 🟡 Adapt (dev profile, optional) | v18.0 | New `workspaces/` directory |
| 2.5 | Tailscale-only remote access | 🔴 Skip (keep Headscale); adopt QR wizard + MagicDNS+ACME | v17.3 | network/ updates |
| 2.6 | ZFS + restic backups | 🔴 Skip (keep BTRFS+Snapper); adopt per-app subvol + rollback UI + perm repair | v17.2 | scripts/ updates |
| 2.7 | Rust cloudd core | 🟡 Adapt (side-car on :8091; consolidation review at v18.1) | v17.3 | aur/scarlix-cloudd |
| 2.8 | Bootstrap install.sh patterns | 🟢 Adopt (adapted — strip ZFS+Tailscale, keep update.sh + stable.json + rollback) | v18.0 | scripts/scarlix-update.sh |
| 2.9 | Komodo App Runtime | 🟢 Adopt | v17.3 | orchestration/komodo/ |
| 2.10 | Per-project gates (per-project Caddy) | 🟢 Adopt | v18.0 | network/caddy/per-project/ |
| 2.11 | Hermes + Pipecat Voice in catalog | 🟡 Adapt (replace 3-container voice stack with one `pipecat-voice` app) | v18.0 | store/pipecat-voice/ |
| 2.12 | Single-user model | 🔴 Skip | — | (SCARLIX stays multi-user) |
| 2.13 | Self-update with rollback | 🟢 Adopt | v18.0 | scripts/scarlix-update.sh |

---

## 5. Technical Compatibility Notes

### 5.1 Rust cloudd vs Go ScarliHQ

| Aspect | cloudd (Rust) | ScarliHQ (Go) | Compatibility |
|--------|---------------|---------------|---------------|
| Web framework | axum 0.8 | net/http | Both have route handlers; ScarliHQ can `httputil.NewSingleHostReverseProxy` to cloudd |
| State | rusqlite 0.37 (bundled SQLite) | database/sql + mattn/go-sqlite3 | Use separate DB files (`/var/lib/scarlix/state.db` for ScarliHQ, `/var/lib/cloudos/cloudd.db` for cloudd); never share a SQLite handle across processes |
| Docker client | bollard 0.19 (Rust) | os/exec `docker compose` CLI | Both work; ScarliHQ will continue using CLI for the 21 hand-built containers, cloudd uses bollard for store apps |
| API doc | utoipa 5 (OpenAPI auto) | none (manually documented) | ScarliHQ can re-export cloudd's OpenAPI at `/v2/openapi.json` |
| Embedding | rust-embed 8 (Desktop → binary) | go:embed (HTML → binary) | Both embed assets; ScarliHQ serves HTMX dashboard at `/`, ScarliHQ proxies Desktop at `/desktop/*` (served by cloudd) |
| Async runtime | tokio | goroutines | Both async; no cross-boundary issue because they communicate via HTTP/JSON |
| Concurrency model | multi-threaded, futures | M:N goroutines | Both scale to thousands of connections on a single home server |
| Binary size | ~30 MB (with bundled SQLite + age + tar + flate2) | ~15 MB | Acceptable for SCARLIX ISO |
| Memory footprint | ~50 MB resident (est.) | ~20 MB resident | Negligible on 32 GB+ home server |
| Build on Arch | `cargo build --release` (works) | `go build` (works) | Both compile cleanly on EndeavourOS via `rustup`/`go` packages |

**Recommendation:** Run cloudd as a side-car on `:8091`, sharing the host's Docker socket. ScarliHQ proxies `/v2/*` routes to it. Both processes are managed by systemd. They share the host's Docker, Caddy, and (future) Komodo + Incus + Coder dependencies but maintain separate state.

### 5.2 Incus vs Docker

| Aspect | AgentVerse-OS | SCARLIX v17.0 | Path forward |
|--------|---------------|---------------|--------------|
| Workspace isolation | Incus system container (or VM, or nesting) per project | None (all containers on host Docker) | Add Incus as opt-in for `dev` profile only |
| Docker-in-workspace | Yes (`incus-nesting` runtime) | N/A (Docker is the host runtime) | Workspace has its own Docker daemon inside Incus |
| VS Code in workspace | code-server on `:8444` | None | Add for `dev` profile |
| Provisioning | Terraform + Coder | Manual `docker compose up -d` | Vendor `templates/incus/main.tf`; Coder manages lifecycle |
| Persistence | `/home/coder` as separate ZFS volume | N/A | Use BTRFS subvolume `@/workspaces/<id>/home` instead |
| Image | Ubuntu 24.04 cloud image (default) | N/A | Add EndeavourOS image build in v18.1; ship with Ubuntu 24.04 image only in v18.0 |
| Arch package | `incus` (AUR + community) | Not installed | Install via `pacman -S incus incus-tools` in `dev` profile |
| Coder | Coder server on host (`:7080` internal, `:8444` proxied) | Not installed | Install via Docker compose; mount Incus socket + add `incus-admin` group |

**Note on Incus-vs-Docker:** Incus is **not** a Docker replacement. Incus runs system containers (full OS) for workspaces; Docker runs app containers inside the workspace. SCARLIX's existing 21 hand-built containers stay on host Docker. Incus is purely for the workspace isolation layer (dev profile).

### 5.3 ZFS vs BTRFS

| Aspect | AgentVerse-OS (ZFS) | SCARLIX v17.0 (BTRFS + Snapper) | Path forward |
|--------|----------------------|--------------------------------|--------------|
| Filesystem | ZFS (zstd compression, xattr=sa, acltype=posixacl) | BTRFS (zstd compression, default subvolumes) | Keep BTRFS |
| Per-dataset recordsize | Yes (16K for Postgres, 128K default, 1M for backups) | No (BTRFS sector size is global) | Accept <5% perf delta on consumer NVMe |
| Snapshots | Per-dataset snapshots, scheduled | Snapper per-subvolume, scheduled | Keep Snapper; add per-app subvolumes |
| Per-app rollback | Yes (`zfs rollback tank/apps/<name>@snap`) | Yes (`btrfs subvolume snapshot` + atomic swap) | Same UX, different command |
| Mirror across disks | Yes (`mirror` vdev) | Yes (BTRFS RAID1 metadata+data) | Equivalent |
| ARC vs page cache | ARC capped at 6 GB by default | Linux page cache (uncapped) | BTRFS uses page cache; SCARLIX server has 32+ GB, no concern |
| NVIDIA DKMS | Works with ZFS 2.x | Works with BTRFS (no DKMS needed) | ZFS-on-root + NVIDIA DKMS is a known Arch friction point; staying on BTRFS avoids it |
| Calamares support | Limited | Native | SCARLIX ISO uses Calamares; BTRFS is the default in Garuda's profiledef |
| License | CDDL (incompatible with kernel GPL, hence out-of-tree) | GPL (in-tree) | SCARLIX sovereign distribution prefers in-tree GPL |

**Recommendation:** Stay on BTRFS. Borrow the per-app subvolume pattern, the per-app rollback UI, and the auto-permission-repair logic. Skip ZFS entirely.

### 5.4 Caddy vs Caddy (both use Caddy, but at different layers)

| Aspect | AgentVerse-OS | SCARLIX v17.0 |
|--------|---------------|---------------|
| Edge | One Caddy (`cloudos-edge`) on :443 → Desktop, :8444 → Coder, :8450+ → apps | One Caddy on :80/:443 → ScarliHQ :8090 |
| TLS | Real Let's-Encrypt cert issued by tailscaled (`CLOUDD_TLS_INTERNAL=false`); fallback internal CA when Tailscale not yet joined | Self-signed Caddy internal CA (no MagicDNS integration yet) |
| Per-project | Per-project `gate-<project>` Caddy instance (project's only door to workspace) | None — single monolithic config |

**Path forward:** SCARLIX adopts the per-project gate pattern (v18.0). For TLS, SCARLIX configures Headscale's `tailscaled` client to issue ACME certs against the headscale domain (verify Headscale supports this — if not, fall back to Caddy internal CA + DNS-01 challenge via Cloudflare/Desec API).

### 5.5 Authelia vs (no SSO in AgentVerse)

AgentVerse-OS has **no SSO** (single-user, alpha 0.2). SCARLIX keeps **Authelia** (TOTP + WebAuthn 2FA) for the family multi-user model. The Desktop will require Authelia session cookies (set by the edge Caddy's `forward_auth` directive) — this is a ScarliHQ-side addition, not a cloudd concern.

### 5.6 Updates: cloudd `updates.rs` vs (no OTA in SCARLIX today)

| Aspect | cloudd | SCARLIX v17.0 |
|--------|--------|---------------|
| Update unit | Release tarball (`agentverse-os-<version>.tar.gz`) with cloudd binary + Desktop static + bootstrap/ + store/ + templates/ | None — reinstall from new ISO |
| Update channel | `stable.json` on GitHub Releases | None |
| Smoke check | After restart: API responds, key components up; rollback if fail | None |
| Rollback mechanism | Keeps previous version on disk; systemd `ExecStartPost` smoke check swaps back on failure | None |

**Path forward:** Adopt this pattern wholesale in v18.0. SCARLIX release tarball = `scarlix-os-<version>.tar.gz` with ScarliHQ Go binary + cloudd Rust binary + Desktop static + manifest store + bootstrap scripts + Caddy config. Smoke check contract: ScarliHQ responds on :8090, cloudd responds on :8091, Docker daemon healthy, LiteLLM responds on :4000 (with 60s grace for cold model load), Zmor profile login works.

---

## 6. Risk Assessment

### 🔴 High Risks

**R1: License audit of the 938 imported catalog apps**
- **Risk:** Runtipi catalog is BSD-3-Clause-compatible, but Coolify's own compose snippets are AGPL-3.0 (coolify-coolify apps), and some Umbrel apps are Polyform Non-Commercial. Bundling AGPL-3.0 or NC apps in the SCARLIX ISO would either trigger copyleft obligations or violate the license.
- **Mitigation:** Run a per-app license audit script (`scripts/audit-store-licenses.sh`) before v17.2 ISO build. Categorize each app: `bundle-ok` (MIT/Apache/BSD), `bundle-block` (AGPL/NC/GPL), `opt-in-only` (source-available). Strip `bundle-block` apps from the default ISO; expose them via "user pulls themselves" install path post-install. Document in `docs/STORE_LICENSE_AUDIT.md`.
- **Owner:** legal/research agent + release engineer.

**R2: Self-update rollback correctness (v18.0)**
- **Risk:** A buggy `scarlix-update` could brick the family server. Rollback must be bulletproof — power loss during update, partial download, smoke-check false-positive, etc.
- **Mitigation:** Stage updates: download → verify SHA256 → stage on disk → swap atomically via symlink → smoke-check → keep previous version for 30 days → auto-purge. Test on a VM with simulated power-loss mid-update before shipping.
- **Owner:** release engineer.

**R3: cloudd + ScarliHQ dual-core operational complexity**
- **Risk:** Two cores (Go + Rust) with overlapping concerns (both want to manage Docker, both have their own state, both have their own logs) creates debugging nightmares and inconsistent behaviors (e.g., cloudd creates a network that ScarliHQ doesn't know about).
- **Mitigation:** Clearly documented API boundary: ScarliHQ owns `/v1/*` (AI layer: mode, GPU, profiles, memory, agents, guard); cloudd owns `/v2/*` (orchestration: projects, workspaces, store, capabilities, gates, updates). ScarliHQ proxies but never re-implements `/v2/*`. cloudd never touches `/v1/*`. Document the boundary in `docs/CORE_BOUNDARY.md` (new). Add v18.1 review milestone for potential consolidation.
- **Owner:** ScarliHQ maintainer + cloudd (vendored) maintainer.

### 🟠 Medium Risks

**R4: Incus on EndeavourOS stability**
- **Risk:** Incus works on Arch, but the SCARLIX ISO with NVIDIA DKMS + BTRFS + Incus (which uses ZFS-on-Linux kernel modules optionally for its storage pool) could hit kernel module conflicts.
- **Mitigation:** Use Incus's `dir` or `btrfs` storage pool driver (not `zfs`) in SCARLIX. Test on a spare test rig before enabling in `dev` profile. Defer to v18.1 if v18.0 hits blockers.
- **Owner:** release engineer.

**R5: Svelte Desktop learning curve + i18n drift**
- **Risk:** The SCARLIX team's expertise is Go + HTMX + Python (AI stack). Adding Svelte 5 + Vite + TypeScript introduces a new toolchain to maintain. i18n drift between EN/RU/UK/ES dictionaries is a known Svelte-check failure pattern.
- **Mitigation:** Keep the existing HTMX dashboard as a documented fallback. Treat Svelte Desktop as the "premium" UI; both must work. Run `npm run check` (svelte-check) in CI to catch i18n drift.
- **Owner:** frontend maintainer (new role, or contracted).

**R6: Headscale ACME cert issuance via tailscaled**
- **Risk:** AgentVerse-OS uses Tailscale's SaaS tailscaled, which issues real Let's-Encrypt certs via Tailscale's ACME integration. Headscale + `tailscaled` client may not yet support this ACME flow (Headscale's feature parity with Tailscale SaaS is partial).
- **Mitigation:** Verify Headscale >= 0.23 supports `tailscale cert`. If not, use Caddy's DNS-01 challenge via a supported DNS provider (Cloudflare API token in SCARLIX env, or DeSEC). Document fallback in `docs/NETWORK.md`.
- **Owner:** network maintainer.

**R7: Catalog bloat in the ISO**
- **Risk:** Bundling 944 app manifests + compose.yaml files adds ~5-10 MB to the ISO (manifests are small, ~1 KB each). Not a disk issue, but catalog UI performance (loading 944 entries into the Svelte Store) could be sluggish on tablets.
- **Mitigation:** Lazy-load catalog by category (Runtipi/Coolify/Umbrel/Manual/Recommended); only show curated subset by default; full catalog on search. Add per-app icon CDN fetch (lazy).
- **Owner:** frontend maintainer.

### 🟡 Low Risks

**R8: cloudd Rust compilation time on ISO build**
- **Risk:** Adding cloudd to the ISO build pipeline adds ~3-5 minutes of Rust compilation per build.
- **Mitigation:** Use `cargo build --release` with `sccache`; pre-build binary in CI and download as a release artifact.

**R9: Coder template image availability**
- **Risk:** `images:ubuntu/24.04/cloud` is a LinuxContainers.org image. If their CDN is down during first workspace creation, the `dev` profile breaks.
- **Mitigation:** Vendor the image as a SCARLIX-hosted mirror on first ISO build, document `incus image import` command.

**R10: Pipecat + ScarliHQ mode-switching integration**
- **Risk:** Pipecat voice bot calling ScarliHQ's `/v1/mode` to switch GPU modes is new work; voice latency vs mode-switch latency mismatch.
- **Mitigation:** Use async fire-and-forget for mode switch (ack voice command immediately, switch in background, status update via SSE). Document in `docs/VOICE_INTEGRATION.md`.

---

## 7. Recommended Sequencing Summary

```
v17.2 (4-6 weeks)        v17.3 (6-8 weeks)         v18.0 (10-12 weeks)
────────────────         ─────────────────         ─────────────────
Vendor store/ (944)       Build cloudd side-car      Vendor workspaces/
Capabilities schema       Vendor Svelte Desktop     dev profile + Incus
21 manifest retrofits     4 SCARLIX theme presets   scarlix-update OTA
BTRFS per-app subvol      Headscale MagicDNS+ACME   Per-project gates
License audit             QR wizard                  Pipecat Voice unified
App-perm-repair CLI       ScarliHQ /v2 proxy        Komodo as App Runtime
1 catalog app prototype   Playwright e2e             Consolidation review
```

**Total elapsed:** 20-26 weeks (~5-6 months) to deliver all 13 components. v17.2 is low-risk (mostly data + scripts). v17.3 introduces the dual-core architecture (medium risk). v18.0 ships the flagship OTA + workspaces features (high risk per R2).

---

## 8. Open Questions for MoZoHuJa

1. **Relationship to upstream:** Should SCARLIX track the upstream `agentverse-os/AgentVerse-OS` directly (and rebase MoZoHuJa's fork as a tracking branch), or treat the fork as a frozen snapshot? Upstream has 981★ and is active — tracking may be worth it.
2. **Single-core vs dual-core:** Is the team open to consolidating on Rust long-term (option B in §2.7), or is Go canonical for ScarliHQ?
3. **License policy:** For AGPL-3.0 catalog apps (Coolify's own compose snippets, anything under Polyform NC), is the SCARLIX policy "opt-in post-install only" acceptable, or do we need to scrub them from the vendored `store/` entirely?
4. **Dev profile scope:** Should the new `dev` profile be a 5th family profile (alongside Zmor/Hugo/XOX/Mon) or a separate "developer mode" toggle orthogonal to family profiles?
5. **Voice roadmap:** Is unifying whisper+piper+wakeword into a single `pipecat-voice` catalog app the right move, or should we keep them as separate containers and just add a Pipecat orchestration layer on top?

---

## 9. References

- **Upstream:** https://github.com/agentverse-os/AgentVerse-OS (Apache-2.0, 981★, alpha 0.2)
- **Fork:** https://github.com/MoZoHuJa/AgentVerse-OS-ScarLiX-1s (forked 2026-09-18, identical content)
- **Source files inspected for this plan:**
  - `README.md` (11 312 bytes)
  - `cloudd/Cargo.toml` (Rust deps)
  - `cloudd/src/model.rs` (17 KB, domain model — 7 entities + 4 contracts)
  - `cloudd/src/main.rs` (12 KB, CLI surface)
  - `cloudd/src/{api,service,store,updates,backup,incus,coder,komodo,caddy,gate,docker,appdata,files,monitor,feeds}.rs` (file listing + sizes)
  - `cloudd/src/importer/{mod.rs(26 KB), runtipi.rs(14 KB), coolify.rs(9 KB), umbrel.rs(10 KB)}`
  - `store/` (945 entries: 938 imported + 6 manual + `recommended.yaml`)
  - `store/garage/{manifest.yaml, compose.yaml, hooks/{grant.sh, revoke.sh, common.sh}}` — sample capability provider
  - `store/recommended.yaml` — curator list
  - `bootstrap/install.sh` (42 KB), `bootstrap/update.sh` (26 KB), `bootstrap/cloudd.env.example`
  - `bootstrap/host/{install-host.sh(6.4 KB), zfs-layout.sh(6.4 KB), docker-daemon.json, docker-incus-firewall.sh}`
  - `bootstrap/{coder,docker-proxy,edge,komodo,systemd}/` (subdirs)
  - `templates/incus/{main.tf(14 KB), coder-compose.yml(3 KB), .terraform.lock.hcl}`
  - `desktop/{package.json, vite.config.ts, svelte.config.js, tsconfig.json, pnpm-workspace.yaml, e2e/}`
  - `projects/alpha/project.yaml` (sample project spec)
- **SCARLIX OS reference files inspected:**
  - `scarlihq/cmd/scarlihq/main.go` (Go binary, :8090, go:embed HTMX)
  - `scarlihq/internal/{api/rest.go, guard/guard.go, profiles/loader.go, scarlix_mode/mode.go, mcp/server.go, webui/ws.go}`
  - `docs/ARCHITECTURE.md` (5-layer model: inference+OS, workspace+infra, agents, ScarliHQ, family)
  - `network/{headscale,docker-proxy,caddy}/` (current SCARLIX remote-access stack)
  - `scripts/backup.sh` (current BTRFS+restic pattern)
  - `garuda-scarlix/airootfs/etc/snapper/configs/root` (Snapper config)
  - `profiles/{zmor,hugo,xox,mon}.yaml` (4 family profiles)
  - `voice/{whisper,piper,wakeword}/docker-compose.yml` (current voice stack)
- **Prior research:** `worklog.md` Task ID RESEARCH-1 (2026-09, 13-repo analysis, identified AgentVerse-OS as 🔴 High priority, recommended v17.2 evaluation)

---

*End of AV-MERGE-PLAN-1. Awaiting review by MoZoHuJa.*
