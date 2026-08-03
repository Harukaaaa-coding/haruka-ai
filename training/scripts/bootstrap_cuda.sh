#!/usr/bin/env bash
# Bootstrap a Linux/WSL cloud GPU worker. Run from the repository root.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VENV_DIR="${VENV_DIR:-$ROOT_DIR/training/.venv}"
PYTHON_BIN="${PYTHON_BIN:-python3}"
# CUDA 12.4 is a broadly supported default. Override this when your cloud
# provider's driver requires a different official PyTorch wheel index.
TORCH_INDEX_URL="${TORCH_INDEX_URL:-https://download.pytorch.org/whl/cu124}"

command -v nvidia-smi >/dev/null || { echo "NVIDIA GPU/driver not found" >&2; exit 1; }
command -v "$PYTHON_BIN" >/dev/null || { echo "Python 3 not found" >&2; exit 1; }
nvidia-smi

"$PYTHON_BIN" -m venv "$VENV_DIR"
PYTHON="$VENV_DIR/bin/python"
"$PYTHON" -m pip install --upgrade pip wheel
"$PYTHON" -m pip install torch torchvision --index-url "$TORCH_INDEX_URL"
"$PYTHON" -m pip install -r "$ROOT_DIR/training/requirements.txt"
"$PYTHON" - <<'PY'
import torch
print(f"torch={torch.__version__}")
print(f"cuda_available={torch.cuda.is_available()}")
if not torch.cuda.is_available():
    raise SystemExit("PyTorch cannot access CUDA; select a compatible TORCH_INDEX_URL or GPU image")
print(f"gpu={torch.cuda.get_device_name(0)}")
print(f"vram_gib={torch.cuda.get_device_properties(0).total_memory / 1024**3:.1f}")
print(f"bf16={torch.cuda.is_bf16_supported()}")
PY
