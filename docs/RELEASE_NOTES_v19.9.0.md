# ScarLiX OS v19.9.0 — Release Notes

**Verzia:** 19.9.0
**Commit:** [`25ff783`](https://github.com/MoZoHuJa/OS/commit/25ff783fa1eae1c0830435a7206115268f15223f) — recorded at packaging (the release-notes genre: the hash lands with the tag)
**Dátum:** 2026-11-13
**Typ:** Feature release — Compute Fabric **Phase 5: Distributed Compute Fabric** (roadmap §9)
**Predchádzajúca verzia:** v19.8.1 (commit `bccb1f8`, tag `v19.8.1` + the post-release acceptance remediation `e1ca753`)

---

## 1. Zhrnutie

v19.9.0 rozširuje resource model z jedného hostu na **viacero dôveryhodných
uzlov bez oslabenia lokálneho bezpečnostného modelu** — presne scope roadmapy §9
(Distributed Compute Fabric). Dizajnový záznam:
`docs/SCARLIX_COMPUTE_FABRIC_PHASE_5_DISTRIBUTED_DESIGN.md` (vrátane rozriešenia
otvorených rozhodnutí §19 #6/#7/#8/#10 s ownerom, evidence, dátumom a
rationale).

**Architektonické rozhodnutie (§19 #7):** hub-and-spoke s **jediným
autoritatívnym reservation ledgerom** na control plane. Node agenty sú
neautoritatívne konštrukčne — split-brain je znemožnený konštrukciou, nie
protokolom (žiadne per-node schedulery, žiadny consensus v v19.9.0; revisit
trigger zaznamenaný).

**Hlavný princíp zostáva:** LLM navrhuje, planner vyberá, admission rezervuje,
Host Bridge vykonáva — a **každý stupeň nezávisle overuje svoje podmienky**.
Nový stupeň (node identity) dodržuje rovnakú disciplínu: *Tailscale je
transport, NIE autorizácia* (roadmap §9.2 verbatim).

---

## 2. Nové balíky a povrchy

### internal/nodeidentity — identita a dôvera uzlov (§9.2)

- **Enrollment na pozvanie:** ADMIN vygeneruje one-time invite (TTL-bound,
  default 30 min, max 24 h); uzol sa zaregistruje prezentácií invite secretru;
  per-node credential sa vydá **práve raz** (raw secret sa nikde nepersistuje
  ani neloguje — uloží sa len sha256 digest, rovnaká disciplína ako
  `reservation.TokenIdentityProvider`).
- **Rotácia s grace window** (default 15 min): starý aj nový digest platný do
  vypršania; re-rotácia reťaz nahradí (previous-previous zomrie okamžite).
- **Revokácia = karanténa:** QUARANTINED odmieta novú prácu aj observations;
  existujúce rezervácie sa NEauto-uvoľňujú (kapacita nie je preukázaná slobodná)
  — cleanup len cez trusted recovery (`Reactivate` vydá čerstvý credential).
- **Node principali** `node:<id>` s uzavretým scope setom (inventory/health
  push, execution report, receive start/stop/reconcile) — nikdy admin, nikdy
  reconciler-of-record. Role→scope mapping je KÓD, nie konfigurácia.
- **Replay ochrana:** každý mutating push nesie
  `X-Scarlix-Node-Request-ID` + `X-Scarlix-Node-Timestamp`; ±90 s freshness +
  per-node bounded LRU (10 min, cap 1024) — fail-closed.
- JSONL persistence (flock, 0700/0600, atomic rewrite, corrupt-refusal);
  registry prežije restart CP (credential aj live invite).

### internal/fabric — observational registry + reconciliation (§9.3/§9.5)

- **Observačný register uzlov** (identity je v nodeidentity, pozorovanie tu):
  posledný snapshot, reachability, latency class, per-node freshness window.
- **Sémantiky zlyhania (§4.1 designu, normatívne):** unreachable → kapacita
  UNKNOWN, snapshot sa ZADRŽÍ, rezervácie ostanú HELD, admission odmietnutá;
  návrat uzlu → **reconcile-before-admit**; quarantine → vylúčenie; inventory
  stale → `INVENTORY_STALE` per-node odmietnutie.
- **Reconciliation engine** — čistá deterministická funkcia: agent execution
  records × ledger held reservations → typované rozhodnutia
  (`reservation-stands` / `reconcile-required` / `stop-foreign-execution`),
  zoradené podľa allocation ID; **nič sama neuvolňuje** — release vždy len cez
  trusted reservation-manager path. Un-ledgered execution (agent beží, ledger
  nemá rezerváciu) sa zastavuje a reportuje, nikdy ticho retro-admitne.

### internal/planner — network-aware multi-node planner (§9.4)

- **`FabricPlanner`** (nový súbor `multinode.go`, ten istý balík): znovu použiva
  extracted §7.4/§7.5 gate-y (`modelGates`, `joinBackends`) — single-node cesta
  sa **nezmazala ani nezmenila**; extrakcia bola verbatim.
- **Network gates (pred optimalizáciou):** artifact locality (present on node
  beats transfer-eligible), **transfer-cost rule** (remote admission REFUSED,
  keď artifact absent a jeho overená veľkosť presahuje
  `MaxArtifactTransferBytes`, default 10 GiB; neznáma veľkosť fail-closed),
  latency-class gate (interactive ≤ B default; neznáme deklarácie = C),
  data-sensitivity gate (`local-only` žiada nikdy neopustia lokálny uzol).
- **Deterministické ordering rozšírené:** policy priority → locality → latency
  class → utilization → tightest fit → stable backend+node+GPU IDs. PlanID
  obsahuje NodeID (od v19.7.2) — rovnaké vstupy → rovnaký plán vrátane
  placement.
- **Remote freshness STRIKTNEJŠIA (rozhodnutie §19 #6):** remote okno default
  **30 s**, lokálne zostáva 60 s — remote kapacita starne v tranzite pred
  pozorovaním CP.
- **Cross-node verification binding:** uzol, ktorého vlastný verifikačný
  záznam nie je viazaný na ROVNAKÝ manifest fingerprint (KPK-19.8.0-P1-02
  rozšírené na N uzlov), nie je kandidát.
- **Regression pin:** nulový počet remote views ≡ single-node rozhodnutie
  (rovnaký PlanID) — preukázané testom aj na HTTP vrstve.

### internal/nodeagent + cmd/scarlix-node-agent — uzlový agent (§9.3)

- **Narrow typed API — presne šesť operácií:** inventory snapshot, health,
  reservation mirror notify, start approved profile, stop owned allocation,
  reconcile. Žiadny shell, žiadny ľubovoľný Docker API, žiadny file access,
  žiadne port publishing — **štruktúrne** (request typy nesú len identifikátory;
  jediná execution hranica je injectnutý `AgentExecutor` cez uzlový vlastný
  hardened host workload bridge).
- **Reservation mirror:** start bez live rezervácie v mirrore = odmietnutý
  (rogue-start abuse case uzavretý na samotnom uzle); mirror je explicitne
  neautoritatívny.
- **Stop ownership:** agent zastaví IBA alokácie, ktoré sám spustil (vlastné
  execution records); shared/pre-existing workload → pomenované odmietnutie.
- **Fail-closed outcomes:** ambiguózna start/stop → `start-uncertain` /
  `stop-uncertain` (slovík reconciliation engine); deterministic refusal
  (`HOST_BRIDGE_UNAVAILABLE`) sa nezaznamenáva ako uncertain — neotrávi
  reconciliation.
- Durable execution records + mirror (JSONL, agent restart ich prežije);
  CP credential auth (constant-time) na každom dátovom route.

### internal/api — control-plane HTTP povrch (fabric.go)

- **ADMIN routes:** `/api/fabric/nodes` (GET list / POST invite — secret vrátený
  práve raz), `/nodes/revoke`, `/nodes/rotate`, `/nodes/reactivate`,
  `/nodes/reconciliation` (rozhodnutie §19 #10: approval set vs. monitoring set
  tvarované pre ScarliCenter/ScarliMonitor split vo fáze 6).
- **AGENT routes:** `/api/fabric/agent/enroll` (invite = autorizácia; jediná
  route bez predchádzajúceho node credentialu), `/agent/inventory`,
  `/agent/health`, `/agent/executions` — node credential + scope + replay
  guard; executions push počíta reconciliation decisions cez
  `FabricLedgerViewProvider` (report-only mode bez neho — releases vždy len cez
  trusted path).
- `/api/compute/plan` preferuje FabricPlanner, keď je fabric konfigurovaná
  (route a povrch nezmenené; bez konfigurácie single-node presne ako v19.8.1).

---

## 3. Otvorené rozhodnutia §19 — uzavreté v tomto release

| # | Rozhodnutie | Zápis |
|---|---|---|
| 6 | Freshness window: lokálne 60 s (nezmenené), remote 30 s (striktnejšie) | design §7 |
| 7 | Autoritatívny store: CP reservation Manager zostáva JEDINÝ ledger | design §1 |
| 8 | Identity: ľudia → profilí, agenti → agent tokeny, uzly → `node:<id>` cez enrollment | design §7 |
| 10 | UI ownership: ScarliCenter = approvals, ScarliMonitor = monitoring (implementácia fáza 6) | design §7 |

---

## 4. Otvorené body (NEzmenené z v19.8.x)

- **Hardware acceptance (§8.7, KPK-06) ostáva OPEN** — target-host matrix
  (CPU/RAM/GPU/CUDA/driver + SSD model/firmware/mount + RandomReads IOPS
  p50/p95/p99 + komparatívne SGLang/+pack/InferSSD merania) je fyzický akt
  operátora; InferSSD zostáva default OFF.
- **Reálne multi-host nasadenie:** ako každý release doteraz, akceptačná
  evidence sú automatizované testy + CI; skutočný fabric acceptance na
  cieľových hostoch (reálne uzly cez reálny tailnet) patrí do operátorovej
  fázy 6 acceptance matrix (analogicky k InferSSD hardware gate).
- Artifact-transfer pipeline: v19.9.0 transfer **modeluje** (transfer-eligible
  plány s `artifact-staging-required` dôvodom; agent start gate vynucuje
  prítomnosť váh), nevykonáva — plán je poctivý o tom, čo robí.

---

## 5. Verifikácia

Vykonané na release kandidátovi (Go 1.23.4, čistý strom):

- `gofmt -l .` → **prázdne** (včetne 6 predchádzajúcich nečistých súborov
  z e1ca753 — admission/modelmeta — opravených v tomto release ako hygiene)
- `go vet ./...` → **čisté**
- `go build ./...` + oba binárky (`cmd/scarlihq`, `cmd/scarlix-node-agent`) → **OK**
- `go test ./...` → **33 balíkov OK, 0 failov**
- `go test -race` (allocation, inventory, reservation, workload, hostbridge,
  compute + **nové: nodeidentity, fabric, nodeagent, planner, api**) → **OK**
- CI race list rozšírený o nové balíky (`.github/workflows/ci.yml`)

### DoD §9 exit criteria → kde je preukázané

| Kritérium | Dôkaz |
|---|---|
| Node enrollment and revocation tested | nodeidentity: happy path, expired/consumed invite, replay, rotation grace + re-rotation chain, quarantine→reactivate, restart load, digest-only persistence, concurrent auth/rotate |
| Remote allocation ownership is authenticated | nodeagent: mirror-refused rogue start, reservation/allocation mismatch, released reservation, runtime allowlist, shared-workload not-owned stop; api: scope enforcement, wrong/no credential, quarantine code |
| Partition and restart behavior tested | fabric: unreachable retains snapshot + refuses admission, return→reconcile-before-admit, registry restart, agent restart records reload |
| Remote reservations reconcile safely | fabric.ReconcileNode matrix (running/stopped/never/uncertain/no-record × held/not-held), determinizmus + idempotencia (shuffled inputs → identický report) |
| Network-aware planner is deterministic | planner: 20× determinizmus, input-order independence, no-remote ≡ single-node (PlanID), HTTP-level determinizmus |
| Tailscale is transport, not authorization | per-node credentials + replay protection na každom mutating push; test: request bez credentialu na dosiahnuteľnom transporte → 401 |

---

## 6. Kompatibilita

- **Additívne len:** allocation states a transition table FROZEN (nedotknuté);
  reservation schema v1 (nedotknutá); single-node planner správanie identické
  (regression pin); `ExecutionRequest.DataSensitivity` nové pole (omitempty —
  spätné kompatibilné); `PlanResult`/`ExecutionPlan` polia nezmene... okrem
  NodeID, ktorý už existoval (v19.7.2) a pre local ostáva "local".
- Rollout: fabric je **opt-in** (`ConfigureFabric` — bez nej všetky /api/fabric/*
  503 a /api/compute/plan beží single-node presne ako v19.8.1).
- Rollback: v19.8.1 (žiadna dátová migrácia; nodeidentity JSONL je nový súbor).

---

## 7. Známe obmedzenia

- Skutočné multi-host acceptance (reálny tailnet, reálne uzly) — operátor,
  fáza 6.
- `FabricLedgerViewProvider` wiring do produkčného reservation managera je
  pripravený (rozhranie + report-only fallback), plné zapojenie release cesty
  patrí do fázy 6 production control plane.
- Artifact staging (transfer) sa modeluje, nevykonáva (§4).

---

## Post-release note: the CI catch — the version bumps the release missed + the Pi-Bolt installer pin drift (external, remediated on main)

The `CI` workflow on the v19.9.0 tag failed with a REAL finding, caught
exactly as designed. Four smoke checks and one shellcheck error exposed
that the v19.9.0 release act itself was INCOMPLETE at the edges the
Go-level verification does not cover:

1. **`install.sh VERSION` lagged the release** (`19.8.1` while the
   VERSION file says `19.9.0` — the five-place bump discipline missed
   one place).
2. **`scarlihq/Dockerfile ARG SCARLIX_VERSION` lagged the release**
   (the v19.0.5 P2 header-drift precedent: the header, the ARG, VERSION,
   main.go and rest.go move TOGETHER).
3. **The smoke test's own current-release expectations lagged the
   release** — check_05's README banner/badge/version-line assertions
   and the VERSION-file guard still expected v19.8.1 (the
   every-release-moves-the-guard discipline missed), so the checks
   failed against a correctly-bumped README/VERSION.
4. **The release's own check was missing** — the every release evolves
   its check inventory discipline (check_40 for v19.8.0, check_41 for
   v19.8.1) was not honored: no check_42 existed for the v19.9.0
   fabric surface.
5. **shellcheck SC2034** — `api_adm_go`, a check_41 fixture variable
   declared but never used (a new shellcheck 0.10 warning surface on
   the already-shipped script).

The `Pin Freshness` job on the tag ALSO failed — the second EXTERNAL
upstream drift of the Pi-Bolt installer (the v19.8.0 precedent, KPK-
19.2.13-P2-01 working as designed): the live installer SHA-256 changed
from the shipped pin
(`91918786f9341ef7d5ef1bf1db6fabeaffdf0434835c9412055bbc5c5fedac39`,
last verified 2026-11-08) to
`79981303f2d11eb8c52a69d2a3ce2fb01718986926950a98a27b8384897440dd`.
The new installer was downloaded and inspected: the documented
structure unchanged (release download + checksum + signature
verification, npm/GitHub sources only, no curl|sh patterns) — the pin
file recomputed and the "last verified" dates in README.md and
docs/SCARLIX_SECURITY_MAP.md moved to 2026-11-14.

Remediated on main (this commit): install.sh + Dockerfile bumped to
19.9.0, the smoke test's current-release expectations moved to
v19.9.0, **check_42 added** (the v19.9.0 fabric surface's own check —
node identity & trust §9.2 markers, the ReconcileNode engine, the
transfer-cost bound, the agent reservation mirror, the admin/agent
HTTP surfaces, the §19 decisions record, the CI race extensions),
the unused check_41 variable removed (SC2034), and the pin refreshed.
The Go-level verification claims in §5 stand (the CI `go-build` job —
vet/gofmt/build/test/race — was green on the tag); what the release
act missed was exactly the shell-level release discipline. The
v19.3.2 post-release-note precedent; the v19.8.0 pin-drift
precedent.
