# Contributing to sql-mcp-server

编码风格以 [Google Go Style Guide](https://google.github.io/styleguide/go/) 为基础。
架构约束及其当前例外见 [`docs/invariants.md`](docs/invariants.md)，测试层级和
命令见 [`docs/testing.md`](docs/testing.md)；安全漏洞报告见
[`SECURITY.md`](SECURITY.md)。本文件不重复维护这些事实。

## 编码规范

- Go 源码必须通过 `make fmt-check`。`make fmt` 使用固定版本 golines 写回可自动
  修复的格式；仓库特有硬限制见“仓库内置门禁”。
- package 使用小写单词；导出标识符使用 PascalCase；未导出标识符使用
  camelCase；`HTTP`、`JSON`、`ID`、`DSN`、`SQL` 等 initialism 全大写。
- package 级哨兵错误以 `Err` 开头。需要上下文的错误使用 struct，并实现
  `Unwrap`；用 `%w` 包装以保留 `errors.Is`/`errors.As`。
- required 参数显式传入，optional 行为使用 `WithXxx` option。构造函数验证
  输入并返回 error，不提供 `MustNew`。
- interface 保持窄且按能力命名；能力标记使用 interface assertion，不使用
  metadata 字符串。
- 提前返回并减少嵌套。函数和文件应按职责拆分并满足仓库硬限制，不为凑数字机械
  切割。
- 导出标识符需要说明 what/why 的 doc comment；package 说明放在 `doc.go`。
- 不引入 DI/CLI framework。核心测试使用标准库 `testing` 和手写 fake，不新增
  mock framework。
- 只修改当前问题所需代码，不顺带格式化、重构或删除无关内容。

## 依赖规则

业务核心位于 `core/`，不得 import `x/`。数据库 driver、MCP SDK、
OpenTelemetry、YAML 等外部适配应位于 `x/` 或入口包，依赖方向保持
`x/ -> core`。新增依赖前检查 `.golangci.yml` 的 depguard。

## 仓库内置门禁

贡献前应理解 Make target 背后的检查，而不是只确认命令退出为零：

- `internal/fmtcheck`：递归检查全部 Go 文件的 gofmt、120 字节行宽、800 行文件和
  50 行函数限制；`make fmt` 只能自动处理格式与行宽，不能替代职责拆分；
- `internal/coveragecheck`：只统计 `./core/...` 的语句覆盖率，要求至少 80.0%；
- `make lint`：要求 golangci-lint v2.12.2，并执行仓库配置的 depguard 等规则；
- `internal/modelscopesmoke`：通过真实 PostgreSQL 和 stdio 验证 manifest、工具
  可见性、mask、tenant 隔离、全表读取和隐藏字段拒绝；
- `internal/quickstartsmoke`：通过 Compose 与 streamable HTTP 验证 quickstart
  的同类 allow/deny 路径，由 `release-image-check` 间接执行；
- 发布脚本还会验证 6 个跨平台归档及 checksum、镜像 MCP label、SBOM、Registry
  metadata 和 quickstart 镜像；
- `release-preflight-fast` 编排 workflow、归档、Registry metadata、镜像、
  quickstart 和 ModelScope 门禁；`release-preflight` 在此之前额外执行
  `ci-full`。

具体阈值、依赖和 CI 对应关系见 [`docs/testing.md`](docs/testing.md)。

## 添加数据库

1. 在 `x/providers/<driver>/` 实现 `core/provider.Provider`；按能力可选实现
   `cost.AnalyzeSampler`。
2. 在 provider 包的 `init` 中向 `x/providerregistry` 注册工厂。
3. 在 `x/providers/all/all.go` 增加 blank import，使内置二进制包含该驱动。
4. 添加方言、EXPLAIN、自省和真实数据库集成测试，并更新配置示例、
   [`provider-compatibility.md`](docs/provider-compatibility.md) 和
   [`supported-versions.md`](docs/supported-versions.md)。

新增数据库不得修改 `core/` 或 `x/bootstrap`。能力差异通过核心接口和
`dialect.Capabilities` 表达，不能根据 driver 名称在核心中分支。

## 变更要求

- 修复缺陷时先添加能复现问题的测试，再验证修复后通过。
- 新行为必须覆盖成功和拒绝路径；安全边界变化需同步
  [`docs/security.md`](docs/security.md)、[`docs/threat-model.md`](docs/threat-model.md)
  或 [`docs/invariants.md`](docs/invariants.md) 中受影响的事实。
- 用户可见的配置、CLI、Provider 能力或兼容性变化必须更新对应单一事实源；
  已发布变更写入 `CHANGELOG.md`，迁移与版本细节写入 release note。
- 不提交 secret、真实 DSN、token、私钥、`dist/`、本地二进制或临时覆盖率产物。
- 一个 PR 只处理一个关注点，不混入无关重构或格式化。

## 提交前检查

完整命令、依赖版本和 CI job 以 [`docs/testing.md`](docs/testing.md) 为准。不要手工
拼接一套弱于 Make target 的“等价检查”。

### 基础门禁

修改 Go、配置、构建或脚本时，提交前至少运行：

```sh
make fmt
make ci
git diff --check
```

`make ci` 等价于 `make ci-local`，包含 `fmt-check`、`vet`、固定版本 lint、build、
race tests、核心 coverage 门槛和 `govulncheck`。命令失败必须修复根因；不得跳过
检查、降低阈值或用忽略规则掩盖新增问题。

仅修改 Markdown 时无需为制造“全绿”重复运行不相关的数据库测试，但必须检查链接、
命令和相对路径，并运行 `git diff --check`；PR CI 仍是最终门禁。

### 按风险追加门禁

- 修改 MCP payload、IR validator、codegen、事务状态机或相关安全边界：
  `make test-fuzz-smoke`；该 target 不在 `make ci` 内，但 PR CI 会单独运行；
- 修改 Provider、方言、自省、成本分析、bootstrap 或 MCP transport：
  `make ci-full`；
- 只修改单个 Provider 且需要快速迭代：
  `make test-integration-<driver>`，最终仍按影响范围运行 `make ci-full`；
- 修改 ModelScope manifest、配置或 smoke：
  `make modelscope-check`，该检查需要 Docker；
- 修改 `examples/quickstart/`、Dockerfile、HTTP transport 或镜像内 quickstart
  行为：`make release-image-check RELEASE_VERSION=x.y.z`；该 target 会构建镜像、
  校验 MCP label、生成 SBOM，并运行 `internal/quickstartsmoke`；
- 修改依赖：先运行 `make tidy` 并审阅 `go.mod`/`go.sum`，再运行 `make ci`；
- 修改 workflow、GoReleaser、Registry metadata、Dockerfile 或发布脚本：
  先运行 `make ci`，再运行
  `make release-preflight-fast RELEASE_VERSION=x.y.z`；
- 准备发布版本号：`make release-bump RELEASE_VERSION=x.y.z` 一次性改写全部
  固定版本引用（`server.json`、示例镜像 tag、README/发布索引的当前 GA 行；
  smoke 客户端版本经 `version.String()` 注入，无需手改）；CHANGELOG、发布
  说明与 roadmap 滚动仍为人工编辑；
- 创建 RC/GA tag 前：
  `make release-preflight RELEASE_VERSION=x.y.z`。

Docker、固定版本工具和无法在本地模拟的 OIDC 发布步骤见
[`docs/testing.md`](docs/testing.md)。

## Commit 与 PR

1. 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)：
   使用英文祈使句，首行不超过 72 字符，例如
   `feat(cost): add dual-threshold gate`。
2. PR 描述应说明问题、解决方案、安全与兼容性影响，以及实际运行的验证命令。
3. 提交前确认 `git status` 只包含本 PR 所需文件，并审阅 staged diff。
4. 不得使用 `--no-verify` 绕过 hook，不得以 force push 改写共享主分支历史。
5. 禁止自动 commit、push 或创建 PR，必须先获得人工明确确认。
