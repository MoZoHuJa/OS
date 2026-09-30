# ScaRgeN — Scar + Generation: 10 vízií pre SCARLIX OS v20+

> ScaRgeN = Scar (Scarlix) + Gen (Generácia). Nová generácia suverénneho domáceho AI OS.
> Tento dokument je brainstorm — nie roadmap. Myšlienky na využitie AI/ML v jadre OS.

---

## 1. 🧠 AI-Native Kernel Scheduler

**Problém:** systemd timery a AI inference bežia nezávisle. pacman update môže zaseknúť AI request, ZRAM swap môže vyhodiť model z RAM.

**Vízia:** ScaRgeN nahradí systemd timer pre AI workload scheduler. Jadro priamo informuje SGLang/Ollama o GPU power state, CPU affinity, a memory pressure. AI requesty dostanú real-time priority scheduling — žiadny AI request nezaseknutý kvôli `pacman -Syu`.

**Implementácia:** eBPF programy ktoré sledujú `nvidia-smi` + `/proc/meminfo` + AI API latency. Keď GPU memory pressure stúpne, scheduler automaticky zníži `--max-running-requests` na SGLang bez reštartu.

---

## 2. 🎯 Neural Model Router (NMR)

**Problém:** LiteLLM má statický fallback chain (SGLang→Ollama→BeeLlama). Nevie, že pre krátke chaty je Ollama rýchlejšia, ale pre dlhé kontexty je SGLang lepší.

**Vízia:** NMR je trénujúci router ktorý sa učí latency/cost/quality tradeoff per-user. Analyzuje TTFT + tok/s + context usage a dynamicky routuje. Model-agnostic — ak pridáš nový model, NMR automaticky zistí jeho profil cez `/v1/models` + benchmark.

**Implementácia:** Malý ML model (LogisticRegression alebo tiny transformer) ktorý beží na CPU. Features: request length, time-of-day, GPU temp, concurrent requests. Output: backend choice + confidence score.

---

## 3. 🔐 Homomorphic Encryption Layer

**Problém:** AI requesty obsahujú privátne dáta (zdravotné otázky, finančné). Aj lokálny model vidí plaintext.

**Vízia:** ScaRgeN pridá FHE (fully homomorphic encryption) vrstvu medzi klient a AI inference. Requesty sú encrypted end-to-end — ani host nemôže čítať prompt obsah. Pre rodinné zdravotné AI, finančné poradenstvo.

**Implementácia:** OpenFHE alebo Concrete-ML. Inference na encrypted inputy — pomalšie ale 100% private. Optional toggle: `scarlix-mode ai --private` aktivuje FHE layer.

---

## 4. 🌐 Federated Learning Mesh

**Problém:** Každý SCARLIX inštalácia má rovnaké modely (Qwen3-14B-AWQ), ale nevedia o sebe. Keď ty z domu použiješ AI, tvoj laptop musí stiahnuť celý model.

**Vízia:** ScaRgeN podporuje federovanú inferenciu cez Tailscale mesh. Viacero SCARLIX inštalácií (doma, v chate, u kamaráta) zdieľajú KV cache a partial results bez zdieľania modelov. Latencia klesá, privacy zostáva.

**Implementácia:** Tailscale + gRPC streaming. KV cache fragmenty sa posielajú medzi node-mi (každý má iný prefix). Pre 3 nodes = 3x viac KV cache kapacity bez ďalšej VRAM.

---

## 5. 🎙️ Voice-First OS Shell

**Problém:** Pre rodinné použitie (deti, starí rodičia) je CLI príliš technické. Dashboard vyžaduje monitor + myš.

**Vízia:** ScaRgeN integruje Whisper STT + Piper TTS do shell úrovne. `scarlix-mode ai` môžeš povedať nahlas. Chybové hlášky sa čítajú nahlas. Dashboard je ovládaný hlasom — žiadny monitor nepotrebný pre základné operácie.

**Implementácia:** `scarlix-voice` daemon — vždy počúva na wake word (openWakeWord), potom Whisper streaming STT → command parser → execution → Piper TTS odpoveď. Wake word: "Scarlix".

---

## 6. 🔄 Self-Healing BTRFS Snapshots

**Problém:** Keď sa model pokazí (Ollama CUDA regresia po update), manuálny rollback je pomalý a chybový.

**Vízia:** ScaRgeN využíva BTRFS send/receive pre incremental AI model snapshots. Keď healthcheck zlyhá 3x po sebe, ScaRgeN automaticky rollbackne `/models` na funkčný snapshot + reštartuje engine. Zero-downtime recovery.

**Implementácia:** Snapper pre `/models` subvolume. Každý `download-models.sh` vytvorí snapshot pred aj po. `scarlix-doctor --auto-heal` monitoruje health a rollbackne automaticky.

---

## 7. 🔑 Quantum-Safe Authentication

**Problém:** Bearer token auth je zraniteľný voči quantum computing (Shor's algorithm). Pre domáci AI OS ktorý môže bežať 10+ rokov, to je reálne riziko.

**Vízia:** ScaRgeN nahradí Bearer token auth post-quantum kryptografiou (Kyber/Dilithium). Future-proof proti quantum hrozbám. Per-user auth s WebAuthn + PQ signatures.

**Implementácia:** liboqs (Open Quantum Safe). ScarliHQ dashboard používa WebAuthn (fingerprint/FaceID) + PQ signature namiesto shared token. Každý rodinný člen má vlastný PQ keypair.

---

## 8. ⚡ Edge AI Cache (EAC)

**Problém:** Rodinné otázky sa opakujú ("aký je recept na...", "ako sa píše..."). Každý request ide cez GPU aj keď odpoveď je rovnaká.

**Vízia:** ScaRgeN pridá edge cache vrstvu ktorá cacheuje AI odpovede pri Tailscale gateway. Rodina z druhého PC dostane instant odpoveď na opakované otázky bez znovu generovania.

**Implementácia:** Redis alebo SQLite cache s semantic deduplication (embedding similarity > 0.95 = cache hit). TTS odpovede sa tiež cacheujú. Cache invalidácia pri model update (hash check).

---

## 9. 🎮 GPU Composable Fabric

**Problém:** 2 GPU (RTX 5060 Ti + RTX 4060 Ti) bežia nezávisle. Ak jeden idle a druhý preťažený, nedá sa presunúť workload.

**Vízia:** ScaRgeN podporuje composable GPU — ak pridáš druhý GPU, ScaRgeN automaticky rozdelí workload (SGLang na GPU0 pre chat, vLLM na GPU1 pre code generation). Dynamic rebalancing podľa request typu.

**Implementácia:** NMR (vízia #2) posiela GPU affinity s requestom. Docker compose s `NVIDIA_VISIBLE_DEVICES` sa dynamicky mení podľa load. vLLM TP=1 na GPU1, SGLang na GPU0.

---

## 10. 📊 Zero-Knowledge Telemetry

**Problém:** Pre improvement potrebujeme vedieť ako systém funguje (latencia, error rate, GPU usage). Ale telemetria = privacy leak.

**Vízia:** ScaRgeN zbiera telemetriu ale len v zero-knowledge forme — agregované štatistiky ktoré nedajú zistiť čo konkrétne sa robí. Lokálny Grafana dashboard bez privacy leaku.

**Implementácia:** Differential privacy — každá metrika má pridaný noise (Laplace mechanism). `avg_latency = real_avg + noise(ε=0.1)`. Trend je viditeľný, konkrétne hodnoty nie. Voliteľné: `scarlix-mode ai --telemetry=strict` vypne aj ZK telemetriu.

---

## 🔮 ScaRgeN Manifesto

ScaRgeN nie je len ďalšia verzia SCARLIX OS. Je to **prvý OS navrhnutý od základov pre AI**:

- **AI nie je aplikácia, je vrstva** — ako networking alebo filesystem
- **Privacy je default** — nie opt-in, ale opt-out
- **Self-healing je štandard** — žiadne manuálne opravy po update
- **Voice je first-class** — CLI je pre power-userov, voice pre rodinu
- **Quantum-ready** — 10+ rokov lifetime znamená PQ crypto teraz

**ScaRgeN v20.0 target:** 2026 Q2. Začne s víziami #5 (Voice), #6 (Self-healing), #8 (Edge Cache) — najnižšie riziko, najvyšší ROI pre rodinné použitie.

---

*This document is a brainstorm, not a commitment. Ideas evolve. SCARLIX OS v18.8.x is the stable foundation — ScaRgeN builds on top.*
