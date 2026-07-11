# Tool Contract 与兼容规则

本文定义对外机器契约的组成、兼容与破坏性变化的判定规则，以及机器检查方式。
运行时安全行为见 [security.md](security.md)；配置字段见
[configuration.md](configuration.md)。

## 契约组成

对外机器契约由三部分组成：

1. **Tool input schema**：`tools/list` 暴露的每个工具的 JSON Schema，源文件
   为 `core/tool/schema/*.json`（procedure 自定义工具的 schema 由 entity
   参数派生，遵循相同规则）；
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
     `resultSummary`、`cost`、`code`、`error`）为空时省略；
   - `cost` 的内部结构暂不属于定版契约，消费方不得依赖其字段名。

## 兼容 vs 破坏性变化

**兼容变化**（可在任意 patch/minor 版本发布，CHANGELOG 记录即可）：

- tool schema：新增可选输入字段、放宽枚举、补充 `description`；
- Denial：新增可选字段、新增 `code` 取值、扩充 `constraints`/`hints` 内容；
- export：新增字段（带默认值）导致的输出差异；
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
