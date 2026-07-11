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

## Now — 无承诺版本（v0.1.6 已发布，下一承诺待证据）

`v0.1.6`（Observable + Measurable）已完成验收并发布，成果见
[发布说明](releases/v0.1.6.md)。

Agent Eval pilot 的结论
（[2026-07-12 no-go](../eval/results/2026-07-12-deepseek-v4-flash.md)）表明
语义元数据的进入门禁**未满足**：三轮 24 任务运行中没有失败可归因于 grain、
时间、枚举、单位或 catalog token 缺失。按本路线图规则，Next 1 不升格；下一个
`Committed` 由采用侧证据决定，当前候选（不构成承诺）：

- SQLite Provider（按 [Provider Roadmap](provider-roadmap.md) 进入条件，
  含 capability model 前置重构）；
- Next 2 的公开 Eval 准备（复用 pilot 框架升级为版本化任务集）。

---

## Next 1 — Semantic Metadata

进入门禁：可观测基线完成，且 pilot 证明 grain、时间、枚举、单位或 catalog token
是显著失败来源。**2026-07-12 pilot 结论为 no-go，本门禁未满足**；真实大
schema 或语义歧义负载出现时重新评估。

目标结果：

- Entity grain、默认时间维度、discoverability 和替代关系；
- Field 语义角色、枚举、单位、币种、尺度和时间含义；
- 服从 RBAC、hidden 和 mask 的渐进披露；
- 稳定语义 lint 与向后兼容的 import/export。

退出门禁：旧配置行为不变，不产生越权或 mask 侧信道，不改变授权和 SQL 语义，并
通过同一 pilot 任务集证明价值或停止投入。

---

## Next 2 — Public Eval + Minimum Control Plane

进入门禁：机器错误、可观测基线和 pilot 稳定；至少两个真实客户端可重复集成；
至少一个真实部署需要 revision、diff 或 simulate。

目标结果：

- 版本化公开任务集与三类可复现 baseline；
- 任务成功、错误修复、越权执行、调用数、token 和业务正确性指标；
- 按真实需求提供 validate、diff、simulate、publish 和 rollback；
- publish/rollback 与 CLI 热重载共用同一套配置变更守卫（现有守卫从 CLI 层
  下沉到 bootstrap，避免两条发布路径规则漂移）；
- 明确控制面不可用、snapshot 不兼容和在途请求语义。

退出门禁：公开结果可复现，tool/error hint 变更有前后对照，安全证据无回归；
控制面若实现，必须具备恢复、拒绝、审计和降级测试。

公开 Eval 必须先于大控制面，控制面不得成为评测阻塞项。

---

## Later

可执行语义、Catalog Discovery、成本与数据出站治理、Provider 扩展、生产运维、
写治理、企业身份、durable audit、管理 UI、查询复用、扩展点，以及受治理查询
表达力与 Provider 优化扩展（IR Evolution）均按证据升级。

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
