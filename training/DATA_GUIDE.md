# 训练数据制作指南

`sft.gopherai.seed.example.jsonl` 和 `dpo.gopherai.seed.example.jsonl` 是
项目场景化的格式参考与最小种子集，不是可用于生产训练的完整数据集。
真实数据请复制到未被 Git 跟踪的 `training/data/sft.jsonl` 和
`training/data/dpo.jsonl`。

每条 SFT 样本应只训练一种可验证的行为：例如解释配置、诊断已给出的错误、
给出带边界条件的操作建议，或在信息不足时正确追问。答案要代表你希望模型
在生产中稳定输出的风格，而不是收集所有历史对话。

每条 DPO 样本应使用同一个 prompt，且 `chosen` 明显优于 `rejected`。优先
标注以下差异：事实正确性、是否遵守权限与安全边界、是否引用检索到的来源、
是否承认不确定性、输出是否符合项目接口约束。不要把纯粹措辞偏好当作唯一
标准。

提交云端前运行：

```bash
python3 training/validate_data.py --kind sft --data training/data/sft.jsonl --strict
python3 training/validate_data.py --kind dpo --data training/data/dpo.jsonl --strict
python3 training/split_jsonl.py --input training/data/sft.jsonl \
  --train-output training/data/sft.train.jsonl \
  --eval-output training/data/sft.eval.jsonl
```

验证集必须在训练前固定，且不能与训练集出现同一问题的改写版本。删除个人资料、
访问令牌、私有 URL、客户数据及未经授权转载的内容。验证器只能发现明显格式
问题和疑似凭据，不能替代人工质量审查。
