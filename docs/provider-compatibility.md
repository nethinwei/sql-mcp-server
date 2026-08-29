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

| 能力 | PostgreSQL | MySQL | OceanBase | Hologres | 证据 |
|---|---|---|---|---|---|
| read、过滤、行策略、mask | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | 未独立验证 | [PG](../x/providers/postgres/integration_test.go)、[MySQL](../x/providers/mysql/integration_test.go)、[OB](../x/providers/oceanbase/integration_test.go) 的 `Test*RLSRowFilterAndMasking`；Hologres 用例见 [`x/providers/hologres/integration_test.go`](../x/providers/hologres/integration_test.go)（`HOLOGRES_TEST_DSN` 门控，待实例实测） |
| update 与主键写保护 | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | 未支持（首版范围为 read + aggregate） | 上述 integration 的 `Test*UpdateUnsafeWriteAndPK` |
| create / delete | 核心层验证 | 核心层验证 | 核心层验证 | 未支持（首版范围为 read + aggregate） | [`tool_write_test.go`](../core/tool/tool_write_test.go)；`delete_record` 默认关闭 |
| aggregate | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | 未独立验证 | [`tool_aggregate_test.go`](../core/tool/tool_aggregate_test.go) 与三库 conformance 差分（`Test*Conformance`）；Hologres conformance 已接入同一套件 |
| 读路径 IR 语义一致性 | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | 未独立验证 | 三库 `Test*Conformance`：reference interpreter 差分 [`internal/conformance`](../internal/conformance)，语义与偏差表见 [IR 语义规范](design/ir-semantics.md) |
| procedure | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | 未支持 | 三库 integration 的 `Test*ExecuteProcedure` |
| 显式事务 | 真实数据库验证（MCP e2e） | 核心层验证 | 核心层验证 | 未支持，fail-closed | [`e2e_test.go`](../x/mcpserver/e2e_test.go) 与 store/provider 契约；Hologres 事务仅覆盖 DDL，`begin_transaction` 以 `TRANSACTION_UNSUPPORTED` 拒绝（`core/tool/transaction.go` capability 检查，单测覆盖） |
| keyset cursor | 能力声明 + 核心层验证 | 能力声明 + 核心层验证 | 能力声明 + 核心层验证 | 能力声明 | 各 provider `dialect.go` 与 codegen 测试 |

## 成本与资源控制

| 能力 | PostgreSQL | MySQL | OceanBase | Hologres | 依据与边界 |
|---|---|---|---|---|---|
| EXPLAIN 估算 | 准确模式，真实数据库验证 | 保守 fail-closed，真实数据库验证 | 保守 fail-closed，真实数据库验证 | 未装配（`ExplainCost=false`） | `Test*CostGate` / `Test*ReadPKWhitelist` 及 [安全模型](security.md#成本闸门)；Hologres 仅文本 EXPLAIN，不装配 Estimate 层，行数硬限制由 EnforceCap 承担 |
| EXPLAIN ANALYZE feedback | 支持，默认关闭 | 未支持，启用时启动失败 | 未支持，启用时启动失败 | 未支持，启用时启动失败 | PostgreSQL `ExplainAnalyze` integration 与 bootstrap 校验 |
| 连接级 timeout | 未独立验证（`statement_timeout`） | 未独立验证（`max_execution_time`） | 未独立验证（`ob_query_timeout`） | 未独立验证（`statement_timeout`，毫秒） | provider DSN/runtime 参数；数据库触发路径尚未独立 integration |
| 应用 context timeout | 已实现 | 已实现 | 已实现 | 已实现 | 统一 bootstrap/tool 执行链 |
| `sql_safe_updates` | 不适用 | 默认注入 | MySQL 协议路径注入 | 不适用 | `x/providers/mysql/adapter.go` |
| scan row cap | 无 | 能力声明；不作为扫描硬保证 | 能力声明；不作为扫描硬保证 | 无 | 应用无法跨方言证明实际扫描行数 |
| resource manager | 无 | 无 | 外部配置 | 无（warehouse/查询队列由实例侧管理） | 由 OceanBase DBA 配置，本服务不代管 |

## Hologres 专列说明

- 连接：pgx 驱动、PostgreSQL wire 协议（服务端 lineage 11），凭据为 RAM
  AccessKey；默认 simple query protocol（`default_query_exec_mode` 可覆盖），
  并注入 `application_name=sql-mcp-server` 便于慢查询诊断归因。
- 事务：Hologres 事务仅覆盖 DDL，对数据语句不提供原子性；capability
  `Transaction=false`，事务工具 fail-closed，不存在静默降级。
- 目录：表发现走 `pg_catalog`（隐藏 LIST 分区子表与外部表），列与主键走
  `information_schema`；内部 schema（`hologres`、`hologres_statistic` 等）
  只有被显式列为实体 source 时才会暴露。

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
make test-integration-hologres   # 需要 HOLOGRES_TEST_DSN（无官方镜像，用真实实例）
make test-e2e
```

固定测试版本见 [支持版本](supported-versions.md)。默认单元测试不等价于上述真实
数据库和 MCP e2e。
