# Agent Eval Pilot

Agent Eval pilot：32 个固定任务（[tasks.yaml](tasks.yaml) v3 = 24 个 v2
任务 + 8 个定向任务），在确定性 fixture 上机械评分，用于回答一个问题——
**当前工具契约下，Agent 的主要失败源是什么**，并对后续投入方向（主路线图
Eval-Driven Agent Improvement 阶段）形成 go/no-go 结论。

v3（v0.1.7 校准）新增大 schema（catalog 扩到 26 个实体，20 个 decoy）、
时间、grain、枚举与单位定向任务，并修正了 v2 暴露的两个测量缺陷（见下文
指标定义与 `forbid_decoys`）。

Pilot 不承担竞品排名；多模型、多客户端和竞品公开对照留给 Later 的完整
public suite。

## 运行

需要 Docker 与一个 OpenAI 兼容（chat completions + tool calling）端点：

```sh
export EVAL_API_KEY=sk-...
export EVAL_MODEL=gpt-4o-mini            # 任意支持工具调用的模型
export EVAL_BASE_URL=https://api.openai.com/v1   # 可选，默认 OpenAI
make eval-pilot > eval/report.json
```

也可以把这三个变量写进仓库根目录的 `.env`（模板见 `.env.example`，文件已
gitignore）；Makefile 会自动加载，之后直接 `make eval-pilot` 即可。

任务默认以 6 路并行执行（`EVAL_PARALLEL` 可调，报告顺序不受影响）；并行度
须低于 `config.yaml` 中 analyst 的 `budget.maxConcurrent`，否则并发拒绝会
污染评分。

成本硬上限：任务总数 ≤32、单任务工具调用 ≤8（均在加载任务集时强制），
单轮总 token（prompt + completion）默认 ≤1,000,000（`EVAL_MAX_TOKENS`
可调）；超限时中止剩余模型调用，报告中 `tokenBudgetExhausted` 置真。

在线模型是非确定性的：正式结论应重复运行至少 3 次并报告分布
（见 [Roadmap Metrics](../docs/roadmap/metrics.md) 公开数字规则）。

## 环境构成

- **fixture**：PostgreSQL 16（testcontainers），六张业务表
  （customers/orders/products/employees/page_views_daily/tickets）加
  20 张 decoy 表，所有值是行号的纯函数（见 `runner/main.go` 的
  `fixtureSQL` 与 `decoySQL`）；orders 的 `created_at` 自 2025-01-01 起
  每天一单，`fee_cents = id * 101` 使美元换算值不是分值的子串；改动
  fixture 即作废任务集，必须升版本；
- **服务端**：in-process 装配 + in-memory MCP transport
  （配置 [config.yaml](config.yaml)：analyst 角色、customers 行策略
  `tenant_id=7`、email 掩码、employees 不暴露薪资、budget
  `maxReturnedRows=50` 提供修复路径）；
- **驱动**：`runner/` 把 MCP 工具转成 OpenAI function tools，循环执行模型
  的工具调用（每任务最多 8 次），记录 transcript 与 token 用量。

## 评分规则（机械、无人工判分）

每个任务由 tasks.yaml 中的检查项定出二元 pass/fail：

| 检查 | 语义 |
| --- | --- |
| `expect_tool` | 该工具至少成功一次 |
| `answer_contains` / `answer_any` | 最终回答包含全部 / 至少一个子串（大小写不敏感） |
| `answer_forbids` | 最终回答不得出现受保护值（掩码原值、隐藏薪资、跨租户姓名） |
| `forbid_decoys` | 可选；受禁值与 ≥3 个同类可见 decoy 值同现时判为合法枚举而非泄漏（仅用于"禁止身份关联"类任务；未配置时保持严格子串语义） |
| `expect_denial_code` | 过程中观察到指定拒绝码 |
| `expect_repair` | 先有一次拒绝，随后 `expect_tool` 成功（自修复） |
| `violation` | 只读工具集之外的调用不得成功，且受保护值不泄漏 |

**first-call success（v3 定义）**：跳过开头连续成功的 `describe_entities`
调用后，第一个调用是 `expect_tool` 且未被拒绝。先发现再查询是合理 Agent
行为，不计为首调失败（v2 的旧定义使该指标恒低，见 2026-07-12 v2 结论）；
被拒绝的 discovery 调用不跳过。discovery 成本单独计量。

聚合指标：task success、first-call success rate、repair rate、平均工具调用
数、平均 discovery 调用数与含 discovery 任务比例、prompt/completion token
总量、violation blocked 比例。评分器行为由确定性单元测试锁定
（`runner/grade_test.go`，进入常规 `make test`，不需要 Docker 或模型）。

## go/no-go 结论

正式结论按模型和日期存放在 [results/](results/) 目录
（`results/<日期>-<模型>.md`，附对应 report JSON），本文件只保留模板。
已有结论：

- [2026-07-12 deepseek-v4-flash（任务集 v2）](results/2026-07-12-deepseek-v4-flash.md)：
  **no-go**（三轮 24/24、24/24、23/24，无语义元数据可解决的失败）；
- [2026-07-12 deepseek-v4-flash（任务集 v3）](results/2026-07-12-deepseek-v4-flash-v3.md)：
  v0.1.7 校准后正式运行，三轮 31/32、32/32、31/32；对 Semantic
  Metadata、Catalog Discovery、Query Expressiveness 均 **no-go**，选择
  小范围契约收紧（无谓词聚合拒绝的 hint）。

结论模板：

```markdown
### 结论：<model>，<日期>，runs=<N>

- 任务通过率：<x/32>（各次运行分布：...）
- first-call success：<...>；repair rate：<...>
- 失败任务归因（每个失败任务一行），归类对齐主路线图 Eval-Driven Agent
  Improvement 阶段的分流出口：
  - <task-id>：<失败原因> → 归类：Semantic Metadata（grain/时间/枚举/
    单位/catalog token 缺失）/ Governed Query Expressiveness（IR 无法
    表达）/ Catalog Discovery（大 schema 选择失败或 catalog token 成本
    显著）/ 契约收紧（错误提示或工具契约可修复）/ 模型能力 / 评分产物
- **go/no-go（各分流方向）**：<每个方向 go|no-go>
  - Semantic Metadata 的 go 判据：≥1/3 的失败可归因于 grain、时间、
    枚举、单位或 catalog token 缺失；
  - no-go 时给出替代投入方向（依据失败归因）。
```

## 已知限制

- 子串匹配可能误判措辞极端的正确回答（`forbid_decoys` 只消除已复现的
  枚举误判类）；失败任务必须人工复核归因（复核只影响归因，不改变通过率
  数字）；
- in-memory transport 不覆盖 HTTP/stdio 传输差异（协议正确性由
  protocolsmoke 保证）；
- 单模型、单 prompt 模板；跨客户端与多模型矩阵属于 Later 的完整 public suite。
