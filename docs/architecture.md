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

客户端只能选择配置中的实体和字段。工具输入先被转换成 `relalg` IR，再由
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
包含实体、SQL、参数、角色和 subject；写操作按实体失效缓存。

## 多数据源与事务

`databases` 创建命名 provider，实体通过 `datasource` 路由。每个数据源有自己的
方言、成本闸门和 prepared statement 缓存。关系不能跨数据源。

显式事务 token 为随机 256-bit 值，并绑定 MCP session、角色、subject 和
数据源。事务有 TTL 和全局 `maxOpen` 上限；session 关闭、TTL 到期、应用关闭
时会回滚未完成事务。当前 MCP 工具不暴露 savepoint，尽管底层 store/provider
存在 savepoint 接口。事务读取在 engine/singleflight 之前校验 token 身份且不
参与去重，避免不同 transport session 共享同一执行。

## 热重载

`bootstrap.Runtime` 先完整构建新 App，再将旧快照标记 retired，等待在途请求
释放后发布新快照；reload 窗口的新请求等待，避免权限收紧时继续取得旧授权。
构建失败保留旧快照。`serve --watch` 通过轮询配置文件内容 hash 触发。
预算 manager 在切换时原子替换限制并保留 session 状态；事务 manager 仅在
`ttl`/`maxOpen` 未变化时复用，否则拒绝 reload。

限制：MCP 工具列表在 server 创建时注册。CLI 会拒绝改变全局 tool flags 或
trusted custom procedure 集合的 reload，要求重启，避免 tools/list 与执行快照
不一致。schema resource 继续按当前快照动态生成。

## 相关文档

安全决策见 [security.md](security.md)，公开配置契约见
[configuration.md](configuration.md)，可验证约束见
[invariants.md](invariants.md)。
