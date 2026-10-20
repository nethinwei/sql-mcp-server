# 设计：固定数据面管线、流式结果预算、结果不确定

状态：**已实施（2026-10-10）**。相关设计：[状态的生命周期](lifetimes.md)、
[授权模型](authorization-model.md)；契约见 [工具契约](../tool-contract.md)。

## 背景与问题

五个数据工具（read、aggregate、create/update/delete、execute）各自编排
resolve → route → authorize → compile → gate → exec → mask → afterWrite。
同一组安全步骤在 7 个数据库调用点各写一遍，存在以下问题：

- 漏掉某一步不会报错。例如 `queryReadRows` 读出的行由调用方自己记得脱敏；
  过程结果先过滤字段再脱敏，顺序也由各处自己保证。
- 返回字节数只在事后检查：`finalizeToolResult` 把整个结果 `json.Marshal`
  一遍，超限的结果已经全部读进内存。读缓存为判断条目大小又编码一遍，传输层还要
  再编码一次。
- 写入只有成功和失败两种结果。连接在写入或提交期间中断时，数据库可能已经执行；
  若报成普通 `DATABASE_ERROR`，Agent 可能原样重试并重复写入。缓存也只在成功时
  失效，结果未知时读缓存可能已经过期。

## 执行器（`core/tool/pipeline.go`）

工具只负责**规划**：解析输入、路由、授权、编译、过闸门，产出一个 `statement`。
数据库由**执行器**调用：

| 类型 | 只能由谁产出 | 含义 |
|---|---|---|
| `grant` | `authorizeRequest`（授权并在拒绝时返回错误） | 请求已被授权 |
| `gated` | `gateSQL`（成本闸门），以及 `admitInsert`（单行 INSERT：create 工具本就不是 `CostGated`） | SQL 已经通过闸门（可能被闸门改写） |
| `statement` | `newStatement(tc, res, grant, gated)` | 一条可执行语句：路由后的上下文、实体、授权、SQL；可选 `fields`（过程结果保留的字段）、`keep`（脱敏前保留原值的连接列）、`rowCap`、`write` |

执行器：`statement.query` / `exec`。

1. 拒绝零值授权或空 SQL（`admitted`）：出错路径返回的零值 `grant{}` 即使被误用
   也是 fail closed；
2. 每一行按“保留连接列原值 → 字段过滤 → 脱敏 → 计量”的顺序处理，越限即停；
3. 错误分类：读按 `WrapDBError`；写按约束违反、权限、结果未知（见下文）分类；
4. 写入结束后，无论结果如何都执行 `afterWrite`（缓存失效、会话读跟随写、事务内标脏）：
   失败的写也可能已部分执行，过程即使结果不可用也可能已经写入（原过程语义的推广）。

结构由测试保证，而不是靠约定：`TestOnlyTheExecutorCallsTheDatabase` 解析
`core/tool` 的源码，限定以下用法只能出现在指定文件：

- `QueryContext`、`ExecContext`：只在 `pipeline.go`。`transaction.go` 例外，它是
  事务连接自身的包装；
- `BeginTx`：只在 `transaction.go`；
- `maskRow` 以及非零的 `grant{…}`、`gated{…}`：只在 `pipeline.go`。

新工具若绕过执行器直接访问数据库，CI 会失败。授权检查本身（`authorize`）仍可用于
不执行语句的场景，例如 describe 的可见性判断、事务权限探测、级联检查。

脱敏顺序有一处调整。原先 read 在 expand 之后才对父行脱敏，因为连接需要用原值。
现在执行器在脱敏前保留连接列的原值（`keep` 得到 `queried.kept`），expand 用这些
原值分组；父行与子行都由各自的语句脱敏，结果与原先相同。

## 流式结果预算（`core/tool/results.go`）

每次调用有一个 `resultMeter`，由 `RunTool` 创建；直接调用工具 `Run` 时由工具入口
补建。执行器每读一行就按其 JSON 编码长度计量：

- 行数：预算的 `maxReturnedRows`，所有工具都受此约束，expand 子行也计入；
- 字节：只读工具受 `cost.maxBytes`（`Limits.MaxReturnedBytes`）与预算
  `maxReturnedBytes` 中更严的一个约束；
- 进程内存：`ResultMemory`（进程层，`rateLimit.maxResultMemory`，默认 256 MiB，
  热更新），各调用合计；调用结束（审计之后）释放。

越限的第一行即中止，后续行不再从驱动读取。`Result.ReturnedBytes` 取计量值。
`finalizeToolResult` 只对未经计量的内容编码一次，即写入结果、describe 以及
singleflight 共享给跟随者的结果；读缓存的条目大小也用计量值。这样一次读取的结果
只在传输层编码一次。

边界：`database/sql` 的 `Scan` 会把一行完整读入内存，因此单行很大的列（例如
50 MB 的 TEXT）在被计量前已经读出。执行器能保证的是：这一行使调用立即失败，
不会再读后续行，也不会累积进结果。要限制单行大小，需要在实体投影或数据库侧
（列类型、视图）处理。

## 结果不确定（`OUTCOME_UNKNOWN`）

`uncertain(err)`：语句发出后连接中断，或执行期间超时、被取消。判断依据：

- `store.ErrConnectionLost`：provider 分类。MySQL 的 `ErrInvalidConn` 表示命令
  执行中途连接中断；
- `io.ErrUnexpectedEOF`、`net.Error`；
- `context.DeadlineExceeded`、`context.Canceled`。

`driver.ErrBadConn` **不算**：按 `database/sql` 的约定，它表示语句尚未发出，并已在
别的连接上重试过。`store.ErrNotConnected` 也不算，并且优先于上面各项：provider 用它
标记建连失败（PostgreSQL 的 `pgconn.ConnectError`、版本不受支持），此时语句没有发出，
即使错误链里还有 `net.Error` 或超时。数据库自身返回的错误也不算。

- 事务外的写入（update/delete/create/execute）遇到上述错误时，返回
  `OutcomeUnknownError`（`errors.Is(err, ErrOutcomeUnknown)`）。原因错误保留在错误链中
  供审计；契约码为 `OUTCOME_UNKNOWN`，不可重试，提示先读后决定。缓存按“已写入”
  失效（见执行器第 4 点）；
- 事务内的语句不报结果未知：提交前没有任何内容生效，结果由提交决定；
- 提交：`sqladapter` 的 `tx.Commit` 改为经过 provider 分类，原先直接返回驱动错误。
  提交结果不确定时，`TransactionManager.Commit` 返回事务写过的缓存目标与
  `OutcomeUnknownError`，commit 工具据此使缓存失效。

**事务墓碑**：事务结束时（提交、回滚、过期、会话关闭、关闭）记录结束方式，保留
10 分钟，最多 4096 条。同一会话、同一身份再用该令牌时，返回
`TransactionEndedError`（仍满足 `errors.Is(err, ErrTransactionNotFound)`），契约码
仍为 `TRANSACTION_NOT_FOUND`，并附 `constraints.outcome`：`committed`、
`rolledBack`、`expired` 或 `unknown`。其他调用方只看到普通的“未找到”，不会获知
别人事务的结局。提交与回滚执行期间，令牌登记在 `ending` 中，并发的查找会等待
结局写入后再返回，所以“未找到”始终意味着事务已经结束。这个等待以调用方的
ctx 为限：回滚可能在等一个已失联的数据库，调用方超时或取消后查找立即返回。

幂等键不在本阶段范围内（见总体方案“需要你决定的事项”）。

## 推迟的部分

- **`tool.Context` 按 Request/Snapshot/Process 三分，以及类型化 Principal**：执行器
  现在只从 `statement.tc`（路由后的上下文）取依赖，三分不会改变任何行为，却要改
  91 处构造（大多在测试中）以及 `rbac.Request.Role` 的字符串主体。等出现跨层误用
  的实际问题，或主体需要携带更多类型信息（例如服务账号）时再做。

## 验证

- `core/tool/pipeline_test.go`：
  - `TestOnlyTheExecutorCallsTheDatabase`（结构）；
  - `TestStatementWithoutGrantIsRefused`；
  - `TestReadStopsAtTheByteLimitAsRowsArrive`：100 行、每行约 1 KB、上限 2500 字节，
    只读到第 3 行就停止；
  - `TestResultMemoryBoundsTheProcess`：超出进程上限即失败，调用结束后内存归零；
  - `TestWriteWithLostConnectionHasUnknownOutcome`：`OUTCOME_UNKNOWN`、不可重试、
    写入被跟进；数据库自身返回的错误仍是 `DATABASE_ERROR`；
  - `TestCommitOutcomeAndTombstones`：提交结果未知时返回脏目标，墓碑区分
    `unknown` 与 `committed`，其他会话看不到结局；
- `x/providers/mysql` `TestClassifyLostConnections`：`ErrInvalidConn` 归为连接中断，
  `ErrBadConn` 不归入；
- 原有工具测试（脱敏、expand、字段 ACL、成本闸门、事务）全部通过；契约快照新增
  `OUTCOME_UNKNOWN: false`。
