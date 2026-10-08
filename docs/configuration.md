# 配置参考

完整示例见 [`examples/config.example.yaml`](../examples/config.example.yaml)。

字段的类型、约束、默认值和“修改需重启”标记都定义在 `core/config` 的结构体
标签与 `ApplyDefaults` 中，字段说明在 `core/config/fields.yaml`；
`go generate ./core/config` 由它们生成 `core/config/schema.json`（
`config.Schema()`，供 YAML 编辑器与管理控制台补全和校验）和本文末尾的
[字段参考](#字段参考)，测试保证三者一致。本文其余部分只描述字段之外的行为。

加载顺序为 YAML 解码、默认值、静态校验（含结构体标签中的规则）、driver 注册
检查、secret 解析、provider 连接和 schema drift 检查。同一份配置也可以作为
revision 保存在配置存储中（`serve --store`），加载链路完全相同，另加 store 模式的
密钥规则；见[运维指南](operations.md#配置存储)。

配置中出现未知字段（包括拼写错误）时加载失败并报告行号，不会被静默忽略；
行过滤（`rows`、`rowPolicies`、`tenantPolicy`）是自由结构，不受此限制。

显式 `--transport`/`--addr`/`--role`/`--user` 优先于 `server` 中的同名字段。
热重载不是 YAML 字段，通过 `serve --watch` 开启；标记“修改需重启”的字段在
热重载时被拒绝（store 模式下可以发布，等待重启生效）。

## Server 与认证

只有 DSN 会经过内置 secret resolver，`server.auth` 中的字段按字面读取，不能写
`${MCP_TOKEN}` 期待环境变量替换；生产环境应生成受限权限的配置文件或由部署系统
渲染。HTTP 默认地址的安全含义、identity header 与非 loopback HTTP 的信任规则
统一见[安全模型](security.md)。

stdio 请求和走共享 token 的 HTTP 请求都使用默认身份：`server.user` 优先，
否则为 `server.role`。

## 数据源与实体

DSN 中的 `${file:/path}` 必须是 `server.secrets.allowedRoots` 下的绝对路径，
且符号链接不能逃逸允许根。启动自省会拒绝数据库中缺失的 table/view 或字段；
额外的数据库列不报错，procedure 不参与 drift 检查。

行过滤（`rowPolicies` 的一项、授权的 `rows`、`tenantPolicy`）写作
`{op, field, value}` 或 `and`/`or` 组合，operator 与工具 filter 相同：`eq`、
`ne`、`gt`、`gte`、`lt`、`lte`、`in`、`not_in`、`like`、`is_null`、
`is_not_null`。`value` 可以引用 `${subject.x}`。

## 用户、角色与权限

```yaml
roles:
  analyst:
    description: 业务分析
    grants:
      - entity: orders
        actions: [read, aggregate]
        fields: {read: [id, amount, region]}   # 省略 = 全部可见字段
        rows: {op: eq, field: region, value: CN} # 省略 = 不限制行
users:
  alice:
    tokenHash: "sha256:<64 hex>"
    roles: [analyst]
    subject: {tenant_id: t1}
    grants:
      - entity: refunds
        actions: [read]
```

- 用户有效权限 = 各角色授权 ∪ 直授 `grants`；同名角色（顶层与实体级）的授权
  合并。一次请求只使用**覆盖其全部字段**的授权项，它们的行范围取 OR，再 AND
  实体 `tenantPolicy`；没有单一授权项能覆盖时返回 `AMBIGUOUS_FIELD_SCOPE`，
  约束中列出可选字段集。完整语义见[授权模型设计](design/authorization-model.md)。
- 用户表随热重载生效；删除或禁用用户后其 HTTP 会话被解除并回滚在途事务。
- 未配置 `users` 时行为与之前完全一致。

## 成本

`cost.allowTemplates` 只跳过 Estimate，不绕过 mandatory Safety/Enforcement；
`whitelistPKPoint` 同样只跳过 Estimate，不跳过结果上限。

配置启用 `cost.aqe.explainAnalyze` 时，任一 datasource 为 MySQL/OceanBase 都会
在校验或启动装配阶段 fail-fast；多数据源按实体当前 datasource 选择 sampler，
不会回退到其他 provider。provider 会拒绝未由 codegen 标记为 `ReadOnly` 的语句；
PostgreSQL sampler 始终使用独立 read-only transaction 并 rollback，调用可能写入
的 volatile function 会令采样失败。采样失败只进入 hook/best-effort 审计，不会
把成功读取改为失败。

### 多数据源模板迁移

从单数据源升级到 `databases` 多数据源前，必须迁移 `allowTemplates` 和
`rejectTemplates` 中的旧裸 SQL。每项应替换为 datasource 隔离的
`fp:v2:<sha256>` fingerprint，或改成 `datasource:SQL`（例如
`primary:SELECT * FROM users`）。只要多数据源配置中仍存在裸 SQL，配置校验、
启动装配和 reload 都会 fail-closed；reload 失败时继续保留旧运行快照。该检查
同时覆盖 allow 和 reject，避免 allow 错误放行，也避免 reject 静默失效。

## Budget 与执行控制

预算在 session 关闭时清理；行数与 cost 限制的执行时点见
[security.md](security.md)。`rateLimit` 中普通 `Submit` 使用 IO pool，只有调用
方显式使用 `SubmitCPU`/`SubmitClass` 才使用 CPU pool，engine 不会自动拆分 SQL
执行阶段。

## 事务

begin 工具可指定 `datasource`、`isolation` 和 `readOnly`；角色必须在该数据源
至少拥有一个读/聚合或写/执行权限。`readOnly` 省略时为 true，只有具备写权限的
角色可显式请求 false。后续实体工具通过 `transaction` 传 token。隔离值为
`read_uncommitted`、`read_committed`、`repeatable_read`、`serializable`。

## 字段参考

<!-- BEGIN GENERATED: config-fields -->
<!-- 本节由 internal/schemagen 根据 core/config 的结构体标签与
core/config/fields.yaml 生成（go generate ./core/config），请勿手改。 -->

### `version`：配置版本

配置契约版本标记，当前为 "1"；加载器保存但暂不限制取值。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `version` | 字符串 |  |  | 配置契约版本标记，当前为 "1"；加载器保存但暂不限制取值。 |

### `server`：服务（监听、认证、TLS）

传输方式、默认身份与 HTTP 认证。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `transport` | 字符串 | `"stdio"` | 可选 `stdio`、`http`；修改需重启 | 传输方式；CLI --transport 优先。 |
| `addr` | 字符串 |  | 修改需重启 | HTTP 监听地址；CLI --addr 优先，两处都未设置时为 :8080。非 loopback 地址必须配置 token 或 mTLS。 |
| `role` | 字符串 |  |  | 没有用户身份的请求使用的默认角色；CLI --role 覆盖。 |
| `auth.token` | 字符串 |  |  | 共享 bearer token，按字面读取（不做 ${ENV} 替换）。配置用户后各用户使用自己的 token。 |
| `auth.trustProxyHeaders` | 布尔 |  |  | 信任代理传入的身份头（X-MCP-User / X-MCP-Role / X-MCP-Subject）；需要 mTLS 或 trustedProxyCIDRs 建立信任边界。 |
| `auth.trustedProxyCIDRs` | 字符串列表 |  |  | 允许传入身份头的代理地址段。 |
| `auth.tls.cert` | 字符串 |  |  | TLS 证书文件路径。 |
| `auth.tls.key` | 字符串 |  |  | TLS 私钥文件路径。 |
| `auth.tls.clientCA` | 字符串 |  |  | 设置后开启 mTLS，要求并校验客户端证书。 |
| `auth.tls` | 对象 |  |  | TLS / mTLS。cert 与 key 必须同时设置。 |
| `auth` | 对象 |  | 修改需重启 | HTTP 认证。trustProxyHeaders、trustedProxyCIDRs 与非 loopback 的信任规则见 security.md。 |
| `secrets.allowedRoots` | 字符串列表 | `["/run/secrets","/var/run/secrets"]` |  | 允许读取 secret 文件的根目录（绝对路径）；符号链接不能逃逸这些目录。 |
| `secrets` | 对象 |  |  | DSN 中 ${file:...} 占位符的读取限制。 |
| `user` | 字符串 |  |  | 没有用户身份的请求使用的默认用户，优先于 role；CLI --user 覆盖。必须是已配置且未禁用的用户。 |

### `database`

旧式单数据源配置，加载时迁移为名为 default 的数据源。需要 database 或至少一个 databases 项。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `database` | `database` |  |  | 旧式单数据源配置，加载时迁移为名为 default 的数据源。需要 database 或至少一个 databases 项。 |

### `databases`

数据源名称到连接配置的映射。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `databases` | 映射（名称 → `database`） |  |  | 数据源名称到连接配置的映射。 |

### `entities`

显式暴露给 Agent 的实体；可以为空。启动自省会拒绝数据库中缺失的表、视图或字段。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `entities` | `entity` 列表 | `[{"name":"x","fields":[{"name":"x"}],"roles":{},"mcp":{"dmlTools":true},"relationships":[{"name":"","target":"","cardinality":"","joinOn":null}]}]` |  | 显式暴露给 Agent 的实体；可以为空。启动自省会拒绝数据库中缺失的表、视图或字段。 |

### `roles`

顶层角色：角色名 → 授权。名称规范化为小写。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `roles` | 映射（名称 → `role`） |  | 名称格式 `^[a-z0-9][a-z0-9_-]*$` | 顶层角色：角色名 → 授权。名称规范化为小写。 |

### `users`

通过 HTTP 访问的调用方：用户名 → 配置。增删、禁用、轮换 token 可热加载；首次配置用户或删除全部用户需要重启。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `users` | 映射（名称 → `user`） |  | 名称格式 `^[a-z0-9][a-z0-9_-]*$` | 通过 HTTP 访问的调用方：用户名 → 配置。增删、禁用、轮换 token 可热加载；首次配置用户或删除全部用户需要重启。 |

### `tools`：工具开关

省略整个块时除 deleteRecord 外全部开启；一旦写出该块，未写的工具保持关闭。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `describeEntities` | 布尔 |  |  | 列出可访问的实体与字段。 |
| `readRecords` | 布尔 |  |  | read_records：按条件读取记录。 |
| `createRecord` | 布尔 |  |  | create_record：新增记录。 |
| `updateRecord` | 布尔 |  |  | update_record：按主键修改记录。 |
| `deleteRecord` | 布尔 |  |  | delete_record：按主键删除记录。 |
| `executeEntity` | 布尔 |  |  | execute_entity：执行存储过程实体。不影响实体 mcp.customTool 注册的独立工具。 |
| `aggregateRecords` | 布尔 |  |  | aggregate_records：分组统计。 |
| `beginTransaction` | 布尔 |  |  | 开启事务。 |
| `commitTransaction` | 布尔 |  |  | 提交事务。 |
| `rollbackTransaction` | 布尔 |  |  | 回滚事务。 |

### `cost`：成本闸门与输入上限

执行前的成本闸门与输入上限。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `enabled` | 布尔 | `true` |  | 是否启用基于 EXPLAIN 的成本闸门。不影响 requirePKForWrite。 |
| `softScore` | 整数 | `60` | 0–100 | 安全分（越高越安全）低于它时软拒绝，并返回缩小查询的提示；需 ≥ hardScore。 |
| `hardScore` | 整数 | `40` | 0–100 | 安全分低于它时直接拒绝。 |
| `maxRows` | 整数 | `10000` | ≥ 1 | 读取 SQL 的外层 LIMIT。 |
| `maxBytes` | 整数 | `16777216` | ≥ 1 | 单次响应的字节上限。 |
| `maxINListSize` | 整数 | `256` | ≥ 1 | IN 列表最多元素数，关系展开也按它分批。 |
| `maxFilterConditions` | 整数 | `32` | ≥ 1 | 一次请求最多过滤条件数。 |
| `maxGroupByFields` | 整数 | `8` | ≥ 1 | 最多分组字段数。 |
| `maxAggregates` | 整数 | `16` | ≥ 1 | 最多聚合表达式数。 |
| `maxExpand` | 整数 | `8` | ≥ 1 | 最多展开的关系数。 |
| `maxProcedureRows` | 整数 | `1000` | ≥ 1 | 存储过程结果行数上限。 |
| `rejectFullScan` | 布尔 | `true` |  | 拒绝全表扫描。 |
| `whitelistPKPoint` | 布尔 | `true` |  | 主键点查询跳过估算（仍受结果上限约束）。 |
| `requirePKForWrite` | 布尔 | `true` |  | 写操作必须带主键条件；不随 cost.enabled 关闭。 |
| `requireKnownScan` | 布尔 | `true` |  | 无法判断扫描方式时拒绝；MySQL/OceanBase 的 EXPLAIN 失败即拒绝。 |
| `requireFreshStats` | 布尔 |  |  | 统计信息过旧时拒绝。 |
| `queryTimeout` | 时长 | `"30s"` | ≥ 0 | 查询超时，同时下发为数据库语句超时。 |
| `allowTemplates` | 字符串列表 |  |  | 跳过估算的 SQL 模板：推荐 fp:v2:<sha256> 指纹；多数据源下精确 SQL 写成 datasource:SQL。不绕过安全检查。 |
| `rejectTemplates` | 字符串列表 |  |  | 总是拒绝的 SQL 模板，写法同 allowTemplates。 |
| `aqe.windowSize` | 整数 | `32` | ≥ 0 | 每个模板保留的反馈样本数。 |
| `aqe.anomalyFactor` | 数字 | `3` | ≥ 0 | 实际与估算偏差超过该倍数视为异常。 |
| `aqe.anomalyMinSamples` | 整数 | `5` | ≥ 0 | 判断异常前至少需要的样本数。 |
| `aqe.explainAnalyze` | 布尔 |  |  | 对采样读取额外执行 EXPLAIN (ANALYZE, BUFFERS)。仅 PostgreSQL；缓存命中不采样。 |
| `aqe.readOnly` | 布尔 | `true` |  | 采样只在只读事务中执行并回滚；启用采样时必须为 true。 |
| `aqe.sampleRate` | 数字 |  | 0–1 | 采样比例；0 不采样，1 每次成功读取都采样。 |
| `aqe.timeout` | 时长 | `"1s"` | 0–5s | 采样超时，独立于主查询；启用采样时必须大于 0。 |
| `aqe.maxFingerprints` | 整数 | `4096` | ≥ 1 | 全局最多跟踪的模板数。 |
| `aqe` | 对象 |  |  | 基于实际执行反馈的自适应估算与可选的 EXPLAIN ANALYZE 采样。 |

### `budget`：预算

按 MCP 会话隔离的资源预算，进程内有界状态；0 表示不限。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `roles` | 映射（名称 → `budgetLimits`） |  |  | 按角色的预算：角色名 → 限额。 |
| `tenants` | 映射（名称 → `budgetLimits`） |  |  | 按租户的预算，命中时覆盖而非合并角色限额。租户取自 subject 的 tenant / tenant_id / tenantID。 |
| `users` | 映射（名称 → `budgetLimits`） |  |  | 按用户的预算，优先于角色；未配置时逐项取其各角色中最宽松的值（任一角色未配置即视为不限）。 |

### `cache`：缓存

读取结果缓存；条目数和单条大小始终有界。下表默认值是启用缓存后的取值。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `enabled` | 布尔 |  |  | 是否启用读取缓存。 |
| `ttl` | 时长 | `"30s"` | ≥ 0 | 缓存有效期。 |
| `maxSize` | 整数 | `4096` | ≥ 0 | 最多缓存条目数。 |
| `maxEntryRows` | 整数 | `10000` | ≥ 0 | 单条缓存最多行数；默认等于 cost.maxRows，启用结果缓存时必须大于 0。 |
| `maxEntryBytes` | 整数 | `16777216` | ≥ 0 | 单条缓存最多字节数；默认等于 cost.maxBytes，启用结果缓存时必须大于 0。 |
| `preparedMaxSize` | 整数 |  | ≥ 0 | 预编译语句缓存条目数，与结果缓存相互独立；0 表示不缓存。 |

### `rateLimit`：限流与熔断

并发控制、限流、熔断与连接池。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `enabled` | 布尔 | `true` |  | 是否启用。 |
| `rps` | 数字 |  | ≥ 0 | 每秒请求数上限（令牌桶）；0 表示只做并发限制。 |
| `maxInflight` | 整数 | `256` | ≥ 0 | 同时进行的请求上限。 |
| `ioPool` | 整数 | `16` | ≥ 0 | IO 工作池大小；普通执行使用它。 |
| `cpuPool` | 整数 |  | ≥ 0 | CPU 工作池大小；默认为逻辑 CPU 数。只有显式提交 CPU 任务时使用。 |
| `minConcurrency` | 整数 | `1` | ≥ 0 | 自适应并发的下限。 |
| `rttThreshold` | 时长 |  | ≥ 0 | 延迟超过该值时降低并发；0s 不触发。 |
| `breakerThreshold` | 整数 | `5` | ≥ 0 | 连续失败多少次后熔断。 |
| `breakerCooldown` | 时长 | `"5s"` | ≥ 0 | 熔断后多久重试。 |
| `connMaxIdleTime` | 时长 | `"5m"` | ≥ 0 | 数据库连接最长空闲时间。 |
| `connMaxLifetime` | 时长 | `"30m"` | ≥ 0 | 数据库连接最长存活时间。 |

### `mask`：脱敏

字段脱敏。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `enabled` | 布尔 | `true` |  | 是否对配置了 mask 的字段脱敏。 |

### `audit`：审计

审计日志，以 JSON Lines 追加写入。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `enabled` | 布尔 |  |  | 是否写审计日志；启用时必须设置 path。 |
| `path` | 字符串 |  |  | 审计日志文件路径。 |
| `queueSize` | 整数 | `1024` | ≥ 0 | 异步写入队列长度；队列满时丢弃事件并计数。 |

### `transactions`：事务

事务工具的限制。begin 可指定 datasource、isolation 与 readOnly；只有具备写权限的角色能请求 readOnly false。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `ttl` | 时长 | `"5m"` | ≥ 0；修改需重启 | 事务最长存活时间。 |
| `maxOpen` | 整数 | `128` | ≥ 0；修改需重启 | 每个角色/subject 同时打开的事务上限。 |
| `beginTimeout` | 时长 | `"5s"` | ≥ 0 | 开启事务超时。 |
| `commitTimeout` | 时长 | `"30s"` | ≥ 0 | 提交超时。 |
| `rollbackTimeout` | 时长 | `"30s"` | ≥ 0 | 回滚超时。 |

### 定义 `database`

一个数据源连接。validate 会确认 driver 已注册，但不会建立连接。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `driver` | 字符串 |  | 必填；格式 `^[a-z][a-z0-9_-]*$` | 数据库驱动；内置 postgres、mysql、oceanbase，扩展程序可注册其他名称。 |
| `dsn` | 字符串 |  | 必填；非空 | 连接串，可包含 ${ENV} 或 ${file:/path} 占位符（仅 DSN 解析占位符；file 路径须在 server.secrets.allowedRoots 下）。 |

### 定义 `entity`

一个表、视图或存储过程。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | 字符串 |  | 必填；非空 | MCP 逻辑名。 |
| `source` | 字符串 |  |  | 数据库中的表、视图或过程名；省略时等于 name。不能使用保留前缀 smcp_。 |
| `datasource` | 字符串 |  |  | 所在数据源；省略时为 default。 |
| `schema` | 字符串 |  |  | 数据库 schema。 |
| `kind` | 字符串 |  | 可选 `table`、`view`、`procedure` | 实体类型；省略时为 table。 |
| `description` | 字符串 |  |  | 给 Agent 看的实体说明。 |
| `primaryKey` | 字符串列表 |  |  | 主键字段，决定 keyset 分页与主键写保护。 |
| `fields.name` | 字符串 |  | 必填；非空 | 数据库列名。 |
| `fields.alias` | 字符串 |  |  | 对 Agent 暴露的名称；省略时用列名。 |
| `fields.description` | 字符串 |  |  | 给 Agent 看的字段说明。 |
| `fields.mask` | 字符串 |  | 内置 `email`、`idcard`、`phone`、`secret` | 脱敏规则：内置规则或扩展程序注册的规则，未知规则会拒绝启动。脱敏字段只能出现在读取结果中，不能用于过滤、游标、分组、聚合或写谓词。 |
| `fields.exclude` | 布尔 |  |  | 隐藏该字段，Agent 不可见。 |
| `fields` | 对象列表 |  |  | 暴露的字段。 |
| `roles.read` | 字符串列表 |  |  | 可读取的角色。 |
| `roles.create` | 字符串列表 |  |  | 可新增的角色。 |
| `roles.update` | 字符串列表 |  |  | 可修改的角色。 |
| `roles.delete` | 字符串列表 |  |  | 可删除的角色。 |
| `roles.execute` | 字符串列表 |  |  | 可执行存储过程的角色。 |
| `roles.aggregate` | 字符串列表 |  |  | 可聚合统计的角色。 |
| `roles` | 对象 |  |  | 旧式实体级授权：动作 → 允许的角色。与顶层 roles 合并到同名角色。 |
| `fieldACL.read` | 字符串列表 |  |  | 可读字段。 |
| `fieldACL.write` | 字符串列表 |  |  | 可写字段。 |
| `fieldACL` | 映射（名称 → 对象） |  |  | 旧式角色级字段白名单：角色 → 可读、可写字段。 |
| `mcp.dmlTools` | 布尔 | `true` |  | 是否加入通用实体工具（读、写、聚合）；省略时为 true，显式 false 会保留。 |
| `mcp.customTool` | 布尔 |  |  | 存储过程额外注册独立 MCP 工具；与 tools.executeEntity 无关。修改需要重启。 |
| `mcp.trustedProcedure` | 布尔 |  |  | DBA 已审核该过程的权限与内部成本。只有为 true 且 CALL 指纹命中 allowTemplates 时才能执行。 |
| `mcp` | 对象 |  |  | 实体在 MCP 中的暴露方式。 |
| `rowPolicies` | 映射（名称 → 自由对象） |  |  | 旧式角色级行范围：角色 → 过滤条件（{op, field, value} 或 and/or 组合）。 |
| `relationships.name` | 字符串 |  | 必填；非空 | 关系名，展开时使用。 |
| `relationships.target` | 字符串 |  | 必填；非空 | 目标实体名。 |
| `relationships.cardinality` | 字符串 |  | 必填；可选 `one`、`one-to-one`、`belongs-to`、`many`、`one-to-many`、`has-many` | 基数；belongs-to/one 展开为单个对象，has-many/many 展开为列表。 |
| `relationships.joinOn` | 映射（名称 → 字符串） |  | 必填 | 连接键：本实体字段 → 目标实体字段。 |
| `relationships` | 对象列表 |  |  | 可展开的关系。当前只支持同数据源、恰好一个连接键的一层展开。 |
| `tenantPolicy` | 自由对象 |  |  | 租户硬边界，语法同一条行过滤；对所有主体始终 AND，不参与多角色合并。引用的 ${subject.x} 缺失时匹配零行。 |
| `params` | 字符串列表 |  |  | 存储过程参数的固定位置顺序；省略或空表示无参。 |

### 定义 `role`

一个角色。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `description` | 字符串 |  |  | 角色说明。 |
| `grants` | `grant` 列表 |  |  | 角色的授权项。 |
| `permissions` | 字符串列表 |  |  | 系统权限（如 sql:execute@<datasource>），为后续版本预留；本版声明任何值都会校验失败。 |

### 定义 `grant`

一条实体授权。一次请求只使用覆盖其全部字段的授权项，它们的行范围取 OR，再 AND 实体 tenantPolicy。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `entity` | 字符串 |  | 必填；非空 | 授权的实体名。 |
| `actions` | 字符串列表 |  | 必填；可选 `read`、`create`、`update`、`delete`、`execute`、`aggregate`；至少 1 项 | 允许的动作。 |
| `fields.read` | 字符串列表 |  |  | 可读字段。 |
| `fields.write` | 字符串列表 |  |  | 可写字段。 |
| `fields` | 对象 |  |  | 字段范围；省略表示全部可见字段（含以后新增的字段）。 |
| `rows` | 自由对象 |  |  | 行范围，语法同行过滤；省略表示不限制。 |

### 定义 `user`

一个用户。有效权限为各角色授权与直授 grants 的并集。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `description` | 字符串 |  |  | 用户说明。 |
| `tokenHash` | 字符串 |  | 格式 `^sha256:[0-9a-f]{64}$` | bearer token 的 sha256 hash，由 sql-mcp-server user token 生成；明文不进入配置。各用户之间、与 server.auth.token 之间不能重复。 |
| `roles` | 字符串列表 |  | 格式 `^[a-z0-9][a-z0-9_-]*$` | 用户拥有的角色，可以是顶层角色或实体级授权中出现的角色名。 |
| `subject` | 自由对象 |  |  | 固定的主体属性，供 ${subject.x} 解析；优先于代理 X-MCP-Subject 中的同名属性。 |
| `grants` | `grant` 列表 |  |  | 只给这个用户的直授授权。 |
| `permissions` | 字符串列表 |  |  | 系统权限，为后续版本预留；本版声明任何值都会校验失败。 |
| `disabled` | 布尔 |  |  | 禁用后无法认证，也不能作为 server.user；会话立即解除。 |

### 定义 `budgetLimits`

一组预算限额；0 表示不限。

| 字段 | 类型 | 默认值 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `maxConcurrent` | 整数 |  | ≥ 0 | 同时执行的请求数上限。 |
| `maxExecution` | 时长 |  | ≥ 0 | 单次执行时长上限。 |
| `maxEstimatedScannedRows` | 整数 |  | ≥ 0 | 单次调用的估算（及实际）扫描行数上限。 |
| `maxScannedRows` | 整数 |  | ≥ 0 | 已弃用，等同 maxEstimatedScannedRows。 |
| `maxReturnedRows` | 整数 |  | ≥ 0 | 单次调用返回行数上限。 |
| `maxReturnedBytes` | 整数 |  | ≥ 0 | 单次调用返回字节上限。 |
| `maxSessionCost` | 整数 |  | ≥ 0 | 会话累计成本上限。 |

<!-- END GENERATED: config-fields -->

## 确定性导出

`sql-mcp-server export --config config.yaml` 将生效配置输出为确定性 YAML：

- 字段顺序固定（顶层与嵌套结构按契约顺序，map 键按字典序）；
- 默认值由与启动相同的加载器物化，输出是完整的生效配置；
- 实体、字段与旧式访问配置中的空值省略（空字符串、false、空列表、空映射），
  省略与写出空值语义相同；缺省即开启的开关（`cost.enabled`、
  `cost.requirePKForWrite`、`rateLimit.enabled`、`mask.enabled`）由默认值物化为
  `true`。缺省会触发默认值的键（`tools`、`cost`、实体 `mcp.dmlTools`）始终写出，
  显式的 false 与 0 不会丢失；
- secret 占位符（`${ENV}`、`${file:...}`）原样保留，绝不解析为明文；配置中
  直接写入的字面量按原样输出。

同一有效配置重复导出字节级一致，导出结果可直接通过 `validate` 并再次导出得到
相同字节。输出的兼容规则见 [tool-contract.md](tool-contract.md)。
