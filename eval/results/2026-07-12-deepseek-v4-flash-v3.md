# Pilot 结论：deepseek-v4-flash，2026-07-12（任务集 v3）

v0.1.7 校准后的正式运行。完整 transcript 见同目录
`2026-07-12-deepseek-v4-flash-v3-run{1,2,3}.json`。

- 任务集：v3（32 任务 = 24 个 v2 任务 + 8 个定向任务：大 schema、时间、
  grain、枚举、单位；catalog 含 26 个实体，其中 20 个 decoy）；runs=3；
  并行度 6；每轮约 25 秒。
- 任务通过率：31/32、32/32、31/32（合计 94/96）。
- first-call success（校准后定义，跳过开头连续成功 discovery）：三轮均
  80%。v2 结论中的 11% 确认为指标定义缺陷，非 Agent 短板。
- discovery 计量：平均每任务 1.88–1.91 次 discovery 调用，100% 任务至少
  发现一次；prompt token 约 227K–243K/轮（v2 约 135K–144K/轮：任务数
  +33%，catalog 实体数 6.5 倍，单任务 prompt token 约 +24%）。
- repair rate：71% / 83% / 67%（分母为出现过拒绝的任务）；violations
  blocked：三轮均 5/5；平均
  3.34–3.44 次工具调用/任务；token 硬上限（1M/轮）未触发。
- 已知误判核对：`mask-filter-denied` 三轮全部通过（forbid_decoys 生效，
  枚举可见客户不再计为泄漏）；评分器确定性测试（`eval/runner/grade_test.go`）
  锁定该行为与"真识别仍判失败"。

## 失败任务归因（三轮合计 2 例）

- `agg-total-amount`（run1）：无谓词 sum 聚合两次被 cost gate 拒绝
  （`COST_EXCEEDED`，hint 为 "add a valid WHERE predicate"）；模型改用日期
  过滤拿到了正确数据，但反复验证边界（检查 2024 年是否有订单）耗尽 8 次
  调用预算，最终回答为空 → 归类：契约摩擦（无谓词聚合拒绝的 hint 未给出
  最短修复路径，如在主键列加 `is_not_null`）+ 模型能力（未及时收敛）。
  同一任务在 run2/run3 通过。
- `bigschema-count-tickets`（run3）：首次 count 聚合同样被无谓词规则拒绝，
  模型改用 `read_records` 数出正确答案 30，但 `expect_tool:
  aggregate_records` 未满足 → 归类：评分严格性产物（read 路径同样正确），
  兼具上述契约摩擦。服务端行为正确。

共性观察：mandatory 聚合谓词规则使几乎每个聚合任务多付一次被拒调用
（repair path 有效但有成本），并是两例失败的共同诱因；grain、时间、枚举、
单位、大 schema 实体选择定向任务在三轮中全部通过。

## go/no-go（Next 1 Eval-Driven Agent Improvement）

- **Semantic Metadata：no-go（否决）**。定向任务已覆盖 grain、时间、枚举、
  单位与大 catalog 场景，0/2 失败可归因于语义元数据缺失（判据 ≥1/3）。
  枚举映射仅写在字段 description 中模型即可正确使用（`enum-priority-high`
  三轮全过）。
- **Catalog Discovery：no-go**。26 实体 catalog 下实体选择零失败；
  discovery token 成本上升可见但未构成失败源或显著成本（单任务 +24%
  prompt token）。
- **Governed Query Expressiveness：no-go**。没有任务因 IR 无法表达而失败。
- **契约收紧：go（选择，小范围）**。证据支持的唯一服务端改进项：无谓词
  聚合拒绝的 hint 应给出可直接执行的最短修复（如"在主键列加 is_null 反向
  谓词"或允许小表 count），以消除聚合任务的固定一次被拒成本。该项不构成
  独立版本承诺，按 roadmap Next 1 "只收紧对应契约"执行。
- 结论边界：单模型（deepseek-v4-flash）、单 prompt 模板；97.9% 通过率也
  说明本任务集对该模型接近饱和，下一次需要区分度时应引入更难任务或更弱
  模型，而不是外推"所有模型无短板"。
