# 设计评审：配置存储与 Revision

状态：**评审结论（2026-10-03，管理面 3，预期 v0.1.12）**。本文把
[Revision 与 Snapshot](revision-snapshot.md) 的评审结论落到可实现的存储边界、
表结构、发布流程与运行时接入，实现以本文为准。对应主
[Roadmap](../roadmap.md#管理面-3--structured-config-store-and-revisions)
管理面 3。

## 背景与问题

现状（代码事实）：

- 配置只能来自一个 YAML 文件：`x/configyaml.Load` → 解码、默认值、校验；
  `bootstrap.Assemble` 再解析 DSN 占位符并连接数据库。
- 热重载靠 `Runtime.Watch` 轮询文件内容哈希，变化后 `Runtime.Reload(path)`
  重建 snapshot；构建失败保留旧 snapshot。
- `sql-mcp-server export` 输出确定性 YAML（`${...}` 占位符原样保留），已有
  "导出 → 校验 → 再导出字节一致"的测试。
- 用户、角色（v0.1.11）都在同一份配置里，没有独立于文件的持久化位置。

问题：配置没有版本历史，无法 diff、回滚或审计"谁在什么时候改了什么"；部署
迁移需要手工搬运文件；后续的管理 API 与管理后台（管理面 4、5）没有可写入的
结构化存储。

## 目标与非目标

目标：

- 引入 `ConfigStore`，以 **revision** 为单位持久化整份配置，支持 draft、
  publish、rollback、diff 与历史查询；
- 首批实现：本地 SQLite（默认）与已接入的 PostgreSQL / MySQL / OceanBase；
- 服务可以从 store 启动，并在有新发布时热重载，与文件 watch 走同一条构建链；
- YAML、SQLite、服务端数据库之间可以确定性迁移，`contentHash` 一致；
- 不使用 store 时行为完全不变。

非目标（本版）：

- 管理 API、管理后台（管理面 4、5）；本版只提供 CLI；
- 多写、分布式共识、跨实例的发布协调（多实例各自轮询同一 store 即可得到最终
  一致，但不承诺发布时刻一致，仍属 L7）；
- 按字段的细粒度变更、并发编辑合并；
- 在 store 中保存任何查询结果或业务数据。

## Revision 模型（结论）

沿用 revision-snapshot 设计的数据模型：

| 字段 | 含义 |
| --- | --- |
| `id` | 单调递增，发布后不可复用 |
| `parent_id` | 创建该 draft 时基于的 revision（可空） |
| `content_hash` | `sha256:` + payload 字节的 SHA-256 |
| `payload` | 规范化后的确定性 export YAML |
| `state` | `draft` / `published` / `superseded` / `rolled-back` |
| `author`、`comment` | 操作者与说明（本版来自 CLI 参数或 OS 用户名） |
| `created_at`、`published_at` | 时间戳（UTC） |

- **规范化**：任何写入 store 的配置都先经 `configyaml.Decode`（解码、默认值、
  完整校验），再用 export 序列化成 payload。因此 store 里只会有通过校验的
  配置，同一语义的配置得到同一 `content_hash`。
- **append-only**：revision 内容写入后不再修改，只有 `state` 与
  `published_at` 会变化。
- **唯一 published**：任一时刻最多一个 revision 处于 `published`。

状态机：

```text
draft ──publish──▶ published ──(另一个 revision 发布)──▶ superseded
                       │
                       └──rollback──▶ rolled-back
```

- `publish <id>`：draft → published，原 published → superseded；
- `rollback`：当前 published → rolled-back，并以**目标 revision 的 payload
  新建一个 revision 直接发布**（新 `id`，`parent_id` 指向被恢复的 revision）。
  这样发布历史单调递增，轮询方只需比较最新发布 `id`。默认目标是"内容不同于
  当前发布的最近一个 superseded revision"（实现时补充：若只取最近的
  superseded，连续两次回滚会回到同一份内容）；`--to <id>` 可显式指定任一非
  draft revision；
- 发布与回滚在一个数据库事务中完成，并以"期望的当前 published id"做乐观
  并发检查，冲突时报错，不静默覆盖。

## 存储边界与表结构（结论）

纯接口放在 `core/revision`（只依赖标准库，满足 core-purity）：

```go
type Store interface {
    Create(ctx, Draft) (Revision, error)          // 写入 draft，返回 id 与 hash
    Get(ctx, id int64) (Revision, error)
    List(ctx, limit int) ([]Revision, error)      // 摘要（不含 payload），新到旧
    Published(ctx) (Revision, error)              // 无发布时返回 ErrNoPublished
    Publish(ctx, id, expectedCurrent int64, meta) (Revision, error)
    Rollback(ctx, expectedCurrent, target int64, meta) (Revision, error)
    ImportHistory(ctx, []Revision) error          // 仅空 store，供 migrate --history
    Close() error
}
```

SQL 实现放在 `x/configstore`，各方言只在占位符与列类型上不同：id 由
`smcp_store_meta` 中的计数行分配，publish/rollback 先更新锁行，借行锁在四种
后端上统一串行化（实现时补充，避免各库自增语法与迁移后序列修正）。表名统一使用 `smcp_` 前缀：

```sql
smcp_store_meta (key PRIMARY KEY, value)           -- schema_version 等
smcp_revisions (
  id            整数自增主键,
  parent_id     整数,
  content_hash  文本 NOT NULL,
  payload       大文本 NOT NULL,
  state         文本 NOT NULL,
  author        文本 NOT NULL,
  comment       文本 NOT NULL,
  created_at    时间 NOT NULL,
  published_at  时间
)
```

- **store 自身 schema 版本化**：`smcp_store_meta.schema_version` 记录表结构
  版本；打开 store 时版本未知或更新于程序 → fail closed；旧版本按内置迁移
  升级，迁移在事务内执行。
- **首次使用**：`store init` 显式建表；服务启动时不会自动建表（避免误把
  store 建进业务库）。

## Store 的声明与连接（结论）

store 的位置不能放在 revision payload 里（先有 store 才能读到配置），因此由
启动参数声明：

```text
sql-mcp-server serve --store sqlite:/var/lib/sql-mcp-server/config.db
sql-mcp-server serve --store 'postgres:${file:/run/secrets/store_dsn}'
```

- 格式为 `<driver>:<dsn>`，`driver` 取 `sqlite`、`postgres`、`mysql`、
  `oceanbase`；也可用环境变量 `SQL_MCP_STORE`；
- DSN 部分支持与数据源相同的 `${ENV}` / `${file:/path}` 占位符；`file`
  路径的允许根由 `--secret-root` 指定（store 先于配置加载，不能读取配置里的
  `server.secrets.allowedRoots`）；
- PostgreSQL / MySQL / OceanBase 复用现有 provider 已依赖的驱动，store 单独
  建立自己的小连接池，不与数据面共享连接。

## 运行时接入（结论）

### 启动

- `serve --store ...`：读取当前 published revision 的 payload，走
  `configyaml.Decode` → `Assemble`，与文件模式完全相同的链路；
- 没有 published revision 时拒绝启动（fail closed），提示先 `store import`
  并 `store publish`；
- 不允许同时指定 `--config` 和 `--store`，避免两份配置来源的歧义。

### 热重载

- `serve --store ... --watch`：按 `--watch-interval`（默认 5s）轮询 store 的
  最新 published `id`；变化后取 payload 重建 snapshot。轮询与文件 watch 共用
  "取字节 → 构建 → 校验热重载守卫 → 发布"的逻辑，为此 `Runtime` 的 builder
  从"文件路径"改为"配置字节 + 来源描述"；
- 构建或守卫失败：保留当前 snapshot（fail-static），记录错误并在
  `/readyz/snapshot` 暴露"store 有未应用的发布"状态（仍为 200，附带
  `X-Snapshot-Stale` 信息，避免编排器把正在服务的实例摘掉）；
- store 不可达：同样保留当前 snapshot 并记录；恢复后从持久化状态收敛；
- 热重载守卫（transport/addr/auth/TLS/工具集、用户功能开关）从 CLI 层下沉到
  `x/bootstrap`，文件模式与 store 模式共用一份规则（revision-snapshot 设计的
  遗留项）。

### 发布前校验

`store publish` 会对目标 revision 与当前 published revision 运行同一套热重载
守卫：

- 只包含可热应用的变化 → 直接发布；
- 包含需要重启的变化（如监听地址、TLS）→ 拒绝，除非加 `--restart-required`；
  加了之后照常发布，运行中的实例保留旧 snapshot 并标记 stale，重启后生效。

## 隔离与安全（结论）

- **store 表不可被暴露**：配置校验拒绝任何 `source` 以 `smcp_` 开头的实体；
  introspection 结果同样过滤 `smcp_` 前缀，因此后续的 schema 导入（管理面 4）
  不会把 store 表收进草稿；
- **密钥不落库**：store 模式下 payload 里的 DSN 必须是 `${...}` 占位符，
  `server.auth.token` 必须为空（改用 v0.1.11 的每用户 token，配置里只有 hash）；
  不满足时 import 失败。文件模式不受此限制；
- **审计**：每次 import、publish、rollback 都向配置审计日志（stderr 结构化
  日志）写一条记录：操作、revision id、content_hash、author；
- store 只保存 payload，不保存解析后的 DSN 与任何明文凭据。

## CLI（结论）

| 命令 | 作用 |
| --- | --- |
| `store init --store S` | 建表（已存在且版本相同时为空操作） |
| `store import --store S --config F [--comment C]` | 校验并规范化 F，写成 draft；与当前 published 的 hash 相同时提示并不创建 |
| `store list --store S` | 列出 revision 摘要 |
| `store show --store S <id>` | 输出 payload |
| `store diff --store S <a> [<b>]` | 统一 diff（缺省 b 为当前 published） |
| `store publish --store S <id> [--restart-required]` | 发布 |
| `store rollback --store S [--to id] [--restart-required]` | 回滚（默认回到内容不同的上一个发布） |
| `migrate --from X --to Y` | 在 `file:`、`sqlite:`、`postgres:` 等之间迁移 |

`migrate` 规则：

- 源为文件 → 目标 store 写入一个 draft 并直接发布；
- 源为 store → 默认只迁移当前 published revision；加 `--history` 迁移全部
  revision（保留原 `id`、状态、时间与作者）；
- 目标为文件 → 写出当前 published 的 payload；
- 每次迁移后比对 `content_hash`，不一致即失败。

## 兼容（结论）

- 不传 `--store` 时，文件模式的行为、热重载语义、export 输出均不变；
- payload 是现有 export 格式，`version` 仍为 `"1"`；
- 新增依赖 `modernc.org/sqlite`（纯 Go，不需要 cgo，跨平台发布流程不变）。

## 实现落点

- `core/revision`：`Store` 接口、`Revision`/`Draft`/`Summary` 类型、
  状态机规则与哈希函数；
- `x/configstore`：SQL 实现、方言差异、schema 迁移、`<driver>:<dsn>` 解析；
  `x/configstore/sqlite` 注册 modernc 驱动；
- `x/bootstrap`：builder 改为字节输入，热重载守卫下沉，`WatchStore`，stale 状态；
- `x/configyaml`：拒绝 `smcp_` 前缀实体（放在 `core/config` 校验中）；
  各 provider introspector 过滤 `smcp_` 表；
- `cmd/sql-mcp-server`：`--store`、`--secret-root`、`store *`、`migrate` 子命令。

## 安全影响与验收

新增 threat 条目（实现时登记到[威胁模型](../threat-model.md)）：

- 通过实体或 schema 导入读取 store 表——前缀拒绝与 introspection 过滤 + 测试；
- store 中残留明文凭据——import 校验 + 测试；
- 并发发布互相覆盖——乐观并发检查 + 测试；
- 被篡改的 payload（hash 不匹配）被加载——加载时重算 hash，不一致 fail closed。

退出门禁与 Roadmap 管理面 3 一致，具体为：

- [ ] SQLite、PostgreSQL、MySQL、OceanBase 四种 store 与 YAML 之间往返
  `content_hash` 一致，有 golden 测试与三库 integration；
- [ ] 无 published revision 时拒绝启动；store 不可达或构建失败时保留旧
  snapshot，有测试；
- [ ] publish/rollback 的乐观并发与状态机有测试；
- [ ] `smcp_` 表不可被实体引用、不出现在 introspection 结果中，有测试；
- [ ] 热重载守卫下沉后，文件模式现有测试全部通过。

## 评审结论

2026-10-03 评审通过以下决策：

1. store 位置用启动参数 `--store <driver>:<dsn>`（及环境变量
   `SQL_MCP_STORE`）声明，不引入引导配置文件；
2. store 模式强制 DSN 为占位符，禁止共享 token 写入 payload（fail closed）；
3. rollback 新建 revision 复制上一个发布的 payload，发布序列保持单调；
4. 包含需重启变化的 revision 默认拒绝发布，需显式 `--restart-required`；
5. 服务启动不自动建表，必须先 `store init`；
6. SQLite 驱动使用纯 Go 的 `modernc.org/sqlite`。
