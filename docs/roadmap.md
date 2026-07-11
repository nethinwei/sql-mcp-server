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

- 定义稳定、机器可读的拒绝分类、rewrite hints 和 `decision ID`，覆盖查询形状上限、
  成本与预算拒绝，并保持现有文本错误的兼容性；
- 让 `decision ID` 可关联 MCP 响应、审计事件与 trace；
- 为真实 streamable HTTP `/mcp` 路径建立 e2e，覆盖认证、subject/tenant、
  allow/deny、mask、row policy、成本拒绝和事务；
- 定义 MCP tool schema、机器可读错误和配置导出格式的版本兼容规则；
- 提供确定性 YAML `export`，固定默认值、字段顺序和 secret 表示规则；
- 完成 revision/snapshot 数据模型、兼容性和失败语义设计评审。

### 验收条件

- 拒绝响应可稳定解析 `code`、`reason`、`retryable`、`constraints`、`hints` 和
  `decision ID`；修复建议只能收紧或等价改写请求，不得放宽授权或安全限制；
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
- 暴露请求、拒绝分类、延迟、reload 成败和 audit dropped 等最小 metrics；
- 为启动、reload、拒绝和审计降级提供结构化日志，关联 `decision ID`、数据源和
  配置 fingerprint；
- 建立可复现的 data-plane overhead benchmark，记录控制面引入前的延迟、资源基线，
  以及成本估算与实际反馈的偏差；
- 对公开声明的 streamable HTTP MCP 能力运行 conformance 或等价协议 smoke。

### 验收条件

- readiness 在配置不可用和数据库依赖不可用时按文档语义失败，liveness 不被误用；
- metrics 和结构化日志能够定位拒绝、reload 失败与审计丢弃，并比较成本估算、超时
  与实际反馈，且不泄露 secret、transaction token 或受保护字段；
- benchmark 可在固定环境重复运行并报告 p50/p95/p99 增量延迟与资源开销；
- stdio 与 streamable HTTP 的公开能力均有协议级 smoke 证据。

### 非目标

- 完整 SLO 平台、托管 dashboard 或告警系统；
- Helm chart、生产集群拓扑、故障注入平台或多实例观测；
- durable audit、Provider 扩展或控制面 API。

---

## Next — v0.1.7 Semantic Metadata Baseline

### 进入条件

- `v0.1.5` 和 `v0.1.6` 验收完成；
- 语义配置、渐进披露和兼容性规则通过设计评审。

### 目标

在控制面持久化实体与字段模型前，建立向后兼容的语义元数据和发现契约，减少 Agent
对 row grain、时间、枚举、单位和对象替代关系的猜测，同时不改变数据面执行语义。

### 候选范围

- 为 Entity 声明显式 row grain、默认时间维度、discoverability、弃用与替代关系；
- 为 Field 声明 `identifier`、`dimension`、`measure`、`time` 等语义角色，以及
  枚举值含义、单位、币种、尺度、时间含义和 nullable 信息；
- 为实体描述提供 `summary`、`standard`、`full` 渐进披露级别，所有级别继续服从
  RBAC、hidden 和 mask 规则；
- 扩展 schema lint，检测 alias 冲突、无效替代引用、grain/time 引用错误，以及金额、
  比例和 duration 等尺度歧义；可选元数据缺失默认产生 warning 而非启动失败；
- 将新增元数据纳入确定性 YAML import/export，并定义 additive 兼容规则。

### 验收条件

- 现有有效配置无需修改即可加载，未声明语义元数据时保持当前行为；
- 同一配置重复 export 的语义元数据字段顺序与表示确定；
- 不同披露级别不返回未授权、hidden 或可造成 mask 侧信道的字段信息；
- lint 结果具有稳定机器码，并区分 error 与 warning；
- 新增元数据不改变现有授权决定、SQL 编译、聚合合法性或查询结果。

### 非目标

- Metric 表达式、派生指标或可执行的业务语义层；
- Query Intent、动态 catalog search、description eval 或自动歧义消解；
- 运行时枚举值翻译、按语义角色强制聚合或业务日历计算；
- 结果摘要、采样、统计 profile 或压缩协议。

---

## Next — v0.2.0 Control Plane Foundation

### 进入条件

- `v0.1.4`、`v0.1.5`、`v0.1.6` 和 `v0.1.7` 验收完成；
- 数据模型、snapshot 兼容性和失败语义通过设计评审；
- 持久化技术选择与单实例降级语义有可验证原型。

### 目标

提供无 UI 的最小控制面，使配置能够持久化、版本化、发布、回滚并安全分发到数据面。

### 候选范围

- 数据源、实体、字段、v0.1.7 语义元数据和现有策略的持久化管理 API；
- draft → publish 的 immutable revision；
- 已发布 snapshot 的校验、加载和回滚；
- token / capability profile 与 expiry、revocation、audience；
- principal、actor、subject、tenant 的明确区分；
- 覆盖语义元数据的 `validate`、`diff`、`simulate`、import 和 export；
- `simulate` 不执行目标查询，返回授权结果、强制策略、访问字段和预估成本等安全摘要；
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

## Next — v0.3.0 Agent Effectiveness Baseline

### 进入条件

- `v0.2.0` 验收完成；
- 至少有两个真实 MCP 客户端集成，或稳定的多模型测试环境；
- 主要拒绝路径已提供稳定、机器可读的错误与修复建议。

### 目标

建立可复现的 Agent 任务评测，使 tool surface、description 和错误恢复的优化由数据
驱动，而不是依赖单一模型或人工直觉。

### 候选范围

- 建立带版本的固定任务集与期望答案，覆盖查询、分页、聚合、多租户、拒绝、修复和
  语义正确性；
- 测量 entity/tool selection、argument validity、first-call/final task success、
  repair rate、tool-call count，以及 schema、context 和 result token；
- 覆盖不同模型家族和 MCP 客户端，并固定可公开的模型、客户端、提示和环境元数据；
- 将确定性的 harness、fixture 和 replay 纳入 CI；有随机性的在线模型评测在定期或
  发布前运行，报告多次运行分布，不以单次结果阻塞每次提交；
- 使用前后对照结果评审 tool schema、description 和 error hint 变更。

### 验收条件

- 公开任务集版本、评分规则、运行配置和首份 baseline，结果可由文档化命令复现；
- 报告不同模型与客户端的成功率分布、修复率、调用数和 token footprint；
- description、tool schema 或 error hint 的行为变化具有同任务集前后对照；
- critical/high 安全 corpus、不变量和授权路径无回归。

### 非目标

- Metric 表达式执行、Query Intent 或动态 catalog search；
- 自动改写并执行生产查询；
- 将非确定性的在线模型结果作为每次 CI 的硬门禁；
- LLM token 硬预算或模型供应商排名。

---

## Next — v0.4.0 Executable Semantic Layer

### 进入条件

- `v0.3.0` 评测证明 grain、时间或指标推导是主要失败来源；
- `v0.1.7` 语义元数据已用于至少一个真实配置；
- 语义表达式、兼容性和失败语义通过设计评审。

### 目标

在现有受控 IR 和安全执行链上增加有限、可验证的业务语义，使常见分析问题不再依赖
Agent 临时推导指标、粒度和聚合规则。

### 候选范围

- 定义有限的 `Metric`、`Dimension` 和 `TimeDimension`；
- 支持去重键、合法聚合、可加性、预定义过滤、单位与币种限制；
- 对比例范围、时间顺序等基础结果不变量进行可配置校验；
- 为 grain、聚合、单位、币种和结果校验提供稳定的 `SEMANTIC_*` 拒绝分类；
- 所有 Metric 编译到现有受控 IR，并继续经过授权、row policy、mask、成本 gate、
  预算和统一 engine。

### 验收条件

- 预先固定的 semantic correctness 任务集相对 `v0.3.0` baseline 达到设计评审设定
  的提升目标；
- 非法 grain/聚合和无明确换算规则的跨币种组合 fail closed；
- 结果不变量失败可解释、可审计，并且不返回未授权数据；
- 未声明可执行语义模型的现有配置保持当前查询与授权行为。

### 非目标

- 自然语言转任意 SQL、完整 BI DSL 或通用 join；
- 自动汇率换算、复杂业务日历或任意表达式执行；
- 动态 catalog search、Query Intent、结果采样或压缩协议。

---

## Later — Evidence-Gated Directions

以下方向按依赖排序，只有满足各自进入条件后才提升为 `Next` 并获得版本号。

### Cost and Data Egress Governance

分别计量数据库扫描、网络与结果传输、LLM 上下文和敏感数据暴露成本；候选能力包括
request、session、principal、tenant 和 daily 预算，以及写操作、大时间范围、
高基数 group、敏感字段、大规模 export/expand 和未知 fingerprint 的风险自适应
Agent dry-run。LLM token enforcement 只有真实成本和绕过数据后才进入承诺范围。

进入条件：`v0.1.6` benchmark 和 `v0.2.0` principal/token 模型稳定，并有预算绕过、
估算偏差、结果传输或敏感数据暴露证据。

### Catalog Discovery

在大 schema 场景下提供授权过滤的 `search_catalog → describe → execute` 发现路径，
返回候选及名称、alias、字段、Metric、示例和历史效果等匹配理由。embedding 只能是
排序信号之一；未被发现不等于无权限，真正授权仍由执行层强制。

进入条件：`v0.3.0` 任务集证明渐进披露在真实大 schema 上造成显著选择失败或 token
开销，且 `v0.4.0` 语义目录契约稳定。

### Provider Capability Assurance

Provider capability 同时表达能力是否存在和保证强度，保证强度统一为
`unsupported`、`best_effort`、`enforced`。硬安全限制缺少 `enforced` 能力时，
必须由核心层提供等价强制或 fail closed；能力模型升级不自动附带新 Provider。

进入条件：至少一个现有或候选 Provider 暴露出 bool capability 无法准确表达的安全
或成本差异，并完成跨 provider 兼容性设计评审。

### Production Operations and Doctor

在 `v0.1.6` 最小基线上扩展端到端 OpenTelemetry、SLO/告警、provider 一致性、
外部审计 sink、生产 runbook、多实例观测和故障注入；候选 `doctor` 检查数据库账户
权限、timeout、TLS、schema drift 和危险 procedure，并明确控制面、snapshot、预算、
审计、数据源和成本估算失败时的降级语义。

进入条件：至少有一个持续运行的参考部署，并有真实故障或容量数据证明扩展价值。

### Write Operation Governance

候选能力包括 `idempotency_key`、乐观锁 precondition、写 preview、blast-radius cap
和绑定 principal/scope/TTL 的 approval token。无谓词写与现有 I14–I19 保证继续
fail closed，不以 approval 代替授权、成本或预算。

进入条件：有真实 Agent 写操作需求，且风险自适应 dry-run、token/profile 和审计
语义已稳定。

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

### Query Learning and Reuse

管理员审核的业务查询模板、语义 cache key 和 fingerprint 自动校准只有在 revision
语义稳定，并有高频重复查询、估算偏差或 Agent 重试数据证明收益后才立项。自动建议
只能收紧或等价改写查询，不得绕过统一授权、成本 gate 和预算。

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

- 技术与安全：MCP/provider conformance、稳定错误码覆盖率、schema drift 检出率、
  critical/high corpus 通过率、持续 fuzzing crash、p99 增量延迟、公开 benchmark
  和 policy decision 可重放比例；
- Agent 效果：entity/tool selection、argument validity、task success、
  semantic correctness、first-call success、repair rate、tool calls、schema/context/
  result token 和 policy violation attempt rate；
- 开源采用：安装与活跃生产部署、公开 case study、外部 issue/贡献者、外部维护
  Provider、Registry/客户端集成和 release cadence；
- 可信供应链：外部安全审计、signed release、SBOM/provenance 覆盖、持续 fuzzing
  时长和漏洞确认、修复与披露 SLA。

star 数只作为传播信号，不作为产品质量代理指标。

长期 graduation targets 包括至少 10 个可验证生产部署、3 个公开 case study、
5 个持续外部 contributor 和 2 个外部维护的 Provider。这些是产品成熟度目标，
不承诺由某个版本单独达成，也不能替代安全、性能和 Agent 评测证据。

## Roadmap Maintenance

- 每个 release 后复审一次优先级和 `Next` 范围；
- `Committed` 同时只保留一个版本；
- 新能力进入 `Committed` 前必须具备问题证据、明确非目标和可验证验收条件；
- 未完成事项不自动滚入下一版本，先重新评估价值和依赖；
- 已发布行为进入 release notes、配置参考或安全文档，不继续留在 roadmap。
