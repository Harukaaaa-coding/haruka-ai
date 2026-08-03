"""Validate JSONL datasets before sending them to a paid GPU job.

The validator intentionally uses only the Python standard library so it can run
on the developer machine before the training environment is installed.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import Counter
from pathlib import Path
from typing import Any


ALLOWED_ROLES = {"system", "user", "assistant"}
PLACEHOLDER_RE = re.compile(r"<[^>]{1,80}>|\[TODO\]|待补充", re.IGNORECASE)
SECRET_PATTERNS = (
    re.compile(r"\bsk-[A-Za-z0-9_-]{16,}\b"),
    re.compile(r"\bAKIA[0-9A-Z]{16}\b"),
    re.compile(r"(?i)api[_ -]?key\s*[:=]\s*[^\s]{8,}"),
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Validate GopherAI SFT or DPO JSONL data")
    parser.add_argument("--kind", choices=("sft", "dpo"), required=True)
    parser.add_argument("--data", required=True)
    parser.add_argument("--strict", action="store_true", help="Treat quality warnings as failures")
    return parser.parse_args()


def message_list(value: Any, label: str, errors: list[str], warnings: list[str]) -> list[dict[str, str]]:
    if not isinstance(value, list) or not value:
        errors.append(f"{label} must be a non-empty message list")
        return []
    result: list[dict[str, str]] = []
    for index, message in enumerate(value):
        if not isinstance(message, dict):
            errors.append(f"{label}[{index}] must be an object")
            continue
        role = message.get("role")
        content = message.get("content")
        if role not in ALLOWED_ROLES:
            errors.append(f"{label}[{index}].role must be one of {sorted(ALLOWED_ROLES)}")
        if not isinstance(content, str) or not content.strip():
            errors.append(f"{label}[{index}].content must be non-empty text")
            continue
        if PLACEHOLDER_RE.search(content):
            warnings.append(f"{label}[{index}] contains a placeholder")
        if any(pattern.search(content) for pattern in SECRET_PATTERNS):
            warnings.append(f"{label}[{index}] looks like it contains a credential")
        result.append({"role": str(role), "content": content})
    return result


def validate_sft(record: Any, errors: list[str], warnings: list[str]) -> int:
    if not isinstance(record, dict):
        errors.append("record must be an object")
        return 0
    messages = message_list(record.get("messages"), "messages", errors, warnings)
    roles = [message["role"] for message in messages]
    if "user" not in roles or "assistant" not in roles:
        errors.append("messages must include at least one user turn and one assistant turn")
    if messages and messages[-1]["role"] != "assistant":
        warnings.append("the final turn is not an assistant answer")
    return sum(len(message["content"]) for message in messages)


def validate_dpo(record: Any, errors: list[str], warnings: list[str]) -> int:
    if not isinstance(record, dict):
        errors.append("record must be an object")
        return 0
    prompt = message_list(record.get("prompt"), "prompt", errors, warnings)
    chosen = message_list(record.get("chosen"), "chosen", errors, warnings)
    rejected = message_list(record.get("rejected"), "rejected", errors, warnings)
    if prompt and prompt[-1]["role"] != "user":
        warnings.append("prompt should normally end with a user turn")
    for label, answer in (("chosen", chosen), ("rejected", rejected)):
        if answer and answer[-1]["role"] != "assistant":
            errors.append(f"{label} must end with an assistant answer")
    if chosen and rejected and chosen[-1]["content"].strip() == rejected[-1]["content"].strip():
        errors.append("chosen and rejected answers must differ")
    return sum(len(message["content"]) for group in (prompt, chosen, rejected) for message in group)


def main() -> int:
    args = parse_args()
    path = Path(args.data)
    if not path.is_file():
        print(f"error: dataset does not exist: {path}", file=sys.stderr)
        return 2

    counts: Counter[str] = Counter()
    chars = 0
    failures: list[str] = []
    warnings: list[str] = []
    validator = validate_sft if args.kind == "sft" else validate_dpo
    with path.open("r", encoding="utf-8") as file:
        for line_number, raw_line in enumerate(file, start=1):
            if not raw_line.strip():
                failures.append(f"line {line_number}: blank lines are not allowed")
                continue
            try:
                record = json.loads(raw_line)
            except json.JSONDecodeError as error:
                failures.append(f"line {line_number}: invalid JSON ({error.msg})")
                continue
            record_errors: list[str] = []
            record_warnings: list[str] = []
            chars += validator(record, record_errors, record_warnings)
            counts["records"] += 1
            failures.extend(f"line {line_number}: {item}" for item in record_errors)
            warnings.extend(f"line {line_number}: {item}" for item in record_warnings)

    print(f"checked {counts['records']} {args.kind.upper()} records; total text characters: {chars}")
    for warning in warnings:
        print(f"warning: {warning}", file=sys.stderr)
    for failure in failures:
        print(f"error: {failure}", file=sys.stderr)
    if failures or (args.strict and warnings):
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
