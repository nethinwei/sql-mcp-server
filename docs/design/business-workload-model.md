# 真实业务负载模型（Business Workload Reference Model）

本文件是 `v0.1.9`（Real Business Workload Model）范围与验收的唯一事实源，
由[主路线图](../roadmap.md)引用。

v0.1.9 新增一条独立于现有确定性回归评测（v3 冻结基线）的真实业务负载轨，
用于验证 sql-mcp-server 在复杂业务 Schema、异步状态、跨领域关联和资金
治理场景下的实际适用性。

本版本不以增加 SQL 功能或扩大 Provider 数量为主要目标，而是建设一组
可复现、可扩展、可供社区共同改进的业务 reference model、测试数据和
自然语言任务。评测结果将作为 Semantic Metadata、Catalog Discovery、
IR Expressiveness、Provider Capability 和 Control Plane 等后续方向的
进入依据。

## 一、模型组织原则

真实业务模型采用"通用核心模块 + 行业扩展模块"的组合方式，不建设一套
仅适用于直播业务的封闭 Schema。

首期模型分为以下四个业务模块：

```text
Commerce Core
    通用商业实体、租户、客户、商品和业务订单

Payment Orchestration
    支付意图、支付尝试、渠道路由、退款和异步回调

Ledger & Settlement
    账户、账务分录、余额、费用、结算和对账

Live Monetization
    直播、虚拟资产、礼物消费、主播与公会分成
```

默认 reference profile 为：

```text
commerce-core
+ payment-orchestration
+ ledger-settlement
+ live-monetization
```

其中前三个模块必须保持行业中立，可被电商、游戏、SaaS、出海应用、创作者
经济和聚合支付系统复用；`live-monetization` 是首个行业扩展模块。

## 二、Commerce Core：通用商业基础模型

该模块提供跨行业共用的租户、客户、商品和业务订单语义。

建议包含以下核心实体：

```text
organizations
business_units
applications

users
customers

products
product_price_versions

orders
order_items

countries
regions
currencies
```

### 核心语义

1. `organization` 表示系统中的租户或业务主体。
2. `application` 表示同一组织下独立运营的产品、站点或应用。
3. `customer` 表示发生交易的业务客户，不与系统登录用户强制等同。
4. `order` 表示业务交易意图，不代表支付已经成功。
5. `order_item` 表示订单中的独立商品或服务明细。
6. 商品价格必须支持历史版本，历史订单不得通过关联当前价格重算。

### 必须覆盖的复杂度

- 多租户数据隔离；
- 组织、应用和客户之间的多层归属关系；
- 订单与订单项的不同 grain；
- 商品当前价格与历史成交价格的区别；
- 逻辑删除；
- 多国家、多区域和多币种；
- 多种时间字段，例如创建、确认、取消和完成时间。

## 三、Payment Orchestration：支付编排模型

该模块用于模拟支付中台或 Payment Orchestration Platform，将不同支付
渠道统一到稳定的支付领域模型中。

建议包含以下核心实体：

```text
payment_intents
payment_attempts

payment_methods
payment_channels
channel_accounts

routing_rules
routing_rule_versions
routing_decisions

authorizations
captures

refunds
refund_attempts

disputes
chargebacks

payment_events
channel_callbacks
callback_delivery_attempts
```

### 核心语义

#### 1. Payment Intent

`payment_intent` 表示一笔希望完成的业务支付，例如"用户希望为订单支付
100 元"。它描述业务目标，不描述某一次具体渠道请求。

#### 2. Payment Attempt

`payment_attempt` 表示一次实际支付尝试。同一个支付意图可以包含多次
尝试：

```text
payment_intent
├── attempt 1：信用卡渠道失败
├── attempt 2：电子钱包处理中
└── attempt 3：备用渠道成功
```

因此 `payment_intent 1 ── N payment_attempt`。支付意图成功率与支付尝试
成功率必须作为两个不同指标处理。

#### 3. Routing Decision

每次支付尝试可以关联一次路由决策，用于记录：

- 候选渠道；
- 实际选择渠道；
- 命中的路由规则版本；
- 路由失败或降级原因；
- 重试和切换渠道行为。

历史查询必须使用当时生效的路由规则版本，不能关联当前规则。

#### 4. Channel Callback

渠道回调是外部事件，不等同于内部支付最终状态。系统必须允许出现：

- 重复回调；
- 乱序回调；
- 延迟回调；
- 回调状态与主动查询状态冲突；
- 回调接收成功但处理失败；
- 同一事件多次投递。

### 必须覆盖的复杂度

- 业务订单、支付意图和支付尝试的不同 grain；
- 首次尝试结果与最终支付结果的差异；
- 多渠道重试和路由切换；
- 授权与扣款分离；
- 全额和部分退款；
- 内部标准状态与渠道原始状态并存；
- 事件发生时间、接收时间和处理时间并存；
- 幂等键和重复事件；
- 规则版本；
- 异步最终一致性。

## 四、Ledger & Settlement：账务、结算与对账模型

该模块用于模拟钱包、虚拟资产、平台资金、商户结算和渠道对账。

建议包含以下核心实体：

```text
assets
ledger_accounts

ledger_transactions
ledger_entries

balance_snapshots
pending_balances

fee_rules
fee_rule_versions
fees

settlements
settlement_items

payouts
payout_attempts

external_statements
external_statement_items

reconciliation_matches
reconciliation_exceptions
```

### 核心语义

#### 1. 业务事实与账务事实必须分离

以下状态不能视为等价：

```text
订单成功
≠ 支付成功
≠ 渠道确认成功
≠ 内部账务已入账
≠ 商户已结算
≠ 银行已到账
```

业务订单、支付记录、账务分录、结算记录和渠道账单必须使用不同实体表达。

#### 2. Ledger Transaction 与 Ledger Entry

`ledger_transaction` 表示一个完整的账务事务；`ledger_entry` 表示事务中
的单条借贷或资产变动分录。一个账务事务可以产生多条分录：

```text
用户钱包             -100
主播待结算账户         +50
公会待结算账户         +10
平台收入账户           +35
渠道手续费账户          +5
```

所有分录必须能够通过同一个事务标识进行关联。

#### 3. Balance Snapshot

`balance_snapshot` 是特定时点的余额快照，不是资金流水。查询余额时必须
区分：

- 当前余额；
- 可用余额；
- 冻结余额；
- 待结算余额；
- 某一历史时点余额；
- 由流水重新计算出的余额。

#### 4. Settlement 与 Reconciliation

`settlement` 表示平台向商户、主播、公会或其他收款方进行结算；
`reconciliation` 表示内部记录与外部渠道账单之间的核对结果。系统必须
能够表示：

- 内部成功但外部账单缺失；
- 外部账单存在但内部记录缺失；
- 金额不一致；
- 币种不一致；
- 手续费不一致；
- 状态不一致；
- 日期错位；
- 多条内部记录匹配一条外部记录。

### 必须覆盖的复杂度

- 流水与余额快照并存；
- 多账户和多资产；
- 法币与虚拟资产；
- 原始币种和结算币种；
- 最小货币单位存储；
- 汇率和汇率时间；
- 手续费规则版本；
- 待结算、已结算和已到账状态；
- 内外部账单差异；
- 冲正、撤销和补账；
- 账务不可变记录与修正分录。

金额字段必须采用整数最小单位（如 `amount_minor` + `currency_code`），
不得使用浮点数保存法币金额。

## 五、Live Monetization：直播与创作者经济扩展模型

该模块是 v0.1.9 的首个行业扩展，用于覆盖直播平台中的充值、虚拟资产
消费、礼物、主播收入和公会分成。

建议包含以下核心实体：

```text
creators
agencies

live_rooms
live_sessions

virtual_assets
wallets

gift_definitions
gift_price_versions
gift_events

consumption_orders
consumption_items

revenue_share_rules
revenue_share_rule_versions
revenue_splits

creator_settlements
agency_settlements
```

### 核心语义

#### 1. Live Room 与 Live Session

`live_room` 是长期存在的直播房间；`live_session` 是一次具体开播场次，
`live_room 1 ── N live_session`。以下指标必须明确区分：

- 房间历史累计收入；
- 单场直播收入；
- 主播当日所有场次收入；
- 某场直播中的礼物数量。

#### 2. Gift Definition 与 Gift Price Version

礼物定义与礼物价格必须分离。礼物价格可能受生效时间、国家和地区、应用、
虚拟资产类型、活动、用户等级、分发渠道影响。历史消费必须保留成交时
价格和价格版本，不能通过当前价格重新计算。

#### 3. Gift Event 与 Consumption Order

`gift_event` 表示用户在直播场景中执行送礼行为；`consumption_order`
表示该行为对应的虚拟资产消费交易。二者不能假设严格一对一，因为可能
出现：

- 送礼事件创建后扣款失败；
- 扣款重试；
- 重复事件去重；
- 风控冻结；
- 后续撤销或冲正。

#### 4. Revenue Split

礼物消费后可以按照规则拆分为主播收入、公会收入、平台收入、渠道或
发行方成本、税费、活动补贴。分成规则必须支持版本化，并能够按业务
发生时生效的规则计算。

### 必须覆盖的复杂度

- 虚拟资产与法币同时存在；
- 礼物价格和分成规则版本；
- 房间、场次、主播和公会的多层关系；
- 用户消费、主播收入和平台收入的不同口径；
- 待结算、可提现和已结算金额；
- 撤销、退款和账务冲正；
- 跨地区和跨币种；
- 业务事件和账务事件之间的非一对一关系。

## 六、首批真实负载任务

v0.1.9 的任务不得只验证简单查询，应优先覆盖业务口径、grain、时间和
状态陷阱。

### 1. 首次尝试成功率与最终支付成功率

> 统计昨天各支付渠道的首次尝试成功率和最终支付成功率。

验证：payment intent 与 payment attempt、首次尝试与最终结果、渠道
归因、创建时间与完成时间、重复尝试去重。

### 2. 渠道切换成功订单

> 查询首次支付失败，但后续切换渠道并最终支付成功的订单比例。

验证：attempt sequence、routing decision、同一 payment intent 下的
多次尝试、最终状态判定。

### 3. 支付成功但账务未入账

> 找出支付已经成功，但内部账本尚未完成入账的交易。

验证：支付事实与账务事实的区别、跨领域关联、最终一致性、异常状态识别。

### 4. 账务已结算但渠道未匹配

> 找出已经完成内部结算，但尚未匹配外部渠道结算单的记录。

验证：settlement、external statement、reconciliation、时间范围和币种
匹配。

### 5. 平台净收入

> 统计每个应用上月的净收入，扣除退款、主播分成、公会分成和渠道手续费。

验证：充值金额、消费金额、平台收入和净收入口径、多类费用、多币种、
退款和冲正、分成规则。

### 6. 主播已结算收入

> 查询每个主播上月已经完成结算的收入，不包含待结算和被冻结金额。

验证：gift event、revenue split 和 settlement 的差异、主播与公会关系、
已结算/待结算/冻结状态、收入 grain。

### 7. 重复回调影响

> 统计各渠道重复回调的数量，并判断是否造成支付金额或成功订单重复统计。

验证：external event ID、callback delivery attempt、幂等和去重、事件数
与业务交易数的差异。

### 8. 历史规则回溯

> 按照交易发生时生效的手续费和分成规则，重新计算指定月份的平台收入。

验证：规则版本、valid time、历史规则与当前规则、point-in-time
correctness。

## 七、数据和复杂度要求

fixture 必须包含可核对的真实复杂度，不得通过添加只有 `id` 和 `name`
字段的人工 decoy 表模拟真实业务。

首期数据至少应包含：

- 多个组织和应用；
- 多个国家、地区和币种；
- 多个支付渠道及渠道账户；
- 成功、失败、处理中、退款和争议交易；
- 同一支付意图的多次支付尝试；
- 路由切换；
- 重复和乱序回调；
- 部分退款；
- 多种手续费和分成规则；
- 规则版本变更；
- 多种虚拟资产；
- 余额快照和账务流水；
- 正常与异常对账记录；
- 多个主播、公会、房间和直播场次；
- 逻辑删除数据；
- 跨日、跨月和跨时区数据。

数据生成器必须满足：

```text
确定性 seed
可重复生成
可控制数据规模
可注入指定异常模式
跨 Provider 结果一致
```

## 八、模块交付结构

每个业务模块使用统一目录规范：

```text
fixtures/v4/
  commerce-core/
    schema/
    seed/
    generator/
    policies/
    tasks/
    expected/
    metadata/
    README.md

  payment-orchestration/
  ledger-settlement/

  verticals/
    live-monetization/
```

每个模块至少提供：

- 逻辑数据模型；
- Provider DDL；
- 确定性数据生成器；
- Entity 和 relation 配置；
- RBAC、字段 ACL、RLS 和 mask 示例；
- 自然语言任务；
- 预期结果；
- 失败分类；
- grain、时间、单位和状态说明。

## 九、任务定义规范

每个任务必须显式记录其目标能力和潜在失败类型。示例：

```yaml
id: payments.retry.final_success_rate

question: >
  统计昨天各支付渠道的首次尝试成功率和最终支付成功率。

capabilities:
  - aggregation
  - multi_entity_relation
  - temporal_filter

semantic_traps:
  - intent_vs_attempt_grain
  - first_attempt_vs_final_result
  - created_at_vs_completed_at
  - duplicate_attempt_counting

expected:
  result_fixture: expected/payments_retry_success.csv

failure_categories:
  - wrong_entity
  - wrong_relation
  - wrong_grain
  - wrong_time_field
  - wrong_status
  - duplicate_counting
  - ir_not_expressible
```

## 十、版本验收标准

v0.1.9 完成需要同时满足以下条件：

1. 完成 `commerce-core`、`payment-orchestration`、`ledger-settlement`
   和 `live-monetization` 四个模块的首期 Schema；
2. 至少提供一个可直接运行的组合 profile；
3. 数据生成器可使用固定 seed 重复生成相同数据；
4. 至少提供 20 个真实业务任务，其中通用商业与支付任务不少于 12 个、
   账务/结算/对账任务不少于 5 个、直播行业任务不少于 3 个；
5. 每个任务均包含预期结果和失败分类；
6. PostgreSQL、MySQL 和 OceanBase 在支持范围内产生一致的逻辑结果；
7. 至少覆盖以下复杂度中的八项：相似实体；多条可选关联路径；多种时间
   字段；流水与快照并存；不同 grain；多币种或多资产；枚举和状态映射；
   规则版本；逻辑删除；异步最终一致性；重复事件；内外部对账差异；
8. 评测报告能够将失败归因到：Agent 发现；参数构造；关系选择；grain；
   时间语义；状态语义；单位或币种；IR 表达能力；Provider 差异；治理
   策略；
9. 至少使用一套真实或脱敏后的支付中台工作负载进行 dogfooding，并输出
   问题清单；
10. v0.1.9 的结果能够为至少一个休眠 Roadmap 方向提供明确的 go、no-go
    或继续观察结论。

## 十一、非目标

v0.1.9 不以以下工作为目标：

- 完整复刻任何现有支付产品的内部数据库；
- 建设生产级支付系统；
- 实现支付渠道 SDK；
- 实现真正的资金清算；
- 建设完整风控引擎；
- 建设完整计费平台；
- 为每个行业提供独立完整模型；
- 为了覆盖任务而无门禁地扩大 IR（IR 不可表达记为失败证据，走主路线图
  休眠项的分流出口）；
- 将业务语义直接硬编码进 sql-mcp-server 核心。

本版本的目标是建立一套足够真实、可复现、可扩展的业务问题发现工具，
而不是交付支付业务产品。

## 十二、后续扩展方向

在核心模块稳定后，社区可以按照相同接口增加新的行业 profile：

```text
marketplace
saas-billing
gaming
digital-content
travel
mobility
cross-border-commerce
```

新增行业模块不得复制支付、账务和商业核心实体，而应复用现有模块并只
增加行业特有语义。

v0.1.9 最终应形成一个长期可扩展的真实业务负载框架：

```text
通用商业模型
+ 支付编排模型
+ 账务结算模型
+ 可插拔行业模型
```

该框架既服务于项目自身的能力边界验证，也允许外部开发者贡献真实
Schema、任务、失败案例和治理需求。
