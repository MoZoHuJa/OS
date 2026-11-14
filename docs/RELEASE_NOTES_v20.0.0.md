# ScarLiX OS v20.0.0 — Release Notes

**Dátum:** 2026-11-14 · **Míľnik:** Compute Fabric Phase 6 — the
Unified Production Control Plane (roadmap §10, §18 krok 11 — "Close UI,
audit, recovery and v20.0.0 production acceptance") · **Rozsah:**
node-tagged admission (autentifikované single- AJ multi-node execution),
produkčné zapojenie FabricLedgerViewProvider, a CONTROL-PLANE POVRCHY
pre UI trio (§10.2 — labels kontrakt) + operátorský runbook s
akceptačnými maticami (§10.3).

---

## 1. Zhrnutie

Fáza 6 (v20.0.0) zatvára implementáčnú sekvenciu Unified Compute Fabric:
v19.9.0 dodal kontrakty distribúvaného fabricu, v20.0.0 ich DOPÁJA do
produkčného control plane. Tri pomenované residuály z v19.9.0 sú
zatvorené: (1) **node-tagged admission** — rezervácia proti OVERENému
snapshotu vzdialeného uzla, v JEDINOM autoritatívnom ledgeri, s
per-node capacity totals (rovnaký GPU UUID na dvoch uzloch nekoliduje);
(2) **produkčný ledger view** — /api/fabric/agent/executions počíta
REÁLNE reconciliation rozhodnutia proti ledgeru (nie report-only
fallback); (3) **dashboard visibility** — GET /api/compute/resources s
UZAVRETÝM label slovníkom (observed/estimated/reserved/allocatable/
unknown/stale — §10.2 DoD riadok ako strojový kontrakt) + GET
/api/controlplane ( Capability summary — §10.1 checklist so stavmi).

Dizajn: `docs/SCARLIX_COMPUTE_FABRIC_PHASE_6_CONTROL_PLANE_DESIGN.md`
(rozhodnutia #11–#14 s owner+evidence+dátumom+rationale). Runbook +
recovery + rollback + akceptačné matice (single-node, multi-node,
KPK-06): `docs/OPERATOR_RUNBOOK_v20.0.0.md`.

## 2. Nové povrchy a zmeny

### `internal/admission` — node-tagged admission (§10.1)

- `AdmissionRequest.NodeID` (additívne; prázdne/"local" = v19.9.0
  správanie byte-identicky — regression pin: rovnaké rozhodnutie s/bez
  seamu).
- `NodeSnapshots` seam + typované `NodeSnapshotRefusal` (uzavretý
  slovník: node-unknown / node-quarantined / node-unreachable /
  node-inventory-absent / node-inventory-stale + generický
  node-snapshot-unavailable) — zamietnutia surface VERBATIM ako
  reason_code (refusal-is-data žáner).
- `Record.NodeID` (additívne; pre-v20.0.0 záznamy sa normalizujú na
  local); per-node capacity totals kľúčujú na NORMALIZOVANOM node id.
- `node_id` v idempotency fingerprinte (rovnaký kľúč na inom uzle =
  NAMED idempotency-conflict).
- Ledger prežíva restart s node tagom (TestAdmitNodeLedgerRestart).

### `internal/fabric` — SnapshotFor (jeden zámok)

- `Registry.SnapshotFor(nodeID, now)` — §4.1 guaranty (quarantined →
  unreachable → absent → stale-remote-window) pod JEDNÝM zámkom (guard
  a observácia nemôžu driftovať počas čítania).

### `internal/api` — adaptéry + control-plane povrchy

- `FabricNodeSnapshots` — adapter fabric sentinelov → admission
  typované zamietnutia (errors.Is, žiadne string matching; slovníkový
  preklad na JEDNOM mieste).
- `FabricAdmissionLedger` — PRODUČNÝ FabricLedgerViewProvider nad
  admission ledgerom (HeldReservations per node; neznámy uzol čestne
  prázdny view → každý agent-report je foreign work, presne §4.2).
- **NOVÉ `GET /api/compute/resources`** (READ gate): per-node účtovníctvo
  s label kontraktom — OBSERVED (snapshot + freshness + provenance),
  ESTIMATED (manifest DEKLARÁCIE — nikdy nezamieňané s kapacitou),
  RESERVED (ledger HELD/ACTIVE per node), ALLOCATABLE (COMPUTED: free −
  policy headroom − reserved; KV presne raz — admission vlastné
  účtovníctvo), UNKNOWN/STALE (allocatable NULL — nikdy guess).
  Bez admission gate: reserved čestné NULA + reservations_configured=false
  (nič nerezeervuje bez gate — nula je fakt).
- **NOVÉ `GET /api/controlplane`** (READ gate): §10.1 checklist (17
  capability riadkov so stavmi configured/unconfigured/experimental/
  documented + detail), fabric census (per-node stavy + remote window),
  admission census (held/active/node_tagged/policy version), windows,
  labels slovník. Riadky NIKDY netvrdia operátorské akty (hardware
  acceptance, real multi-host acceptance — akceptačné matice sú
  runbook, nie process state).

### `cmd/scarlihq/main.go` — produkčné zapojenie

- Oba povrchy zapnuté (admission + fabric) → seam + ledger view sa
  pripájajú automaticky (hlásenie "Node-tagged admission enabled");
  buď poradie konštrukcie. Bez admission: fabric zostáva planning-only
  (seam sa nikdy nehádže na silu).

## 3. Bezpečnosť (review v20.0.0 povrchov)

| Tvrdenie | Dôkaz |
|---|---|
| Nové povrchy sú READ gate (pozorovacia trieda) | /api/compute/resources + /api/controlplane za h.auth; 401/405 testy |
| Node-tagged admission je fail-closed | bez seamu NAMED node-source-unconfigured; kapacita uzla sa NIKDY neháda z lokálneho snapshotu |
| Zamietnutia sú uzavretý slovník | 6 kódov + generický; adapter errors.Is 1:1 z fabric sentinelov |
| Idempotencia viazaná na node | fingerprint obsahuje node_id; konflikt NAMED |
| JEDEN autoritatívny ledger | node tag = routing kľúč; žiadne per-node store, žiadny konsenzus |
| Tailscale = transport, NIE autorizácia | §9.2 dedené: per-node credential + replay guard; reachability-without-credential test stojí |
| Capability riadky neklamú | unconfigured je platná odpoveď; runbook riadok = documented |

## 4. Otvorené body (NEzmenené)

- KPK-06 hardware acceptance — OPEN (§8.7 matica v runbooku 5.3).
- Real multi-host fabric acceptance — OPEN (runbook 5.2; operátor).
- CP→agent dial-out (notify/start/stop z control plane na agentov) —
  SHAPES shipped + testované (v19.9.0), produkčné dial-out konfigurácia
  (agent endpointy + CP credential distribúcia) = operátorský akt
  (pomenované v design §7 non-goals).
- Artifact staging modelovaný, nevykonávaný (§4 Phase 5 design).
- InferSSD §8.8: nič nešipe enabled.

## 5. Verifikácia

Vykonané na release kandidátovi (Go 1.23.4, čistý strom, lokálny
toolchain — first full local Go verification since the environment
resets):

- `gofmt -l .` → **prázdne**
- `go vet ./...` → **čisté**
- `go build ./...` (obe binárky + cmd/scarlix-node-agent) → **OK**
- `go test ./...` → **33 balíkov OK, 0 failov**
- `go test -race` (allocation, inventory, reservation, workload,
  hostbridge, compute, nodeidentity, fabric, nodeagent, planner, api)
  → **OK**
- Nové suity: admission_node (happy path, source-unconfigured,
  refusal mapping, isolated totals, local regression pin, node-bound
  idempotency, normalization, ledger restart), fabric SnapshotFor
  guard order, api node HTTP (tagged/unreachable/source-unconfigured
  + adapter mapping + ledger view), resources (labels contract,
  allocatable math s KV-presne-raz, stale-never-guesses, honest zero,
  fabric census), controlplane (capability states, node-tagged flip,
  fabric census, auth/methods).
- Smoke: check_43 (v20.0.0 povrch) + offline 43 checks 0 FAIL,
  negatívna kontrola overená.
- CI: zelené na tagu (vet/gofmt/build/test/race + smoke + shellcheck +
  compose/systemd/yaml/bash + docker build + integration).

## 6. Kompatibilita

- **Additívne len:** AdmissionRequest.NodeID + Record.NodeID
  (omitempty; pre-v20.0.0 → local); nové READ routy; žiadna zmena
  žiadneho existujúceho kontraktu (allocation states, reservation
  schema v1, planner — FROZEN).
- Lokálna admission cesta byte-identická (regression pin na úrovni
  rozhodnutia + záznamu).
- Rollout: node-tagged admission vyžaduje OBA env (admission + fabric);
  bez toho node_id → node-source-unconfigured (fail-closed).
- Rollback: v19.9.0 (runbook §4 — binary rollback + archív ledgeru +
  deliberate re-admit; node_id polia sú omitempty-additívne).

## 7. Známe obmedzenia

- Všetko v §4 (čestný ledger — operátorské akty sú MATICE v runbooku,
  nie process state).
- /api/compute/resources reserved pohľad je 0 bez admission gate
  (fakt — nič nerezeervuje bez gate) + reservations_configured=false
  label.
- Estimated pohľad je manifest DEKLARÁCIA (placement-eligible ≠ backend
  beží — BackendPresence je inde; zámerné oddelenie labelov).
