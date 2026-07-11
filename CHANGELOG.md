# Changelog

本项目的重要变更记录于此。格式参考
[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本遵循
[Semantic Versioning](https://semver.org/lang/zh-CN/)。

CHANGELOG 只维护版本级摘要和 breaking 提示；完整能力、迁移步骤、证据边界与
版本时点限制在对应 `docs/releases/vX.Y.Z.md` 中维护。

## Unreleased

## 0.1.6 - 2026-07-12

### Added

- 健康分离：`/healthz` 保留为 liveness，新增 `/readyz/snapshot`（配置快照
  可用）与 `/readyz/db`（数据库可达）readiness 端点，探针缺失或失败一律
  503（fail closed），响应体不回显失败细节。
- 最小可观测：HTTP transport 在 `/metrics` 暴露
  `sql_mcp_tool_calls_total{tool,outcome}`、按 tool 的时长直方图和
  `sql_mcp_audit_dropped_total`（Prometheus 文本格式，token 保护）；`serve`
  改为 stderr JSON 结构化日志，工具失败日志携带 `decisionId` 与 `outcome`；
  设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 后初始化 OTLP HTTP exporter，使既有
  hook 产生真实 span。
- `hook.Join` 组合多组生命周期 hook（tracing、metrics、logging 共存）。
- 协议 smoke（`make smoke-protocol`）进入 PR/主分支 CI：stdio 与
  streamable HTTP 各验证 initialize、tools/list、allow、机器可读 deny，
  HTTP 另验证健康/就绪/metrics 端点。
- 可复现 data-plane overhead benchmark（`make bench-overhead`）：固定
  fixture 下对比直连查询与完整治理路径的 p50/p95/p99，方法与样例见
  [`docs/benchmarks/data-plane-overhead.md`](docs/benchmarks/data-plane-overhead.md)。
- Agent Eval pilot 框架（`make eval-pilot`）：24 个固定任务（任务集 v2）、
  确定性 fixture、机械评分、并行执行与完整 ReAct transcript 记录，经
  OpenAI 兼容端点驱动（见 [`eval/README.md`](eval/README.md)）；三轮正式
  运行的书面结论（对语义元数据阶段 no-go）存于
  [`eval/results/`](eval/results/)。

### Changed

- 审计事件 JSON Lines schema 定版：字段改为固定 camelCase json tag
  （`time`、`decisionId`、`role`、`entity`、`action`、`tool`、`input`、
  `resultSummary`、`cost`、`allowed`、`code`、`error`、`returnedRows`、
  `durationMs`）；新增稳定拒绝码 `code` 字段；主路径补记 `entity` 与
  `action`；`durationMs` 为整数毫秒。此前输出为未定版的 Go 字段名
  （`Time`、`Tool` 等），消费该格式的脚本需按
  [`docs/tool-contract.md`](docs/tool-contract.md) 的定版 schema 迁移。

详见 [`docs/releases/v0.1.6.md`](docs/releases/v0.1.6.md)。

## 0.1.5 - 2026-07-12

### Added

- 机器可读拒绝契约：业务拒绝在 `structuredContent` 携带稳定 `code`、
  `reason`、`retryable`、`constraints`、`hints` 和 `decisionId`；decision ID
  贯穿 MCP 响应、审计事件与 trace span。兼容规则见
  [`docs/tool-contract.md`](docs/tool-contract.md)，契约由 golden 快照在 CI
  机器检查。
- 真实 streamable HTTP `/mcp` e2e：认证、身份 header、allow/deny、mask、
  row policy、成本拒绝与事务，与 in-memory e2e 共享断言以证明传输等价。
- CLI `export` 子命令：确定性 YAML 导出（固定字段顺序、物化默认值、secret
  占位符原样保留）。
- quickstart 六场景 Demo（新增 mask 不可过滤、按结构化错误收窄重试）及对应
  smoke 自动验证；客户端接入核对（`docs/clients.md`）与证据索引
  （`docs/evidence.md`）。
- critical/high threat ID 到回归测试的机器可检查映射
  （`internal/threatcheck`）。

### Changed

- 预算拒绝（`budget exceeded`）从协议层内部错误改为业务级 `IsError` 结果，
  携带 `BUDGET_EXCEEDED` 拒绝契约。
- RBAC 拒绝原因不再在工具层丢弃：详细原因写入审计事件；客户端可见的
  `UNAUTHORIZED` reason 统一泛化，防止受限角色枚举隐藏实体/字段（TM-002）。
- 审计事件新增 `DecisionID` 字段（JSON Lines 兼容追加）。
- 工具生命周期 hook 现在在预算获取前触发 `BeforeTool`、在 span 结束前记录
  错误，使预算拒绝同样可通过 trace 中的 `decision.id` 定位。

详见 [`docs/releases/v0.1.5.md`](docs/releases/v0.1.5.md)。

## 0.1.4 - 2026-07-12

### Added

- 威胁模型与证据账本，覆盖安全资产、信任边界、攻击者假设、critical/high threat ID、
  控制措施、验证证据、剩余风险和非保证范围。
- critical/high adversarial corpus、四个定向 fuzz target，以及 CI 中有界、无 Docker
  的四项 fuzz smoke。

### Security

- 将 MCP payload、IR validator、参数化 SQL codegen 和 transaction state machine
  的安全属性纳入确定性 seed 回放与持续 fuzz 验证。
- 明确 PostgreSQL、MySQL、OceanBase 的共享层、三库 integration 和未独立验证的证据
  边界，避免将核心层测试外推为三库端到端保证。

详见 [`docs/releases/v0.1.4.md`](docs/releases/v0.1.4.md)。

## 0.1.3 - 2026-07-11

### Added

- GoReleaser tag workflow，发布 6 个平台归档、SHA-256 checksum、归档 SBOM 和
  keyless Cosign 签名。
- GHCR linux/amd64 与 linux/arm64 镜像、镜像签名和镜像 SBOM。
- PostgreSQL Docker Compose quickstart，覆盖授权读取、tenant 隔离、脱敏、全表
  扫描和字段越权拒绝。
- MCP Registry `server.json`、官方 publisher CI 校验与 GitHub OIDC 发布流程。
- Provider 兼容矩阵、支持版本和 Cursor/Claude Desktop/VS Code 配置模板。
- 魔搭 ModelScope 本地分发展示 manifest、专用安全配置和真实 stdio smoke。

### Changed

- OceanBase integration 镜像固定到 4.3.5.6，避免 `latest` 漂移。

详见 [`docs/releases/v0.1.3.md`](docs/releases/v0.1.3.md)。

## 0.1.2 - 2026-07-11

### Added

- 业务包迁入 `core/`；YAML 解码迁至 `x/configyaml`；provider 通过
  `x/providerregistry` 可插拔注册。
- 成本链 Safety/Enforcement/Estimate 分层（`core/cost/layers.go`）。
- `internal/fmtcheck` 文件、函数与行宽限制；`make fmt` 集成 golines。
- procedure 独立结果上限 `maxProcedureRows`；expand 分批 IN；审计输入脱敏与
  transaction token 哈希。

### Security

- 修复 aggregate 未脱敏、mask 字段谓词/分组侧信道、数据库错误详情外泄、
  procedure rows 泄漏路径和 commit 失败未 rollback。
- bearer token 改为固定长度摘要恒时比较；角色统一小写规范化；动态 JSON 保留
  大整数精度。
- `${file:...}` secret 限制到允许根目录并阻止符号链接逃逸；扩充 DSN 脱敏。

### Changed

- 成本链拆分为不可关闭的 Safety/Enforcement 与可选 Estimate；
  `cost.enabled: false` 不再关闭写保护、CALL 审核、输入及结果上限。
- MySQL/OceanBase 使用保守 EXPLAIN 并在错误/未知/全扫时 fail closed；三种
  provider 同时装配数据库原生 statement timeout。
- procedure 默认拒绝，须设置 `mcp.trustedProcedure: true` 并命中 reviewed
  `allowTemplates`。
- cache、feedback、IN/filter/groupBy/aggregate/expand、预算 session 和响应字节
  均增加硬边界。
- 热重载改为 drain-before-publish；改变工具发现集合的 reload 要求重启。
- prepared statement 不再锁内执行网络 prepare；singleflight 传播 deadline；
  RPS 配置现已实际装配。
- 审计文件格式改为 JSON Lines。

### Breaking

- mask 字段不再允许用于 filter、cursor、group-by、aggregate 或写谓词。
- `maxScannedRows` 被 `maxEstimatedScannedRows` 取代（旧字段暂作 deprecated
  alias）；零值不再能产生无界缓存或 mandatory cost limit。
- 角色在配置与请求入口统一 trim 并转为小写，规范化碰撞会拒绝启动。
- Go import 路径由顶层包改为 `core/<pkg>`（例如 `core/config`）。

详见 [`docs/releases/v0.1.2.md`](docs/releases/v0.1.2.md)。

## 0.1.1 - 2026-07-11

### Changed

- 将 PostgreSQL、MySQL、OceanBase 方言实现从核心 `dialect` 包移至
  `x/providers/*`，核心仅保留 `Dialect` 接口与 `Capabilities` 声明。
- 配置 JSON Schema 与 MCP 工具 input schema 改为 `embed` 静态 JSON 文件，不再
  硬编码在 Go 源码中。
- 重组文档，使配置、安全边界、运行和测试分别拥有单一真相源。
- 扩充用于 YAML 编辑辅助的配置 JSON Schema，并统一配置字段的 lowerCamelCase
  名称；Schema 不作为标准 `encoding/json` 输入契约。
- Go 源码中的 `config.CostConfig.Enabled` 从 `bool` 改为 `*bool`，以区分“省略”
  与显式 `false`，从而保持默认开启的安全三态。程序化构造配置时请将
  `Enabled: true/false` 迁移为 `Enabled: config.Bool(true/false)`，读取有效值
  请使用 `EnabledOrDefault()`。
- 多数据源配置中的精确 SQL baseline 必须写成 `datasource:SQL`；裸 SQL 仅在
  单数据源配置中为兼容旧配置而继续接受。`fp:v2:` fingerprint 已包含数据源，
  不受影响。
- 热重载会原子更新预算限制并保留 session 用量；事务 `ttl`/`maxOpen` 变化因
  无法安全迁移在途事务而拒绝 reload，需重启生效。

详见 [`docs/releases/v0.1.1.md`](docs/releases/v0.1.1.md)。

## 0.1.0 - 2026-07-11

### Added

- PostgreSQL、MySQL、OceanBase provider。
- stdio/streamable HTTP MCP 和 HTTP token、TLS/mTLS。
- 实体 CRUD、procedure、aggregate 与显式事务工具。
- RBAC、字段 ACL、行级策略、mask、审计和成本控制。
- 多数据源、关系展开、分页、prepared cache、预算与热重载。
- 授权 schema resource、安全 prompts、CLI 和分层测试。

完整能力与限制见 [`docs/releases/v0.1.0.md`](docs/releases/v0.1.0.md)。
