# 真实负载轨结论：deepseek-v4-flash，2026-07-12（任务集 v4）

v0.1.9 真实业务负载模型（[设计文档](../../docs/design/business-workload-model.md)）
首次正式运行。完整 report 见同目录
`2026-07-12-deepseek-v4-flash-workload-v4-run{1,2,3}.json`。

- 任务集：v4（21 任务 = 商业 4 + 支付 8 + 账务 5 + 直播 4；分布门禁由
  loader 机械强制）；fixture 为四模块组合 profile（45 个实体、约 1200
  行业务数据，seed=1、scale=1、全部异常模式开启）；runs=3；并行度 6；
  每轮约 40 秒。
- 任务通过率：21/21、21/21、21/21（合计 63/63）。
- first-call success：62% / 62% / 57%；repair rate：100% / 67% / 100%
  （分母为出现过拒绝的任务，拒绝主要来自无谓词聚合守卫，v0.1.8 收紧的
  hint 修复路径全部有效）。
- 成本：平均 5.86–6.05 次工具调用/任务、2.90–3.05 次 discovery 调用/
  任务；prompt token 377K–421K/轮——**单任务约 18–20K，为 v3（约
  7.4K/任务）的 2.6 倍**，主因是 45 实体 catalog 与更长的字段描述；
  token 硬上限（2M/轮）未触发。
- 证据通道：`evidenceRowsFound` 对点查类任务有效（规则版本、快照、
  对账分布类任务预期行 100% 出现在工具返回中）；聚合类任务预期值由
  模型侧计算合成，行覆盖率为 0 属预期，只作复核信号。

## 评分产物（已修复并锁定）

修复前的非正式一轮出现 2 例失败（`commerce.historical-price`、
`ledger.fees-may`），复核为**评分产物**：数字边界匹配把句号当作小数点
延续拒绝了 "**4,589**." 这类合法表述。评分器已修复（句末标点是合法
数字边界）并由确定性测试锁定
（`eval/runner/workload_test.go`），上述三轮为修复后的正式运行。

## 失败归因

三轮 0 例真实失败。十类归因出口（`agent_discovery` /
`argument_construction` / `relation_selection` / `grain` /
`time_semantics` / `status_semantics` / `unit_currency` /
`ir_expressibility` / `provider_divergence` / `governance_policy`）
全部为空。

共性观察：

- 多跳任务（如 `payments.channel-switch-recovered`：intent → attempt →
  状态判定）通过 Agent 多次调用分解完成，最多一例用到 13 次调用（上限
  16）——现有单跳 relationship 展开够用，但调用成本可见；
- grain、时间语义（occurred/received/processed）、规则版本
  point-in-time、最小货币单位换算、软删除、重复事件去重等全部语义陷阱
  任务均正确处理；
- 无谓词聚合守卫使聚合任务多付一次被拒调用，`PrimaryKey is_not_null`
  hint 的修复路径三轮全部有效（与 v3 结论一致）。

## go/no-go（休眠项 Eval-Driven Agent Improvement 重开评估）

- **Semantic Metadata：no-go（维持）**。真实形态负载下 grain、时间、
  单位、枚举、版本化语义任务零失败；字段 description 承载的语义已被
  正确使用。
- **Catalog Discovery：继续观察**。45 实体 catalog 下实体选择零失败，
  但单任务 prompt token 较 v3 上升 2.6 倍——成本信号显著且随 schema
  规模增长，尚未构成失败源或采用障碍；scale 提升或真实大宽表部署出现
  时重新评估。
- **Governed Query Expressiveness：no-go（维持）**。没有任务因 IR
  无法表达而失败；多跳分解的调用成本是效率问题而非能力缺口。
- 结论边界：单模型（deepseek-v4-flash）、单 prompt 模板、scale=1；
  97.9%→100% 的通过率说明该模型对本任务集同样接近饱和，**下一次需要
  区分度时应使用更弱模型、提高 scale 或引入 dogfooding 真实负载**，
  而不是外推"所有模型无短板"。

## 回归轨核对

同日 `make eval-pilot`（v3 冻结基线，目录迁移到 `eval/regression/`
之后）：32/32，first-call 72%，repair 78%，violations blocked 5/5——
迁移与 runner 双轨化无回归（report:
`2026-07-12-deepseek-v4-flash-v3-regression-check.json`）。
