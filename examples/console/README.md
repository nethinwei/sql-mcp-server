# 控制台演示：多租户电商

用 Docker Compose 启动一个带示例数据的 PostgreSQL 和开启管理控制台的
sql-mcp-server。部署步骤见[管理控制台 · Docker 部署](../../docs/console.md#docker-部署)。

## 数据

`init.sql` 生成一个多租户电商 SaaS 的数据（3 个租户、3000 个客户、4 万个订单），
每张表和每一列都有数据库注释：

| 数据库 / schema | 表 | 是否已在 `config.yaml` 中配置 |
| --- | --- | --- |
| `shop` / `crm` | `tenants`、`customers`（含邮箱、手机号、身份证号）、`support_tickets` | 是 |
| `shop` / `sales` | `products`、`orders`、`order_items`、`refunds`，视图 `v_daily_sales` | 是（视图除外） |
| `shop` / `archive` | `orders`（与 `sales.orders` 同名） | 是，实体名 `archive_orders` |
| `shop` / `finance` | `invoices`、`payments` | 否：在控制台“数据源与导入”中导入 |
| `shop` / `ops` | `warehouses`、`shipments` | 否：同上 |
| `analytics` / `public` | `daily_kpi` | 是（第二个数据源） |

实体与字段的说明大多留空，运行时使用数据库注释；`sales.orders.amount` 写了自己的
说明，覆盖注释“金额”。

## 角色与用户

| 角色 | 内容 |
| --- | --- |
| `analyst` | 平台经营分析：跨租户读取、聚合交易与经营指标，看不到客户与成本价 |
| `support` | 商家客服：本租户的客户（脱敏）、订单与工单，可改工单状态、可新建退款 |
| `marketing` | 会员运营：客户分层字段，看不到联系方式 |
| `finance` | 平台财务：已支付订单、退款、含成本价的商品 |

`crm.customers` 与 `crm.support_tickets` 设置了租户硬边界
（`tenant_id = ${subject.tenant_id}`），任何角色都只能看到自己租户的数据。

以下 token **只用于本地演示**，部署前在控制台“用户”页重新生成：

| 用户 | 角色 | 租户 | token |
| --- | --- | --- | --- |
| `bi-agent` | analyst | 跨租户 | `smcp_MTSZ1FfrtGfi10ECZvMFORLjebhS1RfvYGl2TNgA8_s` |
| `support-agent` | support | 1 | `smcp_sOwYBj4c-yPlpE25X2Ma7UDt5HsHs5PgFC_ow5O27G0` |
| `support-agent-t2` | support | 2 | `smcp_hjLUmIu0MwoJQXy61MdJSevYyOBOzhDZSn0j-SGkuQo` |
| `crm-agent` | support + marketing | 1 | `smcp_e764Haa2RiJd8dH1OnHzeyUZLtsGufIjPW_L1i6_j-M` |
| `finance-bot` | finance，另直授 `archive_orders` | 跨租户 | `smcp_WwX5CLyiBP4uMCCG8JCrLFPzteuVOaDy37S9KqBJaBc` |
| `legacy-bot` | analyst，已停用 | — | `smcp_0iPLJ7bp1wmuNwZYtVIi7a7Hhtnv7gD1RJ3wI_k2LRg` |
| `new-agent` | analyst | — | 没有 token（概览页会提示） |

## 可以体验的场景

- **租户隔离**：`support-agent` 与 `support-agent-t2` 读取同一个客户 ID，只有所属
  租户能读到；读取订单并 `expand: [customer]` 时，其他租户的客户显示为 `null`。
- **脱敏**：客服读到的邮箱、手机号是 `u***@example.com`、`100****7919` 形式。示例
  中的商户、客户、手机号与身份证号都是虚构的，号段不会被真实分配。
- **写操作**：`support-agent` 可新建退款（`id`、`created_at` 由数据库默认生成），
  可修改本租户工单的 `status`、`priority`。
- **字段范围分散**：`crm-agent` 的两个角色对 `customers` 可读字段不同，不指定字段
  读取会返回 `AMBIGUOUS_FIELD_SCOPE` 并列出 `fieldScopes`，在一个范围内显式选字段
  即可；`sql-mcp://schema` 资源也给出同样的提示。
- **成本闸门**：`bi-agent` 不带过滤读取 `orders`（4 万行）会被拒绝并给出改写建议。
- **多数据源**：`bi-agent` 聚合 `daily_kpi`（`analytics` 库）。
- **停用用户**：`legacy-bot` 的请求返回 401。
- **控制台**：导入 `finance`、`ops` 两个 schema 的表；在“权限模拟”中对比各用户；
  修改角色后在“检查并发布”查看差异与需要重启的项。
