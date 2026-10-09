# 控制台演示：三种数据源

用 Docker Compose 启动三种受支持的数据库（PostgreSQL、MySQL、OceanBase，各带一套
示例业务与数据）和开启管理控制台的 sql-mcp-server。部署步骤见
[管理控制台 · Docker 部署](../../docs/console.md#docker-部署)。OceanBase 首次启动约需
3–5 分钟、约 6 GB 内存。

每个数据源是一套独立的业务，各自覆盖全部特性（见下方特性矩阵）；服务起来后运行
`python3 verify.py` 会通过 MCP 在三个数据源上逐项验证。

| 数据源 | 数据库 | 业务 | 初始化脚本 |
| --- | --- | --- | --- |
| `shop` | PostgreSQL | 多租户电商：3 个租户、3000 个客户、4 万个订单 | `init.sql` |
| `warehouse` | MySQL | 仓储：5 个仓库、240 个 SKU、5 万张运单 | `mysql-init.sql` |
| `ledger` | OceanBase（MySQL 模式） | 记账：12 个租户账户、5 万笔分录 | `ob-init.sql` |

每张表和每一列都有数据库注释，实体与字段的说明大多留空，运行时使用数据库注释。
每个数据源都留一个库（schema）不在 `config.yaml` 中：在控制台“数据源与导入”中导入。

## 特性矩阵

| 特性 | `shop`（PostgreSQL） | `warehouse`（MySQL） | `ledger`（OceanBase） |
| --- | --- | --- | --- |
| 多个库（schema）、按库导入 | `crm` `sales` `archive`，未配置 `finance` | `inventory` `logistics` `archive`，未配置 `purchasing` | `ledger` `billing` `archive`，未配置 `audit` |
| 同名表（同一数据源的不同库），实体同名 | `sales.orders` / `archive.orders` | `logistics.waybills` / `archive.waybills` | `ledger.entries` / `archive.entries`（实体另名 `archive_entries`） |
| 同名表（不同数据源），实体同名 | `crm.tenants` | `logistics.tenants` | `ledger.tenants` |
| 视图 | `sales.v_open_orders`（实体 `open_orders`） | `inventory.v_low_stock`（`low_stock`） | `ledger.v_accounts`（`account_overview`） |
| 物化视图 | `sales.mv_product_sales`（导入） | 数据库不支持 | 不演示 |
| 分区表（一张表对外） | `sales.daily_kpi` | `inventory.stock_daily` | `billing.statements` |
| 复合主键 | `daily_kpi (day, tenant_id)` | `stock (warehouse_id, sku)`（可写） | `statements (period_start, account_id)` |
| 数据库生成的主键 | `refunds`（identity） | `waybills`（auto_increment） | `entries`（auto_increment） |
| 不能定位行的唯一键：含可空列 | `customers (tenant_id, email)` | `items (gtin)` | `accounts (tenant_id, external_ref)` |
| ：表达式 | `customers (lower(email))` | `items ((lower(title)))` | `currencies ((lower(code)))` |
| ：部分索引 | `customers (id_card) WHERE …` | 数据库不支持 | 数据库不支持 |
| ：列前缀 | 数据库不支持 | `items (barcode(8))` | `accounts (account_no(8))` |
| 共享列的两个复合唯一键 | `order_items` | `waybills` | `billing.invoices` |
| 索引类型 | btree、hash、gin、brin，部分索引 | btree、全文、空间、前缀 | btree、全文、空间、前缀 |
| 外键 `ON DELETE CASCADE` | `order_items → orders` | `stock → items` | `entries → accounts` |
| 外键 `ON DELETE SET NULL`（跨库） | `support_tickets → sales.orders` | `waybills → carriers`；跨库 `waybills → inventory.warehouses` | `billing.invoices → ledger.accounts` |
| 外键 `ON UPDATE CASCADE` | `customers → tenants` | `stock → items` | `accounts → currencies` |
| 触发器（写入使整个库的缓存失效） | `support_tickets` 写 `ticket_events` | `stock` 写 `stock_log` | `entries` 更新 `accounts.balance` |
| 存储过程（独立 MCP 工具） | `crm.close_ticket` | `inventory.restock` | `ledger.post_entry` |
| 租户行策略 | `customers` `support_tickets` | `logistics.waybills` | `accounts` `entries` `account_overview` |
| 脱敏 | 邮箱、手机号、身份证号 | 收件人手机号 | 银行账号 |
| 关系展开 | `customers → tickets` | `items → stock` | `entries → account` |
| 读、聚合、新建、修改、删除、事务 | 是 | 是 | 是 |
| 成本闸门（大表无过滤读取被拒） | `sales.orders` | `archive.waybills` | `entries` |
| 两个账号：读走只读账号，写、过程与写后 5 秒的读走读写账号 | `shop_ro` / `shop_rw` | `warehouse_ro` / `warehouse_rw` | `ledger_ro` / `ledger_rw` |

## 角色与用户

| 角色 | 数据源 | 内容 |
| --- | --- | --- |
| `analyst` | `shop` | 平台经营分析：跨租户读取、聚合交易与经营指标，看不到客户与成本价 |
| `support` | `shop` | 商家客服：本租户的客户（脱敏）、订单与工单，可改工单、关闭工单、新建退款 |
| `marketing` | `shop` | 会员运营：客户分层字段，看不到联系方式 |
| `finance` | `shop` | 平台财务：已支付订单、退款（可新建、删除）、含成本价的商品 |
| `ops` | `warehouse` | 仓储运营：商品与库存、补货、本货主的运单（收件人手机号脱敏） |
| `bookkeeper` | `ledger` | 记账：本租户的账户（账号脱敏）、分录与对账单 |

以下 token **只用于本地演示**，部署前在控制台“用户”页重新生成：

| 用户 | 角色 | 租户 | token |
| --- | --- | --- | --- |
| `bi-agent` | analyst | 跨租户 | `smcp_MTSZ1FfrtGfi10ECZvMFORLjebhS1RfvYGl2TNgA8_s` |
| `support-agent` | support | 1 | `smcp_sOwYBj4c-yPlpE25X2Ma7UDt5HsHs5PgFC_ow5O27G0` |
| `support-agent-t2` | support | 2 | `smcp_hjLUmIu0MwoJQXy61MdJSevYyOBOzhDZSn0j-SGkuQo` |
| `crm-agent` | support + marketing | 1 | `smcp_e764Haa2RiJd8dH1OnHzeyUZLtsGufIjPW_L1i6_j-M` |
| `finance-bot` | finance，另直授 `archive.orders` | 跨租户 | `smcp_WwX5CLyiBP4uMCCG8JCrLFPzteuVOaDy37S9KqBJaBc` |
| `ops-agent` | ops | 1 | `smcp_9yrxe6J6_IJZZrzH-BOCuyBFJArH8bxWziaUDJh8GYY` |
| `ledger-agent` | bookkeeper | 1 | `smcp_T8f0enbMwGVv40UZ3xpzdM70kwLjfde4VPBgtKL-43Q` |
| `legacy-bot` | analyst，已停用 | — | `smcp_0iPLJ7bp1wmuNwZYtVIi7a7Hhtnv7gD1RJ3wI_k2LRg` |
| `new-agent` | analyst | — | 没有 token（概览页会提示） |

## 可以体验的场景

- **逐项验证**：`python3 verify.py`（默认连 `http://127.0.0.1:8080/mcp`，用
  `SMCP_MCP_URL` 改），三个数据源各 18 项，另有 3 项实体命名。
- **实体命名**：实体按 `数据源.库.名称` 识别，名称默认就是表名，只需在同一数据源
  与库内唯一：三个数据源各有一个 `tenants` 实体，`shop` 的 `sales`、`archive` 库各有
  一个 `orders`。引用（授权、关系、`affects` 与 Agent 调用的 `entity`）写能唯一确定
  实体的最短后缀，如 `crm.tenants`、`archive.orders`；有歧义时发布被拒，控制台会自动
  补全限定。Agent 只在自己能访问的实体中解析：`bi-agent` 只能访问 `shop`，直接写
  `tenants` 即可，写 `orders` 则返回 `AMBIGUOUS_ENTITY` 并列出 `sales.orders`、
  `archive.orders`。`ledger` 的数据源与库同名，`ledger.entries` 会同时指向两张
  `entries`，因此归档表另名 `archive_entries`，演示实体名与表名不同。
- **租户隔离**：`support-agent` 与 `support-agent-t2` 读取同一个客户 ID，只有所属
  租户能读到；`ops-agent`、`ledger-agent` 同样只能读到租户 1 的运单与账户。
- **脱敏**：邮箱、手机号显示为 `u***@example.com`、`100****7919` 形式，银行账号
  整体隐藏。示例中的号码都是虚构的，号段不会被真实分配。
- **存储过程**：`support-agent` 调用 `procedure_close_ticket_*`，`ops-agent` 调用
  `procedure_restock_*`，`ledger-agent` 调用 `procedure_post_entry_*`；过程声明了
  影响的实体，随后的读取不会命中旧缓存（记账过程经触发器更新余额也一样）。
- **读写分离**：新建一条记录后立即读取，5 秒内本会话的读取走读写账号，能读到
  刚写的数据；之后回到只读账号。
- **字段范围分散**：`crm-agent` 的两个角色对 `customers` 可读字段不同，不指定字段
  读取会返回 `AMBIGUOUS_FIELD_SCOPE` 并列出 `fieldScopes`。
- **成本闸门**：不带过滤读取大表会被拒绝并给出改写建议；聚合必须带过滤条件。
- **停用用户**：`legacy-bot` 的请求返回 401。
- **控制台**：导入各数据源未配置的库，在索引表里看各数据库的索引类型与不能定位行
  的唯一键；在“权限模拟”中对比各用户；修改后在“检查并发布”查看差异。
