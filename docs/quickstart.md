# 5 分钟快速体验

本文件是 quickstart、MCP Inspector 请求和 allow/deny 演示的唯一完整出处。

该示例启动 PostgreSQL 16 和已发布的 SQL MCP Server 镜像，演示低权限读取、
字段脱敏、tenant 行隔离及拒绝路径。只需要 Docker、Docker Compose 和一个 MCP
客户端。

## 启动

在仓库根目录执行：

```sh
docker compose -f examples/quickstart/compose.yaml up -d --wait
curl -fsS http://127.0.0.1:8080/healthz
```

服务只发布到本机 `127.0.0.1:8080`。示例 bearer token 为
`quickstart-only-token`，不得用于其他环境。

## 连接 MCP Inspector

```sh
npx -y @modelcontextprotocol/inspector http://127.0.0.1:8080/mcp
```

在 Inspector 中为连接设置以下 header：

```text
Authorization: Bearer quickstart-only-token
X-MCP-Role: reader
X-MCP-Subject: {"tenant_id":7}
```

调用 `read_records`：

```json
{
  "entity": "users",
  "filter": [{"field": "id", "op": "eq", "value": 1}]
}
```

应返回 Alice，且 `email` 已脱敏。把 `id` 改为 `2` 时返回空结果，因为该行属于
tenant 8。

再执行两个拒绝示例：

```json
{"entity": "users"}
```

该全表读取会被成本闸门拒绝。

```json
{
  "entity": "users",
  "fields": ["tenant_id"],
  "filter": [{"field": "id", "op": "eq", "value": 1}]
}
```

该调用会被字段 ACL 拒绝。`tools/list` 中也不会出现默认关闭的
`delete_record`。

维护者可用 Go SDK 一次性验证全部路径：

```sh
examples/quickstart/smoke.sh
```

## 连接桌面客户端

[`examples/clients/`](../examples/clients/) 提供 Cursor、Claude Desktop 和 VS Code
的 stdio 配置模板。把二进制和配置路径改为绝对路径后复制到客户端配置中。

stdio 身份在进程启动时由 `--role` 固定，不能传递 per-request subject。因此，
上面的 `${subject.tenant_id}` 动态隔离示例必须通过 HTTP 和可信身份注入演示；
不要声称 stdio 模板具有同等 tenant 身份语义。

## 安全边界

quickstart 为便于本机演示，信任固定 Docker 子网中的
`X-MCP-Role`/`X-MCP-Subject`。这不是生产认证方案。生产环境必须由可信代理完成
调用者认证、删除外部同名 header 后重新注入身份，并按
[安全模型](security.md) 配置 mTLS 或严格的代理 CIDR。

完成后清理：

```sh
docker compose -f examples/quickstart/compose.yaml down -v
```
