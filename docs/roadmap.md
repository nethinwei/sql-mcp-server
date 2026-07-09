# SQL MCP Server — 设计与路线图

## 目标

一款 SQL MCP server，参考微软 Data API Builder（DAB）的 SQL MCP Server，并在其能力之上新增两个核心特性：

1. **成本门限**：读/写查询执行前估算成本，超阈值时硬拒绝并返回结构化错误（含估算值、阈值、重写建议），引导 agent 自行重写后重试。
2. **接口化能力 + 配置开关**：每个 DML 能力是实现窄接口的组件，通过配置按工具开关（关闭 = 不注册，agent 不可见）。

支持 PostgreSQL、MySQL、OceanBase。MCP 协议层基于官方 `modelcontextprotocol/go-sdk`（stdio + streamable HTTP）。

## 技术决策

- **MCP 协议层**：官方 `github.com/modelcontextprotocol/go-sdk`，仅作传输适配层。
- **目标数据库**：PostgreSQL + MySQL + OceanBase（OceanBase MySQL 协议兼容，复用 mysql driver）。
- **成本门限语义**：硬拒绝 + 引导重写（升级为软/硬双阈值，见成本闸门设计）。
- **Go 基线**：go 1.25（go-sdk 要求；享用 1.23–1.25 特性）。

## 架构边界（machine-enforced）

只有 `x/mcpserver` 接触 go-sdk 类型，所有业务逻辑为核心包、不依赖 go-sdk。核心可独立测试、可复用到非 MCP 场景，隔离 go-sdk 版本升级影响。该边界用 `depguard` linter 强制（`.golangci.yml`），不靠人工自觉。

```text
sql-mcp-server/
├── doc.go / go.mod / Makefile / README.md / CONTRIBUTING.md
├── cmd/sql-mcp-server/main.go     # 应用入口（flag、装配、运行）
│  ╭── 核心包（零外部依赖：仅标准库 + 互相依赖，depguard 强制）──╮
├── config/        # 配置模型 + 校验 + ApplyDefaults + Schema
├── relalg/        # 关系代数 IR（Scan/Select/Project/Aggregate/Sort/Limit/Distinct/Values/Insert/Update/Delete + Predicate）
├── codegen/       # IR → 方言 SQL（按 Capabilities 降级；内联 IsPKPoint 判定与常量折叠）
├── entity/        # 实体抽象（Relation/Attribute/Domain/Key/FK/Registry/Relationship）
├── dialect/       # 方言 + Capabilities 协商
├── store/         # DB/Rows/Result/Tx/Canceler（*sql.DB 可满足）
├── rbac/          # Authorizer + RoleAuthorizer + 行级安全
├── mask/          # 字段脱敏接口 + 规则
├── cost/          # 成本门限：Plan/Score/Explainer/Gate/CostExceededError/hints
├── audit/         # 审计接口（best-effort，非阻塞）
├── tool/          # DML 工具组件 + Registry + Enabled 开关
├── cache/         # 读缓存接口 + 表级精确失效 + plan cache TTL
├── hook/          # 生命周期回调（nil-safe）
├── ratelimit/     # 自适应限流（AIMD）+ 熔断
├── engine/        # bounded worker pool + 背压 + singleflight + 优雅 drain
├── introspect/    # schema 自省接口
│  ╰────────────────────────────────────────────────────────╯
│  ╭── x/：外部依赖适配层（与核心物理分离）──╮
├── x/
│   ├── mcpserver/     # 引 go-sdk：core Tool → mcp.Tool/ToolHandler；stdio/http；错误码映射
│   ├── providers/
│   │   ├── postgres/  # 引 pgx
│   │   ├── mysql/     # 引 go-sql-driver（含可复用 Adapter + Introspector）
│   │   └── oceanbase/ # 引 go-sql-driver
│   ├── otel/          # 引 otel：span/metrics 适配（TODO）
│   └── bootstrap/     # 引 yaml.v3：配置加载 + introspect 校验 + 装配
└── examples/config.example.yaml
```

核心包零外部依赖：不 import 任何非标准库（含不 import `x/`）。`database/sql` 是标准库，`store.DB` 由 `*sql.DB` 满足；driver 注册只发生在 `x/providers/*`。`x/` 依赖核心，核心永不依赖 `x/`——依赖单向。

## 核心设计

### 关系模型抽象层（数学完备性）

核心以**关系代数中间表示（IR）**为方言无关的枢纽：DML 工具构造 `relalg.Expr`（逻辑计划）→ `codegen` 按 dialect 渲染成 SQL（内联轻量变换）→ `Explainer` 取物理计划 → `ScorePlan` 评分 → 执行。

依据 Codd 关系模型：
- **完备性**：只读算子 σ(Select)+π(Project)+γ(Aggregate)+τ(Sort)+Limit+δ(Distinct) 对"安全只读查询"关系完备；写操作以关系赋值（Insert/Update/Delete over predicate）表达；不做 DDL；有意排除 NL2SQL/任意 SQL——表达力边界即确定性 + 可门限 + 防注入。Join/Union 留待 P1。
- **逻辑/物理分离**：IR=逻辑计划，EXPLAIN 结果=物理计划，成本评分作用于物理计划。
- **接入契约**：新增关系库 = 实现 5 个窄接口（`dialect.Dialect`+`Capabilities` / codegen 能力降级点 / `store.DB`+`Tx`+`Canceler` / `cost.Explainer` / `introspect.Introspector`），核心与 IR 零改动。
- **能力协商**：`Dialect.Capabilities()` 声明 RETURNING/EXPLAIN cost/Savepoint 等，codegen 按能力降级。
- **类型与域**：字段附 `Domain`（类型+约束），IR 构造期校验值类型与域，早失败 + 防注入。
- **事务语义**：ACID 是关系模型不可分割部分，提供 `store.Tx` 抽象（隔离级别/保存点）。

### 成本与资源闸门（defense in depth）

EXPLAIN 估算有根本缺陷（不可靠、跨方言不可比、SQLite 无数值），不能把门限全押在它上。本设计采用**多层级联闸门**：EXPLAIN 仅作"可选预筛层"，按各 DB 能力差异化装配；确定性 LIMIT/timeout 兜底；DB 原生资源治理作底层硬保护；反馈学习让阈值随时间自适应。任一层拦截即安全，单层失效不致命。

层级（按序）：
1. **StaticRule**（免 EXPLAIN）：白名单（PK 点查 `IsPKPoint`）、模板基线（`allowTemplates` 放行 / `rejectTemplates` 拒绝）。
2. **WriteGuard**（确定性写保护，免 EXPLAIN）：`requirePKForWrite`（默认开）时非主键点写（UPDATE/DELETE）硬拒——不依赖估算，兜底 MySQL/OceanBase 等估算不可信的库。
3. **Estimate**（EXPLAIN，可选预筛）：仅当 `ExplainAccurate` 且统计可信时启用；计划归一化为 0–100 安全分（越高越安全），低于 `hardScore` 硬拒、`[hardScore, softScore)` 软拒；不可信时跳过交下层兜底。
4. **EnforceCap**（确定性兜底）：读查询注入强制 LIMIT 包裹，保证最坏 N 行；不依赖估算正确性。
5. **RuntimeGuard**（运行时中断）：请求 `queryTimeout` 经 context 下推，由 driver 取消查询。
6. **DBNative**（底层委托）：MySQL/OceanBase 连接注入 `sql_safe_updates`，由 DB 拒绝无键无 LIMIT 的全表写。

按 DB 差异化装配：
- **PostgreSQL**：Estimate 启用（可信）+ EnforceCap + `statement_timeout`。
- **MySQL**：Estimate 弱化（偏差大，仅软提示）+ EnforceCap 强制 LIMIT + `max_execution_time` + `sql_safe_updates`。
- **OceanBase**：Estimate 弱化 + EnforceCap + `ob_query_timeout` + `max_read_size`（扫描行数硬上限）+ tenant 资源隔离。
- **SQLite**：完全跳过 Estimate + EnforceCap + 应用层行数闸。

EXPLAIN 失败降级为 `Plan{ScanUnknown, !StatsFresh}`，由 `RequireKnownScan`/`RequireFreshStats` 决定 fail-close/open（不 panic）。

### 安全与资源治理

- **行级安全（RLS）**：`Authorizer` 注入角色行级过滤 Predicate，与请求 Predicate AND 叠加；策略值支持 `${subject.x}` 占位符，按请求主体属性解析（属性缺失即解析为 NULL，fail-closed）。过滤/写字段经字段级校验，杜绝引用隐藏列的侧信道。
- **审计**：`Auditor.Record` 异步投递有界队列 + 后台 flusher，满则丢弃计数，绝不阻塞主业务。
- **脱敏**：`Masker` 字段级 mask（email/phone/idcard/secret），类型无关（数字型敏感值也脱敏）；配置的规则在启动期校验，未知规则 fail-fast。
- **资源治理**：statement timeout（下推 driver）、连接池（≤ IO 池上限）、查询取消下推（`pg_cancel_backend`/`KILL QUERY`）、限流/并发/熔断由 engine 承载。
- **凭证管理**：DSN 支持 `${ENV}`/`${file:}` 占位符，缺失 fail-fast。

### 服务架构（Go 并发）

`engine` 包：bounded worker pool（IO/CPU 分离）、有界背压队列（Little 定律）、singleflight（防缓存击穿，含 panic recover）、自适应并发（AIMD）、优雅 drain。使用 Go 特性：`semaphore`（chan）/`singleflight`（自实现）/`errgroup`/`sync.Pool`/`context.WithCancelCause`/`atomic`/`sync.Map`/pprof/race detector。核心零外部依赖，故 singleflight/semaphore 用标准库自实现。

### Go 特性、性能与 GC

泛型减 `any`、`iter.Seq2` 流式结果集、`weak.Pointer` 软缓存（TODO）、`unique.Make` 驻留、`sync.Pool` 对象复用、atomic 无锁热路径、容器感知（GOMAXPROCS/GOMEMLIMIT）、PGO、GC 友好设计（值类型/预分配/避免逃逸）、`testing/synctest` 时序测试。

## 核心不变量（形式化规约，CI 与属性测试须验证）

- **I1** 核心包 import 仅标准库与核心。
- **I2** 依赖单向：`x/ -> core`。
- **I3** 所有生成 SQL 参数化用户值，无字符串拼接。
- **I4** CostGated 工具执行前必过 `Gate.Check`（EnforceCap 可改写注入 LIMIT）。
- **I5** `Gate.Check` 在 `Authorizer.Authorize` 允许之后。
- **I6** 返回字段 ∈ `Decision.Fields`。
- **I7** 最终 `Predicate = user_predicate AND role_row_filter`。
- **I8** 工具注册 ⟺ `Enabled(flags) && entity.MCP.DMLTools`。
- **I9** `Engine`/`Registry` 无每请求可变状态，并发复用安全。
- **I10** 每个 `Rows`/`Tx` 必有 `Close`/`Commit`/`Rollback` 收尾。
- **I11** context 取消 ⇒ DB 查询取消 + goroutine 回收。
- **I12** `Auditor.Record` 不阻塞主流程。
- **I13** `in-flight > 上限 ⇒ ErrOverloaded`，无 goroutine 堆积。
- **I14** 写操作（UPDATE/DELETE）非 PK 点查时，`requirePKForWrite` 下必被 WriteGuard 拒。
- **I15** filter/groupBy/set/values 字段 ∈ 实体可见属性（隐藏列不可作谓词或写目标）。

## 编码规范

见 `CONTRIBUTING.md`。要点：`gofmt` 唯一标准；构造函数 `NewX(必参, opts ...Option) (T, error)` 不 panic 不 MustNew；哨兵错误 + struct + `Unwrap`；窄接口（1–2 方法）；扁平 discriminated union（sealed `Expr`/`Predicate`）；手写 fake 测试（仅标准库 `testing`，禁 testify/mockgen）；无 CLI/DI 框架；可观测走 hook；单函数 ≤50 行、单文件 ≤800 行；Conventional Commits；不自动提交需人工确认。

## 测试与 CI

- **分层**：单元（核心包，手写 fake，无 docker）→ 集成（`x/providers`，testcontainers 真实 PG/MySQL/OceanBase，`//go:build integration`）→ e2e（真实 DB + MCP client，`//go:build e2e`，TODO）→ 属性测试（不变量 I1–I13）→ 混沌测试。
- **CI**（GitHub Actions）：lint（golangci-lint + depguard）/ unit（矩阵 go 1.25+stable，`-race`）/ coverage（≥85%）/ integration（testcontainers postgres）/ govulncheck。`main` 受保护，Conventional Commits。
- 本地 `Makefile`：`make test` / `test-integration` / `test-e2e` / `lint` / `coverage` / `ci`。

## 实现里程碑

- ✅ **M1 核心抽象骨架**：relalg/codegen/config/entity/dialect/store/rbac/mask/cost/cache/hook/ratelimit/engine/audit/introspect/tool。全 fake，`-race` 全绿。
- ✅ **M2 DML 工具组件**：七工具 `New*`+`Enabled`+`Run`，CostGated。
- ✅ **M3 PostgreSQL provider + 集成测试**：testcontainers 起 PG，EXPLAIN 解析、门限端到端（全表扫描硬拒、PK 点查白名单通过）已验证。
- ✅ **M4 MySQL + OceanBase provider**：两种 EXPLAIN 解析（含 OceanBase 漂移降级）。
- ✅ **M6 MCP 适配 + 传输 + schema 自省/漂移**：`x/mcpserver` + cmd + `x/bootstrap` + introspect merge + 漂移检测 + 错误码映射。
- ✅ **M5 安全与资源治理**：RLS/审计/脱敏/限流/熔断/取消下推/凭证占位符/连接池/查询超时/schema 漂移全实现，三库集成测试验证。
- ✅ **M7 可观测 + 架构硬化 + CI**：CI（lint/unit/coverage≥80% 核心/integration/e2e/govulncheck）；OTEL span、异步审计、有界并发（背压/AIMD/熔断/singleflight）经 `tool.RunTool` 统一接入每次工具调用；`goleak` 在 e2e 校验无泄漏。PGO 待补。

## Roadmap

### P1
- 执行反馈闭环 / AQE：基础已落地（FeedbackStore + runRead 记录 + Estimate 校准读侧）；完整 AQE（异常突增拦截、plan cache 自动失效重算）待深化。
- 计划基线 / 查询模板白名单：已知良好模板免 EXPLAIN 放行，已知坏模板直接拒。
- 资源管理器：角色/租户级 CPU/并行度/执行时间配额。
- 关系展开：`Relationship` 嵌套查询（取 user 带出 orders）。
- 多数据源：跨库实体。
- MCP resources + prompts：schema 暴露为可订阅资源；安全查询 prompt 模板。
- 配置热重载。
- CLI 子命令：`init`/`add entity`/`validate`/`explain <sql>`。
- prepared statement 复用。
- 事务边界：显式 begin/commit/rollback 工具。
- EXPLAIN ANALYZE 采样校准。
- 会话/租户级成本预算。
- `weak.Pointer` 软缓存、`unique.Make` 驻留落地。

### P2
- 物化视图；bucketing/broadcast join 策略（若引入 JOIN）；动态 tool registration；HTTP OAuth/CORS/session 管理；Docker/Helm/operator；OpenAPI/GraphQL 同源多面；配置多文件 merge/include；文档站与交互式 playground；跨库行为一致性测试矩阵；MCP 官方 conformance 契约测试；MySQL/OceanBase testcontainers 集成测试。

## 关键文件

- `cost/cost.go` + `cost/layers.go` — 多层级联闸门（StaticRule/WriteGuard/Estimate/EnforceCap），EXPLAIN 仅作可选预筛层。
- `tool/tool.go` + `tool/tools.go` — 能力接口 + Registry + Enabled 开关 + 七 DML 工具。
- `relalg/relalg.go` + `codegen/codegen.go` — 关系代数 IR / 渲染（数学完备性基石）。
- `rbac/rbac.go` — RBAC + 行级安全 + 字段投影。
- `engine/engine.go` — bounded pool / 背压 / singleflight / 优雅 drain。
- `x/mcpserver/mcpserver.go` — core Tool → MCP Tool 桥接 + 错误码映射。
- `x/providers/postgres/explain.go` — PostgreSQL EXPLAIN 解析。

## 端到端验证

1. `make ci`：`make test`（单元 `-race`）+ `make test-integration`（testcontainers PG）+ `gofmt -l .` 无输出 + `go vet` + golangci-lint(depguard) + govulncheck 全绿；覆盖率 ≥85%（门禁待启用）。
2. 启动：`sql-mcp-server --config examples/config.example.yaml --transport stdio --role reader`，启动期 introspect 与配置 merge、漂移检测通过。
3. MCP Inspector 连 stdio 或 `http://localhost:8080/mcp`：
   - `describe_entities` 列实体/字段（敏感字段脱敏）。
   - `read_records` 全表扫描 → 硬拒绝 + 结构化 hints；PK 点查 → 跳过 EXPLAIN 正常返回；软阈值查询 → 软拒绝建议加 LIMIT。
   - `update_record` 全表 filter → 写门限硬拒绝 + `ErrUnsafeWrite`。
   - `reader` role 写操作被 RBAC 拒；行级过滤使 reader 只见本租户数据。
   - 并发压测：singleflight 去重；DB 慢时背压 `ErrOverloaded`；SIGTERM 优雅 drain 无泄漏。
   - `delete_record` 未注册（tools/list 不可见）。
