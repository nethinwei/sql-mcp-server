# 架构、安全边界与证据索引

一页说明：本服务是什么、边界在哪、每条对外声明的证据在哪。细节以各链接
文档为单一真相源。

## 架构与安全边界（一页）

**The governed SQL gateway for untrusted AI agents.** 不可信 Agent 通过 MCP
调用受治理的实体工具；服务不接受任意 SQL。

```text
Agent (MCP client)
  │  stdio / streamable HTTP（bearer、TLS/mTLS、可信代理身份注入）
  ▼
传输层 x/mcpserver ── 认证、session 身份绑定、body 上限
  ▼
统一执行链 core/tool.RunTool ── 每次调用生成 decision ID
  ├─ RBAC + 字段 ACL + 行级策略（${subject.x}，缺失即不匹配，fail closed）
  ├─ mask（可返回不可过滤/分组/写入，杜绝侧信道）
  ├─ 成本闸门 Safety → Guard → Estimate → EnforceCap（执行前拒绝）
  ├─ 预算/限流/事务边界
  └─ 参数化 SQL codegen → Provider（PostgreSQL / MySQL / OceanBase）
  ▼
审计 JSON Lines（best-effort，输入脱敏，含 decision ID）＋ OTel span
```

边界要点：所有入口 fail closed（非 loopback 必须认证；身份 header 仅在显式
信任代理时生效）；业务拒绝以机器可读契约返回（`code`/`retryable`/`hints`/
`decisionId`，见 [tool-contract.md](tool-contract.md)）；内部错误不回显细节。
完整行为见[安全模型](security.md)与[架构](architecture.md)。

## 证据索引

| 声明 | 证据 | 位置 |
| --- | --- | --- |
| 威胁模型与剩余风险 | TM-001~008 证据账本 | [threat-model.md](threat-model.md) |
| threat ID → 测试映射 | 机器可检查映射清单（CI 单测校验） | `internal/threatcheck/coverage.json` |
| 授权/mask/成本/事务 e2e | in-memory 与真实 HTTP 双传输 e2e（真实 PostgreSQL） | `x/mcpserver/e2e_test.go`、`x/mcpserver/e2e_http_test.go`（`make test-e2e`） |
| 三库 integration | PostgreSQL/MySQL/OceanBase 固定版本容器 | `x/providers/*/integration_test.go`（`make test-integration`） |
| 对抗与 fuzz | critical/high corpus + CI 有界 fuzz smoke（每 target 20s，非长期持续 fuzz） | [testing.md](testing.md)、`make test-fuzz-smoke` |
| 机器契约稳定性 | tool schema/错误码/Denial golden | `core/tool/testdata/contract.json`、[tool-contract.md](tool-contract.md) |
| 六场景产品证明 | quickstart 文档 + 自动 smoke（release 链） | [quickstart.md](quickstart.md)、`internal/quickstartsmoke` |
| 客户端接入 | 四客户端核对结论与边界声明 | [clients.md](clients.md) |
| 签名/checksum/SBOM | keyless Cosign、SHA-256、归档与镜像 SBOM 及验证命令 | [operations.md](operations.md) |
| Registry 元数据 | MCP Registry `server.json` + publisher CI 校验 | 仓库根 `server.json`、[testing.md](testing.md) |
| Provider 保证边界 | 能力矩阵与证据层级 | [provider-compatibility.md](provider-compatibility.md)、[supported-versions.md](supported-versions.md) |

未列入本索引的能力不构成对外声明；各证据的版本时点限制见对应
[release notes](releases/README.md)。
