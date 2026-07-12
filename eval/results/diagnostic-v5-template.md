# 诊断轨结论模板：&lt;模型&gt;，&lt;日期&gt;（任务集 v5）

运行：`make eval-diagnostic`（或 `EVAL_TASKS_DIR=... go run ./eval/runner -track diagnostic`）

## 运行参数

- 任务集：v5（guided 21 + 扩展 27 ≈ 48 单轮；见 `fixtures/v4/tasks-v5/`）
- 模型：&lt;EVAL_MODEL&gt;
- runs=&lt;n&gt;；并行度 &lt;EVAL_PARALLEL&gt;

## 汇总

- 通过率：&lt;x/y&gt;（各轮）
- 行为准确度（clarify/deny/qualify/unsupported）：&lt;%&gt;
- Oracle 归因率：&lt;%&gt;
- 未归因失败：&lt;n&gt;
- Product-fixable 失败：&lt;n&gt;；model-only：&lt;n&gt;
- first-call success / discovery calls / prompt tokens：&lt;...&gt;

## 按维度覆盖

见 `fixtures/v4/coverage/matrix.md`（`make eval-coverage` 生成）。

## Dormant 方向 go/no-go（§8）

| 方向 | 结论 | 证据 |
| --- | --- | --- |
| Semantic Metadata | go / no-go / 继续观察 | grain/时间/状态/单位失败是否集中 |
| Catalog Discovery | go / no-go / 继续观察 | medium profile token/选表曲线 |
| Governed Query Expressiveness | go / no-go / 继续观察 | ir_expressibility 归因 |
| Tool Contract | go / no-go / 继续观察 | 拒绝不可操作、边界模糊 |
| Evidence Envelope | go / no-go / 继续观察 | 调查场景无法解释来源 |

P1 medium catalog 或调查场景未正式运行时，对依赖该证据的方向标记
“证据不足”，不作 go/no-go。

## 已知限制

- 单模型/单 prompt 模板不外推；
- Natural 任务区分度需更弱模型或 dogfooding 验证；
- P1 多轮场景与 medium catalog 待 runner 完全接入。
