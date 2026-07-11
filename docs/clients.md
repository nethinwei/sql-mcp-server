# 客户端接入核对

本文记录四个入口客户端的接入方式、核对方法与结论。演示步骤以
[quickstart.md](quickstart.md) 为唯一事实源；stdio 与 HTTP 的身份语义差异见
该文“连接桌面客户端”一节。

## 核对结论

| 客户端 | 传输 | 模板/入口 | 核对方式与结论 |
| --- | --- | --- | --- |
| MCP Inspector | streamable HTTP | quickstart“连接 MCP Inspector”一节 | quickstart 六场景手册路径；同一 endpoint/headers 由 `internal/quickstartsmoke` 在 release 链自动验证 |
| Cursor | stdio | [`examples/clients/cursor-mcp.json`](../examples/clients/cursor-mcp.json) | 模板参数与 CLI `serve --config --transport stdio --role` 逐项核对一致；stdio 协议路径由 ModelScope smoke（真实子进程 + PostgreSQL）自动验证 |
| Claude Desktop | stdio | [`examples/clients/claude-desktop-config.json`](../examples/clients/claude-desktop-config.json) | 同上；`mcpServers` 键结构符合 Claude Desktop 配置格式 |
| VS Code | stdio | [`examples/clients/vscode-mcp.json`](../examples/clients/vscode-mcp.json) | 同上；`servers` + `"type": "stdio"` 结构符合 VS Code MCP 配置格式 |

自动化证据：

- HTTP 路径：`go test -tags=e2e ./x/mcpserver`（真实 streamable HTTP listener
  + bearer + 身份 header）与 release 链的 quickstart smoke；
- stdio 路径：`make modelscope-check`（真实 stdio 子进程 + 真实 PostgreSQL，
  覆盖 mask、row policy、allow/deny）。

## 边界声明

- 三个桌面客户端的模板已与当前 CLI 参数和各客户端配置格式核对一致，stdio 与
  HTTP 协议路径均有自动化验证；但未在原生 macOS/Windows 桌面客户端 GUI 中
  执行人工 smoke，该项不作为安全保证；
- stdio 模板以 `--role` 固定单一角色，不具备 per-request tenant 身份；需要
  tenant 隔离的演示必须走 HTTP + 可信身份注入。

## 常见故障

- 桌面客户端无响应：确认 `command`/`--config` 均为绝对路径，且配置对
  stdio 模式不要求 HTTP 认证材料；
- HTTP 401：缺少或错误的 `Authorization: Bearer` header；
- HTTP 403 `untrusted proxy`：`trustProxyHeaders` 开启但调用方不在
  `trustedProxyCIDRs` 内；
- 身份 header 被忽略：`trustProxyHeaders` 未开启时 `X-MCP-Role`/
  `X-MCP-Subject` 一律不生效（fail closed，防伪造）。
