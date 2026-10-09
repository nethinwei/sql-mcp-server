-- warehouse datasource (MySQL 8): a warehousing business across databases
-- (MySQL databases are the schemas the console lists and imports by).
--
--   inventory   warehouses (spatial), items (full-text, prefix and expression
--               keys), stock (composite key, trigger), stock log, daily stock
--               (partitioned), low stock view, procedure restock
--   logistics   tenants (same table name as in the shop and ledger
--               datasources), carriers, waybills (large; tenant rows, phone
--               numbers)
--   archive     waybills                (same name as logistics.waybills)
--   purchasing  suppliers, purchase orders   (not in config.yaml: try importing)
--
-- The features each datasource shows are listed in README.md; the gateway
-- reaches these databases through two accounts, warehouse_ro and
-- warehouse_rw (end of file).

-- The comments and data are UTF-8: clients default to latin1 otherwise.
SET NAMES utf8mb4;

CREATE DATABASE inventory CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE logistics CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE archive CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE purchasing CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- 0..99999, to generate rows without recursion limits.
CREATE TABLE purchasing.seq (n int PRIMARY KEY);
INSERT INTO purchasing.seq
SELECT a.d + 10 * b.d + 100 * c.d + 1000 * d.d + 10000 * e.d
FROM (SELECT 0 d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
      UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) a,
     (SELECT 0 d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
      UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) b,
     (SELECT 0 d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
      UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) c,
     (SELECT 0 d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
      UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d,
     (SELECT 0 d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
      UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) e;

-- inventory -------------------------------------------------------------------

USE inventory;

CREATE TABLE warehouses (
  id       int PRIMARY KEY COMMENT '仓库 ID',
  name     varchar(64) NOT NULL COMMENT '仓库名称',
  region   varchar(16) NOT NULL COMMENT '覆盖大区',
  location point NOT NULL SRID 0 COMMENT '坐标（经度、纬度）',
  SPATIAL INDEX warehouses_location (location)
) COMMENT '仓库';

INSERT INTO warehouses VALUES
  (1, '华东一号仓', '华东', ST_PointFromText('POINT(121.47 31.23)')),
  (2, '华南一号仓', '华南', ST_PointFromText('POINT(113.26 23.13)')),
  (3, '华北一号仓', '华北', ST_PointFromText('POINT(116.40 39.90)')),
  (4, '西南一号仓', '西南', ST_PointFromText('POINT(104.07 30.67)')),
  (5, '西北一号仓', '西北', ST_PointFromText('POINT(108.94 34.34)'));

-- Unique keys that do not identify a row: on a column prefix, on an
-- expression, over a nullable column.
CREATE TABLE items (
  sku         varchar(32) PRIMARY KEY COMMENT 'SKU 编码',
  title       varchar(200) NOT NULL COMMENT '商品标题',
  description text COMMENT '商品描述',
  barcode     varchar(64) NOT NULL COMMENT '条码（前 8 位为厂商码）',
  gtin        varchar(14) COMMENT '国际贸易编码（可空）',
  UNIQUE KEY items_vendor (barcode(8)),
  UNIQUE KEY items_title_lower ((lower(title))),
  UNIQUE KEY items_gtin (gtin),
  KEY items_title_prefix (title(10)),
  FULLTEXT KEY items_text (title, description)
) COMMENT '商品主数据';

INSERT INTO items
SELECT CONCAT('SKU-', LPAD(n, 5, '0')),
       CONCAT(ELT(1 + n % 8, '有机燕麦', '手冲咖啡壶', '蓝牙耳机', '机械键盘', '全麦吐司', '羊毛围巾', '保温杯', '香薰蜡烛'),
              ' ', ELT(1 + (n DIV 8) % 4, '标准款', '升级款', '礼盒装', '家庭装'), ' #', n),
       CONCAT('适合', ELT(1 + n % 3, '日常', '送礼', '办公'), '使用'),
       CONCAT(LPAD(n, 8, '0'), LPAD(n * 7919 % 100000, 5, '0')),
       IF(n % 4 = 0, NULL, CONCAT('069', LPAD(n, 11, '0')))
FROM purchasing.seq WHERE n BETWEEN 1 AND 240;

-- Deleting an item deletes its stock; renaming a SKU renames it in stock.
CREATE TABLE stock (
  warehouse_id int NOT NULL COMMENT '仓库',
  sku          varchar(32) NOT NULL COMMENT 'SKU',
  quantity     int NOT NULL COMMENT '库存数量',
  updated_at   datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (warehouse_id, sku),
  FOREIGN KEY (warehouse_id) REFERENCES warehouses (id),
  FOREIGN KEY (sku) REFERENCES items (sku) ON DELETE CASCADE ON UPDATE CASCADE
) COMMENT '库存（仓库 × SKU）';

INSERT INTO stock (warehouse_id, sku, quantity)
SELECT w.id, i.sku, (w.id * 37 + CAST(SUBSTRING(i.sku, 5) AS UNSIGNED) * 13) % 500
FROM warehouses w JOIN items i ON CAST(SUBSTRING(i.sku, 5) AS UNSIGNED) % 5 <> w.id - 1;

-- A stock change is recorded by a trigger: a write to stock may change
-- stock_log too, so it invalidates the whole database's cache.
CREATE TABLE stock_log (
  id           bigint AUTO_INCREMENT PRIMARY KEY COMMENT '日志 ID',
  warehouse_id int NOT NULL COMMENT '仓库',
  sku          varchar(32) NOT NULL COMMENT 'SKU',
  delta        int NOT NULL COMMENT '变化量',
  at           datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '时间'
) COMMENT '库存变动日志（由触发器写入）';

CREATE TRIGGER stock_changed AFTER UPDATE ON stock FOR EACH ROW
  INSERT INTO stock_log (warehouse_id, sku, delta) VALUES (NEW.warehouse_id, NEW.sku, NEW.quantity - OLD.quantity);

-- Daily stock, partitioned by half-year: one table to the gateway.
CREATE TABLE stock_daily (
  day          date NOT NULL COMMENT '日期',
  warehouse_id int NOT NULL COMMENT '仓库',
  inbound      int NOT NULL COMMENT '入库件数',
  outbound     int NOT NULL COMMENT '出库件数',
  PRIMARY KEY (day, warehouse_id)
) COMMENT '仓库日报（按半年分区）'
PARTITION BY RANGE COLUMNS (day) (
  PARTITION p2024h1 VALUES LESS THAN ('2024-07-01'),
  PARTITION p2024h2 VALUES LESS THAN ('2025-01-01')
);

INSERT INTO stock_daily
SELECT DATE '2024-01-01' + INTERVAL s.n DAY, w.id, (s.n * 37 + w.id * 11) % 400, (s.n * 29 + w.id * 7) % 380
FROM purchasing.seq s, warehouses w WHERE s.n < 366;

-- A projection of stock: a filter on its key reaches the primary key.
CREATE VIEW v_low_stock AS
SELECT warehouse_id, sku, quantity FROM stock WHERE quantity < 20;

DELIMITER //
CREATE PROCEDURE restock(IN p_warehouse int, IN p_sku varchar(32), IN p_quantity int)
  COMMENT '补货：增加一个仓库一个 SKU 的库存，返回补货后的数量'
BEGIN
  UPDATE stock SET quantity = quantity + p_quantity WHERE warehouse_id = p_warehouse AND sku = p_sku;
  SELECT warehouse_id, sku, quantity FROM stock WHERE warehouse_id = p_warehouse AND sku = p_sku;
END //
DELIMITER ;

-- logistics -------------------------------------------------------------------

USE logistics;

-- The shippers this warehouse serves. The shop and ledger datasources have a
-- tenants table too: same name, other databases, other rows.
CREATE TABLE tenants (
  id      int PRIMARY KEY COMMENT '货主 ID',
  name    varchar(64) NOT NULL COMMENT '货主名称',
  contact varchar(32) NOT NULL COMMENT '联系人'
) COMMENT '货主（与电商、记账数据源的 tenants 同名）';

INSERT INTO tenants VALUES (1, '货主甲（食品）', '仓储专员 1'), (2, '货主乙（数码）', '仓储专员 2'),
  (3, '货主丙（家居）', '仓储专员 3');

CREATE TABLE carriers (
  id   int PRIMARY KEY COMMENT '承运商 ID',
  name varchar(64) NOT NULL UNIQUE COMMENT '承运商名称'
) COMMENT '承运商';

INSERT INTO carriers VALUES (1, '承运商 A'), (2, '承运商 B'), (3, '承运商 C');

-- Large enough that an unfiltered read is a full scan the cost gate refuses.
-- Deleting a carrier keeps its waybills (ON DELETE SET NULL); the warehouse
-- is in another database.
CREATE TABLE waybills (
  id             int AUTO_INCREMENT PRIMARY KEY COMMENT '运单 ID',
  tenant_id      int NOT NULL COMMENT '所属租户（货主）',
  warehouse_id   int NOT NULL COMMENT '发货仓',
  carrier_id     int COMMENT '承运商（承运商删除后置空）',
  tracking_no    varchar(32) NOT NULL COMMENT '运单号',
  receiver_phone varchar(20) NOT NULL COMMENT '收件人手机号',
  shipped_at     datetime NOT NULL COMMENT '发货时间',
  UNIQUE KEY waybills_carrier_tracking (carrier_id, tracking_no),
  UNIQUE KEY waybills_tenant_tracking (tenant_id, tracking_no),
  KEY waybills_tenant_shipped (tenant_id, shipped_at),
  FOREIGN KEY (tenant_id) REFERENCES tenants (id),
  FOREIGN KEY (warehouse_id) REFERENCES inventory.warehouses (id),
  FOREIGN KEY (carrier_id) REFERENCES carriers (id) ON DELETE SET NULL
) COMMENT '运单';

INSERT INTO waybills (id, tenant_id, warehouse_id, carrier_id, tracking_no, receiver_phone, shipped_at)
SELECT n, 1 + n % 3, 1 + n % 5, 1 + n % 3, CONCAT('TRK', LPAD(n, 10, '0')),
       CONCAT('100', LPAD(n * 7919 % 100000000, 8, '0')), TIMESTAMP '2024-01-01 08:00:00' + INTERVAL n * 10 MINUTE
FROM purchasing.seq WHERE n BETWEEN 1 AND 50000;

-- archive: same table name as logistics.waybills ------------------------------

USE archive;

CREATE TABLE waybills (
  id          int PRIMARY KEY COMMENT '运单 ID',
  tenant_id   int NOT NULL COMMENT '所属租户（货主）',
  tracking_no varchar(32) NOT NULL COMMENT '运单号',
  shipped_at  datetime NOT NULL COMMENT '发货时间'
) COMMENT '2023 年运单归档（只读）';

INSERT INTO waybills
SELECT n, 1 + n % 3, CONCAT('OLD', LPAD(n, 10, '0')), TIMESTAMP '2023-01-01 08:00:00' + INTERVAL n * 30 MINUTE
FROM purchasing.seq WHERE n BETWEEN 1 AND 8000;

-- purchasing (not configured; import it from the console) ----------------------

USE purchasing;

CREATE TABLE suppliers (
  id   int PRIMARY KEY COMMENT '供应商 ID',
  name varchar(64) NOT NULL UNIQUE COMMENT '供应商名称'
) COMMENT '供应商';

INSERT INTO suppliers VALUES (1, '供应商甲'), (2, '供应商乙'), (3, '供应商丙');

CREATE TABLE purchase_orders (
  id          int PRIMARY KEY COMMENT '采购单 ID',
  supplier_id int NOT NULL COMMENT '供应商',
  sku         varchar(32) NOT NULL COMMENT 'SKU',
  quantity    int NOT NULL COMMENT '采购数量',
  ordered_at  date NOT NULL COMMENT '下单日期',
  FOREIGN KEY (supplier_id) REFERENCES suppliers (id),
  FOREIGN KEY (sku) REFERENCES inventory.items (sku)
) COMMENT '采购单';

INSERT INTO purchase_orders
SELECT n, 1 + n % 3, CONCAT('SKU-', LPAD(1 + n % 240, 5, '0')), 50 + n % 200, DATE '2024-01-01' + INTERVAL n DAY
FROM seq WHERE n BETWEEN 1 AND 300;

-- The gateway's accounts: warehouse_ro reads (the read connection, a replica
-- in config.yaml), warehouse_rw writes and calls procedures. TRIGGER lets
-- information_schema show them the triggers.
CREATE USER 'warehouse_ro'@'%' IDENTIFIED BY 'warehouse_ro';
CREATE USER 'warehouse_rw'@'%' IDENTIFIED BY 'warehouse_rw';
GRANT SELECT, SHOW VIEW, TRIGGER ON inventory.* TO 'warehouse_ro'@'%';
GRANT SELECT, SHOW VIEW ON logistics.* TO 'warehouse_ro'@'%';
GRANT SELECT ON archive.* TO 'warehouse_ro'@'%';
GRANT SELECT ON purchasing.* TO 'warehouse_ro'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE, EXECUTE, SHOW VIEW, TRIGGER ON inventory.* TO 'warehouse_rw'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE, SHOW VIEW ON logistics.* TO 'warehouse_rw'@'%';
GRANT SELECT ON archive.* TO 'warehouse_rw'@'%';
GRANT SELECT ON purchasing.* TO 'warehouse_rw'@'%';

ANALYZE TABLE inventory.items, inventory.stock, logistics.waybills, archive.waybills;
