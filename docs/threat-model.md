# 威胁模型与证据账本

本文件记录安全分析、稳定 threat ID、控制措施、验证证据与剩余风险。运行时实际行为、
默认值和已知限制仍以 [安全模型](security.md) 为单一真相源；本文件不把设计意图、
单元测试或规划中的 corpus/fuzz target 表述为形式化证明。

## 范围与分级

保护资产包括数据库数据与 schema、subject/tenant 身份、事务隔离、配置快照、缓存与
预算状态、DSN secret 和审计记录。主要入口是 MCP payload、HTTP header/session、
配置与热重载、实体 IR/codegen、数据库 provider、事务 token、缓存和 procedure。

信任边界位于调用方与 transport、可信代理与服务、配置管理员与运行时、统一 engine
与数据库，以及不同 role/subject/tenant/session 之间。攻击者可能控制 MCP 参数和
未经信任的 HTTP header，可能持有共享 bearer token；不假设攻击者能修改配置、
服务进程或数据库权限。可信代理必须先认证调用方、删除外部身份 header，再注入规范化
身份。数据库管理员审核的 procedure 及绕过本服务的数据库访问属于外部信任范围。

等级用于安排 adversarial corpus 和回归优先级，不表示漏洞已被确认：

- **critical**：一旦控制失效，可直接造成跨租户/越权数据访问或写入、身份边界绕过，
  且可能影响整个 datasource。
- **high**：一旦控制失效，可稳定泄露受保护信息、破坏快照/事务隔离，或以低成本造成
  显著资源耗尽；影响通常受单个入口、会话或配置边界限制。

本阶段只登记 `critical` 和 `high`。ID 一经公开不复用；威胁关闭后保留条目和 ID，
后续仅更新状态、证据与剩余风险。

Provider 适用范围使用以下口径：

- **共享层**：控制位于 transport、配置、tool、engine、cache 或 transaction 层，
  PostgreSQL、MySQL、OceanBase 共用；这不等于三库均已有独立真实数据库测试。
- **三库 integration**：当前三种 provider 均有对应真实数据库测试。
- **部署边界**：主要由反向代理、网络和服务配置决定，与 SQL 方言无关。

## Threat 账本

### TM-001 — identifier/schema confusion

- **等级/状态**：critical / 已由 adversarial corpus 与属性测试持续验证。
- **攻击**：把用户值、别名、大小写或相似 schema/字段名解释为配置标识符，访问未授权
  表、字段或 procedure。
- **控制**：工具不接受原始 SQL；标识符只能来自已解析实体模型并由方言引用；用户值
  只能进入 placeholder；未知字段、隐藏字段和不允许的字段用途在 codegen 前拒绝；
  启动自省检查配置实体与字段。
- **现有证据**：`core/config/config_test.go` 的规范化碰撞与字段 ACL 校验、
  `core/tool/tool_read_test.go` 的隐藏字段 filter 拒绝，以及共享 codegen/tool 测试。
- **持续验证**：`core/codegen/adversarial_test.go` 覆盖分隔符和 dialect 引用；
  `core/tool/adversarial_test.go` 覆盖未知 schema、entity 和字段输入；IR validator 与
  codegen fuzz/property target 持续断言值参数化、引用稳定或 fail closed。
- **剩余风险**：当前没有形式化语法证明；数据库 collation、搜索路径和配置管理员误配
  仍可能产生差异。自省后的外部 schema drift 需由部署与 drift 检测处理。
- **Provider**：共享层适用于 PostgreSQL、MySQL、OceanBase；引用与 schema 解析存在
  方言差异，计划 corpus 必须按三种 dialect 运行，不能由单一 provider 结果外推。

### TM-002 — 字段侧信道

- **等级/状态**：high / 已由 corpus 覆盖字段拒绝与输出边界。
- **攻击**：通过隐藏或 mask 字段的 filter、cursor、group-by、aggregate、写谓词、
  错误差异或结果统计推断受保护值。
- **控制**：未知字段与被排除字段返回同类错误；所有字段用途先做可见性/ACL 检查；
  mask 字段只允许投影，禁止值揭示用途；describe/schema/procedure 输出字段收敛，
  所有结果路径返回前执行 mask；授权拒绝（`UNAUTHORIZED`）对客户端返回统一泛化
  `reason`，不回显实体/字段/角色细节，详细拒绝原因仅写入审计并由 decision ID
  关联。
- **现有证据**：`core/tool/tool_read_test.go` 的隐藏字段拒绝与授权范围缓存测试、
  `core/tool/tool_aggregate_test.go` 的 mask 用途拒绝和结果 mask 测试，以及三库
  `Test*RLSRowFilterAndMasking` integration。
- **持续验证**：`core/tool/adversarial_test.go` 对隐藏/mask 字段的 filter、cursor、
  未知嵌套字段和 payload 尾随值断言同类拒绝且不执行查询；既有 aggregate/describe
  测试继续覆盖其他用途和输出路径。
- **剩余风险**：响应延迟、数据库级错误、合法聚合的小样本推断和多次查询关联尚无
  差分隐私保证；mask 不是加密。机器错误码本身仍可区分“实体不存在”
  （`ENTITY_NOT_FOUND`）与“存在但无权”（`UNAUTHORIZED`），这是为 Agent 自修复
  保留的既有语义，接受为已记录的枚举侧信道残余。
- **Provider**：字段授权与 mask 是共享层，基础 read/row policy/mask 已有三库
  integration；时序和数据库错误文本可能随 provider 不同，尚未独立证明不可区分。

### TM-003 — row policy 绕过

- **等级/状态**：critical / 已由组合 corpus 与三库 integration 覆盖基础路径。
- **攻击**：使用 OR/NULL、嵌套 filter、aggregate、expand、写操作或缺失 subject
  弱化/绕过租户行谓词。
- **控制**：row policy 与用户 filter 固定以 AND 合并；`${subject.x}` 缺失解析为
  NULL 条件；read/write/aggregate 共用授权后的 IR；无用户或行级谓词的危险写入与
  aggregate fail closed。
- **现有证据**：`core/rbac/rbac_test.go` 的 subject 解析、`core/codegen` 与
  `core/tool` 测试，以及 PostgreSQL/MySQL/OceanBase 的
  `Test*RLSRowFilterAndMasking` integration。
- **持续验证**：`core/tool/adversarial_test.go` 断言用户 tenant filter 与 policy
  始终 AND 合并；三库 integration 对宽松用户 filter 的尝试仍要求 policy 生效。
- **剩余风险**：它不是数据库原生 RLS；任何直连数据库、受信 procedure 内部 SQL
  或未来绕过统一 engine 的入口都不受此保证。配置错误的 policy 仍由管理员承担。
- **Provider**：共享 policy/IR 逻辑适用于三库，基础路径已有三库 integration；
  procedure 内部行为不在 provider 证明范围。

### TM-004 — 身份/transaction token 混淆

- **等级/状态**：critical / 已由 scope corpus、状态机 fuzz 与既有 MCP 测试验证。
- **攻击**：在不同 session、role、subject 或 datasource 重放 transaction token，
  或在身份校验前命中缓存/singleflight，借用他人事务上下文。
- **控制**：token 查找绑定 session、规范化 role、完整 subject 与 datasource；
  显式事务在进入 engine 前校验 token，绕过全局缓存并禁用 singleflight；HTTP
  session 同样绑定创建时身份；关闭 session 时回滚并清理。
- **现有证据**：`core/tool/transaction_test.go` 的 scope、commit 失败回滚和断连
  回滚测试，`core/tool/tool_runtool_test.go` 的 transaction-before-singleflight
  测试，以及 `x/mcpserver/http_test.go` 的 session identity 绑定测试。
- **持续验证**：`core/tool/transaction_adversarial_test.go` 交叉组合
  token/session/role/subject/datasource、终态重用和状态机操作序列，并断言关闭后不遗留
  开放事务。
- **剩余风险**：token 是进程内 capability，不是独立认证凭据；进程重启后状态丢失。
  共享 bearer token 本身不映射独立调用方，身份可信度仍取决于 transport/代理；
  配置 `users` 后可改用每用户 token（见 TM-010）。
- **Provider**：transaction manager 与 MCP 身份绑定属于共享层；目前只有 PostgreSQL
  MCP e2e，MySQL/OceanBase 为核心层验证，不能宣称三库端到端等价。

### TM-005 — trusted proxy spoofing

- **等级/状态**：critical / 已由 handler corpus 覆盖，真实代理 e2e 仍未实现。
- **攻击**：外部调用方伪造 `X-MCP-Role`/`X-MCP-Subject`，利用错误的代理信任范围
  冒充其他 tenant 或 role。
- **控制**：默认忽略代理身份 header；启用 `trustProxyHeaders` 时必须同时配置
  mTLS `clientCA` 或非空 `trustedProxyCIDRs`；CIDR 模式校验来源地址；畸形
  subject JSON 返回 400。认证可热重载：新认证在装配前准备好（校验并读取证书，失败则 reload 失败），发布时
  直接切换到准备好的对象，不再读取文件，不会出现配置已发布而认证仍是旧的；
  启用 mTLS 时每个请求都按当前 `clientCA` 校验其连接实际出示的客户端证书，
  切换前建立、未出示证书（或由已替换 CA 签发）的连接不会被视为 mTLS 通道。
- **现有证据**：`core/config/config_test.go` 的信任边界校验，
  `x/mcpserver/http_test.go` 的 untrusted header、malformed subject、CIDR 和
  session identity 测试，以及 `x/mcpserver/auth_reload_test.go` 的“热切换到
  mTLS 后旧连接被拒”测试。
- **持续验证**：`x/mcpserver/http_test.go` 覆盖不可信来源伪造身份、畸形和尾随
  subject JSON、body 边界与 session 身份切换；真实反向代理 e2e 仍是剩余工作。
- **剩余风险**：服务不能证明代理已认证调用方或删除外部 header；错误配置 CIDR、
  代理链保留来源地址不当、同机受信网络内攻击均可能突破该边界。
- **Provider**：部署边界，与 PostgreSQL/MySQL/OceanBase 无关；数据库 integration
  不能提供此项证据。

### TM-006 — 跨租户缓存污染

- **等级/状态**：critical / 已由授权 scope corpus 与既有失效测试覆盖。
- **攻击**：构造 role/subject 序列化碰撞、复用 singleflight，或利用事务写与缓存
  失效顺序，让一个授权范围读取另一范围或陈旧事务结果。
- **控制**：缓存/singleflight key 包含 role 与完整 subject；编码区分边界和类型；
  显式事务绕过全局读缓存，事务写只在成功 commit 后按实体失效；expand 不缓存。
- **现有证据**：`core/tool/tool_helpers_test.go` 的字符串边界/类型碰撞测试、
  `core/tool/tool_read_test.go` 的授权 scope 隔离测试、transaction cache 测试及
  `core/engine/engine_test.go` 的 singleflight 并发测试。
- **持续验证**：`core/tool/adversarial_test.go` 断言不同 role/subject 不能共享
  cache 或 singleflight key；既有 helper、transaction 和 engine 测试覆盖编码边界与
  commit/rollback 失效顺序。
- **剩余风险**：进程内缓存不提供跨实例一致性；配置错误使两个 tenant 获得相同
  subject 时无法由缓存层区分。未来新增授权属性时必须同步进入 key。
- **Provider**：共享 cache/tool 层，适用于三库；当前证据主要是核心层 fake DB，
  尚无三库独立缓存隔离 integration。

### TM-007 — reload 竞态

- **等级/状态**：high / 已由 publish-then-drain 与 race 测试覆盖。
- **攻击**：在配置发布、旧请求 drain、事务/预算状态保留或 provider 关闭交错时，
  让请求混用新旧授权快照、使用已关闭资源或绕过新限制。
- **控制**：新快照完整构建成功后才发布，失败保留旧 app；每个请求自始至终只用
  一个快照（`Acquire` 租约）。发布立即生效，新请求不等待旧请求（旧做法
  drain-before-publish 会让一个慢查询或长时间持有租约的操作卡住全部新请求）；
  旧 app 由后台在其在途请求结束后关闭。被删除/禁用用户的会话在发布时解除并回滚
  事务，旧 app 排空后再按这些会话 ID 回滚旧请求中途打开的事务；会话 ID 不复用，
  用户重新启用后新建的会话不受影响。新旧快照共享连接池、IO 配额与读缓存
  （`bootstrap.Shared`）：两代合计不超过 IO 上限，任一代的写入按物理数据库使两代
  缓存失效，读取在失效后不会回填旧值（失效戳）；待回收快照最多 3 个。
  transaction manager 与 budget 状态保留并原子更新限制；事务绑定开启时的连接，
  重载把其数据源改到别的连接后，该事务的后续语句返回 `TRANSACTION_STALE`（只能
  回滚），不会按新配置的实体映射与失效目标在旧连接上执行。只有 listener（传输、
  地址、TLS 开关）变化拒绝热重载。控制台 schema 扫描使用独立连接、不持有租约。
- **现有证据**：`x/bootstrap/runtime_test.go` 的“发布不等待租约、旧 app 在租约
  释放后关闭”、失败保旧、事务与预算限制更新、待回收上限测试；
  `core/tool/transaction_test.go` 的“连接被重载改掉后事务拒绝语句”测试；
  `x/bootstrap/bootstrap_access_test.go` 的吊销通知与“重新启用后新会话不受旧
  撤销影响”测试；`x/bootstrap/shared_test.go` 的跨代缓存失效、共享 IO 配额与
  连接复用测试；CI 默认运行 race detector。
- **持续验证**：以上测试在默认单元测试中运行；CI race 检查并不枚举所有调度交错。
- **剩余风险**：现有测试不是所有调度的形式化证明；新旧快照重叠期间各自的 engine
  在途队列与预编译语句并存（IO 配额、连接池与缓存已共享）；熔断器为进程级而非按
  物理数据库；多进程/多实例配置发布不在当前一致性模型内。
- **Provider**：runtime 控制为共享层；provider 资源 drain 依赖各 driver 行为，
  尚无三库专门的 reload integration。

### TM-008 — oversized payload

- **等级/状态**：high / HTTP 边界与 MCP payload fuzz 已持续验证。
- **攻击**：发送超大 body/header、深层 JSON、超量 filter/IN/expand 或巨大返回值，
  消耗内存、CPU、goroutine、数据库连接或审计容量。
- **控制**：HTTP body 默认 4 MiB、header 默认 1 MiB，并配置 header/idle timeout；
  IR 对 filter、IN、group-by、aggregate、expand 数量设上限；engine、预算、缓存、
  事务、返回行数/字节与执行时间均有容量或 deadline。
- **现有证据**：`x/mcpserver/http_test.go` 的 oversized body 拒绝、
  `core/tool/tool_runtool_test.go` 的返回字节上限，以及 config/tool/engine/cache
  的容量和 cardinality 单元测试。
- **持续验证**：`x/mcpserver/http_test.go` 覆盖 body/subject 边界；
  `core/tool/adversarial_test.go` 和其 fuzz target 覆盖畸形、尾随、未知和受限字段
  payload，并断言拒绝时不进入数据库执行。
- **剩余风险**：stdio 不受 HTTP body/header 限制；JSON 解析在结构校验前仍会消耗
  资源；分布式限流和 durable audit 不在当前范围，应用限制不能替代上游连接限制。
- **Provider**：transport/IR/engine 控制属于共享层；数据库 timeout 参数随 provider
  不同，连接级 timeout 的数据库触发路径尚未分别做 integration。

### TM-009 — 多角色合并越权与租户打穿

- **等级/状态**：critical / 覆盖规则与租户硬边界已由单元、随机化与 HTTP e2e 验证。
- **攻击**：用户同时拥有字段集和行范围都不同的多个角色，或叠加直授权限，
  期望字段与行分别取并集，从而读到任何单一授权项都没有授予的"字段 × 行"组合；
  或借助一个不限行的角色打穿租户隔离。
- **控制**：授权按请求选择覆盖集——只有覆盖本次请求所用全部字段的授权项参与，
  行范围在覆盖集内取 OR；没有单一授权项覆盖时拒绝并返回
  `AMBIGUOUS_FIELD_SCOPE`（只列出调用方本就可读的字段集）；实体 `tenantPolicy`
  对所有主体始终 AND，不参与授权项之间的合并；用户配置的 subject 优先于代理头。
- **现有证据**：`core/rbac/grant_test.go` 的跨授权项组合拒绝、租户硬边界、
  直授/顶层/实体级授权合并测试与覆盖不变量随机化测试；
  `x/mcpserver/e2e_users_test.go` 在真实 HTTP + PostgreSQL 上验证多角色用户
  看不到其他角色行上的字段。
- **持续验证**：覆盖不变量随机化测试在默认单元测试中运行；e2e 随
  `make test-e2e` 运行。
- **剩余风险**：未实现逐行逐列的精确并集，部分合法组合需要 Agent 显式指定字段；
  create 不校验写入值满足租户约束（与现有 row policy 行为一致）。
- **Provider**：授权属于共享层；e2e 只覆盖 PostgreSQL。

### TM-010 — 用户身份伪造、提升与吊销残留

- **等级/状态**：critical / 认证链路、代理头与吊销已由 handler 与 runtime 测试验证。
- **攻击**：伪造或猜测用户 token；在已认证用户请求上附加 `X-MCP-User`/
  `X-MCP-Role`/`X-MCP-Subject` 冒充他人或改写租户；利用被删除/禁用用户的旧会话
  与在途事务继续访问。
- **控制**：用户 token 只以 `sha256:` hash 保存，CLI 生成 256 bit 随机 token；
  配置用户后，无共享 token 且非 mTLS/可信代理通道时必须携带用户 token；带用户
  token 的请求出现任何代理身份头返回 403；`X-MCP-User` 必须指向启用用户且不能与
  `X-MCP-Role` 同时出现；用户表跟随 snapshot，热重载删除或禁用用户后解除其会话
  绑定并回滚在途事务；启用/关闭用户功能随重载切换（是否要求用户 token 跟随当前
  快照是否配置了用户），新认证在装配前准备好。
- **现有证据**：`x/mcpserver/users_test.go` 的认证矩阵、代理头、身份冲突与会话
  吊销测试；`x/bootstrap/bootstrap_access_test.go` 的吊销通知测试；
  `core/config/config_access_test.go` 的 tokenHash 与名称校验测试。
- **持续验证**：以上测试在默认单元测试中运行。
- **剩余风险**：token 没有过期时间；hash 未加盐，依赖 token 本身的高熵，人工设置
  低熵 token 会降低强度；`X-MCP-User` 的真实反向代理 e2e 仍未实现。
- **Provider**：transport/部署边界，与数据库无关。

### TM-011 — 配置存储泄露、篡改与并发覆盖

- **等级/状态**：high / 隔离、密钥规则、完整性与并发已由单元与三库 integration
  验证。
- **攻击**：把与业务同库的 `smcp_` store 表配置成实体或经 schema 导入读出配置
  与用户 hash；让明文 DSN 密码或共享 token 进入 store；直接改写 store 中的
  payload；两个运维同时发布互相覆盖。
- **控制**：配置校验拒绝 `source` 使用 `smcp_` 前缀的实体，introspection 跳过
  这些表；store 模式 import 与加载都要求 DSN 密码为占位符、共享 token 为空；
  加载 revision 前重算 SHA-256，不一致拒绝并保留当前快照；发布与回滚在锁行上
  串行化，并以期望的当前发布 id 做乐观并发检查。
- **现有证据**：`core/config/config_access_test.go` 的保留前缀校验；
  `x/bootstrap/bootstrap_store_test.go` 的明文密钥与篡改 payload 测试；
  `cmd/sql-mcp-server/store_cmd_test.go` 的 import 拒绝测试；
  `x/configstore` 的 SQLite 与 PostgreSQL/MySQL/OceanBase 一致性测试（含 8 路
  并发发布只有一个成功）；三个 provider 的 introspection integration 断言跳过
  `smcp_` 表。
- **持续验证**：单元测试默认运行；三库 store 与 introspection 随
  `make test-integration` 运行。
- **剩余风险**：能直接写 store 表的数据库账号可以改写配置（hash 只防意外损坏
  与不一致，不防有权限的改写）；store 不加密 payload，用户 token hash 与实体
  元数据对 store 读者可见；没有 payload 签名。
- **Provider**：store 覆盖 SQLite、PostgreSQL、MySQL、OceanBase；introspection
  过滤覆盖 PostgreSQL、MySQL/OceanBase。

## 证据维护规则

- 本账本每个 threat ID 到回归测试的映射由
  `internal/threatcheck/coverage.json` 维护，并在默认单元测试中机器校验：
  新增 TM ID 未映射、引用的测试被删除或改名、任一条目缺失测试证据均使 CI
  失败；剩余缺口必须在映射中显式标注。
- corpus 用例应引用稳定 threat ID，并标记等级、入口、预期拒绝/允许结果及适用
  provider；critical/high 用例只有进入 CI 后才可标记为“持续验证”。
- fuzz crash 必须固定 seed、最小化并增加引用 threat ID 的回归用例；短时 fuzz
  通过只说明该运行未发现 crash。
- 修改身份作用域、字段校验、row policy 合并、缓存 key、事务状态机、reload 发布
  或 payload 上限时，应同时复审本账本、[核心不变量](invariants.md)和相关测试。
- Provider 证据按 [Provider 兼容矩阵](provider-compatibility.md)区分真实数据库
  验证、核心层验证和未独立验证，不以共享代码推断 provider 行为完全一致。
