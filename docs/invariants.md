# 核心不变量

这些不变量是评审和测试的约束，不等同于“每项都有形式化或 property test”。
验证方式包括 depguard、单元测试、race/e2e 和代码结构；具体测试命令见
[testing.md](testing.md)。[威胁模型](threat-model.md)把攻击场景映射到这些不变量
及其他控制与证据；映射关系不把单元测试提升为形式化证明。

## 依赖边界

- **I1** `core/` 中的包只依赖标准库和其他核心包。
- **I2** 外部适配层依赖方向为 `x/ -> core`，核心不得 import `x/`。

主要由 `.golangci.yml` 的 `core-purity` depguard 规则与 CI lint 检查。
YAML presence 解码位于 `x/configyaml`，不会把 `yaml.v3` 引入核心。

## 查询与授权

- **I3** 用户值只能作为 SQL 参数，不能拼接到 SQL 文本；标识符来自已解析实体。
- **I4** 标记为 `CostGated` 的实体工具在执行生成 SQL 前调用成本 gate。
- **I5** 成本 gate 在实体 action/字段授权通过后调用。
- **I6** 读取返回字段不得超出授权 decision 的字段集合。
- **I7** 有效谓词为 `用户谓词 AND (覆盖集内各授权项行范围的 OR) AND tenantPolicy`；
  覆盖集中任一授权项不限行时中间项为 TRUE。只有单个角色时即"用户谓词 AND 角色
  谓词"。授权主体是角色名或 `user:<name>`，缓存、singleflight、事务和预算均按
  主体隔离。
- **I8** 通用实体工具只能访问 `entity.MCP.DMLTools` 开启的实体。
- **I15** filter、group-by、set、values 和 cursor 字段必须是可见实体字段；
  隐藏字段不能作为谓词或写目标。
- **I16** mask 字段只允许作为读取投影；不得用于 filter、cursor、group-by、
  aggregate 或 update/delete 谓词，所有结果路径在返回前执行 mask。
- **I17** 写保护、CALL 审核、输入 cardinality、timeout 和结果 cap 属于不可关闭
  的 Safety/Enforcement；`cost.enabled: false` 只能关闭 EXPLAIN/AQE 估算。
- **I18** 主键白名单与 allow template 只能跳过 Estimate，不得绕过 mandatory
  enforcement。
- **I19** procedure 默认拒绝；必须同时标记 `trustedProcedure` 且命中 reviewed
  allow fingerprint，返回行数受独立硬上限。
- **I20** aggregate 必须包含用户或行级谓词；无谓词聚合 fail closed。
- **I21** filter、IN、group-by、aggregate 和 expand 数量有上限；expand 必须分批。
- **I22** cache 条目、单条缓存值、feedback fingerprint、预算 session 和事务均
  有全局容量边界。
- **I23** begin、commit、rollback 各自受 deadline；commit 失败必须尝试 rollback。
- **I24** 不能将返回行数冒充实际扫描行数；跨方言只承诺 EXPLAIN 估算上限，并在
  计划未知或 EXPLAIN 失败时 fail closed。
- **I25** 多角色/直授合并时，每个返回的行和列都至少被一个授权项同时覆盖：
  只有覆盖请求所用全部字段的授权项参与行范围合并，没有单一授权项覆盖时拒绝
  （`AMBIGUOUS_FIELD_SCOPE`），不对字段与行分别取并集。
- **I26** 实体 `tenantPolicy` 对所有主体始终 AND，不参与授权项之间的 OR；用户
  配置的 subject 属性优先于可信代理注入的同名属性。
- **I27** 配置存储表（`smcp_` 前缀）不能被任何实体的 `source` 引用，也不出现在
  introspection 结果中。
- **I28** 从 store 加载的 revision 必须先通过 content hash 校验与 store 模式密钥
  规则，任一失败都保留当前快照；任一时刻至多一个 published revision，状态转换
  以乐观并发串行化。
- **I29** 可信 SQL 逃生通道 `execute_sql` 只在 `tools.executeSql`、调用用户持有
  `sql:execute@<数据源>`、数据源声明 `sqlEscape` 三者同时成立时可见与可执行；其
  语句仍只由执行器（`pipeline.go`）调用数据库，timeout、结果行数 / 字节上限、预算与
  限流不可关闭；审计记录（执行前后各一条）写入失败即不执行或不返回结果；受治理面
  （IR、Metric、语义层、扩展点）没有任何路径到达它。I3、I4–I8、I15、I16、I19、I24
  只对受治理面成立：逃生通道不参数化调用方的 SQL、不做字段与行治理、不脱敏、
  估算失败按配置拒绝或跳过。

MCP 工具是否在 tools/list 中还取决于全局 `tools` 开关；custom procedure tool
由 `mcp.customTool` 独立注册，不受通用 `executeEntity` 开关控制。

## 并发与资源生命周期

- **I9** Registry 和静态实体模型可并发复用；每请求身份与依赖放在
  `tool.Context`。
- **I10** 查询 Rows 必须关闭；显式事务必须 commit、rollback、超时回滚或由
  App/session 关闭回滚。
- **I11** context 取消应传递到 `database/sql` 调用并释放工具执行。
- **I12** 异步审计队列不得阻塞主调用；队列满时允许丢弃并计数。
- **I13** engine/预算达到配置并发上限时拒绝新工作，不创建无界 goroutine。所有
  触达数据库的执行都在执行总线的某个通道上准入并共享 `rateLimit.ioPool`，非工具
  调用通道合计至多占一半；生产代码的 goroutine 与定时器只经 `core/bus` 启动，由
  `internal/buscheck` 强制。

这些条目由包级单元测试、`go test -race` 和部分 MCP goleak e2e 覆盖；它们不
构成对数据库 driver 或外部审计 sink 的形式化证明。

## 写安全与字段隔离

- **I14** `requirePKForWrite` 开启时，非完整主键点 update/delete 必须被
  `WriteGuard` 拒绝，除非精确命中 allow template；无任何有效谓词的写始终由
  工具层拒绝。
- **I15** 的字段可见性检查同时覆盖读过滤和写目标，避免通过隐藏字段推断数据。
- **I30** 写后的行仍在调用方的行范围内（WITH CHECK）：create 插入的行、update
  修改后的行都必须满足 `授权项行范围的 OR AND tenantPolicy`。create 时调用方未提供、
  而行范围以等值固定的列（通常是隐藏的租户列）由服务端按该值填入；调用方提供的值
  使行越界即拒绝（`ROW_POLICY_VIOLATION`），不改写。update 把 `set` 代入行范围：
  必然越界即拒绝，否则剩余条件并入 WHERE，越界的行不被修改。比较按
  [IR 语义](design/ir-semantics.md)；无法精确判定（EXISTS、类型不可比、NULL 与
  未知列混合）时拒绝。
- **I31** update 默认不能修改标识行的列（主键，无主键时为第一个可标识行的唯一键），
  实体显式 `allowKeyUpdate: true` 时才允许；codegen 拒绝无谓词的 UPDATE/DELETE 与
  不赋值的 UPDATE，作为工具层之外的最后防线。

MySQL/OceanBase 的 `sql_safe_updates` 是额外防线，不替代 I14。配置显式关闭
`requirePKForWrite` 或加入 allow template 会缩小这项保证，必须经过安全评审。

## 变更规则

修改跨层执行顺序、字段校验、缓存 key、身份作用域、成本链、事务生命周期或
provider 路由时，应明确指出受影响的不变量与 threat ID，并增加对应回归测试。
若代码事实与本文件不一致，应先修正事实或降低本文件中的保证，不能只更新宣传
描述；threat 证据状态也应随实际测试范围更新。
