# commerce-core 语义说明

- **grain**：`orders` 一行一订单；`order_items` 一行一订单内商品明细
  （更细粒度，金额汇总必须选对层级）。
- **时间**：`created_at` / `confirmed_at` / `cancelled_at` /
  `completed_at` 是不同生命周期时间；"某月订单"默认按 `created_at`。
- **单位**：金额一律最小货币单位整数（`*_minor` + `currency_code`）；
  USD/CNY 缩放 2 位，KRW 缩放 0 位（见 `currencies.minor_unit_scale`）。
- **状态**：`orders.status ∈ created | confirmed | cancelled |
  completed`；订单存在不代表支付成功（见 payment-orchestration）。
- **价格版本**：现价与历史价分离——`product_price_versions` 按
  `[valid_from, valid_to)` 生效，`order_items.unit_price_minor` 固化
  下单时价格，禁止用现价重算历史。
- **相似实体陷阱**：`users` 是平台登录账号（员工/运营），交易主体是
  `customers`。
- **逻辑删除**：`customers.deleted_at` 非 NULL 即已删除，活跃客户统计
  必须排除。
