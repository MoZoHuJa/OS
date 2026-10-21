# ScarLiX OS — Dependency Audit: všetky externé programy vs upstream

**Dátum auditu:** 2026-10-21 (relácia Task 13)
**Rozsah:** všetky docker image piny (24 compose súborov + 4 Dockerfile FROM), Go moduly
(scarlihq), Pi-Bolt installer pin, externé URL v install.sh.
**Metóda:** statická verifikácia cez verejné registry API (hub.docker.com
`/v2/repositories/*/tags`, ghcr.io `/v2/*/tags/list` + manifest annotations,
proxy.golang.org `@v/list`) + git história (`git log -S` na presné piny).
**Obmedzenie (čestné):** sandbox nemá Docker — verifikované sú EXISTENCIA a DÁTUMY
tagov, nie beh kontajnerov. Behové testy sú v pláne na hostovi (§ Plán).
**Kontext:** naviazané na Hermes migráciu (Task 12, `agents/hermes/UPGRADE_REPORT.md`)
— užívateľ požiadal o rovnaký audit pre všetky ostatné programy.

> Základné pravidlo prevzaté z Hermes guide: **žiadna zmena verzie bez overenia**.
> Tento audit je fáza „audit + plán" (guide §13.1). Žiadny image pin nebol zmenený.

---

## 1. Executive Summary

| Skupina | Počet | Záver |
|---|---|---|
| ✅ OK — pin = upstream latest | 11 | žiadna akcia |
| 🔴 P0 — pin už NEEXISTUJE (pull fail) | 3 | coolify, sunshine, buzz/minio |
| 🟠 P1 — výrazne zastarané + default-on + security | 5 | crowdsec, ollama, gitea, nextcloud, headscale |
| 🟡 P2 — zastarané, deliberate upgrade | 12 | vllm, sglang, grafana, VM, n8n, piper, … |
| ⚪ P3 — build base images (EOL) | 2 | golang:1.23-alpine, alpine:3.20 |
| ⚫ Známe experimental `:latest` (dokumentované) | 5 | browser-mcp, freetoken, laya, needle, buzz |

**Kľúčový nález:** „Hermes problém" (starý pin + vyšla nová verzia + hrozí
rozbitie) má v repu **dve podoby**:
1. **Rozbité piny** — 3 tagy už na registri neexistujú (404): čistý deploy
   (`docker compose pull`) týchto stackov **zlyhá**. Rovnaký vzorec ako Hermes:
   upstream pohnul, náš pin zostal.
2. **Zastarané piny** — 17 programov má pin na verziu 3 týždne až 4 roky
   starú; nová verzia vyšla. Upgrade bez plánu = riziko rozbitia (breaking
   changes: config kľúče, DB migrácie, odstránené flagy).

**Gap v automatizácii:** CI workflow `pin-freshness.yml` monitoruje **len
Pi-Bolt installer hash**. Docker image piny nemajú žiadny automated drift
alarm — všetko je manuálne („Update deliberately when upgrading"). Odporúčam
rozšíriť workflow (§6).

---

## 2. Kompletná auditná tabuľka (dôkazy: dátumy pushu tagov z API, dátumy pinnutia z git)

`pinnuté` = git commit kedy bola aktuálna verzia pripnutá · `push` = kedy upstream
publikoval tag · `latest` = najnovší stable tag upstream (overený 2026-10-21)

### 2a. DEFAULT-ON služby (bežia pri `docker compose up` bez profilu)

| Program | Pin | Pinnuté | Push tagu | Latest stable | Gap | Stav |
|---|---|---|---|---|---|---|
| hermes | v0.22.2@digest | 2026-10-21 | 2026-10-15 | v0.22.2 (v0.22.3 len RC) | 0 | ✅ Task 12 |
| litellm | main-v1.23.9 | 2026-10-07 | ~2026-10 | main-v1.23.9 | 0 | ✅ |
| alertmanager | v0.34.1 | 2026-09-30 | 2026-09-17 | v0.34.1 | 0 | ✅ |
| whisper (faster-whisper-server) | sha-307e23f-cuda | 2026-10-02 | 2025-01-07 | 0.6.0-rc.3 == sha-307e23f | 0 | ✅ (pozn. nižšie) |
| llama.cpp | @sha256 (b11277) | 2026-10-01 | 2026-10-01 | b11749+ (≈470 buildov) | 3 týždne | 🟡 digest bez tagu |
| sglang | v0.4.9.post6-cu128-b200 | 2026-10-02 | 2025-07-29 | v0.5.13 (2026-06-11) | 10 mes. | 🟡 varianta! |
| smg | v1.4.1.post1-sglang-v0.5.10 | 2026-09-30 | ~2026-04 | v1.9.0-vllm-kimi-k3 (sglang-variant v1.4.1 = posledný) | — | ✅ viazané na sglang |
| vllm | v0.8.5 | 2026-10-01 | 2025-04-29 | v0.32.0 (2026-10-18) | 18 mes./24 min. | 🟡 Tier-2 gated |
| ollama | 0.5.4 | 2026-08-07 | 2024-12-17 | 0.40.7 (2026-10-21) | ~2 roky | 🟠 |
| hp-agent/coolify | 4.0.30 | 2026-08-07 | ? | 4.4.0 (2026-10-06) | 4 minory | 🔴 **tag 404** |
| hp-agent/gitea | 1.23 | 2026-08-07 | 2025-05-12 | 28.2.0 (2026-10-18) | major | 🟠 |
| hp-agent/n8n | 2.42.1 | 2026-09-30 | 2026-09-30 | 2.45.0 (2026-10-20) | 3 minory | 🟡 |
| hp-agent/nextcloud | 29 | 2026-08-07 | 2025-07-04 | 35.0.2 (2026-10-21) | 6 majorov | 🟠 |
| hp-agent/photoprism | 260919 | 2026-09-30 | 2026-09-19 | 261007 (2026-10-07) | 3 týždne | 🟡 |
| hp-agent/postgres | 16-alpine | 2026-08-07 | 2026-09-21 | 18.6 (16 stále patchované) | patch fresh | ✅ major neskôr |
| hp-agent/redis | 7-alpine | 2026-08-07 | 2026-09-21 | 8.8.3 (7 stále patchované) | patch fresh | ✅ major neskôr |
| network/headscale | 0.25.0 | 2026-08-07 | 2025-02-11 | 0.29.0 (2026-06-17) | 4 min./20 mes. | 🟠 |
| security/crowdsec | v1.4.0 | 2026-08-07 | **2022-07-19** | v1.8.1 (2026-09-03) | **4 roky** | 🟠 najhoršie |
| voice/piper | 1.5.0 | 2026-08-07 | 2024-02-26 | 2.5.2 | major | 🟡 |
| voice/wakeword | 1.10.0 | 2026-08-07 | 2024-02-18 | 2.1.0 | major | 🟡 |
| scarlihq | lokálny build (golang:1.23-alpine / alpine:3.20) | — | — | Go 1.27.3 / Alpine 3.24 | EOL base | ⚪ P3 |

### 2b. PROFILOVANÉ služby (opt-in, nebežia defaultne)

| Program | Pin | Pinnuté | Latest | Gap | Profil | Stav |
|---|---|---|---|---|---|---|
| jellyfin | 12.1 | 2026-09-30 | 12.2 (2026-10-05) | 1 minor | (žiadny — default!) | 🟡 |
| sunshine-tv | v2026.929.125923-ubuntu-24.04 | 2026-09-30 | v2026.1020.123939 (2026-10-20) | 3 týždne | [tv] | 🔴 **tag 404** |
| minecraft/pumpkin | @sha256 | 2026-09-30 | sha-*. novšie existujú | ? | (žiadny — default!) | 🟡 digest |
| monitoring/grafana | 11.3.0 | 2026-08-07 | 13.2.4 (2026-10-13) | 2 roky/2 majory | (žiadny — default!) | 🟡 Angular! |
| monitoring/victoria-metrics | v1.108.0 | 2026-08-07 | v1.154.1 (2026-10-20) | 2 roky | (žiadny — default!) | 🟡 |
| monitoring/otel-collector | 0.161.0 | 2026-09-30 | 0.162.0 (2026-10-05) | 1 minor | (žiadny — default!) | 🟡 |
| monitoring/openlit | 1.5.0 | 2026-10-01 | 1.9.2 | 4 minory | [tracing] | 🟡 |
| comfyui | v2-cuda-12.1.1-base-22.04-v0.2.7 | 2026-09-30 | v0.2.7 (posledný v sérii) | 0 | [creative] | ✅ v sérii |
| musicgen / video | lokálny build (python:3.11-slim, WAN2GP_COMMIT pripnutý) | — | python 3.11-slim fresh (2026-10-21) | 0 | [creative] | ✅ |
| buzz-relay | ghcr.io/block/buzz:latest | — | iba `sha-*` a `latest` tagy (bez semver) | floating | [buzz] | ⚫ známe |
| buzz/minio | RELEASE.2024-10-13T13-34-50Z | 2026-10-07 | ? (repo API 404) | 2 roky | [buzz] | 🔴 **repo 404** |
| browser-mcp | mcp/playwright-server:latest | — | repo API 404 | — | [experimental] | ⚫ známe (v18.8.2) |
| freetoken | freetoken/serving:latest | — | repo API 404 | — | [moe] | ⚫ známe (v18.8) |
| laya | laya/decision-engine:latest | — | repo API 404 | — | [experimental] | ⚫ známe (v18.8) |
| needle | ghcr.io/cactus-compute/needle:latest | — | ghcr 403 | — | [experimental] | ⚫ známe (v18.8.2) |

### 2c. Negrafické závislosti

| Závislosť | Pin | Latest | Stav |
|---|---|---|---|
| Pi-Bolt installer (SHA-256) | c92a8cd4…adccc | live hash zhodný (overené 2026-10-21) | ✅ + weekly CI monitoring |
| Go: github.com/gorilla/websocket | v1.5.3 | v1.5.3 (proxy.golang.org) | ✅ |
| Go: gopkg.in/yaml.v3 | v3.0.1 | v3.0.1 | ✅ |
| yay (AUR, install.sh) | git clone master | floating | ⚫ akceptované (build-time nástroj) |

**Poznámky:**
- `whisper`: pin `sha-307e23f-cuda` == upstream `0.6.0-rc.3` (rovnaký digest,
  overené cez manifest aj tag list). Upstream projekt premenoval na „Speaches"
  — už zdokumentované v compose (v19.0.3 P0-2), budúca samostatná migrácia.
- `llama.cpp`: pripnutý digest má anotácie `version=b11277, revision=eae11d2`
  (zistené cez ghcr manifest API). Najnovšie buildy sú b11749+. Pin je bez
  tagu — verzia nie je z compose vidno (anti-pattern, §6).
- `sunshine`: upstream povoľuje iba dátumové „nightly" tagy (dokumentované v
  compose v18.7.7 P1). Tag v2026.929 už v API 404; existujú v2026.1017 a
  v2026.1020.
- `coolify`: tag 4.0.30 v API 404; overené existujúce: 4.3.1 (2026-08-12),
  4.4.0 (2026-10-06). Kontajner má `docker.sock:ro` — privilegovaný kontext,
  upgrade overovať dôkladne.
- `gitea`: upstream verzovanie v tomto timeline prešlo na 28.x (latest 28.2.0,
  2026-10-18). Skok z 1.23 vyžaduje stepwise DB migrácie.
- `nextcloud`: 29 → 35 NIE je priamy skok; Nextcloud vyžaduje stepwise
  (30→31→…→35, každý `occ upgrade`). Overené existujúce medzičlánky: 33.0.9,
  34.0.4.
- `sglang`: pre v0.5.13 sondy nenašli `-cu128-b200` variant (404). Pred
  akýmkoľvek bumpom overiť existujúce varianty cieľovej verzie na hostovi —
  zmena CUDA suffixu naslepo rozbíja GPU passthrough (varovanie už v compose
  v19.0.1 P1).
- `grafana`: 12.0+ odstraňuje Angular — staré dashboardy/panelové pluginy môžu
  prestať fungovať. Pred bumpom inventarizovať dashboardy.
- `postgres/redis`: floating-patch piny v rámci majora sú čerstvé (2026-09-21);
  major upgrade (16→18, 7→8) je samostatný projekt s `pg_upgrade`/dump —
  naplánovať neskôr, nie v tomto cykle.

---

## 3. 🔴 P0 — rozbité piny (pull fail; najbližšie riešiť)

Všetky tri prípady: **ak host spraví `docker compose pull` / čistý deploy,
image sa nestiahne a služba nenastartuje** (pokiaľ nie je image už v lokálnom
cache). Dôkaz: HTTP 404 z `hub.docker.com/v2/repositories/{repo}/tags/{tag}`
(2026-10-21). Možná príčina: upstream prunol staré tagy (normálne správanie
pri minio/sunshine/coolify) — alebo obmedzenie API pohľadu; preto pred zmenou
vždy potvrdiť na hostovi.

### Postup (na hostovi, všetko 3×):

```bash
# 1) potvrdenie, že pin naozaj neexistuje (z hosta s Dockerom):
docker manifest inspect coollabsio/coolify:4.0.30            # očakávaný FAIL
docker manifest inspect lizardbyte/sunshine:v2026.929.125923-ubuntu-24.04  # FAIL
docker manifest inspect minio/minio:RELEASE.2024-10-13T13-34-50Z            # FAIL
# 2) overenie náhrady:
docker manifest inspect coollabsio/coolify:4.4.0             # cieľ (existuje, 2026-10-06)
docker manifest inspect lizardbyte/sunshine:v2026.1020.123939-ubuntu-24.04  # cieľ (2026-10-20)
docker pull minio/minio && docker image inspect minio/minio  # zistiť aktuálny RELEASE tag
```

| Program | Náhrada | Bezpečnostná poznámka | Rollback |
|---|---|---|---|
| coolify (hp-agent, default-on) | **stepwise**: 4.0.30→4.3.1→4.4.0 (DB migrácie medzi minorami; záloha `./coolify/data` + `pg_dump` pred každým krokom) | docker.sock:ro — po upgrade preveriť verziu a funkčnosť | obnoviť zálohu + vrátiť pin (image má host v cache) |
| sunshine-tv (gaming, profil tv) | v2026.1020.123939-ubuntu-24.04 **+ pripnúť digest** (nightly tagy prúnia) | `privileged: true` + host sieť — overiť po štarte pairing ovládača | image v cache; vrátiť starý tag, kým je v cache |
| buzz/minio (profil buzz) | zistiť dostupný RELEASE tag na hostovi; príp. alternatíva `quay.io/minio/minio` | buzz je experimental profil — najnižšia priorita | profil opt-in, nejde o produkciu |

**Dôležité:** pokiaľ `docker manifest inspect` pripnutého tagu NA HOSTOVI
prejde (t.j. 404 bol artefakt API pohľadu zo sandboxu), nič nemeň — pin je
platný a riziko odpadá. Zmena len s dôkazom.

---

## 4. 🟠 P1 — výrazne zastarané, default-on, bezpečnostne kritické

Vzorec „rovnaký problém ako Hermes": pinnuté 2026-08-07 (252d1f2), odvtedy
neboli dotknuté (~10,5 týždňa), upstream medzitým vydal nové verzie vrátane
security fixes.

| Program | Teraz → Cieľ | Postup (skrátený) | Riziká |
|---|---|---|---|
| **crowdsec** v1.4.0 (2022!) | → v1.8.1 | záloha `./config`+`./data` (SQLite) → **stepwise** 1.4→1.5→1.6→1.7→1.8 (každý krok: start, logy, `cscli bidders`/`cscli alerts list`) → overiť `COLLECTIONS` env (novšie verzie menia acquis/hub) | 4 roky CVE dlhu; zmenený hub formát |
| **ollama** 0.5.4 (2024-12) | → 0.40.7 | záloha `/var/lib/scarlix/ollama` → bump → healthcheck `ollama list` → test qwen2.5:3b CPU inferencie | mem_limit 6g (novšie vydania žerú viac); formát modelov |
| **gitea** 1.23 (2025-05) | → 28.2.0 (stepwise) | `gitea dump` + `pg_dump` → stepwise cez existujúce tagy → po každom kroku web + git push/pull test | major verzia, DB migrácie; GITEA__ env kompat overiť v changelogoch |
| **nextcloud** 29 (2025-07) | → 35.0.2 (stepwise) | full záloha (data+config+DB dump) → 29→30→…→35, každý: `occ maintenance:mode on` → image → `occ upgrade` → mode off → apps check | dlhé; app kompatibilita; EOL security tlak |
| **headscale** 0.25.0 (2025-02) | → 0.29.0 (stepwise) | záloha `./config`+`./data` → stepwise 0.25→0.26→…→0.29, po každom: `headscale serve` + pripojenie uzla | zmeny config formátu medzi minorami; noise klúče |

Všeobecné pre P1: **vždy najprv záloha + digest cieľového tagu + changelog
prečítaný + jeden krok naraz + overovací test + rollback plán**. Neprekračovať
viac než jednu minor/major verziu na krok.

## 5. 🟡 P2 — deliberate upgrade (naplánovať, nič nie je akútne rozbité)

- **grafana 11.3.0 → 13.2.4** (default-on!): **najprv inventár dashboardov —
  v12 padá Angular**. Export JSON dashboardov, hľadať angular pluginy,
  stepwise 11.3→11.x→12→13, GF_ env kompat.
- **victoria-metrics v1.108.0 → v1.154.1**: VM je konzervatívna; `--storageDataPath
  --retentionPeriod` stabilné. vmbackup pred bumpom; skok v rámci v1 akceptovateľný.
- **vllm v0.8.5 → v0.32.0**: 24 minorov — **stepwise po väčších míľnikoch**
  (0.10→0.15→0.20→0.25→0.32). Pred každým: overiť server flagy (`--dtype`,
  LoRA flagy) v `--help` cieľa. Tier-2 je gated cez `/etc/scarlix/.experimental`
  → nižšia urgentnosť.
- **sglang v0.4.9.post6-cu128-b200 → v0.5.x**: **cielené varovanie** — pre v0.5.13
  neexistuje `-cu128-b200` variant (sondy 404). Na hostovi vypísať dostupné
  varianty cieľa; nesmieň meniť CUDA suffix naslepo. `--disable-flashinfer`
  musí v cieľovej verzii existovať (Blackwell sm_120). Tier-1 DEFAULT —
  najopatrnejší bump v celom systéme. Súvisí so **smg** (sglang-variant smg
  v1.4.1 je posledný; novšie smg v1.8/1.9 sú vllm-kimi-k3) → rozhodnutie
  sglang-ostáva vs vllm-prechod je architektonické, nie mechanické.
- **n8n 2.42.1 → 2.45.0**: záloha `./n8n/data` + DB dump; **POZOR**:
  `N8N_BASIC_AUTH_*` env sú v novších n8n deprekované (user management) —
  overiť auth flow po upgrade; test jeden workflow run.
- **jellyfin 12.1 → 12.2**: minor; po upgrade prejsť pluginy v UI.
- **photoprism 260919 → 261007**: date tag; automatická DB migrácia pri štarte;
  záloha `./photoprism/storage`.
- **otel-collector 0.161.0 → 0.162.0**: minor; `otelcol validate` configu
  cieľovej verzie pred nasadením.
- **piper 1.5.0 → 2.5.2** a **wakeword 1.10.0 → 2.1.0**: major skoky (2.5 r.);
  overiť, že `--voice sk_SK-lili-medium` a `--wake-word hey_jarvis` + threshold
  flagy v cieľových verziách existujú; test TTS + wake detekcie.
- **openlit 1.5.0 → 1.9.2** [tracing]: 4 minory; changelog + env check.
- **llama.cpp**: re-pull digest + **dopísať build číslo do komentára** (b11749+)
  — digest-only pin bez anotácie je neudržateľný (§6).
- **pumpkin (minecraft)**: re-pull `latest` digest + koment s `sha-*` tagom.

## ⚪ P3 — build base images

- `scarlihq/Dockerfile`: `golang:1.23-alpine` (Go 1.23 EOL, posledný patch
  2025-08-06) → `golang:1.25-alpine`+; `alpine:3.20` (EOL od ~2026-05) →
  `alpine:3.22`+. Statický Go binary, nízke riziko; CI má docker-build +
  runtime smoke test, ktoré to overia. `go.mod` `go 1.23` direktíva môže ostať
  (novší toolchain kompiluje spätne kompatibilne).
- `python:3.11-slim` (musicgen, video): fresh (2026-10-21); Python 3.11 EOL
  2027-10 → naplánovať 3.12/3.13 migráciu do budúcna. Teraz OK.

---

## 6. Gap: automated monitoring docker pinov

`pin-freshness.yml` (weekly) kontroluje len Pi-Bolt hash — **docker piny
nemajú drift alarm**. Návrh pre budúci release (neimplementované zámerne —
audit je fáza „plán"):

1. **Existencia tagov (P0 detector):** weekly job, ktorý pre každý pripnutý
   `image:tag` zavolá registry API a pri 404 FAILne (coolify/sunshine/minio
   by odhalil týždne pred týmto auditom).
2. **Vek tagu (P1/P2 detector):** rovnaký job vypíše `last_updated` pripnutého
   tagu; FAIL ak pin starší než N dní (napr. 180 pre security-kritické,
   365 pre ostatné) — s allowlistom pre „frozen on purpose" piny
   (whisper-rc, smg-sglang-variant, …).
3. **Digest piny s anotáciou:** pre `@sha256` piny vypísať
   `org.opencontainers.image.version` do komentára compose súboru (llama.cpp,
   pumpkin) — verzia musí byť čitateľná bez API.

## 7. Reprodukcia auditu

```bash
# existencia + dátum pripnutého tagu:
curl -s "https://hub.docker.com/v2/repositories/{ns}/{repo}/tags/{tag}" | jq -r '.last_updated'
# najnovšie pushnuté tagy:
curl -s "https://hub.docker.com/v2/repositories/{ns}/{repo}/tags?page_size=100&ordering=last_updated" | jq -r '.results[].name' | head
# ghcr.io (anonymný token + tags/list):
curl -s "https://ghcr.io/token?scope=repository:{owner}/{repo}:pull&service=ghcr.io" | jq -r .token
# digest → verzia/revízia (anotácie):
curl -s -H "Authorization: Bearer $TOK" -H "Accept: application/vnd.oci.image.index.v1+json" \
  "https://ghcr.io/v2/{owner}/{repo}/manifests/sha256:..." | jq '.annotations'
# kedy bol pin pinnutý (v repo):
git log --format="%ad %h" --date=short -1 -S "image:tag" -- path/to/compose.yml
```

## 8. Čo bolo zámerne NEMENENÉ (a prečo)

- **Žiadny image pin nebol zmenený.** Pravidlo repa (Hermes guide §13.1 +
  „Zakázané skratky"): „Nepoužiť neoverený image". Sandbox nemá Docker — beh
  nových verzií nemožno overiť; existencia tagu cez API nie je dôkaz
  kompatibility. Každá zmena verzie preto čaká na overenie na hostovi (§3–§5).
- **CI workflow pin-freshness nebol rozšírený** — návrh v §6, implementácia
  ako samostatný krok po odsúhlasení.
- **Verzia systému nebola bumpnutá, tag nebol vytvorený** — konzistentne so
  zadanim Task 12 (in-place zmeny v aktuálnej verzii, žiadna nová git verzia).

*Audit vyhotovil: Z.ai Code (Zmor), Task 13, 2026-10-21. Všetky dátumy a
existencie tagov overené priamo proti registry API v deň auditu; git dátumy
pinnutia z histórie repa (main 908b6ca).*
