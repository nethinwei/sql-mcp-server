# Roadmap

当前稳定基线为 `v0.1.21`。已发布能力以
[发布说明](releases/v0.1.21.md)、[CHANGELOG](../CHANGELOG.md)、
[配置参考](configuration.md)和[安全模型](security.md)为准。

本文件只给出未发布成果的顺序和门禁：

- **Committed**：当前版本承诺；同时只保留一个版本；
- **Milestone**：跨版本的二元判据集合，标记阶段切换，不绑定日期；
- **Parallel Workstream**：非版本化的证据生产机制，与版本并行推进，
  不占 Committed 位，不阻塞发布；
- **Next**：已排序阶段，满足进入条件后才获得版本承诺；
- **Dormant**：门禁评估已完成且结论为 no-go，仅保留重开条件；
- **Later**：由证据触发，不承诺版本或时间；
- **Deferred**：与定位冲突或缺少证据，暂不规划。

详细文档：

- 长期方向：[Evidence-Gated Directions](roadmap/directions.md)
- 衡量方法：[Roadmap Metrics](roadmap/metrics.md)
- 数据库候选：[Provider Roadmap](provider-roadmap.md)

---

## 产品方向

> **The governed SQL gateway for AI agents — untrusted by default, trusted by
> explicit grant.**

项目默认把调用方视为不可信 Agent：受治理面只接受显式 Entity + 关系代数 IR，
不接受任意 SQL，在不可绕过、可解释、成本可控的边界内访问关系数据。管理面阶段
起引入**用户 + 角色 + 权限**的分级授权：只有被显式授予 `sql:execute` 权限的
用户才能使用受审计、受硬上限约束的 SQL 逃生通道；该通道是独立入口，不扩大受治理面的表达能力，
不改变未授权调用方的默认行为。路线图围绕四类结果推进：

- **Adopt**：五分钟体验、客户端接入、可部署发布和可视化配置；
- **Prove**：安全、性能和 Agent 效果可复现；
- **Operate**：拒绝、成本、预算和故障可解释，权限、配置变更与部署迁移可管理；
- **Understand**：减少“SQL 合法但业务答案错误”。

所有新入口默认 fail closed；控制面、Provider、语义层和逃生通道不得绕过统一的
身份、授权、预算与审计链；未经用户需求、测试、Eval 或 benchmark 验证的能力
不进入承诺范围。

---

## Committed

`v0.1.15` 交付 Provider Capability Model 后，按 [Next 1](#next-1--evidence-backed-sqlite)
的既定条件，Evidence-Backed SQLite 获得版本承诺（预期 `v0.1.22`；`v0.1.16` 为控制台大规模
可用性版本，`v0.1.17` 为生产稳定性与 MCP 协议版本，`v0.1.18` 为查询表达力与成本治理版本，`v0.1.19` 为其修复版本，
`v0.1.20` 为处理外部 review 的加固版本，`v0.1.21` 为发布前审查与加固版本）：先写设计文档并评审，
范围与退出验收以 [Provider Roadmap](provider-roadmap.md) 为唯一事实源。SQLite 同时承担
把 DSN 密钥解析移入 provider 的收尾（见能力模型设计）。

### 已交付

- `v0.1.21`：加固与发布前审查——PostgreSQL 只连接 11–18、继承与分区的级联归入授权、
  成本模板 `fp:v4:`、结果截断与游标、缓存总字节上限、语句超时的稳定分类；四轮独立审查
  处理约 70 项发现；Evidence-Backed SQLite 顺延到 `v0.1.22`
  （[发布说明](releases/v0.1.21.md)）；
- `v0.1.20`：加固版本——外部代码与设计 review 的全部结论逐条处理（写入的行范围检查、审计
  关闭竞态、配置校验、共享 token 强度、mTLS 证书到用户的映射、数值精度、成本与资源上限、
  配置存储约束），控制台“同步实体”；原定随 SQLite 交付的 MySQL 写超时在数据库侧强制、只读
  账号不告警写超时、服务端原因文本本地化一并交付；Evidence-Backed SQLite 顺延到 `v0.1.22`
  （[发布说明](releases/v0.1.20.md)）；
- `v0.1.19`：v0.1.18 的修复版本，quickstart 冒烟移到发布之前（[发布说明](releases/v0.1.19.md)）；
- `v0.1.18`：查询表达力与成本治理——扫描量硬上限与按量级计分、排序与 `nextCursor`、
  OR/NOT 过滤、`having` 与去重、沿已声明关系的过滤/排序/分组与 `exists`、索引信息与针对性
  提示、`execute_sql` 元数据语句；控制台数千实体下的性能（[发布说明](releases/v0.1.18.md)）；
- `v0.1.17`：生产稳定性——缺表缺列与数据源不可达按实体降级、失败版本退避、表结构运行中
  变更的识别与重新装配、事务失败即回滚与空闲超时、按角色/用户的资源限制；MCP 协议对齐——
  结构化输出、工具注解、`describe_entities` 分页与 schema 资源模板、会话 404 自动重连
  （[发布说明](releases/v0.1.17.md)）；
- `v0.1.16`：控制台在数千实体下按页加载——实体列表、授权矩阵、可见性预览分页与管理 API
  `visibilityPage`；版本历史加载更多、审核摘要折叠；镜像构建不再经 QEMU 模拟
  （[发布说明](releases/v0.1.16.md)）；
- `v0.1.15`：Provider Capability Model——按连接判定的能力与保证强度、硬限制告警与严格
  模式 `cost.requireNativeLimits`、发布前判定、生成的兼容矩阵、跨库治理等价任务；聚合
  结果列名跨库统一；管理面 4 的测试缺口收口（[发布说明](releases/v0.1.15.md)、
  [设计文档](design/capability-model.md)）；
- `v0.1.14`：[管理面 2](#管理面-2--trusted-sql-escape-hatch) 可信 SQL 逃生通道
  `execute_sql`（[发布说明](releases/v0.1.14.md)）；
- `v0.1.13`：多管理员协作的管理面与执行总线——方案驱动的批量配置、配置 MCP
  令牌、持久化扫描批次、服务端工作区与版本管理、快进发布、执行总线、配置意图与
  生效值、固定数据面管线（[发布说明](releases/v0.1.13.md)）；超出原管理面范围，
  分别落地 [L18](roadmap/directions.md#l18-governed-configuration-scaffolding) 的
  批量配置与多副本协作的最小子集；
- `v0.1.12`：元数据批量扫描与按库后台导入、发布后排空的热重载与跨快照共享服务、
  实体命名空间（[发布说明](releases/v0.1.12.md)）；
- `v0.1.11`：管理面 1、3，管理面 4、5 的核心路径，以及
  [数据源模型](design/datasource-model.md)（[发布说明](releases/v0.1.11.md)）；
- `v0.1.10`：Diagnostic Evaluation（[发布说明](releases/v0.1.10.md)、
  [正式结论](../eval/results/2026-07-12-deepseek-v4-flash-diagnostic-v5.md)）。

---

## Milestone v0.2.0 — 管理面与治理数据面收口

`v0.2.0` 是阶段切换标记：**分级授权与管理面、受治理数据面（治理语义、
跨库一致性、评测体系、采用入口）同时收口**。不绑定日期；以下二元判据全部
满足即可发布：

- [x] Diagnostic Eval 交付（`v0.1.10`）；
- [x] 管理面 1、2、3 交付（`v0.1.11`、`v0.1.14`、`v0.1.11`）；
- [ ] 管理面 4、5 收口：管理面 4 的门禁随 `v0.1.15` 全部满足；管理面 5 剩余交互判据的
  外部实测与前端供应链核实，见[管理面 5](#管理面-5--admin-console-ui)；OIDC / 通用
  OAuth2 登录经 2026-10-12 复审降为 [Later](#later)，不再是本判据的一部分；
- [x] Provider capability model 交付（`v0.1.15`）；
- [ ] Evidence-Backed SQLite 交付（`Next 1`，预期 `v0.1.22`）并通过
  conformance + workload 差分验收；
- [ ] dogfooding：至少一套真实或脱敏的支付中台工作负载经 `EVAL_DSN`
  模式运行并输出问题清单（v0.1.9 发布复审移交本 Milestone 的必要判据，
  harness 与模板已随 v0.1.9 交付）；
- [ ] 黄金 Demo 补全并发布演示材料（外部证据冲刺前两项）；
- [ ] ≥1 页 dogfooding case study 与 ≥2 个 design partner 试用观察
  记录（外部证据冲刺）；
- [ ] Result Provenance and Evidence Envelope 交付，或其进入门禁经
  发布复审判定证据不成立并书面记录（`Next 2`）；
- [ ] `v0.2.0` 发布链上 Eval 三轨（回归/负载/诊断）全绿，失败归因体系
  至少产出一次基于诊断轨或 dogfooding 的 go/no-go 结论。

---

## Parallel Workstream — 外部证据冲刺

`Next 2`、Schema drift 治理与管理面 5 交互判据的门禁依赖真实部署或真实用户
反馈，该类证据当前没有生产机制。按下文"衡量与维护"的规则（没有生产机制的
门禁项要么补建机制，要么降级），本工作流即为其证据生产机制，同时直接供给
`v0.2.0` Milestone 的 dogfooding、Demo 与 case study 判据：

- 黄金 Demo 补全：在 [quickstart](quickstart.md) 现有低权限读取、字段
  脱敏、tenant 隔离与拒绝路径之上，补齐"大查询被成本闸门拒绝后自修复"
  与"用审计 decision trace 解释一次拒绝"两个场景，使 Demo 完整覆盖
  产品差异声明；
- 3 分钟演示视频与"对比任意 SQL MCP Server"的安全架构对照材料；
- 维护者 dogfooding 部署：以真实或脱敏的支付中台工作负载（即
  [真实业务负载模型](design/business-workload-model.md)验收标准中的
  dogfooding 项）经本服务暴露给真实 Agent 任务，形成第一个不依赖外部
  响应的参考部署和一页 dogfooding case study——维护者自己的生产工作
  负载也是采用证据；
- 邀请 3–5 个 design partner（AI 数据分析、内部 BI、SaaS Agent 或
  数据库安全方向），每个只观察四件事：能否安装、能否配置第一个
  Entity、Agent 能否发现并调用、第一次失败发生在哪里；试用同时记录控制台
  交互判据（从连接数据源到首个实体可调用、≥50 张表批量导入与授权的耗时与
  首个卡点），作为[管理面 5](#管理面-5--admin-console-ui)剩余门禁的证据；
- 每个试用形成一页 case study：原问题、数据库规模、Entity 数量、Agent
  任务、治理要求、接入成本、失败与修复、最终效果。

产出（参考部署、失败记录、反馈）直接作为 `Next` 各阶段门禁与
[Graduation Targets](roadmap/metrics.md#graduation-targets) 的证据输入。

---

## 管理面阶段

2026-09-30 路线图复审将本阶段提前到 `Next` 之前：目标从"Agent 能否正确、
安全地查到数据"扩展到"运营者能否放心地授权、变更、审计、迁移和管理这套
系统"。`v0.1.11`–`v0.1.14` 交付了管理面 1–5 的核心路径；各项的设计以对应
design 文档为准，已发布行为以发布说明为准。本节只保留交付版本、退出门禁的
证据指针与剩余缺口。任何管理面路径不得绕过统一的身份、授权、预算与审计链。

### 管理面 1 — Users, Roles and Permissions

已随 `v0.1.11` 交付；设计见[授权模型](design/authorization-model.md)。

- [x] 未配置 `users` 时行为与之前等价：`TestValidateAccessAllowsLegacyColonRolesWithoutUsers`、
  `TestReloadTogglesUsers`；
- [x] 多角色行范围 OR 与租户硬边界 AND 有 adversarial 与随机化测试，登记 TM-009；
- [x] 缓存、singleflight、事务与预算不跨用户复用：用户主体键 `user:<name>` 进入
  cache/singleflight/事务作用域（TM-004、TM-006），租户并发按租户聚合
  （`TestTenantConcurrencySpansSessions`）；
- [x] 身份不能经请求参数或未受信 header 提升，吊销与在途会话语义确定（TM-010）；
- [x] 不同用户对同一实体得到不同字段与行（`x/mcpserver/e2e_users_test.go`）；
  [核心不变量](invariants.md) I7、I25、I26 同步。

剩余风险（用户 token 无过期、`X-MCP-User` 无真实反向代理 e2e）见
[威胁模型](threat-model.md) TM-010。

### 管理面 2 — Trusted SQL Escape Hatch

已随 `v0.1.14` 交付；设计与评审决议见[设计文档](design/sql-escape-hatch.md)。

- [x] 开关、用户权限、数据源许可的矩阵测试（`TestExecuteSQLMatrix`、
  `TestExecuteSQLListedAndCalledPerUser`）；
- [x] timeout、结果 cap 与预算不可关闭（`TestExecuteSQLLimitsSurviveCostDisabled`）；
- [x] 审计 sink 不可用时拒绝执行（`TestExecuteSQLRefusesWhenAuditFails`）；
- [x] 不变量 I29、威胁 TM-013 登记例外范围；README 与[安全模型](security.md)同步。

已知限制见[发布说明](releases/v0.1.14.md#已知限制)。

### 管理面 3 — Structured Config Store and Revisions

已随 `v0.1.11` 交付（原预期 `v0.1.12`）；设计见[配置存储与 Revision](design/config-store.md)。

- [x] store 与 YAML 确定性往返（`TestMigrateRoundTripsFileAndStores`、
  `TestStoredPayloadsRoundTripAsSpecs`），PostgreSQL/MySQL/OceanBase store 由
  `x/configstore` 一致性套件在 integration 中覆盖；
- [x] 篡改 payload 保留当前快照（`TestWatchStoreRejectsTamperedPayload`），没有已发布
  revision 时拒绝启动；
- [x] store 表不可被实体引用或被 introspection 收录（I27、TM-011）；
- [x] 发布复用热重载变更守卫（`TestPublishGuardsRestartAndRollback`）。

### 管理面 4 — Admin API, Login and Schema Import

核心路径（本地账号登录、GraphQL 管理 API、schema 导入、simulate）已随 `v0.1.11`
交付；设计见[管理 API](design/admin-api.md)。OIDC / 通用 OAuth2 登录未交付，
2026-10-12 复审降为 [Later](#later)。

- [x] 导入草稿默认零权限（`TestSchemaImportProposesZeroPermissionCandidates`），
  授权经工作区写入与发布留痕；
- [x] 管理 API 每个操作都有越权测试并登记 threat ID：`TestEveryOperationEnforcesItsPermissions`
  对每个查询与变更在 16 种权限组合下断言，新增操作未登记权限即失败；登记 TM-014
  （配置 MCP 见 TM-012）；
- [x] 本地账号登录的会话、CSRF、锁定、并发闸门与改密吊销测试（`x/admin/admin_test.go`）；
  OIDC 的 state/nonce/重放门禁随 OIDC 一并移至 Later；
- [x] 查询深度与复杂度上限有测试锁定（`TestSelectionDepth`、`TestComplexityLimit`）。

### 管理面 5 — Admin Console UI

核心路径已随 `v0.1.11` 交付，`v0.1.12`–`v0.1.14` 持续扩展（库 → 表导入、按规则
配置、待处理、服务端工作区、系统权限）；使用见[控制台](console.md)。

- [ ] 交互判据：新用户从连接数据源到首个实体可被 Agent 调用 ≤ 5 分钟；
  ≥50 张表的 schema 完成批量导入与授权 ≤ 15 分钟；由维护者与 ≥2 位外部试用者
  实测并记录首个卡点——尚无记录，证据由[外部证据冲刺](#parallel-workstream--外部证据冲刺)产出；
- [ ] CI 含前端 lint 与测试（`make web`：eslint、vitest、vue-tsc、构建，已满足）；
  前端依赖纳入[供应链规则](roadmap/metrics.md#可信供应链)（漏洞扫描、SBOM 覆盖
  pnpm 依赖）待核实。

---

### 管理面 · 并行 — Schema Drift and Compatibility Governance

进入门禁：真实部署出现数据库 schema 演进需求，或管理后台的重新导入需要
区分 schema 变化的影响。证据生产机制：外部证据冲刺的参考部署与管理后台的
重新导入（目录的表状态与差异、"待处理"页的 drift 已是该机制的入口）。

阶段结果：把现有启动/reload 时的 drift 检查扩展为可分级的漂移治理——schema
fingerprint、drift 分类（compatible / behavior-changing /
security-sensitive / breaking）、对 Entity/Field/Policy/Mask 的影响分析、
incompatible drift 拒绝 readiness、配置 revision 与 schema revision 绑定。
核心不是"SQL 还能执行"，而是"schema 变化是否扩大授权资源闭包或改变策略
语义"。范围见
[Schema Drift Detection and Impact Analysis](roadmap/directions.md#l15-schema-drift-detection-and-impact-analysis)。

退出门禁：security-sensitive 与 breaking drift 默认 fail closed 并可解释；
影响分析有针对每类 drift 的回归测试；与 revision 设计
（[Revision 与 Snapshot](design/revision-snapshot.md)）的绑定语义评审通过；
管理后台的重新导入 diff 复用同一分类。

---

## Next（v0.2.0 前 — 数据面与采用）

### Next 1 — Evidence-Backed SQLite

进入门禁、SQLite 首版范围和退出验收均以
[Provider Roadmap](provider-roadmap.md) 为唯一事实源；`v0.1.8` conformance
suite 与 `v0.1.9` workload 差分是任何新 Provider 的验收前置；capability
model（`v0.1.15` 已交付）为前置工程。

**进入条件已确立**（`v0.1.9` 发布复审）：架构验证 + 采用目标——SQLite 是
新 capability model 的首个新增 Provider 消费者，验证"弱成本证明、核心层
兜底"的执行模型，同时是产生真实采用证据的最低门槛入口。capability model
已交付，本阶段获得版本承诺（预期 `v0.1.22`，见 [Committed](#committed)）。SQLite 驱动已随管理面 3 的
配置存储引入，Provider 复用同一驱动。

阶段结果：一个受现有 IR 和统一 engine 约束的窄 SQLite Provider，通过
conformance corpus 与 workload 差分双重验收。

---

### Next 2 — Result Provenance and Evidence Envelope

进入门禁：Eval 失败归因、真实用户反馈或公开对照需求证明"Agent 给出的业务
结论无法追溯到数据、配置版本和一次具体执行"是信任或采用障碍。证据生产
机制：外部证据冲刺的 design partner 反馈与 Eval 真实负载轨（v4）失败
归因。本项是 `v0.2.0` Milestone 判据（交付，或书面记录证据不成立）。

阶段结果：每次工具结果附带机器可读 evidence envelope（逻辑查询与物理计划
fingerprint、snapshot/schema revision、涉及实体与字段、已应用策略与 mask
决策摘要、数据新鲜度与时区、截断/采样/近似标记、输出列 lineage、
decisionId 与可引用 citation handle），直接服务 Understand 结果——减少
"SQL 合法但业务答案错误"并使答案可解释、可复现。范围与约束见
[Result Provenance and Evidence Envelope](roadmap/directions.md#l14-result-provenance-and-evidence-envelope)。

退出门禁：envelope 字段进入版本化工具契约并有 golden 测试；envelope 内容
服从 RBAC/mask 可见性（不得成为新的侧信道）；至少一个 Eval 任务或 Demo
场景证明 Agent 能引用 envelope 解释答案来源。

---

## Dormant — Eval-Driven Agent Improvement

`v0.1.7` 校准已完成并给出结论
（[2026-07-12 v3](../eval/results/2026-07-12-deepseek-v4-flash-v3.md)，
三轮 31/32、32/32、31/32）：
[Semantic Metadata](roadmap/directions.md#foundation-semantic-metadata)、
[Catalog Discovery](roadmap/directions.md#l2-catalog-discovery) 和
[Governed Query Expressiveness](roadmap/directions.md#l12-governed-query-expressiveness)
均 **no-go**——定向任务全部通过，失败无一可归因于对应缺失；唯一证据支持
项（无谓词聚合拒绝的 hint 收紧）已随 `v0.1.8` 交付。

`v0.1.9` 真实负载轨重评估
（[2026-07-12 v4](../eval/results/2026-07-12-deepseek-v4-flash-workload-v4.md)，
三轮 21/21）：Semantic Metadata 与 Governed Query Expressiveness 维持
**no-go**（grain/时间/单位/版本化语义任务零失败；多跳靠分解完成）；
Catalog Discovery 转**继续观察**——45 实体 catalog 下实体选择零失败，
但单任务 prompt token 较 v3 上升 2.6 倍，成本信号随 schema 规模增长。
两轮结论均在合成 fixture 与单模型下成立，不外推为"所有模型无短板"。

`v0.1.10` 诊断轨重评估
（[2026-07-12 v5](../eval/results/2026-07-12-deepseek-v4-flash-diagnostic-v5.md)，
三轮 41/48、43/48、43/48）：Semantic Metadata 与 Governed Query
Expressiveness 维持 **no-go**，Catalog Discovery 继续观察；三项澄清任务和
因果 unsupported 任务稳定失败，**Tool Contract 转 go（设计评估）**。
单客户聚合的推断风险限定同样稳定失败，但尚无受监管部署需求，不升级 L16。

重开条件：dogfooding、design partner、更弱模型或更大 catalog scale 产生新的
可测量失败；Tool Contract 的设计评估不得自动扩大核心能力，须以 v5 稳定失败
做前后对照。无法解释答案来源时再评估 Evidence Envelope。

Tool Contract 设计评估自 `v0.1.10` 转 go 后尚未执行；2026-10-12 复审将其与
capability model 解耦（此前是其进入门禁之一），在本节跟踪，不占
Committed 位。证据生产机制是诊断轨：执行时以 v5 的三项澄清任务与因果 unsupported
任务为前后对照，没有可测量改善则维持现状。

---

## Later

[Semantic Metadata](roadmap/directions.md#foundation-semantic-metadata)、
[完整 Public Eval](roadmap/metrics.md#agent-与业务效果)、
[推断与策略组合安全](roadmap/directions.md#l16-data-inference-and-policy-composition-safety)
（受监管部署前必须完成）、
[MCP 协议一致性与契约稳定](roadmap/directions.md#l17-mcp-protocol-conformance-and-contract-stability)、
[受治理配置脚手架](roadmap/directions.md#l18-governed-configuration-scaffolding)
中 introspection 导入与方案批量配置以外的部分（dbt manifest、数据字典导入等）、
管理后台的 OIDC / 通用 OAuth2 人员登录（2026-10-12 复审由管理面 4 降入；触发证据：
真实部署要求接入企业 IdP/SSO，见
[L7](roadmap/directions.md#l7-enterprise-identity-and-scale)）、
第二种架构验证型 Provider（SQL Server 或 ClickHouse）及其他战略方向均按证据
升级。具体范围、触发证据和跨阶段非目标见
[Evidence-Gated Directions](roadmap/directions.md)；Provider 能力模型与候选顺序
以 [Provider Roadmap](provider-roadmap.md) 为唯一事实源。

---

## 衡量与维护

技术可信度、Agent 效果、采用生态、供应链和公开数字规则见
[Roadmap Metrics](roadmap/metrics.md)。

- 每个 release 后复审阶段证据和 `Next` 顺序；
- 复审时为每个 `Next` 项记录证据缺口与其生产机制（如 Eval、参考部署、
  Demo 反馈渠道）；没有生产机制的门禁项要么补建机制，要么降级 `Later`；
- 新能力进入 `Committed` 前必须有问题证据、明确非目标和二元验收条件；
- 没有证据支持的版本核心时，`Committed` 允许为空——空 Committed 是
  evidence-gated 的正确结果，不为发版而立项；
- Later 项不因时间流逝自动升级；
- 未完成事项不自动滚入下一版本；
- 已发布行为移入 release notes 或对应事实文档。
