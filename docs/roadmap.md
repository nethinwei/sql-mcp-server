# Roadmap

当前稳定基线为 `v0.1.4`。已实现能力、默认值和已知边界分别以
[v0.1.4 发布说明](releases/v0.1.4.md)、[配置参考](configuration.md) 和
[安全模型](security.md) 为准。

本文件只描述未发布工作的优先级、目标和验收条件：

- **Committed**：当前版本承诺，范围已收敛；同时只保留一个版本；
- **Next**：已排序候选，满足进入条件后才升级为版本承诺；
- **Later**：战略方向，不承诺版本或交付时间；
- **Deferred**：与当前定位冲突或缺少需求证据，暂不规划；
- `docs/releases/` 只记录已发布版本，不预建未来 release 文档；
- 协议、数据模型和接口细节在立项后进入独立设计文档。

---

## 产品方向

SQL MCP Server 是面向 AI Agent 的**受控 SQL 数据访问网关**。项目不追求数据库
功能数量最多，而聚焦四项可验证优势：

1. **安全边界可证明**：授权、成本、数据隔离和失败语义有测试证据；
2. **Agent 使用有效**：Agent 能正确选取工具、构造参数并从拒绝中恢复；
3. **安装与集成简单**：外部用户可以快速安装、验证和升级；
4. **生产行为可测量**：性能、容量、审计和降级语义均有公开基线。

### 规划原则

- Outcome 优先：每个版本交付可观察结果，而非堆积功能清单；
- 安全优先：所有新入口默认 fail closed，不以可用性优化替代授权；
- 增量交付：一个版本只承担一个主要风险；
- 证据驱动：未经用户需求、测试或 benchmark 验证的能力不进入承诺范围；
- 窄接口：不接受任意 SQL，不让插件或控制面绕过统一 engine；
- 兼容优先：破坏性变化必须有迁移说明和明确的版本策略。

---

## Committed — v0.1.5 Data Plane Contract Hardening

### 目标

在控制面接管配置和 token 生命周期前，固定数据面可观察契约、HTTP 协议路径和
可移植配置表示，避免 `v0.2.0` 同时承担协议、配置与持久化风险。

### 候选范围

- 定义稳定、机器可读的拒绝分类、rewrite hints 和 `decision ID`，并保持现有文本
  错误的兼容性；
- 让 `decision ID` 可关联 MCP 响应、审计事件与 trace；
- 为真实 streamable HTTP `/mcp` 路径建立 e2e，覆盖认证、subject/tenant、
  allow/deny、mask、row policy、成本拒绝和事务；
- 定义 MCP tool schema、机器可读错误和配置导出格式的版本兼容规则；
- 提供确定性 YAML `export`，固定默认值、字段顺序和 secret 表示规则；
- 完成 revision/snapshot 数据模型、兼容性和失败语义设计评审。

### 验收条件

- 拒绝响应可稳定解析 `code`、`reason`、`hints` 和 `decision ID`；
- HTTP e2e 与当前 PostgreSQL MCP e2e 的关键授权路径一致；
- tool contract 的兼容与破坏性变化有机器可检查的版本规则；
- 同一有效配置重复 export 得到确定性、可审阅的结果；
- 无效、损坏或不兼容 snapshot 的拒绝语义，以及在途请求与新 revision 的一致性
  语义形成评审通过的设计结论。

### 非目标

- 持久化 revision、draft/publish/rollback 或 token revocation；
- `diff`、`simulate` 和管理 API；
- Provider 扩展、管理 UI、完整可观测性平台或 durable audit。

---

## Next — v0.1.6 Operational Safety Baseline

### 进入条件

`v0.1.5` 验收完成，且关键拒绝路径已能通过 `decision ID` 关联。

### 目标

在控制面引入持久化、发布和撤销路径前，建立最小可观测与性能基线，使数据面故障和
`v0.2.0` 引入的回归可以被检测、定位和比较。

### 候选范围

- 区分进程存活、配置 snapshot 已加载和数据库依赖可用的 liveness/readiness；
- 暴露请求、拒绝、延迟、reload 成败和 audit dropped 等最小 metrics；
- 为启动、reload、拒绝和审计降级提供结构化日志，关联 `decision ID`、数据源和
  配置 fingerprint；
- 建立可复现的 data-plane overhead benchmark，记录控制面引入前的延迟与资源基线；
- 对公开声明的 streamable HTTP MCP 能力运行 conformance 或等价协议 smoke。

### 验收条件

- readiness 在配置不可用和数据库依赖不可用时按文档语义失败，liveness 不被误用；
- metrics 和结构化日志能够定位拒绝、reload 失败与审计丢弃，且不泄露 secret、
  transaction token 或受保护字段；
- benchmark 可在固定环境重复运行并报告 p50/p95/p99 增量延迟与资源开销；
- stdio 与 streamable HTTP 的公开能力均有协议级 smoke 证据。

### 非目标

- 完整 SLO 平台、托管 dashboard 或告警系统；
- Helm chart、生产集群拓扑、故障注入平台或多实例观测；
- durable audit、Provider 扩展或控制面 API。

---

## Next — v0.2.0 Control Plane Foundation

### 进入条件

- `v0.1.4`、`v0.1.5` 和 `v0.1.6` 验收完成；
- 数据模型、snapshot 兼容性和失败语义通过设计评审；
- 持久化技术选择与单实例降级语义有可验证原型。

### 目标

提供无 UI 的最小控制面，使配置能够持久化、版本化、发布、回滚并安全分发到数据面。

### 候选范围

- 数据源、实体、字段和现有策略的持久化管理 API；
- draft → publish 的 immutable revision；
- 已发布 snapshot 的校验、加载和回滚；
- token / capability profile 与 expiry、revocation、audience；
- principal、actor、subject、tenant 的明确区分；
- `validate`、`diff`、`simulate`、import 和 export；
- YAML 继续作为 bootstrap 和可移植配置格式；
- 控制面操作审计。

### 验收条件

- 重启后 published revision 和 token 状态保持一致；
- 无效、损坏或不兼容 snapshot 被拒绝加载；
- token 撤销传播延迟有明确上限并可测试；
- 在途请求与新 revision 的一致性语义有测试和文档；
- 用户可从 YAML 导入，并导出确定性、可审阅的配置；
- 数据面在控制面短时不可用时的行为有测试和文档。

### 非目标

- 管理 UI、通用 typed ABAC 或完整 GitOps promotion workflow；
- 多实例强一致与企业 OAuth、DPoP、WIF；
- durable audit；
- 任何新 Provider 捆绑发布。

---

## Later — Evidence-Gated Directions

以下方向只有满足进入条件后才提升为 `Next`。

### Operational Evolution

在 `v0.1.6` 最小基线上扩展 trace、SLO/告警、provider 一致性、外部审计 sink、
Helm chart、生产运维 runbook、多实例观测和故障注入。

进入条件：至少有一个持续运行的参考部署，并有真实故障或容量数据证明扩展价值。

### Agent Effectiveness

建立覆盖查询、分页、聚合、多租户、拒绝和修复的任务集，测量 task success、
tool selection、argument validity、first-call success、repair rate、tool calls 和
token footprint，并基于数据优化 tool surface。

进入条件：至少有两个真实客户端集成或稳定的模型测试环境。

### Management UI

覆盖数据源、实体、策略、token/profile、发布、回滚、模拟和 decision trace。

进入条件：控制面 API 经至少一个版本稳定，且用户研究证明 UI 是主要采用障碍。

### Enterprise Identity and Scale

包括标准 MCP OAuth 2.1、issuer/audience/scope、delegation chain、多实例 snapshot
distribution、shared budget、persistent session、signed snapshot、灾难恢复和
短期数据库凭证。DPoP、WIF 和各云数据库身份分别独立评估。

### Data Governance and Durable Audit

包括数据分类策略、sensitive egress budget、聚合隐私阈值、approval workflow、
lineage，以及 `best_effort`、`durable`、`fail_closed` 三种明确审计语义。现有
schema drift detection 的后续增强按真实误报、漏报或迁移需求立项。

### Constrained Extensibility

候选扩展点包括 `IdentityProvider`、`SecretProvider`、`AuditSink`、`BudgetStore`、
`SnapshotStore`、`PolicyAttributeProvider`、`MaskingFunction`、`Provider` 和
`TelemetryHook`。任何扩展必须经过统一 engine，不允许直接注册可执行任意 SQL 的
MCP tool。

### Provider Expansion

Provider 扩展不作为 `v0.2.0` 前置项，也不与控制面捆绑发布。候选顺序、capability
model、进入条件和数据库级风险见 [Provider Roadmap](provider-roadmap.md)。

---

## Deferred

以下能力与当前安全定位冲突或缺少需求证据，暂不规划：

- 自然语言转任意 SQL；
- 通用 SQL parser + sanitizer；
- 通用 join / union 和跨数据源 federated query；
- 自动写入任意新表；
- 自定义脚本策略或内嵌大模型；
- 自建完整 IAM 或 secret manager；
- 在核心语义稳定前一次性扩展大量数据库或绕过 provider capability 契约；
- GraphQL / OpenAPI、复杂 BI 可视化和数据库管理工具全集；
- 差分隐私等尚无明确场景的高级能力。

---

## Project Health Metrics

这些指标用于判断方向和排序，不作为单个版本的硬性交付门槛：

- 产品与安全：MCP conformance、跨 provider 一致性、critical/high corpus 通过率、
  fuzzing crash、增量延迟和 policy decision 可重放比例；
- Agent 效果：task success、first-call success、repair rate、tool calls、token
  footprint 和 policy violation attempt rate；
- 开源采用：安装与活跃部署趋势、外部 issue/贡献者/集成、生产案例、release cadence
  和漏洞响应时间。

star 数只作为传播信号，不作为产品质量代理指标。

## Roadmap Maintenance

- 每个 release 后复审一次优先级和 `Next` 范围；
- `Committed` 同时只保留一个版本；
- 新能力进入 `Committed` 前必须具备问题证据、明确非目标和可验证验收条件；
- 未完成事项不自动滚入下一版本，先重新评估价值和依赖；
- 已发布行为进入 release notes、配置参考或安全文档，不继续留在 roadmap。
