# IR 语义规范（v0.1.8）

状态：**规范（v0.1.8 定版）**。本文为当前已承诺的 Canonical IR 读路径子集
定义形式化操作语义，是 reference interpreter（`core/relalg/interp`）与跨
Provider differential conformance suite（`internal/conformance`）的唯一
依据。写算子（Insert/Update/Delete/Call）语义等价不在本版本范围。

规范原则：

- **canonical 语义**：所有 Provider 必须一致的行为；conformance suite 强制；
- **documented deviation**：无法统一的行为，逐项列出并绑定 Provider；
  conformance suite 的等价 corpus 回避这些点，任何新增偏差必须先写入本文
  才允许通过验收；
- 静默差异不允许存在：不在本文的行为差异视为 bug。

## 数据模型

- 关系是行的 **bag（多重集）**：重复行保留，无隐式去重；`Distinct` 是唯一
  的显式去重算子；
- 无 `Sort` 时行序**不作保证**；结果比较使用 bag 相等；
- 标量值域（conformance 覆盖）：`NULL`、整数、精确小数（decimal）、字符串。
  时间、布尔、二进制类型的跨库归一化属于后续扩展（见"已知限制"）。

## 谓词：SQL 三值逻辑

谓词求值到 `TRUE | FALSE | UNKNOWN`。`Select` 只保留求值为 `TRUE` 的行
（`FALSE` 与 `UNKNOWN` 均被过滤）。

| 谓词 | 语义 |
| --- | --- |
| `eq/ne/gt/gte/lt/lte` | 任一侧为 `NULL` → `UNKNOWN`；否则数值按数值比较、字符串按字节序比较（collation 偏差见下） |
| `like` | 左侧为 `NULL` → `UNKNOWN`；`%` 匹配任意序列，`_` 匹配单字符；canonical 语义大小写敏感（偏差见下） |
| `in (v1..vn)` | 等价于 `x=v1 OR ... OR x=vn`：任一 `TRUE` → `TRUE`；否则若任一 `UNKNOWN`（含列表中的 `NULL`）→ `UNKNOWN`；否则 `FALSE` |
| `not_in` | `NOT(in)`：列表含 `NULL` 时永不为 `TRUE`（最多 `FALSE/UNKNOWN`） |
| `is_null` / `is_not_null` | 二值，永不 `UNKNOWN` |
| `AND` | 任一 `FALSE` → `FALSE`；否则任一 `UNKNOWN` → `UNKNOWN`；否则 `TRUE` |
| `OR` | 任一 `TRUE` → `TRUE`；否则任一 `UNKNOWN` → `UNKNOWN`；否则 `FALSE` |
| `NOT` | `TRUE`↔`FALSE`；`UNKNOWN` 保持 `UNKNOWN` |

## 读路径算子

| 算子 | 输入 → 输出 | 语义 |
| --- | --- | --- |
| `Scan` | 基表 → bag | 全部行、DDL 声明列序 |
| `Select` | bag → bag | 保留谓词为 `TRUE` 的行，重复保留 |
| `Project` | bag → bag | 按 items 取列/改名；不去重；引用不存在的列是编译期错误 |
| `Aggregate` | bag → bag | 见下节 |
| `Sort` | bag → 序列 | 按 OrderTerm 依次比较；`asc`/`desc`；排序键相等的行之间顺序不作保证（需要确定序时必须补唯一键） |
| `Limit` | 序列 → 序列 | 先 offset 后 count；无前置 `Sort` 时选中哪些行不作保证 |
| `Distinct` | bag → bag | 整行去重；`NULL` 与 `NULL` 视为相同（distinct 语义，非 `=` 语义） |

## 聚合语义

- `count(*)` 计行数；`count(field)` 计 `field` 非 `NULL` 的行数；
- `sum`/`avg`/`min`/`max` 忽略 `NULL` 输入值；
- **无 groupBy 的空输入**：恒返回一行——`count → 0`，`sum/avg/min/max →
  NULL`；
- **有 groupBy 的空输入**：返回零行（不存在空组）；
- groupBy 分组时 `NULL` 与 `NULL` 归入同一组（distinct 语义）；
- 输出列序：groupBy 列在前（声明序），聚合列在后（声明序）。跨 Provider
  的聚合**列名**不保证一致（PG 为 `count`、MySQL 为 `count(*)`），比较必须
  按列位置；
- `sum` 溢出：canonical 语义为报错或精确值，不允许静默回绕（integer sum 在
  PG 提升为 numeric/bigint；conformance corpus 不触碰溢出边界，溢出行为属
  documented deviation）。

## 错误语义

无效 IR 必须在校验/编译期拒绝，不允许落到数据库错误：

- 非白名单操作符/聚合函数：`relalg.ErrInvalidOp` / `ErrInvalidAggFunc`；
- 空 `AND`/`OR`、nil 操作数、空字段名：`ErrInvalidPredicate`；
- `is_null`/`is_not_null` 携带 value：`ErrInvalidPredicate`；
- IN 列表为空、非 `[]any` 或超出上限：`ErrINCardinality`；
- 引用不存在的列：interpreter 报错；SQL 端由 entity 字段校验在工具层拦截
  （`INVALID_INPUT`），不作为 conformance 差分点。

## Documented deviations（按 Provider）

conformance 等价 corpus 回避以下点；触碰它们的行为差异不算失败，但必须在
此登记：

| 行为 | PostgreSQL | MySQL / OceanBase | corpus 处理 |
| --- | --- | --- | --- |
| `Sort` 中 `NULL` 的位置 | ASC 时 NULL 在后 | ASC 时 NULL 在前 | 有序比较只用非 NULL 且唯一的排序键；含 NULL 排序键的 case 用 bag 比较 |
| `like`/字符串比较的大小写 | 大小写敏感 | 默认 collation（`utf8mb4_0900_ai_ci` 等）大小写不敏感 | corpus 字符串全小写且互不为大小写变体 |
| `avg` 的小数精度 | numeric 高精度 | 默认 4 位小数（`div_precision_increment`） | corpus 只包含 ≤2 位小数可精确终止的平均值 |
| 聚合结果列名 | `count`/`sum`… | `count(*)`/`sum(x)`… | 一律按列位置比较 |
| 整数 `sum` 的结果类型 | `bigint`/`numeric` | `decimal` | 数值归一化后精确比较 |
| 整数溢出 | numeric 提升，不回绕 | BIGINT 报错 | corpus 不触碰溢出边界 |
| 时间/时区/布尔类型 | — | — | 本版本 corpus 不含此类列 |

新增 Provider（如 SQLite）必须先按本表逐项声明自己的偏差，再进入
conformance 验收。

## Conformance 验收方式

- oracle：`core/relalg/interp` 按本规范逐条实现，每条语义有对应单元测试；
- 差分：同一确定性 fixture、同一 IR，interpreter 结果 vs Provider 经
  `codegen.Renderer` 编译执行的结果；无 `Sort` 用 bag 相等，有 `Sort`（键
  唯一且非 NULL）用有序相等；数值经精确 decimal 归一化后比较；
- corpus：固定边界用例 + 固定 seed 的有界随机组合
  （`internal/conformance`），三库（PostgreSQL/MySQL/OceanBase）经
  `make test-integration` 全跑；
- 差异处置：修复 codegen/方言，或作为 documented deviation 登记进上表并在
  [Provider 兼容矩阵](../provider-compatibility.md) 标注——二选一，不允许
  静默。

## 已知限制

- 覆盖读路径 + 聚合；写算子与事务语义等价由现有 integration/e2e 保证行为，
  精确语义规范留待 L12 逐能力立项时补齐；
- conformance corpus 值域限于整数、decimal、小写 ASCII 字符串与 `NULL`；
  时间/时区/collation/布尔的跨库归一化尚未进入等价 corpus；
- interpreter 不是性能参考，也不承诺与任何 Provider 的错误消息文本一致，
  只承诺错误分类一致。
