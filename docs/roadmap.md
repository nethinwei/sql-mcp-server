# Roadmap

当前稳定基线为 `v0.1.6`。已发布能力以
[发布说明](releases/v0.1.6.md)、[CHANGELOG](../CHANGELOG.md)、
[配置参考](configuration.md)和[安全模型](security.md)为准。

本文件只给出未发布成果的顺序和门禁：

- **Committed**：当前版本承诺；同时只保留一个版本；
- **Next**：已排序阶段，满足进入条件后才获得版本承诺；
- **Later**：由证据触发，不承诺版本或时间；
- **Deferred**：与定位冲突或缺少证据，暂不规划。

详细文档：

- 长期方向：[Evidence-Gated Directions](roadmap/directions.md)
- 衡量方法：[Roadmap Metrics](roadmap/metrics.md)
- 数据库候选：[Provider Roadmap](provider-roadmap.md)

---

## 产品方向

> **The governed SQL gateway for untrusted AI agents.**

项目让不可信 Agent 在不可绕过、可解释、成本可控的边界内访问关系数据，不接受
任意 SQL。路线图围绕四类结果推进：

- **Adopt**：五分钟体验、客户端接入和可部署发布；
- **Prove**：安全、性能和 Agent 效果可复现；
- **Operate**：拒绝、成本、预算和故障可解释；
- **Understand**：减少“SQL 合法但业务答案错误”。

所有新入口默认 fail closed；控制面、Provider 和语义层不得绕过统一 engine；
未经用户需求、测试、Eval 或 benchmark 验证的能力不进入承诺范围。

---

## Committed — v0.1.7 Agent Eval Calibration

`v0.1.6`（Observable + Measurable）已完成验收并发布，成果见
[发布说明](releases/v0.1.6.md)。现有 Agent Eval pilot 的结论
（[2026-07-12 no-go](../eval/results/2026-07-12-deepseek-v4-flash.md)）表明，
当前简单 fixture 不能证明语义元数据值得投入，也暴露出 discovery 污染
first-call success、子串评分可能误判等测量问题。

`v0.1.7` 的目标是在有限测试成本内校准 Agent Eval，使其能可靠识别下一阶段最值得
解决的问题：

- 保留单模型、单驱动和一个版本化任务集，新增不超过 8 个大 schema、grain、时间、
  枚举或单位定向任务；
- 将合理 discovery 与执行失败分开计量，修正 first-call success 定义；
- 收紧 `answer_forbids` 等易误判规则，并为评分器增加确定性测试；
- 使用一个主模型正式运行三轮，报告分布、失败归因和后续方向 go/no-go。

成本边界：不做多模型或多客户端矩阵，不做竞品对照；在线模型运行不进入每次 CI；
任务数、单任务调用数和 token 必须有硬上限。其他模型仅允许按需运行，不作为发布
门禁。

退出门禁：评分器确定性测试通过；已知误判被复现并消除；discovery 不再计为首调
失败；三轮结果完成书面归因；至少明确选择或否决一个后续方向。

---

## Next 1 — Eval-Driven Agent Improvement

进入门禁：`v0.1.7` 完成校准，且失败归因证明存在显著、可由服务端解决的 Agent
短板。只升级证据支持的一项：

- grain、时间、枚举、单位或 catalog token 缺失显著时，进入
  [Semantic Metadata](roadmap/directions.md#foundation-semantic-metadata)；
- 任务因现有 IR 无法表达而失败时，进入
  [Governed Query Expressiveness](roadmap/directions.md#l12-governed-query-expressiveness)；
- 大 schema 选择失败或 catalog token 成本显著时，进入
  [Catalog Discovery](roadmap/directions.md#l2-catalog-discovery)；
- 错误提示或[工具契约](tool-contract.md)导致可修复失败时，只收紧对应契约。

若没有显著短板，本阶段不进入 `Committed`，也不为制造版本内容而扩张 Agent 功能。
任何升级项必须使用能覆盖对应失败来源的任务集验收，不得复用缺少该场景的旧 pilot
作为价值证据。

---

## Next 2 — Provider Capability + Evidence-Backed SQLite

进入门禁、SQLite 首版范围、capability 前置工程和退出验收均以
[Provider Roadmap](provider-roadmap.md) 为唯一事实源。

阶段结果：先独立交付“范围 + 保证强度 + 证据”的 capability model，再交付一个受
现有 IR 和统一 engine 约束、且已有采用证据的窄 SQLite Provider。进入条件未满足
时，本阶段不构成版本承诺。

---

## Next 3 — Minimum Control Plane

进入门禁：至少一个真实部署明确需要 revision、diff、simulate、publish 或
rollback，且无法由现有 CLI 热重载流程合理满足。

只实现已被真实需求触发的最小操作；revision 数据模型、配置兼容性、失败处理和
在途请求语义遵循已评审的
[Revision 与 Snapshot 设计](design/revision-snapshot.md)。

退出门禁：实现满足上述设计并具备恢复、拒绝、审计和降级测试；任何路径不得绕过
统一 engine。

---

## Later

[Semantic Metadata](roadmap/directions.md#foundation-semantic-metadata)、
[完整 Public Eval](roadmap/metrics.md#agent-与业务效果)及其他战略方向均按证据
升级。具体范围、触发证据和跨阶段非目标见
[Evidence-Gated Directions](roadmap/directions.md)；Provider 能力模型与候选顺序
以 [Provider Roadmap](provider-roadmap.md) 为唯一事实源。

---

## 衡量与维护

技术可信度、Agent 效果、采用生态、供应链和公开数字规则见
[Roadmap Metrics](roadmap/metrics.md)。

- 每个 release 后复审阶段证据和 `Next` 顺序；
- 新能力进入 `Committed` 前必须有问题证据、明确非目标和二元验收条件；
- Later 项不因时间流逝自动升级；
- 未完成事项不自动滚入下一版本；
- 已发布行为移入 release notes 或对应事实文档。
