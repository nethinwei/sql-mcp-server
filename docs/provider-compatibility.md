# Provider 兼容矩阵

本页区分“真实数据库验证”“核心层验证”和“未独立验证”，避免把方言能力声明
误写成生产保证。安全语义以 [安全模型](security.md) 为准。

状态：

- **真实数据库验证**：当前 CI 对固定数据库镜像运行 provider integration 或 MCP
  e2e；
- **核心层验证**：共享 tool/codegen 单元测试覆盖，但该 provider 没有独立场景；
- **未独立验证**：存在实现或 capability 声明，但既无该 provider 的真实数据库
  场景，也无足以证明该语义的 provider 专属测试；
- **未支持**：启动时拒绝或无实现；
- **外部配置**：能力依赖 DBA/部署配置，本服务不代管。

## 功能

| 能力 | PostgreSQL | MySQL | OceanBase | 证据 |
|---|---|---|---|---|
| read、过滤、行策略、mask | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | [PG](../x/providers/postgres/integration_test.go)、[MySQL](../x/providers/mysql/integration_test.go)、[OB](../x/providers/oceanbase/integration_test.go) 的 `Test*RLSRowFilterAndMasking` |
| update 与主键写保护 | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | 上述 integration 的 `Test*UpdateUnsafeWriteAndPK` |
| create / delete | 核心层验证 | 核心层验证 | 核心层验证 | [`tool_write_test.go`](../core/tool/tool_write_test.go)；`delete_record` 默认关闭 |
| aggregate | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | [`tool_aggregate_test.go`](../core/tool/tool_aggregate_test.go) 与三库 conformance 差分（`Test*Conformance`） |
| 读路径 IR 语义一致性 | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | 三库 `Test*Conformance`：reference interpreter 差分 [`internal/conformance`](../internal/conformance)，语义与偏差表见 [IR 语义规范](design/ir-semantics.md) |
| procedure | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | 三库 integration 的 `Test*ExecuteProcedure` |
| 显式事务 | 真实数据库验证（MCP e2e） | 核心层验证 | 核心层验证 | [`e2e_test.go`](../x/mcpserver/e2e_test.go) 与 store/provider 契约 |
| keyset cursor | 能力声明 + 核心层验证 | 能力声明 + 核心层验证 | 能力声明 + 核心层验证 | 各 provider `dialect.go` 与 codegen 测试 |

## 成本与资源控制

| 能力 | PostgreSQL | MySQL | OceanBase | 依据与边界 |
|---|---|---|---|---|
| EXPLAIN 估算 | 准确模式，真实数据库验证 | 保守 fail-closed，真实数据库验证 | 保守 fail-closed，真实数据库验证 | `Test*CostGate` / `Test*ReadPKWhitelist` 及 [安全模型](security.md#成本闸门) |
| EXPLAIN ANALYZE feedback | 支持，默认关闭 | 未支持，启用时启动失败 | 未支持，启用时启动失败 | PostgreSQL `ExplainAnalyze` integration 与 bootstrap 校验 |
| 语句超时、无键写入兜底 | 见 [Provider 能力](#provider-能力) | 同左 | 同左 | 按连接判定；三库 `Test*StatementTimeout` integration |
| 应用 context timeout | 已实现，并发送 cancel request | 已实现（只关闭连接） | 已实现（只关闭连接） | 统一 bootstrap/tool 执行链 |
| scan row cap | 无 | 能力声明；不作为扫描硬保证 | 能力声明；不作为扫描硬保证 | 应用无法跨方言证明实际扫描行数 |
| resource manager | 无 | 无 | 外部配置 | 由 OceanBase DBA 配置，本服务不代管 |

## Provider 能力

每项能力分别给出保证强度（`enforced` / `best_effort` / `unsupported`）与证据，强度按
连接判定：服务注入的会话设置被 DSN 覆盖、或经 transaction pooler 时会降级。启动与
热重载时，数据源依赖的硬限制（读、被授权的写与过程、开启时的 SQL 逃生通道的语句
超时）不是 `enforced` 即告警；`cost.requireNativeLimits: true` 时拒绝加载。模型见
[能力模型设计](design/capability-model.md)。

<!-- BEGIN GENERATED: provider-capabilities -->
<!-- 本节由 internal/capgen 根据各 provider 的能力声明生成，勿手改；`go generate ./x/providers/all` 重新生成。 -->

默认连接（服务注入全部会话设置，不经 pooler）的保证强度与证据：

| 能力 | PostgreSQL | MySQL | OceanBase |
|---|---|---|---|
| `write.returning` | `enforced`（核心层验证） | `unsupported`（核心层验证） | `unsupported`（核心层验证） |
| `cost.explain` | `enforced`（真实数据库验证） | `enforced`（真实数据库验证） | `enforced`（真实数据库验证） |
| `cost.explain.accurate` | `enforced`（真实数据库验证） | `best_effort`（真实数据库验证） | `best_effort`（真实数据库验证） |
| `cost.explain.analyze` | `enforced`（真实数据库验证） | `unsupported`（核心层验证） | `unsupported`（核心层验证） |
| `limit.timeout.read` | `enforced`（真实数据库验证） | `enforced`（真实数据库验证） | `enforced`（真实数据库验证） |
| `limit.timeout.write` | `enforced`（真实数据库验证） | `unsupported`（仅声明） | `enforced`（真实数据库验证） |
| `limit.unkeyedWrite` | `unsupported`（仅声明） | `enforced`（真实数据库验证） | `enforced`（真实数据库验证） |

按连接判定的限制（默认连接，以及与默认不同的连接方式）：

| Provider | 连接方式 | 能力 | 强度 | 说明 |
|---|---|---|---|---|
| PostgreSQL | 默认 | `limit.timeout.read` | `enforced` | statement_timeout just under cost.queryTimeout, and a cancel request at the request deadline |
| PostgreSQL | 默认 | `limit.timeout.write` | `enforced` | statement_timeout just under cost.queryTimeout, and a cancel request at the request deadline |
| PostgreSQL | 默认 | `limit.unkeyedWrite` | `unsupported` | no database-side refusal; the gate's WriteGuard refuses unkeyed writes |
| PostgreSQL | DSN 设置 `statement_timeout` | `limit.timeout.read` | `enforced` | a cancel request at the request deadline (the DSN sets its own statement_timeout) |
| PostgreSQL | DSN 设置 `statement_timeout` | `limit.timeout.write` | `enforced` | a cancel request at the request deadline (the DSN sets its own statement_timeout) |
| PostgreSQL | `pooler: transaction` | `limit.timeout.read` | `best_effort` | statement_timeout is a session setting a transaction pooler does not keep; a cancel request at the request deadline stops the statement only if the pooler forwards it |
| PostgreSQL | `pooler: transaction` | `limit.timeout.write` | `best_effort` | statement_timeout is a session setting a transaction pooler does not keep; a cancel request at the request deadline stops the statement only if the pooler forwards it |
| MySQL | 默认 | `limit.timeout.read` | `enforced` | max_execution_time just under cost.queryTimeout (SELECT only) |
| MySQL | 默认 | `limit.timeout.write` | `unsupported` | max_execution_time stops SELECT only, and a cancelled request only closes the connection: bound writes on the database side (DBA) |
| MySQL | 默认 | `limit.unkeyedWrite` | `enforced` | sql_safe_updates = 1 |
| MySQL | DSN 设置 `max_execution_time` | `limit.timeout.read` | `best_effort` | the DSN sets max_execution_time, so cost.queryTimeout is not applied natively |
| MySQL | DSN 设置 `sql_safe_updates=0` | `limit.unkeyedWrite` | `unsupported` | the DSN turns sql_safe_updates off; the gate's WriteGuard still refuses unkeyed writes |
| MySQL | `pooler: transaction` | `limit.timeout.read` | `best_effort` | max_execution_time is a session setting a transaction pooler does not keep |
| MySQL | `pooler: transaction` | `limit.unkeyedWrite` | `best_effort` | sql_safe_updates is a session setting a transaction pooler does not keep |
| OceanBase | 默认 | `limit.timeout.read` | `enforced` | ob_query_timeout just under cost.queryTimeout |
| OceanBase | 默认 | `limit.timeout.write` | `enforced` | ob_query_timeout just under cost.queryTimeout |
| OceanBase | 默认 | `limit.unkeyedWrite` | `enforced` | sql_safe_updates = 1 |
| OceanBase | DSN 设置 `ob_query_timeout` | `limit.timeout.read` | `best_effort` | the DSN sets ob_query_timeout, so cost.queryTimeout is not applied natively |
| OceanBase | DSN 设置 `ob_query_timeout` | `limit.timeout.write` | `best_effort` | the DSN sets ob_query_timeout, so cost.queryTimeout is not applied natively |
| OceanBase | DSN 设置 `sql_safe_updates=0` | `limit.unkeyedWrite` | `unsupported` | the DSN turns sql_safe_updates off; the gate's WriteGuard still refuses unkeyed writes |
| OceanBase | `pooler: transaction` | `limit.timeout.read` | `best_effort` | ob_query_timeout is a session setting a transaction pooler does not keep |
| OceanBase | `pooler: transaction` | `limit.timeout.write` | `best_effort` | ob_query_timeout is a session setting a transaction pooler does not keep |
| OceanBase | `pooler: transaction` | `limit.unkeyedWrite` | `best_effort` | sql_safe_updates is a session setting a transaction pooler does not keep |
<!-- END GENERATED: provider-capabilities -->

## 关键安全差异

参数化 SQL、成本拒绝、应用层 row policy、procedure 信任边界和数据库资源治理的
行为定义统一见[安全模型](security.md)。本页只记录各 Provider 的证据状态，不复制
运行时安全语义。

## 证据边界

共享层 corpus 与 fuzz 不能单独证明某个 Provider 的真实数据库行为，也不能把
“核心层验证”或“未独立验证”提升为“真实数据库验证”。威胁与剩余风险见
[威胁模型](threat-model.md)，当前发布时点证据见
[`v0.1.4` 发布说明](releases/v0.1.4.md)。

## 验证命令

```sh
make test-integration-postgres
make test-integration-mysql
make test-integration-oceanbase
make test-e2e
```

固定测试版本见 [支持版本](supported-versions.md)。默认单元测试不等价于上述真实
数据库和 MCP e2e。
