#!/usr/bin/env bash
set -euo pipefail

# SCARLIX OS v19.0.0 — Model Downloader (v17.5 keys, correct HF repo IDs)
#
# v18.5 FIXES:
#   - Missing Ollama compose file = FAILED (was: silently skipped → false "complete")
#   - Missing SGLang hf_repo = FAILED (was: warn only → false "complete")
#   - Disk-space check before EACH download (not just start — partial-download protection)
# v17.9.7 FIXES:
#   - FAILED counter (FAILED=$((FAILED+1)) — was boolean overwrite)
#   - Disk-space pre-check on /models (fail-hard < 10GB)
# v17.9.5 FIXES (vs v17.5):
#   - Uses v17.5 keys: .beellama.* (not .llamacpp.*), .ollama.model (not .ollama_main)
#   - SGLang: uses hf_repo field (not model_path which is local path)
#   - Uses python venv for huggingface_hub (PEP 668 safe)
#   - Waits for Ollama API before pull
#   - Correct container names (ollama-agent, not ollama-main)

# v18.5 P0: download-models.sh needs root for /var/lock + /var/log/scarlix/ + pacman
# v18.8 P1: Drop -E (was: sudo -E preserved entire env → unnecessary surface for
#   a privileged downloader; also inconsistent with scarlix-mode which dropped -E
#   in v18.7.7. MODELS_CONFIG/MODELS_DIR overrides below use ${VAR:-default}
#   which still works because sudo preserves HOME by default; for explicit override
#   users should edit /etc/scarlix/models.yaml, not pass env vars to root.)
if [ "$(id -u)" -ne 0 ]; then
  exec sudo /usr/local/bin/download-models.sh "$@"
fi

MODELS_CONFIG="${MODELS_CONFIG:-/etc/scarlix/models.yaml}"
MODELS_DIR="${MODELS_DIR:-/models}"
LOG_FILE="/var/log/scarlix/model-download.log"
VENV_DIR="/opt/scarlix/venv"

mkdir -p "$(dirname "$LOG_FILE")" "$MODELS_DIR" "$VENV_DIR"

# v18.5 P1: Shared lock with scarlix-mode + model-manager (was: race condition on /models)
# v18.5 P0: Lock file moved to /var/lib/scarlix/ (was: /var/lock — EACCES on Arch which is 0755 root:root, not 1777 like Debian)
# v18.5 P0: /var/lib/scarlix/ is root:root 755 after install.sh P0-1 fix — safe for root to write
MODELS_LOCK="/var/lib/scarlix/.models.lock"
if ! mkdir -p "$(dirname "$MODELS_LOCK")" 2>/dev/null; then
  echo "FATAL: cannot create models lock directory $(dirname "$MODELS_LOCK")" >&2
  exit 1
fi
# v18.8 P1: Fail-closed mkdir (was: || true → cryptic set -e exit if dir RO)
exec 9>"$MODELS_LOCK"
# v18.5 P0: Non-blocking + exclusive (was: shared -s, blocking without -n)
# v18.5 P1: Exclusive because download-models WRITES to /models (was: -s shared → race with model-manager)
flock -n -x 9 || { echo "ERROR: cannot acquire models lock (scarlix-mode or model-manager running?)" >&2; exit 1; }

echo "============================================" | tee "$LOG_FILE"
echo "  SCARLIX OS v19.0.0 — Model Downloader" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"

# Install yq if missing
command -v yq >/dev/null 2>&1 || { echo "Installing yq..."; pacman -S --noconfirm --needed yq >> "$LOG_FILE" 2>&1 || { echo "ERROR: yq install failed"; exit 1; }; }

# P1 v17.9.7: Disk-space pre-check (full models = 50-150GB)
FREE_MB=$(df -m "$MODELS_DIR" 2>/dev/null | awk 'NR==2{print $4}')
if [ -n "$FREE_MB" ]; then
  FREE_GB=$((FREE_MB / 1024))
  echo "  /models free space: ${FREE_GB}GB (${FREE_MB}MB)" | tee -a "$LOG_FILE"
  if [ "$FREE_MB" -lt 10240 ]; then
    echo "  ✗ CRITICAL: < 10GB free on /models — cannot download models" | tee -a "$LOG_FILE"
    exit 1
  elif [ "$FREE_MB" -lt 51200 ]; then
    echo "  ⚠ WARNING: < 50GB free on /models — full models may not fit (need 50-150GB)" | tee -a "$LOG_FILE"
    echo "  Continuing anyway (SGLang AWQ ~9GB, GGUF ~9GB, Ollama qwen2.5:3b ~2GB)..." | tee -a "$LOG_FILE"
  fi
fi

# v18.5.3 P0: Disk-space check function (was: checked once at start, not before each download)
check_disk_space() {
  local required_mb="${1:-1024}"
  local free_mb
  free_mb=$(df -Pm "$MODELS_DIR" 2>/dev/null | awk 'NR==2{print $4}')
  if ! [[ "$free_mb" =~ ^[0-9]+$ ]]; then
    echo "  ✗ ERROR: unable to determine free disk space" | tee -a "$LOG_FILE"
    return 1
  fi
  if [ "$free_mb" -lt "$required_mb" ]; then
    echo "  ✗ ERROR: insufficient disk space (need ${required_mb}MB, have ${free_mb}MB)" | tee -a "$LOG_FILE"
    return 1
  fi
  return 0
}

# Setup venv for huggingface_hub (PEP 668 safe)
if [ ! -f "$VENV_DIR/bin/huggingface-cli" ]; then
  echo "Setting up Python venv for huggingface-cli..." | tee -a "$LOG_FILE"
  python -m venv "$VENV_DIR" >> "$LOG_FILE" 2>&1
  # v18.7.6 P2: Exact dependency pinning (was: range >=0.25,<0.27 → not reproducible)
  "$VENV_DIR/bin/pip" install "pip==24.3.1" "huggingface_hub[cli]==0.26.2" >> "$LOG_FILE" 2>&1 || { echo "ERROR: huggingface_hub install failed"; exit 1; }
fi
HF_CLI="$VENV_DIR/bin/huggingface-cli"

# v18.5 P1: Validate models.yaml schema (was: silent fail on typos like hf_repo_id)
validate_models_yaml() {
  local errors=0
  local required_keys=(".sglang.hf_repo" ".sglang.model_path" ".beellama.hf_repo" ".beellama.hf_file" ".ollama.model")
  echo "  Validating models.yaml schema..." | tee -a "$LOG_FILE"
  for key in "${required_keys[@]}"; do
    local val
    val=$(yq -r "$key // empty" "$MODELS_CONFIG" 2>/dev/null || echo "")
    if [ -z "$val" ] || [ "$val" = "null" ]; then
      echo "  ✗ Missing or invalid: $key" | tee -a "$LOG_FILE"
      errors=$((errors + 1))
    else
      echo "  ✓ $key = $val" | tee -a "$LOG_FILE"
    fi
  done
  # v18.9.8 P1-8: Check for deprecated/removed keys (was: old config from previous
  #   version silently ignored → stale values used. Now: warn on deprecated keys.)
  local deprecated_keys=(".sglang.quantization" ".sglang.flashinfer" ".beellama.model_file")
  for dkey in "${deprecated_keys[@]}"; do
    local dval
    dval=$(yq -r "$dkey // empty" "$MODELS_CONFIG" 2>/dev/null || echo "")
    if [ -n "$dval" ] && [ "$dval" != "null" ]; then
      echo "  ⚠ DEPRECATED: $dkey is no longer used (value: $dval) — remove from models.yaml" | tee -a "$LOG_FILE"
    fi
  done
  if [ "$errors" -gt 0 ]; then
    echo "FATAL: models.yaml schema validation failed ($errors errors)" | tee -a "$LOG_FILE"
    return 1
  fi
  echo "  Schema OK" | tee -a "$LOG_FILE"
  return 0
}
validate_models_yaml || exit 1

TOTAL_STEPS=4
CURRENT_STEP=0
FAILED=0
progress() {
  CURRENT_STEP=$((CURRENT_STEP + 1))
  local pct=$((CURRENT_STEP * 100 / TOTAL_STEPS))
  echo "" | tee -a "$LOG_FILE"
  echo "[$CURRENT_STEP/$TOTAL_STEPS] ($pct%) $1" | tee -a "$LOG_FILE"
}

# === Step 1: SGLang model (safetensors, GPU 0) ===
# v17.9.5 FIX: Use hf_repo field (not model_path which is local)
# v18.5 FIX: missing hf_repo = FAILED (was: warn only → false "complete")
progress "SGLang model (safetensors)"
SGLANG_HF_REPO=$(yq -r '.sglang.hf_repo // empty' "$MODELS_CONFIG" 2>/dev/null || echo "")
if [ -z "$SGLANG_HF_REPO" ]; then
  echo "  ✗ No .sglang.hf_repo in models.yaml — SGLang is REQUIRED (Tier-1 engine)" | tee -a "$LOG_FILE"
  echo "  To enable: add 'hf_repo: Qwen/Qwen3-14B-AWQ' to .sglang section" | tee -a "$LOG_FILE"
  FAILED=$((FAILED+1))
else
  SGLANG_LOCAL_PATH=$(yq -r '.sglang.model_path // empty' "$MODELS_CONFIG" 2>/dev/null || echo "/models/$SGLANG_HF_REPO")
  # v18.9.7 P1-06: Validate model_path is under /models/ (was: no validation →
  #   user could set arbitrary path → download to wrong location)
  if [[ "$SGLANG_LOCAL_PATH" != /models/* ]]; then
    echo "  ✗ ERROR: sglang.model_path must start with /models/ (got: $SGLANG_LOCAL_PATH)" | tee -a "$LOG_FILE"
    FAILED=$((FAILED+1)); continue_skipped=1
  fi
  # v18.5.3 P0: Check disk space before EACH download (was: only checked once at start)
  check_disk_space 12000 || { FAILED=$((FAILED+1)); continue_skipped=1; }
  if [ -z "${continue_skipped:-}" ]; then
    echo "  Downloading: $SGLANG_HF_REPO → $SGLANG_LOCAL_PATH" | tee -a "$LOG_FILE"
    # v18.9.3 P2: Unique staging dir + atomic swap (was: fixed staging path + rm -rf old + mv
    #   → if mv failed after rm -rf, user lost both old and new model. Now: unique
    #   staging via mktemp -d, swap via mv to .new then rename, old model preserved
    #   as .previous for rollback)
    # v18.9.4 P2-03: Explicit permissions on staging dir (was: mkdir without chown/chmod)
    # v18.9.4 P2-04: Fail-closed mkdir (was: || true → mktemp fails silently later)
    STAGING_DIR="/models/.staging"
    if ! mkdir -p "$STAGING_DIR" 2>/dev/null; then
      echo "  ✗ FATAL: cannot create staging directory $STAGING_DIR" | tee -a "$LOG_FILE"
      FAILED=$((FAILED+1)); continue_skipped=1
    else
      chown root:root "$STAGING_DIR" 2>/dev/null || true
      chmod 700 "$STAGING_DIR" 2>/dev/null || true
    fi
    MODEL_NAME=$(basename "$SGLANG_LOCAL_PATH")
    STAGING_PATH=$(mktemp -d "${STAGING_DIR}/${MODEL_NAME}.XXXXXX") || { echo "  ✗ Cannot create staging dir" | tee -a "$LOG_FILE"; FAILED=$((FAILED+1)); continue_skipped=1; }
    if [ -z "${continue_skipped:-}" ]; then
    if "$HF_CLI" download "$SGLANG_HF_REPO" --local-dir "$STAGING_PATH" >> "$LOG_FILE" 2>&1; then
      # v18.9.3 P2: Verify model has config.json + at least one safetensors/index
      if [ -f "$STAGING_PATH/config.json" ]; then
        # v18.9.4 P1-04: Full shard verification (was: only checked *.safetensors exists →
        #   partial model with missing shards accepted. Now: if index.json exists,
        #   parse it and verify ALL shards are present.)
        verify_safetensors() {
          local model_dir="$1"
          if [ -f "$model_dir/model.safetensors.index.json" ]; then
            # v18.9.4 P1-04: Parse index and verify all shards
            python3 -c "
import json, os, sys
with open(os.path.join('$model_dir', 'model.safetensors.index.json')) as f:
    idx = json.load(f)
shards = set(idx.get('weight_map', {}).values())
missing = [s for s in shards if not os.path.exists(os.path.join('$model_dir', s))]
if missing:
    print(f'MISSING_SHARDS:{missing}', file=sys.stderr)
    sys.exit(1)
print(f'Verified {len(shards)} shards', file=sys.stderr)
" 2>&1 | tee -a "$LOG_FILE"
            return $?
          elif ls "$model_dir"/*.safetensors 1>/dev/null 2>&1; then
            # Single safetensors file — OK
            return 0
          else
            return 1
          fi
        }
        if verify_safetensors "$STAGING_PATH"; then
          # v18.9.3 P2: Atomic swap — rename old to .previous, mv new to target
          # If mv fails, old model is preserved as .previous and can be restored
          if [ -d "$SGLANG_LOCAL_PATH" ]; then
            mv -f "$SGLANG_LOCAL_PATH" "${SGLANG_LOCAL_PATH}.previous" 2>/dev/null || true
          fi
          if mv -f "$STAGING_PATH" "$SGLANG_LOCAL_PATH" 2>/dev/null; then
            echo "  ✓ SGLang model downloaded + verified (all shards present)" | tee -a "$LOG_FILE"
            # v18.9.4 P2-01: Keep .previous for rollback (was: rm -rf .previous on success
            #   → if new model fails at runtime, no rollback available. Now: .previous
            #   is kept and can be manually restored. Cleaned up on next successful update.)
          else
            echo "  ✗ SGLang staging move FAILED — restoring previous model" | tee -a "$LOG_FILE"
            # Restore old model if it was moved to .previous
            [ -d "${SGLANG_LOCAL_PATH}.previous" ] && mv -f "${SGLANG_LOCAL_PATH}.previous" "$SGLANG_LOCAL_PATH" 2>/dev/null || true
            rm -rf "$STAGING_PATH" 2>/dev/null || true
            FAILED=$((FAILED+1))
          fi
        else
          echo "  ✗ SGLang download incomplete (no safetensors files found)" | tee -a "$LOG_FILE"
          rm -rf "$STAGING_PATH" 2>/dev/null || true
          # Restore old model if it was moved
          [ -d "${SGLANG_LOCAL_PATH}.previous" ] && mv -f "${SGLANG_LOCAL_PATH}.previous" "$SGLANG_LOCAL_PATH" 2>/dev/null || true
          FAILED=$((FAILED+1))
        fi
      else
        echo "  ✗ SGLang download incomplete (no config.json in staging)" | tee -a "$LOG_FILE"
        rm -rf "$STAGING_PATH" 2>/dev/null || true
        FAILED=$((FAILED+1))
      fi
    else
      echo "  ✗ SGLang download FAILED" | tee -a "$LOG_FILE"
      rm -rf "$STAGING_PATH" 2>/dev/null || true
      FAILED=$((FAILED+1))
    fi
    fi  # v18.9.3: close continue_skipped guard
  fi
  unset continue_skipped 2>/dev/null || true
fi

# === Step 2: BeeLlama / llama.cpp GGUF model (CPU offline) ===
# v17.9.5 FIX: Uses .beellama.* keys (not .llamacpp.*)
progress "BeeLlama/llama.cpp GGUF model (CPU offline)"
BEE_HF_REPO=$(yq -r '.beellama.hf_repo // empty' "$MODELS_CONFIG" 2>/dev/null || echo "")
BEE_HF_FILE=$(yq -r '.beellama.hf_file // empty' "$MODELS_CONFIG" 2>/dev/null || echo "")
if [ -z "$BEE_HF_REPO" ] || [ -z "$BEE_HF_FILE" ]; then
  # v18.8 P1: BeeLlama is Tier-4 CPU fallback — optional but warned (was: silent
  #   "NOT downloaded" → download-models.sh exited SUCCESS even though offline
  #   fallback model was missing → scarlix-mode offline would fail later with
  #   no model. Now: explicit warning that offline mode will be broken without it,
  #   but NOT FAILED++ since BeeLlama is optional — SGLang+Ollama stack still works.)
  echo "  ⚠ No .beellama.hf_repo/hf_file in models.yaml — GGUF NOT downloaded" | tee -a "$LOG_FILE"
  echo "  ⚠ OFFLINE MODE (scarlix-mode offline) WILL NOT WORK without this model" | tee -a "$LOG_FILE"
  echo "  ⚠ AI/creative/game/turbo modes still work (SGLang + Ollama fallback)" | tee -a "$LOG_FILE"
else
  # v18.5.3 P0: Check disk space before BeeLlama download
  check_disk_space 10000 || { FAILED=$((FAILED+1)); continue_skipped=1; }
  if [ -z "${continue_skipped:-}" ]; then
    echo "  Downloading: $BEE_HF_REPO / $BEE_HF_FILE → /models/" | tee -a "$LOG_FILE"
    if "$HF_CLI" download "$BEE_HF_REPO" --include "$BEE_HF_FILE" --local-dir "$MODELS_DIR" >> "$LOG_FILE" 2>&1; then
      echo "  ✓ GGUF model downloaded" | tee -a "$LOG_FILE"
    else
      echo "  ✗ GGUF download FAILED" | tee -a "$LOG_FILE"
      FAILED=$((FAILED+1))
    fi
  fi
  unset continue_skipped 2>/dev/null || true
fi

# === Step 3: Ollama model (CPU fallback) ===
# v17.9.5 FIX: Uses .ollama.model (not .ollama_main.model), correct container name
# v18.5 FIX: missing compose file = FAILED (was: silently skipped → false "complete")
progress "Ollama model"
OLLAMA_MODEL=$(yq -r '.ollama.model // "qwen2.5:3b"' "$MODELS_CONFIG" 2>/dev/null || echo "qwen2.5:3b")
echo "  Pulling Ollama model: $OLLAMA_MODEL" | tee -a "$LOG_FILE"
# Start Ollama container
if ! command -v docker >/dev/null 2>&1; then
  echo "  ✗ Docker not installed — Ollama model NOT pulled" | tee -a "$LOG_FILE"
  FAILED=$((FAILED+1))
else
  COMPOSE_FILE="/opt/scarlix/ai/ollama/docker-compose.yml"
  if [ ! -f "$COMPOSE_FILE" ]; then
    echo "  ✗ Ollama compose file missing: $COMPOSE_FILE" | tee -a "$LOG_FILE"
    FAILED=$((FAILED+1))
  else
    # v18.5.2 P1: Don't mask docker compose failure (was: || true → continued → 60s wait → docker exec fail)
    if ! docker compose -f "$COMPOSE_FILE" up -d >> "$LOG_FILE" 2>&1; then
      echo "  ✗ Ollama container startup FAILED" | tee -a "$LOG_FILE"
      FAILED=$((FAILED+1))
    else
      # Wait for Ollama API to be ready (max 60s)
      echo "  Waiting for Ollama API..." | tee -a "$LOG_FILE"
      # v18.5.3 P0: Track ready state (was: break from loop but continued to docker exec regardless)
      OLLAMA_API_READY=false
      for i in $(seq 1 12); do
        if curl -sf http://localhost:11435/api/tags >/dev/null 2>&1 || curl -sf http://localhost:11434/api/tags >/dev/null 2>&1; then
          echo "  Ollama API ready" | tee -a "$LOG_FILE"
          OLLAMA_API_READY=true
          break
        fi
        sleep 5
      done
      # v18.5.3 P0: Only pull if API actually became ready (was: continued to docker exec even after timeout)
      if [ "$OLLAMA_API_READY" != true ]; then
        echo "  ✗ Ollama API did not become ready within 60s — skipping model pull" | tee -a "$LOG_FILE"
        FAILED=$((FAILED+1))
      elif docker exec ollama-agent ollama pull "$OLLAMA_MODEL" >> "$LOG_FILE" 2>&1; then
        echo "  ✓ Ollama model pulled: $OLLAMA_MODEL" | tee -a "$LOG_FILE"
      else
        echo "  ✗ Ollama pull FAILED" | tee -a "$LOG_FILE"
        FAILED=$((FAILED+1))
      fi
    fi
  fi
fi

# === Step 4: Verify ===
progress "Verification"
echo "  Models in /models:" | tee -a "$LOG_FILE"
ls -lh "$MODELS_DIR"/*.gguf 2>/dev/null | tee -a "$LOG_FILE" || echo "  (no .gguf files found)" | tee -a "$LOG_FILE"
ls -d "$MODELS_DIR"/*/ 2>/dev/null | tee -a "$LOG_FILE" || echo "  (no safetensors dirs found)" | tee -a "$LOG_FILE"

echo "" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"
if [ "$FAILED" -ne 0 ]; then
  echo "  ✗ MODEL DOWNLOAD FAILED — check $LOG_FILE" | tee -a "$LOG_FILE"
  echo "  Some models may be missing. scarlix-mode ai may not start." | tee -a "$LOG_FILE"
  exit 1
fi
echo "  Model download complete" | tee -a "$LOG_FILE"
echo "  Next: scarlix-mode ai" | tee -a "$LOG_FILE"
echo "============================================" | tee -a "$LOG_FILE"
