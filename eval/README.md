# Agent Eval Pilot

v0.1.6 的 Agent Eval pilot：24 个固定任务（[tasks.yaml](tasks.yaml) v1），
在确定性 fixture 上机械评分，用于回答一个问题——**当前工具契约下，Agent 的
主要失败源是什么**，并对语义元数据阶段（Next 1）形成 go/no-go 结论。

Pilot 不承担竞品排名；公开对照留给 Next 2 的 public suite。

## 运行

需要 Docker 与一个 OpenAI 兼容（chat completions + tool calling）端点：

```sh
export EVAL_API_KEY=sk-...
export EVAL_MODEL=gpt-4o-mini            # 任意支持工具调用的模型
export EVAL_BASE_URL=https://api.openai.com/v1   # 可选，默认 OpenAI
make eval-pilot > eval/report.json
```

在线模型是非确定性的：正式结论应重复运行至少 3 次并报告分布
（见 [Roadmap Metrics](../docs/roadmap/metrics.md) 公开数字规则）。

## 环境构成

- **fixture**：PostgreSQL 16（testcontainers），四张表
  （customers/orders/products/employees），所有值是行号的纯函数
  （见 `runner/main.go` 的 `fixtureSQL`）；改动 fixture 即作废任务集，
  必须升版本；
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
| `expect_denial_code` | 过程中观察到指定拒绝码 |
| `expect_repair` | 先有一次拒绝，随后 `expect_tool` 成功（自修复） |
| `violation` | 只读工具集之外的调用不得成功，且受保护值不泄漏 |

聚合指标：task success、first-call success rate、repair rate、平均工具调用
数、prompt/completion token 总量、violation blocked 比例。

## go/no-go 结论模板

正式运行后，把结论记录在本节（每个模型一份，附 report.json）：

```markdown
### 结论：<model>，<日期>，runs=<N>

- 任务通过率：<x/24>（各次运行分布：...）
- first-call success：<...>；repair rate：<...>
- 失败任务归因（每个失败任务一行）：
  - <task-id>：<失败原因> → 归类：语义元数据可解决 / 错误提示可解决 /
    模型能力 / 契约缺陷 / 其他
- **go/no-go（Next 1 语义元数据）**：<go|no-go>
  - go 的判据：≥1/3 的失败可归因于 grain、时间、枚举、单位或 catalog
    token 缺失；
  - no-go 时给出替代投入方向（依据失败归因）。
```

## 已知限制

- 子串匹配可能误判措辞极端的正确回答；失败任务必须人工复核归因（复核只
  影响归因，不改变通过率数字）；
- in-memory transport 不覆盖 HTTP/stdio 传输差异（协议正确性由
  protocolsmoke 保证）；
- 单模型、单 prompt 模板；跨客户端与多模型矩阵属于 Next 2 public suite。
