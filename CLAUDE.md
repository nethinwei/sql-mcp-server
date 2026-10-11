# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

面向不可信 AI Agent 的受控 SQL 网关：Agent 只能通过显式 Entity + 关系代数 IR +
参数化 codegen 访问 PostgreSQL/MySQL/OceanBase，不接受任意 SQL/DDL，能力无法证明时
fail closed。仓库文档是各自主题的唯一事实源，本文件只做索引与易踩坑提示：
编码规范与门禁见 `CONTRIBUTING.md`，测试与 CI 见 `docs/testing.md`，数据面架构见
`docs/architecture.md`，配置见 `docs/configuration.md`，管理面设计见
`docs/design/{config-store,admin-api,authorization-model}.md`，术语见
`docs/glossary.md`（pending/draft/role/database 的用法以它为准）。

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
`x/providers`。工具只规划语句，数据库只由执行器 `core/tool/pipeline.go` 调用（语句须由
`grant` + `gated` 构成；脱敏、流式计量、错误分类与写入跟进都在这里，
`TestOnlyTheExecutorCallsTheDatabase` 强制），见 `docs/design/pipeline.md`。`x/bootstrap.Runtime` 持有可原子替换的 App 快照（热重载、
`RestartChanges`/`CheckHotReload`）；进程级状态（连接及其熔断/AIMD、事务、预算、
写跟踪、缓存）在 `x/bootstrap.Shared`，快照只引用，重载不搬运状态；新 revision
装配失败但能加载时，它改动的用户先暂停（`hold.go`），见 `docs/design/lifetimes.md`。授权是 grant 模型（`core/rbac`），用户主体为
`user:<name>`。

## 管理面（docs/architecture.md 尚未覆盖）

- **配置存储**：`core/revision`（接口 + MemoryStore + `revisiontest` 一致性套件）、
  `x/configstore`（SQLite/PG/MySQL/OceanBase，写操作在事务内用 store 锁行串行）。
  `serve --store <driver>:<dsn> [--watch] [--admin]` 从已发布 revision 启动。
- **业务规则只在服务层实现，入口（CLI、GraphQL）只做参数、身份、输出**：
  - 管理员账号：`x/admin/accounts.Service`；唯一写入口是 `configstore.MutateAdmins`
    （“至少保留一个账号管理员”在同一事务内检查，CLI 无绕过）；
  - revision 操作：`x/revisionops`（规范化、草稿、发布只允许快进（草稿须基于当前已发布）
    及 `ExpectedPublished` 乐观并发、
    回滚、diff 统一重编码、审计）；
  - 工作区（版本管理，`docs/design/versioning.md`）：`core/workspace`（纯逻辑：项、
    三方合并 `Merge`、冲突、Store 接口 + `workspacetest` 一致性套件）→
    `x/workspaceops`（Read/WriteItems/Update/Reset/MergeDraft/Resolve/Checkpoint/
    Commit/Log；配置经 `Codec`/`History` 注入，depguard `versioning-generic` 禁止它导入
    配置类型）；`x/admin/configapp/codec.go` 是配置 ⇄ 项与合并规则 `Schema`；
  - 草稿合并：`x/configedit.Apply`（类型化分区写入 base，整体校验，tokenHash 保留规则）。
- **管理 API**：`x/admin`（会话 Cookie + CSRF、登录并发闸门、改密吊销会话）+
  `x/admin/graph`（gqlgen，schema-first，`schema.graphqls`；读模型如实返回配置值，
  未设置即 null）。
- **批量配置**（`docs/design/planning.md`）：`x/catalog`（扫描目录 DTO、Store 与
  扫描批次，配置库 schema 3/4 持久化；批次的跨进程租约/心跳/接管/重试在 `x/leasing`，
  交错模型测试 `TestInterleavings` 锁定其不变量）→ `x/configplan`（纯计算：Plan → Proposal +
  Report、目录索引，候选实体命名也在这里）→ `x/admin/configapp`（配置服务：目录、
  扫描批次、方案预览快照、管理员工作区（委托 `x/workspaceops`；方案写入绑定实际影响）、
  模板、待处理、令牌、差异与模拟；鉴权与业务规则只在
  这里）→ 适配器 `x/admin/graph`（GraphQL；服务类型经 `app_types.go` 别名由 gqlgen
  绑定）与 `x/admin/configmcp`（`/admin/mcp`，管理员令牌可限定部分数据源，
  无发布工具）。
- **控制台**：`web/admin`（Vue 3 + Naive UI + urql + vue-i18n zh-CN/en），通过
  `x/admin/ui` go:embed。工作区状态在 `src/stores/workspace.ts`（服务端工作区的副本、
  变更检测、改名记录；不做合并、冲突计算与引用维护），按项写回、读取与所有版本操作
  （update/reset/合并草稿/解决冲突/检查点/提交，均调用服务端）在
  `src/lib/workspaceSync.ts` 的 `workspace`；页面只调用其中的操作；`/admin/events`（SSE）推送新发布与工作区写入。工作区项的类型与编解码
  （配置 ⇄ `Sections`）只在 `x/admin/configapp/items.go`，控制台从 `Revision.sections`
  读取，不自己构造；形状由 `x/admin/configapp/testdata/workspace_sections.json` 锁定
  （`go test ./x/admin/configapp -run TestSectionsShapeAndRoundTrip -update` 重新生成；
  形状变化会让已保存的工作区读作“已修改”）。
  编辑语义（`docs/design/editing.md`）：引用维护（改名、删除、字段删除、导入造成的歧义）
  只在服务端 `configapp.reconcile`（`workspaceops.Service.Reconcile`，规则在
  `x/configrefs`，与 configplan 共用）；控制台改名时写 `renamedFrom`。目录、方案预览、
  校验、diff、模拟默认读调用者的工作区（`configapp.Against`；控制台用
  `workspace.read` 先写出本地修改），配置 MCP 的查询读已发布配置。

## 配置规则的单一来源

- 意图与生效值（`docs/design/spec.md`）：`config.Spec` 是配置写了什么（未写为 nil，
  由 `internal/schemagen` 从 `Config` 生成 `core/config/spec_gen.go`，勿手改），
  `config.Config` 是生效值。payload、工作区项、`configedit` 都只处理 Spec；
  `config.Resolve(spec)` = 转换（同时得出 presence）→ `ApplyDefaults` → `Validate`。
  在代码里构造的配置用 `config.SpecOf(cfg)` 转回 Spec（零值即未写）。
- 字段类型/约束/重启标记写在 `core/config` 结构体的 `schema:"..."` 标签
  （语法见 `core/config/schema_rules.go`，命名枚举/正则用 `@name` 引用），默认值来自
  `ApplyDefaults`（唯一来源，`schema.json` 的 default 由它推出），字段说明（中英文）在
  `core/config/fields.yaml`；`go generate ./core/config` 生成 `schema.json`、配置文档字段表与
  `spec_gen.go`，过期即测试失败。
- 加载：严格解码为 Spec（未知字段失败；`omitempty` 字段的零值与空集合规范化为未写）
  → `Resolve`。`Validate` 要求先 `ApplyDefaults`。
- 前后端校验语义一致由 `core/config/testdata/rules.json` 保证（Go 与 vitest 共用）；
  新增可选块要能只靠默认值通过校验（`TestOptionalBlocksLoadWithDefaults`）。
- 新增配置字段时同步：标签、`fields.yaml`、`go generate`、GraphQL DTO 与
  `x/admin/graph/testdata/roundtrip.yaml`（往返与字段覆盖测试会指出遗漏）。
- 存储编码 Spec（`configyaml.EncodeSpec`），只写写过的值；`export` 编码生效值
  （`configyaml.Encode`）。工作区项的合并规则（键、零值即未写）在项类型标签上
  （`merge:"key=…"`、`merge:"unset"`，`x/admin/configapp/merge_schema.go`），控制台经
  `testdata/merge_schema.json` 共用。

## 本地演示服务

```sh
sql-mcp-server store init --store sqlite:/path/config.db
sql-mcp-server store import --store sqlite:/path/config.db --config config.yaml && sql-mcp-server store publish --store sqlite:/path/config.db 1
printf 'a long password\n' | sql-mcp-server admin create --store sqlite:/path/config.db --username root
sql-mcp-server serve --store sqlite:/path/config.db --admin --watch   # 控制台 http://127.0.0.1:18090/admin/（默认地址以配置为准）
```

`web/admin` 下 `pnpm dev` 会把 API 代理到 `SMCP_ADMIN_API`（默认 `http://127.0.0.1:18090`）。
