# ScarLiX OS — Compute Fabric: Reservation Governor (F-04)

**Version:** v19.3.2 · **Status:** TESTED (CI) — ships **disabled by default**; enable only after host acceptance
**Components:** `scarlihq/cmd/scarlix-reservation` (Go binary), `scarlihq/internal/reservationops` (production providers), `files/etc/systemd/system/scarlix-reservation.service`, `docs/examples/reservation.json.example`

---

## 1. What this is

The **production reservation composition root** — the last documented Compute
Fabric (Phase 5) gap, closed in v19.3.2 (audit F-04 / roadmap row
„Fabric → scarlix-mode bridge"). Until now, `reservation.NewFileBackedManager`
had **only test callers** (KPK-P2-03, true through v19.3.1). The governor is
the real long-running service that owns it:

```
startup:  load config → build providers → NewFileBackedManager → Recover(reconciler)
run:      RunLeaseReaper(ctx, token, 30s)   [expired leases + lease repair + interrupted releases]
stop:     SIGTERM → ctx cancel → clean exit
```

It is deliberately **off the HTTP plane**: it owns *reconciliation*, not
intake. `Reserve`/`Release` intake (the compute API) is the next documented
step and will reuse the same stores under the same flock.

## 2. Deployed shape

| Piece | Value |
|---|---|
| Binary | `/usr/local/bin/scarlix-reservation` (built by install.sh, Go 1.23, CGO disabled) |
| Unit | `scarlix-reservation.service` — User/Group 65532, `SupplementaryGroups=docker`, `ProtectSystem=strict`, `StateDirectory=scarlix-reservation` (0700) |
| Config | `/etc/scarlix/reservation.json` — root-owned, mode 0640 root:65532, **tokens inside** (world-readable configs are rejected at load) |
| Stores | `/var/lib/scarlix-reservation/reservations.jsonl` + `allocations.jsonl` (JSONL, flock, 0600, atomic rewrites) |
| Enabled? | **NO** — copied only. Operator enables after configuring tokens + host acceptance |

The allocation store is **shared** with the ScarliHQ compute lifecycle
(`SCARLIHQ_ALLOCATION_STORE=/var/lib/scarlix-reservation/allocations.jsonl`
in the opt-in compose block): both sides run as UID 65532 and both go through
the same flock, so the reservation engine and the workload lifecycle see one
consistent allocation record set.

## 3. Providers (internal/reservationops)

| Provider | Implementation | Fail mode |
|---|---|---|
| `TrustedIdentityProvider` | `TokenIdentityProvider` from config: 1 reconciler token (`system.reservation-governor`) + optional user principals | unknown token → `ErrUnauthenticated`; empty user map = nobody can reserve (fail-closed) |
| `InventoryProvider` | `HostInventoryProvider` — CollectGPUs/CollectRuntimes/CollectModels + health refinement (same composition as ScarliMonitor); managed ports from config only | missing sources → empty slices → unreservable, never guessed |
| `CleanupVerifier` | `DockerCleanupVerifier` — `docker ps --filter label=scarlix.managed=true`; occupancy by canonical resource ID (port/runtime/model label match; GPU UUID → display-ID mapping via live inventory) | docker unreachable → **error** (retry next tick); unmappable/unknown resource → `Verified=false` → reservation converges to **CONFLICT** (fail-closed, operator-visible) |

## 4. Enabling (operator runbook)

```bash
# 1. Tokens (keep SEPARATE from SCARLIHQ_TOKEN):
openssl rand -hex 32   # → reconciler_token

# 2. Install config from the shipped template:
sudo install -o root -g 65532 -m 0644 docs/examples/reservation.json.example /tmp/reservation.json
sudo vi /tmp/reservation.json            # fill reconciler_token, adjust managed_ports
sudo install -o root -g 65532 -m 0640 /tmp/reservation.json /etc/scarlix/reservation.json
rm /tmp/reservation.json

# 3. Review + enable:
systemctl cat scarlix-reservation
sudo systemctl enable --now scarlix-reservation.service
journalctl -u scarlix-reservation -f      # expect: recovery summary → "lease reaper engaged"
```

`CHANGE_ME` placeholders intentionally fail validation with actionable errors
— a copied-but-unedited template can never start the service.

## 5. Security notes

- **Docker group = host-root-equivalent** (same caveat as the workload
  bridge). The governor is a **read-only** docker consumer (`docker ps` for
  inventory + cleanup verification). It has **no socket, no API, no exec
  path** — workload start/stop stays on the allowlisted workload bridge.
- The reconciler token is a **separate secret** from `SCARLIHQ_TOKEN`
  (dashboard). KPK precedent: reconciler identities are separately trusted.
- Hard fail-closed semantics: corrupt store → exit (unit parks after 5
  restarts); docker down → reaper errors, nothing released; ambiguous
  ownership → `CONFLICT`/`CLEANUP_REQUIRED`, never silent `RELEASED`.
- Rollback: `systemctl disable --now scarlix-reservation` — stores stay on
  disk (data is never deleted by the governor); re-enabling re-runs `Recover`
  and converges idempotently.

## 6. Host acceptance (v19.3.2 additions)

See `docs/RELEASE_19.3.0_HARDWARE_ACCEPTANCE.md` § „v19.3.2 additions":
service start/stop, recovery idempotence after `kill -9`, fail-closed on
removed config, reaper convergence for an expired lease on the real host.

## 7. What is deliberately NEXT (not claimed here)

- **Compute intake API** — `Reserve`/`Release` over the governor's stores
  (trusted principal mapping + allocation intake, P5.1 „Required next work"
  #1). The `user_tokens` config field is already validated and waiting.
- ScarliHQ dashboard surfacing of reservation state (read-only, via
  host-status or a governor status file — design TBD with the intake API).
