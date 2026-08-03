# GopherAI 模型训练

本目录用于微调项目默认模型对应的原始权重：
`deepseek-ai/DeepSeek-R1-Distill-Qwen-1.5B`。项目运行时使用的
Ollama/GGUF 模型只用于推理；训练必须从 Hugging Face Transformers
权重开始。

## 环境

建议使用 Linux 或 WSL2、NVIDIA CUDA GPU 和 Python 3.11。QLoRA 的实际
显存取决于序列长度、batch size 和软件版本，开始训练前至少保留充足的
磁盘空间用于模型缓存、checkpoint 与合并后的权重。

```bash
cd /mnt/c/Users/Haruka/GolandProjects/goAI
bash training/scripts/bootstrap_cuda.sh
```

该脚本会创建 `training/.venv`、安装 GPU 版 PyTorch 并检查 CUDA 是否真正
可用。默认使用 CUDA 12.4 的官方 PyTorch wheel；如果云端镜像的驱动要求其他
版本，请在执行前设置 `TORCH_INDEX_URL`。如模型仓库要求鉴权，请先使用
`huggingface-cli login`，不要将 token 写入仓库。

云端训练的完整顺序见下方；每次执行前先运行数据校验，避免花费 GPU 时间后才
发现 JSONL 格式或疑似凭据问题。

## 数据格式

SFT 数据每行是一个 `messages` 对话，参考
`training/data/sft.gopherai.seed.example.jsonl`。DPO 数据每行包含 `prompt`、
`chosen` 和 `rejected`，参考 `training/data/dpo.gopherai.seed.example.jsonl`。
真实数据文件会被 Git 忽略。数据标准、校验和可复现切分方式见
[`DATA_GUIDE.md`](DATA_GUIDE.md)；样例只用于验证格式，不能产生有用模型。

## SFT（QLoRA）

```bash
bash training/scripts/run_sft.sh training/data/sft.train.jsonl \
  --eval-data training/data/sft.eval.jsonl \
  --epochs 2
```

没有 bitsandbytes/CUDA 时可传 `--no-use-4bit`，但全精度训练的资源需求会
显著增加。先用少量数据和较短 `--max-length` 做冒烟验证，再开始完整训练。

## DPO

DPO 应在已经完成 SFT 的模型上进行。当前脚本可以直接对基座创建 DPO
adapter；如果要从 SFT adapter 继续训练，应先合并 SFT adapter，再把合并
目录传给 `--model`：

```bash
training/.venv/bin/python training/merge_adapter.py \
  --adapter training/outputs/sft-adapter \
  --output training/outputs/sft-merged

export DPO_MODEL=training/outputs/sft-merged
bash training/scripts/run_dpo.sh training/data/dpo.train.jsonl \
  --eval-data training/data/dpo.eval.jsonl \
  --epochs 1
```

## 导入 Ollama

先合并最终 adapter：

```bash
training/.venv/bin/python training/merge_adapter.py \
  --adapter training/outputs/dpo-adapter \
  --output training/outputs/final-merged
```

然后使用与当前模型架构兼容的 `llama.cpp/convert_hf_to_gguf.py` 转换并量化。
不同 llama.cpp 版本的量化命令可能变化，应以所安装版本的帮助信息为准。
把最终文件命名为 `training/model.gguf` 后执行：

```bash
ollama create gopherai-deepseek-r1:1.5b -f training/Modelfile.example
ollama run gopherai-deepseek-r1:1.5b
```

验证完成后，将 `config/config.local.toml` 中的 `ollamaConfig.modelName`
和 `ragModelConfig.chatModelName` 改为 `gopherai-deepseek-r1:1.5b`。不要直接
修改生产配置，且不要在完成回归评估前替换现有模型。

## 训练前检查

- 划分训练集、验证集和独立测试集，避免同一问题泄漏到多个集合。
- 清除密码、API Key、个人信息和无授权内容。
- SFT 先验证领域能力；只有可靠的成对偏好数据才使用 DPO。
- 比较基座与微调模型的任务正确率、拒答、安全性、延迟和输出格式。
- 保存数据版本、随机种子、依赖版本、参数和模型来源，确保可复现。
