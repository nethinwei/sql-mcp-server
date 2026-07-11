# 配置参考

本文件是公开 YAML 配置的单一真相源。完整示例见
[`examples/config.example.yaml`](../examples/config.example.yaml)；
`config.Schema()` 返回供 YAML 编辑器补全和校验使用的 JSON Schema，其中 duration
按 `30s` 这类 YAML 字符串描述；它不是标准 `encoding/json` 输入契约。加载顺序为
YAML 解码、默认值、静态校验、driver 注册检查、secret 解析、provider 连接和
schema drift 检查。

## 顶层

- `version`：契约版本标记；当前值为字符串 `"1"`，加载器保存但暂不限制其取值。
- `server`：默认角色及 HTTP 认证材料。
- `database`：旧式单数据源配置，会迁移为名为 `default` 的数据源。
- `databases`：名称到 `{driver, dsn}` 的映射；内置 driver 为 `postgres`、
  `mysql` 和 `oceanbase`，扩展程序可注册其他名称。必须提供 `database` 或至少
  一个 `databases` 项。
- `entities`：显式暴露的实体列表；可以为空。
- `tools`、`cost`、`budget`、`cache`、`rateLimit`、`mask`、`audit`、
  `transactions`：执行控制。

显式 `--transport`/`--addr` 优先于 `server.transport`/`server.addr`；两处均未
设置时分别回退到 `stdio`/`:8080`。HTTP 默认地址的安全含义和 fail-closed 规则
见[安全模型](security.md)。热重载不是 YAML 字段，通过 `serve --watch` 开启。

## Server 与认证

```yaml
server:
  role: reader
  secrets:
    allowedRoots: [/run/secrets, /var/run/secrets]
  auth:
    token: ""
    trustProxyHeaders: false
    trustedProxyCIDRs: []
    tls:
      cert: ""
      key: ""
      clientCA: ""
```

`token` 是共享 bearer token。只有 DSN 会经过内置 secret resolver，auth 字段
按字面读取，不能写 `${MCP_TOKEN}` 期待环境变量替换；生产环境应生成受限权限的
配置文件或由部署系统渲染。`cert` + `key` 开启 TLS，额外设置 `clientCA` 开启
mTLS；`cert`/`key` 只设置一个会校验失败。`trustProxyHeaders`、
`trustedProxyCIDRs`、identity header 和非 loopback HTTP 的信任规则统一见
[安全模型](security.md)。

## 数据源与实体

每个 database 需要 `driver` 和非空 `dsn`。DSN 可包含 `${ENV}` 或
`${file:/path}`，占位符仅在 DSN 中解析。file 路径必须是
`server.secrets.allowedRoots` 下的绝对路径，且符号链接不能逃逸允许根。
`validate` 会确认 driver 已注册，但不会因此建立数据库连接。

实体字段：

- `name`：MCP 逻辑名，必填；`source` 省略时等于 `name`。
- `datasource`：命名数据源，默认 `default`。
- `schema`、`kind`（`table`/`view`/`procedure`）、`description`。
- `primaryKey`：主键字段，决定 keyset 和主键写保护。
- `fields`：`name`、`alias`、`description`、`mask`、`exclude`。mask 字段只允许
  读取投影，不能用于 filter/cursor/group-by/aggregate/写谓词。
- `roles`：`read`、`create`、`update`、`delete`、`execute`、`aggregate`。
- `fieldACL.<role>.read/write`：角色级字段白名单。
- `rowPolicies.<role>`：`{op, field, value}` 或 `and`/`or` 组合。operator 与
  工具 filter 相同：`eq`、`ne`、`gt`、`gte`、`lt`、`lte`、`in`、
  `not_in`、`like`、`is_null`、`is_not_null`。
- `mcp.dmlTools`：是否加入通用实体工具，省略默认 true。
- `mcp.customTool`：procedure 是否额外注册独立 MCP 工具；它与全局
  `tools.executeEntity` 解耦，后者只控制通用 `execute_entity` 工具。
- `mcp.trustedProcedure`：DBA 已审核 procedure 权限与内部成本；默认 false。
  只有 true 且 CALL fingerprint 命中 `allowTemplates` 时才能执行。
- `params`：procedure 参数的固定位置顺序；省略或空数组表示无参 procedure。
- `relationships`：`name`、`target`、`cardinality`、`joinOn`。当前只支持同
  数据源且恰好一个 join pair 的一层展开。

启动自省会拒绝数据库中缺失的 table/view 或字段；额外数据库列不报错，
procedure 不参与 drift 检查。

## 工具开关

`tools` 支持：

`describeEntities`、`readRecords`、`createRecord`、`updateRecord`、
`deleteRecord`、`executeEntity`、`aggregateRecords`、`beginTransaction`、
`commitTransaction`、`rollbackTransaction`。

省略整个 `tools` 块时采用安全默认：除 `deleteRecord` 外全部开启。只要显式
提供 `tools` 节点（包括全 false/空对象），未设置项就保持 false；它不是逐字段
合并默认值。`executeEntity: false` 不会关闭由实体 `mcp.customTool: true`
注册的独立 procedure 工具；若要关闭后者，必须修改对应实体配置并重启服务。

## 成本

- `enabled`：默认 true。
- `softScore`/`hardScore`：0–100，越高越安全，要求
  `softScore >= hardScore`。省略时分别默认 60/40。
- `maxRows`：读取 SQL 的确定性外层 LIMIT；默认 10000。
- `maxBytes`：响应硬上限，默认 16 MiB。
- `maxProcedureRows`：reviewed procedure 结果硬上限，默认 1000。
- `maxINListSize`、`maxFilterConditions`、`maxGroupByFields`、`maxAggregates`、
  `maxExpand`：默认 256/32/8/16/8；超限在执行前拒绝，expand 按 IN 上限分批。
- `rejectFullScan`、`requireKnownScan`：默认 true；MySQL/OceanBase 使用保守
  EXPLAIN，失败或未知时 fail closed。
- `whitelistPKPoint`：主键点查询只跳过 Estimate，不跳过结果 cap。
- `requirePKForWrite`：默认 true 且不可由 `cost.enabled` 间接关闭。
- `queryTimeout`：Go context 与数据库原生 statement timeout，默认 `30s`。
- `allowTemplates`/`rejectTemplates`：匹配生成 SQL，推荐使用包含 datasource
  隔离的 `fp:v2:<sha256>` fingerprint。精确 SQL 在多数据源配置中必须写成
  `datasource:SQL`，裸 SQL 仅为单数据源旧配置保留兼容。allow 只跳过 Estimate，
  不绕过 mandatory Safety/Enforcement。
- `aqe.windowSize`、`anomalyFactor`、`anomalyMinSamples`、
  `maxFingerprints`：进程内读取反馈窗口和全局模板上限，默认 32/3/5/4096。
- `aqe.explainAnalyze`：默认 false。仅 PostgreSQL 支持；启用后，命中采样的成功
  且实际访问数据库的读取会额外执行一次
  `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` 包装的生成 SQL；缓存命中不采样。
- `aqe.readOnly`：省略时默认 true，启用采样时必须为 true。provider 还会拒绝
  未由 codegen 标记为 `ReadOnly` 的语句。PostgreSQL sampler 始终使用独立
  read-only transaction 并 rollback；调用可能写入的 volatile function 会令采样
  失败，但不会改变已成功的主读取结果。
- `aqe.sampleRate`：`0..1`，默认 0；0 表示不采样，1 表示每次成功读取都采样。
- `aqe.timeout`：独立于主查询的 Go duration，默认 `1s`；启用采样时必须大于 0
  且不超过 `5s`。采样失败只进入 hook/best-effort 审计，不会把成功读取改为失败。

配置启用 `cost.aqe.explainAnalyze` 时，任一 datasource 为 MySQL/OceanBase 都会在校验或
启动装配阶段 fail-fast；多数据源按实体当前 datasource 选择 sampler，不会回退
到其他 provider。

### 多数据源模板迁移

从单数据源升级到 `databases` 多数据源前，必须迁移 `allowTemplates` 和
`rejectTemplates` 中的旧裸 SQL。每项应替换为 datasource 隔离的
`fp:v2:<sha256>` fingerprint，或改成 `datasource:SQL`（例如
`primary:SELECT * FROM users`）。只要多数据源配置中仍存在裸 SQL，配置校验、
启动装配和 reload 都会 fail-closed；reload 失败时继续保留旧运行快照。该检查
同时覆盖 allow 和 reject，避免 allow 错误放行，也避免 reject 静默失效。

## Budget

`budget.roles.<role>` 与 `budget.tenants.<tenant>` 接受：
`maxConcurrent`、`maxExecution`、`maxEstimatedScannedRows`、
`maxReturnedRows`、`maxReturnedBytes`、`maxSessionCost`。旧
`maxScannedRows` 仅作 deprecated alias。零值表示不限，tenant 命中时覆盖而非
合并 role 限制。

tenant 从 subject 的 `tenant`、`tenant_id`、`tenantID` 中按顺序提取。预算是
按 MCP session 隔离的单进程有界内存状态，session 关闭时清理；行数与 cost
限制的执行时点见 [security.md](security.md)。

## Cache、限流、脱敏与审计

- `cache.enabled`、`ttl`（启用时默认 `30s`）、`maxSize`（默认 4096）、
  `maxEntryRows`、`maxEntryBytes`、`preparedMaxSize`。条目数和单条值必须有界；
  prepared 为 0 时禁用。
- `rateLimit.enabled` 默认 true；`maxInflight` 默认 256，`ioPool` 默认 16，
  `cpuPool` 默认逻辑 CPU 数，`minConcurrency` 默认 1，
  `breakerThreshold`/`breakerCooldown` 默认 5/`5s`，
  `connMaxIdleTime`/`connMaxLifetime` 默认 `5m`/`30m`。
  `rttThreshold: 0s` 不触发延迟下降。
  普通 `Submit` 使用 IO pool；只有调用方显式使用 `SubmitCPU`/`SubmitClass`
  才使用 CPU pool，engine 不会自动拆分 SQL 执行阶段。
  `rps > 0` 会装配 token-bucket 限流；0 表示仅使用并发限制。
- `mask.enabled` 默认 true。
- `audit.enabled` 为 true 时 `path` 必填；`queueSize` 默认 1024。启用后向
  `path` 追加 JSON Lines 事件（非 YAML）。

## 事务

`transactions.ttl` 默认 `5m`，`maxOpen` 默认每个角色/subject scope 128；
`beginTimeout`/`commitTimeout`/`rollbackTimeout` 默认 `5s`/`30s`/`30s`。
begin 工具可指定 `datasource`、`isolation` 和 `readOnly`；角色必须在该数据源
至少拥有一个读/聚合或写/执行权限。`readOnly` 省略时为 true，只有具备写权限的
角色可显式请求 false。后续实体工具通过 `transaction` 传 token。隔离值为
`read_uncommitted`、`read_committed`、`repeatable_read`、`serializable`。
