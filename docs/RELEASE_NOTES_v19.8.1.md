# ScarLiX OS v19.8.1 — Release Notes

**Verzia:** 19.8.1
**Commit:** [`bccb1f8`](https://github.com/MoZoHuJa/OS/commit/bccb1f8) — recorded at packaging (the release-notes genre: the hash lands with the tag)
**Dátum:** 2026-11-09
**Typ:** KpK remediation release (the v19.8.0 KpK audit findings fixed in code)
**Predchádzajúca verzia:** v19.8.0 (commit `095a690edfe267b3f7a99cc7d2fbb3645c1fe047`, tag `v19.8.0` + the post-release pin refresh `54265ff`)

---

## 1. Zhrnutie

v19.8.1 je **remediation release**: verzia opravuje všetky otvorené nálezy oboch
KpK recenzií v19.8.0 — **EN audit (CONDITIONAL PASS)**: 2×P1 + 2×P2 + 1×P2
integration gate + 1×P3; **SK audit (APPROVED FOR TAG)**: jeho P2 odporúčania
(benchmark max-age, local-only health prober, stale-snapshot SSD regresný
test). Žiadna nová schopnosť nepridávaná; **Phase 4 experimental InferSSD
architektúra stojí nezmenená** (default OFF, hardware acceptance OPEN).

Hlavný princíp zostáva: **LLM navrhuje, planner vyberá, admission rezervuje,
Host Bridge vykonáva — a každý stupeň musí nezávisle overiť podmienky, ktoré
mu patria.** v19.8.1 ho dotiaha do konca: admission teraz overuje presne tie
skutočnosti, ktoré doteraz overoval len planner.

---

## 2. Nálezy a opravy

### KPK-19.8.0-P1-01 — Admission nevynucovala random-read IOPS + p95 latenciu

**Nález (potvrdený v kóde):** `Manager.reserveAtomic()` kontrolovala len
sekvenčný MB/s + free bytes; `ResourceVector` nesie `MinimumRandomReadIOPS` +
`MaximumP95LatencyMs`, adapter ich napĺňa, `QualifySSD()` ich kontroluje —
ale finálna kapacitná autorita nie. Planner mohol vydať kvalifikovaný
SSD-backed plán, ktorý admission prijala aj proti zariadeniu, ktorého
namerané random metriky floors NEspĺňajú.

**Oprava:** jediný zdieľaný pure predikát **`storage.CheckMeasuredFloors()`**
(leaf balík, ktorý obaja konzumenti už importujú) — sekvenčná floor + random
IOPS floor (chýbajúca random fáza = odmietnutie) + p95 strop + vek benchmarku.
Volajú ho **admission aj `infer_ssd.QualifySSD`** — drift je štruktúrne
nemožný bez zmazania volania. Pomenované odmietnutia:
`storage-no-read-profile` (existujúce), **`storage-read-profile-stale`** (nové),
`storage-read-profile-insufficient` (rozšírené o random/p95).

### KPK-19.8.0-P1-02 — Verifikačné záznamy neboli previazané na manifest identitu

**Nález (potvrdený v kóde):** záznam kľúčovaný model ID; `checkModel()`
kontroloval len existenciu + `AllVerified` + vek (7 dní). Manifest zmenený po
verifikácii recykloval starý záznam — stale autorizačný token pre iný
artefakt.

**Oprava:** **`modelmeta.ManifestFingerprint(m)`** — kanonický SHA-256 cez
identity-bearing polia manifestu (schema, id, revision, checkpoint
format/quantization/digest (lowercase), architektúra + expert geometria,
context limit, KV precision + bytes/token, sorted backend set, expert-pack
enabled/format/version/digest/compatible backend, storage cesty). Záznam nesie
`ManifestFingerprint`; **oba gate-y** (admission `checkModel`, planner `Plan`)
prepočítajú fingerprint aktuálneho manifestu a vyžadujú EXACT zhodu:

- zmena → `model-verification-identity-mismatch` (admission) /
  `plan-model-verification-identity-mismatch` (planner) — pre-verifikovať;
- pre-19.8.1 záznam (prázdny fingerprint) → `model-unverified` (fail-closed);
- kozmetiká (case digestu, poradie backendov) identitu nemenia.

`VerifyManifestArtifacts` fingerprint zapisuje; kosmetické zmeny ho nemenia,
identity-bearing zmeny ho rušia — regresný test `TestManifestFingerprintIdentityBinding`.

### KPK-19.8.0-P2-03 — KV cache počítaná dvakrát v VRAM admission

**Nález (potvrdený v kóde):** adapter aj planner vkladali `shared + KV +
reserve` do `VRAMBytes` a deklarovali KV aj v `KVCacheBytes`; admission
pridala KV k dopytu ešte raz → 2×. Konzervatívne, ale odmietalo workloady,
ktoré sa zmestia.

**Oprava:** single-count kontrakt: `VRAMBytes = shared + reserve` (adapter
aj planner), KV jazdí v `KVCacheBytes`, admission ho pridáva presne raz
(deterministicky k prvému GPU). `ResourceVector.Validate` odmieta
`kv_cache_bytes > 0` bez GPU dopytu BY NAME; invariant zdokumentovaný na
oboch poliach. Test `TestAdmitKVCountedExactlyOnce` (VRAM 8 GiB + KV 1 GiB →
hold PRESNE 9 GiB).

### KPK-19.8.0-P2-04 — Idempotency replay nebol viazaný na principal/žiadosť

**Nález (potvrdený v kóde):** replay vrátil predchádzajúce rozhodnutie pre
akýkoľvek dopyt s rovnakým kľúčom (bez porovnania principal/modelu/vectoru);
kontrola bežala PRED ledger zámkou (TOCTOU — konkurentné identické dopyty
mohli double-admitnuť).

**Oprava:** kontrola presunutá **VNÚTRI atomickej ledger transakcie**
(`view.byIdempotency`); záznam nesie `RequestFingerprint` (SHA-256 cez
principal + request_id + model_id + backend_id + celý resource vector —
deterministický JSON marshal). Replay vyžaduje EXACT principal + fingerprint
zhodu; mismatch → **`idempotency-conflict`** (named 409-style refusal).
Uzavretý (RELEASED/EXPIRED) záznam je kľúč cieľom reuse-ovateľný —
deliberate + dokumentované pravidlo (hold už neexistuje; nová admisia je
nový záznam).

### KPK-19.8.0-P2-05 — Admission lease bez renew/confirm životného cyklu

**Nález (potvrdený v kóde):** len Admit/Release/Reconcile; 10-min lease
vyexpirovala aj bežiaci workload — kapacita sa uvoľnila časovačom, kým
workload konzumoval GPU/RAM/storage (P1 oversubscription risk pri Host
Bridge integrácii).

**Oprava:** kompletný lifecycle:

- **`Renew`** — principal-bound, durabilné, monotónne predĺženie lease
  (`POST /api/compute/admission/renew`, ADMIN gate, principal server-side);
- **`ConfirmStarted`** — HELD → **ACTIVE** (nový stav), principal-bound,
  idempotentný (retry-safe), `POST /api/compute/admission/confirm-started`;
- **ACTIVE nikdy neexpiruje časovačom** (a timer alone must not free capacity
  a confirmed-start workload still consumes), naďalej sa počíta do kapacity
  (HELD + ACTIVE agregujú do rovnakých totalov), uzatvára sa explicitne
  release-om; crash recovery pokrýva obe; `sweepExpired`/`reconcileLocked`
  čistia len HELD; `trimTrail` neodstraňuje ACTIVE;
- `StartedAt` na zázname; rollback pri persist failures v oboch operáciách.

### KPK-19.8.0-P3-06 — YAML akceptoval unknown fields

**Nález (potvrdený v kóde):** `yaml.Unmarshal` (default ignoruje unknown
mapping keys) v troch loaderoch operator-controlled manifestov.

**Oprava:** **`modelmeta.DecodeSingleStrictYAML`** — `yaml.Decoder` s
`KnownFields(true)` + odmietnutie druhého dokumentu; použitý v
`LoadManifests`, `LoadExpertPacks`, `LoadRuntimeProfile`. Chybové hlášky
držia existujúci tvar (`YAML parse failed:`). Deliberately-invalid example
manifesty ostanú invalid so svojimi pomenovanými dôvodmi.

### SK audit P2 odporúčania

- **P2-02 (health prober local-only):** `HTTPHealthProber.CheckHealth` —
  URL musí byť loopback (127/8, ::1, localhost) alebo `*.scarlix.internal`
  (compute-internal konvencia); inak named refusal, nikdy nedialovaný
  (`localOnlyHost`, `net/url` parse, port validovaný). Redirect disciplína
  nezmenená.
- **P2-04 (benchmark max-age):** `Policy.MaxBenchmarkAge` (default 30 dní) —
  admission odmieta starý benchmark `storage-read-profile-stale` (hodiny
  vlastní admission, rovnako ako VerificationMaxAge).
- **P2-05 (stale snapshot na SSD ceste):** regresný test
  `TestAdmitStaleSnapshotRefusedOnSSDPath` — freshness gate predchádza
  floors vždy.
- **P2-06 (MaxConcurrent == 1):** zostáva vynútené (config validate +
  smoke check_40 asserty) — uvoľnenie je §8.6 evidence akt, nie config edit.
- **P2-01/P2-03 (enablement discipline / prerequisites UX):** dokumentované
  nižšie (§ Enablement checklist) — pack prerequisites (byte verification +
  §8.7 matrix) už planner pomenúva v `expert-pack-declared-not-planned`
  vysvetlení.

---

## 3. Zmeny API

| Route | Metóda | Popis |
|---|---|---|
| `/api/compute/admission/renew` | POST (ADMIN) | predĺži HELD lease o policy TTL; 409 pri not-HELD / cudzom principal / neznámom ID |
| `/api/compute/admission/confirm-started` | POST (ADMIN) | HELD → ACTIVE (idempotentné); 409 pri uzavretom zázname / cudzom principal |

Existujúce routy (`/api/compute/admission`, `/release`) nezmenené; GET state
obsahuje HELD aj ACTIVE záznamy (`Held()` = capacity holds).

## 4. Zmeny kontraktov

- `admission.Policy`: nové pole `MaxBenchmarkAge` (default `30d`); verzia
  **`admission/v19.8.1`** (rozhodnutia ho nesú).
- Planner policy: **`planner/v19.8.1`** (KV single-count + fingerprint gate
  menia správanie plánovania).
- Nové reason kódy (aditívne, žiadne rename):
  `model-verification-identity-mismatch`,
  `plan-model-verification-identity-mismatch`,
  `storage-read-profile-stale`, `idempotency-conflict`.
- Nový stav záznamu: `ACTIVE` (aditívny; ledger schema zostáva v1 —
  doplnkové polia + stav sú spätne kompatibilné, starý reader
  pred-19.8.1 ACTIVE nepozná → one-way upgrade path, dokumentované).
- `ArtifactVerification`: nové pole `manifest_fingerprint`.
- Admission record: nové polia `request_fingerprint`, `started_at`.

## 5. Testy

Nové regresné testy: `TestAdmitRandomReadFloorsEnforced`,
`TestAdmitBenchmarkStaleRefused`, `TestAdmitVerificationIdentityMismatch`,
`TestAdmitKVCountedExactlyOnce`, `TestAdmitIdempotencyConflict`,
`TestAdmitStaleSnapshotRefusedOnSSDPath`, `TestAdmissionRenewExtendsLease`,
`TestAdmissionConfirmStartedLifecycle`,
`TestManifestFingerprintIdentityBinding`, `TestLoadManifestsStrictYAML`,
`TestLoadExpertPacksStrictYAML`, `TestLoadRuntimeProfileStrictYAML`,
`TestHTTPHealthProberLocalOnly`, `TestPlanRefusesVerificationIdentityMismatch`,
API `TestAdmissionLifecycleRoutes`. Aktualizované fixtures (fingerprint-bound
verifikácie; policy verzie; KV single-count očakávania).

Smoke: **check_41** (nový, 41 checks celkovo) + current-release asserty v
check_28/check_37 presunuté na v19.8.1.

## 6. Kompatibilita / migrácia

- **Verifikačné záznamy**: pre-19.8.1 záznamy (bez fingerprintu) budú
  odmietnuté — pre-bežte `scarlix-inventory --verify-manifests` po upgrade.
- **Ledger**: existujúci HELD záznamy fungujú; nové polia sa doplnia pri
  zápise. Starý reader (pre-19.8.1) ACTIVE nepozná — one-way upgrade.
- **Idempotency kľúče**: zatvorené záznamy — kľúč reuse-ovateľný (nové
  pravidlo, dokumentované).

## 7. Enablement checklist (pred prvým InferSSD enable na cieľovom hoste)

1. §8.7 acceptance matrix vyplnená (datovaná) — CPU/RAM/GPU/CUDA/driver +
   SSD model/firmware/fs/mount + sequential + random IOPS p50/p95/p99 +
   free bytes pred/po + baseline SGLang / + pack / InferSSD performance +
   contention/failover správanie.
2. Digest-pinned image prítomný a overený.
3. `RunReadBenchmark` s `RandomReads` na cieľovom SSD (benchmark mladší ako
   `MaxBenchmarkAge`); floors zaznamenané.
4. GPU compute cap + driver CUDA EXACT match zdokumentovaný.
5. Politika injektuje `SSDProfile` až po `Validate` prejde; reštart ScarliHQ;
   potvrdiť, že plan refusals sa pretnú len vtedy.
6. Verifikácia manifestov aktuálna (`--verify-manifests` — fingerprint viazaný).

## 8. Residuals (bez zmeny, zostávajú pomenované)

- Hardware acceptance **OPEN** (KPK-06; §8.7 target-host matrix — operátorov akt).
- Žiadny enabled profile sa nedoručuje; žiadny infer_ssd runtime nie je
  pozorovaný; pack-byte verification zostáva pomenovaná future work
  (declared digests are declarations).
- Host Bridge integrácia vlastní workload-side reconciliation (ACTIVE
  záznam mŕtveho procesa je crash-recovered na zázname; workload, ktorý
  prežil svoj proces, overí budúci Host Bridge reconciler).
- v19.9.0 distributed fabric / v20.0.0 production podľa roadmapy.

---

## 9. Post-release remediation (2026-11-13, po vydaní — bez zmeny verzie)

Na **čerstvom klone tagu v19.8.1** (sandbox prostredie bolo resetnuté, repozitár
obnovený z GitHubu) sa preverovala integrita vydania a objavili sa dva defekty
vlastnej akceptačnej sady v19.8.1 — release claim „30 packages OK" bol
zastaraný pre finálny stav fixture:

1. **Kódový defekt — poradie braní (admission):** `checkModel` bežala PRED
   ledger transakciou, takže replay s iným modelom pod rovnakým kľúčom
   vrátil `model-unknown` namiesto zdokumentovaného `idempotency-conflict`
   (udalosť zneužitia kľúča zamaskovaná okamžitou validáciou dopytu —
   presne to zakazuje kontrakt KPK-19.8.0-P2-04), a exaktný replay dopytu,
   ktorého manifest bol po prvej admisii odstránený, bol odmietnutý namiesto
   vrátenia PÔVODNÉHO rozhodnutia. **Oprava:** model/backend brány presunuté
   VNÚTRI atomickej ledger transakcie, ZA idempotency probe (krok 0.5) —
   mismatched replay teraz vracia `idempotency-conflict` aj keď samotný
   replay dopyt je nevalidný, a exaktný replay vracia originál bez ohľadu
   na neskorší stav manifestu; cesta pre čerstvé kľúče je nezmenená.
2. **Test-only defekty (planner):** 8 fixture mutovalo identity-bearing polia
   manifestu (backendy / checkpoint formát / shared bytes / KV estimate) bez
   re-bindu verifikačného záznamu na zmutovanú identitu — P1-02 fingerprint
   gate (fungujúci podľa dizajnu) ich odmietal: `TestPlanCPUExecutionClass`,
   `TestPlanBackendPresenceJoin`, `TestPlanBackendFormatMismatch`,
   `TestPlanGPUInsufficient`, `TestPlanRAMInsufficient`,
   `TestPlanPriorityTieBreak` (odmietnutie zanechalo `res.Plan` nil →
   panic), `TestPlanKVUnknownHonest` +
   `TestPlanExperimentalBackendCarriedVisibly` (zamaskované za panikou).
   Každý fixture sa teraz re-binduje cez `freshVerification(m, ...)` po
   mutácii — rovnaká disciplína, ktorou už SSR a pack suite prechádzali.

Akceptancia: `go test ./...` 30 balíkov OK; race čistý (admission + planner +
api); `go vet` čistý; smoke `--offline` 41 checks 0 FAIL; chirurgický diff
(37 insertov + 13 deletov v 2 súboroch, bez whitespace churn). Residuals
nezmenené: hardware acceptance OPEN (KPK-06), žiadny enabled profile,
pack-byte verification zostáva pomenovaná future work. Remediácia zaznamenaná
bez verzie bump podľa precedentu v19.8.0 post-release — ďalším release
zostáva **v19.9.0 (Fáza 5 — Distributed Compute Fabric)**.

---

*v19.8.1 · KpK remediation · ScarLiX OS*
