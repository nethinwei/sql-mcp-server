# 设计：Trusted SQL Escape Hatch（`execute_sql`）

状态：**评审结论（2026-10-11，管理面 2，v0.1.14）**，按“交付步骤”实施；七项决议见
文末。对应主
[Roadmap](../roadmap.md#管理面-2--trusted-sql-escape-hatch) 管理面 2。相关设计：
[授权模型](authorization-model.md)（系统权限字段的预留）、
[固定数据面管线](pipeline.md)（执行器）、[数据源模型](datasource-model.md)
（连接与路由）、[状态的生命周期](lifetimes.md)（热重载）。

## 背景与问题

作为 SQL MCP Server，可信操作者（DBA、排障的工程师、做临时分析的人）需要完整的
SQL 能力。今天服务没有这个入口，他们只能绕过本服务直连数据库：没有统一审计、
没有预算与超时、没有 decision trace。一个受审计、受硬上限约束的入口，风险低于
一条不受管理的直连。

现状：

- 管理面 1 已交付用户、角色与权限，并为系统权限预留了 `permissions` 字段
  （`core/config/config.go` 的 `RoleDefinition.Permissions`、`UserConfig.Permissions`）；
  `validatePermissions` 对任何取值都返回校验错误，等生效版本开放；
- 数据面只有一个执行器（`core/tool/pipeline.go`），语句只能由授权（`grant`）和
  过闸门的 SQL（`gated`）构成，`TestOnlyTheExecutorCallsTheDatabase` 强制；
- 审计是异步、best-effort 的（`audit.AsyncAuditor`，不变量 I12）：队列满时丢弃；
- MCP 工具列表（`x/mcpserver.listedTools`）对所有会话相同，只按全局 `tools` 开关
  与快照变化；
- go-sdk v1.6.1 没有按会话过滤工具列表的内置选项，但提供
  `Server.AddReceivingMiddleware`，可以拦截 `tools/list` 的结果。

## 目标与非目标

目标（路线图“阶段结果”逐条）：

1. 独立 MCP 工具 `execute_sql`，默认不注册；注册与调用必须同时满足：全局开关显式
   开启、调用方用户持有目标数据源的 `sql:execute@<datasource>` 权限、目标数据源
   显式声明允许逃生通道；
2. `tools/list` 按用户过滤：未授权用户看不到该工具；
3. 逃生通道走显式声明的连接，权限上限就是该连接账号的数据库权限；服务不解析、
   不改写、不 sanitize SQL；
4. 成本约束仍然生效且不可关闭：statement timeout、结果行数 / 字节上限、并发与
   rate limit、用户 / session 预算；EXPLAIN 估算只对可 EXPLAIN 的语句尝试，估算
   失败默认拒绝，可显式配置为只保留硬上限；
5. 审计 `fail_closed`：审计写入失败则拒绝执行；记录完整 SQL 文本、用户、数据源、
   耗时与影响行数；
6. 结果不套用实体级 mask / fieldACL（没有实体语义），工具描述与文档明确声明；
   实施时为保证 `query` 模式的字节上限生效，工具在 `query` 模式下重新计量
   （`RunTool` 把非只读工具按写入计量，不设字节上限）；
7. 受治理面不变：IR、Metric、语义层与扩展点不得调用 `execute_sql`。

非目标：

- 不做 SQL parser / sanitizer / 自动改写，不做 NL2SQL；
- 不与受治理面的事务令牌混用：`execute_sql` 没有 `transaction` 输入，每次调用是
  自动提交的独立语句；
- 不做系统权限通配（`sql:execute@*`）与显式 deny，与授权模型本版非目标一致；
- 不扩展控制台对数据源连接的编辑（连接与路由本就不在工作区项中，见下文）；
- 不改变未授权调用方的任何行为：不配置本功能时，配置、工具列表、审计事件与
  v0.1.13 逐字节等价。

## 配置结构

```yaml
tools:
  executeSql: true                 # 全局开关，默认 false：默认不注册
databases:
  crm:
    connections:
      rw:  { dsn: ${CRM_RW} }
      ops: { dsn: ${CRM_OPS} }     # 逃生通道专用账号（建议，不强制）
    routing: { read: rw, write: rw, execute: rw }
    sqlEscape:
      connection: ops              # 显式声明：该数据源允许逃生通道，走哪个连接
roles:
  dba:
    permissions: ["sql:execute@crm"]
users:
  alice:
    tokenHash: sha256:…
    roles: [dba]
    permissions: ["sql:execute@reporting"]   # 直授，与角色权限取并集
audit:
  enabled: true                    # executeSql 开启时必须为 true（校验）
  path: /var/log/sql-mcp-server/audit.jsonl
cost:
  sqlEscape:
    estimate: required             # required（默认）| skip
```

规则：

- `tools.executeSql`：`ToolFlags` 新字段，`DefaultToolFlags` 为 false。放在 `tools`
  下的理由是工具注册已经按 `Tool.Enabled(flags)` 判定，无需第二套开关；它只决定
  工具是否存在，不决定谁能用。热重载生效。
- `databases.<name>.sqlEscape.connection`：必须是该数据源已声明的连接名（`dsn`
  简写时只能是 `default`）。缺少本块即该数据源不允许逃生通道。标记
  `schema:"restart"`，与 `connections` / `routing` 一致（连接按 DSN 在
  `bootstrap.Shared` 复用，引用一个已打开的连接并不真的需要重启，但路由类配置
  统一按重启处理，避免两套规则）。
- 系统权限格式固定为 `sql:execute@<datasource>`：`<datasource>` 必须是已配置的
  数据源，否则校验失败（引用不存在的对象是配置错误）；数据源**未**声明
  `sqlEscape` 时权限可以存在但不生效（这是三个独立条件之一，矩阵测试需要这种
  组合），`store lint` 与控制台校验给出警告。其他前缀（`admin:*`）仍按现状拒绝，
  随管理面 4 的细分权限开放。
- 用户有效系统权限 = 所属各角色 `permissions` ∪ 直授 `permissions`，与实体授权的
  合并规则相同，没有 deny。
- `tools.executeSql: true` 要求 `audit.enabled: true`（`Validate` 检查）：没有
  审计 sink 的逃生通道不能存在，这是审计 `fail_closed` 在配置层的体现。
- `cost.sqlEscape.estimate`：`required`（默认）时 EXPLAIN 失败即拒绝；`skip` 时不做
  估算，只保留硬上限。这是路线图允许的唯一放宽；`cost.enabled: false` 对本入口
  同样只关闭估算，不关闭硬上限（I17）。

字段同步：`schema:"…"` 标签、`core/config/fields.yaml`（中英文说明）、
`go generate ./core/config`（`schema.json`、`spec_gen.go`、配置文档字段表）、
`x/admin/graph/testdata/roundtrip.yaml`、`core/config/testdata/rules.json`
（前后端校验一致）。`TestOptionalBlocksLoadWithDefaults` 要求新块只靠默认值能
通过校验（不写 `sqlEscape` 即关闭）。

## 身份与授权

- 系统权限编译进 `rbac.Policy`：`rbac.Principal` 新增 `Permissions map[string]bool`
  （键为规范化后的权限串）；`Policy.Roles` 旁新增 `RolePermissions
  map[string]map[string]bool`。`x/bootstrap.accessPolicy` 填充二者。
- 新增 `rbac.Authorizer` 之外的独立判定 `rbac.Policy.HasPermission(principal,
  permission string) bool`：按 `user:<name>` 查用户，取角色权限与直授权限的并集。
  不走 `Authorize`（那是实体授权），避免把无实体的请求塞进覆盖集逻辑。
- 只有用户主体（`user:<name>`）可能持有系统权限。角色路径（共享 token 且未设
  `server.user`、`X-MCP-Role`）没有用户，永远拒绝；stdio 使用进程默认身份
  `server.user`，设了用户才可能看到工具。这与安全模型“stdio 保持进程级默认身份”
  一致。
- `bootstrap.App` 新增 `EscapeHatch(datasource string) (tool.EscapeRoute, bool)`：
  数据源声明了 `sqlEscape` 时返回其连接（`store.DB`，经 `guard` 包裹，与其他连接
  一样受熔断 / AIMD / IO 槽约束）与方言。`tool.Services.Sources` 旁新增
  `Escapes map[string]EscapeRoute`，装配时填充。

三条件在**两处**各判一次，缺一即 `ErrUnauthorized`：

| 位置 | 作用 |
|---|---|
| `tools/list` 中间件 | 决定该会话是否列出 `execute_sql` |
| 工具 `Run` 内（`authorizeSQL`） | 决定本次调用是否执行；列表过滤不是安全边界 |

拒绝码沿用 `UNAUTHORIZED`：泛化文案，不泄露是开关、权限还是数据源哪一项缺失
（TM-002 的枚举防护同样适用）；细节只进审计事件。

## 工具契约

工具名 `execute_sql`，`ReadOnly` 为 false（列表中不带 `readOnlyHint`，因为语句可能
写入），不实现 `CostGated`（它不走 `ChainGate`，见下文），不出现在
`DefaultTools` 之外的任何注册路径。

输入 schema（`core/tool/schema/execute_sql.json`）：

```json
{
  "type": "object",
  "properties": {
    "datasource": {"type": "string", "description": "Datasource the SQL runs on; required even with one datasource"},
    "sql": {"type": "string", "minLength": 1, "description": "One statement, run as is and auto-committed. Not parsed, rewritten or masked."},
    "args": {"type": "array", "description": "Positional parameters, in the dialect's placeholder style ($1 / ?)"},
    "mode": {"type": "string", "enum": ["query", "exec"], "default": "query",
             "description": "query returns rows; exec returns rowsAffected (DML, DDL, SET)"}
  },
  "required": ["datasource", "sql"]
}
```

- `datasource` 必填，不默认到 default 数据源：逃生通道对哪个库生效必须是显式的。
- `mode` 由调用方声明，服务不从 SQL 文本推断（不解析）。`query` 走
  `QueryContext`，`exec` 走 `ExecContext`。
- `args` 鼓励参数化但不强制；服务不检查 SQL 文本里有没有拼接字面量。
- 没有 `transaction` 字段；带了未知字段按 `INVALID_INPUT` 拒绝（与其他工具一致）。

输出（`Result.Content`）：

- `query`：`{"columns": [...], "rows": [[...], ...], "rowCount": n, "truncated": bool}`
  行用位置数组而不是对象，因为任意 SQL 可能返回重名列；`truncated` 为 true 表示
  读到行上限即停，而不是报 `BUDGET_EXCEEDED`（排障场景下“前 N 行”比拒绝有用，
  上限本身不可绕过）。
- `exec`：`{"rowsAffected": n}`。

Denial 新增一个 code：`AUDIT_UNAVAILABLE`（不可重试）——审计 sink 写入失败。
其余复用：`UNAUTHORIZED`、`INVALID_INPUT`、`COST_EXCEEDED`（估算失败或超阈值）、
`BUDGET_EXCEEDED`、`DATABASE_ERROR`、`OUTCOME_UNKNOWN`（`exec` 期间连接中断）。
`DATABASE_ERROR` 对本入口**回显数据库原文**（决议 3）：`reason` 携带驱动返回的
错误文本，`constraints.sqlstate` 携带 SQLSTATE。这是工具契约中唯一的例外，
`docs/tool-contract.md` 单列说明：受治理面的 `DATABASE_ERROR` 仍不回显；本工具的
调用方本就持有该连接账号的全部数据库权限，原文不扩大其可见范围，但会进入代持
令牌的 Agent 上下文，记入 TM-013 剩余风险。

契约变化均为兼容变化：新增工具、新增 code、审计事件新增可选字段与 `action` 取值。
`go test ./core/tool -run TestToolContractGolden -update` 重生成快照，CHANGELOG
记录。

## 执行路径

目标是不新增第二个数据库调用点。扩展 `core/tool/pipeline.go` 的执行器而不是绕过它：

```
RunTool（hook、预算、engine、审计）
  └─ ExecuteSQLTool.Run
       ├─ decodeTool（schema 校验）
       ├─ authorizeSQL(tc, datasource)      → grant   // 三条件；只在 pipeline.go
       ├─ estimateSQL(tc, route, sql, args) → gated   // EXPLAIN 估算；只在 pipeline.go
       ├─ newRawStatement(tc, route, grant, gated, mode)
       ├─ auditBegin（同步，失败即拒绝；见“审计”）
       └─ st.query / st.exec                // 执行器：计量、错误分类、写跟进
```

- `grant` 与 `gated` 的字面量仍只在 `pipeline.go` 出现，`authorizeSQL` 与
  `estimateSQL` 是它们的新来源；`TestOnlyTheExecutorCallsTheDatabase` 的允许表
  不变。`statement` 新增 `raw bool` 与 `escape EscapeRoute`：
  - `present`：`raw` 时不做字段过滤与 mask（没有实体，`res` 为零值），这是文档声明的
    “不套用实体级 mask”；
  - `fail`：`raw` 读按 `WrapDBError`，`raw` 写按 `uncertain` 判定 `OUTCOME_UNKNOWN`，
    其余归 `DATABASE_ERROR`（不做约束违反分类，那需要实体字段映射）；
  - `settle`：`raw` 写入后不知道改了哪些表，按数据源整体失效读缓存（复用数据源模型
    为视图准备的整库失效）并记录 `WriteTracker`（会话随后的读走 write 连接）。
- `authorizeSQL` 产出的 `grant` 是 `rbac.Decision{Allowed: true, Grants:
  ["permission:sql:execute@crm"]}`，审计 `grants` 字段因此能回答“凭哪条权限”。
- 路由：`tc.DB` 指向 `sqlEscape.connection`，不经过 `routeEntity`；`tc.DataSource`
  记录数据源名；`Dialect` 用于占位符说明与 EXPLAIN 前缀。
- 并发与限流：`RunTool` 现有链路全部复用——`acquireToolBudget`（scope 的 Role 是
  `user:<name>`，`budget.users` 生效）、`engine.Submit`（rate limit、熔断、AIMD、
  IO 槽；非只读不进 singleflight）、`meterCall`（行 / 字节计量）、
  `finalizeToolResult`（预算结算）。`budgetReservation` 为新 action `sql` 预留
  `MaxRows`。
- 超时：`statement timeout` 复用 `cost.queryTimeout`（`Limits.Timeout`），
  由 `beginCall` 的 context deadline 实施，与其他工具一致；驱动层无法保证服务端
  取消时，deadline 到期后连接被回收，这与受治理面相同。

受治理面不变：`core/relalg`、`core/codegen`、`core/tool/tools_*.go`、hook 与
扩展点没有任何路径能到达 `authorizeSQL`；`internal/` 增加一条结构检查
（`TestOnlyExecuteSQLUsesRawStatements`：`newRawStatement` 只有
`tools_execute_sql.go` 一个调用方）。

## 成本约束

硬上限（不可关闭，`cost.enabled: false` 不影响）：

| 约束 | 来源 | 实施点 |
|---|---|---|
| statement timeout | `cost.queryTimeout` | `beginCall` deadline |
| 返回行数 | `cost.maxRows`、`budget.*.maxReturnedRows` 取小 | `resultMeter`；到上限即停并标 `truncated` |
| 返回字节 | `cost.maxBytes`、`budget.*.maxReturnedBytes` 取小 | `resultMeter` |
| 并发 / rate limit | `rateLimit.*`、`budget.*.maxConcurrent` | `engine`、`budget.Lease` |
| session 预算 | `budget.*.maxSessionCost` | `finalizeToolResult` |

估算（可配置，默认开）：

- `estimateSQL` 直接调用该数据源 Provider 的 `cost.Explainer`（PG：`EXPLAIN
  (FORMAT JSON)`，MySQL / OB：`EXPLAIN FORMAT=JSON`），不构造 `codegen.Compiled`
  也不进 `ChainGate`：闸门里的 mandatory 层（`WriteGuard`、模板白名单、PK 点查）
  依赖 IR，对任意 SQL 没有意义，硬塞进去只会误判。
- 估算结果走与闸门同一套打分：`cost.ScorePlan` + `Threshold.HardScore`、
  `FullScanMaxRows`、`RejectFullScan`，加上预算的 `MaxEstimatedScannedRows`。超阈值
  `COST_EXCEEDED`，`hints` 复用 `cost.Hints`。
- “可 EXPLAIN 的语句”不靠解析判断：对每条语句都尝试 EXPLAIN，数据库报错（DDL、
  `SET`、`SHOW`、多语句等）即视为不可估算——`estimate: required` 时拒绝
  （`COST_EXCEEDED`，reason 说明估算不可用），`skip` 时跳过。这正是路线图
  “估算失败时默认拒绝，可显式配置为只保留硬上限”。
- 唯一的例外是元数据语句（`cost.sqlEscape.metadata: allow`，默认）：`query` 模式下的
  单条 `SHOW`、`DESCRIBE <表>`、`DESC <表>` 不做 EXPLAIN，只受硬上限约束。识别只看去掉
  空白与注释后的开头关键字、以及除末尾外不含 `;`，不是 SQL 解析；识别不出的照常估算。
  `DESCRIBE`/`DESC` 在 MySQL/OceanBase 也是 `EXPLAIN` 的同义词，`DESCRIBE ANALYZE`
  会执行语句，所以后面紧跟 `ANALYZE`、`FORMAT`、`SELECT` 等 EXPLAIN 关键字时不算元数据
  语句。理由：排障最常用的看表、看索引在默认配置下全部被拒，而 `skip` 会对所有语句
  关闭估算，代价过大；元数据语句只读系统目录，硬上限已足够。
- 估算用的 EXPLAIN 在逃生通道连接上执行（而不是 read 连接）：EXPLAIN 本身需要对象
  权限，用同一账号才能反映真实可见性，也避免 read 账号看见 ops 账号的对象。
- 估算结果写入审计事件 `cost` 字段（结构仍不属于定版契约）。

## 审计（`fail_closed`，仅本入口）

现有 `Auditor.Record` 是异步 best-effort，本入口需要同步且失败即拒绝：

- `core/audit` 新增接口 `Durable`：`RecordSync(ctx, Event) error`。`FileSink` 实现它
  （写入并 `Sync`，在 `FileSink.mu` 下与异步 flusher 串行）；`AsyncAuditor` 持有
  sink 时转发到 sink 的同步写；`NoopAuditor` 不实现（配置校验已经排除了这种装配，
  运行时再查一次 `tc.Auditor.(audit.Durable)`，不满足即 `AUDIT_UNAVAILABLE`）。
- 每次调用两条事件（同一 `decisionId`）：
  1. `phase: "begin"`：执行前同步写入，内容含 `user`、`roles`、`grants`、
     `datasource`、`connection`、`sql`（完整文本，不脱敏）、`args`（脱敏规则同现有
     `RedactInput`：超长截断）、`mode`。写入失败 → 拒绝执行，数据库未被调用；
  2. `phase: "end"`：执行后同步写入，含 `durationMs`、`returnedRows` /
     `rowsAffected`、`cost`、`allowed`、`code`、`error`（含数据库原文）。写入失败 →
     结果**不返回**给调用方，返回 `AUDIT_UNAVAILABLE`；`exec` 的效果此时已发生，
     文档按 `OUTCOME_UNKNOWN` 的口径说明（结果以数据库为准）。
- `RunTool` 末尾的 `recordToolAudit` 对本工具跳过（否则一次调用三条事件）；
  `Event` 新增可选字段 `phase`、`datasource`、`connection`、`sql`、`rowsAffected`，
  `action` 取值新增 `sql`。受治理面事件不写这些字段，形状不变。
- 同步事件可能排在队列中更早的异步事件之后落盘；审计消费方按 `time` 排序，
  文档说明。
- 审计文件轮转、远端 sink 仍不在范围内（L8 其余部分按触发证据升级）。

## MCP 注册与按用户过滤

- `listedTools.sync` 继续以 `app.Tools.Enabled(app.ToolFlags)` 决定是否向 go-sdk
  注册 `execute_sql`（开关关闭即不注册，列表与调用都不可达）。
- 注册 `AddReceivingMiddleware`：方法为 `tools/list` 时，取 `callerIdentity(ctx,
  app)`，`HasPermission` 对任一已声明 `sqlEscape` 的数据源成立才保留
  `execute_sql`，否则从结果里删掉。HTTP 下 ctx 带认证身份；streamable HTTP 的会话
  身份绑定（`sessionIdentityStore`）保证同一会话不能换人；stdio 用默认身份。
- `notifications/tools/list_changed` 仍随快照广播；用户权限热重载后，客户端重新
  `tools/list` 得到新结果，在途调用由 `Run` 内的判定兜底（吊销后立即拒绝）。
- `registerTool` 的 handler 不变：`currentTool` 找到工具后交 `RunTool`，`Run` 内
  `authorizeSQL` 二次判定。中间件只影响可见性。

## 热重载与进程级状态

- 热重载：`tools.executeSql`、`roles[].permissions`、`users[].permissions`、
  `cost.sqlEscape.estimate` 随快照生效（`CheckHotReload` 无需改动）；
  `databases.*.sqlEscape` 标 restart。
- 新 revision 装配失败但能加载时，`hold.go` 按用户暂停的逻辑对权限变更同样适用
  （改动了谁的权限，谁先暂停）。
- 没有新的进程级状态：无事务、无缓存条目（`raw` 读不进读缓存：任意 SQL 没有可靠
  的失效键）、预算与写跟踪复用 `Shared`。

## 管理面

- GraphQL（`x/admin/graph/schema.graphqls`）：`Role.permissions: [String!]!`、
  `User.permissions: [String!]!`、`RoleInput` / `UserInput` 同名字段；
  `Datasource.sqlEscape: DatasourceSqlEscape`（只读，`connection`）。读模型如实返回
  配置值。
- 工作区项（`x/admin/configapp/items.go`）：`RoleItem.Permissions []string`、
  `UserItem.Permissions []string`，合并语义与 `UserItem.Roles` 相同（整体替换，
  两边改成不同值即冲突）。`testdata/workspace_sections.json` 重新生成；**已保存的
  工作区会读作“已修改”**，发布说明提示。
- 引用维护（`x/configrefs`）：数据源改名不在工作区内（连接不可编辑），无需处理；
  删除数据源时 `sql:execute@<name>` 成为悬空引用，`reconcile` 按现有“删除造成的
  歧义”规则移除该权限并记入改动。
- 校验：`core/config/testdata/rules.json` 增加权限格式与引用规则，前端 vitest 共用。
- 控制台（`web/admin`）：`RoleView` / `UserView` 增加“系统权限”区块——按已声明
  `sqlEscape` 的数据源列多选框（数据源列表来自 `datasources` 查询），未声明的
  数据源置灰并提示；`SimulateView` 增加一行“可用 `execute_sql` 的数据源”；
  `AgentView`（Agent 接入）说明该工具只对授权用户可见。首轮只做这些，权限矩阵
  （`GrantMatrix`）不扩展。
- 配置 MCP（`/admin/mcp`）：不新增工具；方案引擎（`x/configplan`）不生成系统权限
  （授权规则只产出实体授权）。

## 文档与契约变更

| 文件 | 变更 |
|---|---|
| `README.md` | 边界句改为：受治理面不接受任意 SQL、不提供 DDL；可信逃生通道 `execute_sql` 默认关闭，只对显式授权的用户可见且受审计与硬上限约束 |
| `docs/security.md` | 新节“可信 SQL 逃生通道”：三条件、连接账号即边界、不脱敏、审计 fail closed、估算策略 |
| `docs/invariants.md` | I3、I4、I5、I6、I7、I15、I16、I19、I24 加注“只对受治理面成立；`execute_sql` 见 I29”；新增 **I29**：`execute_sql` 只在三条件同时成立时可见与可执行；timeout、结果上限、预算不可关闭；审计写入失败不执行；受治理面任何路径不得调用它 |
| `docs/threat-model.md` | 新增 **TM-013 — 逃生通道越权、越限与审计失效**；`internal/threatcheck/coverage.json` 映射测试 |
| `docs/tool-contract.md` | 审计事件新增字段与 `action: sql`；Denial 新增 `AUDIT_UNAVAILABLE`；`execute_sql` 的输出形状；单列 `DATABASE_ERROR` 对本工具回显原文的例外 |
| `docs/configuration.md` | “用户、角色与权限”节增加系统权限；字段表由 `go generate` 产出 |
| `docs/architecture.md` | “执行编排”节补一段逃生通道路径 |
| `docs/clients.md`、`docs/quickstart.md` | 工具列表按用户不同的说明；quickstart 不开启逃生通道 |
| `docs/evidence.md` | 证据索引加 TM-013 与矩阵测试 |
| `docs/design/authorization-model.md` | 非目标中“系统权限随管理面 2 生效”改为指向本文 |
| `docs/roadmap.md` | 管理面 2 退出门禁勾选；Committed 段更新；顺带修正管理面 4 一节与 `admin-api.md` 状态行里过期的“预期 v0.1.14” |
| `CHANGELOG.md`、`docs/releases/v0.1.14.md`、`docs/releases/README.md` | 版本记录；无 breaking |
| `examples/console/config.yaml` | 增加一个 `dba` 角色与 `sqlEscape` 示例（注释掉，说明默认关闭） |

## 测试与退出门禁

路线图退出门禁逐条映射：

1. **矩阵测试**（开关 × 用户权限 × 数据源许可，8 种组合只有全真可用）：
   - `core/tool`：`TestExecuteSQLMatrix`——每种组合下 `Run` 的结果，并断言拒绝时
     `FakeDB` 未被调用、审计未写 `begin` 事件；
   - `x/mcpserver`：`TestE2EHTTPExecuteSQLListedPerUser`——两个用户（一个有权限）
     各自 `tools/list`，再各自调用；`TestReloadRevokesExecuteSQL`——热重载删掉权限
     后列表变化、调用被拒；stdio 默认身份有 / 无用户各一条；
   - 角色路径（无用户）永远不可见：`TestExecuteSQLNeedsAUser`。
2. **硬上限不可关闭**：`TestExecuteSQLLimitsSurviveCostDisabled`——`cost.enabled:
   false`、`estimate: skip` 下 timeout、行数、字节、预算仍生效；`truncated` 语义；
   `cost.sqlEscape.estimate` 没有第三个取值（schema 枚举锁定）。
3. **审计故障注入**：`TestExecuteSQLRefusesWhenAuditFails`——`begin` 写入失败：拒绝且
   数据库未被调用；`end` 写入失败：返回 `AUDIT_UNAVAILABLE`、结果不返回；`Auditor`
   不实现 `Durable`：拒绝；`core/config`：`executeSql` 开启而 `audit.enabled` 为 false
   校验失败。
4. **文档与不变量**：`TestOnlyTheExecutorCallsTheDatabase` 不变；
   `TestOnlyExecuteSQLUsesRawStatements`；`internal/threatcheck` 对 TM-013 的映射；
   `make docs-check`。

其他：

- `core/config`：权限格式、未知数据源、`sqlEscape.connection` 不存在、`dsn` 简写下
  只能 `default`、`replica` 连接允许（只读逃生）、重启标记（`RestartFieldChanges`）；
  `TestOptionalBlocksLoadWithDefaults`。
- `core/rbac`：`HasPermission` 的并集与规范化；用户禁用后无权限。
- `core/tool`：`mode` 与 `QueryContext` / `ExecContext` 的对应、`exec` 后整库缓存失效
  与写跟踪、`OUTCOME_UNKNOWN`、不脱敏（配置了 mask 的列原样返回）、估算失败在
  `required` / `skip` 下的行为、未知输入字段、`transaction` 字段被拒、
  `budgetReservation`；`TestExecuteSQLEchoesDatabaseErrors`——本工具的
  `DATABASE_ERROR` 带原文与 SQLSTATE，受治理面工具对同一个 fake 错误仍不带。
- 契约：`TestToolContractGolden` 快照更新，`contract.json` 新增工具与 code。
- `x/admin/graph`：`roundtrip.yaml` 往返；权限写入工作区、合并冲突；控制台 vitest
  对校验规则。
- 三库 integration（`-tags=integration`）：PG / MySQL / OceanBase 各一条
  `execute_sql` 用例——`query` 返回列与行、`exec` 返回 `rowsAffected`、EXPLAIN 对
  DDL 报错触发 `required` 拒绝、`skip` 放行。
- `make ci-full` 全绿，`make web`。

## 交付步骤

每步独立可合并，`make ci` 通过后进入下一步；最后一步前不改 README 的边界句。

1. **配置与权限模型**：`ToolFlags.ExecuteSQL`、`DatabaseConfig.SqlEscape`、
   `CostConfig.SqlEscape`、`validatePermissions` 开放 `sql:execute@<datasource>`、
   审计前置校验、`fields.yaml`、`go generate`、`rules.json`、`roundtrip.yaml`；
   `rbac.Policy` 权限集合与 `HasPermission`；`bootstrap` 装配 `Escapes`。
   → 验证：`core/config`、`core/rbac`、`x/bootstrap` 单元测试；`make docs-check`。
2. **审计同步通道**：`audit.Durable`、`FileSink.RecordSync`、`AsyncAuditor` 转发、
   `Event` 新字段。→ 验证：`core/audit` 测试；现有事件 JSON golden 不变。
3. **执行器与工具**：`pipeline.go` 的 `authorizeSQL` / `estimateSQL` /
   `newRawStatement` 与 `raw` 分支；`tools_execute_sql.go`；schema 与契约快照；
   Denial `AUDIT_UNAVAILABLE`。→ 验证：矩阵、硬上限、审计故障注入、结构测试、
   契约 golden。
4. **MCP 注册与过滤**：`listedTools` 注册、`tools/list` 中间件、e2e（HTTP 多用户、
   热重载、stdio）。→ 验证：`make test-e2e`。
5. **管理面**：GraphQL 类型与输入、工作区项与 `workspace_sections.json`、
   `configrefs` 悬空权限、控制台三处改动。→ 验证：`go generate ./x/admin/graph`、
   `make web`、roundtrip 与 sections 形状测试。
6. **文档、威胁模型与发布**：上表全部文件，`coverage.json`，三库 integration，
   `make ci-full`，发布说明与 CHANGELOG。

## 评审决议（2026-10-11）

| # | 议题 | 决议 | 取舍 |
|---|---|---|---|
| 1 | 逃生通道连接是否必须独立于 `routing` 连接 | **不强制**，文档强烈建议独立账号 | 单账号部署也能开启；代价是共用账号时权限上限等于受治理面写账号，审计靠 `connection` 字段区分来源 |
| 2 | `end` 审计事件写入失败如何处理结果 | **丢弃结果，返回 `AUDIT_UNAVAILABLE`** | 严格的 fail closed，调用方拿不到未留痕的数据；`exec` 的效果已发生，文档按 `OUTCOME_UNKNOWN` 口径说明 |
| 3 | `DATABASE_ERROR` 是否回显数据库原文 | **回显**，工具契约单列例外，`constraints.sqlstate` 一并携带 | 排障效率优先；原文可能含对象名与数据值并进入 Agent 上下文，记入 TM-013 剩余风险 |
| 4 | 单数据源时 `datasource` 可否省略 | **必填** | 作用对象永远显式；单库部署每次多写一个字段 |
| 5 | 路线图“管理面 4 预期 v0.1.14”的过期文案 | **只修文案**：改为“已交付（v0.1.11），OIDC / OAuth2 待排期”，OIDC 不入本版 | v0.1.14 范围与 Committed 段一致；OIDC 继续没有版本承诺 |
| 6 | 控制台范围 | **角色 / 用户表单加系统权限多选，模拟页加一行** | 零手写 YAML 即可授权；数据源的 `sqlEscape` 声明仍走 YAML 并需重启，控制台只显示 |
| 7 | `cost.sqlEscape.estimate` 默认值 | **`required`**：EXPLAIN 失败即拒绝 | 与路线图一致、默认 fail closed；DDL / `SET` 在默认配置下被拒，运维场景须显式改 `skip`；`SHOW`/`DESCRIBE` 后来由 `cost.sqlEscape.metadata`（默认 allow）放行，见“估算” |

决议 3 与初稿推荐不同，已回写到“工具契约”“文档与契约变更”与测试清单。

仍属剩余风险、不作承诺的事项：

- **多语句**：不解析 SQL 就无法禁止驱动层接受的多语句（pgx 走扩展协议，实测拒绝
  `cannot insert multiple commands into a prepared statement`；MySQL / OceanBase 取决于
  DSN 的 `multiStatements`，默认关闭）。审计记录完整文本，硬上限对整次调用生效，写进
  TM-013 剩余风险。
- **不读表的语句**：常量 `SELECT`、`INSERT … VALUES` 的计划没有扫描节点，本地走查时
  三库都被 `required` 估算以“未知扫描”拒绝；修正为各 provider 把不读表的节点
  （PostgreSQL `Result` / `Values Scan`、MySQL `insert` 节点、OceanBase `EXPRESSION`）
  视为点查，集成测试加入这两类语句。
- **`raw` 读不进读缓存**：排障查询不该命中缓存；若后续需要，再按
  `(datasource, sql, args)` 加键并随 `exec` 整库失效。
