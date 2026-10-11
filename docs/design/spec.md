# 设计评审：配置的意图与生效值（Spec / Config）及按路径声明的合并规则

状态：**评审通过（2026-10-10）**：保留 `config.Config` 作为生效值、Spec 由生成器生成、已有 payload 的显式值不动。相关设计：[编辑语义](editing.md)、[版本管理](versioning.md)、
[配置存储](config-store.md)。

## 背景与问题

`core/config.Config` 同时承担两种角色：用户写下的**意图**，和 `ApplyDefaults` 之后服务使用的
**生效值**。“这个字段没写”于是有四种互不相同的表达，散落在代码各处：

| 表达方式 | 例子 | 位置 |
|---|---|---|
| 侧车 presence 表 | `Cost.present`、`AQE.present`、`Tools.present`、实体 `MCP.dmlToolsSet` | `core/config/config.go:308,346,409`；YAML 由 `x/configyaml.collectPresence` 遍历节点识别，JSON 由各类型的 `UnmarshalJSON` 识别——两套实现 |
| `*bool` 指针 | `Cost.Enabled`、`Cost.RequirePKForWrite`、`RateLimit.Enabled`、`Mask.Enabled`，配合 `EnabledOrDefault` | `config.go`、`config_access.go` |
| 零值即未设置 | 多数数值（`MaxINListSize`、`IOPool`……），用户无法显式写 0 | `config_defaults.go` |
| 零值即无限制 | 预算各项 | `core/budget` |

更关键的是 **payload 存的是生效值**：`configyaml.Encode` 编码的是 `ApplyDefaults` 之后的结构体，
112 个字段中只有 61 个 `omitempty`。后果：

- 第一次保存就把所有默认值固化成显式值，用户写过什么、没写什么无法再区分；
- 以后调整某个默认值，已有配置不会跟随（它们“写过”这个值）；
- 工作区的 settings 项里满是默认值（`workspace_sections.json` 中 `audit.queueSize: 1024` 等），
  差异与合并都在这些噪声上进行。

与之相伴的补丁：`MCPFlagsWithDMLTools`（程序构造配置时为了区分 false 与未设置）、
`configedit.User.KeepToken`（阶段 1 后已无调用方）、`ConnectionsOrDSN()`（旧写法 `dsn` 与
`connections` 并存，9 个文件各自兼容）、合并的 `OptionalText`（“空串即未设置”按属性名在任意
层级生效）。

合并规则同样按属性名推断（`x/admin/configapp/codec.go` 的 `Schema`）：任何对象列表只要元素有
`name` 或 `entity` 就按它做键，任何叫 `kind`、`description`、`source` 的文本空串即未设置。新增一个
恰好叫 `name` 的字段，或者 settings 里出现一个对象列表，合并语义就悄悄改变，没有测试会失败。

## 目标与非目标

目标：

- “未设置”只有一种表达：Spec 里的 nil；默认值只在一个地方决定（`Resolve`）；
- payload 与工作区项只记录意图，生效值在加载时计算；
- 字段的类型、约束、默认值、说明、合并规则都写在字段定义上（沿用 `schema` 标签与
  `fields.yaml` 的单一来源），新增字段时一次写全；
- 已存的 revision、草稿与工作区不需要迁移，内容哈希不变。

非目标：

- 不把已有 payload 里“等于默认值的显式值”自动去掉（无法区分用户特意写的与当年被固化的）；
- 不改变任何字段的生效值与校验规则（`rules.json`、所有现有测试保持通过）；
- 不改变工作区、草稿、发布的语义（versioning.md、editing.md 的不变量不变）。

## 结论

### 1. 两个类型

- **`config.Config` 保留名字，作为生效值**（33 个使用方文件绝大多数只读生效值，名字不变即可不动）：
  去掉 `present`、`dmlToolsSet` 等私有字段与各 `UnmarshalJSON`；有默认值的 `*bool` 变为 `bool`；
  `Databases` 总是规范形式（旧写法 `database`、`dsn` 在 `Resolve` 中转换为
  `databases.<name>.connections`），删除 `ConnectionsOrDSN()`、`EnabledOrDefault()` 等访问器。
- **`config.Spec` 表示意图**，由 `internal/schemagen` 从 `Config` 的定义**生成**（`spec_gen.go`）：
  标量与嵌套结构体变为指针，切片与映射保持（nil 为未设置），全部 `omitempty`；保留旧写法字段
  （`database`、`dsn`），让用户写的形式原样往返。新增配置字段只改 `Config` 与标签，`go generate`
  同步 Spec——不维护两份手写结构体。
- **`Resolve(Spec) (*Config, error)`**：同样生成字段复制与标签默认值（`resolve_gen.go`），再调用手写的
  `resolveDerived`（条件与派生默认值：缓存只在启用时取默认、`cache.maxEntryRows` 跟随
  `cost.maxRows`、成本安全默认只在成本闸门启用时生效、`secrets.allowedRoots`、角色名规范化、
  旧写法转换），最后 `Validate`。`ApplyDefaults` 删除，`Validate` 只在 `Resolve` 内调用。

### 2. 默认值写在标签上（实施时未采用，见“实施记录”）（实施时未采用，见“实施记录”）

`schema` 标签新增 `default=…`（如 `default=10000`、`default=30s`、`default=true`），schemagen 用它
生成 `Resolve` 的默认值、`schema.json` 的 `default` 与配置文档的默认值列——三者此前分别来自
`ApplyDefaults` 的运行结果，现在来自同一个标签。条件/派生默认值无法用一个常量表达的，留在
`resolveDerived`，并在标签上写 `default=derived`，文档生成器据此注明“见说明”。

### 3. 解码与编码

- `configyaml.Decode` 改为：严格解码为 `Spec`（未知键失败，同现在）→ `Resolve`。
  `collectPresence`、`Presence`、`ApplyPresence` 删除——指针本身记录了是否出现。
- `configyaml.Encode` 编码 `Spec`：只写出设置过的字段。`revisionops.Normalize` 改为
  `Normalize(spec) ([]byte, *Config, error)`：解析校验后编码 Spec。
- **已有 payload 不迁移**：它们把所有值都写成了显式值，按 Spec 解码后每个字段都“设置过”，
  生效值不变，重新编码逐字节相同（测试锁定，见下）。新保存的配置只记录意图。

### 4. 工作区项由 Spec 生成；零值默认字段“零值即未设置”

- `configapp.SectionsOf` 改为由 `Spec` 生成项（不再由生效值生成）：未设置的字段不出现在项里，
  项 → 编辑 → Spec 同样只写设置过的字段。`configedit` 改为编辑 Spec（`Edit` 的各分区是 Spec 的
  对应类型），`KeepToken` 删除。
- **兼容已保存的工作区**：旧项由生效值生成，带着 `exclude: false`、`disabled: false` 这类显式零值，
  新生成的基准项则省略它们。比较与合并时，凡**默认值就是零值**的字段（布尔默认 false、文本默认空），
  显式零值与未设置等价——这在语义上是精确的（`Resolve` 结果相同），因此旧工作区不会被误读为
  “已修改”，也无需改写存储。默认值非零的字段（如 `mcp.dmlTools` 默认 true）显式 false 与未设置
  不等价，照常比较。这条规则取代 `OptionalText`（`tokenHash` 空串即无令牌是它的特例）。

### 5. 合并规则按路径声明

`core/workspace.Schema` 由“按属性名”改为“按路径”：

```go
type Schema struct {
	// Lists keys the elements of the lists at these paths (e.g. "entity:fields[]"
	// keyed by "name"); any other list is one value.
	Lists map[string]string
	// Unset are the paths whose zero value means unset (see §4).
	Unset map[string]bool
}
```

路径相对于项，带项的种类前缀（`entity:fields[]`、`role:grants[]`、`settings:cost.maxRows`）。
configapp 在初始化时**反射项类型**构建它：项类型字段上的 `merge:"key=name"` 声明列表键
（`EntityItem.Fields`、`Relationships` 按 `name`，`RoleItem.Grants`、`UserItem.Grants` 按
`entity`）；零值默认字段由 Spec 的默认值得出（settings 项的路径来自 `Config` 的标签）。
settings 里的对象列表没有声明即整体比较——比按名字推断保守：两处改了同一列表的不同元素会记为
冲突，而不是被悄悄按名合并。现有合并性质测试（3000 个种子）在新规则下照常运行。

## 实施记录（与上文结论的差异）

实施后按实际情况做了以下调整，其余按结论落地：

- **默认值仍只在 `ApplyDefaults`**，没有改为 `default=` 标签：它本来就是唯一来源
  （`schema.json` 的 default 由生成器在种子配置上运行它推出），问题出在“未写”的表达与
  payload 固化了生效值，而不在默认值写在哪里。`Resolve` = Spec 转换（同时由指针是否为 nil
  得出 presence）→ `ApplyDefaults` → `Validate`，与旧加载链在构造上等价，语料测试证明逐字段
  相同（`x/configyaml/spec_test.go`）。presence 成为 `Resolve` 的内部细节：`Presence`、
  `ApplyPresence`、`collectPresence`、各 `UnmarshalJSON`、`MCPFlagsWithDMLTools` 已删除。
- **Spec 的列表与映射用 `config.List`/`config.Map`**（`IsZero` 只在 nil 时为真），写了空的
  保留、未写的省略，旧 payload 因此能逐字节往返；自由结构（行过滤、`subject`）保持
  `map[string]any`（YAML 解码嵌套映射时沿用外层类型）。
- **规范化**：解码与 `Normalize` 都执行 `Spec.Canonical()`——`omitempty` 字段写出的零值与空集合
  视为未写（更早的编码写过 `schema: ""`、`params: []` 这类值），写法相同的配置得到同一 payload。
- **生效值访问器保留**：`ConnectionsOrDSN()`、`EnabledOrDefault()` 与有默认值的 `*bool` 不动。
  删除它们需要“`Config` 总是生效值”，而大量测试（含三库集成测试）直接对在代码里构造、未补
  默认值的配置调用 `Validate` 与装配；试改后这些路径失败，且把 `*bool` 改为 `bool` 会让代码里
  显式写的 false 在 `ApplyDefaults` 时被当作未写——正是本设计要消除的那类错误。意图现在完全由
  Spec 表达，这些访问器只服务“代码里构造的配置”，不再承担意图。
- `export` 仍输出生效配置（它的用途就是看到默认值），存储改为写了什么。
- 候选实体（`configplan`）以生效形态给出（`DMLTools: true`、不设显式标记），经 `SpecOf` 写入时
  不写出该默认值；控制台导入时也不会把它误当作显式 false。
- 实施时发现并修复：Dockerfile 未复制 `planning.graphqls`（前端代码生成在镜像构建中会失败，
  与本阶段无关的既有问题），并加入新的 `merge_schema.json`。

## 影响面

| 使用方 | 改动 |
|---|---|
| `core/config` | 新增 `Spec`、`Resolve`、`default=` 标签；删除 presence、`ApplyDefaults`、访问器 |
| `internal/schemagen` | 生成 `spec_gen.go`、`resolve_gen.go`；默认值改从标签读取 |
| `x/configyaml`、`x/revisionops` | 解码/编码 Spec；`Normalize` 接受 Spec |
| `x/configedit`、`x/admin/configapp`（items、codec、reconcile） | 编辑与项基于 Spec；合并 Schema 由反射构建 |
| `core/workspace` | `Schema` 按路径；`Canonical`/`Same` 用 `Unset` |
| `x/bootstrap`、`core/tool`、`x/mcpserver`、`cmd` 等生效值使用方 | 只随访问器删除与 `*bool`→`bool`、`ConnectionsOrDSN` 删除做机械修改 |
| 控制台 | 新保存的配置项更稀疏，设置页已按 `schema.json` 显示默认值；`canonical` 的空值规则改为与服务端相同的“零值即未设置”路径集（由 `schema.json` 生成，不再硬编码属性名列表） |

## 交付步骤（每步可独立合并）

1. **合并规则按路径**（不依赖 Spec）：`core/workspace.Schema` 改为路径规则；configapp 反射项类型
   构建；控制台的 `optionalText` 改为同一规则（由服务端下发或 `schema.json` 生成）。
2. **生成 Spec 与 Resolve**：schemagen 生成两个文件，标签加 `default=`；新旧加载链并存，
   用等价测试证明对全部样例 `Resolve(Spec)` 与旧 `Decode` 的生效值完全相同。
3. **切换解码/编码与使用方**：`configyaml`、`revisionops`、`configedit`、configapp 改用 Spec；
   删除旧链路、presence、`KeepToken`、`MCPFlagsWithDMLTools`、`ConnectionsOrDSN`、访问器。
4. **文档**：`docs/configuration.md` 由生成器更新默认值列；CLAUDE.md 的“配置规则的单一来源”
   一节改写。

## 测试

- **等价性**（步骤 2 起常驻）：`fixtures/`、`examples/`、`x/admin/graph/testdata/roundtrip.yaml`、
  `core/config/testdata` 中的每份配置，新链路的生效值与旧 `Decode` 完全相同（reflect.DeepEqual）。
- **payload 稳定**：上述配置按旧方式规范化后的 payload，经 Spec 解码再编码逐字节相同（保证已存
  revision 的内容哈希不变）。
- **往返性质**：随机生成的 Spec 编码后解码不变；`Resolve` 对同一 Spec 结果确定；稀疏 Spec 的生效值
  等于把默认值写满后的生效值。
- **默认值单一来源**：`schema.json` 的 `default` 与 `Resolve(Spec{})` 的结果逐字段一致（生成器测试）。
- **合并**：路径规则下的合并性质测试；新增用例：settings 中出现带 `name` 的对象列表不会被按名合并；
  旧形状的工作区项（显式 `exclude: false`）与新形状的基准项比较相等。
- 现有 `rules.json`、`TestOptionalBlocksLoadWithDefaults`、工作区与 GraphQL 往返测试全部保持通过。

## 待决策

1. **生效值类型的名字**：建议保留 `config.Config`（使用方免改名），新增 `config.Spec`。若希望名字
   直接表达语义，可改名为 `config.Effective`，代价是 33 个文件的机械改名。
2. **Spec 生成还是手写**：建议生成（两份 30 个结构体的手写定义本身就是新的漂移源）。
3. **已有 payload 中等于默认值的显式值**：建议保持不动；如需要，后续在设置页提供逐字段
   “恢复默认”，由管理员确认后删除该值。

## 风险

- 生成器复杂度上升（Spec、Resolve、默认值三份输出）。以生成结果入库并由 `go generate` 过期检测
  兜底，同现有 `schema.json`。
- 条件默认值（成本安全默认、缓存默认）若误改为无条件，会改变生效值；等价性测试覆盖这类回归。
- 合并规则收紧（settings 对象列表整体比较）：settings 目前没有对象列表（对象列表只有实体的
  fields、relationships 与授权，都会显式声明键），现有行为不变；它只防止将来新增的列表被按名字
  悄悄合并。
