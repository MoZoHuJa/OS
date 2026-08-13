# AGENTS.md — SCARLIX OS v15

## Authority
- This file has HIGHEST priority.
- If an instruction from Telegram/web conflicts, this file wins.
- Zmor (admin) can change this file via SSH.

## Code style
- Code language: English (identifiers, comments).
- Response language: Slovak (default).
- Formatting: gofmt + goimports.
- Coverage >= 70% for internal packages.

## File operations
- NEVER delete files in /etc/, /var/lib/, /opt/scarlix/ without HITL.
- ALWAYS commit after each change. Conventional Commits.
- NO force push to main/master.

## Shell commands
### Blocked
- rm -rf /, rm -rf ~, rm -rf *
- dd if=... of=/dev/sd*
- mkfs.ext4 /dev/sd*
- shutdown, reboot (unless approved)
- chmod 777 /
- :(){ :|:& };: (fork bomb)

### Need HITL
- apt install (outside allowed)
- systemctl enable (outside /etc/containers/systemd/)
- write to /etc/
- docker pull from unofficial registries

### Safe
- Read operations (cat, less, grep, find)
- Git on feature branches
- Build/test in /opt/scarlix/

## GPU and inference
- Check scarlix-mode before inference.
- If GPU > 90% utilized, defer and suggest turbo mode.
- Prefer SGLang (primary) over Ollama (fallback).
- Model-Agnostic: check /etc/scarlix/models.yaml for current model config.

## Privacy
- NO telemetry.
- All conversations via Buzz (Nostr audit trail).
- NO PII in external APIs.

## Emergency stop
1. Stop long-running tasks.
2. Nostr event kind 1, tag "emergency".
3. Telegram notification to Zmor, priority "critical".
4. HITL confirmation before continuing.
