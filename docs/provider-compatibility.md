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
| aggregate | 核心层验证 | 核心层验证 | 核心层验证 | [`tool_aggregate_test.go`](../core/tool/tool_aggregate_test.go) |
| procedure | 真实数据库验证 | 真实数据库验证 | 真实数据库验证 | 三库 integration 的 `Test*ExecuteProcedure` |
| 显式事务 | 真实数据库验证（MCP e2e） | 核心层验证 | 核心层验证 | [`e2e_test.go`](../x/mcpserver/e2e_test.go) 与 store/provider 契约 |
| keyset cursor | 能力声明 + 核心层验证 | 能力声明 + 核心层验证 | 能力声明 + 核心层验证 | 各 provider `dialect.go` 与 codegen 测试 |

## 成本与资源控制

| 能力 | PostgreSQL | MySQL | OceanBase | 依据与边界 |
|---|---|---|---|---|
| EXPLAIN 估算 | 准确模式，真实数据库验证 | 保守 fail-closed，真实数据库验证 | 保守 fail-closed，真实数据库验证 | `Test*CostGate` / `Test*ReadPKWhitelist` 及 [安全模型](security.md#成本闸门) |
| EXPLAIN ANALYZE feedback | 支持，默认关闭 | 未支持，启用时启动失败 | 未支持，启用时启动失败 | PostgreSQL `ExplainAnalyze` integration 与 bootstrap 校验 |
| 连接级 timeout | 未独立验证（`statement_timeout`） | 未独立验证（`max_execution_time`） | 未独立验证（`ob_query_timeout`） | provider DSN/runtime 参数；数据库触发路径尚未独立 integration |
| 应用 context timeout | 已实现 | 已实现 | 已实现 | 统一 bootstrap/tool 执行链 |
| `sql_safe_updates` | 不适用 | 默认注入 | MySQL 协议路径注入 | `x/providers/mysql/adapter.go` |
| scan row cap | 无 | 能力声明；不作为扫描硬保证 | 能力声明；不作为扫描硬保证 | 应用无法跨方言证明实际扫描行数 |
| resource manager | 无 | 无 | 外部配置 | 由 OceanBase DBA 配置，本服务不代管 |

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
