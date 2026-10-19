# 设计：Provider Capability Model

状态：**已随 v0.1.15 交付（2026-10-12）**，见[发布说明](../releases/v0.1.15.md)；决议见文末，
实现中的发现见“实施记录”。对应主 [Roadmap](../roadmap.md#已交付)；能力范围、保证强度与
进入条件以 [Provider Roadmap](../provider-roadmap.md#provider-capability-model)
为唯一事实源，本文落到实现。相关设计：[固定数据面管线](pipeline.md)（执行器）、
[数据源模型](datasource-model.md)（连接、路由与 pooler）、
[IR 语义规范](ir-semantics.md)（conformance）。

## 背景与问题

`core/dialect.Capabilities` 只有三个 bool（`Returning`、`ExplainCost`、
`ExplainAccurate`），按方言写死。消费者只有成本闸门装配
（`cost.NewGateFromCapabilities`）与写入后读回（`core/codegen`、
`core/tool/tools_write_impl.go`）。其余能力判断隐含在各 provider 的 `Open` 里，
并且会**静默降级**：

| 场景 | 位置 | 实际效果 |
|---|---|---|
| PostgreSQL `pooler: transaction` | `x/providers/postgres/postgres.go` | 不设置 `statement_timeout`，原生超时消失；驱动的 cancel request 只在 pooler 转发时生效（见下） |
| DSN 自带 `statement_timeout` / `max_execution_time` / `ob_query_timeout` | 各 provider `Open` | 不再注入，`=0` 即关闭原生超时 |
| DSN 自带 `sql_safe_updates=0` | `x/providers/mysql/adapter.go` | 无键写入的原生兜底关闭 |
| MySQL 写语句 | MySQL 语义 | `max_execution_time` 只作用于只读 SELECT；v0.1.20 起由 `KILL QUERY` 在请求截止时停止（见 D1） |
| MySQL / OceanBase context 取消 | go-sql-driver | 只关闭连接，服务端语句可能继续执行；MySQL 直连时另由 `KILL QUERY` 停止 |

此外：EXPLAIN ANALYZE 支持经类型断言另行判断（`x/bootstrap` 的 `planners`）；
[兼容矩阵](../provider-compatibility.md)手写，三库“连接级 timeout”仍为“未独立验证”。

结果：服务声明的硬上限在部分配置下只是尽力而为，且运营者、控制台与文档都看不到。

## 目标与非目标

目标：

- 每项能力分别表达**范围、保证强度、证据**，不再有混合语义的 bool；
- 强度**按连接判定**（取决于 DSN、pooler 与数据库版本），不按方言写死；
- 静默降级变为可见：启动告警、管理 API 与控制台显示同一份 profile；
- 可选严格模式：硬限制缺少 `enforced` 且核心层无等价强制时 fail closed；
- 兼容矩阵由 profile 与证据测试引用生成，过期即测试失败；
- 跨 Provider conformance 增加拒绝等价与治理等价任务（≥5）。

非目标：

- 不新增 Provider（SQLite 属 `Next 1`）；不引入 `native`/`emulated` 实现方式维度；
- 默认运行时行为不变（行为等价重构）；只有显式开启严格模式才改变加载结果；
- 不把连接权限感知（`bootstrap.EntityCapabilities`，按实体与动作的授权能力）并入
  本模型：两者回答的问题不同，见“术语”。

## 术语

- **Provider 能力（capability）**：一个连接能为某项保证提供什么强度、有什么证据；
  本文的对象。
- **连接权限（entity capability）**：路由连接的数据库账号对某实体能做什么动作；
  `v0.1.11` 已交付，名称保留，文档中统一称“连接权限”以免混淆。

## 模型（`core/dialect`）

```go
type Strength string // "unsupported" | "best_effort" | "enforced"
type Evidence string // "db_verified" | "core_verified" | "declared" | "external"

type Claim struct {
    Strength Strength
    Evidence Evidence
    Reason   reason.Reason // 提供方式或降级原因，进入告警与报告（见“原因”）
}

type Key string
type Profile map[Key]Claim
```

- `enforced`：本服务已装配、可测试的强制机制，失败时不会静默降级；
- `best_effort`：会尝试，但不能据此声明硬保证（例如 DSN 可覆盖、只对部分语句生效）；
- `unsupported`：不提供。
- 证据取值与兼容矩阵现有四档一一对应（真实数据库验证、核心层验证、仅声明、外部配置）。

**首批能力键**（只收已有消费者的能力，不做预留）：

| 键 | 消费者 | PG | MySQL | OceanBase |
|---|---|---|---|---|
| `write.returning` | codegen、写入读回 | enforced | unsupported | unsupported |
| `cost.explain` | 闸门 Estimate 层 | enforced | enforced | enforced |
| `cost.explain.accurate` | 闸门是否在非 fail-closed 下启用 Estimate | enforced | best_effort | best_effort |
| `cost.explain.analyze` | AQE 反馈 | enforced | unsupported | unsupported |
| `limit.timeout.read` | 硬限制检查 | 见下 | 见下 | 见下 |
| `limit.timeout.write` | 硬限制检查 | 见下 | 见下 | 见下 |
| `limit.unkeyedWrite` | 无键写入兜底 | unsupported（核心层 WriteGuard） | 见下 | 见下 |

超时与无键写入按连接判定：

- PostgreSQL：`statement_timeout` 由服务注入时，读写均为 `enforced`（原生）；DSN 覆盖
  （含 `0`）时原生超时失效，由 pgx 的 cancel request 在数据库侧终止语句，仍为 `enforced`，
  `Reason` 写明来源。transaction pooler 不保留 `statement_timeout`，cancel request 也只在
  pooler 转发时才到达数据库（pgbouncer 会转发，但未测试），因此与 MySQL 同口径，定为
  `best_effort`，`cost.requireNativeLimits` 下拒绝加载。
- MySQL：`max_execution_time` 由服务注入时读 `enforced`；DSN 覆盖或 transaction pooler
  （会话变量不保证保留）时读降为 `best_effort`。写由 `KILL QUERY` 在请求截止时停止
  （v0.1.20 起），直连时 `enforced`，不受 DSN 覆盖影响；经 transaction pooler 时
  `unsupported`（见 D1）。
- OceanBase：`ob_query_timeout` 作用于全部语句；服务注入时读写 `enforced`，DSN 覆盖
  或 transaction pooler 时 `best_effort`。
- `sql_safe_updates`：服务注入或 DSN 设为 `1`/`ON` 时 `enforced`，DSN 设为 `0`/`OFF` 时
  `unsupported`，DSN 设为其他无法判定的值或经 transaction pooler 时 `best_effort`。

### 原因（`core/reason`，v0.1.20 起）

能力的原因、连接权限的原因、限制所依赖的工作（读、写、过程、逃生通道）以及发布前判定的
提示，都是 `reason.Reason{Code, Params, Parts, Text}`：

- `Code` 是稳定的编码（如 `timeout.mysqlWritePooled`、`privilege.missing`），
  全部登记在 `core/reason/codes.go`，每个编码有一个带 `{占位符}` 的英文模板；
- `Params` 是占位符的取值（变量名、权限名、驱动错误原文等）；`Parts` 是由另一个原因填充的
  占位符（如判定提示中的 `{cause}`、`{use}`、`{strength}`），不再嵌套；
- `Text` 是填好的英文原文，日志、CLI、MCP、兼容矩阵与 GraphQL 原有的 `reason`/`use`/
  `warnings` 字段照旧输出它。

管理 API 另以 `reasonDetail`、`useDetail`、`Validation.notices`、`PlanException.reasonDetail`
返回结构化的原因；控制台按 `reasons.<code>` 经 vue-i18n 显示 zh-CN/en，部分先各自翻译再填入，
未知编码回退 `Text`。`core/reason/testdata/codes.json` 是编码与英文模板的清单
（`go test ./core/reason -run TestCodesFixture -update` 重新生成），控制台测试据此核对每个编码
在 zh-CN 与 en 中都有文案、且占位符一致。

新增一个原因：在 `codes.go` 加常量与英文模板，在产生它的地方用 `reason.New`（或带部分的
`reason.Compose`），重新生成清单，再在 `web/admin/src/i18n` 的 zh-CN 与 en 中加
`reasons.<code>`；漏掉任一步测试即失败。

## 判定位置

- `core/dialect.Dialect.Capabilities()` 返回方言的静态 profile（与连接无关的能力）；
  `core/provider.Provider.Profile()` 返回连接的 profile：方言能力加上该连接的限制。
- 每个 provider 导出纯函数 `Limits(dsn, opts)`（只解析 DSN，不连接），`Open` 用同一份
  判定写入连接（`sqladapter.Base.WithLimits`），兼容矩阵的生成器（`internal/capgen`）
  也调用它；`providerregistry.Drivers()` 让生成器发现漏列的驱动。
- profile 是连接的属性，随连接共享（`bootstrap.Shared`），热重载不重新判定未变的连接。
- DSN 密钥解析仍按驱动名在 `x/bootstrap/dsn.go` 分支，本版不动；新增 SQLite 时一并移入
  provider。

## 消费者迁移

- `cost.NewGateFromCapabilities` 改为读 profile：`cost.explain` 为 `unsupported` 时不装配
  Estimate；`cost.explain.accurate` 不是 `enforced` 时，只有 `ExplainFailClosed` 才装配
  （与现状相同）。
- codegen 与写入读回读 `write.returning`。
- AQE 的 `explainAnalyze` 校验读 `cost.explain.analyze`（采样器仍由类型断言取得，两者
  不一致时同样拒绝装配）。
- 行为等价由现有闸门、codegen 与 bootstrap 测试加一组“旧 bool → 新 profile”对照测试锁定。

## 硬限制与严格模式

规则：**`best_effort` 不能满足硬限制；缺少 `enforced` 时，由核心层提供等价强制或
fail closed。**

核心层等价强制（现状）：

- 读：应用层 context 超时 + 结果逐行计量（`cost.maxRows`/`maxBytes`）约束调用方
  等待与返回量，但不终止服务端语句。
- 已核实：pgx v5 在请求 deadline 到达时丢弃连接，丢弃前（`asyncClose`）先向服务端发送
  cancel request（经 pooler 转发），服务端语句随之终止，不需要额外数据库权限。因此
  PostgreSQL 在 transaction pooler 与 DSN 覆盖 `statement_timeout` 时读写超时仍为
  `enforced`（核心层），以 integration 断言语句已从 `pg_stat_activity` 消失、写入未生效
  为证据（`TestPGStatementTimeout`）。
- go-sql-driver/mysql 取消时只关闭连接，服务端语句可能继续：MySQL / OceanBase
  没有核心层等价强制（见 D1）。

数据源依赖的硬限制（`bootstrap.NativeLimitIssues`）：读连接的 `limit.timeout.read`；
有角色或用户能写入时（被授予 create/update/delete，对应写工具开启且实体 `dmlTools` 开启）
写连接的 `limit.timeout.write`，以及写后读（`readAfterWrite`）或读写事务开启时写连接的
`limit.timeout.read`；有人能执行过程时（被授予 execute，过程 `trustedProcedure`，经
`execute_entity` 或自己的工具暴露）执行连接的 `limit.timeout.write`；`tools.executeSql`
开启时逃生通道连接的读写两项，其中写一项在该连接的账号不能写时不报告（v0.1.20 起：
服务器处于 `read_only`，或 MySQL `SHOW GRANTS` 显示只有读权限；有授予的角色或无法判断时
照旧报告）。`limit.unkeyedWrite` 只报告：核心层 WriteGuard 已是
等价强制。

配置 `cost.requireNativeLimits`（默认 `false`，可热重载）：

- `false`：行为与现状一致；不是 `enforced` 的硬限制在启动与热重载时告警（同一进程内
  只在变化时重复），并出现在管理 API（`providerCapabilities`）与控制台数据源摘要；
- `true`：存在上述问题时配置不能加载（启动失败或热重载拒绝，`ErrNativeLimits`），
  错误逐条列出数据源、连接、键、用途与 `Reason`。

发布前：控制台的校验与发布审查对尚未应用的配置按同一规则判定，连接的能力由驱动登记的
`Limits` 从 DSN 算出（`providerregistry.RegisterLimits`；密钥按服务自己的解析器解析，
不连接），算不出的连接本身算一项问题；严格模式下有问题即校验失败、阻止发布。CLI 发布
不连接数据库，由服务拒绝应用。

注入的原生超时比 `queryTimeout` 少十分之一（至多 1 秒，`Options.NativeTimeout`），使
数据库通常先停住语句、报告结果确定；请求 deadline 从调用开始计时（含规划与 EXPLAIN），
所以只是“通常”。

## 证据

- 三库 integration（`Test*StatementTimeout`）：PostgreSQL 断言注入的
  `statement_timeout` 停住读写、DSN 覆盖为 0 时 cancel request 使语句离开
  `pg_stat_activity` 且写入未生效；MySQL 断言 `max_execution_time` 停住读、
  `sql_safe_updates` 拒绝无键 UPDATE，并由 `TestMySQLWriteTimeoutKillsTheStatement`、
  `TestMySQLLockWaitingWriteIsStopped` 断言超时的慢 UPDATE（直接与预编译）在库中被停止并
  回滚、行锁立即释放、等锁的 UPDATE 在锁释放后不再生效；OceanBase 断言 `ob_query_timeout` 停住读写且写入
  未生效、`sql_safe_updates` 拒绝无键 UPDATE。
- 判定本身（DSN 覆盖、pooler）由各 provider 的 `TestLimits` 锁定；严格模式与依赖判定由
  `x/bootstrap` 的 `TestNativeLimitIssuesFollowTheRoutedWork`、
  `TestRequireNativeLimitsRefusesTheConfiguration` 锁定；逃生通道只读账号的判定由
  `TestReadOnlyEscapeConnectionNeedsNoWriteTimeout`、`TestCannotWrite`、MySQL 的
  `TestGrantsWrite` 与 integration `TestMySQLReadOnlyEscapeAccountNeedsNoWriteTimeout`
  （只读与可写账号各一）锁定。
- 兼容矩阵的 Provider 能力表由 `go generate ./x/providers/all` 生成，
  `internal/capgen` 的测试发现过期或漏列驱动即失败。

## 跨 Provider conformance

`internal/conformance.RunGovernance` 在现有结果差分（已含空集聚合与 NULL 语义）之后，
对三库同一 fixture 经完整工具链（授权、行策略、字段 ACL、mask、成本闸门、执行器）运行：

1. `governed-read`：行策略 + 字段 ACL + mask 下的行与解释器结果相同（治理等价）；
2. `adversarial-filter`：调用方过滤只能收窄行策略；
3. `governed-aggregate`：聚合只看到行策略内的行，结果与解释器相同；
4. `full-scan-refusal`：拒绝全表扫描时三库都返回 `COST_EXCEEDED`（拒绝等价）；
5. `unkeyed-write-refusal`：无过滤的写入为 `UNSAFE_WRITE`，非键过滤的写入为
   `COST_EXCEEDED`（WriteGuard）；
6. `statement-timeout`：数据库侧超时在三库都归类为 `store.ErrStatementTimeout`。

## 实施记录

- **pgx 的取消行为**：评审时以为 pgx 默认只给连接设 deadline、不发 cancel request，
  核实后不成立：deadline 到达时 `asyncClose` 先发 cancel request 再关闭连接。因此
  PostgreSQL 无需改驱动配置，`postgres.go` 原注释正确；结论与证据见上文。
- **语句超时的错误分类**：数据库报告的超时（PostgreSQL 57014、MySQL 3024、OceanBase
  4012）此前是普通 `DATABASE_ERROR`；现归类为 `store.ErrStatementTimeout`：遥测记为
  timeout，写入不再被当作结果未知（语句已回滚）。与此前相同，它计入熔断：数据库整体
  变慢时，超时正是它的表现。
- **聚合列名不一致（conformance 发现的缺陷）**：codegen 不给聚合列起别名，结果的键是
  数据库自己的列名——PostgreSQL 把同一函数的多个聚合都叫 `sum`（按列名转成行时互相
  覆盖，丢值），MySQL/OceanBase 则为 `COUNT(*)`、``SUM(`val`)``。现由执行器按位置
  命名（`store.IterNamed`：分组字段，然后 `count`、`<func>_<field>`），与分组字段同名时
  拒绝；SQL 不加别名，`allowTemplates`/`rejectTemplates` 与估算反馈的指纹不变（加别名
  会让已配置的 `rejectTemplates` 失配而放行）。结果键名的变化是 breaking，见 CHANGELOG。

## 交付步骤

1. 本文评审 → 2. `core/dialect` 模型与三库 profile（含 DSN/pooler 判定）与对照测试 →
3. 消费者迁移（闸门、codegen、AQE） → 4. 严格模式、告警、GraphQL 与控制台 →
5. 三库超时 integration 与生成的兼容矩阵 → 6. conformance 任务 →
7. 附带项：管理 API 逐操作越权矩阵与威胁条目（TM-014）、复杂度上限测试 →
8. `make ci-full`、路线图同步（发布说明随发版准备）。

## 评审决议

- **D1 MySQL 写超时由 provider 发送 `KILL QUERY`**（v0.1.20 修订，此前判为
  `unsupported`）：与 PostgreSQL 在请求截止时发送 cancel request 对等。每条连接建立时读取
  `CONNECTION_ID()`；语句的 context 先于语句结束时，经另一条连接执行
  `KILL QUERY <线程号>`（限时 2 秒，失败只记日志），该连接此后不再复用。同一账号停止自己
  的线程不需要 `PROCESS` 或 `CONNECTION_ADMIN`。MySQL 的线程号在服务器运行期间单调递增、
  不复用，迟到的 `KILL QUERY` 只会落到同一线程或落空。覆盖实体写入、存储过程与逃生通道
  （都经同一连接池）。经 transaction pooler 时连接读到的线程号对应的是借出的后端会话，
  `KILL QUERY` 可能停掉其他客户端的语句，因此不发送，写仍为 `unsupported`。OceanBase 由
  `ob_query_timeout` 覆盖全部语句，不需要 `KILL QUERY`。实现在 `x/providers/mysql/kill.go`，
  核心层不变。
- **D2 profile 不进入 MCP 侧**：只出现在启动告警、管理 API 与控制台；
  Agent 不据此决策，暴露反而泄漏部署细节。
