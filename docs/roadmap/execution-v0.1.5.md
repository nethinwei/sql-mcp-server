# v0.1.5 落地计划

状态：**Execution Plan**（对应承诺范围见
[v0.1.5 — Contract + Product Proof](committed-v0.1.5.md)）

返回[主路线图](../roadmap.md)。

本文件把 v0.1.5 的承诺范围拆解为七个工作包，给出依赖顺序、交付物和每包的
验证方式。承诺范围、退出标准和非目标以 committed 文档为准；本文件只回答
“按什么顺序做、做到什么程度算完成”。

---

## 现状基线

对照承诺范围，当前（v0.1.4 之后）的主要差距：

- MCP 错误出口（`x/mcpserver` 的 `toResult`）只输出纯文本 `IsError`；
  `rbac.Decision.Reason` 与 cost hints 在出口被丢弃；无
  `code`/`retryable`/`constraints`/`decision ID`；审计事件
  （`core/audit.Event`）无任何关联 ID；
- 现有 e2e（`x/mcpserver/e2e_test.go`）走 in-memory 传输，真实 streamable
  HTTP 路径只有中间件级单元测试与 release 链 quickstart smoke；
- CLI 无 `export` 子命令；tool schema 为 embed 静态 JSON
  （`core/tool/schema/*.json`），无 golden 快照与兼容规则检查；
- quickstart 六场景覆盖 4/6，缺“mask 可返回但不可过滤”与“Agent 按结构化
  错误收窄后成功”；quickstart smoke 仅在 release 链运行；
- 无证据索引页；四客户端只有配置模板、无核对记录；threat ID
  （TM-001~TM-008）到测试的映射只存在于 `threat-model.md` 的行文中；
- 运行时 snapshot/reload 已实现（`x/bootstrap/runtime.go`）；持久化
  revision 按非目标只做设计评审。

---

## 工作包

### WP1 机器可读拒绝契约 + decision ID

最优先；WP2 的错误断言和 WP6 的第六场景都依赖它。

- 在 core 定义结构化拒绝类型，包含 `code`、`reason`、`retryable`、
  `constraints`、`hints` 和 `decision ID`；为现有 sentinel error
  （`ErrUnauthorized` 等）、`cost.ExceededError`、`budget.ErrExceeded`
  分配稳定机器码；
- 贯通链路：`rbac.Decision.Reason` 与 cost hints 不再在工具层丢弃；
  `toResult` 输出结构化 JSON（`StructuredContent` + 文本兜底）；
- 每次工具调用生成 decision ID，写入 MCP 响应、审计事件新字段和
  OTel span 属性；
- 修复建议（hints）只能收紧或等价改写请求，不得扩大权限或放松约束。

验证：错误解析稳定性 golden 测试；审计 JSONL 含 decision ID；e2e 断言
MCP 响应、审计与 trace 三方 decision ID 一致。

### WP2 真实 streamable HTTP /mcp e2e

依赖 WP1（拒绝断言使用结构化字段）。

- 新增 `e2e` build tag 测试，启动真实 HTTP listener + bearer token，
  覆盖：认证失败、subject/tenant header、allow/deny、mask、row policy、
  执行前成本拒绝和显式事务；
- 与 in-memory PostgreSQL e2e 共享断言，证明关键授权路径等价。

验证：`make test-e2e` 同时跑 in-memory 与 HTTP 两条传输路径且结论一致。

### WP3 tool schema / 错误 / export 兼容规则

- 文档定义兼容与 breaking 变化规则（新增可选字段=兼容；删除字段、改变
  语义=breaking）；
- 为 `core/tool/schema/*.json` 与错误码表建立 golden 快照和 CI 机器检查；
  breaking 变化必须显式更新快照并在 CHANGELOG 标注。

验证：CI 在 schema 或错误码未声明变更时失败；规则文档评审通过。

### WP4 确定性 YAML export

与 WP1–WP3 无依赖，可并行。

- 新增 CLI `export` 子命令：固定字段顺序、显式化默认值策略、secret 统一
  表示（保留 `${ENV}`/`${file:...}` 引用，不落明文）。

验证：同一有效配置重复 export 字节级一致的 golden 测试；
`export → validate` 往返通过。

### WP5 revision/snapshot 设计评审

仅文档，不实现持久化（committed 非目标）。

- 产出设计结论：revision 数据模型、snapshot 兼容性、控制面不可用时的
  拒绝语义和在途请求一致性；
- 归档到 `docs/roadmap/` 或 `docs/design/`，作为 Next 3 控制面的前置结论。

验证：设计评审通过并记录结论；不引入任何运行时行为变化。

### WP6 六场景 Demo + 客户端入口 + 证据索引

依赖 WP1（第六场景）与 WP3（对外契约表述）。

- quickstart 补两场景：mask 字段可返回但用于 filter 被拒；Agent 依据
  结构化错误收窄请求后成功；
- `internal/quickstartsmoke` 同步扩展为六场景自动验证；
- 四客户端（MCP Inspector、Claude Desktop、Cursor、VS Code）逐一核对
  模板并记录核对结论；
- 新增一页架构与安全边界说明，以及汇总签名、SBOM、Registry、Provider
  兼容矩阵和威胁账本链接的证据索引页。

验证：新用户按文档五分钟完成六场景；quickstart smoke 覆盖全部六场景。

### WP7 critical/high threat ID → 测试可追溯映射

与其他工作包无依赖，可并行。

- 为 TM-001~TM-008 建立结构化映射（测试代码 TM-ID 标注或映射清单文件）；
- CI 校验每个 critical/high threat 均有测试证据或明确标注的证据缺口。

验证：映射机器可检查；`threat-model.md` 的证据段落与映射一致。

---

## 依赖与建议顺序

- 先行：WP1（契约是 WP2、WP6 的前置）；
- 并行组 A：WP3、WP4、WP5、WP7（互不依赖，可与 WP1 并行）；
- 收尾：WP2（依赖 WP1）→ WP6（依赖 WP1、WP3，组合成产品证明）。

未完成 WP1 前不对外表述结构化错误契约；未完成 WP6 前不宣称五分钟
产品证明。

---

## 验收核对

对齐 [committed-v0.1.5.md](committed-v0.1.5.md) 的退出标准：

| 退出标准 | 对应工作包 |
| --- | --- |
| 拒绝响应可稳定解析全部约定字段，hints 只收紧 | WP1 |
| HTTP e2e 与 PostgreSQL MCP e2e 关键授权路径一致 | WP2 |
| tool contract 兼容/破坏性变化机器可检查 | WP3 |
| 同一配置重复 export 确定性 | WP4 |
| snapshot 拒绝语义与在途一致性有评审结论 | WP5 |
| 新用户五分钟完成六场景 | WP6 |
| 每个 critical/high threat 可追溯到测试或证据缺口 | WP7 |
