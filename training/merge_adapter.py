from __future__ import annotations

import argparse

import torch
from peft import AutoPeftModelForCausalLM
from transformers import AutoTokenizer


def main():
    parser = argparse.ArgumentParser(description="Merge a trained PEFT adapter into its base model")
    parser.add_argument("--adapter", required=True)
    parser.add_argument("--output", default="training/outputs/merged-model")
    args = parser.parse_args()

    dtype = torch.bfloat16 if torch.cuda.is_available() else torch.float32
    model = AutoPeftModelForCausalLM.from_pretrained(
        args.adapter,
        torch_dtype=dtype,
        device_map="auto" if torch.cuda.is_available() else None,
        trust_remote_code=False,
    )
    tokenizer = AutoTokenizer.from_pretrained(args.adapter, trust_remote_code=False)
    model = model.merge_and_unload()
    model.save_pretrained(args.output, safe_serialization=True, max_shard_size="4GB")
    tokenizer.save_pretrained(args.output)


if __name__ == "__main__":
    main()
