# SQL MCP Server

SQL MCP server，让 AI agent 通过受控契约安全访问关系数据库。参考微软
Data API Builder（DAB）的 SQL MCP Server，支持 PostgreSQL、MySQL、OceanBase。

## 特性

- **关系代数 IR**：查询经方言无关的中间表示渲染为参数化 SQL，杜绝注入；接入新库只需实现 5 个窄接口。
- **成本门限**：多层级联闸门（defense in depth）——静态规则（PK 白名单/模板基线）→ 写主键保护（WriteGuard：非主键点写硬拒）→ EXPLAIN 预筛（仅可信方言启用）→ 确定性 LIMIT 兜底（EnforceCap），再叠加请求超时与 MySQL/OceanBase 原生 `sql_safe_updates`。计划质量归一化为 0–100 安全分（越高越安全），低于阈值软/硬拒并返回重写建议。支持计划基线（`allowTemplates`/`rejectTemplates`）与反馈闭环（记录实际行数校准估算）。
- **安全**：RBAC（字段级投影 + 过滤/写字段校验，杜绝隐藏列侧信道）+ 行级安全（`${subject.x}` 按请求主体动态过滤，属性缺失即 fail-closed）+ 字段脱敏（类型无关，未知规则启动即报错）+ 每次工具调用异步审计。
- **接口化工具**：七个 DML 工具（describe/read/create/update/delete/execute/aggregate）按配置开关启用，关闭即不注册；`execute_entity` 支持存储过程调用。
- **分页**：支持 offset 与 keyset（游标）分页，大表续页用 `WHERE pk > cursor ORDER BY pk LIMIT n` 避免 O(offset) 开销。
- **有界并发**：所有工具调用经统一编排（`tool.RunTool`）接入 bounded worker pool + 背压（`ErrOverloaded`）+ singleflight 读去重（共享结果）+ 自适应限流（AIMD）+ 熔断。
- **可观测**：每次工具调用发射 OpenTelemetry span/属性（经 hook 适配，核心不绑后端）+ 健康检查。
- **密钥管理**：DSN 支持 `${ENV}` / `${file:/path}` 占位符；`SecretResolver` 接口可注入 Vault 等外部 secret manager。
- **核心零外部依赖**：核心包仅用标准库，外部依赖（go-sdk、driver、yaml、otel）隔离于 `x/`，depguard 强制单向依赖。

## 安装

```sh
go build -o sql-mcp-server ./cmd/sql-mcp-server
```

## 快速开始

配置文件示例见 [`examples/config.example.yaml`](examples/config.example.yaml)。

stdio 模式（供 MCP 客户端通过子进程调用）：

```sh
sql-mcp-server --config config.yaml --transport stdio --role reader
```

HTTP 模式（streamable HTTP）：

```sh
sql-mcp-server --config config.yaml --transport http --addr :8080 --role reader
```

用 [MCP Inspector](https://github.com/modelcontextprotocol/inspector) 调试：

```sh
npx -y @modelcontextprotocol/inspector http://localhost:8080/mcp
```

## 配置要点

- `database.driver`：`postgres` | `mysql` | `oceanbase`；`dsn` 支持 `${ENV}` / `${file:/path}` 占位符，缺失即启动失败（fail-fast）。
- `tools`：按工具开关，`deleteRecord` 默认关闭。
- `cost`：`softScore`/`hardScore`（0–100 安全分阈值，越高越安全，需 `softScore ≥ hardScore`；低于 `hardScore` 硬拒，`[hardScore, softScore)` 软拒）、`maxRows`（EnforceCap 强制 LIMIT）、`rejectFullScan`、`whitelistPKPoint`、`requirePKForWrite`（默认开：非主键点写硬拒）、`allowTemplates`/`rejectTemplates`（计划基线）、`queryTimeout`（默认 30s）。
- `mask.enabled` / `rateLimit.enabled`：脱敏与限流可按需关闭（默认开）。
- `rateLimit`：`ioPool`/`cpuPool`/`maxInflight`（并发）、`breakerThreshold`/`breakerCooldown`（熔断）、`minConcurrency`/`rttThreshold`（AIMD）、`connMaxIdleTime`（连接空闲）。
- `audit.queueSize`、`cache.ttl`/`maxSize`。
- `entities`：暴露的表/视图/存储过程，含别名、主键、字段投影（`exclude`）、脱敏（`mask`）、角色权限、行级策略（`rowPolicies`）。
- 启动期 introspect 自动发现 schema 并与配置比对，配置引用不存在的实体/字段即 fail-fast。
- **角色/主体**：stdio 模式用启动 `--role`；HTTP 模式每请求角色与主体属性经 `X-MCP-Role`、`X-MCP-Subject`（JSON）请求头传入，须由可信网关在认证后设置（完整 OAuth 见 roadmap）。

## 架构

```text
核心包（零外部依赖）  →  x/ 适配层（go-sdk / driver / yaml / otel）
  relalg, codegen, entity, dialect, store, rbac, mask, cost,
  audit, tool, cache, hook, ratelimit, engine, introspect, config
```

只有 `x/mcpserver` 接触 go-sdk 类型；业务逻辑为核心包，可独立测试。完整设计与路线图见 [`docs/roadmap.md`](docs/roadmap.md)。

## 开发

```sh
make test              # 单元测试（-race）
make test-integration  # testcontainers 集成测试：PG/MySQL/OceanBase（需 docker）
make test-e2e          # 端到端：真实 DB + MCP 客户端 + goleak 泄漏检测
make lint              # golangci-lint + depguard 边界强制
make coverage          # 核心包覆盖率（≥80%）
make ci                # 全套
```

编码规范见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。设计与路线图见 [`docs/roadmap.md`](docs/roadmap.md)。

## 状态

核心与三库 provider 已实现并通过 testcontainers 集成测试（PG/MySQL/OceanBase）与 e2e MCP 客户端测试（含 goleak 泄漏检测）验证。核心包覆盖率 ≥80%，lint 0 issues。进行中的工作见 `docs/roadmap.md` 的里程碑与 P1/P2 路线图。

## 许可

MIT，见 [LICENSE](LICENSE)。
