# Evidence-Gated Directions

返回[主路线图](../roadmap.md)。

本文件记录尚未获得版本承诺的战略方向。以下方向按依赖排序，只有满足触发证据后，
才可提升为主路线图中的 `Next`。

## Foundation. IR Semantics and Provider Conformance

为 Canonical IR 定义可验证的操作语义，并建立跨 Provider 一致性证据。它回答
的不是"支持更多 SQL"，而是"同一个受治理查询在不同 Provider 上到底是不是同
一个查询"：

- 每个 IR 算子的输入、输出与错误语义；bag semantics（不误按集合处理）；
  SQL 三值逻辑（`TRUE`/`FALSE`/`UNKNOWN`）；`NULL`、空集、重复行与排序
  稳定性；decimal、timestamp、timezone、collation 与 overflow 边界；
- reference interpreter 作为 codegen 与 Provider 执行的 oracle；
- 跨 Provider differential test：相同 IR、相同 fixture，结果与错误分类必须
  等价，差异要么修复、要么标注为 documented deviation 并降级 capability；
- Provider lowering 的语义保持义务（refinement obligation）：不要求数学上
  证明数据库实现正确，但每个 Provider 必须对其支持的 IR 子集提供可验证的
  精化证据；
- `native` / `emulated` / `restricted` / `unsupported` 的判定规范，落入
  [Provider Roadmap](../provider-roadmap.md) 能力模型。

状态：**已随 `v0.1.8` 交付**（[发布说明](../releases/v0.1.8.md)）——
[语义规范](../design/ir-semantics.md)、reference interpreter 与三库
differential conformance suite。conformance suite 是之后任何新 Provider
和 IR 表达力扩展（L12/L13）的验收前置；扩展 IR 时本方向随之扩展。

## Foundation. Semantic Metadata

为现有声明式配置补充不直接执行查询的语义元数据：

- Entity grain、默认时间维度、discoverability 和替代关系；
- Field 语义角色、枚举、单位、币种、尺度和时间含义；
- 服从 RBAC、hidden 和 mask 的渐进披露；
- 稳定语义 lint 与向后兼容的 import/export。

触发证据：`v0.1.7` 校准后的定向任务或真实大 schema/语义歧义负载证明，grain、
时间、枚举、单位或 catalog token 缺失是显著失败来源。

旧配置行为必须保持不变；元数据不得改变授权或 SQL 语义，不得产生越权或 mask
侧信道。退出门禁必须使用覆盖对应失败来源的任务集提供前后对照；若没有可测量改善，
停止投入。该方向稳定并用于真实配置后，才可触发 L1 Executable Semantic Layer。

## L1. Executable Semantic Layer

在现有 IR 上增加有限的 `Metric`、`Dimension` 和 `TimeDimension`，支持 grain、
去重键、合法聚合、预定义过滤、单位与币种限制及结果不变量。

触发证据：公开 Eval 证明业务语义是主要失败源，且声明式元数据已用于真实配置。
所有 Metric 必须编译到现有 IR，并继续经过授权、row policy、mask、成本和预算。

## L2. Catalog Discovery

提供授权过滤的 `search_catalog → describe → execute`，返回候选和可解释匹配理由。
embedding 只能作为排序信号，真正授权仍由执行层强制。

触发证据：公开 Eval 证明渐进披露在真实大 schema 上仍造成显著选择失败或 token
开销，且语义目录契约稳定。

## L3. Cost and Data Egress Governance

分别治理数据库扫描、结果传输、LLM 上下文和敏感数据暴露成本，并评估 principal、
tenant、daily 和敏感数据预算。

触发证据：benchmark、真实成本反馈或绕过数据证明现有 request/session 预算不足。

## L4. Provider Capability Assurance and Expansion

能力模型、保证强度、证据门槛和 SQLite、SQL Server、ClickHouse 等候选顺序均以
[Provider Roadmap](../provider-roadmap.md) 为唯一事实源。

触发证据：满足 Provider Roadmap 中对应候选的进入条件。新 Provider 可独立并行
立项，但不得与控制面或语义层捆绑。

## L5. Production Operations and Doctor

扩展端到端 OpenTelemetry、SLO/告警、外部审计 sink、生产 runbook、多实例观测、
故障注入和 `doctor`。

触发证据：至少一个持续运行的参考部署产生真实故障、容量或诊断数据。

## L6. Write Operation Governance

评估 `idempotency_key`、乐观锁、写 preview、blast-radius cap 和 approval token。

触发证据：存在真实 Agent 写需求，且 token/profile、审计和 dry-run 语义稳定。

## L7. Enterprise Identity and Scale

评估 MCP OAuth 2.1、delegation chain、多实例 snapshot、shared budget、persistent
session、signed snapshot、灾难恢复和短期数据库凭证。

触发证据：真实企业部署证明单实例身份与配置分发是采用或安全阻碍。

## L8. Data Governance and Durable Audit

评估数据分类、sensitive egress budget、聚合隐私阈值、lineage，以及
`best_effort`、`durable`、`fail_closed` 审计语义。

触发证据：受监管部署提出明确保存期、完整性、可用性和故障处理要求。

## L9. Management UI

覆盖数据源、实体、策略、token/profile、发布、回滚、模拟和 decision trace。

触发证据：最小控制面 API 至少稳定一个版本，且用户研究证明 UI 是主要采用障碍。

## L10. Query Learning and Reuse

评估管理员审核的查询模板、语义 cache key 和 fingerprint 自动校准。自动建议只能
收紧或等价改写查询。

触发证据：revision 稳定，并出现高频重复查询、估算偏差或 Agent 重试数据。

## L11. Constrained Extensibility

评估 `IdentityProvider`、`SecretProvider`、`AuditSink`、`BudgetStore`、
`SnapshotStore`、`MaskingFunction`、`Provider` 和 `TelemetryHook` 等扩展点。

触发证据：至少两个独立集成需要同一扩展边界。扩展必须经过统一 engine，禁止注册
可执行任意 SQL 的 MCP tool。

## L12. Governed Query Expressiveness

在不引入任意 SQL 的前提下扩大统一 IR 可表达的声明式查询集合。候选池按
"Agent 任务覆盖率 / 实现风险"排序，而不是按 SQL 语法完整性排序：

1. 沿显式授权关系的受控 join；
2. Having 与复合聚合；
3. conditional aggregation（如 `SUM(CASE WHEN status = 'paid' THEN
   amount ELSE 0 END)`，大量业务任务依赖它而不需要完整 CTE）；
4. date bucketing 与日期算术；
5. semi/anti join 与存在性查询；
6. 受控标量函数；
7. Top-K per group；
8. 窗口函数；
9. 集合运算；
10. CTE；
11. as-of / effective-time 查询（金融、订单状态、价格与历史版本数据）。

每项能力必须定义完整语义（bag、NULL、排序、聚合空集、可见性传播、
RLS/mask/ACL 作用位置、成本语义与 Provider 降级），通过跨 Provider 统一语义
测试与 property/differential codegen 测试（基线为 Foundation IR Semantics
的 conformance suite），禁止 `RawSQL`/`RawExpression` 逃生口。

触发证据：Agent Eval（pilot 或公开 suite）或真实部署证明任务失败源于 IR 表达力
不足，且无法用现有 IR 合理、稳定且低成本地完成。逐能力独立过门禁，不整体升级；
L1 的 Metric 编译目标是本方向的消费者。详细设计见
[IR Evolution](../design/ir-evolution.md)。

## L13. Provider Optimization Extensibility

在统一逻辑语义与治理不变量下为 Provider 开放类型化优化切口：Canonical IR →
Provider Lowering → Physical Execution Plan 三层模型，涵盖 logical rewriter、
physical planner、参数绑定、类型映射、Explain 解析、执行策略与临时资源生命
周期。lowering 必须语义保持且授权资源闭包不得扩大；Provider 决定如何执行，
不决定是否允许执行，任何切口不得绕过授权、RLS、mask、预算、审计或统一 engine。

每次优化必须产出可审计的 proof artifact（非数学定理证明）：输入 IR 与输出
计划的 fingerprint、应用的规则、依赖的 capability 假设、优化前后的授权资源
闭包、对应的语义测试类别。机器可检查两条不变量——优化后访问的资源集合仍是
授权闭包的子集，且可观测语义与优化前一致——使 Provider 作者无法经优化切口
扩大访问范围。

触发证据：至少两个 Provider 出现可测量的原生优化需求或语义差异问题，且现有
统一 codegen 无法表达。capability 的实现方式分级
（`native`/`emulated`/`restricted`/`unsupported`）落入
[Provider Roadmap](../provider-roadmap.md) 能力模型；系统级扩展边界清单以 L11
为准。详细设计见 [IR Evolution](../design/ir-evolution.md)。

## L14. Result Provenance and Evidence Envelope

审计回答"系统为什么允许或拒绝了这个请求"；本方向补另一条链路——"Agent 给
用户的这个数字来自哪些数据、哪个配置版本、哪次执行"。每次结果附带机器可读
evidence envelope：

- 逻辑查询与物理计划 fingerprint、snapshot/config revision、数据源与
  schema revision；
- 涉及实体与字段、已应用策略与 mask 决策摘要；
- 数据新鲜度、时区与统计截止时间；截断、采样或近似计算标记；
- 输出列到 Entity/Field/Metric 的 lineage；`decisionId` 与 Agent 可引用的
  compact citation handle。

目标是让 Agent 能回答"这个收入数字来自订单表，使用 UTC 自然日，数据更新到
昨日 23:59，排除了退款订单"，直接增强"SQL 合法但业务答案错误"的可诊断性。
envelope 内容必须服从 RBAC、hidden 与 mask 可见性，不得成为新的侧信道；
字段进入版本化工具契约并由 golden 测试冻结。

触发证据：Eval 失败归因、真实用户反馈或公开对照需求证明答案不可追溯是信任
或采用障碍（已提升为主路线图 Next 2）。

## L15. Schema Drift Detection and Impact Analysis

配置 revision 之外，数据库自身也在变化：字段删除、`INT` 改 `BIGINT`、enum
增值、precision 变化、index 消失、view 定义变化、timezone/collation 变化、
row-policy 依赖列变化。本方向把现有启动/reload 的 drift 检查扩展为分级
治理：

- 数据库 schema fingerprint 与 startup/reload 校验；
- drift 分类：`compatible` / `behavior-changing` / `security-sensitive` /
  `breaking`；
- drift 对 Entity、Field、Policy、Mask、Metric 的影响图；
- incompatible drift 默认拒绝 readiness（fail closed）；
- 配置 revision 与 schema revision 绑定；发布前 simulate、schema 变化后的
  canary validation、升级与回滚的双版本兼容窗口。

安全网关不能只验证"SQL 还能执行"，必须验证 schema 变化是否扩大授权资源
闭包或改变策略语义（例如有限枚举列变为自由字符串）。

触发证据：真实部署出现 schema 演进需求，或最小控制面进入条件满足（本方向
是其前置，已提升为主路线图管理面阶段第 1 项）。

## L16. Data Inference and Policy Composition Safety

现有 RBAC、字段 ACL、row policy 与 mask 回答"单个查询是否允许"；本方向回答
"多个各自合法的查询组合起来，是否可以推导出本不该看到的信息"（inference /
differencing attack，如由部门人数、平均薪资和剔除一人后的平均薪资反推个人
薪资）：

- mask 与 aggregate 的组合执行顺序；row policy 与 join 后可见性的组合规则；
- 小分组抑制（如 `COUNT < k` 时拒绝或降精度）与 differencing query 检测；
- 多轮查询的推断预算；hidden entity/field 的存在性侧信道；
- error、EXPLAIN、row count 与 timing 泄漏；
- policy monotonicity：收紧权限后不得增加可观测信息；
- non-interference 风格的安全测试进入回归。

触发证据：受监管部署或真实租户提出推断泄漏要求；受监管部署上线前必须完成。
现有 fail-closed 边界不因本方向未完成而声明覆盖推断攻击。

## L17. MCP Protocol Conformance and Contract Stability

把 MCP 协议层从分散的客户端测试独立为认证矩阵：

- 支持的 protocol version 与 capability negotiation；stdio 与 streamable
  HTTP 的行为一致性；cancellation、timeout 与 disconnect 语义；
- tool schema 版本化与 contract fingerprint（schema digest）：防 rug pull /
  tool mutation——客户端首次看到的 tool schema 与后续调用不得静默漂移，不
  兼容变更 fail closed 或要求重新协商（会话内 `tools/list` 固定的现状是
  起点）；
- structured content 与错误返回规范、tool annotations 与安全描述；
- session 与 tenant 绑定、客户端重试的幂等语义；
- Cursor、Claude Desktop、VS Code 等客户端兼容矩阵；协议级 fuzz 与
  malformed message 测试。

触发证据：客户端生态扩大、互操作性问题报告或协议版本演进；契约 digest 与
不兼容变更 fail closed 等安全相关子项可提前并入工具契约维护。

## L18. Governed Configuration Scaffolding

Adopt 的最大真实成本可能不是启动服务，而是手写几十上百个 Entity、Field、
Relation 和 Policy。本方向提供安全的配置生成，核心原则是
**Introspection ≠ Authorization**：

- 从 schema introspection 生成**未授权草稿**：所有生成内容默认
  `discoverable: false`、无任何执行权限、必须经管理员 review；
- 自动推断候选：primary key、foreign key、grain、时间字段、enum、货币
  字段、PII candidate；
- 输出 lint、risk report 与 diff；提供最小化配置建议而不是全量暴露；
- 支持从 dbt manifest、数据字典等外部目录导入。

绝不因为数据库里存在一个表，就自动让 Agent 可见或可访问；自动化只生成
候选，授权永远是人的显式决定。

触发证据：真实大 schema 用户的接入成本证据（此时其价值可能大于新增第五、
第六种数据库）。

## 跨阶段非目标

- 自然语言转任意 SQL、通用 SQL parser + sanitizer，以及 `RawSQL`/
  `RawExpression` 等任意表达式逃生口；
- 自动暴露整个数据库或根据自省结果自动授权；
- 未声明关系的任意 join、跨数据源 federated query 或完整 BI DSL（沿显式授权
  关系的受治理 join 与集合运算属于 L12，须按其门禁逐项升级）；
- 无审计的生产配置变更、写操作或绕过 engine 的插件；
- 在 capability 和数据库级证据稳定前一次性扩展大量 Provider；
- 自建完整 IAM、secret manager、低代码平台或数据库管理工具全集；
- 自动汇率换算、任意表达式、复杂业务日历或未证明需求的差分隐私；
- 查询结果向量化后的自动长期记忆；
- 通用 workflow 或 agent orchestration；
- 通用 NL2SQL（受治理配置脚手架 L18 只生成未授权候选配置，不生成查询）。
