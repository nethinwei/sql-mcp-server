# 设计评审：配置版本管理（修订历史 + 工作区）

## 背景

配置的版本管理原先分散在四处：`core/revision`（历史）、`x/revisionops`（草稿与发布）、
`x/admin/configapp` 与 `x/configstore`（工作区读写）、控制台的 `stores/workspace.ts`
与 `lib/merge.ts`（三方合并、rebase、审核草稿、冲突、提交串联）。同一个概念（合并、
冲突、提交）在 Go 和 TypeScript 各有一半，浏览器要协调多步写入，控制台与配置 MCP
的行为也难以保证一致。

现在版本管理是仓库内一个边界清晰的模块，所有版本操作都在服务端；控制台只做编辑器，
配置 MCP 与控制台调用同一组操作。模型与 SVN 类似：不可变的历史，每人一份工作副本，
update 是 rebase，commit 是服务端的原子操作，草稿就是可以分享的检查点。

## 模块与依赖（结论）

| 包 | 职责 |
|---|---|
| `core/revision` | 不可变历史、发布指针（已有） |
| `x/revisionops` | 规范化、草稿、发布快进与 `ExpectedPublished`、回滚、差异（已有） |
| `core/workspace` | 纯逻辑，只依赖标准库：项（`Ref{Kind, Key}` + JSON 值）、`Schema`、`Merge`/`Diff`、冲突的 `View`/`Replay`/`Carry`/`Resolve`、`Store` 接口、`MemoryStore` 与一致性套件 `workspacetest` |
| `x/workspaceops` | 版本操作服务：`Read`、`WriteItems`、`Update`、`Reset`、`MergeDraft`、`Resolve`、`Checkpoint`、`Commit`、`Log` |
| `x/configstore` | 实现 `workspace.Store`（`smcp_workspaces` + `smcp_workspace_log`） |
| `x/admin/configapp` | 只剩配置相关：`codec`（配置 ⇄ 项，`Codec.Items`/`Build`）、合并规则 `Schema`、方案 → 写入项（影响绑定）、鉴权 |
| `x/admin/graph`、`x/admin/configmcp` | 适配器，调用同一组操作 |
| `web/admin` | 编辑器：本地编辑、按项写回、显示服务端算好的冲突与说明 |

`x/workspaceops` 只认识项和修订，不认识配置类型：配置通过 `Codec`（修订 ⇄ 项、构建
并整体校验）和 `History`（建草稿、发布、丢弃，由 `revisionops.Service` 实现）注入。
depguard 规则 `versioning-generic` 禁止它导入 `core/config`、`x/configedit`、
`x/configplan`、`x/admin/...`。

## 不变量（结论）

1. **历史不可变**：提交、回滚都生成新修订；发布只能快进（`revisionops`）。
2. **工作区每人一份**，保存在服务端，可以暂时不合法；管理员（任何一个控制台）和他的
   Agent（他的令牌）共同写这一份。没写过时就是基准修订。
3. **写入有条件、不丢数据**：每个写入的项带上写入者读到的值（`previous`）。当前值与
   之不同（期间别处写过）时，以“读到的、写入的、当前的”三方合并：不同位置的修改都
   保留，同一位置改得不同则保留写入的值，记为冲突（来源 `workspace`，对方是上一位
   写入者）。
4. **一种合并**：同一工作区的并发写、update 到新修订、合并别人的草稿，用的都是
   `core/workspace` 里同一个 `Merge`。
5. **冲突阻止提交，不阻止编辑**；解决冲突是服务端操作，按冲突 ID。
6. **提交是一个服务端操作**，绑定“工作区版本 + 预期的已发布修订”：版本不符
   （`CONFLICT`，期间有写入）、有未解决冲突（`CONFLICTS`）、基准落后于预期
   （`BEHIND`）、已发布的已不是预期的（带 `published` 的 `CONFLICT`，不建草稿）都拒绝；随后构建并整体校验，建草稿并按 `ExpectedPublished` 发布（失败则
   丢弃刚建的草稿），工作区移到新修订，重放提交期间新写入的项。
7. **方案应用绑定实际影响**：`plan_apply`/`applyPlan` 重新计算出的改动（逐字段：
   种类、键、路径、规范化的值）与预览一致才写入；同一项不同字段的多个方案可以叠加。
8. **每次写入留记录**（`smcp_workspace_log`）：工作区版本、时间、操作者或
   `agent:<令牌名>`、操作、改动的项、说明。只做审计，不做撤销。

## 合并规则

`Merge(base, mine, theirs)` 逐项进行，项内按结构递归：

| 值 | 规则 |
|---|---|
| 对象 | 按键逐个合并；一侧删除、另一侧修改为冲突 |
| 列表，元素有 `name` 或 `entity`（`Schema.ListKeys`） | 按键识别元素：字段、关系按 `name`，授权按 `entity`；新增、删除、修改各自合并，顺序按我方，再追加只有对方新增的；同一键出现多次的元素成组整体比较 |
| 其他列表、标量 | 整体比较：只有一侧改了取那一侧，两侧改得一样取之，改得不同为冲突 |
| `Schema.OptionalText` 中的文本 | 空串等同未设置 |
| 用户的 `tokenHash` | 项里是用户的完整状态：哈希就是令牌，生成、轮换、撤销都是这个值的修改；空串（撤销）等同没有令牌（在 `OptionalText` 中），所以两边都撤销不算冲突，改成不同的值才是冲突。项写回配置时，没有哈希即没有令牌 |

冲突保留“我的”值（工作区的），记录路径（如 `fields[email].mask`，空路径为整项）
与对方的值。路径 `within` 关系用于：整项冲突覆盖其下的值冲突，解决上层冲突同时关闭
其下的。

冲突分两种：

- **相对已发布的**（来源 `published`，不固定对方的值）：对方的值在查看时从基准修订
  读取，所以之后发布的修订若恰好等于我的值，冲突自动关闭；
- **相对草稿或并发写入的**（来源 `draft`/`workspace`，固定对方的值）：对方的值随冲突
  保存。

## 操作

| 操作 | GraphQL / MCP | 语义 |
|---|---|---|
| 读 | `workspace`、`workspaceLog` | 工作区（各项按控制台形状的分区返回）、冲突、说明、已合并的草稿、检查点；写入记录 |
| 写 | `editWorkspace`；`applyPlan`、`plan_apply` | `WriteItems`，见不变量 3；Agent 写入附说明（令牌名与 `comment`） |
| update | `updateWorkspace(to)` | 移到较新的修订，重放本地修改，改得不同的成为相对已发布的冲突 |
| reset | `resetWorkspace(to)` | 变成修订 `to` 原样，丢弃修改、冲突与说明，并清除已合并草稿与检查点的记录（这些草稿仍是草稿，重新出现在待审列表，可以再次合并） |
| 合并草稿 | `mergeDraft(id)` | 草稿相对其基准的修改合并进工作区（先移到最新发布）；与发布冲突保留草稿的，与工作区冲突保留工作区的；重复合并无效果 |
| 解决冲突 | `resolveConflict(id, theirs)` | `theirs` 取对方的值，否则保留我的 |
| 检查点 | `checkpointWorkspace(comment)` | 把工作区存为草稿供他人审核，取代之前存的那份；草稿内容是开始保存时读到的工作区，保存期间或之后有写入（或未写过的工作区跟随了新发布）即不再是“与工作区一致”的草稿 |
| 提交 | `commitWorkspace(input)` | 见不变量 6；提交后丢弃快照里的检查点和已合并进来的草稿（内容已随发布），清除快照里已有的说明；提交期间新合并或保存的草稿仍记在工作区 |

所有操作在服务端以“读—计算—按版本写”的乐观循环执行（最多重试 5 次），存储层写入时
检查版本（`workspace.ErrChanged`），同一版本的并发写只有一个成功。

## 控制台

控制台保存服务端副本（各项的规范化值与原值），本地编辑相对副本计算待写项；
`editWorkspace` 300ms 去抖写回，失败按退避重试，状态显示为已同步、待保存、保存中、
失败。读回时，本地尚未写出的项保留，其余取服务端的；写入在途时又改的项继续以已发送
的值为原值写下一次。其他操作（update、reset、合并草稿、解决冲突、检查点、提交）先写出
本地修改，再调用服务端，读回结果。控制台不做合并，不算冲突。

读回是增量的：每次请求带上控制台持有的版本（`Workspace.items(since)`），服务端按写入
记录（`smcp_workspace_log`）只返回此后改动的项（删除的项值为 null），控制台只替换这些
项；未持有版本、记录不连续，或期间有 update、reset、提交（换了基础修订，记录的是相对
基础的改动）时返回全部项（`complete`）。edit、apply、merge、resolve、checkpoint 的
记录列出它改动的每一项（含引用维护连带改的），由 `TestWorkspaceItemsSinceAVersion`
（随机操作序列后任一旧版本 + 增量 = 当前全部）锁定。几千个实体时一次自动保存的应答
从整份工作区（数 MB）降到改动项。

配置 ⇄ 项的编码只在服务端（`x/admin/configapp/items.go`，见
[编辑语义](editing.md)），控制台从 `Revision.sections` 读取修订的项；形状由
`x/admin/configapp/testdata/workspace_sections.json` 锁定。

## 取舍

- **仓库内模块，不拆仓库或进程**：只有一个使用方；提交要和配置校验、发布在同一边界
  内完成。边界由 depguard 保证，以后有第二个使用方再抽出。
- **不用外部 VCS 库**：go-git 没有三方合并；git2go 需要 cgo 且按文本行合并；Dolt 要换
  配置库并把配置建模成表；structured-merge-diff 要写类型化 schema 并引入字段归属；
  Yjs/Automerge 静默合并、不暴露冲突。事务、条件写和不可变历史数据库已经提供，三方
  合并规则是配置特有的，自己维护最可控。
- **不做**：语义合并（危险组合交给审核页人工判断）；操作日志撤销与事件溯源（只记
  审计）；结构同步的三方比较；单独的“检查点”实体（草稿就是检查点）。

## 测试

- `core/workspace`：表驱动用例；合并性质测试（3000 个种子：冲突两侧如实、未冲突的
  叶子取改动方、交换两侧冲突对称、全取对方等于对方）；跨多次发布与解决的随机模型
  测试；`workspacetest` 一致性套件（内存、SQLite、三库集成）。
- `x/workspaceops`：并发写合并与冲突、update、合并草稿（幂等）、提交（检查点、提交
  期间写入、落后时不留草稿）、写入记录；交错模型测试（`interleave_test.go`，400 个
  种子）随机执行写入、合并草稿、解决冲突、他人发布、update、reset、存草稿与提交，并在
  提交与存草稿构建期间插入其他操作，每步检查：未发布的已合并草稿仍记在工作区、已发布
  的被丢弃、reset 丢掉的重新待审，声称与工作区一致的检查点内容确实一致，提交发布的
  正是审核的快照。
- `x/admin/graph`、`x/admin/configmcp`：完整流程（编辑、并发冲突、提交被拒、解决、
  提交、记录），方案叠加与影响绑定。
- 控制台：副本与待写项、在途写入、服务端冲突的显示。
