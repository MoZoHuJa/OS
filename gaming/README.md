# SCARLIX OS Gaming Stack — v19.3.1

Read-only inventory, profile-based optional installation, one manifest.
Implemented from the **Gaming Stack Integration Plan**, re-baselined onto
v19.3.1 and merged together with the Host Bridge workload lifecycle.

---

## Design rules (non-negotiable)

1. **Preserve the existing system.** Steam, Lutris, Wine, Wine Gecko, Wine
   Mono, native Sunshine, `scarlix-mode`, the Host-Bridge, family profiles
   and the existing Docker projects keep working exactly as before. The
   default install set is unchanged.
2. **Launchers are native desktop applications.** No game launcher goes into
   Docker. Docker remains for `sunshine-tv`, Jellyfin and Minecraft.
3. **The privilege boundary stays.** ScarliHQ has no `docker.sock`, no root,
   no arbitrary shell. The gaming surface in ScarliHQ is **read-only**.
4. **One authoritative manifest.** `files/usr/local/share/scarlix/gaming-packages.conf`
   is the single source of truth for BOTH the installer and the inventory
   detection. `packages.x86_64` remains the source of truth for base repo
   packages.
5. **Optional means truly optional.** Nothing here is installed by default
   beyond the existing base gaming block. `gaming-full` is opt-in.
6. **No removal, ever.** `scarlix-gaming-install` never removes packages and
   never touches game libraries, Wine prefixes or Steam user data. The
   headless profile only *reports* and documents manual removal.
7. **No compatibility over-promising.** A launcher being installed does not
   guarantee any specific Windows game, anti-cheat, DRM or controller works.
   Target-host validation is part of the hardware acceptance gate.

---

## Profiles

| Profile | What it contains | How to get it |
|---|---|---|
| `minimal` *(default)* | steam, wine, wine-gecko, wine-mono, lutris + explicit Vulkan loaders (64+32-bit) | already installed by `install.sh` via `packages.x86_64`; `scarlix-gaming-install --profile minimal` only repairs gaps |
| `full` | minimal + RetroArch (+ XMB/Ozone/GLUI menu assets), Gamescope, MangoHud, GameMode (64+32-bit), Heroic Games Launcher, itch, Bottles | `scarlix-gaming-install --profile full` (AUR parts require yay + a **non-root** user) |
| `headless` | nothing — report-only mode | `scarlix-gaming-install --profile headless` prints what is installed + manual removal guidance; for AI/server-only hosts, comment out the gaming block in `packages.x86_64` **before** running `install.sh` |

```bash
scarlix-gaming-install --list                 # manifest + detection state
scarlix-gaming-install --profile full         # install everything (idempotent)
scarlix-gaming-install --profile full --dry-run
```

Installer behavior: per-package error reporting (one failing package never
fails the whole run silently — the summary counts failures and the exit code
is 1 if anything failed), `--needed` idempotency, disk-space warning, AUR
installs as the invoking user only (root is refused for AUR with a clear
message), `--dry-run` prints exact commands.

---

## Channel decisions (v19.3.1)

- **Heroic** — AUR `heroic-games-launcher-bin` (validated by the integration
  plan's baseline research; upstream documents AUR as the Arch path).
- **itch** — AUR `itch-setup-bin` (ArchWiki-listed).
- **Bottles** — AUR `bottles`. Rationale: the OS already runs a
  battle-tested, non-root yay flow; adding a whole second package ecosystem
  (Flatpak + Flathub remote + sandbox permission model) for one application
  would be the riskier change in a security-audited OS. **Flatpak remains
  the upstream-preferred alternative** and is fully supported as a manual
  path:
  ```bash
  sudo pacman -S flatpak
  flatpak remote-add --if-not-exists flathub https://flathub.org/repo/flathub.flatpakrepo
  flatpak install flathub com.usebottles.bottles
  ```
  A manually Flatpak-installed Bottles is **detected** by the inventory
  (`com.usebottles.bottles` inventory row) — it simply is never installed
  automatically. If the AUR package proves unmaintained on your snapshot,
  use the Flatpak path; a first-class flatpak channel can be added later as
  a separately reviewed change.
- **RetroArch** — repo package `retroarch` + the three per-UI asset packs
  (`retroarch-assets-xmb` / `-ozone` / `-glui`; the assets ship as separate
  packages in current Arch repos, verified via the official package API).
- **Gamescope / MangoHud / GameMode (+ lib32-gamemode)** — repo packages,
  verified via the official Arch package API. All optional (in `full`).
- **Vulkan loaders** — `vulkan-icd-loader` + `lib32-vulkan-icd-loader`
  declared explicitly in the minimal profile (gap fix: Steam pulls them as
  dependencies anyway; now the Proton/Vulkan requirement is explicit and
  verifiable).
- **Proton / GE-Proton / umu** — managed by Steam/Lutris themselves, not by
  this OS. Nothing is installed or claimed here.
- **Native Sunshine** — user-managed (the OS never installs it; `scarlix-mode
  game` only starts `sunshine.service`). Detected via unit-file existence.
- **`sunshine-tv` / Jellyfin / Minecraft** — existing Docker services,
  inventory-only rows. Jellyfin and Minecraft are **servers**, not game
  launchers (documentation previously blurred this).

---

## ScarliHQ integration (read-only)

- `GET /api/gaming` (auth-gated, GET-only) serves the inventory detected by
  `scarlix-host-bridge` from `host-status.json` — no second source of truth,
  no detection inside the ScarliHQ container.
- Detection (host-side, timeout-wrapped): one batched `pacman -Q` over the
  manifest packages (covers repo **and** AUR packages — same local DB), one
  `systemctl` probe for native Sunshine, compose rows reuse the existing
  `docker ps` output, `flatpak list` only when flatpak is installed.
- Entry contract (`internal/gaming`): `id`, `name`, `kind`
  (launcher/compatibility/performance/streaming/server), `source`
  (pacman/aur/flatpak/systemd/compose), `installed`, `version`, `running`,
  `health` (not_installed/not_applicable/stopped/running/unhealthy/unknown),
  `optional`, `last_checked`.
- Health is honest: libraries report `not_applicable`, undetectable states
  report `unknown` — never a guess.
- The dashboard renders a **Gaming Stack** card from `/api/status` /
  WebSocket status updates (all dynamic fields HTML-escaped, same XSS
  convention as GPU/container rows).
- ScarliHQ never launches games, never installs packages, never exposes
  arbitrary commands. If remote start/stop is ever needed, it must go
  through a narrowly scoped, separately reviewed Host-Bridge action — not
  through this API.

---

## AI/game resource policy

The existing mode machine stays authoritative: `scarlix-mode game` stops the
AI inference path and starts native Sunshine; `scarlix-mode ai` returns.
There is **no second GPU manager** and no accidental concurrent GPU-heavy
AI + game mode. Mode transitions keep their fail-closed + stale-state-repair
semantics. `ai → game → ai` regression coverage remains in the existing
test suite; target-host behavior is part of the hardware acceptance gate.

---

## Rollback

- Reverting this release never removes packages: revert the git commit,
  optionally delete `/usr/local/share/scarlix/gaming-packages.conf` and
  `scarlix-gaming-install` — user-installed launchers, game libraries, Wine
  prefixes and Steam data are untouched.
- To go gaming-free manually (headless conversion guidance is printed by
  `--profile headless`), remove packages with `pacman -Rns` **after**
  reviewing each — user data directories are not owned by those packages.

---

## Verification in CI / smoke test

- `check_15` — manifest well-formed (5 fields, valid tokens, no duplicates,
  base block mirrors `packages.x86_64`); installer passes `bash -n`;
  installer contains no package-removal commands in executed positions;
  AUR-as-root fails closed.
- `check_16` — `/api/gaming` is auth-gated and GET-only; `internal/gaming`
  is a pure data contract (no exec/docker); the host bridge publishes the
  `gaming` array into `host-status.json`; `HostStatus` exposes the typed
  entries.
- Go contract tests: enum validation, wire shape, nil→`[]`, HTTP envelope,
  legacy status files without a gaming field.
- Package names for repo packages were verified against the official Arch
  package API at release time; AUR names follow the integration plan's
  baseline research (the AUR RPC is bot-protected and was not verifiable
  from the build sandbox — a per-package install failure is contained and
  reported, never silent).
