# 设计：状态的生命周期、撤销独立生效、按连接隔离故障

状态：**已实施（2026-10-10）**。相关设计：[revision 与快照](revision-snapshot.md)、
[配置的意图与生效值](spec.md)、[授权模型](authorization-model.md)。

## 背景与问题

`bootstrap.App` 是一次配置装配出的快照，重载时原子替换。但它同时持有**进程级**状态：
事务表、会话预算、写跟踪。于是每次重载都要手工搬运（`preserveReloadTransactions`、
`preserveReloadBudget` 以及 `Writes` 的转交）。每新增一种状态就要再加一个搬运函数，
漏搬一次就会丢事务或绕过预算。另外还有三个问题：

- 熔断与自适应并发（AIMD）全进程只有一份：一个数据库故障或变慢，所有数据源一起被拒绝；
  半开状态放行所有请求，冷却一结束就会被流量冲垮；一批同时超时的请求会让 AIMD
  连续减半多次；
- 事务只有全局上限 `transactions.maxOpen`，而每个连接池的上限等于 `rateLimit.ioPool`：
  事务能占满某个池，使该池的普通查询无连接可用；
- 撤销要等整个 revision 装配成功。如果一个 revision 同时禁用用户且因其他原因装配
  失败（如数据库不可达），快照不变，被禁用的用户由暂停机制拒绝（见 `hold.go`）；表或
  字段缺失不再导致装配失败，只让对应实体不可用。

## 分层

| 层 | 内容 | 生命周期 | 位置 |
|---|---|---|---|
| 进程（Process） | 连接（按设置复用、引用计数）及每个连接的健康状态，IO 配额与 RPS，读缓存，执行总线，事务表，预算，写跟踪 | 进程 | `x/bootstrap.Shared` |
| 快照（Snapshot） | 由配置编译出的策略：registry、authorizer、masker、gate、limits、路由、engine | 一次发布 | `x/bootstrap.App` |
| 请求（Request） | 主体、subject、会话、决策 ID | 一次调用 | `tool.Context` |

发布时 `Shared.apply` 把新配置的限制应用到进程层：

- 事务：`UpdateLimits` 与 `SetConnectionLimit`；
- 预算：`UpdateLimits`；
- 速率限制：设置变化时重置每个连接的熔断与 AIMD。

快照只持有进程层状态的引用。**搬运函数全部删除**，重载不再涉及状态的转交。
`Runtime.CloseContext` 最后调用 `Shared.Close`，由它关闭事务表。

## 按连接隔离故障（`x/bootstrap/guard.go`）

每个共享连接带一个 `health`，包含熔断器与 AIMD。它包装连接的 `DB`（查询在 rows 关闭时
才结束计时）、写、`BeginTx`，以及成本闸门的 `EXPLAIN` 与 `EXPLAIN ANALYZE` 采样
（`guardedExplainer`、`guardedSampler`）：熔断打开时 EXPLAIN 也不会打到故障库，估算层
把这类拒绝原样返回（`ErrCircuitOpen` / `ErrRateLimited`），不会变成成本拒绝。数据源 A
持续失败只会打开 A 的熔断器，B 不受影响。
计入健康状态的结果采用白名单 `countsAgainstHealth`：

- 计入：成功，以及数据库或连接报告的失败；
- 不计入：调用方取消或超时，以及数据库因数据（约束）或权限拒绝的语句。

engine 的 `Quota` 只保留进程级部分：RPS 与 IO 配额。

限流原语（`core/ratelimit`）的修正：

- 熔断器半开时只放行一个探测，其余请求立即失败。探测成功则关闭熔断器，失败则重新打开。
  没有结果的调用通过 `Release` 归还探测名额；
- AIMD 记录最近一次减半的时间。在此之前开始的请求，其结果不再触发减半，所以一批同时
  超时的请求只减半一次。调整用 CAS 完成。

## 每连接事务上限

新增配置 `transactions.maxOpenPerConnection`，默认 `max(1, rateLimit.ioPool/2)`，
保证连接池至少一半留给普通查询。`TransactionManager` 按连接（按 `TxBeginner` 的
身份比较）计数。begin 时先后检查全局上限和该连接的上限。每个连接只有一个
`guardedBeginner`，在各代快照之间共享，所以计数跨重载有效。

## 撤销独立生效（`x/bootstrap/hold.go`）

重载失败时（`WatchStore` 与 `Watch`），如果新 revision 能加载（解码、默认值、校验、
驱动已注册，不连接数据库），`holdChanged` 会计算它**改动了哪些现有用户**，并把这些
用户**暂停**。改动包括：删除、禁用，或改了角色、subject、token、授权、权限，
只改描述不算。被暂停的用户：

- `Runtime.UserByTokenHash/UserByName` 查不到，因此 HTTP 每次请求的认证、代理身份头
  都被拒绝；
- 已有会话经 `OnRevokedPrincipals` 关闭，事务立即回滚。

暂停集合在每次失败时按最新 revision 整体重算，而不是累加：如果后一个 revision 恢复
了用户，用户就不再暂停。任何一次成功发布都会清空暂停集合，此后以新快照为准；配置文件
改回正在服务的内容时同样清空（并立即重试之后再次写入的同一内容）。旧快照仍在排空
（`ErrReloadBacklog`）不算 revision 失败：不标记 stale、不退避，下一轮立即重试，期间
照常暂停它改动的用户。

为什么改动一律暂停，而不是只暂停“收窄”的改动：

- 判断授权是收窄还是放宽，需要编译新配置的授权，这又依赖实体与数据源，而它们正是
  装配失败的部分；
- 暂停会多拒绝一些请求（fail closed）。装配失败本身就是需要处理的事故，这个代价可以
  接受。

不覆盖的情况：

- 角色定义收窄（影响持有该角色的所有用户）仍随完整重载生效。需要立即撤销时，禁用
  用户；
- 没有身份的默认主体（stdio）中，`server.role` 由快照解析，不经过暂停集合；
  `server.user` 经过暂停集合，用户被暂停时降为没有任何授权的主体（fail closed），
  而不是继续使用快照中的授权；
- 在途请求在回滚之后才开启的事务，会话已关闭、无法再使用，由事务 TTL 回收。

## 嵌入方自行装配的 App

进程层只存在于 `Shared`。通过 `NewRuntimeWithBuilder` 注入装配函数、不经 `Shared` 装配
的 `App`，其事务表、预算与写跟踪属于那个 `App` 本身：重载后旧 `App` 排空并关闭时，它
的在途事务随之回滚，预算累计与写后读跟踪清零。这是有意的——不再提供搬运函数（见上文
“分层”）。嵌入方要让这些状态跨重载存活，装配函数应复用同一个 `Shared`（`NewShared` 一次，
每次装配都传入它），而不是各自构造。

## 推迟的部分（有意不做）

- **类型化 Principal 与 `tool.Context` 三分**：`tool.Context` 的字段大部分已经只是对
  进程层与快照的引用。三分需要改动所有工具与测试，收益主要在可读性上。留到阶段 4
  固定管线时一起做，届时执行器本身就会按 Request/Snapshot/Process 取参数。
- **进程设置与策略分开存储**：`RestartChanges` 已经按 `schema:"restart"` 标签判定，
  分开存储只会改变存储格式，不会改变行为，不值得再迁移一次 payload。
- **`MaxIdle` 单独配置**：每连接事务上限已经保证普通查询有余量，空闲连接数等于上限，
  目前没有问题。

## 验证

- `x/bootstrap/runtime_test.go` `TestReloadKeepsTheProcessState`：重载前后事务、预算、
  写跟踪是同一实例，新预算限制会计入此前的花费；
- `x/bootstrap/guard_test.go`：每个连接有自己的熔断器；取消的调用不计入；
- `core/ratelimit` `TestBreakerHalfOpenProbes`、`TestAdaptiveBurstDecreasesOnce`；
- `core/tool` `TestTransactionConnectionLimit`；
- `x/bootstrap/hold_test.go`：`TestRevocationAppliesWhileTheRevisionFails`（同一个
  revision 里禁用用户并让 schema 漂移，用户在下一次轮询即被拒绝、会话被关闭，其他
  用户不受影响；成功发布后解除暂停）与 `TestChangedUsers`。
