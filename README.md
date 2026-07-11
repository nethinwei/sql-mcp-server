# SQL MCP Server

面向 AI agent 的**受控 SQL 数据访问网关**。通过实体模型、关系代数 IR 与参数化
codegen 暴露 PostgreSQL、MySQL、OceanBase——**不接受任意 SQL，不提供 DDL**。

设计参考 Microsoft Data API Builder 的 MCP 思路，并在成本闸门、多租户预算与
fail-closed 安全边界上做了强化。详见 [架构](docs/architecture.md)。

## 为什么选择

| 诉求 | 做法 |
|------|------|
| 防止 agent 随意查库 | 仅暴露配置中的实体与字段；值参数化，标识符来自元数据 |
| 防止误删/全表扫 | 写保护、EXPLAIN 预筛、确定性行数/字节上限、查询超时 |
| 多团队/多角色 | RBAC、字段 ACL、行级策略、字段脱敏、按 session 预算 |
| 可运维 | 配置热重载、异步审计、OpenTelemetry hook、分层测试 |

安全保证与 provider 差异以 [安全模型](docs/security.md) 为准，**不以本页为准**。

## 功能概览

**MCP 面** — stdio 与 streamable HTTP；实体工具（describe / read / create /
update / delete / execute / aggregate）与显式事务（begin / commit / rollback）；
授权 schema resource；`safe_read`、`safe_aggregate`、`rewrite_query` prompts。

**治理** — 参数化 SQL；RBAC + 字段 ACL + 行级策略 + mask；不可关闭的
Safety/Enforcement 成本链；模板 fingerprint 与读取反馈；角色/租户进程内预算。

**数据** — 命名多数据源；同源关系展开；offset / keyset 分页；prepared
statement 缓存。

## 快速开始

**Docker（推荐）：** 只需 Docker 与 Docker Compose，即可启动 PostgreSQL 示例、
执行授权读取并观察全表读取/越权字段被拒绝：

```sh
docker compose -f examples/quickstart/compose.yaml up -d --wait
curl -fsS http://127.0.0.1:8080/healthz
```

完整的 Inspector 调用、tenant 隔离和拒绝示例见
[5 分钟快速体验](docs/quickstart.md)。

**源码构建：** 要求 Go 1.25+ 和一个
[已验证数据库版本](docs/supported-versions.md)。

```sh
git clone https://github.com/nethinwei/sql-mcp-server.git
cd sql-mcp-server
make build
```

初始化配置、注入 DSN、校验并启动（stdio，适合本机 MCP 客户端）：

```sh
sql-mcp-server init --config config.yaml --driver postgres
# 编辑 config.yaml，设置 dsn（可用 ${ENV} 占位符）
sql-mcp-server validate --config config.yaml
sql-mcp-server serve --config config.yaml --transport stdio --role reader
```

HTTP（开发时建议绑定 loopback）：

```sh
sql-mcp-server serve --config config.yaml --transport http --addr 127.0.0.1:8080
```

完整配置示例见 [`examples/config.example.yaml`](examples/config.example.yaml)。
运行细节、热重载与 secret 见 [运行与 CLI](docs/operations.md)。

> **注意：** 默认 HTTP 地址 `:8080` 监听所有接口。非 loopback 且未配置 bearer
> token 或 mTLS 时，服务会 **fail closed** 并拒绝启动。

### 连接 MCP 客户端

**stdio**（Cursor、Claude Desktop、VS Code）：在 MCP 配置中指定发布二进制路径
与 `serve` 参数；可复制 [`examples/clients/`](examples/clients/) 中的模板。

**HTTP**：端点为 `/mcp`；可用 MCP Inspector 探测：

```sh
npx -y @modelcontextprotocol/inspector http://127.0.0.1:8080/mcp
```

## 文档

| 主题 | 链接 |
|------|------|
| 配置 | [configuration.md](docs/configuration.md) |
| 安全边界 | [security.md](docs/security.md) · [SECURITY.md](SECURITY.md) |
| 运行与升级 | [operations.md](docs/operations.md) |
| 架构 | [architecture.md](docs/architecture.md) |
| 不变量 | [invariants.md](docs/invariants.md) |
| 测试与 CI | [testing.md](docs/testing.md) |
| 5 分钟体验 | [quickstart.md](docs/quickstart.md) |
| Provider 兼容性 | [provider-compatibility.md](docs/provider-compatibility.md) |
| 支持版本 | [supported-versions.md](docs/supported-versions.md) |
| 变更记录 | [CHANGELOG.md](CHANGELOG.md) |
| 发布说明 | [docs/releases/](docs/releases/) |
| 路线图 | [roadmap.md](docs/roadmap.md) |
| 参与贡献 | [CONTRIBUTING.md](CONTRIBUTING.md) |

版本与迁移说明见 [CHANGELOG](CHANGELOG.md) 及各 [发布说明](docs/releases/)；
未发布规划见 [roadmap](docs/roadmap.md)。

## 安全

如发现漏洞，请参阅 [SECURITY.md](SECURITY.md)。部署前请阅读
[安全模型](docs/security.md) 中的信任边界与已知限制。

## 许可

[MIT](LICENSE)
