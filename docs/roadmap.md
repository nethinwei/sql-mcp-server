# Roadmap

当前稳定基线为 `v0.1.10`。已发布能力以
[发布说明](releases/v0.1.10.md)、[CHANGELOG](../CHANGELOG.md)、
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

`v0.1.11` — [管理面 1 · Users, Roles and Permissions](#管理面-1--users-roles-and-permissions)。
问题证据、非目标与退出门禁见该节；2026-09-30 路线图复审将管理面阶段提前到
`Next` 之前。

`v0.1.10` 已完成 Diagnostic Evaluation，成果与退出门禁见
[发布说明](releases/v0.1.10.md)和
[正式结论](../eval/results/2026-07-12-deepseek-v4-flash-diagnostic-v5.md)；
其发布复审将 Tool Contract 升为设计评估，该设计评估随 `Next` 阶段处理。

---

## Milestone v0.2.0 — 管理面与治理数据面收口

`v0.2.0` 是阶段切换标记：**分级授权与管理面、受治理数据面（治理语义、
跨库一致性、评测体系、采用入口）同时收口**。不绑定日期；以下二元判据全部
满足即可发布：

- [x] Diagnostic Eval 交付（`v0.1.10`）；
- [ ] 管理面 1–5 交付（预期 `v0.1.11`–`v0.1.15`）：用户/角色/权限、
  `execute_sql` 逃生通道、配置存储与 revision、管理 API 与 schema 导入、
  管理后台 UI；
- [ ] Provider capability model 交付（`Next 1`，预期 `v0.1.16`）；
- [ ] Evidence-Backed SQLite 交付（`Next 2`，预期 `v0.1.17`）并通过
  conformance + workload 差分验收；
- [ ] dogfooding：至少一套真实或脱敏的支付中台工作负载经 `EVAL_DSN`
  模式运行并输出问题清单（v0.1.9 发布复审移交本 Milestone 的必要判据，
  harness 与模板已随 v0.1.9 交付）；
- [ ] 黄金 Demo 补全并发布演示材料（外部证据冲刺前两项）；
- [ ] ≥1 页 dogfooding case study 与 ≥2 个 design partner 试用观察
  记录（外部证据冲刺）；
- [ ] Result Provenance and Evidence Envelope 交付，或其进入门禁经
  发布复审判定证据不成立并书面记录（`Next 3`）；
- [ ] `v0.2.0` 发布链上 Eval 三轨（回归/负载/诊断）全绿，失败归因体系
  至少产出一次基于诊断轨或 dogfooding 的 go/no-go 结论。

---

## Parallel Workstream — 外部证据冲刺

`Next 3` 与 Schema drift 治理的进入门禁依赖真实部署或真实用户反馈，该类证据
当前没有生产机制。按下文"衡量与维护"的规则（没有生产机制的门禁项要么
补建机制，要么降级），本工作流即为其证据生产机制，同时直接供给
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
  Entity、Agent 能否发现并调用、第一次失败发生在哪里；
- 每个试用形成一页 case study：原问题、数据库规模、Entity 数量、Agent
  任务、治理要求、接入成本、失败与修复、最终效果。

产出（参考部署、失败记录、反馈）直接作为 `Next` 各阶段门禁与
[Graduation Targets](roadmap/metrics.md#graduation-targets) 的证据输入。

---

## 管理面阶段（v0.1.11 起，先于 Next）

2026-09-30 路线图复审将本阶段提前到 `Next` 之前：目标从"Agent 能否正确、
安全地查到数据"扩展到"运营者能否放心地授权、变更、审计、迁移和管理这套
系统"，并让不同的人与 Agent 在同一服务上获得与其身份相符的能力。

**进入条件已确立**（维护者需求）：

- 当前只有共享 bearer token 与进程级 `server.role`，服务没有身份分级，只能
  把所有调用方整体按不可信处理；可信操作者因此只能绕过本服务直连数据库，
  这类访问没有统一审计、预算和 decision trace；
- 业务要求不同的人/角色看到不同的实体、字段和行。现有 `roles`/`fieldACL`/
  `rowPolicies` 能按单一 role 表达差异，但没有"用户"概念，也无法让一个人
  同时拥有多个角色或单独授权；
- 接入需要先读 DDL 再手写 Entity/Field/Relation/Policy，而服务本身已能
  introspect 表、列和主键；配置成本是采用的主要障碍；
- 配置只能以 YAML 文件存在，用户、角色、revision 等结构化状态无处持久化，
  部署迁移依赖手工搬运文件。

阶段按依赖顺序推进：管理面 1 → 3 → 2 → 4 → 5（配置存储是管理后台的前置，
先于逃生通道交付）；Schema drift 治理为并行项。
同时只有一项进入 Committed。本阶段分别落地
[企业身份](roadmap/directions.md#l7-enterprise-identity-and-scale)、
[durable audit](roadmap/directions.md#l8-data-governance-and-durable-audit)、
[管理 UI](roadmap/directions.md#l9-management-ui)、
[受约束扩展点](roadmap/directions.md#l11-constrained-extensibility)与
[受治理配置脚手架](roadmap/directions.md#l18-governed-configuration-scaffolding)
的最小子集，其余部分仍按各方向的触发证据升级。任何管理面路径不得绕过统一的
身份、授权、预算与审计链。

### 管理面 1 — Users, Roles and Permissions

预期 `v0.1.11`。设计先行：`docs/design/authorization-model.md` 评审通过后
实现。

**问题证据**：见本阶段进入条件前两项。没有用户与多角色授权，逃生通道
（管理面 2）与管理后台（管理面 4、5）都无法安全成立。

**阶段结果**：

- 顶层 `roles`：角色名 → 权限列表，权限内容由管理员自行配置；
- 顶层 `users`：每个用户独立凭据（只存 hash，可吊销、可轮换）、所属角色列表、
  直授权限与 subject 属性（如 tenant）；
- 权限分两类：**实体权限**（实体、动作、可读/可写字段、行策略）与**系统
  权限**（如 `sql:execute@<datasource>`、`admin:*` 细分项）；
- 合并规则：用户有效权限 = 所属各角色权限 ∪ 直授权限。动作与字段取并集；
  行策略取 OR；引用 `${subject.tenant*}` 的租户约束是硬边界，始终 AND，
  不参与并集；
- 兼容：旧的实体内 `roles`/`fieldACL`/`rowPolicies` 编译进同一套内部权限
  模型；未配置 `users` 时行为与现状（共享 token + `server.role`）等价；
- 可信代理注入的身份映射到同一用户模型，不形成第二套身份语义；
- 审计与 decision trace 记录用户与生效角色。

**非目标**：不自建完整 IAM/SSO（外部 IdP 登录随管理面 4，MCP OAuth 2.1、
delegation chain 仍属 L7）；不支持显式 deny 规则；stdio 保持进程级默认身份。

**退出门禁**：

- [ ] 未配置 `users` 时行为与现状等价，有兼容测试；
- [ ] 多角色行策略 OR 合并与租户硬边界 AND 有 adversarial 测试，登记新
  threat ID；
- [ ] 缓存、singleflight、事务与预算不能跨用户复用，有测试锁定；
- [ ] 身份不能通过请求参数或未受信 header 提升；吊销、轮换在热重载与在途
  会话下语义确定；
- [ ] 不同用户对同一实体得到不同的可见字段和行，有 e2e 测试；
  [核心不变量](invariants.md) I5–I8 表述同步更新。

---

### 管理面 2 — Trusted SQL Escape Hatch

预期 `v0.1.13`，前置：管理面 1。`execute` 已用于实体动作和
`execute_entity`，新工具暂定名 `execute_sql`。

**问题证据**：作为 SQL MCP Server，可信操作者需要完整 SQL 能力（排障、临时
分析、运维）。缺少这个入口时，他们只能绕过本服务直连数据库，风险高于一个
受审计、受上限约束的入口。

**阶段结果**：

- 独立 MCP tool `execute_sql`，默认不注册；必须同时满足：全局开关显式开启、
  用户持有目标数据源的 `sql:execute@<datasource>` 权限、目标数据源显式声明
  允许逃生通道；
- `tools/list` 按用户过滤，未授权用户看不到该工具；
- 逃生通道使用单独声明的数据源条目，权限上限等于该 DSN 的数据库权限；服务
  不解析、不改写、不 sanitize SQL，DSN 账号权限就是边界；
- 成本约束仍然生效：statement timeout、结果行数/字节 cap、并发与 rate limit、
  用户/session 预算为不可关闭的硬上限。EXPLAIN 估算只对可 EXPLAIN 的语句
  尝试，估算失败时默认拒绝，可显式配置为只保留硬上限；
- 审计采用 `fail_closed` 语义（L8 的最小子集，仅作用于本入口）：审计写入失败
  则拒绝执行；记录完整 SQL 文本、用户、数据源、耗时和影响行数；
- 结果不套用实体级 mask/fieldACL（没有实体语义），文档与工具描述明确声明；
- 受治理面不变：IR、Metric、语义层和扩展点不得调用 `execute_sql`。

**非目标**：不做 SQL parser/sanitizer/自动改写；不做 NL2SQL；不与受治理面的
事务 token 混用。

**退出门禁**：

- [ ] 开关、用户权限、数据源许可任一缺失时，工具不出现在该用户的
  `tools/list` 且调用被拒绝，有矩阵测试；
- [ ] timeout、结果 cap 与预算在本入口上有测试锁定，且配置无法关闭；
- [ ] 审计 sink 不可用时拒绝执行，有故障注入测试；
- [ ] [核心不变量](invariants.md)与[威胁模型](threat-model.md)登记本入口的
  例外范围（I3、I24 等只对受治理面成立）与新 threat ID；README、
  [安全模型](security.md)的产品边界表述同步更新。

---

### 管理面 3 — Structured Config Store and Revisions

预期 `v0.1.12`，前置：管理面 1。本项吸收原"最小控制面"范围，实现
[Revision 与 Snapshot 设计](design/revision-snapshot.md)，详细设计见
[配置存储与 Revision](design/config-store.md)。

**问题证据**：见本阶段进入条件第四项。

**阶段结果**：

- `ConfigStore` 边界（对应 L11 `SnapshotStore`），首批实现为本地 SQLite
  （默认，纯 Go 驱动）以及已接入的 PostgreSQL/MySQL/OceanBase；
- 持久化 revision（`draft`/`published`/`superseded`/`rolled-back`）、用户、
  角色和配置审计元数据；提供 diff、publish、rollback；
- 部署迁移：任意两种 store 之间、store 与 YAML 之间可确定性往返
  （`contentHash` 一致）；YAML 文件模式继续可用，并可作为 bootstrap；
- store 自身 schema 版本化迁移，遇到未知版本 fail closed；
- store 所用数据源与受治理数据源隔离：store 表不得被任何 Entity 引用，也不得
  被 introspection 草稿收录；secret 只存占位符。

**非目标**：不做多写或分布式共识；多实例 snapshot 分发仍属 L7；不存储查询
结果。

**退出门禁**：

- [ ] SQLite、服务端数据库与 YAML 之间往返 `contentHash` 一致，有 golden 测试；
- [ ] store 不可用时 fail-static、没有可用 snapshot 时 fail closed，符合
  revision 设计的失败语义并有测试；
- [ ] store 表不可被 Entity 暴露，有校验测试；
- [ ] publish 复用热重载变更守卫（下沉到 `x/bootstrap`），与 CLI 共用一份规则。

---

### 管理面 4 — Admin API, Login and Schema Import

预期 `v0.1.14`，前置：管理面 1、管理面 3。

**问题证据**：见本阶段进入条件第三项。服务已经掌握 schema，缺的是把它变成
受治理配置的管理接口。

**阶段结果**：

- GraphQL 管理 API（schema-first），挂载于 `/admin/graphql`；resolver 按
  `admin:*` 细分权限授权；限制查询深度与复杂度，生产环境关闭 GraphQL
  introspection；所有写操作只产生 revision draft，经与 CLI 相同的校验链发布；
- 人员登录可插拔：本地账号密码（首个管理员由 CLI 引导创建）、通用 OIDC、
  通用 OAuth2（userinfo 映射，覆盖非标准 OIDC 平台）；登录后使用 HttpOnly
  会话 Cookie 并防 CSRF；外部身份到本地用户与角色的映射可配置；
- **Schema → 配置导入**（L18 的首个交付形态）：introspection 扩展为返回
  表/列注释与外键；导入结果是**未授权草稿**（`discoverable: false`、无任何
  角色），注释映射为 description，外键生成候选 relationship；
- simulate：以指定用户预览可见实体、字段与行策略效果。

**非目标**：不做通用数据库管理工具（不下发 DDL、不编辑业务数据）；不因
introspection 自动授权；MCP 客户端的 OAuth 2.1 授权仍属 L7。

**退出门禁**：

- [ ] 导入草稿默认零权限，授权必须显式操作并留审计，有测试；
- [ ] 管理 API 的每个操作都有越权测试，登记 threat ID；
- [ ] 各登录模式有 state/nonce/重放、会话固定与 CSRF 测试；
- [ ] 查询深度/复杂度上限有测试锁定。

---

### 管理面 5 — Admin Console UI

预期 `v0.1.15`，前置：管理面 4。

**阶段结果**：

- Web 管理后台，以 TS + Vue3 构建、嵌入服务二进制，挂载于 `/admin`；
  类型与查询由 GraphQL schema 生成，配置表单由配置 JSON Schema 驱动；
- 核心交互以"连接数据源 → 导入 → 授权 → Agent 可调用"为主线：批量导入
  草稿、权限矩阵（角色 × 实体 × 动作/字段）、用户与角色管理、simulate、
  revision diff/publish/rollback、decision trace 查看；追求最短路径和零手写
  YAML。

**非目标**：不做低代码平台；UI 不持有任何绕过管理 API 的写路径。

**退出门禁**：

- [ ] 交互判据：新用户从连接数据源到首个实体可被 Agent 调用 ≤ 5 分钟；
  ≥50 张表的 schema 完成批量导入与授权 ≤ 15 分钟；由维护者与 ≥2 位
  外部试用者实测并记录首个卡点；
- [ ] 前端依赖、构建与发布纳入[供应链规则](roadmap/metrics.md)，CI 含前端
  lint 与测试。

---

### 管理面 · 并行 — Schema Drift and Compatibility Governance

进入门禁：真实部署出现数据库 schema 演进需求，或管理后台的重新导入需要
区分 schema 变化的影响。证据生产机制：外部证据冲刺的参考部署与管理后台的
重新导入。

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

## Next（管理面之后、v0.2.0 前 — 数据面与采用）

### Next 1 — Provider Capability Model

`v0.1.10` 发布后技术前置已满足；是否获得版本承诺仍须结合本次发布复审的
Tool Contract 设计评估决定，预期 `v0.1.16`。进入门禁与退出验收以
[Provider Roadmap](provider-roadmap.md) Capability Model 章节为唯一事实源。

**问题证据**：现有实现（`core/dialect.Capabilities`）是平铺 bool，成本
闸门装配（`cost.NewGateFromCapabilities`）直接消费这些 bool，无法表达
保证强度与证据状态——即无法区分"数据库提供该能力"与"本服务已装配可测试
的强制机制"。SQLite 与新 Provider 立项的前置工程项。

**阶段结果**：

- capability 重构为"范围 + 强度 + 证据"模型：保证强度统一为
  `unsupported` / `best_effort` / `enforced`；字段定义以 Provider Roadmap
  为唯一事实源；
- 闸门装配与 codegen 渲染改为消费新模型；
- PostgreSQL、MySQL、OceanBase 三个现有 Provider 的声明按新模型迁移；
  [Provider 兼容矩阵](provider-compatibility.md)可区分保证强度与证据状态；
- 字段取舍以 SQLite（`Next 2` 交付对象）为校验对象：模型必须能表达
  "缺少 `enforced` 成本证明时由核心层等价强制或 fail closed"这类组合；
- **跨 Provider conformance 套件**（≥5 任务）：PostgreSQL / MySQL /
  OceanBase 上结果等价、拒绝等价、治理等价，作为 capability model 的
  首个实际消费者。

**非目标**：不新增 Provider（SQLite 随 `Next 2`）；不引入 L13 实现方式
维度（仅预留）；不改变三库现有运行时行为与闸门语义（行为等价重构）。

**退出门禁**：

- [ ] 每项 capability 分别表达范围、保证强度和证据，不再存在混合语义的
  单 bool；
- [ ] "`best_effort` 不能满足硬限制；缺少 `enforced` 能力时，必须由核心层
  提供等价强制或 fail closed" 有测试锁定；
- [ ] ≥5 个跨 Provider 诊断任务全绿；
- [ ] 三库 integration/conformance/workload 差分无回归；兼容矩阵更新。

---

### Next 2 — Evidence-Backed SQLite

进入门禁、SQLite 首版范围和退出验收均以
[Provider Roadmap](provider-roadmap.md) 为唯一事实源；`v0.1.8` conformance
suite 与 `v0.1.9` workload 差分是任何新 Provider 的验收前置；capability
model（`Next 1`）为前置工程。

**进入条件已确立**（`v0.1.9` 发布复审）：架构验证 + 采用目标——SQLite 是
新 capability model 的首个新增 Provider 消费者，验证"弱成本证明、核心层
兜底"的执行模型，同时是产生真实采用证据的最低门槛入口。`Next 1` 交付后
本阶段即获得版本承诺（预期 `v0.1.17`）。SQLite 驱动随管理面 3 的
配置存储先行引入，Provider 复用同一驱动。

阶段结果：一个受现有 IR 和统一 engine 约束的窄 SQLite Provider，通过
conformance corpus 与 workload 差分双重验收。

---

### Next 3 — Result Provenance and Evidence Envelope

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

---

## Later

[Semantic Metadata](roadmap/directions.md#foundation-semantic-metadata)、
[完整 Public Eval](roadmap/metrics.md#agent-与业务效果)、
[推断与策略组合安全](roadmap/directions.md#l16-data-inference-and-policy-composition-safety)
（受监管部署前必须完成）、
[MCP 协议一致性与契约稳定](roadmap/directions.md#l17-mcp-protocol-conformance-and-contract-stability)、
[受治理配置脚手架](roadmap/directions.md#l18-governed-configuration-scaffolding)
中 introspection 导入以外的部分（dbt manifest、数据字典导入等）、
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
