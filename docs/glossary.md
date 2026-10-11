# 术语表

代码、文档、控制台与 API 统一使用以下术语。同一个词只表示一件事；容易混淆的词在
“不要混用”中列出。

## 配置与版本

| 术语 | 含义 | 代码 |
|---|---|---|
| Spec（意图） | 配置写了什么，未写的字段为 nil；存储、工作区、编辑只处理它 | `config.Spec`，见 [design/spec.md](design/spec.md) |
| 生效配置 | Spec 经默认值与校验得到的配置，服务按它运行 | `config.Config`、`config.Resolve` |
| Revision（版本） | 配置库里一份不可变的配置，按 ID 递增；状态为草稿或已发布 | `core/revision` |
| Draft（草稿） | **只指**尚未发布的 revision，用于提交审阅 | `revision.Draft` |
| Published（已发布） | 配置库当前指向的 revision；发布只允许快进 | `revisionops` |
| Applied（已应用） | 某个服务进程正在运行的 revision | `Runtime.AppliedRevision` |
| Unapplied revision（未应用版本） | 已发布、但该进程尚未应用（装配失败或需重启）的 revision | `bootstrap.StaleState`，GraphQL `ServerStatus.unapplied` |
| Workspace（工作区） | 一位管理员在服务端的编辑副本，由项组成，基于某个已发布 revision | `core/workspace`、`x/workspaceops`，见 [design/versioning.md](design/versioning.md) |
| Item（项） | 工作区的最小合并单位：一个实体、角色、用户或一个设置分区 | `configapp.Sections` |
| Unsaved items（未保存项） | 控制台本地改动、尚未写回服务端工作区的项 | `workspace.unsavedItems()` |
| Checkpoint / Commit | 把工作区存为草稿 revision / 存为草稿并标记为提交 | `workspaceops` |
| Plan（方案） | 批量配置的输入：范围与规则 | `configplan.Plan` |
| Proposal / Preview | 方案算出的配置改动 / 改动连同报告的快照，可写入工作区 | `configplan`、`configapp.Preview`，见 [design/planning.md](design/planning.md) |
| Plan template（方案模板） | 保存下来、可在每次扫描后重算的方案 | `configapp.PlanTemplate` |
| Pending work（待处理） | 需要管理员处理的事项：模板的新匹配、schema 漂移的表 | `configapp.PendingWork` |

## 数据面

| 术语 | 含义 | 代码 |
|---|---|---|
| Datasource（数据源） | 配置里的逻辑数据源（`databases.<name>`），实体归属于它 | `entity.DatasourceName()` |
| Connection（连接） | 数据源下的一个连接配置（读、写、执行路由到不同连接）；进程内按设置共享并引用计数 | `bootstrap.Connections`、`Shared` |
| Physical database（物理数据库） | 连接实际指向的服务器与库；缓存按它失效 | `physicalDatabase` |
| Entity（实体） | 暴露给 Agent 的表、视图或过程，带字段、键、关系 | `core/entity` |
| Principal（主体） | 被授权的身份：角色名，或用户 `user:<name>` | `tool.Context.Role`、`UserPrincipal` |
| Subject | 调用方属性（行策略 `${subject.x}`、租户） | `tool.Context.Subject` |
| Tenant（租户） | subject 的 `tenant` / `tenant_id` / `tenantID` | `tenantKey` |
| Grant（授权项） | 主体对实体动作、字段、行范围的授权；执行器里的 `grant` 是一次请求通过授权的结果 | `core/rbac`，见 [design/authorization-model.md](design/authorization-model.md) |
| Statement（语句） | 执行器执行的一条语句：授权结果 + 过闸 SQL | `core/tool/pipeline.go`，见 [design/pipeline.md](design/pipeline.md) |
| Outcome unknown（结果未知） | 写入或提交期间连接中断或超时，数据库可能已执行 | `ErrOutcomeUnknown`、`OUTCOME_UNKNOWN` |
| Tombstone（墓碑） | 事务结束后保留 10 分钟的结束方式 | `TransactionEndedError` |

## 生命周期

| 术语 | 含义 | 代码 |
|---|---|---|
| Process（进程层） | 跨配置重载存活的状态与资源：连接及其健康、IO 配额、缓存、事务、预算、写跟踪、结果内存 | `bootstrap.Shared`，见 [design/lifetimes.md](design/lifetimes.md) |
| Snapshot（快照） | 一次发布装配出的、不可变的策略与引擎 | `bootstrap.App` |
| Request（请求） | 一次工具调用：主体、subject、会话、决策 ID | `tool.Context` 的请求字段 |
| Held user（暂停的用户） | 未应用版本改动了的用户，在该版本或后续版本应用前被拒绝 | `bootstrap/hold.go` |

## 不要混用

- **pending**：只用于“待处理”（Pending work）与扫描任务的排队状态。已发布但未应用的
  revision 叫 unapplied；控制台没写回的改动叫 unsaved。
- **draft**：只指草稿 revision。工作区不是草稿；“草稿合并”指把一个草稿 revision 合并进
  工作区。
- **role**：只指配置里的角色。泛指“被授权的身份”时用 principal。
- **逃生通道（escape hatch）**：只指 `execute_sql`——可信用户在声明了 `sqlEscape` 的
  数据源上原样执行 SQL 的独立入口；它不属于受治理面，“受治理面不接受任意 SQL”的
  表述不因它而改变。
- **database**：配置键 `databases` 是数据源；指服务器上的库时说物理数据库。
