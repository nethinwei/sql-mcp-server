# 设计评审：Revision 与 Snapshot

状态：**评审结论（v0.1.5）**。本文只固定数据模型、兼容性与失败语义的设计
结论，不实现持久化 revision、draft/publish/rollback 或管理 API（v0.1.5 明确
非目标）。它是最小控制面阶段（主路线图 Next 2）的前置输入。

## 术语

- **Runtime snapshot**（已实现，`x/bootstrap/runtime.go`）：一次装配成功的
  `App` 的内存快照，由 `Runtime` 原子持有；请求通过 `Acquire()` 租约使用。
- **Revision**（本文设计，未实现）：一份配置的持久化修订，控制面发布后由
  数据面加载为新的 runtime snapshot。

## Revision 数据模型（结论）

| 字段 | 含义 |
| --- | --- |
| `id` | 单调递增的修订号，发布后不可复用 |
| `contentHash` | 确定性 export 字节的 SHA-256，同内容同 hash |
| `payload` | `sql-mcp-server export` 的确定性 YAML（含 `version` 契约标记）|
| `createdAt` / `author` | 审计所需的来源信息 |
| `state` | `draft` / `published` / `superseded` / `rolled-back` |

结论：payload 复用确定性 export 格式，使 revision 可 diff、可复现、可校验；
`contentHash` 由 export 的字节级确定性保证稳定。secret 只存占位符，明文永不
进入 revision。

## 兼容性（结论）

- revision payload 的兼容规则与配置契约一致：新增带默认值的字段兼容；删除、
  重命名或改义为 breaking，须提升 `version` 标记；
- 数据面加载 revision 时执行与启动完全相同的链路（解码、默认值、静态校验、
  secret 解析、drift 检查），不存在“控制面特有”的旁路加载；
- 不认识的 `version` 或校验失败的 payload 必须拒绝加载（fail closed），不做
  尽力兼容。

## 失败语义（结论）

- **控制面不可用**：数据面 fail-static——继续以最后一次成功发布的 snapshot
  服务，不接受新发布；恢复后从持久化状态收敛，不依赖内存。
- **snapshot 构建失败**（配置错误、secret 缺失、drift）：保留当前 snapshot，
  记录失败原因；与现有 `Reload` 语义一致（失败不发布）。
- **snapshot 不兼容**（版本不识别、hash 不匹配）：拒绝加载并显式暴露
  degraded 状态，而不是静默回退到内置默认值。
- 任何失败路径都不得使数据面进入“无授权配置”状态：没有可用 snapshot 时
  拒绝服务（fail closed），有旧 snapshot 时保守续用（fail static）。
- publish 必须复用与 CLI 热重载相同的变更守卫（transport/addr/auth/TLS/
  工具集变化拒绝热发布）；该守卫当前位于 CLI 层
  （`cmd/sql-mcp-server` 的 `validateHotReloadConfig`），实现控制面前应下沉到
  `x/bootstrap`，使两条发布路径共用一份规则。

## 在途请求一致性（结论）

- 一次请求自始至终使用同一个 snapshot（`Acquire()` 租约），发布新 snapshot
  采用 drain-before-publish：旧 snapshot 等待在途请求结束后才关闭；
- 事务跨越多次请求，绑定创建时的事务 manager；`ttl`/`maxOpen` 变化无法安全
  迁移在途事务，拒绝热发布、要求重启（现状保持）；
- budget session 用量在发布时保留并套用新限制（现状保持）；
- `tools/list` 在会话创建时固定；改变工具发现集合的 revision 要求重启或
  新会话，不做会话内热切换。

## 评审通过的验收映射

上述结论覆盖 v0.1.5 退出标准中“snapshot 的拒绝语义及在途请求一致性形成
评审通过的设计结论”一项；实现（持久化、publish/rollback、控制面 API）按
主 [Roadmap](../roadmap.md) 最小控制面阶段的门禁另行立项。
