# Roadmap

当前稳定基线为 `v0.1.8`。已发布能力以
[发布说明](releases/v0.1.8.md)、[CHANGELOG](../CHANGELOG.md)、
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

## Committed

### v0.1.9 — Provider Capability Model

`v0.1.8`（IR Semantics and Provider Conformance）已完成验收，成果见
[发布说明](releases/v0.1.8.md)。本版本承诺 `Next 3` 的前置工程项：
Provider capability model 重构。

**问题证据**：[Provider Roadmap](provider-roadmap.md) 将"范围 + 强度 +
证据"能力模型定义为任何新 Provider 或兼容性认证立项的前置工程项，且应
作为独立变更先行交付。现有实现（`core/dialect.Capabilities`）是平铺
bool，成本闸门装配（`cost.NewGateFromCapabilities`）直接消费这些 bool，
无法表达保证强度与证据状态——即无法区分"数据库提供该能力"与"本服务已
装配可测试的强制机制"。

**阶段结果**：

- capability 重构为"范围 + 强度 + 证据"模型：保证强度统一为
  `unsupported` / `best_effort` / `enforced`；能力范围至少区分数据能力、
  生命周期、成本证明、原生执行边界、安全与身份、证据状态。字段定义以
  [Provider Roadmap](provider-roadmap.md) 的 Capability Model 章节为
  唯一事实源；
- 闸门装配与 codegen 渲染改为消费新模型；
- PostgreSQL、MySQL、OceanBase 三个现有 Provider 的声明按新模型迁移；
  [Provider 兼容矩阵](provider-compatibility.md)中每项安全与成本
  capability 的保证强度和证据状态可区分；
- 字段取舍以 SQLite（[Provider Roadmap](provider-roadmap.md) Adoption
  Accelerator 首位候选）为假想校验对象：模型必须能表达"缺少 `enforced`
  成本证明时由核心层等价强制或 fail closed"这类组合，避免下一个
  Provider 立项时返工。此项只约束模型设计，不构成 SQLite 承诺。

**伴随项**（参照 v0.1.7/v0.1.8 惯例，不扩大版本核心）：Eval 任务集
v4。v3 对 deepseek-v4-flash 已接近饱和（94/96），需引入更难任务或更弱
模型恢复区分度，为 Next 1 重评估与 Next 2 门禁（失败归因）提供测量
工具。

**非目标**：不新增任何 Provider（SQLite 仍受采用证据门禁约束）；不引入
L13 的实现方式维度（`native`/`emulated`/`restricted`/`unsupported` 仅
预留）；不改变三库现有运行时行为与闸门语义（行为等价重构）；能力模型
升级本身不构成新增 Provider 的承诺。

**退出门禁**：

- [ ] 每项 capability 分别表达范围、保证强度和证据，不再存在混合语义的
  单 bool；
- [ ] "`best_effort` 不满足硬限制；缺少 `enforced` 能力时由核心层等价
  强制或 fail closed"有测试锁定；
- [ ] 三库现有 integration 与 conformance suite
  （`make test-integration`）全绿，证明行为无回归；
- [ ] 兼容矩阵按新模型更新；发布链检查（fmt/vet/test/race）通过；
- [ ] Eval 任务集 v4 交付并完成至少一轮运行，结果按
  [Roadmap Metrics](roadmap/metrics.md) 三类基准与公开数字规则记录。

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

校准结论（[2026-07-12 v3](../eval/results/2026-07-12-deepseek-v4-flash-v3.md)，
三轮 31/32、32/32、31/32）：Semantic Metadata、Catalog Discovery 和
Governed Query Expressiveness 均 **no-go**——定向任务全部通过，失败无一可
归因于对应缺失。校准选定的唯一证据支持项（无谓词聚合拒绝的 hint 收紧）已
随 `v0.1.8` 交付。本阶段其余部分不进入 `Committed`，不为制造版本内容而
扩张 Agent 功能。真实大宽表 schema 或语义歧义负载出现时重新评估。

---

## Next 2 — Result Provenance and Evidence Envelope

进入门禁：Eval 失败归因、真实用户反馈或公开对照需求证明"Agent 给出的业务
结论无法追溯到数据、配置版本和一次具体执行"是信任或采用障碍。

阶段结果：每次工具结果附带机器可读 evidence envelope（逻辑查询与物理计划
fingerprint、snapshot/schema revision、涉及实体与字段、已应用策略与 mask
决策摘要、数据新鲜度与时区、截断/采样/近似标记、输出列 lineage、
decisionId 与可引用 citation handle），直接服务 Understand 结果——减少
"SQL 合法但业务答案错误"并使答案可解释、可复现。范围与约束见
[Result Provenance and Evidence Envelope](roadmap/directions.md#l14-result-provenance-and-evidence-envelope)。

退出门禁：envelope 字段进入版本化工具契约并有 golden 测试；envelope 内容
服从 RBAC/mask 可见性（不得成为新的侧信道）；至少一个 Eval 任务或 Demo
场景证明 Agent 能引用 envelope 解释答案来源。

---

## Next 3 — Evidence-Backed SQLite

进入门禁、SQLite 首版范围和退出验收均以
[Provider Roadmap](provider-roadmap.md) 为唯一事实源；`v0.1.8` 交付的
conformance suite（[IR 语义规范](design/ir-semantics.md)）是任何新
Provider 的验收前置。capability model 前置工程已提升为 `v0.1.9`
`Committed`，不再属于本阶段。

阶段结果：在 capability model 交付后，交付一个受现有 IR 和统一 engine
约束的窄 SQLite Provider。进入条件按 Provider Roadmap 为三选一——真实
用户场景、外部 contributor 或明确的采用/架构验证目标——其中架构验证
目标可在 release 复审时显式确立：SQLite 是新 capability model 的首个
新增 Provider 消费者，验证"弱成本证明、核心层兜底"的执行模型，同时是
产生真实采用证据（Next 2/4/5 门禁来源）的最低门槛入口。进入条件未
满足时，本阶段不构成版本承诺。

---

## Next 4 — Schema Drift and Compatibility Governance

进入门禁：真实部署出现数据库 schema 演进需求，或最小控制面（Next 5）进入
条件满足——本阶段是其前置。

阶段结果：把现有启动/reload 时的 drift 检查扩展为可分级的漂移治理——schema
fingerprint、drift 分类（compatible / behavior-changing /
security-sensitive / breaking）、对 Entity/Field/Policy/Mask 的影响分析、
incompatible drift 拒绝 readiness、配置 revision 与 schema revision 绑定。
核心不是"SQL 还能执行"，而是"schema 变化是否扩大授权资源闭包或改变策略
语义"。范围见
[Schema Drift Detection and Impact Analysis](roadmap/directions.md#l15-schema-drift-detection-and-impact-analysis)。

退出门禁：security-sensitive 与 breaking drift 默认 fail closed 并可解释；
影响分析有针对每类 drift 的回归测试；与 revision 设计
（[Revision 与 Snapshot](design/revision-snapshot.md)）的绑定语义评审通过。

---

## Next 5 — Minimum Control Plane

进入门禁：至少一个真实部署明确需要 revision、diff、simulate、publish 或
rollback，且无法由现有 CLI 热重载流程合理满足；Next 4 的 schema drift
治理为前置。

只实现已被真实需求触发的最小操作；revision 数据模型、配置兼容性、失败处理和
在途请求语义遵循已评审的
[Revision 与 Snapshot 设计](design/revision-snapshot.md)。

退出门禁：实现满足上述设计并具备恢复、拒绝、审计和降级测试；任何路径不得绕过
统一 engine。

---

## Later

[Semantic Metadata](roadmap/directions.md#foundation-semantic-metadata)、
[完整 Public Eval](roadmap/metrics.md#agent-与业务效果)、
[推断与策略组合安全](roadmap/directions.md#l16-data-inference-and-policy-composition-safety)
（受监管部署前必须完成）、
[MCP 协议一致性与契约稳定](roadmap/directions.md#l17-mcp-protocol-conformance-and-contract-stability)、
[受治理配置脚手架](roadmap/directions.md#l18-governed-configuration-scaffolding)、
第二种架构验证型 Provider（SQL Server 或 ClickHouse）及其他战略方向均按证据
升级。具体范围、触发证据和跨阶段非目标见
[Evidence-Gated Directions](roadmap/directions.md)；Provider 能力模型与候选顺序
以 [Provider Roadmap](provider-roadmap.md) 为唯一事实源。

---

## 衡量与维护

技术可信度、Agent 效果、采用生态、供应链和公开数字规则见
[Roadmap Metrics](roadmap/metrics.md)。

- 每个 release 后复审阶段证据和 `Next` 顺序；
- 复审时为每个 `Next` 项记录证据缺口与其生产机制（如 Eval、参考部署、
  Demo 反馈渠道）；没有生产机制的门禁项要么补建机制，要么降级 `Later`；
- 新能力进入 `Committed` 前必须有问题证据、明确非目标和二元验收条件；
- Later 项不因时间流逝自动升级；
- 未完成事项不自动滚入下一版本；
- 已发布行为移入 release notes 或对应事实文档。
