# 支持版本

本页区分构建要求、CI 实测和兼容预期。“实测”仅表示仓库 integration/e2e 对该
版本持续运行，不等于数据库厂商生命周期承诺。

## 构建与协议

| 组件 | 范围 | 说明 |
|---|---|---|
| Go | 1.26.9+ | `go.mod` 语言版本为 1.26；CI 与发布均使用 1.26.9 |
| MCP Go SDK | 1.6.x | 当前构建固定 `github.com/modelcontextprotocol/go-sdk v1.6.1` |
| MCP transport | stdio、streamable HTTP | HTTP 端点为 `/mcp` |
| 容器平台 | linux/amd64、linux/arm64 | GHCR 发布 multi-arch manifest |
| 二进制平台 | Linux、macOS、Windows 的 amd64/arm64 | 由 GoReleaser 从 tag 生成 |

MCP Registry 仍处于 preview；`server.json` schema 和 publisher 版本固定在 CI 中，
升级时需要单独评审。

## 数据库

| Provider | CI 实测版本 | 兼容预期 |
|---|---|---|
| PostgreSQL | `postgres:11-alpine` 至 `postgres:18-alpine`，逐版本 | PostgreSQL 11–18 |
| MySQL | `mysql:8` | MySQL 8.x |
| OceanBase | `oceanbase/oceanbase-ce:4.3.5.6-106000012026040916` | OceanBase CE 4.3.5.6，MySQL 模式 |

PostgreSQL 的 provider integration 在 CI 中对 11 至 18 每个大版本各运行一次
（`SQL_MCP_PG_IMAGE` 选镜像，见 `.github/workflows/ci.yml`）。下限 11 来自
`pg_index.indnkeyatts`。每建一条连接，provider 先读取 `server_version_num` 与 `version()`：
PostgreSQL 10 及以下、19 及以上，以及说 PostgreSQL 协议但不是 PostgreSQL 的数据库
（Hologres、Greenplum、Redshift、CockroachDB、openGauss/GaussDB、YugabyteDB 等）
一律拒绝，不提供降级运行。这类数据库的目录、计划、事务与 `RETURNING` 语义不同，
按 PostgreSQL 的假设运行会得到半可用、甚至违背安全保证的结果。

MySQL 与 OceanBase 未列出的版本可能可用，但不宣称已验证。升级测试镜像必须同时运行
对应 provider integration，并更新本页和[兼容矩阵](provider-compatibility.md)。
新增 fuzz/core 证据不扩大数据库兼容范围。
