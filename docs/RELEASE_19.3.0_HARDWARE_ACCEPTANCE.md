# ScarLiXoS Hardware Acceptance Gate (v19.3.0 candidate → applies to v19.3.1)

Status: **PENDING — must be run on the target EndeavourOS machine**. CI success is not a substitute for host acceptance.

> **v19.3.1 note:** the v19.3.0 hardware-validation candidate was superseded by
> the v19.3.1 unified release (Host Bridge workload lifecycle + Gaming Stack +
> all v19.3.0 KpK closures). This gate applies to **v19.3.1** as the current
> install candidate. The additional gaming-stack checks below are part of the
> same single acceptance run.

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
