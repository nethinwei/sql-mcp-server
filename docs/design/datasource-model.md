# 设计评审：数据源模型（物理身份、多连接路由、权限感知、键与约束）

状态：**评审结论（2026-10-08）**。按文末“分阶段交付”实现，实现以本文为准。
相关设计：[用户、角色与权限模型](authorization-model.md)、[配置存储与 Revision](config-store.md)。

## 背景与问题

现状（代码事实）：

- 一个 `databases.<name>` 只有一条 DSN（`core/config.DatabaseConfig{Driver, DSN}`），实体通过
  `datasource + schema + source` 隐式指向一张表，没有“物理库 / 物理表”这一层。
- 缓存按实体名记录和失效（`core/tool/tool.go` 的 `afterWrite`、`transaction.go` 提交时的失效）。
- 导入只扫描 `BASE TABLE`（`x/providers/postgres/introspect.go`、`x/providers/mysql/introspect.go`），
  只读取主键与外键；多列外键会被跳过（`x/admin/graph/importer.go` 的 `candidateEntity`）。
- 单行定位只认主键（`core/codegen` 的 `isPKPoint`、`core/cost` 的 `StaticRule` / `WriteGuard`）。
- 不探测连接账号的数据库权限；数据库报错一律映射为 `DATABASE_ERROR`。

由此产生的问题：

1. **同名表**：不同库或不同 schema 里有同名表时，导入页只能靠 `schema_表名`、`库名_表名`、`_2` 避免名字冲突；
   控制台只显示实体名，分不清对应的是哪个库的哪张表。
2. **多个 DSN 指向同一个库**（只读账号、读写账号、只读副本）：只能配成两个数据源，同一张表变成两个实体。
   - 一个实体写入后，另一个实体的缓存在 TTL 内继续返回旧数据；
   - 租户策略、行过滤、脱敏要维护两份，漏配一份就成了绕过口；
   - 做不到“读走只读账号或副本、写走读写账号”。
3. **不感知权限**：给只读账号的表授“修改”，界面照样能勾选，运行时才失败，agent 只收到笼统的错误。
4. **键与约束覆盖不全**：
   - 只有唯一键的表无法按行修改；
   - 级联外键、触发器带来的副作用不受授权约束；
   - 违反约束时 agent 不知道该怎么改请求。
5. **现存 BUG**：配置允许 `kind: view`，但对账时视图被判为“缺失”，服务无法启动。

## 目标与非目标

目标：

- 引入“物理层 / 逻辑层”两层模型，使上述场景都由同一组不变量推出；
- 支持同一个库下的多个连接（不同账号、只读副本），按动作路由，并保证读后写一致；
- 感知连接权限并用于提示，运行时由数据库裁决，返回稳定的拒绝码；
- 全面覆盖主键、唯一键、外键及其它约束在网关里的作用；
- 现有配置（单 `dsn`）不改动即可继续使用。

非目标：

- 分库分表的逻辑表（一张逻辑表对应多个物理分片、跨分片查询）。需要时把分片代理（ShardingSphere-Proxy、
  Vitess、Citus）作为普通数据源接入；
- 每租户一个 schema / 每租户一个库的动态路由（按调用者身份选 schema）；
- 跨库事务（两阶段提交）、PG 函数（用 SELECT 调用）、OUT / INOUT 参数；
- 凭证轮换（IAM token、Kerberos），以及运行中刷新 DSN。

## 术语

| 术语 | 含义 |
|---|---|
| 数据库（database） | 配置中 `databases.<name>`，代表一个逻辑上的物理库，即实体的 `datasource` |
| 连接（connection） | 访问该库的一个端点加一个账号；角色为 `primary` 或 `replica` |
| 路由（routing） | 动作类别（read / write / execute）到连接的映射 |
| 物理身份 | `(服务器指纹, catalog, schema, 对象名)`，标识一个物理关系（表、视图、过程） |
| 身份键 | 能唯一定位一行的键：主键，或满足纳入条件的唯一键 |
| 能力（capability） | 某个连接对某个物理关系能否执行某个动作：`granted` / `denied` / `unknown` |

## 不变量

- **I-1 一张物理表最多对应一个实体。** 策略、缓存、审计只有一份。比较物理身份前，先按方言规则规范化
  标识符（见“标识符规范化”）。
- **I-2 动作决定连接。** 每个数据库声明 read / write / execute 各走哪个连接；事务固定在一个连接上。
- **I-3 有效能力 = 授权 ∩ 连接权限 ∩ 实体类型。** 权限快照只用于提示；数据库永远是最终裁决者（fail closed）。
- **I-4 单行定位只看身份键。** 成本门禁的免估算快速通道、写入安全检查、游标分页、新增后返回该行，都基于
  身份键。
- **I-5 只有数据库强制执行的键才能用于写入安全。** 视图、外部表上人工声明的键只用于读。
- **I-6 一次写入的全部影响都必须被授权覆盖。** 级联、触发器、存储过程的副作用，要么要求调用方对受影响的
  实体也有权限，要么按保守规则处理，不能借“只写了一个实体”绕过其它实体的策略。

## 配置结构（提案）

```yaml
databases:
  shop:                          # 简写：一个 primary 连接承担全部动作（现有写法，不变）
    driver: postgres
    dsn: ${SHOP_DSN}
  crm:
    driver: mysql
    connections:
      rw:      { dsn: ${CRM_RW} }                    # role 默认 primary
      ro:      { dsn: ${CRM_RO} }                    # 同一主库的只读账号
      replica: { dsn: ${CRM_REPLICA}, role: replica }
      pooled:  { dsn: ${CRM_POOL}, pooler: transaction }   # 经过事务模式的连接池代理
    routing: { read: replica, write: rw, execute: rw }
    readAfterWrite: 5s
entities:
  - name: crm_orders
    datasource: crm
    schema: sales
    source: orders
    primaryKey: [id]              # 表：期望值，以数据库为准（I-5）
    uniqueKeys: [[order_no]]
    allowCascade: true            # 显式允许级联写入（I-6）
    relationships:
      - name: items
        target: crm_order_items
        cardinality: has-many
        joinOn: { id: order_id }
  - name: refresh_kpi
    kind: procedure
    affects: [crm_orders]         # 过程写入的实体；不声明则失效整个库的缓存
```

规则：

- `dsn` 与 `connections` 二选一。简写等价于一个名为 `default` 的 primary 连接，`routing` 全部指向它。
- 有多个连接时 `routing` 必须写全，不做推断。`replica` 不能用作 write / execute。
- 动作归类：
  - read：read、aggregate、describe、EXPLAIN 和只读事务；
  - write：create、update、delete 和读写事务；
  - execute：存储过程。
- `readAfterWrite > 0` 时：同一 MCP 会话在某库写入后的这段时间内，该库的读改走 write 连接。此时 write 连接
  必须有 SELECT 权限（启动时检查）。
- `connections`、`routing`、`pooler` 的变化需要重启（与现有 DSN 一致）；`readAfterWrite` 可以热重载。
- 预留 `catalog` 字段（SQL Server / Trino 的三段式命名），现有 provider 不使用。

## 物理身份

- 指纹：
  - PG：`system_identifier`；读不到时退回 `inet_server_addr():port`，catalog 取 `current_database()`；
  - MySQL / OB：`@@server_uuid`，catalog 为空，schema 即库名。

  指纹只用来告警，例如两个 `databases` 指向同一个库（建议合并为多个连接），或同一数据库的多个 primary 连接不在
  同一个库上。指纹不参与授权判断：副本的指纹本来就可能不同，故障切换后指纹也会变。
- schema 解析：不带 `schema` 的实体解析为连接的默认 schema。启动时检查同一数据库的各连接解析结果一致，
  不一致就要求实体显式写 schema。
- **标识符规范化**：方言新增 `NormalizeIdent`。
  - PG：按引号规则处理，网关始终加引号，因此保持原样；
  - MySQL：按 `lower_case_table_names` 决定表名是否折叠成小写；
  - Oracle：未来按折叠成大写处理。

  I-1 唯一性校验和导入页匹配都先规范化再比较。
- 同名表：导入命名规则不变（`schema_表名` / `库名_表名` / `_n`）。控制台各处统一显示
  `数据源 · schema.表名`；`describe_entities` 与 `sql-mcp://schema` 增加 `datasource` 字段（兼容变更）。

## 关系类型

| 类型 | 导入 | 写入 | 缓存 |
|---|---|---|---|
| 表 | ✅ | 按权限 | 按物理表失效 |
| 视图 | ✅（修复现存 BUG） | 仅可更新视图，键只用于读（I-5） | 同库任何写入后失效 |
| 物化视图（PG） | ✅（查 `pg_class.relkind`） | 不适用 | 同库任何写入后失效 |
| 外部表（FDW / FEDERATED） | ✅，标注“外部” | 按权限；成本估算不可信，按 fail closed 处理 | 同库任何写入后失效 |
| PG 子分区 | 隐藏，只导入父表；配置引用子分区时告警 | — | — |
| PG 继承表 | ✅；父子同时暴露时标为“数据重叠” | 按权限 | 父子互相失效 |
| 存储过程 | 现由配置声明 | 不适用 | 按 `affects`，未声明则失效整个库 |
| 临时表 | 不导入 | — | — |

## 多连接路由与一致性

- 装配：每个连接建一个 provider，复用 `providerregistry.New`，连接池按连接分别配置（`configurePool`）。
- `tool.DataSource` 从单个 DB 改为 `Read` / `Write` / `Execute` 三个 `store.DB` 加 `TxBeginners`，
  `routeEntity` 按动作选择连接。
- 成本门禁的 EXPLAIN 走 read 连接；成本反馈与预算按数据库统计，不按连接。
- 事务：`begin_transaction` 的 `readOnly` 为真时走 read 连接，否则走 write 连接；句柄固定在该连接上，
  事务内的读写都经过它。
- 读后写一致性：会话级记录“数据库 → 最近一次写入的时间”，生命周期与预算的 `CloseSession` 一致。窗口内的
  读取（含只读事务）走 write 连接，并且不读写共享的读缓存、不与其他会话合并在途请求：其他会话可能已用
  副本上的旧数据填充了它们。
- `pooler: transaction`：关闭预编译缓存（`store.WithPreparedCache` 不启用）；PG 改用不预编译的执行模式且不
  设置 `statement_timeout` 启动参数（代理不转发），超时依靠请求取消；MySQL 改为驱动端插值参数。

## 权限感知

- 新增可选接口 `introspect.PrivilegeInspector`，按连接探测表级与列级的 SELECT / INSERT / UPDATE / DELETE
  以及过程的 EXECUTE，结果三态：
  - PG：`has_table_privilege`、`has_column_privilege`、`has_function_privilege`（对过程同样适用；有重载时为
    `unknown`）；
  - MySQL / OB：查 `information_schema` 中 USER、SCHEMA、TABLE、COLUMN 四级权限表。通过 MySQL 8 角色获得的
    权限查不到，判为 `unknown`。
- 只读状态：连接启动时读取 PG `default_transaction_read_only` / `pg_is_in_recovery()`，或 MySQL `read_only` /
  `super_read_only`。只读连接的写和执行能力一律为 `denied`。
- 能力的用途：
  - 管理 API：`capabilities` 查询返回运行中快照的能力（每个实体动作的三态、路由连接、列级权限的列和原因）；
  - 发布检查：`validate` 的 `warnings` 列出超出能力的授权，不阻断；能力以运行中服务装配时的探测为准，
    草稿中新增的实体没有能力数据，不告警；
  - 启动与重载：记录日志；
  - 控制台：权限矩阵单元格有四种状态，分别是可授权、类型不适用、连接无此权限（置灰并提示缺少的权限）、
    未知（可勾选，加提示）。
- 运行时：provider 把权限类错误包装为 `store.ErrPermissionDenied`，网关映射为拒绝码
  `DATASOURCE_FORBIDDEN`（不可重试），并且不计入熔断。错误分类：PG SQLSTATE `42501`、`25006`（只读事务），
  MySQL `1142` / `1143` / `1370` / `1044` / `1290`（`read_only`）。
- 尚未覆盖：MySQL 触发器与例程级授权的探测结果为 `unknown`；pgbouncer 事务模式与 PG 流复制副本没有容器
  测试，路由由同库两个账号的集成测试覆盖。

## 键与约束

键在网关里承担五种作用：行身份、关联、写入副作用、写入合法性、描述。每种约束都按这五种作用定义处理方式。

### 行身份（I-4、I-5）

- 来源：表的键以数据库为准，启动时读取；配置中的 `primaryKey` / `uniqueKeys` 是期望值，不一致时报告漂移告警。
  视图、外部表只能用人工声明的键，且只用于读。
- 唯一键纳入条件：
  - 来自唯一约束或唯一索引（PG 统一读 `pg_index.indisunique`），且所有列非空；
  - PG15 的 `NULLS NOT DISTINCT` 放宽非空要求；
  - 部分索引（WHERE）、表达式索引、MySQL 前缀索引不纳入，导入页标注“不可用作身份”；
  - 可延迟（DEFERRABLE）约束只在事务外当作身份键使用。
- 单行定位：`codegen.WithPrimaryKey` 改为 `WithKeys`，`Compiled.IsPKPoint` 改为 `IsKeyPoint`。
  - 任一身份键的全部列都按等值匹配、且谓词是纯合取时为真（沿用现有的 `flattenAnd` 判定）；
  - `cost.requirePKForWrite` 保留名字，语义扩展为“身份键”。
- 游标分页：用主键；没有主键时用第一个身份唯一键；都没有时拒绝游标分页。
- 无身份键的表：update / delete 与游标分页一律拒绝，实体页提示。
- 投影：读取一律显式列出字段，不再生成 `SELECT *`。这样 MySQL 8 的不可见列（含自动生成的不可见主键
  `my_row_id`）也能被读到。
- 身份键列被隐藏或脱敏、或授权的字段范围不含身份键列时，发布或权限矩阵给出提示（该角色无法按行修改）。
- MySQL 新增后返回该行：自增主键用 `lastInsertId`；其余情况由调用方提供的身份键值组装。

### 关联（外键）

- 导入：单列 / 多列 / 自引用 / 跨 schema / 引用唯一键的外键都生成关联，多列外键生成多对 `joinOn`。
- expand：支持多列关联。每批渲染为 `(a=? AND b=?) OR …`，批大小按列数折算到 `maxINListSize` 以内。
- `belongs-to` / `one` 关联的目标列不是身份键时，发布告警（关联会命中多行）。
- 关联只是查询提示，不假设引用完整性（考虑到 MyISAM、`foreign_key_checks=0`、`NOT VALID`、`MATCH SIMPLE`）。
- 跨数据源的关联继续拒绝。

### 写入副作用（I-6）

- **级联**：导入时读取外键的 `ON DELETE` / `ON UPDATE` 动作。对存在级联引用的实体执行 delete / update 时：
  - 调用方必须对级联链上每一层被级联的实体拥有对应权限（`ON DELETE CASCADE` 要求 delete；`SET NULL` /
    `SET DEFAULT` / `ON UPDATE CASCADE` 要求 update，且字段权限须覆盖被改写的外键列），该权限不带行范围
    限制（数据库会改写所有引用行，网关无法对它们施加行策略），否则拒绝；update 只在修改了被引用列时才
    检查；被删除的行继续按其 `ON DELETE` 级联，被改写的外键列继续按其 `ON UPDATE` 级联，环按已访问跳过；
  - 被级联的表没有暴露为实体时无法证明授权，拒绝；
  - 整条级联链上的实体，其缓存一并失效；链上有未暴露的表时，其后续级联未知，失效整个库；
  - 父实体声明 `allowCascade: true` 可以显式放开（实现时从关联级改为实体级：被级联的表可能根本没有关联
    可以挂这个开关），审查页高亮显示。
- **触发器、规则、INSTEAD OF**：导入时探测（PG `pg_trigger` / `pg_rules`，MySQL `information_schema.TRIGGERS`），
  实体标记“有副作用”。写入后失效整个库的缓存，审查页提示。
- **存储过程**：按 `affects` 失效；未声明时失效整个库。

### 写入合法性

违反约束时映射为新拒绝码 `CONSTRAINT_VIOLATION`（可重试，兼容变更）：

| kind | PG | MySQL |
|---|---|---|
| `unique` | 23505 | 1062 |
| `foreign_key` | 23503 | 1452 / 1451 |
| `not_null` | 23502 | 1048 |
| `check` | 23514 | 3819 |
| `exclusion` | 23P01 | — |

`constraints.kind` 给出类别；`constraints.fields` 只列调用方可见的字段。**不透出约束名和数据库原文**，避免泄露
结构信息。

### 描述

`describe_entities` 与 `sql-mcp://schema` 的字段增加 `required` 与 `readOnly`（兼容变更）：

- `required`：非空且无默认值，也不是自增列；
- `readOnly`：`GENERATED ALWAYS` 的生成列或自增列、虚拟列、存储生成列。网关直接拒绝写入这类列，字段的写
  授权也随之置灰。

## 边界汇总（明确拒绝 / 降级）

| 场景 | 结论 |
|---|---|
| 无身份键表的 update / delete、游标分页 | ⛔ 拒绝 |
| 默认情况下的级联写入（未声明 `allowCascade` 且缺少子实体的无行范围权限，或级联到未暴露的表） | ⛔ 拒绝 |
| 跨数据源的关联、跨库事务 | ⛔ 拒绝 |
| 每租户一个 schema 的动态路由、分库分表的逻辑表 | ⛔ 不支持（非目标） |
| PG 函数、OUT / INOUT 参数 | ⛔ 不支持 |
| MySQL 角色权限、重载过程的权限、序列权限 | ⚠ 判为 `unknown`，由数据库裁决 |
| 外部表的成本估算 | ⚠ 按 fail closed 处理 |
| PG 继承表父子同时暴露 | ⚠ 提示数据重叠 |
| 凭证轮换 | ⛔ 本轮不做 |

## 实现落点

| 层 | 文件 |
|---|---|
| 配置 | `core/config/{config.go,config_validate.go,fields.yaml}`（`go generate ./core/config`） |
| 模型 | `core/entity/entity.go`（Keys 增加 enforced / declared 标记、关系类型、副作用标记） |
| 数据面 | `core/tool/{tool.go,transaction.go,tools_read_impl.go,tools_write_impl.go,denial.go}`、`core/cache`、`core/codegen`、`core/cost/layers.go`、`core/store/store.go` |
| 元数据 | `core/introspect`（`PrivilegeInspector`、键、关系类型）、`x/providers/{postgres,mysql}`、`x/providers/sqladapter`（错误分类） |
| 装配 | `x/bootstrap/{bootstrap.go,bootstrap_assemble.go,bootstrap_entity.go}` |
| 管理面 | `x/admin/graph/{schema.graphqls,importer.go,convert.go}`、`x/revisionops` |
| 控制台 | `web/admin/src/{views/DatasourcesView.vue,views/EntitiesView.vue,components/GrantMatrix.vue,views/ReviewView.vue}` |
| 文档 | 本文、`docs/{architecture.md,configuration.md,tool-contract.md,provider-compatibility.md}` |

## 分阶段交付

| 阶段 | 内容 | 兼容性 |
|---|---|---|
| P0 | 修复视图 BUG（纳入视图与物化视图）；物理身份与 I-1；`NormalizeIdent`；缓存按物理身份失效、视图类整库失效；过程 `affects`；控制台显示物理位置；describe 增加 datasource；PG 子分区不导入 | 重复暴露同一物理表的配置会被拒绝（Breaking） |
| P1 | connections / routing / readAfterWrite / 副本；`pooler: transaction`；schema 一致性检查；事务固定连接；只读状态探测 | 兼容 |
| P2 | 表级与列级权限探测、能力、发布告警、矩阵置灰、`DATASOURCE_FORBIDDEN` | 兼容 |
| P3 | 键与约束全量：身份键（I-4 / I-5）、`IsKeyPoint`、显式投影、MySQL 新增后返回键、级联与触发器（I-6）、`CONSTRAINT_VIOLATION`、required / readOnly、多列外键与 expand | 默认拒绝级联写入（Breaking）；其余兼容 |

每个阶段独立提交，并同步 CHANGELOG、契约快照与文档。

## 安全影响与验收

- I-1 消除“同一张表、两套策略”的绕过口；I-6 消除级联和触发器绕过子实体策略的问题。
- 权限探测只用于提示，不改变授权结果；运行时仍由数据库裁决。
- 新拒绝码不含约束名、SQL 原文与数据库报错原文。
- 验收：
  - 单元测试覆盖每条不变量的正反用例；
  - 集成测试（PG、MySQL）使用只读与读写两个角色、视图、物化视图、子分区、唯一索引的各种形态、多列外键、
    级联外键、触发器、生成列、不可见主键；
  - pgbouncer 事务模式与 PG 流复制放在 `ci-full`；
  - 控制台用 vitest 覆盖矩阵四种状态与物理位置显示。

## 评审结论

1. **I-1 直接报错**，不设告警过渡期（v0.x 允许破坏）。已有“同一张表配了两个实体”的配置，需要合并为一个
   实体，多个账号改为多个连接，迁移步骤写入 CHANGELOG。检查分两层：
   - 发布时做静态检查，拦截同一数据源、同一 schema、同一 source 的完全重复；
   - 启动和重载时做精确检查，覆盖标识符规范化、默认 schema 解析和服务器指纹。重载失败时保留旧快照。
2. **级联写入**：调用方缺少被级联子实体的对应权限时拒绝；实体上声明 `allowCascade: true` 可以显式放开。
   属于破坏性变化。
3. **视图类缓存先只做保守规则**（同库任何写入后都失效），暂不提供 `dependsOn`。
