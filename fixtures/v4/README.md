# fixtures/v4 — 真实业务负载模型

[真实业务负载模型设计文档](../../docs/design/business-workload-model.md)的
参考实现：四个业务模块的确定性 fixture、可运行组合 profile、自然语言任务
与预期结果，服务 Eval 真实负载轨（`make eval-workload`）。

## 目录

```text
generator/                    确定性生成器（Go 包 + workloadgen CLI）
commerce-core/                通用商业基础模型（行业中立）
payment-orchestration/        支付编排模型（行业中立）
ledger-settlement/            账务、结算与对账模型（行业中立）
verticals/live-monetization/  直播与创作者经济行业扩展
profiles/default.yaml         可直接运行的组合 profile（四模块全量实体）
tasks/                        v4 任务集（跨模块聚合）
```

每个模块目录含 `schema/`（各方言 DDL）、`seed/`（各方言种子数据）、
`policies/`（Entity/RBAC/mask/relationship 配置示例）、`expected/`
（任务预期结果 CSV）、`metadata/`（grain、时间、单位与状态说明）。

## 确定性

所有值是 `(seed, 表, 行号)` 的纯函数（默认 seed=1、scale=1、全部异常
模式开启）。`schema/`、`seed/`、`expected/`、`policies/`、`profiles/`
均由生成器输出并纳入版本控制，单元测试锁定磁盘文件与生成器一致；改动
生成器后运行 `make fixtures-v4` 再生成。预期结果与种子数据同源计算，
不存在手工维护的答案。

生成器约束（设计文档第七节）：确定性 seed、可重复生成、`Scale` 控制
数据规模、`Anomalies` 逐项注入异常模式、跨 Provider（PostgreSQL 与
MySQL/OceanBase 方言）渲染同一数据。

## 复杂度清单核对（设计文档第十节第 7 条，需 ≥8 项）

| 复杂度 | 位置 |
| --- | --- |
| 相似实体 | `users`（平台账号）vs `customers`（交易客户） |
| 多条可选关联路径 | order → intent → attempt → callback；gift_event → split → settlement |
| 多种时间字段 | orders 四个生命周期时间；callbacks 的 occurred/received/processed_at |
| 流水与快照并存 | `ledger_entries`（流水）vs `balance_snapshots`（快照） |
| 不同 grain | orders vs order_items；intents vs attempts；rooms vs sessions |
| 多币种或多资产 | USD/CNY/KRW（KRW 无小数位）+ 虚拟资产 star |
| 枚举和状态映射 | 内部 status vs 渠道原始 channel_status |
| 规则版本 | 商品价格、手续费、路由规则、分成规则、礼物价格均带 valid_from/valid_to |
| 逻辑删除 | customers.deleted_at |
| 异步最终一致性 | 支付成功但账本未入账（unposted） |
| 重复事件 | 重复回调（external_event_id）、重复礼物事件（dedup_of） |
| 内外部对账差异 | reconciliation_results 四种状态 |

12/12 项全覆盖。

## 运行组合 profile

```sh
# 任一 PostgreSQL 实例：
psql "$DSN" -f commerce-core/schema/postgres.sql -f commerce-core/seed/postgres.sql \
  -f payment-orchestration/schema/postgres.sql -f payment-orchestration/seed/postgres.sql \
  -f ledger-settlement/schema/postgres.sql -f ledger-settlement/seed/postgres.sql \
  -f verticals/live-monetization/schema/postgres.sql -f verticals/live-monetization/seed/postgres.sql
DATABASE_DSN="$DSN" sql-mcp-server -config fixtures/v4/profiles/default.yaml
```

`make eval-workload` 自动完成以上装配（testcontainers）。

## 扩展新行业模块

新增行业 profile 复用三个行业中立模块，只加行业特有实体（参照
`verticals/live-monetization/`）：在 `generator/` 增加模块 builder 与
任务预期计算，运行 `make fixtures-v4`，drift 测试保证一致性。不得复制
支付、账务和商业核心实体。
