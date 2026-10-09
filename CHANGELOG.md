# Changelog

本项目的重要变更记录于此。格式参考
[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本遵循
[Semantic Versioning](https://semver.org/lang/zh-CN/)。

CHANGELOG 只维护版本级摘要和 breaking 提示；完整能力、迁移步骤、证据边界与
版本时点限制在对应 `docs/releases/vX.Y.Z.md` 中维护。

## Unreleased

## 0.1.12 - 2026-10-09

升级步骤汇总见 [`docs/releases/v0.1.12.md`](docs/releases/v0.1.12.md#迁移)。

### Security

- 构建与发布改用 Go 1.26.9 与 `golang.org/x/net` v0.60.0，修复 govulncheck 报告的
  `net/http`、`crypto`、`html/template` 与 x/net 漏洞。

### Changed

- 控制台：导入的同名表不再加数据源或库前缀；实体列表与权限矩阵按 `数据源 · 库`
  分组，名称与表名不同时才显示表名；引用因新增、改名或移动实体产生歧义时自动改为
  限定写法。示例去掉 `warehouse_tenants`、`archive_orders` 等前缀，`verify.py`
  增加实体命名验证。
- CLI：`add entity` 新增 `--schema`，按 `数据源.库.名称` 查重；`explain --entity` 按引用
  规则解析并输出 `id`。
- 自省按 schema 批量读取元数据（参考 mysqldump）：PostgreSQL 与
  MySQL/OceanBase 的表、列、键、外键、级联、触发器各一次查询，往返次数与表数
  无关；MySQL 只读取目标库的 `information_schema`（此前读取全实例后在内存过滤）。
  本地 600 张表：PostgreSQL 0.98s → 58ms，MySQL 1.32s → 18ms。连接权限探测按
  schema 批量。
- 热重载改为发布后排空（publish-then-drain）：新快照立即生效，新请求不再等待
  旧快照的在途请求（此前一个慢查询或长时间的控制台扫描会让全部新请求停住）；
  旧快照在后台关闭。新旧快照共享连接池（设置未变不重连）、IO 配额与限流熔断、
  读缓存（按物理数据库失效，旧快照的写入同样使新快照的缓存失效，失效后不回填
  旧值）；待回收快照最多 3 个。被吊销用户的会话在发布时关闭，排空后按会话 ID
  回滚，重新启用后新建的会话不受影响（TM-007）。
- 除传输方式、监听地址与 TLS 开关外的配置均可热加载：认证（token、可信代理、
  用户的首次启用与全部删除；mTLS 下每个请求按当前 CA 校验连接出示的客户端
  证书，切换前的连接不能冒充 mTLS 通道）、TLS 证书与客户端 CA 轮换、工具集合与 custom
  procedure tool（客户端收到 `tools/list_changed`）、事务 `ttl`/`maxOpen`。新认证在
  reload 装配前准备好（含读取证书，失败则 reload 失败），发布时直接切换，不再读文件。
- 读缓存失效按数据库索引、写入时按到期堆淘汰，不再扫描全部条目；写入的失效
  目标在装配时预计算，按（物理库, 关系）去重，整库失效合并同库其他目标；事务
  容量按作用域计数。
- 显式事务绑定开启时的连接：重载把其数据源改到别的连接后，事务内后续语句返回
  新错误码 `TRANSACTION_STALE`，只能回滚后重新开启（此前会按新配置在旧连接上
  执行，并失效新数据库的缓存）。连接未变的重载不受影响。
- 启动与重载：各数据源并行对账、自省整体限时 1 分钟；连接权限探测移到发布后的
  后台执行（限时 2 分钟），不再阻塞启动与重载。
- 控制台导入改为“库 → 表”：先列出库，展开时才扫描该库；扫描是后台任务（有界
  worker 池、独立单连接、可取消、限时），结果按读连接保留到下次重新扫描；候选
  实体的命名与关系跨已扫描的库计算。导入页列出全部索引（结构、键部分、条件，
  按各数据库自己的术语），键从同一次索引查询得出。导入页的状态计算不再是表数 × 实体数，表格
  分页。
- 新增热路径 Go benchmark（`make bench`，无需 Docker）与真实数据库的元数据
  扫描、权限探测 benchmark（`make bench-integration`），基线与已知热点见
  `docs/benchmarks/hot-paths.md`。e2e 套件共享一个 PostgreSQL 容器、每个测试
  一个新数据库，`make test-e2e` 约 12 s → 4 s。

### Fixed

- 自动提交的写入级联到同一物理库的其他数据源时，会话随后经这些数据源的读取
  也走写连接（此前只有直接写入的数据源如此，显式事务提交则记录全部）；两条
  路径共用同一个写入后处理。
- MySQL/OceanBase 成本闸门：`EXPLAIN` 解析遍历整棵计划树（聚合、排序与连接时表
  节点嵌套在 `grouping_operation`/`ordering_operation`/`nested_loop` 或 OceanBase
  的 `CHILD_n` 下），按最差的访问方式与最大的行估计评分；此前这些查询一律被判为
  “未知扫描”并被拒绝，MySQL/OceanBase 上带过滤的聚合无法执行。
- OceanBase 自省：表达式、全文与空间索引的键部分被报告为隐藏列
  （`SYS_NC…$`、`__word_segment…`、`__cellid…`），此前被当成真实列，表达式唯一键
  会被误认为能唯一定位行；现在不是表列的键部分按表达式处理。MySQL 8.0.13+ 与
  OceanBase 的表达式索引显示表达式原文；OceanBase 全文与空间索引在
  `information_schema` 中只报告内部列，改用其文档推荐的 `SHOW INDEX`（仅对这些表
  逐表读取）得到真实列与类型。
- MySQL/OceanBase 成本闸门的行估计取任一计划节点的最大值（连接的输出可能远大于
  各表的扫描行数），此前只取扫描节点，可能低估连接结果。
- 读缓存过期堆弹出时清空底层数组的引用，被删除的结果可以被回收。
- 示例改为三种数据源各一套覆盖全部特性的业务（PostgreSQL 电商、MySQL 仓储、
  OceanBase 记账，各经只读与读写两个账号接入，并有跨数据源的同名表），附特性矩阵与 `verify.py`
  逐项验证；初始化脚本声明 `utf8mb4`，修正 MySQL/OceanBase 示例的中文乱码。

### Breaking

- 管理 API：移除 `schemaImport`，改为 `schemaList`、`schemaTables`（一次返回
  数据源已扫描库的表）、`startSchemaScan`、`schemaScan`（只返回任务状态）、
  `cancelSchemaScan`。
- Go API：`introspect.PrivilegeInspector.TablePrivileges` 改为按 schema 批量
  （`tables []string` → `map[string]TablePrivileges`）；`bootstrap.App.Capabilities`
  字段改为方法 `Capabilities()`（评估完成前为 nil），新增 `WaitCapabilities`。
- Go API：`cache.Cache` 新增 `Stamp`，`Set` 增加失效戳参数，`cache.Key.Database`
  改为物理数据库标识（新增 `Datasource`、`Generation`）；`tool.CacheTarget` 改为
  `Physical`/`Relation`，`tool.WriteTargets` 改为结构体；`Runtime.OnRevokedPrincipals` 回调改为返回关闭的会话 ID；
  `App.OpenScan` 接收数据源配置；新增 `bootstrap.Shared`、`Runtime.OnPublish`、
  `engine.Quota`/`WithQuota`、`mcpserver.HTTPAuth`/`PrepareHTTPAuth`/`PreparedAuth` 与
  `HTTPConfig.AuthChanges`。
- 实体改为命名空间身份 `数据源.库.名称`：名称只需在同一数据源与库内唯一（此前全局
  唯一），不得含点（数据源名、库名同样）。授权、关系、`affects` 与 Agent 调用的
  `entity` 写 `名称`、`库.名称`、`数据源.名称` 或完整 ID，必须恰好对应一个实体；
  有歧义的配置引用使校验与发布失败，Agent 侧只在调用方能访问的实体中解析，歧义时
  返回新错误码 `AMBIGUOUS_ENTITY`（`constraints.candidates` 列出无歧义写法）。
  现有配置的名称全局唯一，无需修改。
- describe 与授权 schema 资源的 `name` 为调用方可访问范围内的最短无歧义写法，
  新增 `id`（规范 ID）；审计、能力报告与管理 API 的权限可见性按规范 ID 标识实体；
  GraphQL `Entity` 新增 `id`。
- 过程工具名的哈希改为对规范 ID 计算，工具名后缀随之变化（名称部分仍是过程名）。
- Go API：`entity.Entity.Name` 为规范 ID，新增 `Local`、`entity.ID`/`ReferencesOf`、
  `Registry.Match`/`ShortName`（`Resolve` 接受任一引用写法）；新增
  `config.EntityRefs`、`EntityConfig.ID`；`tool.ProcedureToolName` 改为接收实体。

## 0.1.11 - 2026-10-08

### Added

- 配置存储与 revision：`serve --store <driver>:<dsn>`（或 `SQL_MCP_STORE`）
  从 SQLite（纯 Go `modernc.org/sqlite`）、PostgreSQL、MySQL 或 OceanBase 中的
  已发布 revision 启动，`--watch` 轮询新发布并热重载，失败时保留旧快照并在
  `/readyz/snapshot` 返回 `X-Snapshot-Stale`；新增 `store init/import/list/show/
  diff/publish/rollback` 与 `migrate` 子命令。设计见
  `docs/design/config-store.md`。
- 管理 API 与控制台（`serve --store ... --admin`，挂在 `/admin`）：GraphQL 管理
  API（草稿、校验、差异、模拟、发布与回滚，发布带乐观并发检查）与嵌入二进制的
  Vue 控制台（数据源导入、权限矩阵、可见范围预览、版本历史、运行状态、其他设置
  编辑器）。本地管理员账号（argon2id，`sql-mcp-server admin` 子命令引导）；密码
  校验全局限并发并限制排队，改密立即吊销该账号的所有会话；自己的密码任何登录者
  可改，他人的密码需要 `admin:accounts`。schema 导入按 schema 加表名识别表，
  外键记录被引用表的 schema（`entity.ForeignKey.RefSchema`）。发布产物（GoReleaser、
  镜像）在构建前编译控制台。设计见 `docs/design/admin-api.md`。
- 实体 `source` 保留前缀 `smcp_`，introspection 跳过该前缀的表；威胁 TM-011、
  不变量 I27、I28。

- 用户、角色与权限模型：顶层 `roles`（授权项 grants）与 `users`（每用户
  `tokenHash`、多角色、直授 grants、固定 subject、`disabled`），实体
  `tenantPolicy` 租户硬边界，`budget.users`，`server.user`/`--user` 默认用户，
  `sql-mcp-server user token` 生成 token 与 hash。多角色按请求选择覆盖集合并，
  不对字段与行分别取并集。设计见 `docs/design/authorization-model.md`。
- HTTP 每用户 bearer 认证与可信代理 `X-MCP-User`；热重载删除或禁用用户时解除其
  会话并回滚在途事务。
- 拒绝码 `AMBIGUOUS_FIELD_SCOPE`（retryable，`constraints.fieldScopes`）与审计
  字段 `user`、`roles`、`grants`（兼容变化）。
- 威胁 TM-009（多角色合并越权与租户打穿）、TM-010（用户身份伪造与吊销残留），
  不变量 I25、I26。

- 一个数据源可配置多个连接（`connections`，`role: primary|replica`）并按动作路由
  （`routing.read/write/execute`）；`readAfterWrite` 让同一会话写入后的一段时间内读走写连接；
  只读事务走读连接，读写事务固定在写连接。原有 `dsn` 简写等价于名为 `default` 的单连接。
  连接可声明 `pooler: transaction`（pgbouncer 等事务模式代理），关闭预编译语句与依赖会话的
  超时参数。同一数据源各连接解析出的默认 schema 不同时，未设 `schema` 的实体启动即报错。
  见 [数据源模型](docs/design/datasource-model.md)。
- 连接权限感知：启动与重载时按路由连接探测表级、列级与过程的权限和服务器只读状态，
  授权超出连接能力时记录告警；管理 API 新增 `capabilities` 查询，`validate` 返回 `warnings`
  （不阻断发布），控制台权限矩阵把连接无权执行的动作置灰并提示原因，审查页列出告警。
- 拒绝码 `DATASOURCE_FORBIDDEN`（不可重试）：数据库以权限不足或只读拒绝语句时返回，不计入
  熔断（兼容变化）。

### Breaking

升级步骤汇总见 [`docs/releases/v0.1.11.md`](docs/releases/v0.1.11.md#迁移)。

- **配置中的未知字段直接拒绝加载**（此前静默忽略）：拼错的键（如
  `cost.maxRow`）会让 `validate`、启动、reload 与 store publish 失败，并报告
  行号与字段名；升级前先用 `sql-mcp-server validate` 检查现有配置。行过滤
  （`rows`、`rowPolicies`、`tenantPolicy`）仍是自由结构。
- MySQL/OceanBase 未设 `schema` 的实体解析到连接的当前数据库（与生成的 SQL 一致），
  不再在所有数据库中按表名查找；DSN 未指定数据库时这类实体启动即报缺失（此前能
  启动，但查询会因未选择数据库而失败）。请为实体设置 `schema` 或在 DSN 中指定数据库。
- 字段规则改由结构体 `schema` 标签统一校验，此前只写在 JSON Schema 里的约束开始
  生效：`server.transport`、实体 `kind` 的取值，以及各字段的范围与格式；部分
  校验错误的措辞随之改为 `config: <路径> ...` 形式。`config.Validate` 现在要求
  先调用 `ApplyDefaults`（所有加载器都会这样做），未物化默认值的程序化配置会因
  上限为 0 被拒绝。
- 删除从未生效的配置项 `rateLimit.cpuPool`（engine 没有 CPU 任务）与已弃用别名
  `budget.*.maxScannedRows`；由于未知字段直接拒绝，旧配置需删除前者、把后者改名为
  `maxEstimatedScannedRows`。旧版本会把 `cpuPool` 默认值写进每个 store revision，
  因此 store 部署升级后无法启动、也无法回滚到旧 revision：用 `store show <id>` 导出
  当前 revision，删去该键后 `store import`，再 `store publish --restart-required`。
  当前已发布 revision 无法加载时，发布只接受 `restartRequired`（此前无论如何都会
  被拒绝，store 无法恢复）。
- 一个物理表或视图最多对应一个实体（[数据源模型](docs/design/datasource-model.md) I-1）。同一数据源中
  名称完全相同的重复在校验和发布时拒绝；按默认 schema、MySQL `lower_case_table_names` 和服务器身份
  （多个数据源指向同一个库）解析后的重复在启动和重载时拒绝。此前把同一张表配成两个实体（例如分别走
  只读和读写两个 DSN）会产生两套策略和互不失效的缓存；迁移方式是只保留一个实体，多个账号改用同一数据源
  的多个连接（`connections` 与 `routing`）。
- 表的主键与唯一键以数据库为准：配置的 `primaryKey` 与数据库不一致时启动告警，并以数据库的键为准；
  此前在没有主键约束的表上声明 `primaryKey` 可让修改和删除通过写保护，现在不再生效。视图上声明的键只
  用于读取。`primaryKey` 与新增的 `uniqueKeys` 中的列必须是实体字段。
- 外键级联写入（`ON DELETE CASCADE`、`SET NULL`、`SET DEFAULT`、`ON UPDATE CASCADE`）需要调用方对级联链上
  每一层的实体拥有不带行范围的删除或修改权限（修改须能写被改写的外键列），级联到未暴露的表时拒绝；实体可用 `allowCascade: true`
  显式放开（[数据源模型](docs/design/datasource-model.md) I-6）。
- `read_records` 的 `cursor` 必须是游标键（主键，或第一个身份唯一键）的前缀；此前无法使用时被静默
  忽略，会重复返回第一页。
- `read_records` 的 `offset` 必须与 `limit` 同时给出（此前 `offset` 被静默忽略），
  输入 schema 增加 `dependentRequired`；`describe_entities` 拒绝非法输入（此前忽略
  后列出全部实体）。删除永不产生的拒绝码 `NOT_IMPLEMENTED`。
- Go API：删除仅测试使用的 `bootstrap.NewRuntime`、`mysql.New`、`mysql.NewAdapter`、
  `oceanbase.New`、`providerregistry.KnownDrivers`、`config.Bool`（用 `new(v)`）等；
  `store.Tx` 去掉无调用方的 `Savepoint`/`RollbackTo`，`dialect.Dialect` 去掉
  `ExplainSQL`，`dialect.Capabilities` 只保留被读取的字段；`hook.Hooks.AfterTool`
  去掉恒为 nil 的 result 参数；`budget.Manager` 收敛为单一的预留接口。
  `providerregistry.Factory` 与 `providerregistry.New` 改为接收 `providerregistry.Options`
  （超时与 pooler），`tool.DataSource` 按动作拆分连接。

### Changed

- 配置规则单一来源：字段的类型、约束、默认值与“修改需重启”标记定义在
  `core/config` 的结构体标签与 `ApplyDefaults` 中，字段说明在
  `core/config/fields.yaml`（中英文）；`go generate ./core/config` 生成
  `schema.json`（现含 `additionalProperties: false`、默认值、说明与 `x-restart`）
  和 `docs/configuration.md` 的字段参考，测试保证一致。静态校验、热重载重启判定
  （错误中改为列出配置路径）、管理控制台的提示与选项都从这里读取。
- `ApplyDefaults` 把缺省即开启的 `cost.enabled`、`cost.requirePKForWrite`、
  `rateLimit.enabled`、`mask.enabled` 物化为 `true`，导出结果随之写出这些键。
- 热重载守卫从 CLI 下沉为 `bootstrap.CheckHotReload`，文件 reload、store reload
  与 store publish 共用；事务 `ttl`/`maxOpen` 变化在 reload 构建阶段即被拒绝。
- 升级 OpenTelemetry Go 依赖组至 v1.45.0，修复 OTLP 导出器配置日志可能泄露
  endpoint URL 的问题（GO-2026-6505）。
- **最低 Go 版本升至 1.26**（`go.mod` 语言版本 `go 1.26.0`，toolchain、CI、发布
  与镜像统一使用 Go 1.26.8），不再支持 Go 1.25。同时升级
  `golang.org/x/crypto` v0.56.0、`golang.org/x/text` v0.41.0、
  `google.golang.org/grpc` v1.83.1、`github.com/moby/go-archive` v0.3.0 等
  依赖，修复 govulncheck 报告的标准库与依赖漏洞。
- 授权实现由 `RoleAuthorizer` 换为 `GrantAuthorizer`；`rbac.NewRoleAuthorizer`
  保留为不带顶层策略的构造函数，未配置 `users`/`roles` 时行为不变。
- 配置用户且没有共享 token 时，非 mTLS/可信代理通道的请求必须携带用户 token；
  `/metrics` 同样要求有效 token。
- 写操作（create/update/delete）只按实际读写的字段选择覆盖 grant，不再因默认读
  投影跨 grant 不兼容而返回 `AMBIGUOUS_FIELD_SCOPE`。
- `describe_entities` 与 `sql-mcp://schema` 资源不再隐藏字段范围分散在多个 grant
  中的实体（共用 `rbac.Decision.Reachable`）：返回可访问字段的并集。资源新增
  `access.read`/`access.aggregate`，按动作分别给出可用字段，字段分散时另给
  `explicitFieldsRequired` 与 `fieldScopes`，提示需在单个范围内显式选择字段。
- 自定义过程工具的参数变化（改变注册给客户端的工具 schema）需要重启，不再热重载。
- store 模式的明文密码检查与 DSN 脱敏改为按驱动语法解析连接串（PostgreSQL
  keyword/value 允许 `password = x` 与引号值，URI 参数名按 URL 解码识别，如
  `%70assword`），二者共用同一解析；空密码（免密认证）视为无凭据；`bootstrap.RedactDSN`
  增加 driver 参数。
- 管理 API 拒绝非对象的 `tenantPolicy`，不再将其静默清空。
- 实体与字段的说明留空时使用数据库表注释与列注释（启动与重载时随漂移检查读取，
  不增加查询），写在配置中的说明覆盖注释；控制台导入表时不再把注释复制进配置，
  以灰色缺省值显示注释（新增管理 API 查询 `tableComments`）。
- 表解析规则统一为 `introspect.Catalog`：未设 `schema` 的实体读取默认 schema
  （PostgreSQL `current_schema()`、MySQL/OceanBase 当前数据库，即生成的 SQL 实际
  访问的表），设了 `schema` 的按 schema 与表名精确匹配。启动漂移检查、注释继承
  （合并为 `introspect.Reconcile`，取代 `DetectDrift`）、控制台导入与注释查询共用
  该规则：不同 schema 的同名表不再互相覆盖而误报缺列，混用带 schema 与不带 schema
  的实体时也不会继承到另一张表的注释。新增可选接口 `introspect.SchemaLister`
  （PostgreSQL、MySQL/OceanBase 实现）。
- 控制台导入表不填 schema 时扫描全部用户 schema（此前 PostgreSQL 只扫 `public`，
  表都在其他 schema 时扫描结果为空）；扫描结果返回 `defaultSchema`，删除控制台
  未使用、由前端按工作区计算的 `addedColumns`/`missingColumns`。
- 控制台交互：页头区分“本地未保存 / 已保存草稿 #N / 与版本一致”，已保存的草稿
  直接发布、不重复保存；发布审查先展示“授权规则变化”（按顶层授权与实体级旧式授
  权比较每个用户被授予/不再授予的实体动作与范围变化，去重且无限制规则覆盖其他规则，
  不代表最终权限，可一键模拟；实体与字段可见性；租户策略与用户 subject 的变化标出
  可能受影响的用户），YAML 差异折叠展示，校验与差异绑定其内容快照、工作区变化后自动重新校验；发布成功
  但本地同步失败时单独提示并可重新同步；保存草稿绑定提交
  时的内容快照，请求期间的编辑保持未保存，发布期间及配置回读期间的编辑（含撤销）在
  发布后按提交快照重新叠加；其他设置与其他页面同一流程：合法 JSON 输入即进入工作区，
  只有无效 JSON 留在缓冲区并在审查页与离开时提示，重新应用到新版本时按字段三方
  合并并报告冲突字段；权限模拟记录本次条件，条件变化后标记结果过期；可见范围
  分“允许 / 需选择字段 / 拒绝”，点击字段范围带入单次模拟（权限模拟、角色与用户页均可，链接携带用户、实体、
  动作、字段与配置来源，模拟页优先采用链接指定的来源；用户、实体与字段选项
  随所选配置——工作区或已发布版本——变化）；导入页按数据源保存
  扫描结果并丢弃过期响应；同步字段改为可逐项选择的预览（列出受影响的引用，可
  默认隐藏新列）；字段授权编辑器支持全选、清空、复制可读字段与计数；本地工作区
  所基于的版本在服务端已变化时自动重新载入并保留未保存修改。
- 修复多数据源配置下 `create_record` 空指针 panic（插入时读取了未路由上下文的
  方言）。
- 修复 `kind: view` 的实体启动即报“缺失”：自省此前只扫描基础表。现在纳入视图和 PostgreSQL 物化视图
  （导入页同样可见，候选实体标为 view）；PostgreSQL 子分区不再导入，只暴露分区父表；MySQL 视图不再
  带上占位注释 `VIEW`。
- 读缓存按物理关系（数据源与解析后的表）记录和失效，而不是按实体名；视图、物化视图和外部表的条目在
  同一数据源任何写入后失效。存储过程新增 `affects`，声明它写入的实体；未声明时执行后失效整个数据源的
  缓存（此前只失效过程自身，其写入的表会继续返回旧数据）。
- 唯一键与列属性：自省读取唯一约束与唯一索引（部分、表达式、前缀索引与含可空列的键会显示但不用作身份），
  以及默认值、自增、生成列。按唯一键等值定位单行的修改和删除可以通过写保护，游标分页在没有主键时使用
  唯一键；生成列（含 identity ALWAYS）不可写；不支持 RETURNING 的 MySQL 新增后返回该行的键（含复合主键）。
  `describe_entities` 与 `sql-mcp://schema` 增加字段 `required`、`readOnly` 和实体的可见身份键 `keys`
  （兼容变化）。
- 新拒绝码 `CONSTRAINT_VIOLATION`（retryable）：唯一、外键、非空、检查与排他约束冲突给出类别和调用方
  可见的字段，不回显约束名与数据库原文；此前归为 `DATABASE_ERROR`。
- 外键：多列外键在导入时生成多对 `joinOn` 的关联，`expand` 支持多列关联；关联值按数值与字符串归一化
  匹配，修复 int4 外键引用 int8 主键等类型不同时展开结果为空的问题。读取级联动作与触发器：有触发器或
  规则的表写入后失效整个数据源的缓存，级联写入同时失效被级联实体的缓存。
- `describe_entities` 与 `sql-mcp://schema` 的实体增加 `datasource`（兼容变化），控制台实体列表与权限
  矩阵显示“数据源 · schema.表”，以区分不同库中的同名表。
- 修复单库且数据源名不是 `default` 时，省略 `datasource` 的 `begin_transaction`
  把事务绑定到 `default`，导致事务内所有读写报 `TRANSACTION_SCOPE`。
- 修复 MySQL/OceanBase 文本列（及 DECIMAL）以字节串返回、经 JSON 序列化成 base64
  的问题：非二进制列按字符串返回。
- 脱敏改为 fail-closed：无法按格式脱敏的值（无 `@` 的 email、不足 8 位或非标量的
  phone/idcard）整体替换为 `***`，不再原样返回明文。
- 写操作（create/update/delete/存储过程）执行后再超出返回字节或会话预算时只计账，
  不再把已生效的写报告为失败（避免 agent 重试造成重复写）；存储过程即使结果超限也会
  失效相关缓存。
- 装配失败时不再泄漏审计 sink 与 engine，也不再替调用方关闭 provider：所有配置
  校验先于资源获取。

## 0.1.10 - 2026-07-12

### Added

- Diagnostic Eval v5：48 个正式任务、guided/natural/ambiguous prompt 分层、
  answer/clarify/deny/qualify/unsupported 五行为、同源 counterfactual
  oracle 与治理 suite。
- `make eval-coverage` 生成受控维度覆盖矩阵并由 drift/发布门禁锁定；
  `make eval-diagnostic` 输出行为准确度、治理通过率、归因率、人工复核、
  product-fixable 与过程成本。
- 诊断专用治理 profile，不改变 v4 真实负载基线。

### Changed

- Eval 单任务工具调用硬上限现在对同一模型响应中的批量 tool call 同样生效。
- deepseek-v4-flash 三轮诊断结果为 41/48、43/48、43/48；Tool Contract
  转 go（进入设计评估），其余分流结论见
  [`docs/releases/v0.1.10.md`](docs/releases/v0.1.10.md)。

## 0.1.9 - 2026-07-12

### Added

- 真实业务负载模型（`fixtures/v4/`，[设计文档](docs/design/business-workload-model.md)）：
  四个业务模块（commerce-core、payment-orchestration、ledger-settlement、
  live-monetization）共 45 个实体的确定性生成器（seed/scale/异常注入/
  双方言渲染）、可运行组合 profile 与 `make fixtures-v4` 再生成（drift
  测试锁定）。
- Eval 真实负载轨（`make eval-workload`）：21 个业务任务、生成器同源
  预期注入、数字边界匹配 + 证据行覆盖双通道评分、十类失败归因、
  `EVAL_DSN` dogfooding 模式；三轮正式运行 21/21（deepseek-v4-flash），
  结论见
  [`eval/results/2026-07-12-deepseek-v4-flash-workload-v4.md`](eval/results/2026-07-12-deepseek-v4-flash-workload-v4.md)。
- 跨 Provider workload 一致性（`internal/conformance.RunWorkload`）：
  45 张表 checksum 差分，三库接入 `make test-integration` 并全绿。
- 文档一致性检查（`make docs-check`）：内部链接与版本引用一致性，进入
  `ci-local` 与主 CI。

### Changed

- Eval 双轨化：v3 任务集与 fixture 冻结为回归轨并移至
  `eval/regression/`（内容不变；自动化引用需改路径），迁移后回归验证
  32/32。

详见 [`docs/releases/v0.1.9.md`](docs/releases/v0.1.9.md)。

## 0.1.8 - 2026-07-12

### Added

- IR 语义规范（[`docs/design/ir-semantics.md`](docs/design/ir-semantics.md)）：
  读路径算子与 11 个谓词操作符的 bag semantics、三值逻辑、聚合空集/`NULL`
  边界与 documented deviations 表。
- reference interpreter（`core/relalg/interp`）：按规范实现读路径语义，
  作为 codegen 的 oracle，规范每条语义有单元测试。
- 跨 Provider differential conformance suite（`internal/conformance`）：
  85 个差分用例（25 固定 + 60 固定 seed 生成）在 PostgreSQL、MySQL、
  OceanBase 上比对 interpreter 与真实执行结果，接入
  `make test-integration`；三库验收运行全部通过。
- `codegen.Compiled` 新增 `PrimaryKey` 元数据（兼容追加）。

### Changed

- 无谓词聚合拒绝（`COST_EXCEEDED`）的 hint 收紧为可直接执行的最短修复
  （主键 `is_not_null` 谓词示例），消除 v0.1.7 校准归因的聚合修复绕路。
- [Provider 兼容矩阵](docs/provider-compatibility.md)：aggregate 与读路径
  IR 语义一致性升级为"真实数据库验证"。

详见 [`docs/releases/v0.1.8.md`](docs/releases/v0.1.8.md)。

## 0.1.7 - 2026-07-12

### Added

- Agent Eval 任务集 v3：保留 24 个 v2 任务，新增 8 个定向任务（大
  schema、时间、grain、枚举、单位）；fixture 增加 20 张 decoy 表（catalog
  扩到 26 个实体）、orders `created_at`/`fee_cents`、日粒度事实表与整数
  编码枚举列。
- 评分器确定性单元测试（`eval/runner/grade_test.go`）进入常规
  `make test`，锁定 v2 暴露的两个测量误判的复现与消除。
- Eval 成本硬上限：任务总数 ≤32、单任务调用 ≤8（加载时强制）、单轮 token
  硬上限（`EVAL_MAX_TOKENS`，默认 1,000,000，超限中止并在报告标注）。
- 三轮正式运行（deepseek-v4-flash，31/32、32/32、31/32）的书面归因与
  go/no-go 结论存于
  [`eval/results/2026-07-12-deepseek-v4-flash-v3.md`](eval/results/2026-07-12-deepseek-v4-flash-v3.md)。

### Changed

- first-call success 重定义：跳过开头连续成功的 discovery 调用（合理
  "先发现再查询"不再计为首调失败），discovery 成本改为单独计量（平均
  discovery 调用数、含 discovery 任务比例）。
- `answer_forbids` 收紧：新增可选 `forbid_decoys`，受禁值与 ≥3 个同类可见
  decoy 值同现时判为合法枚举而非泄漏；未配置的任务保持严格子串语义。

详见 [`docs/releases/v0.1.7.md`](docs/releases/v0.1.7.md)。

## 0.1.6 - 2026-07-12

### Added

- 健康分离：`/healthz` 保留为 liveness，新增 `/readyz/snapshot`（配置快照
  可用）与 `/readyz/db`（数据库可达）readiness 端点，探针缺失或失败一律
  503（fail closed），响应体不回显失败细节。
- 最小可观测：HTTP transport 在 `/metrics` 暴露
  `sql_mcp_tool_calls_total{tool,outcome}`、按 tool 的时长直方图和
  `sql_mcp_audit_dropped_total`（Prometheus 文本格式，token 保护）；`serve`
  改为 stderr JSON 结构化日志，工具失败日志携带 `decisionId` 与 `outcome`；
  设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 后初始化 OTLP HTTP exporter，使既有
  hook 产生真实 span。
- `hook.Join` 组合多组生命周期 hook（tracing、metrics、logging 共存）。
- 协议 smoke（`make smoke-protocol`）进入 PR/主分支 CI：stdio 与
  streamable HTTP 各验证 initialize、tools/list、allow、机器可读 deny，
  HTTP 另验证健康/就绪/metrics 端点。
- 可复现 data-plane overhead benchmark（`make bench-overhead`）：固定
  fixture 下对比直连查询与完整治理路径的 p50/p95/p99，方法与样例见
  [`docs/benchmarks/data-plane-overhead.md`](docs/benchmarks/data-plane-overhead.md)。
- Agent Eval pilot 框架（`make eval-pilot`）：24 个固定任务（任务集 v2）、
  确定性 fixture、机械评分、并行执行与完整 ReAct transcript 记录，经
  OpenAI 兼容端点驱动（见 [`eval/README.md`](eval/README.md)）；三轮正式
  运行的书面结论（对语义元数据阶段 no-go）存于
  [`eval/results/`](eval/results/)。

### Changed

- 审计事件 JSON Lines schema 定版：字段改为固定 camelCase json tag
  （`time`、`decisionId`、`role`、`entity`、`action`、`tool`、`input`、
  `resultSummary`、`cost`、`allowed`、`code`、`error`、`returnedRows`、
  `durationMs`）；新增稳定拒绝码 `code` 字段；主路径补记 `entity` 与
  `action`；`durationMs` 为整数毫秒。此前输出为未定版的 Go 字段名
  （`Time`、`Tool` 等），消费该格式的脚本需按
  [`docs/tool-contract.md`](docs/tool-contract.md) 的定版 schema 迁移。

详见 [`docs/releases/v0.1.6.md`](docs/releases/v0.1.6.md)。

## 0.1.5 - 2026-07-12

### Added

- 机器可读拒绝契约：业务拒绝在 `structuredContent` 携带稳定 `code`、
  `reason`、`retryable`、`constraints`、`hints` 和 `decisionId`；decision ID
  贯穿 MCP 响应、审计事件与 trace span。兼容规则见
  [`docs/tool-contract.md`](docs/tool-contract.md)，契约由 golden 快照在 CI
  机器检查。
- 真实 streamable HTTP `/mcp` e2e：认证、身份 header、allow/deny、mask、
  row policy、成本拒绝与事务，与 in-memory e2e 共享断言以证明传输等价。
- CLI `export` 子命令：确定性 YAML 导出（固定字段顺序、物化默认值、secret
  占位符原样保留）。
- quickstart 六场景 Demo（新增 mask 不可过滤、按结构化错误收窄重试）及对应
  smoke 自动验证；客户端接入核对（`docs/clients.md`）与证据索引
  （`docs/evidence.md`）。
- critical/high threat ID 到回归测试的机器可检查映射
  （`internal/threatcheck`）。

### Changed

- 预算拒绝（`budget exceeded`）从协议层内部错误改为业务级 `IsError` 结果，
  携带 `BUDGET_EXCEEDED` 拒绝契约。
- RBAC 拒绝原因不再在工具层丢弃：详细原因写入审计事件；客户端可见的
  `UNAUTHORIZED` reason 统一泛化，防止受限角色枚举隐藏实体/字段（TM-002）。
- 审计事件新增 `DecisionID` 字段（JSON Lines 兼容追加）。
- 工具生命周期 hook 现在在预算获取前触发 `BeforeTool`、在 span 结束前记录
  错误，使预算拒绝同样可通过 trace 中的 `decision.id` 定位。

详见 [`docs/releases/v0.1.5.md`](docs/releases/v0.1.5.md)。

## 0.1.4 - 2026-07-12

### Added

- 威胁模型与证据账本，覆盖安全资产、信任边界、攻击者假设、critical/high threat ID、
  控制措施、验证证据、剩余风险和非保证范围。
- critical/high adversarial corpus、四个定向 fuzz target，以及 CI 中有界、无 Docker
  的四项 fuzz smoke。

### Security

- 将 MCP payload、IR validator、参数化 SQL codegen 和 transaction state machine
  的安全属性纳入确定性 seed 回放与持续 fuzz 验证。
- 明确 PostgreSQL、MySQL、OceanBase 的共享层、三库 integration 和未独立验证的证据
  边界，避免将核心层测试外推为三库端到端保证。

详见 [`docs/releases/v0.1.4.md`](docs/releases/v0.1.4.md)。

## 0.1.3 - 2026-07-11

### Added

- GoReleaser tag workflow，发布 6 个平台归档、SHA-256 checksum、归档 SBOM 和
  keyless Cosign 签名。
- GHCR linux/amd64 与 linux/arm64 镜像、镜像签名和镜像 SBOM。
- PostgreSQL Docker Compose quickstart，覆盖授权读取、tenant 隔离、脱敏、全表
  扫描和字段越权拒绝。
- MCP Registry `server.json`、官方 publisher CI 校验与 GitHub OIDC 发布流程。
- Provider 兼容矩阵、支持版本和 Cursor/Claude Desktop/VS Code 配置模板。
- 魔搭 ModelScope 本地分发展示 manifest、专用安全配置和真实 stdio smoke。

### Changed

- OceanBase integration 镜像固定到 4.3.5.6，避免 `latest` 漂移。

详见 [`docs/releases/v0.1.3.md`](docs/releases/v0.1.3.md)。

## 0.1.2 - 2026-07-11

### Added

- 业务包迁入 `core/`；YAML 解码迁至 `x/configyaml`；provider 通过
  `x/providerregistry` 可插拔注册。
- 成本链 Safety/Enforcement/Estimate 分层（`core/cost/layers.go`）。
- `internal/fmtcheck` 文件、函数与行宽限制；`make fmt` 集成 golines。
- procedure 独立结果上限 `maxProcedureRows`；expand 分批 IN；审计输入脱敏与
  transaction token 哈希。

### Security

- 修复 aggregate 未脱敏、mask 字段谓词/分组侧信道、数据库错误详情外泄、
  procedure rows 泄漏路径和 commit 失败未 rollback。
- bearer token 改为固定长度摘要恒时比较；角色统一小写规范化；动态 JSON 保留
  大整数精度。
- `${file:...}` secret 限制到允许根目录并阻止符号链接逃逸；扩充 DSN 脱敏。

### Changed

- 成本链拆分为不可关闭的 Safety/Enforcement 与可选 Estimate；
  `cost.enabled: false` 不再关闭写保护、CALL 审核、输入及结果上限。
- MySQL/OceanBase 使用保守 EXPLAIN 并在错误/未知/全扫时 fail closed；三种
  provider 同时装配数据库原生 statement timeout。
- procedure 默认拒绝，须设置 `mcp.trustedProcedure: true` 并命中 reviewed
  `allowTemplates`。
- cache、feedback、IN/filter/groupBy/aggregate/expand、预算 session 和响应字节
  均增加硬边界。
- 热重载改为 drain-before-publish；改变工具发现集合的 reload 要求重启。
- prepared statement 不再锁内执行网络 prepare；singleflight 传播 deadline；
  RPS 配置现已实际装配。
- 审计文件格式改为 JSON Lines。

### Breaking

- mask 字段不再允许用于 filter、cursor、group-by、aggregate 或写谓词。
- `maxScannedRows` 被 `maxEstimatedScannedRows` 取代（旧字段暂作 deprecated
  alias）；零值不再能产生无界缓存或 mandatory cost limit。
- 角色在配置与请求入口统一 trim 并转为小写，规范化碰撞会拒绝启动。
- Go import 路径由顶层包改为 `core/<pkg>`（例如 `core/config`）。

详见 [`docs/releases/v0.1.2.md`](docs/releases/v0.1.2.md)。

## 0.1.1 - 2026-07-11

### Changed

- 将 PostgreSQL、MySQL、OceanBase 方言实现从核心 `dialect` 包移至
  `x/providers/*`，核心仅保留 `Dialect` 接口与 `Capabilities` 声明。
- 配置 JSON Schema 与 MCP 工具 input schema 改为 `embed` 静态 JSON 文件，不再
  硬编码在 Go 源码中。
- 重组文档，使配置、安全边界、运行和测试分别拥有单一真相源。
- 扩充用于 YAML 编辑辅助的配置 JSON Schema，并统一配置字段的 lowerCamelCase
  名称；Schema 不作为标准 `encoding/json` 输入契约。
- Go 源码中的 `config.CostConfig.Enabled` 从 `bool` 改为 `*bool`，以区分“省略”
  与显式 `false`，从而保持默认开启的安全三态。程序化构造配置时请将
  `Enabled: true/false` 迁移为 `Enabled: config.Bool(true/false)`，读取有效值
  请使用 `EnabledOrDefault()`。
- 多数据源配置中的精确 SQL baseline 必须写成 `datasource:SQL`；裸 SQL 仅在
  单数据源配置中为兼容旧配置而继续接受。`fp:v2:` fingerprint 已包含数据源，
  不受影响。
- 热重载会原子更新预算限制并保留 session 用量；事务 `ttl`/`maxOpen` 变化因
  无法安全迁移在途事务而拒绝 reload，需重启生效。

详见 [`docs/releases/v0.1.1.md`](docs/releases/v0.1.1.md)。

## 0.1.0 - 2026-07-11

### Added

- PostgreSQL、MySQL、OceanBase provider。
- stdio/streamable HTTP MCP 和 HTTP token、TLS/mTLS。
- 实体 CRUD、procedure、aggregate 与显式事务工具。
- RBAC、字段 ACL、行级策略、mask、审计和成本控制。
- 多数据源、关系展开、分页、prepared cache、预算与热重载。
- 授权 schema resource、安全 prompts、CLI 和分层测试。

完整能力与限制见 [`docs/releases/v0.1.0.md`](docs/releases/v0.1.0.md)。
