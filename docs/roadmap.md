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

- grain、时间、枚举、单位或 catalog token 缺失显著时，进入 Semantic Metadata；
- 任务因现有 IR 无法表达而失败时，进入 Governed Query Expressiveness；
- 大 schema 选择失败或 catalog token 成本显著时，进入 Catalog Discovery；
- 错误提示或工具契约导致可修复失败时，只收紧对应契约。

若没有显著短板，本阶段不进入 `Committed`，也不为制造版本内容而扩张 Agent 功能。
任何升级项必须使用能覆盖对应失败来源的任务集验收，不得复用缺少该场景的旧 pilot
作为价值证据。

---

## Next 2 — Provider Capability + Evidence-Backed SQLite

进入门禁：SQLite 至少具备一个可验证用户场景、外部 contributor 或明确采用目标，
并有固定公开 CI 环境或维护者承担可持续测试。完整条件以
[Provider Roadmap](provider-roadmap.md) 为准。

先将平铺 bool capability 重构为“范围 + 保证强度 + 证据”模型，再交付受现有 IR
和统一 engine 约束的窄 SQLite Provider。首版只承诺有独立 integration 证据的
能力；extension loading、`ATTACH DATABASE`、危险 `PRAGMA` 和配置根目录外文件
默认拒绝。

退出门禁：capability model 不削弱现有 Provider 保证；SQLite 的每项公开能力均有
独立证据或明确标为未验证；兼容矩阵、支持版本、威胁分析和 allow/deny e2e 同时
交付。进入证据未满足时，本阶段不构成版本承诺。

---

## Next 3 — Minimum Control Plane

进入门禁：至少一个真实部署明确需要 revision、diff、simulate、publish 或
rollback，且无法由现有 CLI 热重载流程合理满足。

只实现已被真实需求触发的最小操作；publish/rollback 与 CLI 热重载共用配置变更
守卫，并明确控制面不可用、snapshot 不兼容和在途请求语义。

退出门禁：具备恢复、拒绝、审计和降级测试；控制面不可用不得绕过统一 engine，也
不得阻塞数据面读取已有有效 snapshot。

---

## Later

完整 Public Eval（多模型、多客户端和竞品 baseline）、可执行语义、Catalog
Discovery、成本与数据出站治理、其他 Provider 扩展、生产运维、写治理、企业身份、
durable audit、管理 UI、查询复用、扩展点，以及受治理查询表达力与 Provider
优化扩展（IR Evolution）均按证据升级。

范围、触发证据和跨阶段非目标见
[Evidence-Gated Directions](roadmap/directions.md)。Provider 能力模型与候选顺序
继续以 [Provider Roadmap](provider-roadmap.md) 为唯一事实源。

---

## 衡量与维护

技术可信度、Agent 效果、采用生态、供应链和公开数字规则见
[Roadmap Metrics](roadmap/metrics.md)。

- 每个 release 后复审阶段证据和 `Next` 顺序；
- 新能力进入 `Committed` 前必须有问题证据、明确非目标和二元验收条件；
- Later 项不因时间流逝自动升级；
- 未完成事项不自动滚入下一版本；
- 已发布行为移入 release notes 或对应事实文档。
