# 设计评审：编辑语义统一到服务端（工作区项归属与派生一致性）

状态：**评审通过（2026-10-10）**，按“交付步骤”实施。相关设计：[版本管理](versioning.md)、[批量配置](planning.md)。

## 背景与问题

版本管理（versioning.md）把合并、冲突、提交都移到了服务端，但“编辑”本身仍然有两套实现，
近期管理面的反复修改主要集中在这里（`web/admin/src/stores/workspace.ts`、
`x/admin/graph/convert.go`、`web/admin/src/api/ops.ts` 都在改动最频繁的文件之列）：

1. **工作区项的格式由 API 定义**：项的值是控制台编辑的 GraphQL 输入类型。`configapp` 通过
   函数字段 `Service.WorkspaceEdit` / `Service.WorkspaceSections` 回调 `x/admin/graph`
   完成编解码（`x/admin/configapp/service.go:72`），领域层反向依赖适配层。“配置 → 分区”的
   映射在 Go（`graph.WorkspaceSections`）与 TS（`revisionSections`）各写一份，靠
   `x/admin/graph/testdata/workspace_sections.json` 保持一致。
2. **级联与引用规则写了两遍**：实体改名或移动、删除实体、删除字段、导入实体时的引用改写、
   限定与清理在 TS（`workspace.ts` 的 `retarget`/`rewriteReferences`/`syncFields`/
   `importEntities`），方案应用走 Go（`x/configplan/refs.go` 的 `dropGrantField`、
   `qualifyReferences`）。控制台与 Agent 做同一件事，可能得到不同结果；TS 版角色改名
   还漏掉了实体旧写法（`legacyAccess.roles`）中的引用。
3. **“当前编辑态”有两条路径**：服务端工作区之外，控制台把整份本地副本作为 `DraftInput`
   传给 `validate`、`diff`、`simulate`、`visibility`、`catalog*`、`planScope`、
   `previewPlan`、`pending`，并保留 `createDraft(DraftInput)`。同一个“我正在编辑的
   配置”在服务端有两种来源（`configapp.Draft{Base, Edit}` 与 `workspaceDraft`）。

## 目标与非目标

目标：

- 编辑规则只有一份，在服务端；控制台与配置 MCP 写同一种项、得到同一种结果；
- 工作区项的格式归领域层所有，GraphQL 只做映射；
- 查询类接口只有一个“编辑态”来源：调用者的工作区（或指定 revision）；
- 已保存的工作区、草稿与 revision 不需要迁移。

非目标：

- 不改变合并、冲突、提交语义（versioning.md 的不变量全部保持）；
- 不在本阶段拆分 Spec/Effective、不改按属性名的合并 Schema（阶段 2）；
- 不做命令式 API（见“取舍”）。

## 结论

### 1. 工作区项归领域层，格式不变

- 在 `x/admin/configapp` 新增项编解码（`items.go`）：配置 ⇄ 项、项 ⇄ 配置编辑，类型为
  领域自有的 `EntityItem`/`RoleItem`/`UserItem` 与 settings 映射。**JSON 形状与现在存储
  的完全相同**（即现有 `workspace_sections.json` 锁定的形状），已有工作区无需迁移。
- 删除 `Service.WorkspaceEdit`/`WorkspaceSections` 两个注入函数；`graph.WorkspaceSections`
  与 `graph.WorkspaceEdit` 改为调用 configapp，`DraftInput`、`EntityInput` 等 GraphQL
  类型仅做 DTO ⇄ 领域项映射。
- 控制台不再从 revision 自己构造分区：新增查询 `revisionSections(id: ID!): JSON!`
  （服务端用同一编解码生成），删除 TS 的 `revisionSections`；
  `workspace_sections.json` 只锁 Go 一侧的输出。

### 2. 派生一致性在服务端：`Reconcile`

写入仍按项进行（`editWorkspace`，带 `previous` 的条件写与三方合并不变），新增两点：

- `WorkspaceItemInput.renamedFrom: String`：实体（ID）或角色（名称）改名、移动时写出
  旧键。没有它，“删 A 加 B”与“A 改名为 B”无法区分。同一次写入必须同时写出旧项的删除
  （`value: null`，`previous` 为读到的旧值），旧项已不存在时除外（重试）：旧项在期间被
  别人修改时，靠这个 `previous` 合并成冲突而不是静默丢失；只发改名会被拒绝
  （`workspaceops.ErrRenameAlone`）。
- `workspaceops.Codec` 增加扩展点
  `Reconcile(before, after workspace.Snapshot, renames map[Ref]Ref) (workspace.Snapshot, error)`，
  在 `WriteItems` 的乐观循环里、合并之后、按版本写入之前执行。因此派生改动与触发它的
  写入原子地落在同一个工作区版本里，并发写入时一起参与合并。`workspaceops` 仍不认识
  配置类型（depguard `versioning-generic` 不变），配置的规则由 configapp 实现。

configapp 的 `Reconcile` 规则（与现在控制台的行为一致，并补上缺口）：

| 变化（由 before/after 与 renames 得出） | 派生改动 |
|---|---|
| 实体改名或移动（renamedFrom） | 指向它的引用改为新 ID：角色与用户授权的 `entity`、其他实体关系的 `target`、过程的 `affects`；改名而未设 `source` 时，`source` 取旧名（仍读原表） |
| 删除实体 | 删除指向它的授权、关系与 `affects` 项（`affects` 变空即整库失效，是安全默认） |
| 新增实体使已有短引用变得有歧义 | 已有引用改写为最短无歧义形式（含旧写法中的引用） |
| 删除实体字段 | 从指向该实体的授权 `readFields`/`writeFields` 中移除；关系、主键、唯一键、行范围、租户策略中的引用**不自动改**，见下 |
| 角色改名（renamedFrom） | 用户的 `roles` 与实体旧写法 `legacyAccess.roles` 中的名称随之改 |
| 删除角色 | 从用户的 `roles` 与实体旧写法中移除 |

不变量：**每个引用变更前指向哪个实体，变更后仍指向它（实体被删除则引用随之删除），
并以最短无歧义形式写出**；变更前就不指向唯一实体的引用保持原样。

不自动修改的字段引用（关系、键、行范围、租户策略）由新增查询
`fieldReferences(entity: String!, field: String!): [FieldReference!]!` 给出（取代控制台的
`fieldReferenceIndex`），控制台在删除字段前提示；提交时整体校验仍会拒绝悬空引用。

实现（实施时调整）：**规则只有一份，遍历按表示各一份**。configplan 在完整、已校验的
`config.Config` 上计算方案，而 `Reconcile` 面对的工作区项可以暂时不合法；把项转成配置再转
回会丢掉“未设置”与“设为 false”的区别（例如实体未写 `dmlTools`，转回后成了显式的
`false`），因此不共用遍历：

- `x/configrefs`：引用解析（`Index`）、最短无歧义形式（`ShortName`，与控制台共用
  `core/config/testdata/entity_refs.json`）、引用改写规则（`Retarget`）、行过滤引用的字段
  （`FilterFields`）；
- configplan 的 `qualifyReferences` 与 configapp 的 `reconcile`（直接改项的 JSON，只动
  引用所在的值）都调用 `Retarget`；`fieldUses` 与 `fieldReferences` 都调用 `FilterFields`。

统一后修正的两处原有错误：

- configplan 原先只在引用变得有歧义时改写，且写成完整 ID；导入的实体 ID 恰好等于某个
  已有短引用时（`archive.orders` 原指 `main.archive.orders`），引用不歧义，会被悄悄
  改指向新实体。现在按 `Retarget` 保持原指向，并与控制台一样写成最短形式；
- configplan 的 `filterKeys` 取的是行过滤的键名（`op`、`field`……），不是它引用的字段，
  删除被行过滤或租户策略引用的字段时不会报告；现在用 `FilterFields`。

另外，同一次写入中一个实体被删除、另一个实体改名占用了它的 ID 时，指向被删实体的引用
随之删除，不会转到占用者（`Retarget` 的 `moved` 以空值表示删除；性质测试发现）。

方案应用（`applyPlan`/`plan_apply`）写入的项同样经过 `Reconcile`；方案预览中的限定报告
仍由 configplan 给出，两者用同一规则，结果一致。

### 3. 查询只读工作区或 revision

- `draft: DraftInput` 参数改为 `revision: ID`（可选）：省略时用调用者的工作区（即现有的
  `workspaceDraft`；未改过的工作区等于已发布配置）。涉及：`validate`、`diff`、
  `simulate`、`visibility`、`catalog`、`catalogRefs`、`catalogTable`、`catalogLinks`、
  `planScope`、`previewPlan`、`pending`。
- 控制台在发出这些查询前先写出未保存的修改（复用 `workspaceSync` 的 `flush`；各操作
  已经这样做），查询结果因此反映刚做的编辑。
- 删除 `createDraft(DraftInput)`（由 `checkpointWorkspace` 取代），删除
  `configapp.Draft{Base, Edit}`、`toDraft`、`toEdit`；`DraftInput` 只在
  `x/configedit` 的内部编辑结构中保留（阶段 2 再随 Spec 收敛）。

### 4. 配置 MCP

评审结论：本阶段**不开放**配置 MCP 的工作区写工具，Agent 仍只通过 `plan_apply` 写入；
`plan_apply` 写入的项同样经过 `Reconcile`，因此与控制台的编辑语义一致。

## 取舍

- **按项写入 + 服务端派生，而不是命令式 API**（`RenameEntity`、`RemoveFields`……）：
  按项写入已有条件写、三方合并、审计记录与控制台的去抖写回；命令式 API 要为每个操作
  设计输入、冲突与重放语义，接口面成倍增加。派生一致性是“写入前后两个状态”的纯函数，
  只需 `renamedFrom` 这一处额外信息。
- **格式不变**：只移动归属，不改存储形状，避免工作区与草稿迁移；形状的清理放到
  阶段 2（Spec）一并做。
- **字段引用只提示不自动改**：关系、键、行范围的改写需要人判断语义，沿用现有行为。

## 交付步骤（每步可独立合并）

1. **项编解码归位**（无行为变化）：新增 `configapp/items.go`，删除注入函数，graph 改为
   映射；新增 `revisionSections` 查询，控制台改用它并删除 TS `revisionSections`。
2. **引用规则抽到 `x/configrefs`**（无行为变化）：configplan 改用它；补齐与 TS 行为
   等价的表驱动测试（以 TS 现有用例为准逐条迁移）。
3. **`Reconcile` 与 `renamedFrom`**：workspaceops 扩展点、configapp 实现、
   `fieldReferences` 查询；控制台删除 `retarget`/`rewriteReferences`/`syncFields` 中的
   级联与 `fieldReferenceIndex`，改为写项（带 `renamedFrom`）并读回服务端结果。
4. **去掉 `DraftInput` 并行路径**：查询参数改为 `revision`，删除 `createDraft` 与
   `configapp.Draft`；控制台改为先 flush 再查询。

## 测试

- `x/configrefs`：表驱动用例覆盖上表每一行；性质测试：随机实体集合与随机改名/删除，
  检查“引用语义不变”不变量（与 `core/workspace` 的合并性质测试同样的种子框架）。
- `x/workspaceops/interleave_test.go`：随机操作加入带 `renamedFrom` 的写入，检查派生
  改动与触发写入在同一版本、并发写入下不丢失。
- 等价性：同一组编辑分别经 GraphQL（控制台路径）与配置 MCP 执行，工作区项字节级一致。
- 控制台：`workspace.test.ts` 中级联相关用例改为断言“发出的项（含 `renamedFrom`）”，
  级联结果由服务端测试负责；`make web`、`make test-e2e`。
- 手动：控制台改名实体、删除字段、导入同名实体、改名角色，检查授权与引用；再用配置
  MCP 做同样的事对比结果。

## 风险

- 控制台改名后的乐观显示：写回前界面上的引用仍是旧值，写回结果到达后才更新（300ms
  去抖 + 往返）。可接受；若体验不足，控制台可只为显示做乐观改写，不写出这些派生项。
- `Reconcile` 失败（例如编解码错误）会使整次写入失败，需返回可读错误而不是部分写入。
