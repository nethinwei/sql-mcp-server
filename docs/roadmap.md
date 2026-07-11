# Roadmap

当前稳定基线为 `v0.1.4`。已发布能力以
[发布说明](releases/v0.1.4.md)、[CHANGELOG](../CHANGELOG.md)、
[配置参考](configuration.md)和[安全模型](security.md)为准。

本文件只给出未发布成果的顺序和门禁：

- **Committed**：当前版本承诺；同时只保留一个版本；
- **Next**：已排序阶段，满足进入条件后才获得版本承诺；
- **Later**：由证据触发，不承诺版本或时间；
- **Deferred**：与定位冲突或缺少证据，暂不规划。

详细文档：

- 当前承诺：[v0.1.5 — Contract + Product Proof](roadmap/committed-v0.1.5.md)
- 落地计划：[v0.1.5 落地计划](roadmap/execution-v0.1.5.md)
- 下阶段候选：[v0.1.6（候选）— Observable + Measurable](roadmap/next-v0.1.6.md)
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

## Now — Committed v0.1.5

目标：固定数据面机器契约和真实 HTTP 路径，并把现有能力组合为五分钟产品证明。

关键结果：

- 机器可读拒绝与跨 MCP、审计、trace 的 `decision ID`；
- 真实 streamable HTTP `/mcp` 授权和事务 e2e；
- tool schema、错误与 YAML export 的兼容规则；
- 六场景 Demo、客户端入口和安全证据索引；
- critical/high threat ID 到测试的可追溯映射。

退出门禁：机器错误稳定解析、HTTP 授权路径等价、export 确定、snapshot 失败语义
通过设计评审、Demo 可复现、关键威胁均有证据或明确缺口。

完整范围与非目标见
[v0.1.5 — Contract + Product Proof](roadmap/committed-v0.1.5.md)；工作包
拆解、依赖顺序和验证方式见
[v0.1.5 落地计划](roadmap/execution-v0.1.5.md)。

---

## Next 1 — Observable + Measurable

进入门禁：`v0.1.5` 验收完成，关键拒绝路径可通过 `decision ID` 关联。
候选范围的具体化见
[v0.1.6（候选）— Observable + Measurable](roadmap/next-v0.1.6.md)。

目标结果：

- liveness、snapshot readiness 和数据库 readiness 分离；
- 最小 metrics、结构化日志和 stdio/HTTP 协议 smoke；
- 审计事件 schema 定版（json tag 命名、拒绝码、entity/action）；
- 可复现的 p50/p95/p99 data-plane overhead benchmark；
- 20–30 个固定任务的 Agent Eval pilot。

退出门禁：telemetry 能解释拒绝和故障；benchmark 与 pilot 有固定环境、评分和
复现命令；pilot 对下一阶段形成 go/no-go 结论。

---

## Next 2 — Semantic Metadata

进入门禁：可观测基线完成，且 pilot 证明 grain、时间、枚举、单位或 catalog token
是显著失败来源。

目标结果：

- Entity grain、默认时间维度、discoverability 和替代关系；
- Field 语义角色、枚举、单位、币种、尺度和时间含义；
- 服从 RBAC、hidden 和 mask 的渐进披露；
- 稳定语义 lint 与向后兼容的 import/export。

退出门禁：旧配置行为不变，不产生越权或 mask 侧信道，不改变授权和 SQL 语义，并
通过同一 pilot 任务集证明价值或停止投入。

---

## Next 3 — Public Eval + Minimum Control Plane

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
写治理、企业身份、durable audit、管理 UI、查询复用和扩展点均按证据升级。

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
