# Roadmap

当前稳定基线为 `v0.1.3`。已实现能力、默认值和已知边界分别以
[v0.1.3 发布说明](releases/v0.1.3.md)、[配置参考](configuration.md) 和
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

## Next — v0.1.4 Security Assurance Baseline

该版本在 `v0.1.3` 发布后进入范围锁定。

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

## Next — Provider Expansion Track

该方向扩展受控数据访问覆盖面，不改变“不接受任意 SQL”的产品边界。以下优先级是
技术与市场依赖顺序，不占用已保留的 `v0.1.4` Security Assurance 或 `v0.2.0`
Control Plane 版本号；具体 provider 只有在满足进入条件后才绑定版本。

### 选择原则

- 不以 provider 数量为目标；每个新增 provider 必须带来明确用户覆盖或验证一种
  新执行模型；
- 兼容协议不等于兼容安全语义；MySQL/PostgreSQL 兼容目标使用共享 codegen，
  但必须拥有独立 capability profile、EXPLAIN/timeout/transaction 测试；
- 第一阶段只实现能够被现有 IR、授权、预算和结果限制证明的窄能力；
- 外部 catalog、文件、网络、extension、table function 和 arbitrary setting
  默认拒绝，只有管理员显式注册且通过统一 engine 时才能开放；
- provider 无法可靠执行的硬限制必须 fail closed 或明确标为未保证。

### 优先级与实施策略

#### Adoption Accelerators

1. **SQLite**：零服务 demo、本地 Agent、CI 和嵌入式场景。首版覆盖 read、
   aggregate 和受限 write；默认禁止 extension loading、`ATTACH DATABASE`、
   危险 `PRAGMA` 与配置根目录外的数据库文件。重点验证动态类型、弱 schema、
   并发写、`RETURNING` 版本和内存数据库生命周期。
2. **MariaDB compatibility certification**：复用 MySQL codegen，增加独立
   integration、支持版本和 capability 差异，不把它包装成全新方言。
3. **TiDB compatibility certification**：复用 MySQL codegen，独立验证 TiDB
   EXPLAIN、statement timeout、分布式事务、affected rows、locking read 与隔离
   级别；不能仅标注“兼容 MySQL”。

这组候选实现成本相对可控，可在 `v0.1.4` 之后作为独立小版本评估，但不自动进入
任何 `v0.1.x` 承诺。

#### Architecture-Proving Providers

1. **Microsoft SQL Server / Azure SQL**：最高优先级的新方言。目标为 SQL Server
   2019+ 和 Azure SQL Database，共用一个 provider；Azure/Entra 身份作为独立
   credential 能力。设计验证必须覆盖 `TOP`/`OFFSET FETCH`、参数、`OUTPUT`、
   identifier、SHOWPLAN、timeout、isolation、stored procedure、result set 和
   `SESSION_CONTEXT`，且不能污染核心 IR。
2. **ClickHouse**：最高优先级的分析型 provider。首版只做 projection、filter、
   aggregate、group by、order by、limit 和审慎分页，不做 mutation、DDL、
   dictionary、external table function、arbitrary setting 或任意函数。预算需要
   同时覆盖 read rows/bytes、result rows/bytes、memory、execution time、shard 和
   concurrency，并优先装配数据库原生硬限制。

SQL Server 与 ClickHouse 分别验证企业 OLTP 和现代 OLAP。它们不与 Control Plane
捆绑；进入 Committed 前必须先通过 dialect/provider 设计评审。

#### China Analytics

**StarRocks / Apache Doris 二选一先做**，不并行承诺。两者可以复用部分 MySQL
协议基础，但 EXPLAIN、workload management、timeout、resource group、catalog
和 external table 风险必须独立处理。选择依据优先是可持续 contributor、固定
测试环境和真实用户，而不是方言相似度：

- StarRocks 偏实时分析、serving、多表关联和现代湖仓；
- Doris 偏国内开源生态、BI/报表和易部署体验。

#### Cloud Warehouses

1. **BigQuery**：优先于 Snowflake 进行设计验证。利用 dry run 和 bytes processed
   建立 per-query/session/daily 扫描预算、partition filter rewrite hint 与执行前
   拒绝；provider 生命周期需要支持 submit、poll、cancel、fetch result。
2. **Snowflake**：在 account/warehouse/database/schema/object/role 资源层次和
   credits 成本模型设计完成后进入。候选能力包括 warehouse allowlist、query tag、
   role hierarchy、secure view、mask/row access policy 与短期身份。

BigQuery 与 Snowflake 都不是传统 `database/sql Rows` 的简单方言适配，不能在
异步 job、取消、计费和身份模型进入 provider 契约前实现。

#### Later Provider Candidates

优先顺序暂定：

1. **DuckDB**：本地 OLAP；默认禁止 `read_csv`/`read_parquet`/HTTP/S3、
   `INSTALL`、`LOAD`、`ATTACH` 和任意 table function；
2. **Amazon Redshift**：PostgreSQL 分支 provider，独立处理 WLM、IAM、
   distribution/sort key、Spectrum 和 serverless 差异；
3. **CockroachDB**：PostgreSQL 分支 provider，重点验证 serializable retry、
   transaction restart、regional table 和分布式 EXPLAIN；
4. **Trino limited mode**：仅允许完整绑定的单 catalog entity，禁止跨 catalog、
   arbitrary connector 和通用联邦 join；
5. **Oracle**：只有真实企业用户、外部维护者、测试环境或赞助出现后立项。

Databricks SQL、广泛国产数据库铺开和“接入几十个 JDBC/ADBC 数据源”不进入近期
计划；它们需要单独问题证据，不能借统一驱动绕过 provider 安全契约。

### Provider Capability Model

新增 provider 前先扩展 capability model，至少区分：

- 数据能力：read、aggregate、create、update、delete、procedure；
- 生命周期：transaction、savepoint、async job、poll、cancel、retry；
- 成本证明：EXPLAIN estimate、scan rows/bytes、result rows/bytes、memory、
  execution time、monetary cost；
- 原生执行边界：row/byte/result/memory/timeout cap；
- 安全与身份：native RLS、short-lived credential、session identity、external
  access；
- 语义状态：已装配并验证、依赖 DBA/云配置、仅 capability 声明、未支持。

capability 不能退化为 `supportsSQL = true`，也不能因数据库“提供某项能力”就宣称
本服务已装配硬保证。安全规则必须根据有效 capability fail closed。

### 进入条件与验收

任何 provider/certification 进入 Committed 前必须满足：

- 至少一个真实用户场景、外部 contributor 或明确的采用/架构验证目标；
- 固定版本的公开 CI 环境，或有维护者承担可持续测试基础设施；
- read/aggregate/write/procedure 中每个宣称均有 integration 或明确标为未验证；
- EXPLAIN、timeout、transaction、错误映射和 schema introspection 有独立证据；
- 外部文件、网络、catalog、extension 和云计费边界有 threat analysis；
- 兼容矩阵、支持版本、迁移说明和至少一条 allow/deny e2e 同时交付。

如果只能再立项三个新 provider，优先评估 **SQL Server、ClickHouse、SQLite**：
它们分别补齐企业用户、分析成本治理和零门槛采用。

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
- 在核心语义稳定前一次性扩展大量数据库或绕过 provider capability 契约；
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
