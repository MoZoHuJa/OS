# ScarLiXoS Hardware Acceptance Gate (v19.3.0 candidate → applies to v19.3.1/v19.3.2)

Status: **ATTESTED AS PASSED by the project owner (2026-10-20) for the v19.3.1 scope; v19.3.2 additions below remain OPEN until re-run.**

> **Evidence policy (dôkaz namiesto sľubu):** the attestation below is an
> owner-provided statement, not an automated or third-party verification. It
> is recorded with author, date and scope so the evidence chain stays
> truthful; per the taxonomy in `docs/ScaRgeN_CuLT_Strategy_Obsidian_Lattice_SK_2026-10.pdf`
> (page 21), rows fed by this attestation are marked
> *HARDWARE-VERIFIED (owner-attested)* — distinct from a CI-reproducible proof.
> The owner attests based on access to the reference machine (RTX 5060 Ti +
> RTX 4060 Ti, Ryzen 5, 64 GB RAM per the strategy document, page 14).

> **v19.3.1 note:** the v19.3.0 hardware-validation candidate was superseded by
> the v19.3.1 unified release (Host Bridge workload lifecycle + Gaming Stack +
> all v19.3.0 KpK closures). This gate applies to **v19.3.1** as the current
> install candidate. The additional gaming-stack checks below are part of the
> same single acceptance run.

## ATTESTATION — 2026-10-20 (owner)

Attested by the project owner (repository `MoZoHuJa/OS`) for the **v19.3.1**
scope: the checklist sections *Before installation*, *Install*, *Hardware and
runtime* (including the gaming-stack and workload-bridge items) and *Release
decision* were run on the target host and **passed**; no unresolved critical
failure remained. The v19.3.1 prerelease is therefore treated as
hardware-accepted for its scope.

**Still OPEN — v19.3.2 additions** (not covered by the attestation above;
run on the next host session and record results here):

- [ ] `scarlix-reservation.service` (reservation governor, F-04): binary
      built by the installer, unit copied, service still disabled by default.
- [ ] Install `/etc/scarlix/reservation.json` from the template (0640
      root:65532, real tokens); `systemctl start scarlix-reservation` logs
      the startup `Recover` summary and "lease reaper engaged".
- [ ] Fail-closed probes: copied-but-unedited template (`CHANGE_ME`) is
      rejected; world-readable config is rejected; removing the config makes
      the unit fail (not silently skip).
- [ ] Recovery idempotence: `kill -9` the governor mid-reap, restart — the
      `Recover` summary converges with no duplicate effects.
- [ ] Reaper convergence on the real host: create a HELD reservation whose
      lease expires (or wait for one), confirm the governor verifies cleanup
      via docker and converges the record (RELEASED or CONFLICT — never a
      silent release).
- [ ] Upgrade/rollback: `git pull` to v19.3.2 + rerun installer → governor
      rebuilt, unit re-copied, still disabled until re-enabled; rollback =
      `systemctl disable --now scarlix-reservation` with stores intact.

## Before installation

- [ ] Fresh, updated EndeavourOS install; backup exists; machine can be recovered.
- [ ] Internet and adequate disk space available.
- [ ] Record `uname -a`, `/etc/os-release`, `lspci -nnk`, and current disk free space.
- [ ] Confirm no important workload is running; installer performs system updates and changes drivers/packages.

## Install

```bash
git clone --depth 1 --branch main https://github.com/MoZoHuJa/OS.git ~/scarlix-os && cd ~/scarlix-os && bash install.sh
```

- [ ] Installer exits 0 with no `CRITICAL` failures.
- [ ] `/var/log/scarlix/install.log` reviewed; redact tokens before sharing.
- [ ] Reboot completed; user session re-opened after group changes.

## Hardware and runtime

- [ ] `nvidia-smi` detects all installed NVIDIA GPUs and driver versions.
- [ ] `docker info` works as the intended user after re-login.
- [ ] `docker compose config` succeeds for generated deployment configuration.
- [ ] `scarlix-smoke-test.sh` passes; record PASS/FAIL/WARN totals.
- [ ] `scarlix-doctor` reports no unexplained critical failure.
- [ ] ScarliHQ health endpoint is reachable on the documented local interface.
- [ ] Run a small model inference on each intended GPU; verify device assignment, VRAM and logs.
- [ ] Gaming stack (if installed): Steam starts under the desktop session; 64+32-bit Vulkan paths work against the installed NVIDIA driver (vulkaninfo); Lutris starts and detects Wine.
- [ ] Gaming full profile (if installed): Heroic starts; Bottles creates a test prefix; RetroArch shows its menu (assets present); Gamescope runs a small native test app; MangoHud overlays on a Vulkan test app.
- [ ] `scarlix-mode game` starts native Sunshine; confirm native Sunshine and the `sunshine-tv` container never run simultaneously on the same display/input path; `ai → game → ai` returns cleanly (no stale GPU processes or containers).
- [ ] ScarliHQ dashboard Gaming Stack card reflects reality: every not-installed launcher shows "not installed" (no false positives), versions match `pacman -Q`.
- [ ] (Only if the operator enabled it) `scarlix-workload-bridge.service` starts, the socket is 0600 root:65532-owned, and ScarliHQ `/api/compute/start|stop` round-trips a typed workload. If left disabled (default), confirm the endpoints answer 503.
- [ ] Verify the safe behavior on a single-GPU host/configuration if applicable.
- [ ] Verify fallback behavior after stopping the preferred runtime.
- [ ] Reboot once more and repeat health/smoke checks.

## Release decision

- [ ] Attach redacted install log, smoke-test output, `nvidia-smi`, Docker health, and inference results.
- [ ] Resolve all critical failures before creating a non-prerelease production release.

A green GitHub CI run validates repository checks only. It does not prove NVIDIA kernel-module compatibility, the local GPU runtime, model downloads, inference quality, thermals, or reboot recovery on the target host.
