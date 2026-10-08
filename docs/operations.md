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
解析后的 DSN；`bootstrap.RedactDSN` 只覆盖常见 PostgreSQL URI 和 MySQL DSN
密码形式。

启用 `cost.aqe.explainAnalyze` 时的字段、启动校验和负载约束见
[配置参考](configuration.md#成本)，执行与失败语义见
[安全模型](security.md#成本闸门)。

## 热重载

```sh
sql-mcp-server serve --config config.yaml --watch --watch-interval 1s
```

watcher 轮询文件内容 hash。新配置必须完整通过加载、secret 解析、数据库连接、
自省和装配才会发布；失败会记录日志、继续使用旧快照，并对相同文件内容继续重试。
采用 drain-before-publish：新快照构建成功后，reload 窗口内的新请求等待发布；
旧快照的在途请求结束后才关闭其 engine、审计、prepared statement 和 provider。
事务 manager 与 budget session 状态跨快照保留。新预算限制会原子应用到原
manager；事务 `ttl` 或 `maxOpen` 变化会拒绝 reload，必须重启，不会静默沿用
旧限制。

热重载拒绝[字段参考](configuration.md#字段参考)中标记“修改需重启”的字段
（`server.transport`、`server.addr`、`server.auth`、`tools`、事务
`ttl`/`maxOpen`），以及新增/移除 custom procedure tool、首次启用或全部删除
用户；这些变化必须重启服务。文件模式与 store 模式共用同一份规则。详见
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
- `store list/show/diff` 查看历史；import、publish、rollback 在 stderr 记录一条
  结构化审计日志。
- `migrate --from <file:path|driver:dsn> --to <...>` 在 YAML 文件与各 store 之间
  迁移当前配置并校验 `content_hash`；两侧都是 store 时 `--history` 复制全部
  revision（目标必须为空）。

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
