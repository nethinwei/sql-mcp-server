# Tool Contract 与兼容规则

本文定义对外机器契约的组成、兼容与破坏性变化的判定规则，以及机器检查方式。
运行时安全行为见 [security.md](security.md)；配置字段见
[configuration.md](configuration.md)。

## 契约组成

对外机器契约由三部分组成：

1. **Tool input schema**：`tools/list` 暴露的每个工具的 JSON Schema，源文件
   为 `core/tool/schema/*.json`（procedure 自定义工具的 schema 由 entity
   参数派生，遵循相同规则）；顶层均为 `additionalProperties: false`，未知字段
   一律 `INVALID_INPUT`；
2. **机器可读拒绝（Denial）**：业务级拒绝在 `CallToolResult` 中以
   `IsError: true` 返回，`structuredContent` 携带以下对象：

   ```json
   {
     "code": "COST_EXCEEDED",
     "reason": "cost gate hard reject: score=20 rows=50000 cost=0",
     "retryable": true,
     "constraints": {"estimatedRows": 50000, "maxRows": 1000},
     "hints": ["add LIMIT or narrow the filter"],
     "decisionId": "0123456789abcdef0123456789abcdef"
   }
   ```

   - `code`：稳定机器码（全集见 `core/tool/testdata/contract.json`）；
   - `retryable`：在当前权限内修改请求后是否可能成功；
   - `UNAUTHORIZED` 的 `reason` 为统一泛化文案，不携带实体、字段或角色细节
     （防止受限角色枚举隐藏 schema，见 TM-002）；详细拒绝原因仅写入审计
     事件，经 `decisionId` 关联；
   - `AMBIGUOUS_ENTITY`（retryable）：`entity` 引用（`name`、`schema.name`、
     `datasource.name` 或 `datasource.schema.name`）在调用方可用的实体中命中多个；
     `constraints.candidates` 列出每个候选的无歧义写法，调用方不可用的实体不计入、
     不列出；
   - `AMBIGUOUS_FIELD_SCOPE`（retryable）：多角色用户请求的字段没有被任一
     单个授权项完整覆盖；`constraints.fieldScopes` 列出调用方本就可读的各个字段
     集合，显式指定其中一个集合内的字段即可重试；
   - `CONSTRAINT_VIOLATION`（retryable）：写入违反数据库约束；
     `constraints.kind` 为 `unique`、`foreign_key`、`not_null`、`check` 或
     `exclusion`，`constraints.fields` 只列出调用方可见的相关字段，不含约束名与
     数据库原文；
   - `ROW_POLICY_VIOLATION`（`constraints.fields` 非空时 retryable，v0.1.20 起）：create
     或 update 写出的行会落在调用方的行范围之外（租户策略、授权项行范围）；
     `constraints.fields` 只列出调用方可见、决定结果的字段，不含策略的值；
   - `DATASOURCE_FORBIDDEN`（不可重试）：动作路由到的数据源连接没有对应的数据库权限
     （包括连接到只读服务器），授权本身允许；需要管理员补权限或调整路由，修改请求无法
     绕过；
   - `ENTITY_UNAVAILABLE`（不可重试）：实体的表或已配置的字段在数据库中已不存在（装配时
     发现，或运行中语句报表/列不存在时，v0.1.17 起），调用未
     生成 SQL；`reason` 说明缺失的是表还是哪些列。只在授权通过后返回，无权的调用方
     仍得到 `UNAUTHORIZED`（TM-002）；需要管理员移除该实体或恢复表；
   - `RATE_LIMITED`（retryable）：调用者（用户或角色）自己的 `limits.rps` 或
     `limits.maxInflight` 已用满；稍后重试或减少并发；
   - `OVERLOADED`（retryable）：服务整体过载（执行队列已满、全局限流、正在关闭），
     与调用方无关；稍后重试。v0.1.17 前它是协议级错误，客户端看不到机器码；
   - `DATASOURCE_UNAVAILABLE`（retryable）：调用路由到的数据源连续失败，熔断器暂时
     拒绝新请求；等熔断恢复后重试，修改请求无济于事。v0.1.17 前同为协议级错误。只有
     说明数据库本身有问题的失败（连接中断、资源耗尽、关闭、内部错误）计入熔断；调用方
     造成的失败（取值与列类型不符、语法、约束、权限、死锁等）不计入（v0.1.21 起）；
   - 受治理工具中取值与字段类型不符、被数据库拒绝（如给时间列传非日期文本）时返回
     `INVALID_INPUT`（retryable，v0.1.21 起；此前为 `DATABASE_ERROR`），不带数据库原文；
   - `AUDIT_UNAVAILABLE`（不可重试）：`execute_sql` 的审计记录无法写入，调用未执行或结果
     已被扣下（`exec` 的效果可能已发生，按 `OUTCOME_UNKNOWN` 的口径确认）；
   - `DATABASE_ERROR` / `DATASOURCE_FORBIDDEN` 对 `execute_sql` 的**例外**：`reason` 携带
     驱动返回的错误原文，`constraints.sqlstate` 携带 SQLSTATE（驱动提供时）；受治理面的
     工具仍不回显原文；
   - `OUTCOME_UNKNOWN`（不可重试）：写入或提交执行期间连接中断或超时，数据库可能已经
     执行也可能没有；原样重试可能重复写入，应先读取受影响的行（或事务写过的内容）再决定；
     相关读缓存已按“已写入”失效；
   - `STATEMENT_TIMEOUT`（retryable，v0.1.21 起；此前为 `DATABASE_ERROR`，写入可能报
     `OUTCOME_UNKNOWN`）：数据库在自己的语句超时处停住并回滚了语句，没有生效；收窄请求或
     稍后重试。`execute_sql` 的 `reason` 同样携带驱动原文。与此前相同，它计入数据源熔断。
     语句执行中被中断（MySQL/OceanBase 1317，超过 deadline 时的 KILL QUERY）只对受治理的
     读取报 `STATEMENT_TIMEOUT`；写入、存储过程调用、提交与 `execute_sql`（任一 `mode`：
     查询也可能 `CALL` 过程）报 `OUTCOME_UNKNOWN`（过程中先前的语句可能已生效）；
   - `TRANSACTION_NOT_FOUND`：令牌对应的事务若在最近 10 分钟内结束，且请求来自同一会话
     与身份，`constraints.outcome` 给出结束方式：`committed`、`rolledBack`、`expired`
     或 `unknown`（提交结果未知）；其他调用方只得到不带 `constraints` 的未找到。`expired`
     包括超过 `transactions.ttl` 与空闲超过 `transactions.idleTimeout`；
   - `constraints.transaction`（v0.1.17 起）：事务内的语句失败时事务立即回滚，该次拒绝
     （任一 `code`）带 `"transaction": "rolledBack"` 与提示；之后用该令牌得到
     `TRANSACTION_NOT_FOUND`（`outcome` 为 `rolledBack`），需重新开启事务并重做写入；
   - `constraints`：可选的机器可读限制（估算行数、生效上限等）；
   - `hints`：修复建议，只能收紧或等价改写请求，不得扩权；
   - `decisionId`：贯穿 MCP 响应、审计事件（`DecisionID` 字段）与 trace
     span（`decision.id` 属性）的关联 ID；
   - 内部错误不进入该契约：协议层报错且不回显细节。

3. **YAML export**：`sql-mcp-server export` 的确定性输出（字段顺序、默认值
   与 secret 表示规则见 [configuration.md](configuration.md)）；

4. **审计事件（JSON Lines）**：文件审计 sink 每行一个事件，字段名自
   v0.1.6 起定版：

   ```json
   {
     "time": "2026-01-02T03:04:05Z",
     "decisionId": "0123456789abcdef0123456789abcdef",
     "role": "reader",
     "entity": "users",
     "action": "read",
     "tool": "read_records",
     "input": {"entity": "users"},
     "resultSummary": "2 rows",
     "allowed": false,
     "code": "UNAUTHORIZED",
     "error": "tool: unauthorized: ...",
     "returnedRows": 2,
     "durationMs": 1500
   }
   ```

   - `time`：RFC 3339；`durationMs`：整数毫秒；
   - `entity`/`action`：调用的逻辑实体与动作（`read`、`create`、`update`、
     `delete`、`execute`、`aggregate`、`describe`、`transaction`）；
   - `code`：拒绝时的稳定机器码，与 Denial `code` 同一取值集合；
     `allowed=false` 且 `code` 为空表示内部或协议级失败（不回显给客户端）；
   - `input` 为脱敏后的工具输入（掩码字段替换、transaction 句柄哈希、超长
     截断）；可选字段（`decisionId`、`role`、`entity`、`action`、`input`、
     `resultSummary`、`cost`、`code`、`error`、`user`、`roles`、`grants`）为空时
     省略；
   - `role` 是授权主体：角色名，或已配置用户的 `user:<name>`；`user`、`roles`
     分别为用户名及其角色；`grants` 为本次授权使用的覆盖授权项 ID（v0.1.11
     起新增，兼容变化）；
   - `execute_sql`（v0.1.14 起新增，兼容变化）每次调用写两条事件，`action` 为 `sql`，
     `grants` 为 `permission:sql:execute@<数据源>`，可选字段 `phase`（`begin` 在执行前、
     `end` 在执行后）、`datasource`、`connection`、`sql`（原样的语句文本，不脱敏）、
     `rowsAffected`；两条事件共用 `decisionId`，同步写入，可能排在队列中更早的异步事件
     之后，消费方按 `time` 排序；
   - `cost` 的内部结构暂不属于定版契约，消费方不得依赖其字段名。

`aggregate_records` 的每行以分组字段名加聚合列组成；聚合列名在所有数据库上相同
（v0.1.15 起）：`count(*)` 为 `count`，带字段的为 `<func>_<field>`（如 `sum_amount`），
与某个分组字段同名的聚合被拒绝（`INVALID_INPUT`）。此前列名取数据库自己的命名
（PostgreSQL 为 `count`/`sum`，同函数的多个聚合会互相覆盖；MySQL/OceanBase 为
`COUNT(*)`/``SUM(`amount`)``）。

去重计数（`{"func": "count", "field": "x", "distinct": true}`）的列名为
`count_distinct_<field>`。`having`（与 `filter` 同形）、`orderBy` 与 `limit` 作用于分组结果，
字段写分组字段或上面的聚合列名（如 `{"field": "sum_amount", "dir": "desc"}`）。

## 查询参数

- **过滤组合**：`filter` 的每一项是一个条件 `{field, op, value}`，或一个组：
  `{"any": [...]}`（其一成立，OR）、`{"all": [...]}`（全部成立，AND）、`{"not": {...}}`；
  顶层各项全部成立。组最多嵌套 4 层，条件总数（计入组内）受 `cost.maxFilterConditions`
  约束。read、aggregate、update、delete 通用；含 OR/NOT 的写入不会被当作主键点写。
- **数字**（v0.1.20 起）：整数按 int64 传递，超出范围的整数须写成字符串。带小数点或指数的
  数字按字段的列类型取值：浮点列（real、float、double）为浮点数；整数列拒绝
  （`INVALID_INPUT`）；decimal/numeric 等其他列按原文字传给数据库，不经 float64，
  `0.1`、超过 15 位有效数字的金额都按写出的值比较与写入。用于 `create_record` 的
  `values`、`update_record` 的 `set`、`filter`（含 `exists` 内）与 `cursor`；没有列可依
  的值（存储过程参数、`execute_sql` 的 `args`、关联字段与 `having` 的值）按原文字传递，
  由数据库转换。此前小数一律转为 float64。结果中的非有限浮点数（PostgreSQL 的 `NaN`、
  `Infinity`、`-Infinity`）JSON 无法表示，按数据库的写法以字符串返回（v0.1.21 起；此前调用失败）。
- **排序与分页**（`read_records`）：`orderBy: [{field, dir}]`（`asc` 默认，最多 8 项），
  末尾自动补上实体的键使顺序确定；脱敏字段不能排序。NULL 一律排在最后（可空字段）。
  带 `limit` 且本页取满时，结果在 `result` 旁给出 `nextCursor`，下一页与相同的
  `orderBy` 一起原样作为 `cursor` 传回（可空字段同样可以分页，游标里的 NULL 按“排最后”
  处理）；调用方读不到键时顺序不确定、不分页（用 `offset`）。不带 `orderBy` 的 `cursor`
  仍是“最后一行的主键值”。
- **跨关系**（`read_records`、`aggregate_records`）：`关系名.字段` 可用于 `filter`、
  `orderBy`、`groupBy`（及 `having` 中的该分组列），关系须指向一行（`one`、`one-to-one`、
  `belongs-to`）且连接列是目标实体的键；目标以左连接取得，调用方看不到或不存在的目标行
  表现为 NULL。`{"exists": "关系名", "filter": [...]}` 按任意关系过滤（目标中有满足条件的
  行），可放在 `not` 中；不嵌套。调用方必须能读目标中用到的字段，目标的行策略照常生效，
  脱敏字段不能用；关系的连接列按本实体字段授权；任一侧连接列被脱敏的关系不能用于
  跨关系过滤、排序、`exists` 或 `expand`（子行会带出与父行相等的原值）；目标须在同一
  数据源。`fields` 不能写关联字段（读取关联行用 `expand`），聚合不能聚合关联字段，
  按关联字段排序时不给 `nextCursor`，写操作的 `filter` 不能用 `exists` 或关联字段。
  跨关系的读取不进结果缓存。
- **去重**（`read_records`）：`distinct: true` 对 `fields` 去重；需要 `fields`，不能与
  `expand`、`cursor` 同用；排序字段须在 `fields` 中，NULL 的位置按数据库默认。
- **索引**：`describe_entities` 带 `entity` 时给出 `indexes`：
  `[{name, fields, method, unique, partial}]`，`fields` 是索引从首列起连续的、调用方可见
  且未脱敏的列（列前缀计为该列），第一列就不可见的索引不列出；`unique` 只在全部列都
  列出时为真；部分索引只标 `partial`，不给条件；表达式索引不列出。资源
  `sql-mcp://schema/{entity}` 同样给出。
- **成本拒绝**（`COST_EXCEEDED`）：`constraints` 带 `estimatedScannedRows`（预计读取
  行数）与 `maxScannedRows`（生效上限，见 `cost.maxScannedRows`）；全表扫描的表行数
  未能测得时不给 `estimatedScannedRows`，改给 `scannedRowsUnknown: true`（v0.1.20 起）。
  read/aggregate 的 `hints` 首条列出可用于过滤的索引字段。
- **`execute_sql` 元数据语句**：`query` 模式下单条 `SHOW`、`DESCRIBE <表>`、`DESC <表>`
  不做 EXPLAIN 估算（`cost.sqlEscape.metadata: allow`，默认），只受硬上限约束；
  `DESCRIBE ANALYZE`、`DESC SELECT` 等 EXPLAIN 用法照常估算。

## 工具结果

成功结果（v0.1.17 起）同时给出 `structuredContent` 与一个文本块，文本就是
`structuredContent` 的 JSON，两者内容一致；`tools/list` 为每个工具声明
`outputSchema`：

- 返回行的工具（`read_records`、`aggregate_records`、`execute_entity`、
  `procedure_*`）：`{"result": [行, ...]}`；有序读取还有下一页时另带 `nextCursor`；
  过程（或其他写操作）返回的行超出进程的结果内存（`rateLimit.maxResultMemory`）
  或调用的行上限（`cost.maxProcedureRows`、预算的 `maxReturnedRows`）时，写已生效，
  调用不失败，只返回已读取的行并另带 `"truncated": true`（v0.1.20 起；此前前者不受
  约束，后者返回可重试的 `BUDGET_EXCEEDED`，诱导重复执行），审计事件的
  `resultSummary` 记录截断；
  `create_record` 的 RETURNING 行被截断时返回 `{"rowsAffected": 1, "truncated": true}`；
  读取与聚合最多返回 `cost.maxRows` 行（调用者的 `limits.maxRows` 更严时取它）：`limit` 未写或
  大于它时按它读取，有序读取满页给 `nextCursor` 续读；没有游标可续（`read_records` 未按可翻页的
  `orderBy` 排序，或 `aggregate_records`）而恰好返回这么多行时另带 `"truncated": true`，表示之后
  可能还有行；`expand` 的一批子行同样以该上限为界，超过上限时整次读取以 `BUDGET_EXCEEDED`
  失败（v0.1.21 起；此前超出的子行被静默丢弃；上限就是 `cost.maxRows` 时恰好达到也失败，
  因为无法区分是否被截断），应缩小页大小后重试；
- 返回单个对象的工具直接给出该对象：`describe_entities`、`create_record`、
  `update_record`、`delete_record`、`begin_transaction`、`commit_transaction`、
  `rollback_transaction`、`execute_sql`。
- `create_record` 的对象是新行的标识键（按列名；行范围预填的列也在内，没有 RETURNING 的数据库上
  自增列取驱动的 last insert id）加 `rowsAffected`，没有 RETURNING 时另带 `lastInsertId`，截断时
  另带 `truncated`。这三个名字是保留的：标识键列恰好同名时会被覆盖。
- `rowsAffected` 在各数据库上都是语句**匹配**的行数：值没有变化的 `update_record` 也计入
  （MySQL/OceanBase 默认按 `clientFoundRows=true` 连接；DSN 显式写了 `clientFoundRows` 时按 DSN）。

`describe_entities` 不带 `entity` 时分页列出实体：
`{"entities": [{name, id, description, datasource}], "total": n, "nextOffset": m}`；
`search`（名称、ID、描述的子串，不区分大小写）与 `datasource` 过滤，`limit` 默认
100、上限 500，`nextOffset` 只在还有下一页时出现，原样作为 `offset` 传回。带
`entity` 时返回该实体的对象（`fields`、`keys` 等）。

资源 `sql-mcp://schema` 是摘要：`{role, entities, datasources: [{name, entities}],
usage}`；单个实体的字段、键与按动作的访问范围在资源模板
`sql-mcp://schema/{entity}`（`entity` 接受与工具相同的引用写法；调用方不能用的实体
与不存在的实体一样返回资源未找到）。

每个工具带 `title` 与行为提示 `annotations`：`readOnlyHint`（describe、read、
aggregate）、`destructiveHint`（update、delete、commit、execute、procedure、
`execute_sql`）、`idempotentHint`（describe、read、aggregate、update、delete、
rollback），`openWorldHint` 一律为 `false`（只访问已配置的数据库）。提示仅供客户端
参考，授权仍由服务端执行。

存储过程工具名为 `procedure_<过程名>`（不允许的字符替换为 `_`）；只有规范化后
同名的过程（不同命名空间的同名过程等）才追加 `_<规范 ID 的 8 位哈希>` 区分。

`execute_sql` 的输出不是行列表而是一个对象：`query` 模式为
`{"columns": [...], "rows": [[...]], "rowCount": n, "truncated": bool}`（行按位置给出，
同名列不合并；到行上限即停并标 `truncated`），`exec` 模式为 `{"rowsAffected": n}`。

## 兼容 vs 破坏性变化

**兼容变化**（可在任意 patch/minor 版本发布，CHANGELOG 记录即可）：

- tool schema：新增可选输入字段、放宽枚举、补充 `description`；
- Denial：新增可选字段、新增 `code` 取值、扩充 `constraints`/`hints` 内容；
- export：新增字段（带默认值）导致的输出差异；
- export：省略与缺省语义相同的空值（重新加载得到相同的生效配置）；
- 审计事件：新增可选字段、新增 `action` 或 `code` 取值。

**破坏性变化**（必须在 CHANGELOG `Breaking` 段明示，且遵循 semver）：

- 删除或重命名 schema 字段、收紧必填项、改变字段语义或类型；
- 删除或重命名 Denial 字段、删除或复用已发布的 `code` 值、翻转某 code 的
  `retryable` 语义；
- 改变 export 的字段顺序规则、默认值策略或 secret 表示；
- 删除或重命名审计事件字段、改变 `time`/`durationMs` 的编码。

`reason` 与 `hints` 的具体文案不属于契约，客户端不得对其做字符串匹配；
解析必须基于 `code` 与 `constraints`。

## 机器检查

`core/tool/testdata/contract.json` 是契约的评审快照，覆盖全部 tool schema、
错误码表（含 retryable）与 Denial 字段名。CI 的单元测试
`TestToolContractGolden` 在任何漂移时失败；有意变更时必须：

```bash
go test ./core/tool -run TestToolContractGolden -update
```

并在 PR 中按上文规则将变更归类为兼容或破坏性，写入 CHANGELOG。

Denial 的 JSON 字段名另有精确 golden（`TestDenialJSONGolden`）；审计事件
字段名由 `core/audit` 的 `TestEventJSONGolden` 冻结；export 的确定性由 CLI
golden 测试保证（同一配置重复导出字节级一致）。
