# Provider Roadmap

本文件描述未发布 Provider 与兼容性认证的候选顺序、设计约束和进入条件。当前已发布
能力以 [Provider 兼容矩阵](provider-compatibility.md) 和
[支持版本](supported-versions.md) 为准。

Provider 扩展不改变“不接受任意 SQL”的产品边界，不作为 `v0.2.0` Control Plane
Foundation 的前置项，也不自动占用任何版本号。每个候选只有满足进入条件并被提升为
主 [Roadmap](roadmap.md) 中的 `Committed` 后，才构成发布承诺。

---

## 选择原则

- 不以 Provider 数量为目标；每个新增 Provider 必须带来明确用户覆盖或验证一种
  新执行模型；
- 兼容协议不等于兼容安全语义；MySQL/PostgreSQL 兼容目标可以共享 codegen，但必须
  拥有独立 capability profile、EXPLAIN、timeout 和 transaction 测试；
- 第一阶段只实现能够被现有 IR、授权、预算和结果限制证明的窄能力；
- 外部 catalog、文件、网络、extension、table function 和 arbitrary setting
  默认拒绝，只有管理员显式注册且通过统一 engine 时才能开放；
- Provider 无法可靠执行的硬限制必须 fail closed 或明确标为未保证；
- 一次只承诺一个新方言；兼容性认证可以独立小版本交付，但不能代替 Provider 证据。

---

## 优先级与实施策略

### Adoption Accelerators

1. **SQLite**：零服务 demo、本地 Agent、CI 和嵌入式场景。首版覆盖 read、
   aggregate 和受限 write；默认禁止 extension loading、`ATTACH DATABASE`、
   危险 `PRAGMA` 与配置根目录外的数据库文件。重点验证动态类型、弱 schema、
   并发写、`RETURNING` 版本和内存数据库生命周期。
2. **MariaDB compatibility certification**：复用 MySQL codegen，增加独立
   integration、支持版本和 capability 差异，不把它包装成全新方言。
3. **TiDB compatibility certification**：复用 MySQL codegen，独立验证 TiDB
   EXPLAIN、statement timeout、分布式事务、affected rows、locking read 与隔离
   级别；不能仅标注“兼容 MySQL”。

这组候选实现成本相对可控，但只有出现真实采用证据、可持续测试环境或外部维护者后，
才进入版本承诺。

### Architecture-Proving Providers

1. **Microsoft SQL Server / Azure SQL**：最高优先级的新 OLTP 方言候选。目标为
   SQL Server 2019+ 和 Azure SQL Database，共用一个 Provider；Azure/Entra 身份
   作为独立 credential 能力。设计验证必须覆盖 `TOP`/`OFFSET FETCH`、参数、
   `OUTPUT`、identifier、SHOWPLAN、timeout、isolation、stored procedure、
   result set 和 `SESSION_CONTEXT`，且不能污染核心 IR。
2. **ClickHouse**：最高优先级的分析型 Provider 候选。首版只做 projection、
   filter、aggregate、group by、order by、limit 和审慎分页，不做 mutation、DDL、
   dictionary、external table function、arbitrary setting 或任意函数。预算需要
   同时覆盖 read rows/bytes、result rows/bytes、memory、execution time、shard 和
   concurrency，并优先装配数据库原生硬限制。

SQL Server 与 ClickHouse 分别验证企业 OLTP 和现代 OLAP。它们不与 Control Plane
捆绑；进入 `Committed` 前必须先通过 dialect/provider 设计评审。

### China Analytics

**StarRocks / Apache Doris 二选一先做**，不并行承诺。两者可以复用部分 MySQL
协议基础，但 EXPLAIN、workload management、timeout、resource group、catalog
和 external table 风险必须独立处理。选择依据优先是可持续 contributor、固定
测试环境和真实用户，而不是方言相似度：

- StarRocks 偏实时分析、serving、多表关联和现代湖仓；
- Doris 偏国内开源生态、BI/报表和易部署体验。

### Cloud Warehouses

1. **BigQuery**：优先于 Snowflake 进行设计验证。利用 dry run 和 bytes processed
   建立 per-query/session/daily 扫描预算、partition filter rewrite hint 与执行前
   拒绝；Provider 生命周期需要支持 submit、poll、cancel、fetch result。
2. **Snowflake**：在 account/warehouse/database/schema/object/role 资源层次和
   credits 成本模型设计完成后进入。候选能力包括 warehouse allowlist、query tag、
   role hierarchy、secure view、mask/row access policy 与短期身份。

BigQuery 与 Snowflake 都不是传统 `database/sql Rows` 的简单方言适配，不能在
异步 job、取消、计费和身份模型进入 Provider 契约前实现。

### Later Candidates

优先顺序暂定：

1. **DuckDB**：本地 OLAP；默认禁止 `read_csv`/`read_parquet`/HTTP/S3、
   `INSTALL`、`LOAD`、`ATTACH` 和任意 table function；
2. **Amazon Redshift**：PostgreSQL 分支 Provider，独立处理 WLM、IAM、
   distribution/sort key、Spectrum 和 serverless 差异；
3. **CockroachDB**：PostgreSQL 分支 Provider，重点验证 serializable retry、
   transaction restart、regional table 和分布式 EXPLAIN；
4. **Trino limited mode**：仅允许完整绑定的单 catalog entity，禁止跨 catalog、
   arbitrary connector 和通用联邦 join；
5. **Oracle**：只有真实企业用户、外部维护者、测试环境或赞助出现后立项。

Databricks SQL、广泛国产数据库铺开和“接入几十个 JDBC/ADBC 数据源”不进入近期
计划；它们需要单独问题证据，不能借统一驱动绕过 Provider 安全契约。

---

## Provider Capability Model

> **路线图顺序**（`v0.1.10` 重规划）：Capability Model 为 `Next 1`（预期
> `v0.1.11`），在 Diagnostic Eval（`v0.1.10` Committed）之后交付；不阻塞
> 评测体系升级。SQLite 为 `Next 2`（预期 `v0.1.12`）。

新增 Provider 前先扩展 capability model。当前实现
（`core/dialect.Capabilities`）是平铺 bool，无法表达保证强度，且成本闸门装配
直接消费这些 bool；将其重构为“范围 + 强度 + 证据”模型是任何新 Provider 或
兼容性认证立项的**前置工程项**，应作为独立变更先行交付。每项 capability 必须
分别表达能力范围、保证强度和验证证据，不能用单个 bool 混合表示。保证强度
统一为：

- `unsupported`：服务不提供该能力；
- `best_effort`：服务会尝试使用，但不能据此声明硬安全或资源保证；
- `enforced`：服务已装配可测试的强制机制，失败时不会静默降级。

capability 范围至少区分：

- 数据能力：read、aggregate、create、update、delete、procedure；
- 生命周期：transaction、savepoint、async job、poll、cancel、retry；
- 成本证明：EXPLAIN estimate、scan rows/bytes、result rows/bytes、memory、
  execution time、monetary cost；
- 原生执行边界：row/byte/result/memory/timeout cap；
- 安全与身份：native RLS、short-lived credential、session identity、external
  access；
- 证据状态：已装配并验证、依赖 DBA/云配置、仅 capability 声明、未独立验证。

capability 不能退化为 `supportsSQL = true`，也不能因数据库“提供某项能力”就宣称
本服务已装配硬保证。`best_effort` 不能满足硬限制；缺少 `enforced` 能力时，必须由
核心层提供等价强制或 fail closed。能力模型升级本身不构成新增 Provider 的承诺。

在保证强度之外，L13（Provider Optimization Extensibility，见
[Evidence-Gated Directions](roadmap/directions.md)）激活后需要增加与之正交的
**实现方式**维度：`native`（数据库原生）、`emulated`（核心层或 Provider 模拟）、
`restricted`（受限子集）、`unsupported`。`emulated`/`restricted` 能力必须声明
语义差异、原子性限制、性能影响、支持版本、成本可见性与失败模式。保证强度回答
“能保证什么”，实现方式回答“怎么实现”；该维度在 L13 立项前仅为预留，不构成
当前承诺。详细设计见 [IR Evolution](design/ir-evolution.md)。

---

## 进入条件与验收

任何 Provider 或兼容性认证进入 `Committed` 前必须满足：

- 至少一个真实用户场景、外部 contributor 或明确的采用/架构验证目标；
- 固定版本的公开 CI 环境，或有维护者承担可持续测试基础设施；
- read/aggregate/write/procedure 中每个宣称均有 integration 或明确标为未验证；
- EXPLAIN、timeout、transaction、错误映射和 schema introspection 有独立证据；
- 每项安全与成本 capability 的保证强度和证据状态在兼容矩阵中可区分；
- 外部文件、网络、catalog、extension 和云计费边界有 threat analysis；
- 兼容矩阵、支持版本、迁移说明和至少一条 allow/deny e2e 同时交付。

如果只能再立项三个新 Provider，优先评估 **SQL Server、ClickHouse、SQLite**：
它们分别补齐企业用户、分析成本治理和零门槛采用。实际顺序仍由用户证据、维护能力
和测试基础设施决定。

## 维护规则

- 本文只保存候选与进入条件，不记录已发布能力；
- 候选进入 `Committed` 后，在主 Roadmap 中定义该版本的目标、验收和非目标；
- 发布后将结果移入 Provider 兼容矩阵、支持版本和 release notes；
- 未完成候选不自动继承优先级，每次主版本发布后重新评估。
