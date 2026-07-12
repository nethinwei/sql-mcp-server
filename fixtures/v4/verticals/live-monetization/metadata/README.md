# live-monetization 语义说明

- **grain**：`live_rooms` 长期房间；`live_sessions` 单次开播场次
  （1 : N）。房间累计收入 ≠ 单场收入 ≠ 主播当日多场收入。
- **资产**：礼物计价用虚拟资产 star（整数），不是法币；法币在
  ledger-settlement 侧。
- **事件 vs 交易**：`gift_events` 非 1:1 对应扣款——`charge_failed`
  未扣款、`reversed` 已扣后冲正、`dedup_of` 非 NULL 是重复投递（stars
  为 0，不得重复计数）。
- **价格与分成版本**：`gift_price_versions` 与
  `revenue_share_rule_versions` 按 `[valid_from, valid_to)` 生效
  （2025-04-01 分成换版：创作者 50% → 55%）；历史收入必须按事件时点
  版本计算。
- **分成**：`revenue_splits` 只对 processed 事件生成；独立主播
  （无 agency_id）agency_stars 为 0。
- **结算状态**：`creator_settlements` 只含已结算期间（2025-06-01 前）；
  之后的收入是待结算，不在该表。
