-- ledger datasource (OceanBase, MySQL mode): tenant bookkeeping across
-- databases. Run once OceanBase is up (compose.yaml, service ob-init); it may
-- run again (every `docker compose up`) and then changes nothing.
--
--   ledger   tenants (same table name as in the shop and warehouse
--            datasources), currencies (expression key), branches (spatial), accounts
--            (prefix and nullable keys; account numbers), entries (large;
--            full-text, trigger), accounts view, procedure post_entry
--   billing  invoices (keys sharing a column), statements (partitioned)
--   archive  entries                  (same name as ledger.entries)
--   audit    logins                   (not in config.yaml: try importing)
--
-- The features each datasource shows are listed in README.md; the gateway
-- reaches these databases through two accounts, ledger_ro and ledger_rw (end
-- of file).

-- The comments and data are UTF-8: clients default to latin1 otherwise.
SET NAMES utf8mb4;
-- Generating the large tables takes longer than OceanBase's default 10s.
SET ob_query_timeout = 600000000;

CREATE DATABASE IF NOT EXISTS ledger;
CREATE DATABASE IF NOT EXISTS billing;
CREATE DATABASE IF NOT EXISTS archive;
CREATE DATABASE IF NOT EXISTS audit;

-- 0..99999, to generate rows.
CREATE TABLE IF NOT EXISTS audit.seq (n int PRIMARY KEY);
INSERT IGNORE INTO audit.seq
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

-- ledger ----------------------------------------------------------------------

USE ledger;

-- The tenants whose books this is. The shop and warehouse datasources have a
-- tenants table too: same name, other databases, other rows.
CREATE TABLE IF NOT EXISTS tenants (
  id      int PRIMARY KEY COMMENT '租户 ID',
  name    varchar(64) NOT NULL COMMENT '记账主体名称',
  tax_id  varchar(20) NOT NULL COMMENT '纳税人识别号'
) COMMENT '记账租户（与电商、仓储数据源的 tenants 同名）';

INSERT IGNORE INTO tenants VALUES (1, '记账主体一号', '91000000000000001X'),
  (2, '记账主体二号', '91000000000000002X'), (3, '记账主体三号', '91000000000000003X');

-- A unique key on an expression does not identify a row.
CREATE TABLE IF NOT EXISTS currencies (
  id   int PRIMARY KEY COMMENT '币种 ID',
  code varchar(3) NOT NULL COMMENT '币种代码',
  name varchar(32) NOT NULL COMMENT '币种名称',
  UNIQUE KEY currencies_code (code),
  UNIQUE KEY currencies_code_lower ((lower(code)))
) COMMENT '币种';

INSERT IGNORE INTO currencies VALUES (1, 'CNY', '人民币'), (2, 'USD', '美元'), (3, 'HKD', '港币');

CREATE TABLE IF NOT EXISTS branches (
  id       int PRIMARY KEY COMMENT '网点 ID',
  name     varchar(64) NOT NULL COMMENT '网点名称',
  location point NOT NULL SRID 0 COMMENT '坐标（经度、纬度）',
  SPATIAL INDEX branches_location (location)
) COMMENT '结算网点';

INSERT IGNORE INTO branches VALUES
  (1, '上海结算中心', ST_GeomFromText('POINT(121.47 31.23)')),
  (2, '深圳结算中心', ST_GeomFromText('POINT(114.06 22.54)'));

-- Unique keys that do not identify a row: on a column prefix (the bank
-- code), over a nullable column. Account numbers are masked in config.yaml.
CREATE TABLE IF NOT EXISTS accounts (
  id           int PRIMARY KEY COMMENT '账户 ID',
  tenant_id    int NOT NULL COMMENT '所属租户',
  branch_id    int NOT NULL COMMENT '开户网点',
  name         varchar(64) NOT NULL COMMENT '账户名称',
  account_no   varchar(32) NOT NULL COMMENT '银行账号',
  external_ref varchar(32) COMMENT '外部系统编号（可空）',
  currency     varchar(3) NOT NULL DEFAULT 'CNY' COMMENT '币种',
  balance      decimal(14, 2) NOT NULL DEFAULT 0 COMMENT '余额（元），由分录触发器维护',
  UNIQUE KEY accounts_bank (account_no(8)),
  UNIQUE KEY accounts_external (tenant_id, external_ref),
  FOREIGN KEY (tenant_id) REFERENCES tenants (id),
  FOREIGN KEY (branch_id) REFERENCES branches (id),
  FOREIGN KEY (currency) REFERENCES currencies (code) ON UPDATE CASCADE
) COMMENT '租户资金账户';

INSERT IGNORE INTO accounts (id, tenant_id, branch_id, name, account_no, external_ref)
SELECT n, 1 + (n - 1) DIV 4, 1 + n % 2, ELT(1 + (n - 1) % 4, '货款结算户', '营销费用户', '退款周转户', '保证金户'),
       CONCAT(LPAD(n, 8, '0'), '62220210'), IF(n % 4 = 0, NULL, CONCAT('EXT-', n))
FROM audit.seq WHERE n BETWEEN 1 AND 12;

-- Large enough that an unfiltered read is a full scan the cost gate refuses.
-- Deleting an account deletes its entries; a trigger keeps the balance, so a
-- write to entries may change accounts too and invalidates the whole
-- database's cache.
CREATE TABLE IF NOT EXISTS entries (
  id         bigint AUTO_INCREMENT PRIMARY KEY COMMENT '分录 ID',
  tenant_id  int NOT NULL COMMENT '所属租户',
  account_id int NOT NULL COMMENT '账户',
  amount     decimal(14, 2) NOT NULL COMMENT '金额（元，负数为支出）',
  memo       varchar(200) NOT NULL COMMENT '摘要',
  created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '记账时间',
  KEY entries_account (account_id, created_at),
  FULLTEXT KEY entries_memo (memo),
  FOREIGN KEY (account_id) REFERENCES accounts (id) ON DELETE CASCADE
) COMMENT '资金分录';

INSERT IGNORE INTO entries (id, tenant_id, account_id, amount, memo, created_at)
SELECT n, 1 + (1 + n % 12 - 1) DIV 4, 1 + n % 12, ((n * 37) % 900 - 300) * 1.5,
       ELT(1 + n % 4, '订单结算', '广告投放', '退款', '保证金冻结'),
       TIMESTAMP '2024-01-01 08:00:00' + INTERVAL n * 10 MINUTE
FROM audit.seq WHERE n BETWEEN 1 AND 50000 AND NOT EXISTS (SELECT 1 FROM ledger.entries);

UPDATE accounts a SET balance = (SELECT COALESCE(sum(e.amount), 0) FROM entries e WHERE e.account_id = a.id)
WHERE NOT EXISTS (SELECT 1 FROM information_schema.triggers WHERE trigger_schema = 'ledger' AND trigger_name = 'entry_posted');

DROP TRIGGER IF EXISTS entry_posted;
CREATE TRIGGER entry_posted AFTER INSERT ON entries FOR EACH ROW
  UPDATE accounts SET balance = balance + NEW.amount WHERE id = NEW.account_id;

CREATE OR REPLACE VIEW v_accounts AS
SELECT a.id AS account_id, a.tenant_id, a.name, a.currency, a.balance, b.name AS branch
FROM accounts a JOIN branches b ON b.id = a.branch_id;

DROP PROCEDURE IF EXISTS post_entry;
DELIMITER //
CREATE PROCEDURE post_entry(IN p_account int, IN p_amount decimal(14, 2), IN p_memo varchar(200))
  COMMENT '记账：写入一笔分录，返回账户余额'
BEGIN
  DECLARE v_tenant int;
  SELECT tenant_id INTO v_tenant FROM accounts WHERE id = p_account;
  INSERT INTO entries (tenant_id, account_id, amount, memo) VALUES (v_tenant, p_account, p_amount, p_memo);
  SELECT id AS account_id, balance FROM accounts WHERE id = p_account;
END //
DELIMITER ;

-- billing ---------------------------------------------------------------------

USE billing;

-- Two composite unique keys sharing a column; deleting an account keeps its
-- invoices (ON DELETE SET NULL, across databases).
CREATE TABLE IF NOT EXISTS invoices (
  id         int PRIMARY KEY COMMENT '发票 ID',
  account_id int COMMENT '收款账户（账户删除后置空）',
  period     varchar(7) NOT NULL COMMENT '账期（YYYY-MM）',
  invoice_no varchar(20) NOT NULL COMMENT '发票号码',
  amount     decimal(14, 2) NOT NULL COMMENT '金额（元）',
  UNIQUE KEY invoices_account_period (account_id, period),
  UNIQUE KEY invoices_account_no (account_id, invoice_no),
  FOREIGN KEY (account_id) REFERENCES ledger.accounts (id) ON DELETE SET NULL
) COMMENT '服务费发票';

INSERT IGNORE INTO invoices
SELECT n, 1 + n % 12, CONCAT('2024-', LPAD(1 + n DIV 12 % 12, 2, '0')), CONCAT('INV', LPAD(n, 8, '0')), 100 + n * 3
FROM audit.seq WHERE n BETWEEN 1 AND 144;

-- Statements, partitioned by half-year: one table to the gateway.
CREATE TABLE IF NOT EXISTS statements (
  period_start date NOT NULL COMMENT '账期起始日',
  account_id   int NOT NULL COMMENT '账户',
  opening      decimal(14, 2) NOT NULL COMMENT '期初余额（元）',
  closing      decimal(14, 2) NOT NULL COMMENT '期末余额（元）',
  PRIMARY KEY (period_start, account_id)
) COMMENT '月结对账单（按半年分区）'
PARTITION BY RANGE COLUMNS (period_start) (
  PARTITION p2024h1 VALUES LESS THAN ('2024-07-01'),
  PARTITION p2024h2 VALUES LESS THAN ('2025-01-01')
);

INSERT IGNORE INTO statements
SELECT DATE '2024-01-01' + INTERVAL m.n MONTH, a.n, a.n * 1000, a.n * 1000 + m.n * 50
FROM audit.seq m, audit.seq a WHERE m.n < 12 AND a.n BETWEEN 1 AND 12;

-- archive: same table name as ledger.entries ----------------------------------

USE archive;

CREATE TABLE IF NOT EXISTS entries (
  id         bigint PRIMARY KEY COMMENT '分录 ID',
  tenant_id  int NOT NULL COMMENT '所属租户',
  account_id int NOT NULL COMMENT '账户',
  amount     decimal(14, 2) NOT NULL COMMENT '金额（元）',
  created_at datetime NOT NULL COMMENT '记账时间'
) COMMENT '2023 年分录归档（只读）';

INSERT IGNORE INTO entries
SELECT n, 1 + (n % 12) DIV 4, 1 + n % 12, ((n * 41) % 800 - 300) * 1.5, TIMESTAMP '2023-01-01 08:00:00' + INTERVAL n * 30 MINUTE
FROM audit.seq WHERE n BETWEEN 1 AND 8000 AND NOT EXISTS (SELECT 1 FROM archive.entries);

-- audit (not configured; import it from the console) ---------------------------

USE audit;

CREATE TABLE IF NOT EXISTS logins (
  id        int PRIMARY KEY COMMENT '记录 ID',
  tenant_id int NOT NULL COMMENT '所属租户',
  username  varchar(32) NOT NULL COMMENT '登录名',
  at        datetime NOT NULL COMMENT '登录时间'
) COMMENT '后台登录记录';

INSERT IGNORE INTO logins
SELECT n, 1 + n % 3, CONCAT('user', n % 20), TIMESTAMP '2024-06-01 08:00:00' + INTERVAL n HOUR
FROM seq WHERE n BETWEEN 1 AND 200;

-- The gateway's accounts: ledger_ro reads (the read connection, a replica in
-- config.yaml), ledger_rw writes and calls procedures. TRIGGER lets
-- information_schema show them the triggers: without it a write that fires
-- one would not invalidate what it changes.
CREATE USER IF NOT EXISTS 'ledger_ro' IDENTIFIED BY 'ledger_ro';
CREATE USER IF NOT EXISTS 'ledger_rw' IDENTIFIED BY 'ledger_rw';
GRANT SELECT, TRIGGER ON ledger.* TO 'ledger_ro';
GRANT SELECT ON billing.* TO 'ledger_ro';
GRANT SELECT ON archive.* TO 'ledger_ro';
GRANT SELECT ON audit.* TO 'ledger_ro';
GRANT SELECT, INSERT, UPDATE, DELETE, EXECUTE, TRIGGER ON ledger.* TO 'ledger_rw';
GRANT SELECT, INSERT, UPDATE, DELETE ON billing.* TO 'ledger_rw';
GRANT SELECT ON archive.* TO 'ledger_rw';
GRANT SELECT ON audit.* TO 'ledger_rw';
