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
`make test` 等价于 `go test -race ./...`，因此会以普通测试方式回放 fuzz target
内置 seed 和 `testdata/fuzz/<target>` 下的 crash/regression seed。`make ci` 是
`make ci-local` 的别名，
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

发布前应运行与 GitHub workflow 共用脚本的本地门禁：

```sh
# 快速门禁：workflow、归档、Registry、镜像、quickstart、ModelScope stdio
make release-preflight-fast RELEASE_VERSION=0.1.4

# 完整门禁：额外运行 lint、govulncheck、三库 integration 和 MCP e2e
make release-preflight RELEASE_VERSION=0.1.4
```

需要预先安装 golangci-lint v2.12.2，以及固定版本 GoReleaser、Syft、actionlint
和 `mcp-publisher`，并可使用 Docker。`scripts/release/` 中的归档、metadata、
quickstart 和 attestation 验证脚本由本地 Make target 与 GitHub Actions 共同
调用，避免维护两套命令。

本地无法生成或发布 GitHub OIDC 身份，也不能无副作用模拟 GHCR/Registry 写入。
因此 keyless Cosign 签名、Artifact Attestations、镜像 push 和 Registry publish
仍是 tag workflow 专属步骤；其输入结构、发布前校验和发布后验证应尽量复用上述
脚本。

## 测试层

- 单元测试：默认 `go test ./...`，核心主要使用手写 fake。
- provider 集成测试：使用 testcontainers 和真实数据库镜像。
- MCP e2e：使用真实数据库与 MCP client，带 `e2e` build tag。
- ModelScope smoke：读取根目录 `mcp_config.json`，启动真实 PostgreSQL 和 stdio
  子进程，验证展示配置及 allow/deny 路径。
- 并发测试：CI 默认使用 race detector；MCP e2e 包含 goleak 检查。

## 安全 corpus 与 fuzz

确定性 adversarial corpus 用普通 table-driven/property 测试和 fuzz seed 表达，
覆盖已知恶意输入与安全不变量；它必须在 `make test` 中可重复回放，不依赖随机
fuzzing 才能发现已知回归。首批四个定向 target 的约定为：

- `FuzzToolPayloadDecodeNormalizeFieldGate`：MCP payload 解码、规范化与字段门禁；
- `FuzzValidatePredicate`：关系代数 IR validator；
- `FuzzCompileNoInjectionAndQuoting`：参数化 SQL codegen 与标识符引用；
- `FuzzTransactionStateMachine`：事务状态机。

不需要 Docker 的 CI smoke 可运行：

```sh
make test-fuzz-smoke
```

该 target 对四个 fuzz target 分别启动一次 `go test`，每项 `-fuzztime=20s` 且
`-timeout=30s`，目标总时长约 1–2 分钟。需要在本地深入调查时，应只定向延长一个
target，例如：

```sh
go test ./core/codegen -run='^$' -fuzz='^FuzzCompileNoInjectionAndQuoting$' -fuzztime=10m
```

发现 crash 后，将 Go 写入 fuzz cache 的最小化输入复制到对应包的
`testdata/fuzz/<target>/`，作为确定性 regression seed；确认 `make test` 回放通过
后再保留修复。随机 smoke 是补充证据，不替代 adversarial corpus、race、integration
或 e2e。

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

运行魔搭本地分发展示 smoke：

```sh
make modelscope-check
```

测试标签不是自动由 `go test ./...` 覆盖，不能用默认单元测试通过推断真实
PostgreSQL/MySQL/OceanBase 或 MCP e2e 已在当前机器执行。

## GitHub Actions

当前 workflow 包含：

- `lint`：golangci-lint；
- `unit`：Go 1.25.12 与 1.26.5，通过 Make target 执行 gofmt、vet、开发版本
  build、`go test -race ./...`；
- `coverage`：与 Makefile 共用核心包清单，真实检查合计至少 80.0%；
- `security-fuzz`：Go 1.26.5 下分别运行四个有界、无 Docker 的安全 fuzz smoke；
- `integration`：PostgreSQL、MySQL、OceanBase 三项 testcontainers matrix；
- `e2e`：PostgreSQL + in-memory MCP client，覆盖协议边界上的工具发现、成本、
  RBAC/RLS/字段 ACL、脱敏、写保护、resource/prompt 和事务；custom procedure
  的 MCP 调用使用默认测试中的 fake DB，真实执行由三库 integration 覆盖；
- `modelscope`：根目录 manifest、真实 PostgreSQL、stdio、mask、row policy 和
  allow/deny smoke；
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
