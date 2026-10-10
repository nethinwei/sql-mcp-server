# 设计评审：管理 API、管理员登录与 Schema 导入

状态：**评审结论（2026-10-03，管理面 4，预期 v0.1.14）**。对应主
[Roadmap](../roadmap.md#管理面-4--admin-api-login-and-schema-import)
管理面 4。首轮实现本地账号登录，OIDC/OAuth2 作为同一版本的第二轮。

## 目标与非目标

目标：

- GraphQL 管理 API 一次把**管理数据模型**组织完整：配置的各组成部分、revision、
  数据源、schema 导入预览、simulate 都是结构化类型，后续迭代主要改前端；
- 写入只产生 revision draft，发布、回滚与 CLI 共用同一套校验与热重载守卫；
- 从已接入数据源 introspect 表、列、主键、外键与注释，生成**零权限**实体草稿；
- 独立的管理员账号与会话，权限细分到读、写、发布、账号管理。

非目标（本版）：后台 UI（管理面 5）；多实例共享会话；在后台执行 DDL 或编辑业务
数据；自动授权。

## 可用条件（结论）

- 只在 store 模式（`serve --store`）且 HTTP transport 下可用，并需要显式加
  `--admin`；文件模式不挂载 `/admin`；
- 管理员认证与 MCP 认证完全独立：MCP bearer token 不能访问 `/admin`，管理员
  会话也不能访问 `/mcp`。

## 管理员账号（结论）

- 存在 store 的 `smcp_admin_accounts` 表（store schema 版本 1 → 2，打开时在
  事务内迁移）：`username`、`password_hash`（argon2id，PHC 编码）、
  `permissions`、`disabled`、时间戳；
- 修改即时生效，不进入 revision；配置发布不会影响管理员权限；
- 权限：`admin:read`（查询）、`admin:write`（创建 draft、导入预览、simulate）、
  `admin:publish`（发布、回滚）、`admin:accounts`（管理管理员账号），`admin:*`
  表示全部；
- 第一个管理员由 CLI 创建：`sql-mcp-server admin create --store S --username u`
  （权限默认 `admin:*`，密码从标准输入读取）；
- 账号规则只有一份实现：`x/admin/accounts.Service`，管理 API 与 `admin`
  子命令都调用它（用户名规范化、密码强度、权限校验、argon2id 哈希）；
- 始终至少保留一个启用且有 `admin:accounts` 的账号。检查与写入在
  `configstore.MutateAdmins` 的同一事务中、由 store 锁行串行执行，两个管理员
  同时禁用对方只有一个成功；CLI 没有绕过手段，被锁在外面时用
  `admin create` 或 `admin set --enable` 恢复（它们不会减少管理员）。

## 会话与登录（结论）

- `POST /admin/login`（JSON `{username, password}`）成功后下发
  `smcp_admin` 会话 Cookie（HttpOnly、SameSite=Strict，TLS 下加 Secure），并在
  响应体返回 `csrfToken`；`POST /admin/logout` 注销；
- 所有 `/admin/graphql` 请求都必须带有效会话，并在 `X-CSRF-Token` 头中携带该
  会话的 CSRF token；
- 控制台里会话结束（退出、401、另一个标签页换了账号）时整页重新加载登录页，登录
  成功后也整页加载：上一个账号未完成的操作随页面结束，不会以下一个账号发出；退出
  前先保存本页尚未写回的编辑；
- 会话保存在进程内存，默认 8 小时空闲过期；账号被禁用或删除后，该账号的会话
  在下一次请求时失效；
- 登录失败按用户名计数，连续 5 次失败锁定 1 分钟；失败响应不区分"用户不存在"
  与"密码错误"；
- 登录做成可插拔的 `LoginProvider`，第二轮接入通用 OIDC 与通用 OAuth2。

## GraphQL 数据模型（结论）

读模型（节选，完整定义见 `x/admin/graph/schema.graphqls`）：

```graphql
type Query {
  me: AdminAccount!
  serverStatus: ServerStatus!  # 本进程运行的 revision、是否 --watch、未应用的发布及原因
  published: Revision
  revision(id: ID!): Revision
  revisions(limit: Int): [Revision!]!
  schemaList(datasource: String!, refresh: Boolean): SchemaList!   # 库（schema）列表与各库最近扫描
  schemaTables(datasource: String!, schemas: [String!]): SchemaImport!  # 已扫描库的表（省略 schemas 为全部）
  schemaScan(id: ID!): ScanJob                                          # 扫描任务状态
  validate(draft: DraftInput!): Validation!
  diff(from: ID!, to: ID, draft: DraftInput): String!
  simulate(input: SimulationInput!): Simulation!
  visibility(input: VisibilityInput!): [EntityVisibility!]!  # 一次返回全部实体 × 适用动作
  adminAccounts: [AdminAccount!]!
}

type Revision {
  id: ID!  parent: ID  state: RevisionState!  contentHash: String!
  author: String!  comment: String!  createdAt: Time!  publishedAt: Time
  config: Configuration!  yaml: String!
}

type Configuration {
  datasources: [Datasource!]!   # driver 与脱敏后的 DSN
  entities: [Entity!]!          # 字段、主键、关系、旧式角色、租户约束
  roles: [Role!]!               # 授权项
  users: [User!]!               # 角色、直授、subject；hasToken 与 tokenHash（只有 hash，明文从不保存）
  settings: JSON!               # 其余部分（server、cost、budget 等）
}
```

写模型：

```graphql
type Mutation {
  createDraft(draft: DraftInput!, comment: String): Revision!
  publish(input: PublishInput!): Revision!   # id、restartRequired、expectedPublished
  rollback(input: RollbackInput!): Revision!
  generateUserToken: UserToken!          # 返回一次性明文 token 与 tokenHash
  createAdminAccount(input: AdminAccountInput!): AdminAccount!
  updateAdminAccount(input: AdminAccountUpdate!): AdminAccount!
  setAdminPassword(input: SetAdminPasswordInput!): AdminAccount!
}

input DraftInput {
  base: ID!                 # 基于哪个 revision
  entities: [EntityInput!]  # 提供则整块替换该部分
  roles: [RoleInput!]
  users: [UserInput!]       # tokenHash 省略时沿用 base 中同名用户的 hash
  settings: JSON            # 提供则按顶层键覆盖其余部分
}
```

- 未提供的部分沿用 base；合并后的配置走与 `store import` 相同的链路（解码、
  默认值、校验、store 模式密钥规则、规范化），因此 API 与 CLI 产出的 payload
  完全一致；
- 数据源与 DSN 本版不可通过 API 修改（避免经后台写入凭据），改由 CLI；
- validate、diff、simulate 都接受未保存的 `DraftInput`，前端可在提交前预览；
- simulate 不访问数据库：用配置离线构建实体注册表与授权策略，返回是否允许、
  可见字段、行范围（以与 `rowPolicies` 相同的 JSON 形式）、覆盖授权项与
  `AMBIGUOUS_FIELD_SCOPE` 的可选字段集；
- 发布只允许“快进”：草稿必须基于当前已发布 revision（沿父链找到的第一个发布过的
  祖先，被丢弃的草稿同样跳过），否则返回 `extensions.code = CONFLICT` 与最新的 `published`，CLI、API、
  控制台一律如此——基于旧版本的草稿会整体覆盖期间别人的发布。`expectedPublished`
  另外校验调用方看到的已发布版本；检查与配置库发布之间被他人抢先同样报
  `CONFLICT`。
- `GET /admin/events`（server-sent events，需登录会话）在连接时与每次发布时推送
  已发布 revision（`event: published`，`{"id","author"}`），包括其他进程经同一
  配置库的发布；连接之后版本有变化（另一个管理员保存或丢弃了草稿；按配置库的
  变更序号判断，丢弃较早的草稿也算）时推送 `event: revisions`（`{"sequence"}`），控制台据此刷新草稿列表（`drafts`，每份带服务端
  沿父链求得的基准版本）；登录管理员的工作区被写入（另一个控制台、Agent 的
  `plan_apply`）时推送 `event: workspace`（`{"version"}`）。工作区每人一份，在服务端，
  所有版本操作都是服务端操作：`workspace`、`workspaceLog` 读取；`editWorkspace` 按项
  写入，每项带写入者读到的值，期间别处写过则三方合并（同一处改得不同成为冲突，不丢
  任何一方）；`applyPlan` 把预览过的方案写进工作区（与配置 MCP 的 `plan_apply` 同一段
  逻辑，绑定实际影响）；`updateWorkspace`、`resetWorkspace`、`mergeDraft`、
  `resolveConflict`、`checkpointWorkspace`（`admin:write`）、`commitWorkspace`
  （`admin:publish`；期间有写入为 `CONFLICT`，有未解决冲突为 `CONFLICTS`，基准落后为
  `BEHIND`，发布被抢先为带 `published` 的 `CONFLICT`）。设计与不变量见
  `docs/design/versioning.md`。`discardDraft(id)` 把草稿标为
  `DISCARDED`（`admin:write`），留在历史中且不能再发布。服务端每秒读取一次已发布
  revision、最新 revision 与该管理员工作区的版本号，只在有控制台连接时读取；10 秒
  轮询保留为兜底。
- 三方合并在服务端（`core/workspace`）：对象按键、命名列表按元素（字段与关系按名称，
  授权按实体；同一实体的多条授权作为一组整体合并）。空值与未设置的可选文本视为
  相同。只有一方改动的值自动合并；双方改了同一处且取值不同即为冲突，工作区保留
  自己的值并列出冲突，逐项“保留我的 / 采用对方”（`resolveConflict`），全部解决前
  不能提交；相对已发布的冲突在之后的发布等于我的值时自动关闭；
- `serverStatus` 报告进程实际运行的 revision 与未应用的发布（需重启或加载失败），
  控制台顶栏据此显示运行状态；
- visibility 用同一个离线授权器批量评估某个主体对全部实体、适用动作的默认投影结果，供
  角色/用户页实时预览与模拟总览使用，避免逐格发起 simulate；
- 参数较多的操作统一使用单个 input 对象（实现时调整，便于前端组装，也避免
  生成的 resolver 签名过长）；
- 空值与配置编码一致地省略：可选文本（实体的 schema/description、字段的
  alias/description/mask、角色与用户的 description）未设置时为 null，输入中的
  空字符串等同未设置；列表始终返回（可能为空），布尔值始终返回；
- diff 先把两侧 payload 按当前编码重新编码再比较，旧编码保存的 revision 只在
  内容不同处出现差异；
- 查询深度上限 10、复杂度上限 1000；`--admin-playground` 才启用 GraphiQL，生产
  默认关闭 GraphQL introspection。

## Schema 导入（结论）

- introspection 扩展：PostgreSQL 读取 `pg_description` 表/列注释与外键；
  MySQL/OceanBase 读取 `TABLE_COMMENT`/`COLUMN_COMMENT` 与
  `KEY_COLUMN_USAGE` 外键；`smcp_` 表继续跳过；
- 按库导入：`schemaList` 列出数据源的库（PG schema / MySQL database），控制台
  展开一个库时才扫描它的表。扫描是后台任务：`startSchemaScan` 返回任务 id，
  `schemaScan(id)` 轮询到完成后读取 `schemaTables`，`cancelSchemaScan` 取消；任务在有界 worker 池（2 个
  worker、排队上限 8，超出拒绝）中执行，单个任务限时 5 分钟，使用独立的单连接，
  不占用数据面连接池、不持有快照租约，因此扫描再慢也不阻塞请求与热重载；
- 每个库最近一次扫描的结果和库列表保存在配置库（`x/catalog`，schema 版本 3），
  直到该库被重新扫描（库列表由 `refresh` 刷新，并丢弃已不存在的库）；进程内按扫描
  时间缓存解码结果，重启或换副本后结果仍在。读取时与当前配置比较，状态不会过期。
  保存的内容属于数据源的读连接（驱动与 DSN）：保留名称但改连到别的数据库后，旧
  结果被丢弃；只清理读取配置之前保存的旧库结果，改连前开始、之后才完成的请求或
  扫描不会清掉新库的结果。扫描按入队时的已发布配置建连（不依赖运行时是否已热
  重载），因此结果与其标记的库一致。`SchemaList` 与 `SchemaImport` 带 `source`
  （该身份的摘要，不含 DSN），控制台据此丢弃旧库的结果。跨数据源的检索、分页与
  批量方案见 `docs/design/planning.md`；
- 候选实体的命名与关系在该数据源所有已扫描的库上一起计算：不同库的同名表各得
  其名，外键指向其他已扫描库的表或已配置实体时同样生成关系。因此控制台不按库
  分别缓存，而是在一次请求里读取库列表和全部已扫描库的表（同步），只采用最新
  一次同步的结果：最后一个扫描完成后开始的同步必然看到所有扫描。扫描、同步进行
  中或同步失败（可重试）时不能导入；
- 扫描结果返回每张表的候选实体与状态：`new`（未配置）、`configured`
  （同数据源同表已配置；列出新增与缺失的列）；
- 自省按库批量读取（参考 mysqldump）：表、列、索引、外键、级联、触发器各一次
  查询，往返次数与表数量无关；
- 索引按数据库自己的术语返回：结构（PostgreSQL 的 btree/hash/gin/gist/brin/
  spgist，MySQL/OceanBase 的 btree/hash/fulltext/spatial）、键部分（列、前缀如
  `title(20)`、表达式原文）与部分索引条件；唯一索引同时是键，并标出不能唯一
  定位行的原因。控制台在列表下单独列出索引，列上只标主键与外键；
- 候选实体：名称取表名（只在同一数据源与库内已被占用时加序号；其他命名空间的
  同名实体靠引用区分）、关系目标为规范 ID（控制台加入工作区时改写为最短无歧义
  引用，并限定因此产生歧义的已有引用）、注释映射为 description、
  主键、全部列；单列外键生成候选关系（子表 `belongs-to` 父表，父表 `has-many`
  子表）；**不含任何角色、授权或行策略**，即导入后对任何主体都不可访问；
- 导入本身不写入 store：前端把选中的候选实体放进 `DraftInput.entities` 后
  `createDraft`，授权必须另外显式配置。

## 实现落点

- `x/configyaml`：新增 `Encode`（确定性 export），CLI 与 API 共用；
- `x/configstore`：schema 版本 2、管理员账号表与迁移；版本 3 增加扫描目录、方案
  模板与配置 MCP 令牌，版本 4 增加令牌范围与扫描批次的逐库记录，版本 5 增加管理员工作区（见
  `docs/design/planning.md`）；
- `x/providers/{postgres,mysql}`：注释与外键 introspection；
- `x/admin`：会话、登录、权限、HTTP 挂载；`x/admin/graph`：gqlgen schema 与
  resolver；草稿合并、schema 导入映射、离线 simulate；
- `x/mcpserver`：`HTTPConfig.Admin` 挂载到 `/admin/`；
- `x/admin/accounts`：账号业务规则（API 与 CLI 共用）；
- `x/configedit`：草稿合并（把 DTO 映射来的类型化分区写入 base 配置，统一做默认值、
  校验、store 规则与编码，并处理 tokenHash 保留）；读模型如实返回配置值（如未写
  `source`/`datasource`/`kind` 时为 null），往返测试保证读出后原样保存不改变配置；
- `x/revisionops`：revision 操作（payload 规范化、草稿、发布及乐观并发检查、
  回滚、diff 与审计），`store`/`migrate` CLI 与管理 API 共用，入口只处理参数、
  身份与输出；
- `cmd/sql-mcp-server`：`--admin`、`--admin-playground`，`admin create/passwd/
  set/list` 子命令。

## 安全影响与验收

新增 threat（实现时登记）：管理面越权（每个 resolver 的权限测试）、会话劫持与
CSRF（Cookie 属性、CSRF 头、跨站请求测试）、登录爆破（锁定测试）、经 API 写入
明文凭据（数据源不可写 + 密钥规则测试）。

退出门禁与 Roadmap 管理面 4 一致；另需 schema 导入在三库 integration 上验证
注释、外键与零权限草稿。
