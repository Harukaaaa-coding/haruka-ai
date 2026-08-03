"""Make reproducible train/evaluation JSONL splits without loading ML packages."""

from __future__ import annotations

import argparse
import random
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser(description="Split a JSONL dataset into train and eval files")
    parser.add_argument("--input", required=True)
    parser.add_argument("--train-output", required=True)
    parser.add_argument("--eval-output", required=True)
    parser.add_argument("--eval-ratio", type=float, default=0.05)
    parser.add_argument("--seed", type=int, default=42)
    args = parser.parse_args()
    if not 0 < args.eval_ratio < 1:
        parser.error("--eval-ratio must be between 0 and 1")

    source = Path(args.input)
    rows = [line for line in source.read_text(encoding="utf-8").splitlines() if line.strip()]
    if len(rows) < 2:
        parser.error("at least two non-empty rows are required")
    random.Random(args.seed).shuffle(rows)
    eval_count = max(1, round(len(rows) * args.eval_ratio))
    if eval_count >= len(rows):
        eval_count = len(rows) - 1
    train_rows, eval_rows = rows[eval_count:], rows[:eval_count]
    for output, values in ((Path(args.train_output), train_rows), (Path(args.eval_output), eval_rows)):
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text("\n".join(values) + "\n", encoding="utf-8")
    print(f"wrote {len(train_rows)} train and {len(eval_rows)} eval rows (seed={args.seed})")


if __name__ == "__main__":
    main()
