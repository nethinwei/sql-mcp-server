# 测试与 CI

## 本地检查

无需数据库的默认检查：

```sh
make fmt
go vet ./...
go test -race ./...
golangci-lint run ./...
make build
govulncheck ./...
```

`make fmt` 运行 `internal/fmtcheck`（gofmt + golines，并检查文件/函数/行宽
上限）。仓库 `.vscode/settings.json` 为 gopls 配置 `integration,e2e` build
tag，便于 IDE 分析带标签的测试文件。

本次变更的最低验证也可使用 `go test ./...`；提交前按 CI 使用 `-race`。
`make test` 等价于 `go test -race ./...`。`make ci` 是 `make ci-local` 的别名，
运行格式检查、vet、lint、输出版本为 `dev` 的本机 `sql-mcp-server` build、
race tests、核心 coverage 门槛和
govulncheck，不需要 Docker。`make ci-full` 在此基础上增加三库 integration 和
PostgreSQL MCP e2e，需要 Docker。

发布构建由 GoReleaser 注入 tag 版本。安装 GoReleaser v2.17.0 与 Syft v1.46.0
后可在本地检查：

```sh
make release-check
make release-snapshot
```

snapshot 会生成 6 个目标归档、SHA-256 checksum 和归档 SBOM，但跳过发布与签名。

## 测试层

- 单元测试：默认 `go test ./...`，核心主要使用手写 fake。
- provider 集成测试：使用 testcontainers 和真实数据库镜像。
- MCP e2e：使用真实数据库与 MCP client，带 `e2e` build tag。
- 并发测试：CI 默认使用 race detector；MCP e2e 包含 goleak 检查。

运行所有 provider 集成测试需要 Docker：

```sh
go test -race -tags=integration -timeout 20m ./x/providers/...
# 或 make test-integration
```

运行 MCP e2e：

```sh
go test -race -tags=e2e -timeout 10m ./x/mcpserver/...
# 或 make test-e2e
```

测试标签不是自动由 `go test ./...` 覆盖，不能用默认单元测试通过推断真实
PostgreSQL/MySQL/OceanBase 或 MCP e2e 已在当前机器执行。

## GitHub Actions

当前 workflow 包含：

- `lint`：golangci-lint；
- `unit`：Go 1.25 与 stable，通过 Make target 执行 gofmt、vet、开发版本 build、
  `go test -race ./...`；
- `coverage`：与 Makefile 共用核心包清单，真实检查合计至少 80.0%；
- `integration`：PostgreSQL、MySQL、OceanBase 三项 testcontainers matrix；
- `e2e`：PostgreSQL + in-memory MCP client，覆盖协议边界上的工具发现、成本、
  RBAC/RLS/字段 ACL、脱敏、写保护、resource/prompt 和事务；custom procedure
  的 MCP 调用使用默认测试中的 fake DB，真实执行由三库 integration 覆盖；
- `govulncheck`；
- `release-config`：GoReleaser snapshot、6 个目标归档、checksum 和 SBOM；
- `registry-metadata`：`server.json` 版本一致性与官方 publisher 校验。

tag workflow 会重新运行 release quality、三库 integration 和 MCP e2e，然后发布
签名二进制、GHCR 多架构镜像、quickstart smoke 与 Registry metadata。发布流程
见 [运行与运维](operations.md)。

## 编写测试

- 测试名描述可观察行为，例如 `TestGateRejectsFullScan`。
- 输入差异优先用 table-driven test 和 `t.Run`。
- 核心包使用标准库 `testing` 与手写 fake，不新增 mock framework。
- 修复缺陷时先增加可复现测试；并发和资源生命周期变更至少运行 `-race`。
- 改动 provider、bootstrap 或 MCP transport 时，除单元测试外运行对应
  integration/e2e。

架构和安全约束清单见 [invariants.md](invariants.md)。该清单包含不同强度的
验证方式；不要把所有条目都描述成 property test。
