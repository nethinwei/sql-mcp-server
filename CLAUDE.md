# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

面向不可信 AI Agent 的受控 SQL 网关：Agent 只能通过显式 Entity + 关系代数 IR +
参数化 codegen 访问 PostgreSQL/MySQL/OceanBase，不接受任意 SQL/DDL，能力无法证明时
fail closed。仓库文档是各自主题的唯一事实源，本文件只做索引与易踩坑提示：
编码规范与门禁见 `CONTRIBUTING.md`，测试与 CI 见 `docs/testing.md`，数据面架构见
`docs/architecture.md`，配置见 `docs/configuration.md`，管理面设计见
`docs/design/{config-store,admin-api,authorization-model}.md`。

## 常用命令

```sh
make build                  # 只编 Go；控制台需先 make web
make fmt / make fmt-check   # internal/fmtcheck：gofmt、120 字节行宽、800 行文件、50 行函数
make ci                     # fmt-check vet lint docs-check build test coverage-check govulncheck
make ci-full                # ci + 三库 integration + MCP e2e（需要 Docker）
make test-integration-postgres   # 单个 provider：postgres|mysql|oceanbase（OceanBase 容器约 3.5 分钟）
make test-e2e
make web                    # web/admin：pnpm install、vitest、vue-tsc、vite build → x/admin/ui/dist/app
make docs-check

go test -race ./x/admin/graph -run TestDraftRoundTrip        # 单个测试
go test -race -tags=integration ./x/configstore -run Postgres  # 集成测试带 build tag
cd web/admin && npx vitest run src/lib/jsonSchema.test.ts      # 单个前端测试
go generate ./core/config    # 重新生成 schema.json 与 docs/configuration.md 字段参考
go generate ./x/admin/graph  # gqlgen（schema.graphqls → generated.go/models_gen.go）
cd web/admin && pnpm codegen # GraphQL 客户端类型（src/gql，已 gitignore）
```

注意：

- `make lint` 要求 golangci-lint **v2.12.2**，版本不符直接失败；
- `make fmt`（golines）会改写与当前改动无关的文件并对齐 struct tag，可能反而超宽；
  修改后只保留自己改动的文件，其余用 `git checkout` 还原；行宽按字节计，中文更占宽度；
- 前端 TypeScript 固定 `~5.9`（vue-tsc 不兼容 TS 7）；
- 控制台未构建时二进制只提供占位页；GoReleaser、Dockerfile、CI 都会先 `make web`。

## 依赖方向

`core/` 只能依赖标准库与 `core/`（`.golangci.yml` depguard 强制）；外部依赖、驱动、
MCP SDK、YAML、gqlgen 都在 `x/` 或 `cmd/`。新增数据库只改 `x/providers/<driver>`、
注册到 `x/providerregistry` 并在 `x/providers/all` blank import，不改 `core/` 与
`x/bootstrap`（见 `CONTRIBUTING.md`）。

## 数据面

`x/mcpserver`（stdio/HTTP、身份）→ `core/tool.RunTool`（预算、engine、hook、审计）→
`core/tool`（RBAC/字段/行策略、IR、成本三阶段、脱敏）→ `core/codegen` + `core/dialect` →
`x/providers`。`x/bootstrap.Runtime` 持有可原子替换的 App 快照（热重载、
`RestartChanges`/`CheckHotReload`）。授权是 grant 模型（`core/rbac`），用户主体为
`user:<name>`。

## 管理面（docs/architecture.md 尚未覆盖）

- **配置存储**：`core/revision`（接口 + MemoryStore + `revisiontest` 一致性套件）、
  `x/configstore`（SQLite/PG/MySQL/OceanBase，写操作在事务内用 store 锁行串行）。
  `serve --store <driver>:<dsn> [--watch] [--admin]` 从已发布 revision 启动。
- **业务规则只在服务层实现，入口（CLI、GraphQL）只做参数、身份、输出**：
  - 管理员账号：`x/admin/accounts.Service`；唯一写入口是 `configstore.MutateAdmins`
    （“至少保留一个账号管理员”在同一事务内检查，CLI 无绕过）；
  - revision 操作：`x/revisionops`（规范化、草稿、发布及 `ExpectedPublished` 乐观并发、
    回滚、diff 统一重编码、审计）；
  - 草稿合并：`x/configedit.Apply`（类型化分区写入 base，整体校验，tokenHash 保留规则）。
- **管理 API**：`x/admin`（会话 Cookie + CSRF、登录并发闸门、改密吊销会话）+
  `x/admin/graph`（gqlgen，schema-first，`schema.graphqls`；读模型如实返回配置值，
  未设置即 null）。
- **控制台**：`web/admin`（Vue 3 + Naive UI + urql + vue-i18n zh-CN/en），通过
  `x/admin/ui` go:embed。工作区状态在 `src/stores/workspace.ts`（变更检测、按属性
  三方合并 rebase、实体/角色引用维护），页面只调用其中的操作。

## 配置规则的单一来源

- 字段类型/约束/重启标记写在 `core/config` 结构体的 `schema:"..."` 标签
  （语法见 `core/config/schema_rules.go`，命名枚举/正则用 `@name` 引用），默认值来自
  `ApplyDefaults`，字段说明（中英文）在 `core/config/fields.yaml`；
  `internal/schemagen` 生成 `core/config/schema.json` 与配置文档字段表，过期即测试失败。
- 加载三阶段：严格解码（未知字段失败）→ `ApplyDefaults` → `Validate`（条件/跨字段规则
  是命名检查，最后执行标签规则）。`Validate` 要求先 `ApplyDefaults`。
- 前后端校验语义一致由 `core/config/testdata/rules.json` 保证（Go 与 vitest 共用）；
  新增可选块要能只靠默认值通过校验（`TestOptionalBlocksLoadWithDefaults`）。
- 新增配置字段时同步：标签、`fields.yaml`、`go generate`、GraphQL DTO 与
  `x/admin/graph/testdata/roundtrip.yaml`（往返与字段覆盖测试会指出遗漏）。
- 编码省略空值；缺省会触发默认值的键（`tools`、`cost`、实体 `mcp.dmlTools`）始终写出。

## 本地演示服务

```sh
sql-mcp-server store init --store sqlite:/path/config.db
sql-mcp-server store import --store sqlite:/path/config.db --config config.yaml && sql-mcp-server store publish --store sqlite:/path/config.db 1
printf 'a long password\n' | sql-mcp-server admin create --store sqlite:/path/config.db --username root
sql-mcp-server serve --store sqlite:/path/config.db --admin --watch   # 控制台 http://127.0.0.1:18090/admin/（默认地址以配置为准）
```

`web/admin` 下 `pnpm dev` 会把 API 代理到 `SMCP_ADMIN_API`（默认 `http://127.0.0.1:18090`）。
