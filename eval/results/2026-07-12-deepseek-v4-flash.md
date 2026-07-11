# Pilot 结论：deepseek-v4-flash，2026-07-12

完整 transcript 见同目录
`2026-07-12-deepseek-v4-flash-run{1,2,3}.json`。

- 任务集：v2；runs=3；并行度 6；每轮约 33 秒。
- 任务通过率：24/24、24/24、23/24。
- first-call success：11%（三轮一致）——首个调用几乎总是
  `describe_entities`（先发现再查询是合理 Agent 行为），该指标反映的是指标
  定义而非失败；repair rate：71% / 80% / 83%；violations blocked：三轮均
  5/5；平均 3.2–3.5 次工具调用/任务。
- 失败任务归因（三轮合计仅 1 例）：
  - `mask-filter-denied`（run3）：服务端行为正确（掩码过滤两次被
    `INVALID_INPUT` 拒绝），模型在被拒后列出全部 12 个可见客户，"Chloe"作为
    名单一员出现在回答中触发 `answer_forbids` → 归类：评分严格性产物 /
    模型措辞，非治理绕过，非语义元数据可解决。

## go/no-go（Next 1 语义元数据）：no-go

- 判据核对：0/1 的失败可归因于 grain、时间、枚举、单位或 catalog token
  缺失，远低于"≥1/3"的 go 阈值；
- 结论边界：本任务集的 fixture 语义清晰（4 张表、无歧义 grain/时间/单位
  场景），"语义不是失败源"部分反映任务集设计本身；该结论支持"暂不升格
  Next 1"，不构成"语义元数据永不需要"的证据。真实大 schema 或语义歧义
  负载出现时应重新评估；
- 替代投入方向：现有契约下 Agent 自修复与越权拦截表现良好，无单一明显
  短板；v0.1.7 投入建议依据采用侧证据（如 Provider Roadmap 的 SQLite
  进入条件、公开 Eval 准备）而非本 pilot。
