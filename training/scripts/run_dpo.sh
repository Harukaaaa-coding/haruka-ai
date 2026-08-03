#!/usr/bin/env bash
# Usage: DPO_MODEL=training/outputs/sft-merged training/scripts/run_dpo.sh training/data/dpo.train.jsonl [extra dpo.py args]
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PYTHON="${PYTHON_BIN:-$ROOT_DIR/training/.venv/bin/python}"
DATA="${1:-$ROOT_DIR/training/data/dpo.train.jsonl}"
if [[ $# -gt 0 ]]; then shift; fi
MODEL="${DPO_MODEL:?Set DPO_MODEL to the merged SFT model directory or base model ID}"
OUTPUT="${DPO_OUTPUT_DIR:-$ROOT_DIR/training/outputs/dpo-adapter}"

[[ -x "$PYTHON" ]] || { echo "Training venv is missing; run training/scripts/bootstrap_cuda.sh first" >&2; exit 1; }
"$PYTHON" "$ROOT_DIR/training/validate_data.py" --kind dpo --data "$DATA" --strict
for ((index = 1; index <= $#; index++)); do
  argument="${!index}"
  if [[ "$argument" == "--eval-data" ]]; then
    next_index=$((index + 1))
    (( next_index <= $# )) || { echo "--eval-data requires a path" >&2; exit 2; }
    "$PYTHON" "$ROOT_DIR/training/validate_data.py" --kind dpo --data "${!next_index}" --strict
  elif [[ "$argument" == --eval-data=* ]]; then
    "$PYTHON" "$ROOT_DIR/training/validate_data.py" --kind dpo --data "${argument#--eval-data=}" --strict
  fi
done
exec "$PYTHON" "$ROOT_DIR/training/dpo.py" --model "$MODEL" --data "$DATA" --output "$OUTPUT" --use-4bit "$@"
