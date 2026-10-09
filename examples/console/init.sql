-- Demo data for the admin console: a multi-tenant e-commerce SaaS.
-- Every table and column carries a comment; the console shows them as the
-- default descriptions and the server serves them to agents unless the
-- configuration writes its own. Data is generated deterministically and is
-- fictional: names are placeholders, phone and ID numbers use ranges that are
-- never issued.
--
-- shop database
--   crm      tenants, customers (PII), support tickets
--   sales    products, orders, order items, refunds, daily sales view
--   finance  invoices, payments          (not in config.yaml: try importing)
--   ops      warehouses, shipments        (not in config.yaml: try importing)
--   archive  orders                      (same name as sales.orders)
-- analytics database
--   public   daily KPIs                   (second datasource)

SELECT setseed(0.42);

CREATE SCHEMA crm;
CREATE SCHEMA sales;
CREATE SCHEMA finance;
CREATE SCHEMA ops;
CREATE SCHEMA archive;
COMMENT ON SCHEMA crm IS '客户关系';
COMMENT ON SCHEMA sales IS '交易';
COMMENT ON SCHEMA finance IS '财务';
COMMENT ON SCHEMA ops IS '履约';
COMMENT ON SCHEMA archive IS '历史归档';

-- crm ------------------------------------------------------------------------

CREATE TABLE crm.tenants (
  id         integer PRIMARY KEY,
  name       text NOT NULL,
  plan       text NOT NULL,
  created_at timestamptz NOT NULL
);
COMMENT ON TABLE crm.tenants IS '租户（入驻商家）';
COMMENT ON COLUMN crm.tenants.id IS '租户 ID';
COMMENT ON COLUMN crm.tenants.name IS '商家名称';
COMMENT ON COLUMN crm.tenants.plan IS '订阅套餐：free / pro / enterprise';
COMMENT ON COLUMN crm.tenants.created_at IS '入驻时间';

INSERT INTO crm.tenants VALUES
  (1, '示例商户 A', 'enterprise', '2023-02-01'),
  (2, '示例商户 B', 'pro', '2023-06-15'),
  (3, '示例商户 C', 'free', '2024-03-08');

CREATE TABLE crm.customers (
  id         integer PRIMARY KEY,
  tenant_id  integer NOT NULL REFERENCES crm.tenants (id),
  name       text NOT NULL,
  email      text,
  phone      text,
  id_card    text,
  region     text NOT NULL,
  vip_level  smallint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL
);
COMMENT ON TABLE crm.customers IS '终端客户，含个人信息';
COMMENT ON COLUMN crm.customers.id IS '客户 ID';
COMMENT ON COLUMN crm.customers.tenant_id IS '所属租户';
COMMENT ON COLUMN crm.customers.name IS '姓名';
COMMENT ON COLUMN crm.customers.email IS '邮箱';
COMMENT ON COLUMN crm.customers.phone IS '手机号';
COMMENT ON COLUMN crm.customers.id_card IS '身份证号';
COMMENT ON COLUMN crm.customers.region IS '销售大区';
COMMENT ON COLUMN crm.customers.vip_level IS '会员等级 0-5';
COMMENT ON COLUMN crm.customers.created_at IS '注册时间';
CREATE INDEX ON crm.customers (tenant_id, region);
CREATE INDEX customers_vip ON crm.customers (tenant_id, vip_level) WHERE vip_level >= 3;

INSERT INTO crm.customers
SELECT i,
       1 + (i % 3),
       '客户' || lpad(i::text, 5, '0'),
       'user' || i || '@example.com',
       '100' || lpad(((i * 7919) % 100000000)::text, 8, '0'),
       '999999' || (1970 + i % 35)::text || lpad((1 + i % 12)::text, 2, '0') ||
         lpad((1 + i % 28)::text, 2, '0') || lpad((i % 1000)::text, 3, '0') || (i % 10)::text,
       (ARRAY['华东','华南','华北','西南','西北'])[1 + (i * 3) % 5],
       (i * 13) % 6,
       timestamptz '2023-01-01' + (i * interval '4 hours')
FROM generate_series(1, 3000) AS i;
-- Unique keys that do not identify a row: over a nullable column, partial,
-- on an expression.
ALTER TABLE crm.customers ADD UNIQUE (tenant_id, email);
CREATE UNIQUE INDEX customers_id_card_key ON crm.customers (id_card) WHERE id_card IS NOT NULL;
CREATE UNIQUE INDEX customers_email_lower_key ON crm.customers (lower(email));

CREATE TABLE crm.support_tickets (
  id          integer PRIMARY KEY,
  tenant_id   integer NOT NULL REFERENCES crm.tenants (id),
  customer_id integer NOT NULL REFERENCES crm.customers (id),
  subject     text NOT NULL,
  status      text NOT NULL,
  priority    text NOT NULL,
  created_at  timestamptz NOT NULL,
  closed_at   timestamptz
);
COMMENT ON TABLE crm.support_tickets IS '客服工单';
COMMENT ON COLUMN crm.support_tickets.id IS '工单 ID';
COMMENT ON COLUMN crm.support_tickets.tenant_id IS '所属租户';
COMMENT ON COLUMN crm.support_tickets.customer_id IS '提单客户';
COMMENT ON COLUMN crm.support_tickets.subject IS '问题摘要';
COMMENT ON COLUMN crm.support_tickets.status IS '状态：open / pending / closed';
COMMENT ON COLUMN crm.support_tickets.priority IS '优先级：low / normal / urgent';
COMMENT ON COLUMN crm.support_tickets.created_at IS '创建时间';
COMMENT ON COLUMN crm.support_tickets.closed_at IS '关闭时间';

INSERT INTO crm.support_tickets
SELECT i, c.tenant_id, c.id,
       (ARRAY['物流太慢','商品破损','申请退款','发票抬头修改','优惠券无法使用','更换收货地址'])[1 + i % 6],
       (ARRAY['open','pending','closed','closed'])[1 + i % 4],
       (ARRAY['low','normal','normal','urgent'])[1 + (i * 5) % 4],
       timestamptz '2024-01-01' + (i * interval '3 hours'),
       CASE WHEN i % 4 >= 2 THEN timestamptz '2024-01-02' + (i * interval '3 hours') END
FROM generate_series(1, 1200) AS i
JOIN crm.customers c ON c.id = 1 + (i * 37) % 3000;
CREATE INDEX support_tickets_subject_fts ON crm.support_tickets USING gin (to_tsvector('simple', subject));

-- sales ----------------------------------------------------------------------

CREATE TABLE sales.products (
  id         integer PRIMARY KEY,
  tenant_id  integer NOT NULL REFERENCES crm.tenants (id),
  sku        text NOT NULL UNIQUE,
  name       text NOT NULL,
  category   text NOT NULL,
  price      numeric(10, 2) NOT NULL,
  cost_price numeric(10, 2) NOT NULL,
  active     boolean NOT NULL DEFAULT true
);
COMMENT ON TABLE sales.products IS '商品';
COMMENT ON COLUMN sales.products.id IS '商品 ID';
COMMENT ON COLUMN sales.products.tenant_id IS '所属租户';
COMMENT ON COLUMN sales.products.sku IS 'SKU 编码';
COMMENT ON COLUMN sales.products.name IS '商品名称';
COMMENT ON COLUMN sales.products.category IS '类目';
COMMENT ON COLUMN sales.products.price IS '售价（元）';
COMMENT ON COLUMN sales.products.cost_price IS '成本价（元），商业敏感';
COMMENT ON COLUMN sales.products.active IS '是否在售';

INSERT INTO sales.products
SELECT i, 1 + (i % 3), 'SKU-' || lpad(i::text, 5, '0'),
       (ARRAY['有机燕麦','手冲咖啡壶','蓝牙耳机','机械键盘','全麦吐司','羊毛围巾','保温杯','香薰蜡烛'])[1 + i % 8] ||
         ' ' || (ARRAY['标准款','升级款','礼盒装','家庭装'])[1 + (i / 8) % 4],
       (ARRAY['食品','家居','数码','数码','食品','服饰','家居','家居'])[1 + i % 8],
       round((19 + (i * 37) % 900)::numeric, 2),
       round((19 + (i * 37) % 900)::numeric * 0.62, 2),
       i % 17 <> 0
FROM generate_series(1, 240) AS i;

CREATE TABLE sales.orders (
  id          integer PRIMARY KEY,
  tenant_id   integer NOT NULL REFERENCES crm.tenants (id),
  customer_id integer NOT NULL REFERENCES crm.customers (id),
  status      text NOT NULL,
  amount      numeric(12, 2) NOT NULL,
  currency    text NOT NULL DEFAULT 'CNY',
  region      text NOT NULL,
  channel     text NOT NULL,
  created_at  timestamptz NOT NULL,
  paid_at     timestamptz
);
COMMENT ON TABLE sales.orders IS '订单';
COMMENT ON COLUMN sales.orders.id IS '订单 ID';
COMMENT ON COLUMN sales.orders.tenant_id IS '所属租户';
COMMENT ON COLUMN sales.orders.customer_id IS '下单客户';
COMMENT ON COLUMN sales.orders.status IS '状态：created / paid / shipped / completed / cancelled';
COMMENT ON COLUMN sales.orders.amount IS '金额';
COMMENT ON COLUMN sales.orders.currency IS '币种';
COMMENT ON COLUMN sales.orders.region IS '收货大区';
COMMENT ON COLUMN sales.orders.channel IS '渠道：app / web / mini_program / offline';
COMMENT ON COLUMN sales.orders.created_at IS '下单时间';
COMMENT ON COLUMN sales.orders.paid_at IS '支付时间';
CREATE INDEX ON sales.orders (tenant_id, created_at);
CREATE INDEX ON sales.orders (customer_id);
CREATE INDEX orders_created_brin ON sales.orders USING brin (created_at);

INSERT INTO sales.orders
SELECT i, c.tenant_id, c.id,
       (ARRAY['created','paid','shipped','completed','completed','completed','cancelled'])[1 + (i * 3) % 7],
       round((30 + random() * 1500)::numeric, 2),
       'CNY', c.region,
       (ARRAY['app','app','web','mini_program','offline'])[1 + i % 5],
       timestamptz '2024-01-01' + (i * interval '11 minutes'),
       CASE WHEN (i * 3) % 7 BETWEEN 1 AND 5 THEN timestamptz '2024-01-01' + (i * interval '11 minutes') + interval '3 minutes' END
FROM generate_series(1, 40000) AS i
JOIN crm.customers c ON c.id = 1 + (i * 7) % 3000;

CREATE TABLE sales.order_items (
  id         integer PRIMARY KEY,
  order_id   integer NOT NULL REFERENCES sales.orders (id),
  product_id integer NOT NULL REFERENCES sales.products (id),
  quantity   integer NOT NULL,
  unit_price numeric(10, 2) NOT NULL,
  discount   numeric(10, 2) NOT NULL DEFAULT 0
);
COMMENT ON TABLE sales.order_items IS '订单明细';
COMMENT ON COLUMN sales.order_items.id IS '明细 ID';
COMMENT ON COLUMN sales.order_items.order_id IS '所属订单';
COMMENT ON COLUMN sales.order_items.product_id IS '商品';
COMMENT ON COLUMN sales.order_items.quantity IS '数量';
COMMENT ON COLUMN sales.order_items.unit_price IS '成交单价（元）';
COMMENT ON COLUMN sales.order_items.discount IS '优惠金额（元）';
CREATE INDEX ON sales.order_items (order_id);

INSERT INTO sales.order_items
SELECT row_number() OVER (), o.id, 1 + (o.id * 5 + n * 11) % 240, 1 + (o.id + n) % 3,
       round((19 + ((o.id * 5 + n * 11) % 240) * 3)::numeric, 2), CASE WHEN n = 1 AND o.id % 4 = 0 THEN 10 ELSE 0 END
FROM sales.orders o, generate_series(1, 1 + o.id % 3) AS n;

-- New refunds (support may create them) get their id and time from defaults.
CREATE TABLE sales.refunds (
  id         integer GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  order_id   integer NOT NULL REFERENCES sales.orders (id),
  amount     numeric(12, 2) NOT NULL,
  reason     text NOT NULL,
  status     text NOT NULL DEFAULT 'requested',
  created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE sales.refunds IS '退款记录';
COMMENT ON COLUMN sales.refunds.id IS '退款 ID';
COMMENT ON COLUMN sales.refunds.order_id IS '原订单';
COMMENT ON COLUMN sales.refunds.amount IS '退款金额（元）';
COMMENT ON COLUMN sales.refunds.reason IS '退款原因';
COMMENT ON COLUMN sales.refunds.status IS '状态：requested / approved / rejected / paid';
COMMENT ON COLUMN sales.refunds.created_at IS '申请时间';

INSERT INTO sales.refunds
SELECT row_number() OVER (), o.id, round(o.amount * 0.5, 2),
       (ARRAY['七天无理由','商品破损','发错货','物流超时'])[1 + o.id % 4],
       (ARRAY['requested','approved','paid','paid','rejected'])[1 + o.id % 5],
       o.created_at + interval '2 days'
FROM sales.orders o WHERE o.status = 'completed' AND o.id % 9 = 0;
SELECT setval(pg_get_serial_sequence('sales.refunds', 'id'), max(id)) FROM sales.refunds;

CREATE VIEW sales.v_daily_sales AS
SELECT tenant_id, created_at::date AS day, count(*) AS orders, sum(amount) AS revenue
FROM sales.orders WHERE status <> 'cancelled'
GROUP BY tenant_id, created_at::date;
COMMENT ON VIEW sales.v_daily_sales IS '每日销售汇总（不含已取消订单）';
COMMENT ON COLUMN sales.v_daily_sales.tenant_id IS '所属租户';
COMMENT ON COLUMN sales.v_daily_sales.day IS '日期';
COMMENT ON COLUMN sales.v_daily_sales.orders IS '订单数';
COMMENT ON COLUMN sales.v_daily_sales.revenue IS '销售额（元）';

-- finance (not configured; import it from the console) -------------------------

CREATE TABLE finance.invoices (
  id         integer PRIMARY KEY,
  tenant_id  integer NOT NULL REFERENCES crm.tenants (id),
  order_id   integer NOT NULL REFERENCES sales.orders (id),
  invoice_no text NOT NULL UNIQUE,
  title      text NOT NULL,
  amount     numeric(12, 2) NOT NULL,
  tax_amount numeric(12, 2) NOT NULL,
  issued_at  timestamptz NOT NULL
);
COMMENT ON TABLE finance.invoices IS '发票';
COMMENT ON COLUMN finance.invoices.id IS '发票 ID';
COMMENT ON COLUMN finance.invoices.tenant_id IS '所属租户';
COMMENT ON COLUMN finance.invoices.order_id IS '关联订单';
COMMENT ON COLUMN finance.invoices.invoice_no IS '发票号码';
COMMENT ON COLUMN finance.invoices.title IS '发票抬头';
COMMENT ON COLUMN finance.invoices.amount IS '价税合计（元）';
COMMENT ON COLUMN finance.invoices.tax_amount IS '税额（元）';
COMMENT ON COLUMN finance.invoices.issued_at IS '开票时间';

INSERT INTO finance.invoices
SELECT row_number() OVER (), o.tenant_id, o.id, 'INV' || lpad(o.id::text, 8, '0'),
       c.name || '（个人）', o.amount, round(o.amount * 0.13 / 1.13, 2), o.paid_at + interval '1 day'
FROM sales.orders o JOIN crm.customers c ON c.id = o.customer_id
WHERE o.status = 'completed' AND o.id % 3 = 0;

CREATE TABLE finance.payments (
  id              integer PRIMARY KEY,
  order_id        integer NOT NULL REFERENCES sales.orders (id),
  method          text NOT NULL,
  amount          numeric(12, 2) NOT NULL,
  card_last4      text,
  provider_txn_id text NOT NULL,
  paid_at         timestamptz NOT NULL
);
COMMENT ON TABLE finance.payments IS '支付流水';
COMMENT ON COLUMN finance.payments.id IS '流水 ID';
COMMENT ON COLUMN finance.payments.order_id IS '关联订单';
COMMENT ON COLUMN finance.payments.method IS '支付方式：wallet / bank_transfer / card';
COMMENT ON COLUMN finance.payments.amount IS '支付金额（元）';
COMMENT ON COLUMN finance.payments.card_last4 IS '银行卡后四位';
COMMENT ON COLUMN finance.payments.provider_txn_id IS '支付渠道流水号';
COMMENT ON COLUMN finance.payments.paid_at IS '支付时间';

INSERT INTO finance.payments
SELECT row_number() OVER (), o.id, (ARRAY['wallet','bank_transfer','card'])[1 + o.id % 3], o.amount,
       CASE WHEN o.id % 3 = 2 THEN lpad((o.id % 10000)::text, 4, '0') END,
       'TXN' || md5(o.id::text), o.paid_at
FROM sales.orders o WHERE o.paid_at IS NOT NULL;
ALTER TABLE finance.payments ADD UNIQUE (provider_txn_id);

-- ops (not configured; import it from the console) -----------------------------

CREATE TABLE ops.warehouses (
  id     integer PRIMARY KEY,
  name   text NOT NULL,
  region text NOT NULL
);
COMMENT ON TABLE ops.warehouses IS '仓库';
COMMENT ON COLUMN ops.warehouses.id IS '仓库 ID';
COMMENT ON COLUMN ops.warehouses.name IS '仓库名称';
COMMENT ON COLUMN ops.warehouses.region IS '覆盖大区';

INSERT INTO ops.warehouses VALUES
  (1, '华东一号仓', '华东'), (2, '华南一号仓', '华南'), (3, '华北一号仓', '华北'),
  (4, '西南一号仓', '西南'), (5, '西北一号仓', '西北');

CREATE TABLE ops.shipments (
  id           integer PRIMARY KEY,
  order_id     integer NOT NULL REFERENCES sales.orders (id),
  warehouse_id integer NOT NULL REFERENCES ops.warehouses (id),
  carrier      text NOT NULL,
  tracking_no  text NOT NULL,
  status       text NOT NULL,
  shipped_at   timestamptz NOT NULL,
  delivered_at timestamptz
);
COMMENT ON TABLE ops.shipments IS '发货单';
COMMENT ON COLUMN ops.shipments.id IS '发货单 ID';
COMMENT ON COLUMN ops.shipments.order_id IS '关联订单';
COMMENT ON COLUMN ops.shipments.warehouse_id IS '发货仓';
COMMENT ON COLUMN ops.shipments.carrier IS '承运商';
COMMENT ON COLUMN ops.shipments.tracking_no IS '运单号';
COMMENT ON COLUMN ops.shipments.status IS '状态：in_transit / delivered / returned';
COMMENT ON COLUMN ops.shipments.shipped_at IS '发货时间';
COMMENT ON COLUMN ops.shipments.delivered_at IS '签收时间';

INSERT INTO ops.shipments
SELECT row_number() OVER (), o.id,
       array_position(ARRAY['华东','华南','华北','西南','西北'], o.region),
       (ARRAY['承运商 A','承运商 B','承运商 C'])[1 + o.id % 3], 'TRK' || lpad(o.id::text, 10, '0'),
       CASE WHEN o.status = 'shipped' THEN 'in_transit' ELSE 'delivered' END,
       o.paid_at + interval '1 day',
       CASE WHEN o.status = 'completed' THEN o.paid_at + interval '3 days' END
FROM sales.orders o WHERE o.status IN ('shipped', 'completed');
-- Two composite unique keys sharing a column.
ALTER TABLE ops.shipments ADD UNIQUE (carrier, tracking_no);
ALTER TABLE ops.shipments ADD UNIQUE (order_id, carrier);
CREATE INDEX shipments_tracking_hash ON ops.shipments USING hash (tracking_no);

-- archive: same table name as sales.orders, told apart by schema -----------------

CREATE TABLE archive.orders (
  id          integer PRIMARY KEY,
  tenant_id   integer NOT NULL,
  customer_id integer NOT NULL,
  amount      numeric(12, 2) NOT NULL,
  created_at  timestamptz NOT NULL
);
COMMENT ON TABLE archive.orders IS '2023 年订单归档（只读）';
COMMENT ON COLUMN archive.orders.id IS '订单 ID';
COMMENT ON COLUMN archive.orders.tenant_id IS '所属租户';
COMMENT ON COLUMN archive.orders.customer_id IS '下单客户';
COMMENT ON COLUMN archive.orders.amount IS '金额（元）';
COMMENT ON COLUMN archive.orders.created_at IS '下单时间';

INSERT INTO archive.orders
SELECT i, 1 + i % 3, 1 + (i * 11) % 3000, round((30 + random() * 1200)::numeric, 2),
       timestamptz '2023-01-01' + (i * interval '35 minutes')
FROM generate_series(1, 15000) AS i;

ANALYZE;

-- analytics: a second datasource ------------------------------------------------

CREATE DATABASE analytics;
\connect analytics

CREATE TABLE daily_kpi (
  day          date NOT NULL,
  tenant_id    integer NOT NULL,
  gmv          numeric(14, 2) NOT NULL,
  orders       integer NOT NULL,
  new_buyers   integer NOT NULL,
  refund_rate  numeric(5, 4) NOT NULL,
  PRIMARY KEY (day, tenant_id)
);
COMMENT ON TABLE daily_kpi IS '经营日报（离线计算，T+1）';
COMMENT ON COLUMN daily_kpi.day IS '日期';
COMMENT ON COLUMN daily_kpi.tenant_id IS '租户 ID';
COMMENT ON COLUMN daily_kpi.gmv IS '成交总额（元）';
COMMENT ON COLUMN daily_kpi.orders IS '订单数';
COMMENT ON COLUMN daily_kpi.new_buyers IS '新增买家数';
COMMENT ON COLUMN daily_kpi.refund_rate IS '退款率';

SELECT setseed(0.7);
INSERT INTO daily_kpi
SELECT d::date, t, round((20000 + random() * 80000)::numeric, 2), 60 + (random() * 240)::int,
       5 + (random() * 40)::int, round((random() * 0.06)::numeric, 4)
FROM generate_series(date '2024-01-01', date '2024-12-31', interval '1 day') AS d, generate_series(1, 3) AS t;

ANALYZE;
