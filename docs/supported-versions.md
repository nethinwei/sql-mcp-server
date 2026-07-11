# 支持版本

本页区分构建要求、CI 实测和兼容预期。“实测”仅表示仓库 integration/e2e 对该
版本持续运行，不等于数据库厂商生命周期承诺。

## 构建与协议

| 组件 | 范围 | 说明 |
|---|---|---|
| Go | 1.25.12+ | `go.mod` 语言版本为 1.25；CI 验证 1.25.12 与 1.26.5，发布使用 1.26.5 |
| MCP Go SDK | 1.6.x | 当前构建固定 `github.com/modelcontextprotocol/go-sdk v1.6.1` |
| MCP transport | stdio、streamable HTTP | HTTP 端点为 `/mcp` |
| 容器平台 | linux/amd64、linux/arm64 | GHCR 发布 multi-arch manifest |
| 二进制平台 | Linux、macOS、Windows 的 amd64/arm64 | 由 GoReleaser 从 tag 生成 |

MCP Registry 仍处于 preview；`server.json` schema 和 publisher 版本固定在 CI 中，
升级时需要单独评审。

## 数据库

| Provider | CI 实测版本 | 兼容预期 |
|---|---|---|
| PostgreSQL | `postgres:16-alpine` | PostgreSQL 16 |
| MySQL | `mysql:8` | MySQL 8.x |
| OceanBase | `oceanbase/oceanbase-ce:4.3.5.6-106000012026040916` | OceanBase CE 4.3.5.6，MySQL 模式 |

未列出的数据库版本可能可用，但 v0.1.4 不宣称已验证。升级测试镜像必须同时运行
对应 provider integration，并更新本页和
[兼容矩阵](provider-compatibility.md)。

v0.1.4 的 CI/测试继续使用上述 Go 1.26.5 和数据库镜像；新增 fuzz/core 证据不扩大
数据库兼容范围。
