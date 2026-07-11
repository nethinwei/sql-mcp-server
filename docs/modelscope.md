# 魔搭 ModelScope MCP 广场上架与使用

SQL MCP Server 在魔搭选择**仅本地可用/分发展示**。它需要连接用户自己的数据库，
并由管理员定义 entity、字段 ACL、行策略和成本预算，不适合作为共享 Hosted 服务。

## 上架资料

- 中文名称：`SQL MCP Server（受控 SQL 数据访问网关）`
- 英文名称：`SQL MCP Server`
- 类型：`STDIO`
- 部署方式：`仅本地可用/分发展示`
- 分类：`数据库`、`开发者工具`、`数据分析`
- 来源：`https://github.com/nethinwei/sql-mcp-server`
- License：`MIT`
- 中文简介：`面向不可信 AI Agent 的受控 SQL 数据访问网关，通过实体模型、字段与
  行级授权、脱敏、成本闸门和预算限制安全访问 PostgreSQL、MySQL 与 OceanBase。`
- English description: `A controlled SQL data access gateway for untrusted AI
  agents with entity allowlists, field and row policies, masking, cost gates,
  and bounded execution.`

正式提交应指向[当前 GA Release](releases/README.md)，不长期使用 RC 镜像或
二进制。

## 前置配置

1. 从[发布说明索引](releases/README.md)下载当前 GA 对应平台的二进制，将
   `sql-mcp-server` 放入 `PATH`。
2. 复制 [`examples/modelscope/config.yaml`](../examples/modelscope/config.yaml)，
   按真实数据库修改 entity、field ACL、row policy、mask 和预算。
3. 使用数据库低权限账号设置 `DATABASE_DSN`。不要在 Git 仓库或魔搭页面保存真实
   DSN、密码、token 或 TLS 私钥。
4. 先执行：

```sh
sql-mcp-server validate --config /absolute/path/to/config.yaml
```

根目录 [`mcp_config.json`](../mcp_config.json) 是提交到魔搭的标准配置。使用前必须
把 `/absolute/path/to/config.yaml` 改成实际绝对路径，并替换示例 DSN。

```json
{
  "mcpServers": {
    "sql-mcp-server": {
      "command": "sql-mcp-server",
      "args": [
        "serve",
        "--config",
        "/absolute/path/to/config.yaml",
        "--transport",
        "stdio",
        "--role",
        "reader"
      ],
      "env": {
        "DATABASE_DSN": "postgres://reader:password@127.0.0.1:5432/app?sslmode=require"
      }
    }
  }
}
```

魔搭或 MCP 客户端不会保证替换 `args` 中的环境变量，因此配置路径使用明确占位符，
不宣称无提示一键安装。

## Docker 方式

[`examples/modelscope/docker-mcp-config.json`](../examples/modelscope/docker-mcp-config.json)
使用 GA 镜像和 stdio。提交前必须修改 bind mount 的宿主机绝对路径；Linux 数据库
地址还需按 Docker 网络环境调整，不能机械使用 `host.docker.internal`。

Docker 方式仍需要管理员提供 YAML。只有 DSN 无法安全推断可暴露表、字段、租户策略
和预算，因此项目不会自动暴露数据库 schema。

## 验证

授权读取、mask、tenant 隔离和拒绝路径的 Inspector 请求统一见
[五分钟快速体验](quickstart.md)，本文件不重复维护演示 payload。

维护者提交魔搭前运行：

```sh
make modelscope-check
```

该门禁读取根目录 manifest，启动真实 PostgreSQL 和 stdio 子进程，并验证工具发现、
授权读取、mask、固定 tenant 行策略、全表读取拒绝和隐藏字段拒绝。

## 安全与支持

- stdio 的 role 在进程启动时固定，没有 per-request subject；动态 tenant identity
  应使用可信 HTTP 代理方案，不能由客户端自行伪造 header。
- 行策略是应用层保护，数据库账号仍必须遵循最小权限。
- 服务不接受原始 SQL，不支持 DDL。
- 完整信任边界见 [安全模型](security.md)，问题通过
  [GitHub Issues](https://github.com/nethinwei/sql-mcp-server/issues) 报告，漏洞按
  [SECURITY.md](../SECURITY.md) 私下报告。

ModelScope API Token 仅用于访问魔搭 API，不能替代 MCP 广场网页的创建和审核流程。
