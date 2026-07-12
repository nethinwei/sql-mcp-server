# payment-orchestration 语义说明

- **grain**：`payment_intents` 一行一业务支付目标；`payment_attempts`
  一行一次渠道尝试（1 intent : N attempts）。意图成功率 ≠ 尝试成功率；
  首次尝试结果 ≠ 最终结果。
- **时间**：attempt `created_at` 是尝试时间；intent `completed_at` 是
  最终成功时间；回调有 `occurred_at`（渠道侧）、`received_at`（网关
  接收）、`processed_at`（内部处理，NULL = 收到未处理）三个时间。
- **状态**：内部标准状态（`status`）与渠道原始状态（`channel_status`，
  渠道各自词表）并存，聚合必须用内部状态。
- **路由**：每次尝试关联一条 `routing_decisions`，记录当时生效的
  `routing_rule_versions` 版本；历史查询不得关联当前规则。
- **回调**：`external_event_id` 相同的多行是同一事件的重复投递，事件数
  统计必须去重；乱序与延迟投递存在。
- **退款**：`kind ∈ full | partial`；partial 金额小于 intent 金额。
