from __future__ import annotations

import os
from pathlib import Path

import torch
from datasets import load_dataset
from peft import prepare_model_for_kbit_training
from transformers import AutoModelForCausalLM, AutoTokenizer, BitsAndBytesConfig


DEFAULT_MODEL = "deepseek-ai/DeepSeek-R1-Distill-Qwen-1.5B"


def load_jsonl(path: str):
    source = Path(path)
    if not source.is_file():
        raise FileNotFoundError(f"dataset does not exist: {source}")
    return load_dataset("json", data_files=str(source), split="train")


def load_tokenizer(model_name: str):
    tokenizer = AutoTokenizer.from_pretrained(
        model_name,
        trust_remote_code=False,
        cache_dir=os.getenv("HF_HOME"),
    )
    if tokenizer.pad_token is None:
        tokenizer.pad_token = tokenizer.eos_token
    tokenizer.padding_side = "right"
    return tokenizer


def load_model(model_name: str, use_4bit: bool):
    if use_4bit and not torch.cuda.is_available():
        raise RuntimeError("--use-4bit requires a CUDA GPU and bitsandbytes")

    quantization_config = None
    device_map = None
    dtype = torch.float32
    if torch.cuda.is_available():
        dtype = torch.bfloat16 if torch.cuda.is_bf16_supported() else torch.float16
    if use_4bit:
        quantization_config = BitsAndBytesConfig(
            load_in_4bit=True,
            bnb_4bit_quant_type="nf4",
            bnb_4bit_compute_dtype=dtype,
            bnb_4bit_use_double_quant=True,
        )
        device_map = "auto"

    model = AutoModelForCausalLM.from_pretrained(
        model_name,
        trust_remote_code=False,
        cache_dir=os.getenv("HF_HOME"),
        torch_dtype=dtype,
        quantization_config=quantization_config,
        device_map=device_map,
        use_cache=False,
    )
    if use_4bit:
        # Required preparation for stable QLoRA training: it freezes the base
        # model and casts the layers that must remain in full precision.
        model = prepare_model_for_kbit_training(model)
    return model


def bf16_available() -> bool:
    return torch.cuda.is_available() and torch.cuda.is_bf16_supported()


def fp16_available() -> bool:
    return torch.cuda.is_available() and not torch.cuda.is_bf16_supported()
