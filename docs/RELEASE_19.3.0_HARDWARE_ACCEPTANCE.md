# ScarLiXoS v19.3.0 — Hardware Acceptance Gate

Status: **PENDING — must be run on the target EndeavourOS machine**. CI success is not a substitute for host acceptance.

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
- [ ] Verify the safe behavior on a single-GPU host/configuration if applicable.
- [ ] Verify fallback behavior after stopping the preferred runtime.
- [ ] Reboot once more and repeat health/smoke checks.

## Release decision

- [ ] Attach redacted install log, smoke-test output, `nvidia-smi`, Docker health, and inference results.
- [ ] Resolve all critical failures before creating a non-prerelease production release.

A green GitHub CI run validates repository checks only. It does not prove NVIDIA kernel-module compatibility, the local GPU runtime, model downloads, inference quality, thermals, or reboot recovery on the target host.
