# 测试与 CI

本文件是本地测试、CI job、fuzz target、integration/e2e 和发布前门禁的唯一事实源。
已生成发布产物的 checksum、签名和运行时验证见[运行与运维](operations.md)。

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
上限）。`internal/dockercheck` 核对控制台读取的仓库外文件（GraphQL schema、共享 fixture）都已在 Dockerfile 的 web 阶段复制。`internal/buscheck` 是 `go test ./...` 的一部分，生产代码绕过执行总线
启动 goroutine 或定时器时失败。仓库 `.vscode/settings.json` 为 gopls 配置 `integration,e2e` build
tag，便于 IDE 分析带标签的测试文件。

本次变更的最低验证也可使用 `go test ./...`；提交前按 CI 使用 `-race`。
`make test` 等价于 `go test -race ./...`，因此会以普通测试方式回放 fuzz target
内置 seed 和 `testdata/fuzz/<target>` 下的 crash/regression seed。`make ci` 是
`make ci-local` 的别名，
运行格式检查、vet、lint、输出版本为 `dev` 的本机 `sql-mcp-server` build、
race tests、核心 coverage 门槛和
govulncheck，不需要 Docker。`make ci-full` 在此基础上增加三库 integration 和
PostgreSQL MCP e2e，需要 Docker。

控制台：`make web` 依次运行 `pnpm codegen`、`pnpm lint`（ESLint：`eslint-plugin-vue` +
`typescript-eslint`，配置 `web/admin/eslint.config.js`，不检查排版）、`pnpm test`
（vitest，jsdom）与 `pnpm build`（含 `vue-tsc`）。`src/i18n/keys.test.ts` 检查源码中每个
字面量消息键都存在（en 与 zh-CN 的键由类型保持一致）；组件测试用 `@vue/test-utils`
挂载（如 `src/components/RuntimeBadge.test.ts`），依赖较多的布局逻辑抽成可单测的模块
（`src/lib/liveUpdates.ts`、`src/lib/unloadGuard.ts`）。

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
make release-preflight-fast RELEASE_VERSION=x.y.z

# 完整门禁：额外运行 lint、govulncheck、三库 integration、MCP e2e、控制台与协议 smoke
make release-preflight RELEASE_VERSION=x.y.z
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
- MCP e2e：使用真实数据库与 MCP client，带 `e2e` build tag；同一断言套件在
  in-memory 传输和真实 streamable HTTP listener（bearer + 身份 header）上各跑
  一遍，证明两条传输路径的关键授权行为等价。
- ModelScope smoke：读取根目录 `mcp_config.json`，启动真实 PostgreSQL 和 stdio
  子进程，验证展示配置及 allow/deny 路径。
- 并发测试：CI 默认使用 race detector；MCP e2e 包含 goleak 检查。
- 多管理员发布的边界与压力：`x/revisionops/revisionopstest` 在内存库、SQLite（单元
  测试）和 PostgreSQL/MySQL/OceanBase（配置库集成测试）上运行同一套用例——多人并发
  “读取 → 修改 → 发布，冲突则变基重试”不丢任何修改且历史线性、基于同一版本的草稿
  并发发布只有一个成功、发布与回滚交错、草稿链、回滚后的旧草稿、缺失父版本等；
  `x/admin/events_test.go` 压测发布推送（大量订阅与退订抖动、慢订阅者、会话结束）。
- 版本管理（`docs/design/versioning.md`）：`core/workspace` 的合并性质测试
  （`merge_props_test.go`）用带种子的随机编辑逐叶子校验合并规则（单方修改保留、
  双方不同即冲突且不漏报、两侧交换对称、采用对方后取值一致），
  `conflicts_model_test.go` 随机生成“编辑 / 发布 / update / 解决”序列；失败信息给出
  可复现的种子。`core/workspace/workspacetest` 是工作区存储的一致性套件（内存、
  SQLite 单元测试，PostgreSQL/MySQL/OceanBase 配置库集成测试）；`x/workspaceops`
  测试并发写合并、update、合并草稿、提交的原子性与写入记录，交错模型测试
  （`interleave_test.go`）在提交与存草稿的构建期间插入随机操作（含带 `renamedFrom`
  的改名写入）并检查草稿记录、检查点与提交内容的不变量，以及改名的派生改动与写入同版本。
- 编辑语义（`docs/design/editing.md`）：`x/configrefs` 用与控制台共用的
  `core/config/testdata/entity_refs.json` 检查最短名，表驱动检查引用改写规则；
  `x/admin/configapp` 的 `reconcile_test.go` 按原控制台级联用例逐条检查派生改动（删除、
  改名、移动、角色改名与删除、导入造成的歧义、字段删除），并用 3000 个种子的性质测试
  检查“已有引用变更后仍指向原实体（或随之删除）”；`x/admin/graph` 的
  `TestEditWorkspaceRenamesAnEntity` 与 `TestWorkspaceRoundTripKeepsEveryField` 经 GraphQL
  端到端检查改名与项的往返。
- 批量配置：`x/configplan` 的表驱动测试与随机性质测试（随机目录、配置与方案：
  结果总能通过配置校验，同一方案执行第二次没有修改，每个应授予而未授予的授权都
  有例外；改坏合并或规则顺序时测试失败）；`x/catalog/catalogtest` 与配置库的
  模板/令牌测试在 SQLite 与 PostgreSQL/MySQL/OceanBase 上运行；
  `x/admin/configmcp` 用真实 MCP 客户端端到端测试配置 MCP（没有发布工具、只读
  管理员不能应用方案、过期与停用账号的令牌失效、预览针对工作区、不是预览过的方案
  拒绝应用、方案直接写进工作区且只写改动的项）；`x/admin/graph` 测试按项写入的哈希
  冲突、整体写入的版本冲突与控制台的 `applyPlan`，`x/admin/configapp` 的
  `TestSectionsShapeAndRoundTrip` 锁定工作区项的形状与往返，`x/admin/graph` 的
  `TestRevisionSectionsAreTheWorkspaceShape` 检查控制台读取的 `Revision.sections` 与之一致；控制台按项写入与读回（只有本地改的保留、双方都改的取服务端的、写入途中的
  修改不丢）在 `src/stores/workspace.sync.test.ts`，草稿依次并入与冲突在
  `src/stores/workspace.reviewDraft.test.ts`。控制台的操作链路有单元测试：乱序返回时只采用最新的方案预览
  （`src/lib/planPreview.test.ts`）、“选择全部匹配”的筛选快照与排除
  （`src/lib/selection.test.ts`，含匹配数变化后的确认）、模板载入不改变导入语义、
  扫描批次的跟随与刷新（`src/lib/schemaScans.test.ts`）、工作区计算上下文（载入
  草稿也作为基准）、异步读取的状态与单表同步在找不到表时不提出删除
  （`src/lib/asyncResource.test.ts`）。扫描批次的接管、取消、失败
  重试、幂等键与配额在 `x/admin/configapp/scans_test.go`，批次存储（条件迁移、旧运行
  写不进、并发创建不越过上限）在四种配置库上跑一致性套件；令牌范围与例外确认绑定在
  `x/admin/configmcp`。按需生成的候选实体与全量生成、目录索引过滤与 `Rows` 逐项一致
  由 `x/configplan` 的随机测试保证；缓存的并发去重与预算淘汰在
  `x/admin/configapp/lru_test.go`。规模基准（500 个库、5 万张表）：
  `go test ./x/configplan ./x/admin/configapp -run '^$' -bench 'Large|CatalogPage'`，
  其中 `BenchmarkCatalogPageParallel` 模拟多个管理员并发翻页：ns/op 是吞吐，
  `p50-ms`/`p95-ms` 是逐请求计时的延迟，一半请求的筛选从未出现过、不命中缓存。

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
SQL_MCP_PG_IMAGE=postgres:11-alpine make test-integration-postgres  # 指定 PostgreSQL 版本
```

PostgreSQL 的 provider 与配置库集成测试使用 `SQL_MCP_PG_IMAGE` 指定的镜像，未设置时为
`postgres:16-alpine`（`internal/pgimage`）。只验证 provider 拒绝不受支持服务器的测试固定用
`postgres:10-alpine`，且只在默认镜像下运行一次。

该通道包含三库 conformance 差分（`Test{PG,MySQL,OB}Conformance`）：同一
IR 的 reference interpreter 结果与真实数据库执行结果比对，语义定义见
[IR 语义规范](design/ir-semantics.md)，corpus 在 `internal/conformance`；随后经完整
工具链运行跨库治理等价任务（`conformance.RunGovernance`：受治理读、对抗过滤、受治理
聚合、全表扫描与无键写入的拒绝码、语句超时分类）。`Test{PG,MySQL,OB}StatementTimeout`
是兼容矩阵中语句超时与无键写入兜底“真实数据库验证”的证据。

运行 MCP e2e：

```sh
go test -race -tags=e2e -timeout 10m ./x/mcpserver/...
# 或 make test-e2e
```

运行协议 smoke（stdio + streamable HTTP，需要 Docker）：

```sh
make smoke-protocol
```

运行魔搭本地分发展示 smoke：

```sh
make modelscope-check
```

性能基线与 Agent Eval pilot 按需手动运行，不进入 PR CI：

```sh
make bench            # 热路径 Go benchmark（无需 Docker），见 docs/benchmarks/hot-paths.md
make bench-integration  # 真实 PG/MySQL 的元数据扫描与权限探测（需要 Docker）
make bench-overhead   # data-plane overhead p50/p95/p99（需要 Docker）
make eval-pilot       # Eval 回归轨（v3 冻结基线），需要 Docker 与 EVAL_API_KEY/EVAL_MODEL
make eval-workload    # Eval 真实负载轨（fixtures/v4），环境同上；EVAL_DSN 切换 dogfooding 模式
make docs-check       # 文档内部链接与版本引用一致性（无外部依赖）
make fixtures-v4      # 再生成 fixtures/v4 产物（drift 测试锁定一致性）
```

测试标签不是自动由 `go test ./...` 覆盖，不能用默认单元测试通过推断真实
PostgreSQL/MySQL/OceanBase 或 MCP e2e 已在当前机器执行。

## GitHub Actions

当前 workflow 包含：

- `lint`：golangci-lint；
- `unit`：Go 1.26.10，通过 Make target 执行 gofmt、vet、开发版本
  build、`go test -race ./...`；
- `coverage`：与 Makefile 共用核心包清单，真实检查合计至少 80.0%；
- `security-fuzz`：Go 1.26.10 下分别运行四个有界、无 Docker 的安全 fuzz smoke；
- `integration`：testcontainers matrix，PostgreSQL 11 至 18 每个大版本各一项（经
  `SQL_MCP_PG_IMAGE` 选镜像，provider 与配置库测试都在该版本上运行），以及 MySQL、
  OceanBase 各一项；任一项失败不取消其余项；
- `e2e`：PostgreSQL + in-memory 与真实 streamable HTTP 双传输 MCP client，
  覆盖协议边界上的工具发现、成本、RBAC/RLS/字段 ACL、脱敏、写保护、
  resource/prompt、事务、HTTP 认证/身份 header，以及拒绝契约 decision ID 与
  审计事件的关联；custom procedure 的 MCP 调用使用默认测试中的 fake DB，
  真实执行由三库 integration 覆盖；
- `protocol-smoke`：stdio 与 streamable HTTP 双传输协议 smoke（真实
  PostgreSQL、initialize、tools/list、allow/deny、健康/就绪/metrics 端点），
  本地经 `make smoke-protocol` 复现；
- `quickstart`：用本次提交构建镜像，经 Compose 与 streamable HTTP 运行
  quickstart allow/deny smoke（`internal/quickstartsmoke`），本地经
  `make quickstart-check` 复现；
- `modelscope`：根目录 manifest、真实 PostgreSQL、stdio、mask、row policy 和
  allow/deny smoke；
- `govulncheck`；
- `release-config`：GoReleaser snapshot、6 个目标归档、checksum 和 SBOM；
- `registry-metadata`：`server.json` 版本一致性与官方 publisher 校验。

tag workflow 先校验已提交的 `server.json` 写的正是 tag 版本（不一致即失败；
RC tag `vX.Y.Z-rc.N` 比较 `X.Y.Z`；本地门禁须显式传入 `RELEASE_VERSION`，未传即失败），
再重新运行 release quality、三库 integration、MCP e2e，并用 tag
构建的镜像运行 quickstart smoke；全部通过后才发布签名二进制与 GHCR 多架构镜像，
再对已发布镜像运行 quickstart smoke，最后发布 Registry metadata。发布流程
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
