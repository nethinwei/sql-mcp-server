# 安全模型与边界

本文件是安全行为的单一真相源。配置字段见
[configuration.md](configuration.md)。

## 信任边界

stdio 模式使用进程启动时的默认角色，适合由本机 MCP 客户端管理的子进程。

HTTP 支持共享 bearer token 和 mTLS。监听非 loopback 地址时，如果既无 token
也无 `clientCA`，服务拒绝启动；mTLS 还要求服务端证书和私钥。HTTP 请求体默认
限制为 4 MiB，header 默认限制为 1 MiB，并设置 header/idle timeout。
CLI 地址默认值 `:8080` 会监听所有接口，属于非 loopback，因此选择 HTTP 且未
指定 loopback 地址或认证时会 fail closed。
`/healthz` 不鉴权；当前 CLI 没有挂载 `/metrics` handler。

`X-MCP-Role` 和 `X-MCP-Subject` 不是身份认证机制。只有
`server.auth.trustProxyHeaders: true` 时才读取它们，并且必须同时配置 mTLS
`clientCA` 或非空 `trustedProxyCIDRs`。CIDR 模式只接受列表内来源地址；可信网关
仍必须删除外部同名 header、完成认证并重新注入身份。畸形的
`X-MCP-Subject` JSON 返回 HTTP 400。内置 bearer token 只验证共享 secret，
不把 token 映射到独立角色或 subject。项目尚无 OAuth、CORS 策略或持久
session store。

streamable HTTP session 创建时会记录规范化后的 role/subject；后续所有携带
`Mcp-Session-Id` 的 POST/GET/DELETE 必须使用同一身份，否则返回 HTTP 403。
session 关闭时绑定会同步清理。该绑定防止可信代理后的调用方借已有 session
切换身份，但不替代网关认证。

## 授权与数据隔离

- 每个实体按 action 配置角色：read/create/update/delete/execute/aggregate。
- `fieldACL` 可进一步限制角色可读、可写字段。
- 过滤、投影、group-by 和写入字段均先验证为可见字段；未知字段与被排除字段
  返回同类错误，避免借隐藏列建立侧信道。
- `rowPolicies` 与用户 filter 以 AND 合并。`${subject.x}` 从请求 subject
  解析；属性缺失时生成 NULL 条件，因而不匹配普通行。
- mask 在结果返回前执行。内置规则为 `email`、`phone`、`idcard`、`secret`；
  配置未知规则会在启动时失败。
- `describe_entities` 按实体权限和字段 ACL 过滤；procedure 结果仅返回声明为
  visible 且获 `fieldACL.read` 许可的列，未声明列按 fail-closed 丢弃。
- schema resource 按调用者的 read/aggregate 授权过滤，不返回 procedure。

行级策略不是数据库原生 RLS；保护依赖所有访问都经过本服务的工具执行路径。

## SQL 与写保护

工具不接受原始 SQL。值使用 placeholder 参数化；表、字段和 procedure 标识符
来自配置实体并由方言引用。

update/delete 必须有用户 filter 或行级 filter。`requirePKForWrite` 默认开启时，
非完整主键点写由 `WriteGuard` 拒绝；经人工审核的精确 fingerprint 可加入
`allowTemplates` 绕过后续闸门。`delete_record` 默认不注册。MySQL 协议 provider
还会在 DSN 未显式指定时加入 `sql_safe_updates=1`。

## 成本闸门

同步链的真实顺序为：

1. `StaticRule`：先应用 reject fingerprint，再应用主键点查及 allow 白名单。
2. `WriteGuard`：可选的非主键 update/delete 拒绝。
3. `Estimate`：仅 `ExplainCost && ExplainAccurate` 的方言启用。
4. `EnforceCap`：对 read-only SQL 外包一层确定性 `LIMIT maxRows`。

当前只有 PostgreSQL 声明 `ExplainAccurate`，因此 MySQL 和 OceanBase 的
EXPLAIN parser 存在，但不会进入默认同步闸门。`rejectFullScan`、
`requireKnownScan`、`requireFreshStats` 和 score 阈值在没有 Estimate 层时不会
产生判定。`queryTimeout` 使用 Go context 截止时间取消 driver 调用。

必须明确区分“capability 声明”和“已装配保护”：方言声明了 PostgreSQL
statement timeout、MySQL/OceanBase timeout/scan-row cap、OceanBase resource
manager 等能力，但 provider 当前没有执行对应的 `SET` 或租户资源配置；运行时
实际依赖 context timeout、应用层限制及 MySQL 协议的 `sql_safe_updates`。

反馈 store 按 datasource + 方言 + SQL 模板隔离，并用观察到的行数上调后续
PostgreSQL 估算。v0.1 可显式开启 PostgreSQL 只读 `EXPLAIN ANALYZE` 采样；
命中策略时会额外执行一次由 codegen 标记 `ReadOnly` 的生成语句，并递归汇总
`Actual Rows * Actual Loops`。PostgreSQL 在独立 read-only transaction 中采样并
始终 rollback；包含写副作用的 volatile function 会被数据库拒绝。采样使用独立且至多 `5s` 的 context timeout，
失败不改变已经成功的读取结果。该能力默认关闭；MySQL/OceanBase 不支持，配置
启用且存在这些 datasource 时启动失败，不会静默降级。显式事务中的读取不采样，
避免脱离原事务快照再次执行。

## 预算、缓存与审计

角色/租户预算按 MCP session 隔离，状态受 TTL/容量约束并在 session 关闭时清理；
租户配置优先于角色配置。并发和 session cost 可在调用前拒绝，执行时限通过
context 生效。`maxScannedRows` 仅对成本闸门的估算结果做执行前预检查，不能宣称
数据库扫描硬限制。`maxReturnedRows` 在结果迭代期间中止，并计入 expand 子行；
session cost 是“返回条数 + 毫秒耗时”的近似值，重启后清零。

缓存和 singleflight key 包含角色及完整 subject，避免跨身份共享结果。显式事务
在进入 engine 前先验证 token 的 session/角色/subject，并禁用 singleflight。
expand 读取不进入结果缓存，以免父子任一实体写入后留下陈旧组合。显式事务
完全绕过全局读缓存；事务写仅在成功 commit 后按实体失效缓存，rollback 不失效。
审计使用
有界异步队列，满时丢弃并计数；它是 best-effort，不是不可抵赖日志。审计文件以
`0600` 创建，但日志仍可能包含工具输入，运营者应自行保护和轮转。

## Secret

DSN 支持 `${ENV}` 和 `${file:/path}`。缺少环境变量或不可读文件会失败。
`SecretResolver` 可由嵌入方替换，但仓库没有内置 Vault/云 secret manager
客户端。示例不包含真实凭据。
