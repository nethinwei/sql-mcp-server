# Evidence-Gated Directions

返回[主路线图](../roadmap.md)。

本文件记录尚未获得版本承诺的战略方向。以下方向按依赖排序，只有满足触发证据后，
才可提升为主路线图中的 `Next`。

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

## 跨阶段非目标

- 自然语言转任意 SQL、通用 SQL parser + sanitizer；
- 自动暴露整个数据库或根据自省结果自动授权；
- 通用 join/union、跨数据源 federated query 或完整 BI DSL；
- 无审计的生产配置变更、写操作或绕过 engine 的插件；
- 在 capability 和数据库级证据稳定前一次性扩展大量 Provider；
- 自建完整 IAM、secret manager、低代码平台或数据库管理工具全集；
- 自动汇率换算、任意表达式、复杂业务日历或未证明需求的差分隐私。
