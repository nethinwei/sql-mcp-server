# Roadmap

当前稳定基线为 `v0.1.2`。已实现能力、默认值和已知边界分别以
[v0.1.2 发布说明](releases/v0.1.2.md)、[配置参考](configuration.md) 和
[安全模型](security.md) 为准。

本文件只描述未发布工作的优先级、目标和验收条件：

- **Committed**：当前版本承诺，范围已收敛；
- **Next**：下一阶段候选，完成设计验证后才进入版本承诺；
- **Later**：战略方向，不承诺版本或交付时间；
- `docs/releases/` 只记录已发布版本，不预建未来 release 文档；
- 协议、数据模型和接口细节在立项后进入独立设计文档，不在 roadmap 中维护。

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

## Committed — v0.1.3 Public Adoption Baseline

### 目标

让新用户无需自行构建发布流程，即可安装、启动、验证安全边界并连接主流 MCP
客户端。

### 范围

#### 可验证的发布产物

- GitHub Release；
- Linux、macOS、Windows 的 amd64 / arm64 二进制；
- Docker / OCI image；
- SHA-256 checksums、SBOM 和签名；
- 自动化 release workflow；
- 明确支持的 Go、MCP 和数据库版本范围。

SLSA provenance 在现有 release workflow 能稳定生成时纳入；Homebrew tap 等额外
分发渠道不作为本版本阻塞项。

#### MCP 发现

- 提供符合官方格式的 `server.json`；
- 声明 transport、authentication、provider 和安全能力；
- 发布到官方 MCP Registry；
- 在 CI 中校验 Registry metadata。

#### 5 分钟体验

提供可直接运行的 `docker compose up`，包含：

- PostgreSQL 示例数据；
- 默认安全策略；
- 低权限 reader 与 tenant-scoped reader；
- 正常调用和拒绝调用示例；
- MCP Inspector、Claude Desktop、Cursor、VS Code 的连接配置。

#### Provider 能力说明

发布 PostgreSQL、MySQL、OceanBase 兼容矩阵，至少区分：

- read、write、aggregate、procedure；
- EXPLAIN 估算精度；
- EXPLAIN ANALYZE feedback；
- connection-level timeout；
- provider 原生资源限制；
- 已知安全语义差异。

### 验收条件

- 发布产物由 CI 从 tag 可重复生成，checksum 和签名可验证；
- 全新环境只依赖公开文档即可在 5 分钟左右完成首次安全读取；
- quickstart 同时演示允许与拒绝路径；
- `server.json` 通过官方校验并可被 Registry 发现；
- 兼容矩阵中的能力均有测试或安全文档依据，不使用未经验证的宣称。

### 非目标

- 控制面、管理 UI、Helm chart；
- 新增数据库 provider；
- 完整企业 OAuth；
- 以生产部署数或 star 数作为发布门槛。

---

## Next — v0.1.4 Security Assurance Baseline

该版本在 `v0.1.3` 发布后锁定范围。

### 目标

把现有安全说明升级为可审阅、可回归、可持续验证的安全证明基线。

### 候选范围

#### Threat Model

新增 `docs/threat-model.md`，覆盖资产、信任边界、攻击者、安全假设、攻击入口、
防御措施、剩余风险和非保证范围。

至少区分恶意 Agent、被 prompt injection 操纵的 Agent、恶意 MCP client、
token 泄露者、被攻陷的 trusted proxy、恶意 procedure、错误配置管理员和
数据库侧高权限用户。

#### Adversarial Security Corpus

建立公开 corpus，优先覆盖：

- identifier injection 与 schema confusion；
- 隐藏字段和 mask 字段侧信道；
- row policy 绕过；
- subject、session 与 transaction token 混淆或重放；
- trusted proxy header spoofing；
- cache 跨租户污染；
- procedure 和 reload 竞态；
- oversized payload、深层 filter / expand、超大 IN 列表。

指标按风险类别和覆盖范围报告，不预设用例数量。

#### Fuzzing 与属性测试

首批聚焦 MCP payload、IR validator、codegen 和 transaction state machine。
持续验证以下核心属性：

- 用户值不会进入 SQL 标识符位置；
- 输出字段是有效授权字段集合的子集；
- row policy 不会被用户 filter 弱化；
- 失败路径不会遗留开放事务。

### 验收条件

- 每个 threat 都映射到控制措施、验证方式或明确的剩余风险；
- critical/high corpus 在 CI 中全部通过；
- fuzz target 可在 CI 和本地复现，已知 crash 均有回归用例；
- README 与安全文档中的宣称不超过测试能够证明的范围。

### 非目标

- 完整数据库权限 doctor；
- durable / fail-closed audit；
- 外部安全审计；
- 通用 ABAC 策略语言。

---

## Next — v0.2.0 Control Plane Foundation

该版本只有在数据模型和快照失败语义通过设计评审后才锁定范围。

### 目标

提供无 UI 的最小控制面，使配置能够持久化、版本化、发布、回滚并安全分发到数据面。

### 候选范围

- 数据源、实体、字段和现有策略的持久化管理 API；
- draft → publish 的 immutable revision；
- 已发布 snapshot 的校验、加载和回滚；
- token / capability profile 与 expiry、revocation、audience；
- principal、actor、subject、tenant 的明确区分；
- 机器可读的拒绝原因和 decision ID；
- `validate`、`diff`、`simulate`、`export`；
- YAML 继续作为 bootstrap 和可移植配置格式；
- 控制面操作审计。

### 验收条件

- 重启后 published revision 和 token 状态保持一致；
- 无效、损坏或不兼容 snapshot 被拒绝加载；
- token 撤销传播延迟有明确上限并可测试；
- 在途请求与新 revision 的一致性语义明确；
- 用户可从 YAML 导入，并导出确定性、可审阅的配置；
- 数据面在控制面短时不可用时的行为有测试和文档。

### 非目标

- 管理 UI；
- 通用 typed ABAC；
- 多实例强一致；
- 完整 GitOps promotion workflow；
- 企业 OAuth、DPoP、WIF；
- durable audit。

---

## Next Priorities

以下方向在 `v0.2.0` 之后按用户证据和技术依赖排序，不预先绑定版本号。

### Agent Effectiveness

- 建立可重复的任务集，覆盖查询、分页、聚合、多租户、拒绝和修复；
- 测量 task success、tool selection、argument validity、first-call success、
  repair rate、tool calls 和 token footprint；
- 基于数据优化 tool surface 和 schema discovery；
- 定义稳定、机器可读的错误分类和安全 rewrite hints。

进入条件：至少有两个真实客户端集成或稳定的模型测试环境。

### Operational Baseline

- metrics、结构化日志、trace 和 readiness；
- 可复现的 data-plane overhead benchmark；
- provider 一致性与 MCP conformance；
- 审计 sink、轮转和 dropped-event 告警；
- Helm chart、运维 runbook 和故障注入测试。

进入条件：发布产物稳定，且至少有一个持续运行的参考部署。

### Management UI

- 数据源、实体、策略和 token/profile 管理；
- 发布、回滚、模拟和 decision trace；
- Admin IdP 与 MCP client identity 明确隔离。

进入条件：控制面 API 经至少一个版本稳定，且用户研究证明 UI 是主要采用障碍。

---

## Later — Strategic Directions

以下是长期设计方向，不是发布承诺。只有出现明确生产需求、维护者容量和可验证
验收标准后，才提升为 `Next`。

### Enterprise Identity and Scale

- 标准 MCP OAuth 2.1 与 metadata、resource indicator、PKCE；
- issuer、audience、scope、revocation / introspection；
- principal、actor、subject 与 delegation chain；
- 多实例 snapshot distribution、shared budget、persistent session；
- signed snapshot、过期与防回退语义；
- backup、restore 和灾难恢复；
- 短期数据库凭证与 workload identity。

DPoP、WIF、Enterprise-Managed Authorization 和各云数据库身份分别独立评估，
不作为单一版本的捆绑交付。

### Data Governance

- 数据分类标签和 classification-based policy；
- sensitive egress budget；
- 聚合隐私阈值；
- schema drift detection；
- tool contract versioning；
- approval workflow 与 lineage metadata。

原则保持不变：新增数据库字段不自动暴露；删除或变更已暴露字段时 fail closed。

### Durable Audit

- `best_effort`、`durable`、`fail_closed` 三种明确语义；
- sequence、hash chain、config revision、decision ID；
- external sink ACK、重放和验证工具。

### Constrained Extensibility

候选扩展点包括 `IdentityProvider`、`SecretProvider`、`AuditSink`、`BudgetStore`、
`SnapshotStore`、`PolicyAttributeProvider`、`MaskingFunction`、`Provider` 和
`TelemetryHook`。

任何扩展必须经过统一 engine，不允许直接注册可执行任意 SQL 的 MCP tool。
优先使用 compile-time interface 或 sidecar RPC；实现机制在真实扩展需求出现后决定。

---

## Explicitly Deferred

以下能力与当前安全定位冲突或缺少需求证据，暂不规划：

- 自然语言转任意 SQL；
- 通用 SQL parser + sanitizer；
- 通用 join / union 和跨数据源 federated query；
- 自动写入任意新表；
- 自定义脚本策略或内嵌大模型；
- 自建完整 IAM 或 secret manager；
- 在核心语义稳定前扩展到大量数据库；
- GraphQL / OpenAPI；
- 复杂 BI 可视化；
- 数据库管理和调优工具全集；
- 差分隐私等尚无明确场景的高级能力。

---

## Project Health Metrics

这些指标用于判断方向和排序，不作为单个版本的硬性交付门槛。

### 产品与安全

- MCP conformance 和跨 provider 一致性；
- critical/high security corpus 通过率；
- fuzzing 运行时长、有效 crash 和回归覆盖；
- p50 / p95 / p99 增量延迟与资源开销；
- policy decision 可解释和可重放比例。

### Agent 效果

- task success rate；
- first-call success 和 repair rate；
- median tool calls；
- schema / tool token footprint；
- policy violation attempt rate。

### 开源采用

- 安装和活跃部署趋势；
- 外部 issue、贡献者和集成数量；
- 生产案例和独立维护的扩展；
- release cadence、issue 首次响应和漏洞响应时间。

star 数只作为传播信号，不作为产品质量代理指标。

---

## Roadmap Maintenance

- 每个 release 后复审一次优先级和 `Next` 范围；
- `Committed` 同时只保留一个版本；
- 新能力进入 `Committed` 前必须具备问题证据、明确非目标和可验证验收条件；
- 未完成事项不自动滚入下一版本，先重新评估价值和依赖；
- 已发布行为进入 release notes、配置参考或安全文档，不继续留在 roadmap。
