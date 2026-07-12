# ledger-settlement 语义说明

- **事实分离**：订单成功 ≠ 支付成功 ≠ 账务已入账（`ledger_transactions`
  存在）≠ 已结算（`settlements`）≠ 渠道对账匹配。支付成功但无 posted
  交易 = 未入账（异步最终一致性）。
- **grain**：`ledger_transactions` 一行一账务事务；`ledger_entries`
  一行一借贷分录（1 : N，事务内分录按币种归零）。
- **流水 vs 快照**：`ledger_entries` 是流水；`balance_snapshots` 是
  月末时点余额（由截止 posted 分录重算必须一致）。
- **单位**：`amount_minor` + `currency_code` 整数最小单位；分录金额恒
  为正，方向由 `direction ∈ debit | credit` 表达。
- **规则版本**：`fee_rule_versions` 按 `[valid_from, valid_to)` 生效
  （2025-04-01 换版）；历史手续费必须用交易时点版本重算。
- **对账**：`reconciliation_results.status ∈ matched | amount_mismatch
  | missing_external | missing_internal`；`difference_minor` 为内部减
  外部。
