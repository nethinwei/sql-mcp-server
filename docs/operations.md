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

创建 RC/GA tag 前先运行：

```sh
make release-preflight RELEASE_VERSION=0.1.4
```

该命令复用 GitHub workflow 使用的验证脚本，覆盖完整测试、跨平台 snapshot、
Registry metadata、容器 SBOM 和 quickstart。GitHub OIDC 签名及对 GHCR/Registry
的实际写入只能在 tag workflow 中完成。

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

RC 流程可试运行 GitHub Artifact Attestations，但 v0.1.4 不把 provenance 作为
发布阻塞项。

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

HTTP 暴露 `/mcp` 和无需认证的 `/healthz`。非 loopback 监听必须在配置中提供
bearer token 或 mTLS。地址默认值 `:8080` 监听所有接口，属于非 loopback；
因此选择 HTTP 但未改为 loopback、也未配置认证时，服务会 fail closed 并拒绝
启动。TLS、反向代理身份 header 与已知边界见
[security.md](security.md)。

## Secret 与启动检查

推荐只在配置中放占位符：

```yaml
dsn: "${DATABASE_DSN}"
```

也可用 `dsn: "${file:/run/secrets/database_dsn}"`。缺失 secret、数据库 ping
失败、未知 mask、配置实体/字段在数据库中缺失都会阻止启动。日志中不要打印
解析后的 DSN；`bootstrap.RedactDSN` 只覆盖常见 PostgreSQL URI 和 MySQL DSN
密码形式。

启用 `cost.aqe.explainAnalyze` 还会检查每个 datasource 是否提供 sampler。
v0.1 只有 PostgreSQL 支持；MySQL/OceanBase 会 fail-fast。该选项命中采样率时
会在实际数据库读取后，用独立 read-only transaction 额外执行一次 SQL 并始终
rollback；缓存命中不采样。应评估数据库负载，并保持低采样率和短
`aqe.timeout`。采样超时、volatile function 写入被拒绝等失败不会把原读取改为
失败，但会进入 error hook 和 best-effort 审计（action 为
`explain_analyze_sample`）。

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

热重载明确拒绝 transport/address、auth、TLS、trusted proxy 和 tool-set 变化；
这些变化以及新增/移除 custom procedure tool 都必须重启服务。详见
[architecture.md](architecture.md)。

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

每次工具调用通过 hook 发出 OpenTelemetry span 属性，但仓库不配置 exporter；
需要由运行环境提供 OTel SDK/exporter 设置。`ServeHTTP` 支持注入 metrics
handler，但当前 CLI 未提供该注入，因此默认没有 `/metrics`。

文件审计是异步 best-effort JSON Lines 事件流。队列满时事件会丢弃；当前没有
内置轮转、远程 sink 或告警。生产环境应监控磁盘、限制文件访问并配置外部轮转。

## 升级

升级前阅读 [CHANGELOG](../CHANGELOG.md) 和对应
[发布说明](releases/)（含不兼容变更与迁移步骤），先运行
`validate`，再在测试数据库运行 provider 集成测试。热重载会新建一组数据库连接，
切换期间应为新旧 pool 的短暂重叠留出容量。
