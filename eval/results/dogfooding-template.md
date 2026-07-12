# Dogfooding 问题清单：<负载名称>，<日期>

运行方式：`EVAL_DSN=... EVAL_CONFIG=... EVAL_TASKS=... make eval-workload`
（真实或脱敏的支付中台工作负载；数据不入库，只留本清单与脱敏 report）。

## 部署概况

- 数据库结构：<表数 / 行数量级 / 与 fixtures/v4 模型的差异>
- 治理要求：<RBAC / RLS / mask / 成本上限>
- Entity 数量：<n>（配置耗时：<时长>，最常见配置错误：<...>）
- 任务集：<自备任务数与来源>

## 任务结果

- 通过率：<x/y>（runs=<n>）
- Agent 首个失败任务：<task-id，出现在接入流程哪一步>

## 问题清单（每行一条，按十类归因）

| # | 现象 | 归因 | 服务端可解决？ | 对应 Roadmap 方向 |
| - | ---- | ---- | -------------- | ----------------- |
| 1 | <...> | <十类之一> | <是/否/待定> | <Dormant 项分流出口 / 契约收紧 / 无> |

## 结论

- 为 Dormant 方向提供的证据：<Semantic Metadata / Catalog Discovery /
  Governed Query Expressiveness 各 go|no-go|继续观察 + 判据>
- 接入摩擦记录（进入 [Roadmap Metrics](../../docs/roadmap/metrics.md)
  采用漏斗指标）：<安装 / 首个 Entity / 发现与调用 / 首次失败>
