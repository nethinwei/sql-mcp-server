# Contributing to sql-mcp-server

编码风格以 [Google Go Style Guide](https://google.github.io/styleguide/go/) 为基础。
架构约束及其当前例外见 [`docs/invariants.md`](docs/invariants.md)，测试层级和
命令见 [`docs/testing.md`](docs/testing.md)；安全漏洞报告见
[`SECURITY.md`](SECURITY.md)。本文件不重复维护这些事实。

## 编码规范

- `gofmt` 是唯一格式标准。
- package 使用小写单词；导出标识符使用 PascalCase；未导出标识符使用
  camelCase；`HTTP`、`JSON`、`ID`、`DSN`、`SQL` 等 initialism 全大写。
- package 级哨兵错误以 `Err` 开头。需要上下文的错误使用 struct，并实现
  `Unwrap`；用 `%w` 包装以保留 `errors.Is`/`errors.As`。
- required 参数显式传入，optional 行为使用 `WithXxx` option。构造函数验证
  输入并返回 error，不提供 `MustNew`。
- interface 保持窄且按能力命名；能力标记使用 interface assertion，不使用
  metadata 字符串。
- 提前返回并减少嵌套。函数和文件过大时按职责拆分，不为满足数字机械切割。
- 导出标识符需要说明 what/why 的 doc comment；package 说明放在 `doc.go`。
- 不引入 DI/CLI framework。核心测试使用标准库 `testing` 和手写 fake，不新增
  mock framework。
- 只修改当前问题所需代码，不顺带格式化、重构或删除无关内容。

## 依赖规则

业务核心位于 `core/`，不得 import `x/`。数据库 driver、MCP SDK、
OpenTelemetry、YAML 等外部适配应位于 `x/` 或入口包，依赖方向保持
`x/ -> core`。新增依赖前检查 `.golangci.yml` 的 depguard。

## 添加数据库

1. 在 `x/providers/<driver>/` 实现 `core/provider.Provider`；按能力可选实现
   `cost.AnalyzeSampler`。
2. 在 provider 包的 `init` 中向 `x/providerregistry` 注册工厂。
3. 在 `x/providers/all/all.go` 增加 blank import，使内置二进制包含该驱动。
4. 添加方言、EXPLAIN、自省和真实数据库集成测试，并更新配置文档与示例。

新增数据库不得修改 `core/` 或 `x/bootstrap`。能力差异通过核心接口和
`dialect.Capabilities` 表达，不能根据 driver 名称在核心中分支。

## 提交流程

1. 运行 `make fmt`（或 `make fmt-check`）和 `go vet ./...`。
2. 运行 `go test -race ./...`；改动 provider/transport 时再运行相关
   integration/e2e。
3. 运行 `golangci-lint run ./...`，解释并修复新增告警。
4. 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)：
   英文祈使句，首行不超过 72 字符，例如
   `feat(cost): add dual-threshold gate`。
5. 一个 PR 只处理一个关注点；不得绕过 hook 或 CI。
6. 禁止自动 commit/push/创建 PR，必须先获得人工明确确认。
