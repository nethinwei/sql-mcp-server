# 架构

## 分层

执行路径是：

```text
MCP client
  -> x/mcpserver（协议、stdio/HTTP、身份注入）
  -> core/tool.RunTool（预算、并发、hook、审计）
  -> core/tool（授权、IR 构造、成本检查、执行、脱敏）
  -> core/codegen + core/dialect（参数化 SQL）
  -> x/providers（database/sql driver、EXPLAIN、自省）
  -> PostgreSQL / MySQL / OceanBase
```

`core/` 包含 `config`、`relalg`、`codegen`、`entity`、`dialect`（接口与能力声明）、
`store`、`rbac`、`mask`、`cost`、`budget`、`audit`、`tool`、`cache`、`hook`、
`ratelimit`、`bus`、`engine`、`introspect` 和 provider 契约。外部依赖位于 `x/` 或
可执行入口，业务核心不反向依赖 `x/`；`.golangci.yml` 的 depguard 强制此边界。

`x/mcpserver` 是唯一接触官方 MCP SDK 的业务适配层。provider 与方言实现位于
`x/providers`，YAML 解码位于 `x/configyaml`，provider 工厂注册位于
`x/providerregistry`，secret 解析、schema drift 检查和运行时装配位于
`x/bootstrap`。

## 数据与查询模型

客户端只能选择配置中的实体和字段。实体按命名空间 `数据源.库.名称` 识别（名称只需在
同一数据源与库内唯一），装配时以这个规范 ID 为实体名，授权、级联、关系、`affects`、
缓存失效与审计都按它工作；配置与 Agent 的引用写 `名称`、`库.名称`、`数据源.名称` 或
完整 ID，必须恰好对应一个实体（Agent 只在自己能访问的实体中解析，见 `core/tool`
的 `canonicalEntity`）。工具输入先被转换成 `relalg` IR，再由
方言渲染为参数化 SQL；值通过 placeholder 绑定，标识符只能来自已解析的配置
和实体元数据。

当前支持读取、投影、过滤、聚合、排序/keyset、limit、insert、update、
delete 和 procedure call。关系展开不是通用 SQL join：它只支持同一数据源，
每个关系必须恰好一个 `joinOn` 对，并以一次批量 `IN` 查询展开；不支持嵌套展开。

## 执行编排

`tool.RunTool` 是 MCP 工具调用的统一入口，负责：

- 按角色/租户获取进程内预算 lease；
- 将调用提交到有界 engine（在执行总线的 `serve` 通道准入），非事务读取工具可按
  身份作用域 singleflight；
- 触发 hook，并以 best-effort 方式记录审计；
- 汇总返回行数、耗时和近似 session cost。

实体工具随后执行字段用途校验、RBAC/RLS、方言路由和三阶段成本检查：
不可关闭的 Safety、可选 Estimate、不可关闭的 Enforcement。读缓存 key
包含物理关系（数据源与解析后的表）、SQL、参数、角色和 subject；写操作按物理关系
失效缓存，视图、物化视图和外部表的条目在同一数据源的任何写入后失效，存储过程按
`affects` 失效、未声明时失效整个数据源。一个物理关系最多对应一个实体，见
[数据源模型设计](design/datasource-model.md)。

## 执行总线

进程级执行总线 `core/bus` 管控所有执行资源，`cmd` 创建它，`bootstrap.Shared`
在每次发布时按配置设置上限（热重载生效）。

- **通道（lane）**：触达数据库的工作都在某个通道上准入。`serve` 是工具调用，
  上限为 `rateLimit.maxInflight`，满即拒绝（`OVERLOADED`），新旧快照共享同一上限；
  `admin`（管理员请求内的自省，如模式列表、表注释、导入）、`scan`（扫描任务与扫描
  批次）、`background`（能力评估、装配期自省）由 `execution.*` 配置并发与排队上限，
  排队满立即返回繁忙；装配（启动与重载）只排队等待、不因繁忙失败，扫描任务在提交时即
  进入 `scan` 队列并计入排队数。总线关闭时扫描任务、扫描批次与事件轮询随之停止，
  批次保持未完成，由其他进程接管。
- **IO 配额**：所有通道共享 `rateLimit.ioPool` 个 IO 槽（即 engine 的
  `Quota`），非 `serve` 通道合计最多占一半（`max(1, ioPool/2)`），管理与扫描不会
  挤占工具调用。准入信息随 context 传递，嵌套工作（扫描任务内的自省）用
  `Ensure` 复用同一准入，不重复占槽。
- **后台任务**：所有 goroutine 与定时器经 `bus.Go`、`bus.Fork`、`bus.AfterFunc`
  启动（审计 flush、事务过期回滚、快照退役、扫描批次与心跳、事件轮询、配置
  watch、HTTP 关停）：按名称计数、恢复 panic、关闭时取消并等待。
  `internal/buscheck` 用类型信息检查 `core/`、`x/`、`cmd/` 的生产代码，出现
  `go` 语句、`sync.WaitGroup.Go`、`errgroup.Group.Go` 或 `time.AfterFunc` 即
  失败（仅 `core/bus` 与 engine 的 singleflight 例外，后者受 `serve` 准入约束）。
- **可观测**：`Bus.Stats()` 经 `x/otel.RegisterBusMetrics` 导出 OTel 指标，并由
  GraphQL `serverStatus.execution` 提供给控制台概览页。

进程关闭时先关闭 Runtime（排空快照、关闭审计），再关闭总线等待剩余任务（最多 15 秒）。

## 多数据源与事务

`databases` 创建命名 provider，实体通过 `datasource` 路由。每个数据源有自己的
方言、成本闸门和 prepared statement 缓存。关系不能跨数据源。

显式事务 token 为随机 256-bit 值，并绑定 MCP session、角色、subject 和
数据源。事务有 TTL 和全局 `maxOpen` 上限；session 关闭、TTL 到期、应用关闭
时会回滚未完成事务；不支持 savepoint。事务读取在 engine/singleflight 之前校验
token 身份且不参与去重，避免不同 transport session 共享同一执行。

## 热重载

`bootstrap.Runtime` 先完整构建新 App，构建成功后立即发布，旧快照在其在途请求
结束后于后台关闭（publish-then-drain）；每个请求自始至终只用一个快照。构建失败
保留旧快照。`serve --watch` 通过轮询配置文件内容 hash 触发。

跨快照协调由 `bootstrap.Shared` 承担（进程层，见 `docs/design/lifetimes.md`）：
连接池按设置复用并引用计数，每个连接带自己的熔断与自适应并发；IO 配额与 RPS
共享（`engine.Quota`）；读缓存共享且按物理数据库失效（键含配置代次以隔离读取）；
事务、预算与写跟踪归进程，发布时只更新其限制。被吊销用户的会话
在发布时关闭，旧快照排空后再按这些会话 ID 回滚其在途事务。

MCP 工具列表跟随发布的快照（`Runtime.OnPublish`）：工具集合或 custom procedure
变化时增删工具并通知客户端；HTTP 认证同样在发布时切换，TLS 证书在下一次握手
生效。只有传输方式、监听地址与 TLS 开关需要重启。schema resource 继续按当前
快照动态生成。

## 相关文档

安全决策见 [security.md](security.md)，公开配置契约见
[configuration.md](configuration.md)，可验证约束见
[invariants.md](invariants.md)，运行命令、生命周期和升级见
[operations.md](operations.md)。
