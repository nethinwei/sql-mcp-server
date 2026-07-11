# SQL MCP Server

SQL MCP Server 通过受控的实体、权限和成本契约，让 AI agent 访问
PostgreSQL、MySQL 或 OceanBase。它不接受任意 SQL，也不提供 DDL。

## 已实现能力

- stdio 与 streamable HTTP MCP 传输。
- 七个实体工具：描述、读取、新增、更新、删除、存储过程执行和聚合；另有显式事务的 begin/commit/rollback 工具。
- 参数化 SQL、RBAC、字段级读写限制、行级策略、字段脱敏和异步审计（默认关闭）。
- 成本闸门：模板基线、主键点查白名单、非主键写保护、PostgreSQL EXPLAIN 预筛、读取行数上限和查询超时。
- 命名数据源、同数据源关系展开、offset/keyset 分页、prepared statement 缓存和配置热重载。
- MCP 授权 schema resource，以及 `safe_read`、`safe_aggregate`、`rewrite_query` prompts。

准确的安全保证与 provider 差异见
[`docs/security.md`](docs/security.md)。MySQL/OceanBase 以保守、fail-closed
方式使用 EXPLAIN；仅 PostgreSQL 支持显式 opt-in 的只读
`EXPLAIN ANALYZE` 采样。每次命中采样都会额外执行一次生成的只读语句；
MySQL/OceanBase 配置启用该能力时会 fail-fast。

## 快速开始

要求 Go 1.25+ 和一个可连接的受支持数据库。

```sh
make build VERSION=v0.1.0
sql-mcp-server init --config config.yaml --driver postgres
```

编辑 `config.yaml`，通过 `${DATABASE_DSN}` 注入 DSN，然后校验并启动：

```sh
sql-mcp-server validate --config config.yaml
sql-mcp-server serve --config config.yaml --transport stdio --role reader
```

HTTP 仅建议先绑定 loopback：

```sh
sql-mcp-server serve --config config.yaml --transport http --addr 127.0.0.1:8080
npx -y @modelcontextprotocol/inspector http://127.0.0.1:8080/mcp
```

HTTP 的地址默认值是 `:8080`，它会监听所有接口，属于非 loopback；未通过
`--addr` 或配置改为 loopback 且未配置 bearer token/mTLS 时，服务会 fail
closed 并拒绝启动。完整示例见
[`examples/config.example.yaml`](examples/config.example.yaml)。

## 版本状态

发布构建通过 `make build VERSION=<version>` 将同一版本注入 CLI `version` 和
MCP `Implementation`；未注入的开发构建显示 `dev`。`v0.1.0` 功能基线已包含
核心工具、三种 provider、权限/成本控制、事务、多数据源、热重载和 MCP
resources/prompts，但仍有明确限制；详见
[`v0.1.0 发布说明`](docs/releases/v0.1.0.md)。未来工作只记录在
[`roadmap`](docs/roadmap.md)。

## 文档

- [架构](docs/architecture.md)
- [安全模型与边界](docs/security.md)
- [配置参考](docs/configuration.md)
- [运行与 CLI](docs/operations.md)
- [测试与 CI](docs/testing.md)
- [核心不变量](docs/invariants.md)
- [变更记录](CHANGELOG.md)
- [贡献指南](CONTRIBUTING.md)

## 许可

MIT，见 [LICENSE](LICENSE)。
