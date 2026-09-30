# 设计评审：用户、角色与权限模型

状态：**评审结论（2026-09-30，v0.1.11 管理面 1）**。本文固定用户、角色与权限的
配置结构、合并语义、身份链路和兼容规则，实现以本文为准。对应主
[Roadmap](../roadmap.md#管理面-1--users-roles-and-permissions) 管理面 1。

## 背景与问题

现状（代码事实）：

- 身份只有两种来源：共享 bearer token（`server.auth.token`，不区分调用方）加
  进程级 `server.role`；或可信代理注入的 `X-MCP-Role` / `X-MCP-Subject`
  （`x/mcpserver/mcpserver.go` 的 `withRequestSubject`）。服务自身没有"用户"。
- 授权按实体配置：`entities[].roles.<action>`、`fieldACL.<role>`、
  `rowPolicies.<role>`，编译为 `entity.RoleAccess` / `FieldAccess` /
  `RowPolicies`，由 `core/rbac.RoleAuthorizer` 按**单个**角色名查找。
- `tool.Context.Role` 是缓存 key（`scopeKey`）、engine singleflight key、事务
  作用域、预算作用域（`budget.Scope.Role`）和审计 `role` 字段的共同来源。
- mask 是字段级全局配置（`fields[].mask`），与角色无关。

问题：无法区分"谁"在调用；一个人不能同时拥有多个角色；不能给单个人单独授权；
可信操作者只能绕过服务直连数据库。

## 目标与非目标

目标：

- 引入**用户**、**角色**、**权限**三个一等概念：角色的权限由管理员配置；用户
  可拥有多个角色，有效权限取并集；可给单个用户直接授权；
- 多角色合并**不产生任何单一授权都未允许的访问**（见"合并语义"）；
- 租户隔离是硬边界，不因多角色合并而放宽；
- 未配置 `users` 时，行为与现状逐字节等价。

非目标（本版）：

- 显式 deny 规则；权限通配（如 `entity: "*"`）——批量授权由管理后台完成；
- 按角色解除 mask（mask 保持字段级全局）；
- 人员登录（OIDC/OAuth2/本地密码，随管理面 4）；MCP 客户端 OAuth 2.1
  与 delegation chain（L7）；
- 系统权限的实际效果（`sql:execute`、`admin:*` 分别随管理面 2、4 生效）；
- create 时校验写入值满足租户约束（现有 row policy 也不作用于 create，
  保持现状，另行立项）。

## 术语

- **用户（user）**：一个可认证的调用方，拥有独立凭据、角色列表、直授权限和
  subject 属性。
- **角色（role）**：一组权限的命名集合。
- **授权项（grant）**：一条实体权限——实体、动作集合、可读/可写字段、行范围。
  角色和用户直授权限都由若干 grant 组成。
- **主体（principal）**：一次请求的授权主体。已配置用户时为 `user:<name>`；
  兼容路径下为角色名本身。
- **租户约束（tenantPolicy）**：实体级谓词，对所有主体始终 AND。

## 配置结构（结论）

```yaml
roles:
  analyst:
    description: 业务分析
    grants:
      - entity: orders
        actions: [read, aggregate]
        fields:
          read: [id, amount, region, created_at]   # 省略 = 全部可见字段
        rows: {op: eq, field: region, value: CN}   # 省略 = 不限制行
      - entity: customers
        actions: [read]
  sg_viewer:
    grants:
      - entity: orders
        actions: [read]
        rows: {op: eq, field: region, value: SG}

users:
  alice:
    tokenHash: "sha256:9f2c…"          # 只存 hash；明文 token 不进入配置
    roles: [analyst, sg_viewer]
    subject: {tenant_id: t1}           # 固定属性，供 ${subject.x} 解析
    grants:                            # 直授权限，语法同角色 grants
      - entity: refunds
        actions: [read]
    disabled: false

entities:
  - name: orders
    tenantPolicy: {op: eq, field: tenant_id, value: "${subject.tenant_id}"}
    # 旧式 roles / fieldACL / rowPolicies 继续有效（见"兼容"）
```

- `grants[].actions` 取值与现有实体动作相同：`read`、`create`、`update`、
  `delete`、`execute`、`aggregate`；
- `fields.read` / `fields.write` 语义与现有 `fieldACL` 相同（按 name 或 alias
  匹配，只能引用可见字段）；`rows` 语法与现有 `rowPolicies` 相同；
- `tenantPolicy` 语法与 `rows` 相同；缺少对应 subject 属性时解析为 nil，
  匹配零行（与现有 `resolveSubject` 的 fail-closed 行为一致）；
- 系统权限预留独立字段 `permissions: ["sql:execute@<datasource>", …]`；
  **本版出现任何系统权限都是校验错误**，各自随生效版本开放，避免"配置了但
  不生效"。

校验（启动与 reload 共用，失败 fail closed）：

- 角色名、用户名只允许 `[a-z0-9_-]`，规范化为小写；因此不可能与 `user:` 前缀
  主体键冲突；
- 用户引用的角色必须存在（在顶层 `roles` 中定义，或被任一实体的旧式配置
  引用）；grant 引用的实体、字段必须存在且可见；
- `tokenHash` 必须为 `sha256:<64 hex>` 且在用户间唯一；与
  `server.auth.token` 的 hash 相同也视为冲突。

## 合并语义（结论）

### 为什么不能把字段和行分别取并集

角色 A：可读 `{id, amount}`，行 `region = CN`；角色 B：可读 `{id, phone}`，
行 `region = SG`。若字段、行各自取并集，用户可以读到 **CN 客户的 phone**——
A、B 都没有授予这个组合。分别取并集会产生越权，因此不采用。

### 覆盖规则

对一次请求（实体 E、动作 A、请求使用的读字段 R——投影、filter、group-by、
aggregate、cursor——以及写字段 W）：

1. 候选集 `G` = 该主体在 E 上、`actions` 包含 A 的全部 grant（来自各角色、
   用户直授和旧式实体配置）。`G` 为空 → `UNAUTHORIZED`；
2. 覆盖集 `C` = `G` 中同时满足 `R ⊆ readable(g)`、`W ⊆ writable(g)` 的
   grant。`C` 为空时，若 R/W 超出所有 grant 的字段并集，按现状返回字段不可读/
   不可写；否则返回新拒绝码 `AMBIGUOUS_FIELD_SCOPE`；
3. 行范围 = `用户谓词 AND (OR_{g∈C} rows(g)) AND tenantPolicy(E)`；`C` 中
   任一 grant 未设置 `rows` 时，中间项为 TRUE；
4. 返回字段只取实际请求的字段（它们已被 `C` 中每个 grant 覆盖）。

请求未指定字段（默认投影）时，先取 `P = ∪_{g∈G} readable(g)`，再按上面的
规则计算 `C`：

- `C` 非空时，照常返回；
- `C` 为空时，返回 `AMBIGUOUS_FIELD_SCOPE`，hint 列出每个可选的字段集合，
  由 Agent 显式指定字段后重试。这些字段集合对该主体本来就可读，不构成侧信道。

这条规则保证：**每一行、每一列都至少被一个 grant 同时覆盖**，合并永远不会
超出单个 grant 的授权。常见配置（角色之间只在行上不同、只在字段上不同，或
一个完全包含另一个）不会触发 `AMBIGUOUS_FIELD_SCOPE`。配置校验会对"同一
实体同一动作上字段和行都不同"的组合给出 lint 提示，管理后台据此提前展示。

逐行逐列的精确并集（按行 `CASE WHEN` 置空列）语义最完整，但会改动 codegen，
filter 语义也会变复杂，因此不在本版范围。

### 租户硬边界

- `tenantPolicy` 对所有主体（包括兼容路径下的角色）始终 AND，不参与 grant
  之间的 OR；
- 角色 `rows` 中引用 `${subject.tenant*}` 的写法仍然有效，但该用户有多个
  候选 grant 时，lint 会提示改用 `tenantPolicy`；
- 用户配置的 `subject` 属性优先于可信代理 `X-MCP-Subject` 中的同名属性，
  代理不能改写已配置的租户。

### 其他维度

- **mask**：保持字段级全局，不随角色变化；
- **relation expand**：对目标实体独立执行同一覆盖规则；
- **预算**：新增 `budget.users.<user>`，显式配置时优先；未配置时，按用户
  各角色的 `budget.roles` 逐维取最宽松值（0 表示不限，优先级最高）；
  `budget.tenants` 覆盖规则不变。需要更严格限制时显式配置 `budget.users`。

## 身份链路（结论）

### 认证

- 已配置 `users` 时，HTTP 请求的 bearer token 先按 SHA-256 在当前 snapshot
  的用户表中查找（常数时间比较）：命中且未 `disabled` → 主体 `user:<name>`；
  等于 `server.auth.token` → 兼容路径（`server.role`）；都不命中 → 401；
- 用户表跟随 snapshot 热重载，新增、吊销、轮换无需重启；`server.auth` 本身的
  变化仍需重启（现有守卫不变）；
- 可信代理模式新增 `X-MCP-User`：必须指向已配置且未禁用的用户，否则 403；
  同时带 `X-MCP-User` 与 `X-MCP-Role` 时拒绝；只带 `X-MCP-Role` 保持兼容行为；
- stdio：新增 `server.user`（CLI `--user`），以指定用户身份运行；未设置时
  使用 `server.role`（现状）；
- CLI 新增 `sql-mcp-server user token`：生成高熵随机 token，只打印一次明文
  以及对应的 `tokenHash`。

### 会话与吊销

- streamable HTTP 会话绑定主体键（现有 `bindSessionIdentity` 的 role 字段改为
  主体键）；后续请求的主体不一致 → 403（沿用 TM-004 控制）；
- 每个请求都从当前 snapshot 重新解析用户：用户被删除或禁用后，下一次请求即被
  拒绝；reload 发布时对这些用户已绑定的会话调用 `RollbackSession`，回滚其
  在途事务；
- 角色或权限变化对下一次请求立即生效；缓存随 snapshot 重建，不会复用旧授权的
  结果。

### 作用域键

`tool.Context.Role` 在本版保持字段名不变，语义改为"主体键"：用户为
`user:<name>`，兼容路径为角色名。因此缓存、singleflight、事务和预算自动按
用户隔离，无需逐处修改。字段更名为 `Principal` 属于纯重构，另行处理。

## 实现落点

- `core/config`：`Roles`、`Users` 顶层结构，`EntityConfig.TenantPolicy`，
  `BudgetConfig.Users`；校验与 JSON Schema 同步；
- `core/rbac`：新增 `GrantAuthorizer`，内部保存主体键 → 实体 → `[]Grant`
  的编译表，实现覆盖规则；旧式实体配置编译为以角色名为主体键的 grant，因此
  兼容路径与用户路径共用同一个实现，`RoleAuthorizer` 退役；
  `Decision` 增加 `Grants []string`（覆盖集的 grant 标识），供 decision trace
  使用；
- `x/bootstrap`：snapshot 构建时预编译每个用户的有效 grant 与有效预算；
  预算以主体键注入 `MemoryManager`；reload 时处理被删除或禁用用户的会话；
- `x/mcpserver`：用户 token 认证中间件、`X-MCP-User`、会话绑定主体键；
- `core/audit`：事件新增可选字段 `user`、`roles`、`grants`（兼容变化，
  golden 更新）；兼容路径下 `role` 字段含义不变；
- `core/tool`：新增拒绝码 `AMBIGUOUS_FIELD_SCOPE`（兼容变化，contract golden
  更新）；
- `cmd/sql-mcp-server`：`user token` 子命令、`--user` 参数。

## 兼容（结论）

- 未配置 `users` 与 `roles` 时，授权结果、拒绝码、审计字段、缓存 key 与现状
  一致；以现有 rbac/tool 测试全量回归，并增加新旧实现的差分测试；
- 旧式实体内 `roles`/`fieldACL`/`rowPolicies` 与顶层 `roles` 可以并存，同名
  角色的 grant 合并；
- 确定性 `export` 输出新字段，并保持字节级稳定；
- 配置 `version` 仍为 `"1"`：全部是新增的可选字段，属于兼容变化。

## 安全影响与验收

新增 threat 条目（编号在实现时登记到[威胁模型](../threat-model.md)）：

- 多角色合并越权（字段 × 行组合）——覆盖规则 + adversarial 测试；
- 多角色合并打穿租户——`tenantPolicy` 硬边界 + 测试；
- 身份提升：伪造 `X-MCP-User`、请求参数注入、同时带 User 与 Role 头——
  测试；
- 吊销后的会话复用和事务残留——reload 测试。

不变量更新（实现时同步 [invariants.md](../invariants.md)）：

- I5–I7：授权主体从"角色"改为"主体"；I7 的有效谓词改为
  `用户谓词 AND (OR 覆盖集行范围) AND tenantPolicy`；
- 新增：每个返回的行和列都至少被一个 grant 同时覆盖；
- 新增：`tenantPolicy` 对所有主体始终 AND。

退出门禁与 Roadmap 管理面 1 一致；另加一条：新旧授权实现在现有全部测试
fixture 上差分一致。

## 评审结论

2026-09-30 评审通过以下决策：

1. 多角色合并采用按请求选择覆盖集的规则，不对字段和行分别取并集；
2. 默认投影没有单一覆盖时返回 `AMBIGUOUS_FIELD_SCOPE`，不自动缩小字段；
3. 用户未显式配置预算时，逐维取所属角色中最宽松的值；
4. 本版对任何系统权限都报校验错误，各自随生效版本开放；
5. `tool.Context.Role` 本版只改语义、不改字段名。
