# Roadmap

当前稳定基线为 `v0.1.9`。已发布能力以
[发布说明](releases/v0.1.9.md)、[CHANGELOG](../CHANGELOG.md)、
[配置参考](configuration.md)和[安全模型](security.md)为准。

本文件只给出未发布成果的顺序和门禁：

- **Committed**：当前版本承诺；同时只保留一个版本；
- **Milestone**：跨版本的二元判据集合，标记阶段切换，不绑定日期；
- **Parallel Workstream**：非版本化的证据生产机制，与版本并行推进，
  不占 Committed 位，不阻塞发布；
- **Next**：已排序阶段，满足进入条件后才获得版本承诺；
- **Dormant**：门禁评估已完成且结论为 no-go，仅保留重开条件；
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

### v0.1.10 — Diagnostic Evaluation and Workload Hardening

`v0.1.9`（Real Business Workload Model）已完成验收，成果见
[发布说明](releases/v0.1.9.md)；其发布复审确认：真实负载轨对现有三库 +
现有 IR 已接近饱和（三轮 21/21），**最终正确率 alone 已无法区分**
Semantic Metadata、Catalog Discovery、Governed Query Expressiveness 或
Tool Contract 哪项值得进入产品实现。正式运行暴露的主要问题已从
「能不能答对」转向「是否自主理解语义、过程是否高效、错误是否可诊断」。

本版本将 v4 真实业务 fixture 与 Eval 从「验证 Agent 最终能否得到正确答案」
升级为能够测量自主语义理解、治理边界遵守、合理成本完成，并将失败归因到
明确能力缺口。**建设测量与诊断基础设施，不预设下一项产品功能。**

范围与验收以 [Diagnostic Evaluation 设计文档](design/diagnostic-evaluation.md)
为唯一事实源。

**问题证据**：[v4 正式结论](../eval/results/2026-07-12-deepseek-v4-flash-workload-v4.md)
（21/21 饱和、十类归因出口全空）；现有 workload 任务多为 Guided 表述、
仅支持 `answer` 机械判分，缺少 clarify/deny 任务、counterfactual oracle
与覆盖矩阵。

**阶段结果（P0）**：

- 任务 schema v5：`dimensions`、`prompt_level`（guided/natural/ambiguous）、
  `expected_behavior`（answer/clarify/deny/qualify/unsupported）、
  `oracle` + `confounders`；任务集见 `fixtures/v4/tasks-v5/`；
- 保留 v4 的 21 个 Guided 任务为稳定回归子集；
- 新增 Natural/Ambiguous 任务（8–10）、行为扩展任务（8–10）、治理 suite
  （8–12）；正式单轮任务规模约 35–50；
- 覆盖矩阵自动生成（`fixtures/v4/coverage/` + `make eval-coverage`）；
- ≥8 个核心任务具 counterfactual oracle（同源 generator 计算 confounder）；
- Eval 诊断轨（`make eval-diagnostic`）与升级报告指标（归因率、行为准确度、
  平均工具/discovery 调用、token、unattributed 失败、product-fixable 比例）。

**P1 草案（不阻塞发布、不进入默认诊断轨）**：

- 3 个多轮调查场景草案（`fixtures/v4/scenarios/`）；
- medium catalog scale profile 设计占位（`fixtures/v4/catalog/medium/`）；
- 5 个跨角色任务草案（`fixtures/v4/tasks-v5/cross-role-draft.yaml`）；
  跨 Provider 等价任务留 `v0.1.11`。

**非目标**：不新增 Provider、不扩展 IR、不实现 Semantic Metadata / Catalog
Search API / Evidence Envelope / 控制面；不把
[use-cases.md](../fixtures/v4/use-cases.md) 128 条全部转为 Eval；不削弱
schema 复杂度以降低 Eval 成本；不为制造版本而扩张 Agent 运行时能力。

**退出门禁**：

*用例模型*

- [ ] 所有正式任务具结构化 `dimensions`；覆盖矩阵可自动生成；
- [ ] 每项高优先级 semantic 能力至少有 1 个 non-Guided 任务；
- [ ] 每项高风险治理能力至少有 1 个负向任务。

*行为覆盖*

- [ ] `answer` / `clarify` / `deny` / `qualify` / `unsupported` 均有正式任务；
- [ ] ≥8 任务具 counterfactual oracle。

*诊断能力*

- [ ] grain/time/status/unit 典型错误可机械或半机械归因；
- [ ] 未归因失败、fixture ambiguity 在报告中单列；
- [ ] 报告给出 product-fixable 失败比例。

*工程质量*

- [ ] 回归轨 32/32、integration/conformance/workload 差分无回归；
- [ ] 现有公开工具契约兼容；task schema 有版本号；
- [ ] 发布链检查（fmt/vet/test/race/docs-check）通过。

**发布后决策**：本版本不直接承诺 `v0.1.11` 产品功能；根据诊断 Eval 结果
分流至 Semantic Metadata、Catalog Discovery、Governed Query Expressiveness、
Tool Contract、Evidence Envelope 或维持现状（见设计文档 §8）。

---

## Milestone v0.2.0 — 治理数据面收口

`v0.2.0` 是阶段切换标记：**受治理数据面（治理语义、跨库一致性、评测
体系、采用入口）收口，此后 Committed 优先从"管理面阶段"取项**。不绑定
日期；以下二元判据全部满足即可发布：

- [ ] Diagnostic Eval 交付（`v0.1.10`）；
- [ ] Provider capability model 交付（`Next 1`，预期 `v0.1.11`）；
- [ ] Evidence-Backed SQLite 交付（`Next 2`，预期 `v0.1.12`）并通过
  conformance + workload 差分验收；
- [ ] dogfooding：至少一套真实或脱敏的支付中台工作负载经 `EVAL_DSN`
  模式运行并输出问题清单（v0.1.9 发布复审移交本 Milestone 的必要判据，
  harness 与模板已随 v0.1.9 交付）；
- [ ] 黄金 Demo 补全并发布演示材料（外部证据冲刺前两项）；
- [ ] ≥1 页 dogfooding case study 与 ≥2 个 design partner 试用观察
  记录（外部证据冲刺）；
- [ ] Result Provenance and Evidence Envelope 交付，或其进入门禁经
  发布复审判定证据不成立并书面记录（`Next 3`）；
- [ ] `v0.2.0` 发布链上 Eval 三轨（回归/负载/诊断）全绿，失败归因体系
  至少产出一次基于诊断轨或 dogfooding 的 go/no-go 结论。

---

## Parallel Workstream — 外部证据冲刺

`Next 3` 与管理面阶段的进入门禁均依赖真实部署或真实用户反馈，该类证据
当前没有生产机制。按下文"衡量与维护"的规则（没有生产机制的门禁项要么
补建机制，要么降级），本工作流即为其证据生产机制，同时直接供给
`v0.2.0` Milestone 的 dogfooding、Demo 与 case study 判据：

- 黄金 Demo 补全：在 [quickstart](quickstart.md) 现有低权限读取、字段
  脱敏、tenant 隔离与拒绝路径之上，补齐"大查询被成本闸门拒绝后自修复"
  与"用审计 decision trace 解释一次拒绝"两个场景，使 Demo 完整覆盖
  产品差异声明；
- 3 分钟演示视频与"对比任意 SQL MCP Server"的安全架构对照材料；
- 维护者 dogfooding 部署：以真实或脱敏的支付中台工作负载（即
  [真实业务负载模型](design/business-workload-model.md)验收标准中的
  dogfooding 项）经本服务暴露给真实 Agent 任务，形成第一个不依赖外部
  响应的参考部署和一页 dogfooding case study——维护者自己的生产工作
  负载也是采用证据；
- 邀请 3–5 个 design partner（AI 数据分析、内部 BI、SaaS Agent 或
  数据库安全方向），每个只观察四件事：能否安装、能否配置第一个
  Entity、Agent 能否发现并调用、第一次失败发生在哪里；
- 每个试用形成一页 case study：原问题、数据库规模、Entity 数量、Agent
  任务、治理要求、接入成本、失败与修复、最终效果。

产出（参考部署、失败记录、反馈）直接作为 `Next` 各阶段门禁与
[Graduation Targets](roadmap/metrics.md#graduation-targets) 的证据输入。

---

## Next（v0.2.0 前 — 数据面与采用）

### Next 1 — Provider Capability Model

`v0.1.10` 发布后获得版本承诺（预期 `v0.1.11`）。进入门禁与退出验收以
[Provider Roadmap](provider-roadmap.md) Capability Model 章节为唯一事实源。

**问题证据**：现有实现（`core/dialect.Capabilities`）是平铺 bool，成本
闸门装配（`cost.NewGateFromCapabilities`）直接消费这些 bool，无法表达
保证强度与证据状态——即无法区分"数据库提供该能力"与"本服务已装配可测试
的强制机制"。SQLite 与新 Provider 立项的前置工程项。

**阶段结果**：

- capability 重构为"范围 + 强度 + 证据"模型：保证强度统一为
  `unsupported` / `best_effort` / `enforced`；字段定义以 Provider Roadmap
  为唯一事实源；
- 闸门装配与 codegen 渲染改为消费新模型；
- PostgreSQL、MySQL、OceanBase 三个现有 Provider 的声明按新模型迁移；
  [Provider 兼容矩阵](provider-compatibility.md)可区分保证强度与证据状态；
- 字段取舍以 SQLite（`Next 2` 交付对象）为校验对象：模型必须能表达
  "缺少 `enforced` 成本证明时由核心层等价强制或 fail closed"这类组合；
- **跨 Provider conformance 套件**（≥5 任务）：PostgreSQL / MySQL /
  OceanBase 上结果等价、拒绝等价、治理等价，作为 capability model 的
  首个实际消费者。

**非目标**：不新增 Provider（SQLite 随 `Next 2`）；不引入 L13 实现方式
维度（仅预留）；不改变三库现有运行时行为与闸门语义（行为等价重构）。

**退出门禁**：

- [ ] 每项 capability 分别表达范围、保证强度和证据，不再存在混合语义的
  单 bool；
- [ ] "`best_effort` 不能满足硬限制；缺少 `enforced` 能力时，必须由核心层
  提供等价强制或 fail closed" 有测试锁定；
- [ ] ≥5 个跨 Provider 诊断任务全绿；
- [ ] 三库 integration/conformance/workload 差分无回归；兼容矩阵更新。

---

### Next 2 — Evidence-Backed SQLite

进入门禁、SQLite 首版范围和退出验收均以
[Provider Roadmap](provider-roadmap.md) 为唯一事实源；`v0.1.8` conformance
suite 与 `v0.1.9` workload 差分是任何新 Provider 的验收前置；capability
model（`Next 1`）为前置工程。

**进入条件已确立**（`v0.1.9` 发布复审）：架构验证 + 采用目标——SQLite 是
新 capability model 的首个新增 Provider 消费者，验证"弱成本证明、核心层
兜底"的执行模型，同时是产生真实采用证据的最低门槛入口。`Next 1` 交付后
本阶段即获得版本承诺（预期 `v0.1.12`）。

阶段结果：一个受现有 IR 和统一 engine 约束的窄 SQLite Provider，通过
conformance corpus 与 workload 差分双重验收。

---

### Next 3 — Result Provenance and Evidence Envelope

进入门禁：Eval 失败归因、真实用户反馈或公开对照需求证明"Agent 给出的业务
结论无法追溯到数据、配置版本和一次具体执行"是信任或采用障碍。证据生产
机制：外部证据冲刺的 design partner 反馈与 Eval 真实负载轨（v4）失败
归因。本项是 `v0.2.0` Milestone 判据（交付，或书面记录证据不成立）。

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

## 管理面阶段（v0.2.0 后）

`v0.2.0` Milestone 达成后，Committed 优先从本阶段取项。目标从"Agent 能否
正确、安全地查到数据"转向"运营者能否放心地变更、审计和管理这套系统"：
schema 漂移治理 → 最小控制面 → 由证据触发的
[数据治理与 durable audit](roadmap/directions.md#l8-data-governance-and-durable-audit)、
[企业身份与规模](roadmap/directions.md#l7-enterprise-identity-and-scale)与
[管理 UI](roadmap/directions.md#l9-management-ui)。顺序内两项仍受各自
进入门禁约束，管理面不因 Milestone 达成而自动立项；任何管理面路径不得
绕过
统一 engine。

### 管理面 1 — Schema Drift and Compatibility Governance

进入门禁：真实部署出现数据库 schema 演进需求，或最小控制面（管理面 2）
进入条件满足——本阶段是其前置。证据生产机制：外部证据冲刺的参考部署。

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

### 管理面 2 — Minimum Control Plane

进入门禁：至少一个真实部署明确需要 revision、diff、simulate、publish 或
rollback，且无法由现有 CLI 热重载流程合理满足；管理面 1 的 schema drift
治理为前置。证据生产机制：外部证据冲刺的参考部署。

只实现已被真实需求触发的最小操作；revision 数据模型、配置兼容性、失败处理和
在途请求语义遵循已评审的
[Revision 与 Snapshot 设计](design/revision-snapshot.md)。

退出门禁：实现满足上述设计并具备恢复、拒绝、审计和降级测试；任何路径不得绕过
统一 engine。

---

## Dormant — Eval-Driven Agent Improvement

`v0.1.7` 校准已完成并给出结论
（[2026-07-12 v3](../eval/results/2026-07-12-deepseek-v4-flash-v3.md)，
三轮 31/32、32/32、31/32）：
[Semantic Metadata](roadmap/directions.md#foundation-semantic-metadata)、
[Catalog Discovery](roadmap/directions.md#l2-catalog-discovery) 和
[Governed Query Expressiveness](roadmap/directions.md#l12-governed-query-expressiveness)
均 **no-go**——定向任务全部通过，失败无一可归因于对应缺失；唯一证据支持
项（无谓词聚合拒绝的 hint 收紧）已随 `v0.1.8` 交付。

`v0.1.9` 真实负载轨重评估
（[2026-07-12 v4](../eval/results/2026-07-12-deepseek-v4-flash-workload-v4.md)，
三轮 21/21）：Semantic Metadata 与 Governed Query Expressiveness 维持
**no-go**（grain/时间/单位/版本化语义任务零失败；多跳靠分解完成）；
Catalog Discovery 转**继续观察**——45 实体 catalog 下实体选择零失败，
但单任务 prompt token 较 v3 上升 2.6 倍，成本信号随 schema 规模增长。
两轮结论均在合成 fixture 与单模型下成立，不外推为"所有模型无短板"。

重开条件：`v0.1.10` 诊断 Eval 产出新的可测量失败模式时，按设计文档
§8 分流——语义元数据缺失进入 Semantic Metadata，IR 无法表达进入
Governed Query Expressiveness，大 schema 选择失败或 catalog token 成本
显著进入 Catalog Discovery，错误提示或[工具契约](tool-contract.md)导致
的可修复失败只收紧对应契约，无法解释答案来源进入 Evidence Envelope。
dogfooding、design partner 反馈或更弱模型/更大 scale 运行提供补充证据。
不为制造版本内容而扩张 Agent 功能。

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
- 没有证据支持的版本核心时，`Committed` 允许为空——空 Committed 是
  evidence-gated 的正确结果，不为发版而立项；
- Later 项不因时间流逝自动升级；
- 未完成事项不自动滚入下一版本；
- 已发布行为移入 release notes 或对应事实文档。
