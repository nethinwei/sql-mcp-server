# SQL MCP Server

SQL MCP server，让 AI agent 通过受控契约安全访问关系数据库。参考微软
Data API Builder（DAB）的 SQL MCP Server，支持 PostgreSQL、MySQL、OceanBase。

## 特性

- **关系代数 IR**：查询经方言无关的中间表示渲染为参数化 SQL，杜绝注入；接入新库只需实现 5 个窄接口。
- **成本门限**：多层级联闸门（defense in depth）——EXPLAIN 仅作可选预筛层，确定性 LIMIT 兜底，按数据库能力差异化装配；超限硬拒绝并返回重写建议。
- **安全**：RBAC + 行级安全 + 字段脱敏 + 异步审计。
- **接口化工具**：七个 DML 工具（describe/read/create/update/delete/execute/aggregate）按配置开关启用，关闭即不注册。
- **有界并发**：bounded worker pool + 背压 + singleflight + 自适应限流 + 熔断。
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
- `cost`：`softScore`/`hardScore`（0–100 归一化阈值）、`maxRows`（EnforceCap 强制 LIMIT）、`rejectFullScan`、`whitelistPKPoint`。
- `entities`：暴露的表/视图/存储过程，含别名、主键、字段投影（`exclude`）、脱敏（`mask`）、角色权限、行级策略（`rowPolicies`）。

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
make test-integration  # testcontainers 集成测试（需 docker）
make lint              # golangci-lint + depguard 边界强制
make ci                # 全套
```

编码规范见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。设计与路线图见 [`docs/roadmap.md`](docs/roadmap.md)。

## 状态

核心与三库 provider 已实现，PostgreSQL 端到端（含成本门限真实行为）已通过 testcontainers 集成测试验证。进行中的工作见 `docs/roadmap.md` 的里程碑与 P1/P2 路线图。

## 许可

MIT（见 LICENSE，待添加）。
