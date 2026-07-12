# Medium catalog scale profile (P1 draft)

P1 候选目标：100–150 entities，模拟 raw/clean/serving 分层、
history/snapshot/archive 表、legacy 命名与相似字段名，用于测量 Catalog
Discovery 成本曲线 C(n)/D(n)/P(n)。

当前状态：**草案占位**。默认 Eval 仍使用 `profiles/default.yaml`（45
entities）。medium profile 实现后通过 `EVAL_CONFIG` 切换。

规划实体分层：

- serving：与 default profile 对齐的核心商业/支付/账务实体；
- clean：规范化中间表（去重、标准化状态）；
- raw：渠道原始回调、外部账单、审计日志；
- archive：历史模型 v1 表与只读快照。

测量指标见 [Diagnostic Evaluation 设计文档](../../../../docs/design/diagnostic-evaluation.md)。
