# 运行与运维

## 构建和命令

```sh
make build
sql-mcp-server version
sql-mcp-server init --config config.yaml --driver postgres
sql-mcp-server add entity --config config.yaml --name users --source users
sql-mcp-server validate --config config.yaml
sql-mcp-server explain --config config.yaml --entity users
```

`init` 以 `0600` 创建文件且不覆盖已有文件。`add entity` 只追加实体骨架，不做
数据库自省。`validate` 解析配置、应用默认值、执行静态校验并解析 DSN secret，
但不连接数据库。`explain` 只输出配置中的实体摘要，不执行 SQL `EXPLAIN`。

### 发布产物与完整性验证

RC/GA tag 的 `release-preflight`、依赖和 CI 门禁统一见
[测试与 CI](testing.md#本地检查)。本节只说明已生成发布产物的验证和运行时使用。
GitHub OIDC 签名及对 GHCR/Registry 的实际写入只能在 tag workflow 中完成。

GitHub Release 提供 Linux、macOS、Windows 的 amd64/arm64 归档、`checksums.txt`、
每个归档的 SPDX JSON SBOM，以及 checksum 的 Sigstore bundle。以 Linux amd64
为例：

```sh
sha256sum --check checksums.txt
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp \
  '^https://github.com/nethinwei/sql-mcp-server/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

容器发布到 `ghcr.io/nethinwei/sql-mcp-server`，支持 linux/amd64 和 linux/arm64。
使用不可变 digest 可验证镜像签名：

```sh
cosign verify \
  --certificate-identity-regexp \
  '^https://github.com/nethinwei/sql-mcp-server/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/nethinwei/sql-mcp-server@sha256:<digest>
```

RC 流程可试运行 GitHub Artifact Attestations；当前 release workflow 不把
provenance 作为发布阻塞项。

## 启动

```sh
# stdio
sql-mcp-server serve --config config.yaml --transport stdio --role reader

# loopback HTTP
sql-mcp-server serve --config config.yaml --transport http --addr 127.0.0.1:8080
```

省略子命令时等价于 `serve`。显式 CLI flag 优先；未传
`--transport`/`--addr` 时使用 YAML 中的 `server.transport`/`addr`，再回退到
`stdio`/`:8080`。`--role` 同样可覆盖 YAML 默认角色。

HTTP 暴露 `/mcp` 与三个无需认证的健康端点：

- `/healthz`（liveness）：进程存活并在处理 HTTP，恒返回 200；
- `/readyz/snapshot`（snapshot readiness）：配置快照已发布可服务时返回 200，
  否则 503；
- `/readyz/db`（database readiness）：所有已配置数据库可达（带 5 秒超时的
  轻量 ping，不走查询主路径）时返回 200，否则 503。

readiness 探针 fail closed：未注入探针（如嵌入方直接使用 `Handler` 且未配置
`SnapshotReady`/`DatabaseReady`）时同样返回 503。响应体不回显失败原因，避免
在未认证端点泄露数据库细节。未认证的非 loopback 监听会 fail closed；认证、
TLS、反向代理身份 header 与已知边界以[安全模型](security.md)为准。

## Secret 与启动检查

推荐只在配置中放占位符：

```yaml
dsn: "${DATABASE_DSN}"
```

也可用 `dsn: "${file:/run/secrets/database_dsn}"`。缺失 secret、数据库 ping
失败、未知 mask、配置实体/字段在数据库中缺失都会阻止启动。日志中不要打印
解析后的 DSN；`bootstrap.RedactDSN` 按驱动语法（PostgreSQL URI 与
keyword/value、MySQL/OceanBase DSN）定位密码，与 store 模式的明文密码检查共用
同一解析。

启用 `cost.aqe.explainAnalyze` 时的字段、启动校验和负载约束见
[配置参考](configuration.md#成本)，执行与失败语义见
[安全模型](security.md#成本闸门)。

## 热重载

```sh
sql-mcp-server serve --config config.yaml --watch --watch-interval 1s
```

watcher 轮询文件内容 hash。新配置必须完整通过加载、secret 解析、数据库连接、
自省和装配才会发布；失败会记录日志、继续使用旧快照，并对相同文件内容继续重试。
新快照构建成功后立即发布，新请求不等待旧请求；旧快照的在途请求结束后，由后台
关闭其 engine、审计和 prepared statement。新旧快照共享进程级服务：数据库连接
（设置未变的连接直接复用，不再重连；无快照引用时关闭）、IO 配额与限流熔断状态
（两代合计不超过配置的 `rateLimit.ioPool`），以及读缓存（按物理数据库失效，旧
快照的写入同样使新快照的缓存失效）。待回收的旧快照最多 3 个，超过时 reload 返回
“earlier configurations are still draining”，watcher 下一轮重试。
被删除或禁用的用户在发布时即解除会话绑定并回滚事务；旧快照排空后，再按当时
关闭的会话 ID 回滚其在途请求期间打开的事务，不影响用户重新启用后新建的会话。
装配时的自省（读 schema、对账）各数据源并行、整体限时 1 分钟；连接权限探测只
用于告警与控制台置灰，在发布后于后台进行（限时 2 分钟），不阻塞启动与重载。
事务 manager 与 budget session 状态跨快照保留，新限制（预算、事务 `ttl` 与
`maxOpen`）原子应用：`maxOpen` 约束之后的 begin，新 `ttl` 用于之后开启的事务。

热重载只拒绝[字段参考](configuration.md#字段参考)中标记“修改需重启”的字段
（`server.transport`、`server.addr`）以及开启或关闭 TLS：它们决定监听器本身。
其余变化均可热加载，包括：认证（token、可信代理、用户的启用与全部删除）与 TLS
证书/客户端 CA 轮换（下一次握手生效）、工具集合与 custom procedure tool（客户端
收到 `tools/list_changed` 通知）、事务限制。新认证在装配前准备好（校验并读取证书），
不合法或证书读不到时 reload 失败、继续用旧快照；发布时直接切换到准备好的认证，
不再读取文件；共享设置（IO 配额、缓存上限、连接池大小）
只在新快照发布时应用，失败的 reload 不改变正在服务的设置。关闭缓存的配置仍会
向共享缓存传递写入失效。文件模式与 store 模式共用同一份规则。详见
[architecture.md](architecture.md)。

## 配置存储

配置可以保存在带版本历史的 store 中，而不是单个 YAML 文件：

```sh
sql-mcp-server store init    --store sqlite:/var/lib/sql-mcp-server/config.db
sql-mcp-server store import  --store sqlite:/var/lib/sql-mcp-server/config.db --config config.yaml
sql-mcp-server store publish --store sqlite:/var/lib/sql-mcp-server/config.db 1
sql-mcp-server serve         --store sqlite:/var/lib/sql-mcp-server/config.db --watch
```

- store 位置为 `<driver>:<dsn>`，`driver` 取 `sqlite`、`postgres`、`mysql`、
  `oceanbase`；也可设置环境变量 `SQL_MCP_STORE`。DSN 支持 `${ENV}` 与
  `${file:...}`，后者的允许根由 `--secret-root` 指定。store 表统一使用 `smcp_`
  前缀，可放在业务库中，但实体与 introspection 都不会触及它们。
- `serve --store` 从当前 published revision 启动；没有发布时拒绝启动。
  `--config` 与 `--store` 不能同时使用。
- `--watch` 在 store 模式下默认每 5s 轮询最新发布。应用失败（构建错误、需要
  重启的变化、hash 不匹配、store 不可达）时继续用旧快照服务，
  `/readyz/snapshot` 仍为 200 并带 `X-Snapshot-Stale: <revision id>`，同一失败只
  记录一次日志。
- `store publish` 与 `store rollback` 会把目标与**当前已发布的 revision**比较；
  包含需重启的变化时拒绝，除非加 `--restart-required`（运行中的实例标记 stale，
  重启后生效）。因此回滚到正在运行的旧配置时，也可能需要该参数。
- store 模式下 payload 中的 DSN 密码必须是占位符，`server.auth.token` 必须为空
  （改用 `users` 的每用户 token），否则 import 失败。
- `store list/show/diff` 查看历史。`diff` 先把两侧按当前编码重新编码再比较，
  与控制台显示的差异一致；`--raw` 改为比较存储的原始字节。import、publish、
  rollback 在 stderr 记录一条结构化审计日志（含 `via=cli` 或 `via=admin-api`）。
- `migrate --from <file:path|driver:dsn> --to <...>` 在 YAML 文件与各 store 之间
  迁移当前配置并校验 `content_hash`；两侧都是 store 时 `--history` 复制全部
  revision（目标必须为空）。

## 管理控制台

store 模式下 `serve --admin`（仅 HTTP）在 `/admin` 提供 GraphQL 管理 API 与
Web 控制台，管理员账号用 `sql-mcp-server admin create|passwd|set|list` 管理，
密码从标准输入读取。使用方法、权限说明与 Docker 部署见
[管理控制台](console.md)。

## 容器部署

镜像 `ghcr.io/nethinwei/sql-mcp-server` 基于 distroless，以非 root 用户
（uid 65532）运行，入口为 `sql-mcp-server`，子命令与参数由容器命令给出（如
`serve --config /config/config.yaml`）。`/var/lib/sql-mcp-server` 已建好并归该用户所有，可直接挂载
数据卷存放 SQLite 配置存储。

- 文件模式：挂载配置到 `/config/config.yaml`，参考
  [`examples/quickstart`](../examples/quickstart/compose.yaml)。
- store 模式与控制台：设置 `SQL_MCP_STORE`，用 `docker compose run --rm <服务>
  store ...`/`admin create` 完成初始化后启动 `serve --admin --watch`，完整步骤见
  [管理控制台 · Docker 部署](console.md#docker-部署)。

容器内监听非 loopback 地址时必须配置 MCP 认证（`users`、`server.auth.token`
或 mTLS），否则服务拒绝启动。

## 生命周期

- SIGINT/SIGTERM 取消根 context；HTTP 最多用 15 秒优雅 shutdown。
- `App.CloseContext` 先按调用方 deadline drain engine，再回滚未完成事务并关闭
  provider；drain 超时不会提前释放仍被执行使用的资源。`App.Close` 保留原有
  无 deadline 兼容行为。
- HTTP MCP session 正常关闭时回滚该 session 的事务；无稳定 session ID 的
  transport 依赖事务 TTL 和 App 关闭。
- 数据库连接上限与 `rateLimit.ioPool` 对齐，idle timeout 来自
  `connMaxIdleTime`。

## 监控与审计

每次工具调用通过 hook 发出 OpenTelemetry span（含 `decision.id`、RBAC 与
cost gate 属性）。设置标准 `OTEL_EXPORTER_OTLP_ENDPOINT`（或
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`）后，`serve` 会初始化 OTLP
HTTP/protobuf exporter 并真实导出 span；未设置时保持 no-op，不产生网络流量。

HTTP transport 在 `/metrics` 暴露最小 Prometheus 文本格式指标（配置
`server.auth.token` 时需要同一 Bearer token）：

- `sql_mcp_tool_calls_total{tool,outcome}`：outcome 为 `OK`、Denial 机器码
  （如 `UNAUTHORIZED`、`COST_EXCEEDED`）或基础设施拒绝
  （`OVERLOADED`、`RATE_LIMITED`、`CIRCUIT_OPEN`、`TIMEOUT` 等）；
- `sql_mcp_tool_duration_seconds`：按 tool 的时长直方图；
- `sql_mcp_audit_dropped_total`：审计队列满导致的事件丢弃数。

`serve` 的日志为 stderr 上的 JSON 结构化日志；每次工具失败记录一行，携带
`decisionId` 与 `outcome`，可与 MCP 响应、审计事件和 trace span 关联。

文件审计是异步 best-effort JSON Lines 事件流（字段 schema 见
[tool-contract.md](tool-contract.md)）。队列满时事件会丢弃（计入
`sql_mcp_audit_dropped_total`）；当前没有内置轮转、远程 sink 或告警。生产
环境应监控磁盘、限制文件访问并配置外部轮转。

## 升级

升级前阅读 [CHANGELOG](../CHANGELOG.md) 和对应
[发布说明](releases/)（含不兼容变更与迁移步骤），先运行
`validate`，再在测试数据库运行 provider 集成测试。热重载会新建一组数据库连接，
切换期间应为新旧 pool 的短暂重叠留出容量。
