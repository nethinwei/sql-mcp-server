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
`ratelimit`、`engine`、`introspect` 和 provider 契约。外部依赖位于 `x/` 或
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
- 将调用提交到有界 engine，非事务读取工具可按身份作用域 singleflight；
- 触发 hook，并以 best-effort 方式记录审计；
- 汇总返回行数、耗时和近似 session cost。

实体工具随后执行字段用途校验、RBAC/RLS、方言路由和三阶段成本检查：
不可关闭的 Safety、可选 Estimate、不可关闭的 Enforcement。读缓存 key
包含物理关系（数据源与解析后的表）、SQL、参数、角色和 subject；写操作按物理关系
失效缓存，视图、物化视图和外部表的条目在同一数据源的任何写入后失效，存储过程按
`affects` 失效、未声明时失效整个数据源。一个物理关系最多对应一个实体，见
[数据源模型设计](design/datasource-model.md)。

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

跨快照协调由 `bootstrap.Shared` 承担：连接池按设置复用并引用计数、IO 配额与
限流熔断共享（`engine.Quota`）、读缓存共享且按物理数据库失效（键含配置代次以
隔离读取）。预算与事务 manager 在切换时原子替换限制并保留状态。被吊销用户的会话
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
