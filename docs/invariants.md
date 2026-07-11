# 核心不变量

这些不变量是评审和测试的约束，不等同于“每项都有形式化或 property test”。
验证方式包括 depguard、单元测试、race/e2e 和代码结构；具体测试命令见
[testing.md](testing.md)。

## 依赖边界

- **I1** 核心包只依赖标准库和其他核心包，但 `config` 的 presence-aware YAML
  解码可直接依赖 `gopkg.in/yaml.v3`。
- **I2** 外部适配层依赖方向为 `x/ -> core`，核心不得 import `x/`。

主要由 `.golangci.yml` depguard 与 CI lint 检查。`yaml.v3` 只在单独的
`config-yaml-presence` 规则中对 `config` 精确放行，其他核心包仍由
`core-purity` 规则限制；仓库不宣称绝对“核心零依赖”。

## 查询与授权

- **I3** 用户值只能作为 SQL 参数，不能拼接到 SQL 文本；标识符来自已解析实体。
- **I4** 标记为 `CostGated` 的实体工具在执行生成 SQL 前调用成本 gate。
- **I5** 成本 gate 在实体 action/字段授权通过后调用。
- **I6** 读取返回字段不得超出授权 decision 的字段集合。
- **I7** 有行级策略时，有效谓词为用户谓词与角色谓词的 AND。
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

MCP 工具是否在 tools/list 中还取决于全局 `tools` 开关；custom procedure tool
由 `mcp.customTool` 独立注册，不受通用 `executeEntity` 开关控制。

## 并发与资源生命周期

- **I9** Registry 和静态实体模型可并发复用；每请求身份与依赖放在
  `tool.Context`。
- **I10** 查询 Rows 必须关闭；显式事务必须 commit、rollback、超时回滚或由
  App/session 关闭回滚。
- **I11** context 取消应传递到 `database/sql` 调用并释放工具执行。
- **I12** 异步审计队列不得阻塞主调用；队列满时允许丢弃并计数。
- **I13** engine/预算达到配置并发上限时拒绝新工作，不创建无界 goroutine。

这些条目由包级单元测试、`go test -race` 和部分 MCP goleak e2e 覆盖；它们不
构成对数据库 driver 或外部审计 sink 的形式化证明。

## 写安全与字段隔离

- **I14** `requirePKForWrite` 开启时，非完整主键点 update/delete 必须被
  `WriteGuard` 拒绝，除非精确命中 allow template；无任何有效谓词的写始终由
  工具层拒绝。
- **I15** 的字段可见性检查同时覆盖读过滤和写目标，避免通过隐藏字段推断数据。

MySQL/OceanBase 的 `sql_safe_updates` 是额外防线，不替代 I14。配置显式关闭
`requirePKForWrite` 或加入 allow template 会缩小这项保证，必须经过安全评审。

## 变更规则

修改跨层执行顺序、字段校验、缓存 key、身份作用域、成本链、事务生命周期或
provider 路由时，应明确指出受影响的不变量，并增加对应回归测试。若代码事实与
本文件不一致，应先修正事实或降低本文件中的保证，不能只更新宣传描述。
