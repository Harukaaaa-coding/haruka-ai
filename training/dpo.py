from __future__ import annotations

import argparse

from peft import LoraConfig
from trl import DPOConfig, DPOTrainer

from common import DEFAULT_MODEL, bf16_available, fp16_available, load_jsonl, load_model, load_tokenizer


def parse_args():
    parser = argparse.ArgumentParser(description="DPO training for GopherAI's local chat model")
    parser.add_argument("--model", default=DEFAULT_MODEL, help="Base model or a merged SFT model directory")
    parser.add_argument("--data", default="training/data/dpo.example.jsonl")
    parser.add_argument("--eval-data", help="Optional JSONL validation set in the same DPO format")
    parser.add_argument("--output", default="training/outputs/dpo-adapter")
    parser.add_argument("--epochs", type=float, default=1.0)
    parser.add_argument("--learning-rate", type=float, default=5e-6)
    parser.add_argument("--batch-size", type=int, default=1)
    parser.add_argument("--gradient-accumulation", type=int, default=8)
    parser.add_argument("--max-length", type=int, default=2048)
    parser.add_argument("--max-prompt-length", type=int, default=1024)
    parser.add_argument("--beta", type=float, default=0.1)
    parser.add_argument("--use-4bit", action=argparse.BooleanOptionalAction, default=True)
    return parser.parse_args()


def main():
    args = parse_args()
    tokenizer = load_tokenizer(args.model)
    model = load_model(args.model, args.use_4bit)
    dataset = load_jsonl(args.data)
    eval_dataset = load_jsonl(args.eval_data) if args.eval_data else None
    peft_config = LoraConfig(
        r=16,
        lora_alpha=32,
        lora_dropout=0.05,
        bias="none",
        task_type="CAUSAL_LM",
        target_modules=["q_proj", "k_proj", "v_proj", "o_proj", "gate_proj", "up_proj", "down_proj"],
    )
    train_config = DPOConfig(
        output_dir=args.output,
        num_train_epochs=args.epochs,
        learning_rate=args.learning_rate,
        per_device_train_batch_size=args.batch_size,
        per_device_eval_batch_size=args.batch_size,
        gradient_accumulation_steps=args.gradient_accumulation,
        max_length=args.max_length,
        max_prompt_length=args.max_prompt_length,
        beta=args.beta,
        logging_steps=5,
        save_strategy="epoch",
        eval_strategy="epoch" if eval_dataset is not None else "no",
        gradient_checkpointing=True,
        bf16=bf16_available(),
        fp16=fp16_available(),
        report_to="none",
    )
    trainer = DPOTrainer(
        model=model,
        ref_model=None,
        args=train_config,
        train_dataset=dataset,
        eval_dataset=eval_dataset,
        processing_class=tokenizer,
        peft_config=peft_config,
    )
    trainer.train()
    trainer.save_model(args.output)
    tokenizer.save_pretrained(args.output)


if __name__ == "__main__":
    main()
