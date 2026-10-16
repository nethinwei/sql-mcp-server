# Changelog

本项目的重要变更记录于此。格式参考
[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本遵循
[Semantic Versioning](https://semver.org/lang/zh-CN/)。

CHANGELOG 只维护版本级摘要和 breaking 提示；完整能力、迁移步骤、证据边界与
版本时点限制在对应 `docs/releases/vX.Y.Z.md` 中维护。

## Unreleased

### Breaking

- 成本闸门新增扫描量硬上限 `cost.maxScannedRows`，**默认 100000**：语句预计读取的行数
  （EXPLAIN 中最大的行数估计；全表扫描取表的行数）超过它即拒绝（`COST_EXCEEDED`，
  `constraints` 带 `estimatedScannedRows`、`maxScannedRows`，`hints` 给出数字与可用于过滤的
  索引字段），不论是否用到索引。此前闸门只按访问方式计分，按索引范围读取上千万行的
  聚合（得 70 分）会被放行。read、aggregate 与 `execute_sql` 都受约束；角色或用户的
  `limits.maxScannedRows` 只能收紧。迁移：确实需要更大扫描的部署把全局值调高（0 为不限），
  或只给相应角色放宽；Agent 收到拒绝后应缩小时间范围或分批。
- 安全分的行数扣分改为按量级（>1 万 −15、>10 万 −25、>100 万 −35、>1000 万 −45），
  此前超过 1 万一律只扣 15 分；大范围读取更容易触发软拒绝。

### Added

- `read_records` 支持 `orderBy`（升降序、最多 8 项，末尾自动补键使顺序确定，NULL 排最后，
  脱敏字段不能排序），带 `limit` 时结果给出 `nextCursor`，原样传回即取下一页；
  `distinct` 对所选字段去重。
- `aggregate_records` 支持 `having`、`orderBy`（分组字段或 `sum_amount` 这样的聚合列）与
  `limit`（Top-N），以及去重计数 `{"func":"count","field":"x","distinct":true}`
  （列名 `count_distinct_x`）。
- 过滤条件可组合：`{"any": [...]}`（OR）、`{"all": [...]}`（AND）、`{"not": {...}}`，最多嵌套
  4 层；read、aggregate、update、delete 通用。
- `describe_entities`（及资源 `sql-mcp://schema/{entity}`）给出调用方可用于过滤的索引
  `indexes`；运行时从数据库目录带上索引，不增加查询。
- `execute_sql` 可执行 `SHOW`、`DESCRIBE <表>`：`query` 模式下的单条元数据语句不做 EXPLAIN 估算，
  只受硬上限约束（新配置 `cost.sqlEscape.metadata`，默认 `allow`，设 `refuse` 恢复旧行为）；
  `DESCRIBE ANALYZE` 等 EXPLAIN 用法照常估算。
- 控制台实体列表支持批量删除：勾选（可跨页）后“删除所选”，或在有搜索/数据源筛选时“删除全部
  匹配的 N 个”；确认框列出授权会被一并删除的角色与用户。指向这些实体的授权、关系与过程的
  `affects` 照旧由服务端一并移除。此前只能在实体详情页逐个删除。

### Changed

- 成本拒绝的 `constraints` 现在如实给出 `maxRows`、`maxBytes`（此前判定时的阈值没有传到
  拒绝结果里，这两项从未出现）。
- 几千个实体规模下的性能（基准 3000 实体×20 字段、50 角色、200 用户）：
  - 控制台自动保存只往返改动项：`Workspace.items(since)` 按写入记录返回某版本之后改动的
    项，控制台只替换这些项（换基础修订等情况返回全部）。一次编辑的应答从 3.2 MB 降到约
    6 KB，服务端耗时从约 700 ms 降到约 45 ms；读取工作区从约 500 ms 降到约 60 ms。
    `Workspace.sections` 仍可用，改为只在被选择时编码。
  - 服务端缓存解码后的修订项（修订不可变，按 ID）与配置库中最近读写的工作区（按版本号与
    写入时间校验）；单项写入且没有改名、增删实体/角色/用户时跳过引用维护的全量遍历。
  - 控制台按项缓存规范化结果（项被原地修改也会重算），基线只解析一次；“谁能访问”改为
    一次构建的反向索引；自动保存不再深度侦听整个 store（每次按键遍历约 19 万个对象），
    服务端副本不再是响应式的。浏览器里每次输入的处理从约 250 ms 降到约 35 ms，概览页逐实体
    判断可访问性从约 1.6 s 降到约 1 ms。
  - 控制台载入修订：`Revision.sections` 走同一份修订项缓存，`Revision.config` 按修订缓存
    解析结果，只选数据源时不再转换其余分区；从约 440 ms 降到约 25 ms（每个修订每进程首次
    仍需完整解析）。
  - `describe_entities` 列表从约 8.7 ms 降到约 0.2 ms：实体可用性按角色记忆（与 subject
    无关），注册表预先计算可见字段、遍历不再复制实体。

## 0.1.17 - 2026-10-16

生产稳定性与 MCP 协议对齐版本，**有 breaking 变更**；升级步骤见
[`docs/releases/v0.1.17.md`](docs/releases/v0.1.17.md#迁移)。

### Breaking

- 工具结果的文本改为与新增的 `structuredContent` 相同的 JSON 对象：返回行的工具
  （`read_records`、`aggregate_records`、`execute_entity`、`procedure_*`）为
  `{"result": [行...]}`，此前文本是裸数组 `[行...]`；返回单个对象的工具（写入、事务、
  `execute_sql`、`describe_entities`）直接给出该对象，此前包在单元素数组里。迁移：优先读
  `structuredContent`；解析文本的客户端把 `rows = text` 改为 `rows = text["result"]`，单对象
  结果去掉 `[0]`。
- `describe_entities` 不带 `entity` 时改为分页：返回 `{"entities": [...], "total": n,
  "nextOffset": m}`，默认每页 100 个、上限 500，可按 `search`（名称、ID、描述）与
  `datasource` 过滤；此前一次返回全部实体（几千个实体时上百 KB）。迁移：遍历
  `entities`，有 `nextOffset` 时用它作 `offset` 取下一页，或直接用 `search` 查找。
- 资源 `sql-mcp://schema` 改为摘要（可读实体总数、按数据源计数、用法说明），实体详情移到
  资源模板 `sql-mcp://schema/{entity}`；此前一次给出全部实体的字段与访问范围（几千个实体时
  数 MB）。迁移：先用 `describe_entities` 找到实体，再读 `sql-mcp://schema/<实体>`。
- 存储过程工具名改为可读的 `procedure_<过程名>`，只有规范化后重名的过程才追加
  `_<8 位哈希>`；此前一律带哈希。迁移：按新名称调用（`tools/list` 可查），写死旧名的客户端与
  评测脚本需要更新。
- 工具输入 schema 顶层声明 `additionalProperties: false`（服务端此前已拒绝未知字段，行为
  不变，只是让客户端提前知道）。
- 显式事务中任何一条语句失败，事务立即回滚：该次拒绝带 `constraints.transaction:
  "rolledBack"`，之后用该令牌得到 `TRANSACTION_NOT_FOUND`（`outcome: rolledBack`）。此前
  MySQL/OceanBase 的事务在语句失败后继续可用；PostgreSQL 的事务则已作废却仍被持有，后续
  每次调用（包括提交）都返回 `DATABASE_ERROR`，直到超时才释放锁和连接。迁移：在同一事务里
  “失败后继续”的客户端需要重新开启事务并重做写入。
- 新增 `transactions.idleTimeout`，默认 1 分钟：超过这么久没有执行语句的事务由服务端回滚
  （`outcome: expired`）。此前事务可以空闲到 `transactions.ttl`（默认 5 分钟），期间一直持有
  锁，卡住表结构变更以及排在其后的所有查询。迁移：需要更长思考时间的场景调大该值。

### Added

- 按调用者设置资源限制：角色与用户新增 `limits`（单次请求的 `maxRows`、`maxBytes`、
  `queryTimeout`，成本闸门的 `softScore`、`hardScore`、`rejectFullScan`，按调用者跨会话计的
  `rps`、`maxInflight`，以及会话预算 `budget`）。全局值是上限，只能收紧；用户设置的项优先于
  其角色，多个角色时每项取最宽松的。超出 `rps` / `maxInflight` 返回新拒绝码
  `RATE_LIMITED`（可重试）。控制台角色页与用户页新增“资源限制”卡片，会话预算可直接在这里
  编辑。
- 同步移除库中已删除的表：待处理页列出表已不存在的实体（所在库扫描时没有找到，或运行中的
  服务发现缺表），可多选后从工作区移除；方案的同步规则新增 `removeTables`，配置 MCP 的方案
  预览与明细给出移除数量和实体（`removed`）。移除时一并去掉指向它们的授权、关系与
  `affects`。
- `serve` 在未设置 `GOMEMLIMIT` 时按容器 cgroup 内存上限的 90% 设置 Go 运行时的软内存上限。
- 配置存储接口新增 `PublishedSummary`（不含正文的已发布版本）。
- `server.sessionTimeout`：HTTP MCP 会话的空闲超时，默认 30 分钟（此前固定 5 分钟且不可配置），
  修改需重启。
- 工具在 `tools/list` 中声明 `title`、`outputSchema` 与行为提示（`readOnlyHint`、
  `destructiveHint`、`idempotentHint`，`openWorldHint: false`），描述写明用法、返回形状与
  常见拒绝；服务端 `instructions` 说明先用 `describe_entities` 查找实体，以及如何按拒绝码
  处理（重试、等待或放弃）。
- 新拒绝码 `OVERLOADED`（服务过载、全局限流、正在关闭）与 `DATASOURCE_UNAVAILABLE`（数据源
  熔断），均可重试并带提示；此前这两种情况是协议级错误，Agent 看不到机器码，分不清是否该重试。

### Changed

- 配置的表或字段在数据库中已不存在时，不再让整个 revision 加载失败：只有这些实体不可用，
  其余配置照常加载与热重载，被改动的用户不再因此暂停。有权限的调用返回新拒绝码
  `ENTITY_UNAVAILABLE`（不可重试，不生成 SQL；无权的调用方仍得到 `UNAUTHORIZED`），日志
  告警 `entities unavailable`。管理 API `serverStatus.unavailableEntities` 列出它们，控制台
  顶栏与总览“需要关注”展示并可一键从工作区移除。此前按时间生成、定期清理的快照表一旦被
  删除，整份新配置都无法生效。
- 读取不到结构的数据源（不可达、自省超时）不再让整个 revision 加载失败：它的实体全部不可用
  （调用方只看到“数据源不可达”），其余配置照常加载；`--watch` 时按退避重新装配，数据源恢复
  后自动可用。`serverStatus.unreachableDatasources` 列出它们。`serve` 打开连接时不再 ping
  数据库（`providerregistry.Options.Lazy`）：启动时或新发布的 DSN 指向的库暂时不可达，服务照常
  启动并降级该数据源，不再退出或整版失败；`validate` 等命令仍立即报告连接错误。
- 加载失败的 revision 按退避重试（从轮询间隔起翻倍，最长 5 分钟），有新发布时立即尝试；此前
  每次轮询都完整重建（重新解析配置并自省所有数据源），持续占用 CPU、内存与数据库。版本轮询
  只读已发布版本的摘要，版本变化时才读正文。
- 管理 API 的可见性、模拟、扫描等接口缓存解析后的配置（按版本号，或工作区的基础版本与修改
  内容），不再每次请求都重新解析整份配置。

### Fixed

- 运行中删除实体用到的表或列后，调用返回不可重试、无说明的 `DATABASE_ERROR`，实体要等下次
  发布或重载才被标记不可用。现在返回 `ENTITY_UNAVAILABLE`（说明缺表还是缺列），`--watch`
  时服务随即重新装配当前 revision（按退避），这些实体出现在 `serverStatus` 与控制台“需要
  关注”。PostgreSQL 修改列类型后缓存的语句计划失效（`cached plan must not change result
  type`），事务外的调用自动重试一次，不再报错。成本闸门的 EXPLAIN 先碰到缺表缺列时，此前被当成
  “无法估算 + 统计过旧”而返回误导性的 `COST_EXCEEDED`（提示 ANALYZE），现在同样返回
  `ENTITY_UNAVAILABLE`。表或列不存在、语句计划失效也不再计入数据源健康度：此前一张被删的表
  被反复调用，可能让熔断器把整个数据源挡住（`DATASOURCE_UNAVAILABLE`）。
- PostgreSQL 判断统计信息是否过旧时只看手动 `ANALYZE` 的时间，忽略 autovacuum 的自动分析，
  且只按表名不分 schema：几乎所有表都被当作“统计过旧”扣 15 分，带 LIMIT 的顺序扫描因此低于
  软阈值被拒绝（`COST_EXCEEDED`，提示 ANALYZE），Agent 重试也无法通过。现在自动分析同样算
  数，按 schema 判断，不超过 64 KiB 的小表（autovacuum 不会分析的规模）不扣分。小表
  （`cost.fullScanMaxRows` 以内）的全表扫描因此按文档放行；此前它们多被这 15 分拖到软阈值
  以下。
- 用户的 `subject` 改变后，它已打开的 MCP 会话仍绑定旧身份，此后每次请求都得到 403；现在
  `subject` 变化与删除一样吊销该用户的会话，客户端重新建立会话即可。
- 吊销用户时只解除了会话绑定，传输层的会话要等空闲超时才关闭；现在立即关闭并释放其资源。
- 用于断线续传的事件缓存（全局 10 MiB）按时间顺序淘汰所有会话的事件，一个会话的大结果会让
  其他会话无法续传；现在优先淘汰占用最多的会话自己的旧事件。
- `/mcp` 拒绝跨源的浏览器请求（`Origin` 与主机不符），防止其他网站借浏览器的客户端证书或代理
  身份调用；不是浏览器的客户端不发送 `Origin`，不受影响。
- MCP 会话空闲超时、服务重启或用户被吊销后，客户端用旧会话请求得到的是 403
  `MCP session identity mismatch`，被当作权限错误而不会重连，表现为“用一阵就不能用”。现在
  服务端已不持有的会话返回 404（MCP 协议规定的“会话已结束”），客户端自动重新建立会话；
  身份不一致仍返回 403，被吊销的用户在重新认证时被拒绝。
- 删除用户时，`budget.users` 中它的条目没有一并删除，之后发布因 `budget references unknown
  user` 校验失败；角色改名后 `budget.roles` 仍按旧名，预算悄悄失效。现在两者随角色与用户的
  改名、删除同步。

## 0.1.16 - 2026-10-16

控制台的大规模可用性版本，无 breaking 变更；详见
[`docs/releases/v0.1.16.md`](docs/releases/v0.1.16.md)。

### Added

- 管理 API `visibilityPage(input, filter, offset, limit)`：服务端评估全部实体并返回总数、可访问数
  与需选择字段数，按实体 ID 关键字与“只看可访问”筛选后只返回一页（至多 200 个）。原
  `visibility` 不变。

### Changed

- 控制台在数千实体时按页加载：实体列表、授权矩阵与可见性预览分页（实体列表的搜索、筛选与
  页码保存在 URL 中）；可见性预览改用新增的管理 API `visibilityPage`，由服务端筛选、计数并
  只返回一页；版本历史可加载更早的版本；审核摘要的长列表默认折叠。
- 总览“最近版本”的长说明截断显示，不再挤压版本号与时间。

### Fixed

- 发布：镜像的控制台与 Go 构建阶段改在构建平台上运行（Go 按目标平台交叉编译），多架构
  构建不再经 QEMU 模拟；`v0.1.15` 的镜像任务曾因 arm64 下 Node 在模拟中崩溃而卡到 6 小时
  上限。镜像任务另设 45 分钟超时。

## 0.1.15 - 2026-10-12

升级步骤汇总见 [`docs/releases/v0.1.15.md`](docs/releases/v0.1.15.md#迁移)。

### Security

- 修复：配置了 `users` 且开启 `trustProxyHeaders` 时，可信代理后的调用方发送
  `X-MCP-Role: user:<name>` 就能冒充该用户，获得其全部授权（包括 `sql:execute`）；这条
  路径还不会用用户配置的 `subject` 覆盖头里的属性，`${subject.x}` 行策略取的是调用方自填
  的值，审计与预算也记在被冒充用户名下。自引入用户身份起的各版本都受影响。现在
  `X-MCP-Role` 含 `:` 时返回 400；自定义 transport 经 `WithSubject` 注入的同类值得到的是
  不持有授权的主体。用户身份只能由用户 token 或 `X-MCP-User` 建立（TM-010）。
- mTLS 下不再把每个持有 clientCA 签发证书的调用方都当作可信代理：新配置
  `server.auth.trustedProxyCertSubjects`（叶证书 CN 或 DNS SAN，精确匹配）列出代理的证书。
  其他证书带身份头返回 403，不带身份头时按普通调用方处理（TM-005）。
- 管理 API `Revision.yaml` 与 diff 一样脱敏 DSN 密码和 HTTP token。
- 改自己的管理员密码需要提供当前密码（`SetAdminPasswordInput.currentPassword`）。
- 登录锁定时长在连续锁定时逐次翻倍（1 分钟起，上限 1 小时）。
- HTTP 请求体须在 30 秒内读完（读完即解除该期限，流式响应不受影响），防止慢速发送
  长时间占用连接。
- 管理 API `tableComments` 按数据源检查调用方的作用域，`visibility` 要求调用方不受数据源
  限制（纵深防御：目前会话主体本来就不受限）。
- `server.publicURL` 为 `https://` 时，控制台会话 Cookie 带 `Secure`（反向代理终结 TLS 的
  部署此前不带）。
- `editWorkspace` 拒绝未知的项类型（此前会写入工作区却永远不进入配置），并改为先鉴权再
  校验输入。

### Added

- Provider 能力模型（`docs/design/capability-model.md`）：`core/dialect.Profile` 为每项能力
  分别给出保证强度（`enforced` / `best_effort` / `unsupported`）、证据与原因，强度按连接
  判定——服务注入的会话设置被 DSN 覆盖、经 transaction pooler，或 MySQL 写语句
  （`max_execution_time` 只作用于 SELECT）时如实降级。首批能力键：`write.returning`、
  `cost.explain`、`cost.explain.accurate`、`cost.explain.analyze`、`limit.timeout.read`、
  `limit.timeout.write`、`limit.unkeyedWrite`。
- 数据源依赖的语句超时（读；有人能经开启的工具写入或执行过程时的写与过程，以及写后读、
  读写事务里落在写连接上的读；开启时的 SQL 逃生通道）不是 `enforced` 时，启动与热重载
  告警（同一进程内只在变化时重复）；控制台的校验与发布审查对尚未应用的配置按其 DSN
  （不连接）同样判定。新配置 `cost.requireNativeLimits`（默认 `false`，可热重载）开启后
  这样的配置不能加载，控制台校验报错阻止发布（CLI 发布不连接数据库，由服务拒绝应用）。
- 管理 API `providerCapabilities`（每个服务中连接的能力与未强制的硬限制）；控制台数据源
  卡片显示未强制的语句超时，展开连接后显示明细与各连接在数据库侧的保证，新版本应用后
  刷新。
- 兼容矩阵的 Provider 能力表由 `go generate ./x/providers/all` 从各 provider 的声明生成；
  `providerregistry.RegisterLimits`/`Limits`/`Drivers`，内置驱动都登记了不连接即可判定的
  `Limits`。
- 跨 Provider 治理等价任务（`internal/conformance.RunGovernance`）：受治理读、对抗过滤、
  受治理聚合、全表扫描拒绝、无键写入拒绝、语句超时分类，三库 integration 运行；三库
  语句超时与 `sql_safe_updates` 的真实数据库测试。
- 管理 API 逐操作越权矩阵（每个查询与变更在 16 种权限组合下断言）、鉴权先于输入解析的
  测试与复杂度上限测试；威胁 TM-014。
- `sql-mcp-server healthcheck [--url] [--timeout]`：容器健康检查用（镜像没有 shell 和
  curl）；示例 compose 为 `mcp` 服务配置了它，`docker compose up --wait` 因此会等到服务
  就绪才返回。
- `examples/config.example.yaml` 补齐所有配置字段（roles/users、多连接与路由、
  `execute_sql`、`requireNativeLimits`、`execution` 等）；`make docs-check` 检查示例覆盖
  schema 的每个字段，并检查 Makefile 与 CI 中没有写死的 `RELEASE_VERSION`。

### Changed

- 数据库报告的语句超时（PostgreSQL 57014、MySQL 3024、OceanBase 4012）归类为
  `store.ErrStatementTimeout`：遥测记为 timeout，写入超时按“未生效”而非结果未知处理
  （语句已回滚）；与此前相同，计入熔断。
- 注入数据库的语句超时（`statement_timeout`、`max_execution_time`、`ob_query_timeout`）
  比 `cost.queryTimeout` 少十分之一（至多 1 秒），使数据库通常先于请求 deadline 停住语句
  并报告结果确定。
- 方案的范围统计、预览与应用都先鉴权再解析输入，工作区的 update/reset/合并草稿同样
  先鉴权再解析版本号。
- PostgreSQL 经 transaction pooler 时，语句超时定为 `best_effort`（此前为 `enforced`）：
  pooler 不保留 `statement_timeout`，cancel request 也只有在 pooler 转发时才生效，与 MySQL
  同口径。
- `EnforceCap` 对 codegen 生成的 select 直接收紧（或补上）`LIMIT`，不再外包子查询，
  ORDER BY 因此在上限之下保持有效（此前外层 LIMIT 不保证内层顺序，keyset 分页可能跳行或
  重复）。
- 控制台 SSE 对同一管理员的多个连接每个周期只读一次工作区版本（此前每个连接每秒读
  一次）。
- `RELEASE_VERSION` 默认取 `server.json` 的版本（此前写死为 0.1.4，`make release-bump`
  不带参数会把版本降回去），`release-bump` 必须显式给出新版本；CI 的 govulncheck 钉到
  v1.8.0。

### Fixed

- `aggregate_records` 的聚合列名取数据库自己的命名：PostgreSQL 把同一函数的多个聚合都
  命名为 `sum` 等，按列名转成行时互相覆盖、丢失结果；MySQL/OceanBase 的键为
  `COUNT(*)`、``SUM(`amount`)``，跨数据库不一致。现由执行器按位置命名，生成的 SQL 不变
  （`allowTemplates`、`rejectTemplates` 与估算反馈的指纹不受影响）。
- `read_records` 的负数 `limit`/`offset` 此前绕过校验后被当作“不限”，连同 offset 一起被
  丢弃；现返回 `INVALID_INPUT`。codegen 拒绝 `Count < 1` 或 `Offset < 0` 的 Limit 节点，
  也拒绝没有列、没有元组或列数不齐的 insert（纵深防御）。
- `create_record` 的 `values` 为空时返回 `INVALID_INPUT`（此前生成 `VALUES ()`，由数据库
  报语法错误）。
- `aggregate_records` 中重复给出的同一聚合返回 `INVALID_INPUT`（此前其中一列被静默覆盖）。
- 回滚期间被他人抢先发布时，返回带 `published` 的 `CONFLICT`（此前是未映射的错误），
  控制台随即刷新修订列表。
- MySQL/OceanBase DSN 中的 `sql_safe_updates` 只有 `1`/`ON` 判为 `enforced`，其他无法判定
  的值为 `best_effort`（此前一律判为开启）。
- 查询扫描任务时，任务恰好过期返回 null，不再返回空 ID 与空状态的任务。

### Breaking

- `aggregate_records` 的聚合列名在所有数据库上统一为 `count`（`count(*)`）与
  `<func>_<field>`（如 `sum_amount`、`count_id`），与某个分组字段同名的聚合返回
  `INVALID_INPUT`。按旧键名读取结果的客户端需要更新（只有 PostgreSQL 的 `count(*)` 键名
  不变）。
- 应用方案（控制台 `applyPlan`、配置 MCP `plan_apply`）与预览一样需要 `admin:read` 与
  `admin:write`，此前只要 `admin:write`。
- Go API：`dialect.Dialect.Capabilities()` 返回 `dialect.Profile`（移除
  `dialect.Capabilities` 结构体）；`provider.Provider` 新增 `Profile()`；
  `mysql.NewAdapter` 额外返回连接的限制；`cost.NewGateFromCapabilities` 接收
  `dialect.Profile`。
- `trustProxyHeaders` 不能只靠 mTLS：须配置 `trustedProxyCIDRs` 或
  `trustedProxyCertSubjects`，此前只配 `clientCA` 的配置会校验失败。
- `X-MCP-Role` 不能是主体键（含 `:`，如 `user:alice`），否则返回 400；用户请改用
  `X-MCP-User`。
- 管理 API 改自己的密码必须提供 `currentPassword`。
- `cost.queryTimeout` 须为 0（不限）或不小于 100ms。
- `cost.requireNativeLimits: true` 时，经 transaction pooler 的 PostgreSQL 连接不能加载。
- Go API：移除未使用的 `relalg.OnConflict`、`relalg.Values` 与 `relalg.Insert.Conflict`。

## 0.1.14 - 2026-10-11

升级步骤汇总见 [`docs/releases/v0.1.14.md`](docs/releases/v0.1.14.md#迁移)。

### Added

- 可信 SQL 逃生通道 `execute_sql`（管理面 2，设计见 `docs/design/sql-escape-hatch.md`）：
  `tools.executeSql`（默认关闭，开启要求 `audit.enabled`）注册工具；用户须持有系统权限
  `sql:execute@<数据源>`（`roles[].permissions` 与 `users[].permissions` 取并集，本版唯一
  支持的系统权限），数据源须声明 `sqlEscape: {connection: <连接名>}`，三者缺一即
  `UNAUTHORIZED`；`tools/list` 按用户过滤。语句在声明的连接上原样执行（不解析、不改写、
  不脱敏），`query` 模式返回 `columns`/`rows`/`rowCount`/`truncated`，`exec` 模式返回
  `rowsAffected`；timeout、行数 / 字节上限、预算、限流复用受治理面且不可关闭；估算策略
  `cost.sqlEscape.estimate`（`required` 默认：EXPLAIN 失败即拒绝；`skip`）。审计 fail closed：
  执行前后各一条同步事件（`phase`、`datasource`、`connection`、`sql`、`rowsAffected`，
  `action: sql`），写入失败则不执行或不返回结果（新拒绝码 `AUDIT_UNAVAILABLE`）。
  `DATABASE_ERROR` / `DATASOURCE_FORBIDDEN` 对本工具回显驱动原文与 `constraints.sqlstate`
  （契约例外）。新增不变量 I29 与威胁 TM-013。
- 审计：`audit.Durable`（同步写入）接口，`FileSink.RecordSync`、`AsyncAuditor.Durably`。
- 管理面：GraphQL `Role.permissions`、`User.permissions`（输入同名）、`Datasource.sqlEscape`；
  工作区项 `RoleItem`/`UserItem` 新增 `permissions`（已保存的工作区会读作“已修改”）；
  控制台角色 / 用户页的“系统权限”区块、模拟页与数据源摘要的逃生通道提示。

### Changed

- `tools/list` 的结果因调用身份而异（只影响 `execute_sql` 的可见性）。
- EXPLAIN 解析把不读表的计划（PostgreSQL `Result` / `Values Scan`、MySQL `INSERT … VALUES`
  的目标节点、OceanBase `EXPRESSION`）视为点查：`cost.sqlEscape.estimate: required` 下
  常量查询与 `INSERT … VALUES` 可通过估算，受治理面的语句不受影响。
- 审计与预算对 `execute_sql` 的 `returnedRows` 以实际行数计（0 行不再记作 1）。

### Fixed

- HTTP 传输的 MCP 会话身份在 initialize 响应头写出时绑定，不再等处理函数返回：客户端拿到
  会话 ID 后立即发送的 `notifications/initialized` 此前可能先于绑定到达而得到 403。

## 0.1.13 - 2026-10-11

升级步骤汇总见 [`docs/releases/v0.1.13.md`](docs/releases/v0.1.13.md#迁移)。

### Added

- 执行总线 `core/bus`：所有触达数据库的执行按通道（工具调用 `serve`、管理读取
  `admin`、扫描 `scan`、后台 `background`）准入并共享 `rateLimit.ioPool`，非工具调用
  通道合计至多占一半；新增 `execution.{admin,scan,background}.{maxConcurrent,maxQueue}`
  配置（热重载生效）；管理面自省不再无界开连接；热重载期间新旧快照共享
  `maxInflight`；所有后台 goroutine 与定时器经总线登记、恢复 panic 并在关闭时排空，
  `internal/buscheck` 强制；OTel 指标 `smcp.bus.*`、GraphQL
  `serverStatus.execution` 与控制台概览页“执行资源”卡片。

- 批量配置（设计见 `docs/design/planning.md`）：
  - 扫描结果保存在配置库（schema 版本 3，启动时自动迁移），重启和多副本共享；扫描
    在服务端按批次进行，批次保存在配置库（关闭控制台、重启或切换副本都会继续，所有者
    停止心跳后由其他进程接管；每次运行有独立的所有者标识，每个库的进度按所有者条件
    写入，被接管的旧运行恢复后也不能覆盖新所有者；每个数据源最多 4 个未完成批次（计数与
    创建在配置库同一事务内，并发请求和多副本都不能越过）、每批最多 5000 个库；可停止、只重试失败项）；表目录在服务端跨库检索、分页并缓存，按库名通配、关键字、状态（未导入
    / 工作区未发布 / 已发布）与字段差异筛选；
  - 方案引擎 `x/configplan`：按范围与规则（导入、授权（可限定字段与行范围）、字段
    隐藏/脱敏、同步字段）生成方案，报告涉及对象、所依据的扫描、合并后的变化、分为
    阻断 / 需确认 / 提示三级的例外与生效方式；控制台导入、“按规则配置”、模板与配置
    MCP 共用；
  - 方案模板带版本与修改记录，保存时检测他人的修改；“重复执行”的模板在“待处理”页
    等待审核，不会自动修改工作区；待处理事项可指定负责人、稍后处理或注明原因忽略，
    有处理记录；
  - 配置 MCP `/admin/mcp`：每个客户端一个管理员令牌（命名、有效期、最近使用时间、
    单独吊销）；检索目录、扫描库、针对管理员的工作区预览方案（摘要 + 分页明细）、按
    预览标识把方案直接写进工作区（`plan_apply`）；不提供发布工具。
  - 配置服务 `x/admin/configapp`：目录、扫描批次、方案、模板、待处理、令牌、版本
    差异与权限模拟的业务规则和鉴权都在服务内，GraphQL 与配置 MCP 只做适配（MCP 不再
    经过 GraphQL resolver；结果类型由 gqlgen 直接绑定服务的类型）。
  - 配置库 schema 版本 4（启动时自动从版本 3 升级，原有令牌视为不受限，扫描批次的
    逐库进度从 payload 迁移成行）与版本 5（管理员工作区）。
  - 配置 MCP 令牌可限定部分数据源（`smcp_admin_tokens.scope`；条件命中数与
    预览快照也只在令牌的数据源内可读）；预览结果按
    `planId` 保留为快照，`plan_details` 只带 `planId` 从快照分页读取；
    `plan_apply` 必须用 `confirmed` 逐一列出管理员确认过的需确认例外 ID（例外新增
    `id`），一起预览的多个方案可依次应用到同一工作区；移除 MCP 的 `validate` 工具。
  - 目录共享索引：每张表的状态与差异按扫描、草稿与已发布版本只算一次，筛选只在索引
    上过滤并保存行位置；缓存同键并发未命中只算一次，按内存预算淘汰。翻页每页分配从
    约 10 MB 降到 0.6 MB，并发翻页 p95 从约 64 ms 降到 32 ms。
  - 扫描批次改为批次与库分表（`smcp_scan_batch_items`）：每个库按“所有者 + 当前
    状态”条件迁移，批次结束在同一事务内取消剩余的库；失败的库退避后自动重试（最多
    扫描 3 次）；写状态时配置库出错会重试（已提交但应答丢失的写入读回确认），仍失败则停止本次
    运行、保留批次待接管（接管时放回 pending 失败同样放弃接管），不会把未完成的库
    当作取消；请求可带 `requestKey`，同一请求再次发送得到原来的批次。
  - 预览与应用在服务入口统一规范化方案后再计算 `planId`（例如 `READ` 与 `read`），
    同一方案预览后原样应用不再被判为“已变化”。
  - 配置版本管理成为独立模块（`docs/design/versioning.md`）：`core/workspace`（项、
    三方合并、冲突、存储接口与一致性套件）与 `x/workspaceops`（版本操作服务，不依赖
    配置类型，depguard 强制）。工作区每人一份，保存在服务端（`smcp_workspaces` 与写入
    记录 `smcp_workspace_log`，schema 版本 5），管理员在任何一个控制台、他的 Agent 都
    直接写这一份，发布相当于提交。按项写入时带上读到的值，期间别处写过则服务端三方
    合并：不同位置都保留，同一处改得不同成为冲突，不丢任何一方。update、reset、合并
    草稿、解决冲突、保存草稿（检查点）与提交都是服务端操作（`updateWorkspace`、
    `resetWorkspace`、`mergeDraft`、`resolveConflict`、`checkpointWorkspace`、
    `commitWorkspace`、`workspaceLog`），提交绑定工作区版本与预期的已发布版本。
    Agent 的 `plan_apply` 与方案页的“加入工作区”（`applyPlan`）是同一段服务端逻辑，
    写入绑定预览时的实际影响（逐字段）。服务端写出的项与控制台从版本构造的完全一致
    （共用测试数据锁定）。用户的令牌哈希是项的一部分（读模型 `User` 新增 `tokenHash`，
    只有 hash），合并草稿、update 与并发写都会带上令牌的生成、轮换与撤销，两边改成
    不同的值成为冲突。控制台不再做合并与冲突计算（删除 `lib/merge.ts`），浏览器
    不保存工作区，换账户即丢弃内存状态并停止旧的同步。
  - `revision.Store.Sequence`：配置库的 revision 变更序号（创建、发布、回滚、丢弃
    都会增长；SQL 存储为下一个 ID 加 `smcp_store_meta.revision_seq`，schema 版本 5
    写入，工作区、令牌等其他写入不影响它），`/admin/events` 据此推送 `revisions`，
    丢弃较早的草稿也会通知其他控制台。
  - 升级前控制台不分管理员保存在浏览器里的工作区不再自动归属或删除：有未发布修改
    时提示，由管理员确认“是我的”后恢复到服务端的工作区（仅在自己的工作区没有未发布
    修改时），或丢弃。`/admin/events` 增加 `event: workspace`（工作区被写入）与
    `event: revisions`（草稿列表变化）。
  - 页头“待审草稿”：列出所有尚未处理的草稿（`drafts` 查询，基准版本由服务端沿父链
    求得），可“在工作区审核”（同样转成未发布修改，
    落后的草稿自动合并到最新发布）或丢弃；版本状态新增 `discarded`（`discardDraft`）。
    版本历史中草稿的差异改为相对其自身基准；发布落后草稿不再报原始冲突错误，而是
    引导在工作区审核。发布后运行状态持续刷新，直到服务完成加载。
  - 版本差异（控制台与配置 MCP 的 `diff` 共用）在生成前对两侧配置结构化脱敏：DSN
    密码与 HTTP token 一律替换为 `***`，上下文行里也不出现；变化了的标为
    `(changed)`；已无法解码的版本不显示内容。CLI `store diff --raw` 仍比较原文。
- 控制台导入页重做：数据源连接详情默认折叠、库扫描批次（进度、停止、重试）、跨页
  选择与“选择全部匹配的 N 项”（保存筛选快照与排除项）、可分页搜索导出的已选清单、
  导入前的方案预览（可排除表、改名、钻取差异）、加入后的下一步入口；同步字段时被行
  范围等引用的字段不能删除，并可直接跳到引用位置；“全部扫描”重新扫描列出的所有库，
  “同步所有字段”对筛选范围内字段有差异的表生成一个同步方案预览。扫描不按时间标记
  “过期”：表结构是否变化与扫描时间无关，以重新扫描的结果为准。

### Fixed

- 未知的字段脱敏规则（如 `mask: x`）以前能通过校验并发布，但热加载失败，重启时服务
  无法启动；现在配置校验只接受内置规则（`email`、`idcard`、`phone`、`secret`），
  预览、校验、保存草稿、导入与发布都会拒绝。另外，存储与发布前还会执行运行时组装中
  不依赖数据库的检查（`bootstrap.CheckOffline`：实体、授权、脱敏器），`validate`
  命令同样执行，运行时会拒绝加载的配置不能写入配置库或发布。
- 控制台在载入草稿后，目录状态、范围统计和方案预览按已发布版本计算（只在有未保存
  修改时才带上工作区），方案加入工作区时可能覆盖草稿里的字段设置；现在始终基于
  工作区当前的基准版本。单表同步在找不到表时会把所有字段当作已删除并默认勾选移除；
  现在只在读到表结构后计算差异，找不到表时提示，重新打开时清空上次的勾选。
- 控制台登录页在密码框按回车会提交两次，第二次读不到原页面地址，登录后落到概览页
  而不是原来要去的页面。
- 很多库中有同名表时（分库），按库名查找表会复制所有同名表，5 万张表的检索内存
  分配从约 2.6 GB 降到约 40 MB。
- MySQL/OceanBase 成本闸门误拒小查询：
  - 小表（配置表、维表）的全表扫描评分一律为 20，在默认 `rejectFullScan` 与
    `hardScore: 40` 下被硬拒绝。新增 `cost.fullScanMaxRows`（默认 1000）：被全表
    扫描的表本身不超过这么多行时按普通顺序扫描评分并放行，更大的表仍被拒绝；设为 0
    恢复此前行为。表的行数取自表的规模而不是计划的输出行数（大表按未建索引的条件
    筛出一行仍要读完整张表）：PostgreSQL 按 `pg_class` 的统计换算当前页数（从未
    ANALYZE 的表按每页最多行数估上限），MySQL 取全表扫描的 `rows_examined_per_scan`，
    OceanBase 按表（别名）取 `EXPLAIN EXTENDED` 的 `table_rows`；任何一张全表扫描
    的表得不到行数时都不豁免。
  - MySQL 在执行前就判定不读表的计划（唯一键未命中 `no matching row in const
    table`、`Impossible WHERE`、`Select tables optimized away` 等）只有提示没有
    表节点，被当作“未知扫描”拒绝；现在按点查（0 行）处理。
- 后台协程（导入页的扫描任务、发布后的权限探测、各数据源并行对账、发布推送）
  中的 panic 此前会使整个进程退出（MCP 与控制台一起中断，网关返回 5xx）；现在记录
  堆栈并使该任务失败，对账失败时重载保留原配置。
- 控制台不再把网关的错误页原文（HTML）显示为错误信息，改为区分“没有连上服务”、
  “网关返回 HTTP 状态码”和“网关超时”；`docs/console.md` 补充放在网关后的要求
  （会话粘滞、超时、推送连接）。
- 控制台：同一浏览器中另一个标签页登录（会话 Cookie 被替换）后，原标签页的请求
  被拒绝（403），检查并发布页一直停在“正在重新检查”。现在重新读取会话：仍是同一
  管理员时换用新的 CSRF token 并重试，已换成其他管理员或已退出时回到登录页，不会
  以其他管理员的身份操作；检查失败时显示错误与“重新检查”。
- 两个管理员几乎同时发布时，后提交的一方被配置库拒绝但没有 `CONFLICT` 错误码，
  控制台不提示基于新版本变基；现在返回 `CONFLICT` 与最新的已发布版本。
- 预算：租户限额与角色限额逐字段取更严的值（此前整体替换，租户未设的项会解除角色
  限制）。
- 事务：过期回收不再在全局锁内等待数据库回滚；提交与回滚不再受熔断、自适应并发与
  IO 槽阻挡（仍受每秒请求数限制，panic 同样被恢复）；等待一个正在结束的事务以调用方
  的超时为限；关闭进程有统一期限（15 秒）。
- 熔断只计数据库与连接错误（白名单），调用方取消、约束违反与权限拒绝不再触发熔断。

### Changed

- 新增 `server.pathPrefix`：网关把服务挂在路径前缀下时，控制台页面的 `<base>`、
  会话 Cookie 路径与重定向由服务带上前缀，网关只需剥掉前缀，不再需要改写响应体、
  Cookie 与 `Location`。控制台的脚本与 API 地址改为相对页面 `<base>`；CSP 的
  `base-uri` 由 `'none'` 改为 `'self'`。
- 控制台的校验、差异、草稿请求只提交改动过的配置节（未改动的由服务端取自 base，
  删除的设置节以 null 提交），请求体从整份配置降为改动部分；自动重新检查的防抖
  放宽到 800ms。`/admin/graphql` 的响应在客户端支持时 gzip 压缩（页面与脚本不压缩，
  网关仍可改写其中的路径）。
- 用户页生成 token 后给出各客户端的接入配置（Claude Code 命令、Cursor 与 VS Code 的
  `mcp.json`、通用 JSON、curl 连通性测试），每段可一键复制。新增配置
  `server.publicURL`：客户端经网关或反向代理访问 MCP 端点的完整地址，接入配置以它
  为准，未设置时退回控制台所在地址并提示。
- 权限矩阵默认只看已授权的实体（没有任何授权时显示全部）。
- 发布只允许快进：草稿必须基于当前已发布版本，否则返回 `CONFLICT`（CLI `store
  publish`、未带 `expectedPublished` 的 API 调用同样生效），基于旧版本的草稿不能再
  整体覆盖期间别人的发布。**Breaking**：此前可直接发布的旧草稿需基于新版本重建。
- 新增 `GET /admin/events`（server-sent events）推送新发布，控制台立即提示变基，
  不再等待 10 秒轮询。
- 控制台变基改为细粒度三方合并（字段、关系按名称，授权按实体，同一实体的多条授权
  作为一组），双方改了同一处时
  记为冲突，必须逐项选择保留哪一方，全部解决前不能保存或发布（此前默认保留本地
  值，可直接发布覆盖对方）。
- 编辑领域收归服务端（`docs/design/editing.md`）：工作区项的类型与编解码只在
  `x/admin/configapp`；改名、删除引起的引用维护由服务端 `Reconcile` 完成，控制台只写
  `renamedFrom`；目录、方案预览、校验、diff、模拟默认读调用者的工作区。
- 配置分为意图（`config.Spec`）与生效值（`docs/design/spec.md`）：配置库只存写过的
  值，旧 payload 逐字节兼容；工作区合并规则按路径由项类型标签声明，控制台共用。
- 进程级状态（连接及其熔断/自适应并发、事务、预算、写跟踪、缓存、结果内存）归
  `bootstrap.Shared`，重载不再搬运状态（`docs/design/lifetimes.md`）。熔断与自适应并发
  按数据库连接隔离，半开只放行一个探测，一批同时超时只减半一次；新增
  `transactions.maxOpenPerConnection`（默认 `ioPool/2`）。
- 已发布 revision 能加载但装配失败（如 schema 漂移）时，它删除、禁用或改动的用户
  立即暂停并关闭会话，不再等待整个版本应用（TM-007/TM-010）。
- 数据面固定管线（`docs/design/pipeline.md`）：数据库只由执行器调用，语句须由授权
  结果与过闸 SQL 构成（结构测试强制）；结果逐行计量，越过 `cost.maxBytes`/预算的第一行
  即中止；新增 `rateLimit.maxResultMemory`（默认 256 MiB）约束进程同时持有的结果。
- 新增错误码 `OUTCOME_UNKNOWN`（不可重试）：写入或提交期间连接中断或超时；相关
  缓存按已写入失效。事务令牌结束后 10 分钟内，同一调用方再用会得到
  `TRANSACTION_NOT_FOUND` 附 `constraints.outcome`（`committed`/`rolledBack`/`expired`/
  `unknown`）。
- 控制台：ESLint 进入 `make web`；消息键存在性测试；实时更新在 SSE 连接期间把
  轮询从 10 秒降到 60 秒。新增术语表 `docs/glossary.md`，部署模式与预算分层写入
  `docs/configuration.md`。
- 代码整理（行为不变）：扫描批次的跨进程租约、心跳、接管与重试抽成 `x/leasing`
  （交错模型测试锁定其不变量）；`core/store/storetest`、`core/cost/costtest` 承载测试
  替身，生产包不再编译它们；`x/providers/sqladapter` 的 `Base` 与 `Scanned` 为三个
  provider 共用连接骨架与扫描累积；配置库行扫描、占位符与加锁事务收敛为泛型助手；
  装配入口收敛为一个 `assembly`；`internal/kit` 收拢指针/切片小工具；`config.Spec`
  成为管理面唯一的内存表示，工作区写入按索引单遍应用，合并按子树记忆规范形式。
- `/metrics` 改由 OpenTelemetry 指标经 Prometheus exporter 提供，指标名、标签与
  直方图分桶不变（标签按名称排序输出）。

### Breaking

- 租户 `budget.tenants.<t>.maxConcurrent` 改为按租户聚合（所有会话、所有主体合计），
  此前按会话计数，开新会话即可绕过。
- 新增 `transactions.maxOpenPerConnection`，默认 `max(1, rateLimit.ioPool/2)`：同一连接上
  同时打开的显式事务此前不限（只受 `transactions.maxOpen` 约束），升级后超出的 `begin`
  返回 `TRANSACTION_CAPACITY`；需要原行为时设为 `rateLimit.ioPool`（事务可占满连接池）。
- GraphQL：`ServerStatus.pending: PendingRevision` 改名为
  `unapplied: UnappliedRevision`；移除 `DraftInput` 与 `createDraft`（改用工作区与
  `checkpointWorkspace`），查询的 `draft` 参数改为 `revision`（默认调用者的工作区）。
- GraphQL 配置读模型（`Configuration.entities/roles/users`、方案预览与导入候选的
  `Entity`）改为与工作区项同一形状：如实返回配置写了什么，未设置即 null（`mcp`、
  `allowCascade`、`Field.exclude`、`Grant.fieldsRestricted`、`User.disabled` 变为可空），
  移除可推导的 `Entity.id`、`Role.members`、`User.hasToken`（无令牌即 `tokenHash` 为 null）；
  `ScanState`、`TableStatus`、`Action` 枚举在 Go 侧为字符串。
- Go API：`relalg.IsNull` 谓词移除，改用 `Condition{Op: OpIsNull/OpIsNotNull}`；
  `engine.Submit`/`engine.Run` 改为泛型包级函数；`mcpserver.HTTPConfig` 的鉴权字段
  移入嵌入的 `HTTPAuth`；`store.Fake*`、`cost.Fake*` 移至 `storetest`/`costtest`；
  `tool.OutcomeUnknownError` 类型移除（`errors.Is(err, ErrOutcomeUnknown)` 不变）。

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
