# 业务用例目录（每模块 Top 32）

> **v0.1.10 目标交付物（草案）**。`v0.1.9` 已发布并交付 21 个 Eval
> 任务；本目录扩展为四模块各 32 条参考用例，验收见
> [Roadmap](../../docs/roadmap.md) `v0.1.10`。

按真实电商、支付中台、账务结算与直播平台运营/分析场景的**使用频率从高到低**
排列。排名 1 为最常见日报或值班查询，排名 32 为低频但高风险的边界核对。

本目录不等同于 Eval 任务全集：`tasks/tasks.yaml` 沿用 `v0.1.9` 的 21 项
（见「Eval 任务」列）；`v0.1.10` 验收时本目录四模块各 32 条齐备，并与各
模块 `metadata/` 语义一致。

语义陷阱与 grain/时间/单位说明见各模块 `metadata/README.md`。

---

## commerce-core（通用商业）

| 排名 | 用例 | 典型问题 | 主要语义陷阱 | Eval 任务 |
| --- | --- | --- | --- | --- |
| 1 | 日订单量与 GMV | 昨天各应用完成了多少订单、总收入多少（美元）？ | 完成状态、`minor_units`、`created_at` vs `completed_at` | — |
| 2 | 活跃客户规模 | 当前有多少活跃客户？中国（CN）有多少？已删除的不算。 | `soft_delete`、`users` vs `customers` | `commerce.active-customers-by-country` |
| 3 | 按月已完成收入 | 应用 1（novashop）2025 年 6 月已完成订单总收入（美元）？ | `minor_units`、时间字段、订单状态 | `commerce.completed-revenue-app1-june` |
| 4 | 订单与明细计数 | 2025 年 3 月创建了多少订单、共多少订单行？ | `order` vs `order_item` grain | `commerce.orders-vs-items-grain` |
| 5 | 按国家客户分布 | 各国家活跃客户数及占比？ | `soft_delete`、国家归属 | `commerce.active-customers-by-country` |
| 6 | 订单完成率 | 本月创建订单中，已完成/已取消/仍进行中的各多少？ | 状态枚举、`created_at` 窗口 | — |
| 7 | 按应用收入拆分 | 各应用上月 GMV（换算为美元）？ | 多租户、`currency_code`、完成状态 | — |
| 8 | 客单价（AOV） | 上月已完成订单的平均订单金额？ | 订单 grain、金额单位 | — |
| 9 | 商品销量排行 | 上月销量最高的 5 个商品各卖了多少件？ | `order_item` grain、完成时间 | — |
| 10 | 历史标价查询 | 商品 2 在 2025-02-01 与 2025-04-01 的单价（美元）？ | `product_price_versions`、不得用现价 | `commerce.historical-price` |
| 11 | 多币种未换算汇总陷阱 | 各币种原始 `amount_minor` 直接相加是否正确？ | 币种不可混加、需按 `minor_unit_scale` | — |
| 12 | 登录用户 vs 交易客户 | 平台 `users` 与 `customers` 数量差多少？为何不等？ | 相似实体、`users` ≠ 交易主体 | — |
| 13 | 逻辑删除客户占比 | 历史客户中有多少已被逻辑删除？ | `deleted_at` | — |
| 14 | 确认未完成的滞留单 | 已确认但尚未完成/取消的订单有多少？ | `confirmed_at` vs `completed_at` | — |
| 15 | 按区域销售分布 | 各 `region`（amer/apac/emea）已完成收入？ | 国家→区域映射 | — |
| 16 | 大订单识别 | 单笔订单行数超过 5 行的订单有哪些？ | `order` vs `order_item` | — |
| 17 | 组织层级汇总 | 组织 1 旗下所有应用合计收入？ | `organization` → `application` | — |
| 18 | 成交价与标价一致性 | 订单行 `unit_price_minor` 是否与下单日有效价格版本一致？ | 历史价固化、禁止现价重算 | — |
| 19 | KRW 零小数位展示 | 韩元订单金额从 `minor` 换算为展示单位时小数位如何处理？ | KRW `minor_unit_scale=0` | — |
| 20 | 跨月订单趋势 | 2025 年 Q1 各月订单量趋势？ | 时间窗口、`created_at` 默认 | — |
| 21 | 取消订单金额 | 本月取消订单的原价合计？ | `cancelled_at`、状态 | — |
| 22 | 新客首单 | 首次下单客户数（按客户 grain）？ | 客户 grain、时间 | — |
| 23 | 回头客占比 | 下过 2 次及以上订单的客户占比？ | 客户 grain、去重 | — |
| 24 | 业务单元拆分 | 各 `business_unit` 收入贡献？ | 组织内层级 | — |
| 25 | 空国家客户 | 未填 `country_code` 的活跃客户有多少？ | NULL 语义 | — |
| 26 | 商品目录活跃 SKU | 仍有成交记录的商品数？ | 商品与订单行关联 | — |
| 27 | 跨应用客户去重 | 在多个应用都下过单的客户数？ | 客户 grain、跨应用 | — |
| 28 | 订单金额与行合计 | 订单头金额是否等于各行 `quantity * unit_price` 之和？ | 头行一致性 | — |
| 29 | 最高价成交商品 | 单笔成交价最高的商品是哪一笔？ | `order_item` grain | — |
| 30 | 季度同比 | 2025 Q2 vs Q1 订单量变化？ | 时间边界、完成 vs 创建 | — |
| 31 | 待支付订单量 | 已确认但支付意图尚未成功的订单数？ | 订单 ≠ 支付成功 | — |
| 32 | 业务单元零收入 | 哪些 `business_unit` 本季无已完成订单？ | 反连接、空集 | — |

---

## payment-orchestration（支付编排）

| 排名 | 用例 | 典型问题 | 主要语义陷阱 | Eval 任务 |
| --- | --- | --- | --- | --- |
| 1 | 渠道支付成功率 | 昨天各渠道最终支付成功率？ | `intent` grain、内部 `status` | `payments.final-success-by-channel` |
| 2 | 日支付量与金额 | 昨日创建了多少支付意图、成功金额多少？ | `intent` grain、`minor_units` | — |
| 3 | 意图数 vs 尝试数 | 2025 年 5 月支付意图与支付尝试各多少？为何不同？ | `intent` vs `attempt` grain | `payments.intent-vs-attempt-counts` |
| 4 | 首次 vs 最终成功 | 2025 年 4 月创建的意图中，首次尝试成功与最终成功各多少？ | 首次结果 ≠ 最终结果 | `payments.first-vs-final-success-april` |
| 5 | 失败支付监控 | 当前处于 processing 的支付意图有多少？ | 异步状态 | — |
| 6 | 渠道切换恢复 | 首次尝试失败、换渠道后最终成功的尝试有多少？ | 重试序列、`attempt_number` | `payments.channel-switch-recovered` |
| 7 | 按渠道最终成功分布 | 各渠道承接了多少最终成功的支付意图？stripe 多少？ | 成功尝试的渠道归因 | `payments.final-success-by-channel` |
| 8 | 退款量监控 | 本月全额退款与部分退款各多少笔？ | `full` vs `partial` | `payments.partial-refunds` |
| 9 | 部分退款识别 | 有多少退款是部分退款？ | 部分金额 < 意图金额 | `payments.partial-refunds` |
| 10 | 授权未扣款 | 已授权但未 capture 的支付有多少？ | 授权与扣款分离 | — |
| 11 | 争议与拒付 | 处于 dispute/chargeback 状态的支付有多少？ | 状态映射 | — |
| 12 | 重复回调统计 | 重复投递的回调行有多少（总行 − 去重 `external_event_id`）？ | 事件去重 | `payments.duplicate-callbacks` |
| 13 | 未处理回调 | 已收到但 `processed_at` 为空的回调有多少？ | `received` vs `processed` | `payments.unprocessed-callbacks` |
| 14 | 回调乱序影响 | 同一 `external_event_id` 最早与最晚 `received_at` 差多久？ | 乱序投递 | — |
| 15 | 路由规则时点 | 2025-05-01 的支付尝试适用哪条路由规则版本？ | `routing_rule_versions` 有效期 | `payments.routing-rule-on-may-1` |
| 16 | 路由降级原因 | 因路由失败切换渠道的尝试有多少？ | `routing_decisions` | — |
| 17 | 渠道原始状态分布 | 各 `channel_status` 原始状态计数？ | 内部状态 vs 渠道状态 | — |
| 18 | 平均尝试次数 | 成功支付的平均尝试次数？ | `attempt` grain、去重 | — |
| 19 | 首次失败率 by 渠道 | 各渠道首次尝试失败率？ | 首次尝试 grain | — |
| 20 | 支付耗时 | 从意图创建到 `completed_at` 的 P95 耗时？ | 时间字段选择 | — |
| 21 | 手续费核对 | 成功意图上记录的 `fee_minor` 合计？ | 时点手续费版本 | — |
| 22 | 退款金额汇总 | 本月退款总金额（minor）？ | 退款 grain | — |
| 23 | 幂等键冲突 | 相同幂等键的重复尝试有多少？ | 幂等、去重 | — |
| 24 | 事件与交易数差异 | 回调事件数 vs 成功支付意图数差多少？ | 事件 grain ≠ 交易 grain | `payments.duplicate-callbacks` |
| 25 | 渠道账户余额预警 | 各 `channel_account` 今日成功金额？ | 渠道账户维度 | — |
| 26 | 支付方式分布 | 各 `payment_method` 使用占比？ | 方法 vs 渠道 | — |
| 27 | 跨日完成支付 | 昨日创建、今日才完成的意图有多少？ | `created_at` vs `completed_at` | — |
| 28 | 退款尝试失败 | `refund_attempt` 失败次数？ | 退款尝试 grain | — |
| 29 | 渠道不可用时段 | 某渠道 disabled 期间仍成功的异常？ | 渠道 `status` | — |
| 30 | 大额支付监控 | 超过阈值的单笔成功支付？ | 金额单位 | — |
| 31 | 回调处理延迟 | `received_at` 到 `processed_at` 超 1 小时的回调？ | 三段时间 | — |
| 32 | 路由版本切换影响 | 路由规则换版日前后成功率变化？ | 规则版本、时间 | — |

---

## ledger-settlement（账务结算）

| 排名 | 用例 | 典型问题 | 主要语义陷阱 | Eval 任务 |
| --- | --- | --- | --- | --- |
| 1 | 账户当前余额 | 商户应付账户 3 当前余额（minor）？ | 快照 vs 流水 | `ledger.balance-snapshot-march` |
| 2 | 支付成功未入账 | 支付已成功但尚无 posted 账务事务的意图有多少？ | 支付事实 ≠ 账务事实 | `ledger.succeeded-unposted` |
| 3 | 日入账量 | 昨日 posted 的账务事务有多少？ | `ledger_transactions` grain | — |
| 4 | 对账异常分类 | 对账结果中 amount_mismatch / missing_external / missing_internal 各多少？ | 内外部记录 | `ledger.reconciliation-exceptions` |
| 5 | 结算净额 | 组织 1 结算净额（扣费与退款后，美元）？ | 毛额 vs 净额、`minor_units` | `ledger.settlement-net-org1` |
| 6 | 渠道手续费汇总 | 2025 年 5 月成功支付的手续费合计（minor）？ | 时点 `fee_rule_versions` | `ledger.fees-may` |
| 7 | 月末余额快照 | 账户 3 在 2025-03-31 快照余额？不得从流水重算。 | 快照时点 | `ledger.balance-snapshot-march` |
| 8 | 流水重算 vs 快照 | 截止 3 月末流水汇总的余额是否与快照一致？ | 流水与快照并存 | — |
| 9 | 待结算余额 | 各账户 `pending_balances` 合计？ | 待结算 vs 可用 | — |
| 10 | 分录借贷平衡 | 某账务事务内各币种借贷是否归零？ | `debit`/`credit`、多币种 | — |
| 11 | 多资产余额 | 法币账户与虚拟资产账户余额分别多少？ | 多资产 | — |
| 12 | 结算 vs 入账 | 已结算但未入账（或反之）的异常有多少？ | 结算 ≠ 入账 | — |
| 13 | 出款失败 | `payout_attempt` 失败次数？ | 出款尝试 grain | — |
| 14 | 外部账单缺失 | 内部有记录、外部账单缺失的对账条数？ | `missing_external` | `ledger.reconciliation-exceptions` |
| 15 | 内部记录缺失 | 外部有账单、内部缺失的条数？ | `missing_internal` | `ledger.reconciliation-exceptions` |
| 16 | 金额不一致明细 | `difference_minor` 最大的对账异常？ | 内外金额差 | — |
| 17 | 手续费规则换版影响 | 2025-04-01 手续费规则换版前后费用差异？ | `fee_rule_versions` | — |
| 18 | 冲正与补账 | 修正分录（冲正）笔数？ | 不可变流水 + 修正 | — |
| 19 | 跨币种结算 | 原始币种与结算币种不同的结算单有多少？ | 原币 vs 结算币 | — |
| 20 | 汇率时点 | 某笔结算使用的汇率及生效时间？ | 汇率时间 | — |
| 21 | 平台收入账户 | 平台收入账户本月贷方合计？ | 账户类型 | — |
| 22 | 商户待结算 | 商户待结算账户余额合计？ | 待结算状态 | — |
| 23 | 结算周期汇总 | 各 `settlement` 周期出款金额？ | 结算 grain | — |
| 24 | 外部账单导入量 | 本期 `external_statement` 行数？ | 外部账单 grain | — |
| 25 | 一对多对账匹配 | 多条内部记录匹配一条外部记录的案例？ | 对账匹配 grain | — |
| 26 | 冻结余额 | 冻结余额合计及占比？ | 可用 vs 冻结 | — |
| 27 | 手续费账户流向 | 渠道手续费账户本月变动？ | 费用分录 | — |
| 28 | 未匹配结算项 | `settlement_items` 中未关联支付的条数？ | 结算项关联 | — |
| 29 | 账务延迟监控 | 支付完成到入账超过 1 小时的笔数？ | 异步最终一致性 | `ledger.succeeded-unposted` |
| 30 | 虚拟资产分录 | star 资产的分录笔数与金额？ | 虚拟资产 | — |
| 31 | 日终对账完成率 | 本期对账 matched 占比？ | 对账状态 | — |
| 32 | 历史时点可用余额 | 2025-03-15 的可用余额（非当前）？ | 时点查询 | — |

---

## live-monetization（直播变现）

| 排名 | 用例 | 典型问题 | 主要语义陷阱 | Eval 任务 |
| --- | --- | --- | --- | --- |
| 1 | 日礼物收入 | 昨日 processed 礼物收入（star）？ | 事件状态、`stars` 单位 | — |
| 2 | 房间累计 vs 单场最高 | 房间 1 历史累计礼物收入与单场最高收入？ | `room` vs `session` grain | `live.room1-vs-best-session` |
| 3 | 主播收入排行 | 上月各主播 processed 礼物收入 TOP 10？ | 主播 grain、事件状态 | — |
| 4 | 已结算 vs 待结算 | 主播 1 已结算与仍待结算的分成（star）？ | 结算期间边界 | `live.creator1-settled-vs-pending` |
| 5 | 礼物历史价格 | Rose 礼物在 2025-02-01 与 2025-05-01 的 star 价格？ | `gift_price_versions` | `live.gift-price-versions` |
| 6 | 重复礼物事件 | `dedup_of` 非空的重复投递有多少？stars 是否为 0？ | 重复事件去重 | `live.duplicate-gift-events` |
| 7 | 场次收入分布 | 各 `live_session` 礼物收入？ | `session` grain | `live.room1-vs-best-session` |
| 8 | 公会分成占比 | 有公会的主播 vs 独立主播收入结构？ | `agency_id`、分成规则 | — |
| 9 | 扣款失败礼物 | `charge_failed` 礼物事件有多少？ | 事件 ≠ 扣款成功 | — |
| 10 | 冲正礼物 | `reversed` 状态礼物及冲正 star 数？ | 冲正 | — |
| 11 | 分成规则换版影响 | 2025-04-01 分成换版前后创作者分成差异？ | `revenue_share_rule_versions` | — |
| 12 | 待结算创作者 | 待结算 star 合计（按创作者）？ | 待结算 vs 已结算 | `live.creator1-settled-vs-pending` |
| 13 | 房间活跃场次 | 各房间本月开播场次？ | `room` → `session` | — |
| 14 | 礼物销量 | 各礼物定义送出次数（去重后）？ | 礼物 grain | — |
| 15 | 虚拟钱包余额 | 用户虚拟钱包 star 余额？ | 虚拟资产 | — |
| 16 | 平台抽成 | 平台从礼物收入中的抽成合计？ | `revenue_splits` | — |
| 17 | 公会结算 | 各公会已结算金额？ | 公会结算 grain | — |
| 18 | 单场峰值礼物 | 单场直播中最高礼物笔数？ | `session` grain | — |
| 19 | 跨房间主播 | 同主播多房间收入汇总？ | 主播跨房间 | — |
| 20 | 活动期礼物价 | 活动期间礼物价格与常规定价差异？ | 价格版本 | — |
| 21 | 消费订单关联 | `consumption_order` 与 `gift_event` 非 1:1 案例？ | 事件 vs 交易 | — |
| 22 | 风控冻结礼物 | 冻结状态的礼物事件？ | 状态语义 | — |
| 23 | 创作者当日收入 | 主播今日所有场次合计收入？ | 日 grain vs 场次 | — |
| 24 | 礼物均价 | 平均每笔 processed 礼物的 star 数？ | 事件 grain | — |
| 25 | 独立主播分成 | 无 `agency_id` 时 `agency_stars` 是否为 0？ | 分成规则 | — |
| 26 | 房间零收入场次 | 开播但零礼物收入的场次？ | 空集、场次 grain | — |
| 27 | 重复事件收入影响 | 若不去重，礼物收入会被高估多少？ | 去重前后对比 | `live.duplicate-gift-events` |
| 28 | 跨地区礼物价 | 不同地区有效礼物价格？ | 地区维度价格版本 | — |
| 29 | 公会待结算 | 公会侧待结算 star？ | 公会结算状态 | — |
| 30 | 礼物定义上下架 | 已下架礼物是否仍有历史成交？ | 礼物生命周期 | — |
| 31 | 场次时长与收入 | 场次时长与礼物收入相关性？ | 时间 + 收入 | — |
| 32 | 结算周期边界 | 结算截止日前后 1 天的礼物归属？ | 结算期间边界 | `live.creator1-settled-vs-pending` |

---

## 与 Eval 任务的关系

| 模块 | Top 32 用例 | 已有 Eval 任务 | 覆盖率 |
| --- | --- | --- | --- |
| commerce-core | 32 | 4 | 4/32 |
| payment-orchestration | 32 | 8 | 8/32 |
| ledger-settlement | 32 | 5 | 5/32 |
| live-monetization | 32 | 4 | 4/32 |
| **合计** | **128** | **21**（v0.1.9 已交付） | **21/128** |

`v0.1.9` 的 21 个 Eval 任务覆盖各模块高频区（排名 1–15）中的语义陷阱
子集。`v0.1.10` 交付本目录全文；未覆盖项作为后续 Eval 扩展与 dogfooding
问题清单的候选来源。
