# Diagnostic Evaluation and Workload Hardening

本文件是 `v0.1.10`（Diagnostic Evaluation and Workload Hardening）范围与
验收的唯一事实源，由[主路线图](../roadmap.md)引用。

`v0.1.9` 交付了四模块真实业务 reference model 与 21 个 Guided 负载任务；
v4 正式运行（[结论](../../eval/results/2026-07-12-deepseek-v4-flash-workload-v4.md)）
显示最终正确率已接近饱和。本版本将 Eval 从「验证最终答案」升级为能够
测量自主语义理解、治理边界、过程效率，并将失败归因到明确能力缺口。

本版本**不增加运行时产品能力**；产出用于决定后续应进入 Semantic Metadata、
Catalog Discovery、Governed Query Expressiveness、Tool Contract、Evidence
Envelope，或维持现状。

## 一、问题背景

v4 已覆盖相似实体、多关联路径、多 grain、多时间字段、状态机、规则版本、
多币种、异步一致性等复杂度，且 Guided 任务在受控契约下 21/21 通过。

当前 Eval 局限：

1. 最终正确率饱和，难以区分产品能力缺口；
2. 部分题目直接提示正确表、字段、粒度或计算方式；
3. 缺少澄清、拒绝和不确定性任务；
4. 主要检查最终答案，不能自动识别 grain/时间/状态/单位错误；
5. 以单轮独立查询为主，缺少调查工作流；
6. 治理攻击、跨角色用例未系统化；
7. Catalog 成本与首次决策质量尚未成为正式门禁。

本版本以提高每条用例的**诊断价值**为主要目标，而非单纯增加业务问题数量。

## 二、任务 schema（v5）

正式任务由 `fixtures/v4/tasks/tasks.yaml` 的 21 个 Guided 任务、
`fixtures/v4/tasks-v5/guided-metadata.yaml` 元数据覆盖和
`fixtures/v4/tasks-v5/additions.yaml` 的 27 个扩展任务合并而成。

### 2.1 能力维度 `dimensions`

```yaml
dimensions:
  domain: [payment-orchestration]
  business_operation: [monitoring, reconciliation]
  query_shape: [aggregation, anti_join, multi_hop_relation]
  semantic_challenges: [grain, time_semantics, status_semantics, unit_semantics,
    version_semantics, async_consistency]
  governance_challenges: [tenant_isolation, field_acl, masking, inference_risk]
  difficulty: L3
  source: expert_derived   # expert_derived | dogfooding | regression_derived
```

目标：自动生成覆盖矩阵，识别重复覆盖、缺负向任务、缺非 Guided 任务。

### 2.2 Prompt 分层 `prompt_level`

| 层级 | 用途 |
| --- | --- |
| `guided` | 明确业务口径与查询约束；稳定回归 |
| `natural` | 自然业务语言，不泄露查询路径 |
| `ambiguous` | 保留真实歧义，测试澄清能力 |

不要求所有场景三档齐全；优先高价值场景的代表性任务。

### 2.3 期望行为 `expected_behavior`

| 值 | 含义 |
| --- | --- |
| `answer` | 口径与权限充分，直接回答 |
| `clarify` | 缺少关键口径，必须追问 |
| `deny` | 违反租户、角色、ACL 或 mask |
| `qualify` | 可回答但须附带限定条件 |
| `unsupported` | 数据不足以支持用户结论 |

### 2.4 Counterfactual Oracle

高价值 `answer` 任务可附 `oracle`：

```yaml
oracle:
  confounders:
    attempt_grain:
      value: "0.7134"
      failure_category: grain
    created_at_window:
      value: "0.8276"
      failure_category: time_semantics
```

答案匹配 confounder 值时，机械归因到对应 `failure_category`。

Confounder 值由 `fixtures/v4/generator` 同源计算（`Oracles(cfg)`），
不得手工维护。

## 三、任务规模与 suite 结构

### P0（必须）

| 类别 | 数量 | 说明 |
| --- | --- | --- |
| Guided 回归 | 21 | v4 任务保留，加 v5 元数据 |
| Natural/Ambiguous | 8–10 | 高价值场景自然语言改写 |
| 行为扩展 | 8–10 | clarify/deny/qualify/unsupported |
| 治理 suite | 8–12 | 跨租户、mask、成本攻击等 |
| Oracle 任务 | ≥8 | 含 confounder |

正式单轮任务约 **35–50**；硬上限 50（roadmap 锁定）。

### P1 草案（不阻塞发布、不进入默认诊断轨）

- 3 个多轮调查场景（`fixtures/v4/scenarios/`）
- medium catalog profile（100–150 entities）
- 5 个跨角色任务草案（`tasks-v5/cross-role-draft.yaml`）
- 跨 Provider 任务留 `v0.1.11`（capability model 消费者）

P1 仅保留设计输入；在 runner、fixture 与评分规则接入前，不计入覆盖矩阵、
正式任务数或 `v0.1.10` 退出门禁。

### 目录布局

```text
fixtures/v4/
  tasks/           v4 guided（冻结，workload 轨默认）
  tasks-v5/        v0.1.10 元数据、正式扩展任务与 P1 草案
  scenarios/       P1 多轮调查草案
  oracles/         confounder 元数据（生成器为事实源）
  coverage/        dimensions + 覆盖矩阵
  catalog/medium/  P1 规模 profile 草案
  use-cases.md     选材参考草案（非验收全集）

eval/suites/       suite 索引与说明（渐进迁移）
```

## 四、Eval 轨与指标

### 4.1 三轨体系

| 轨 | 命令 | 用途 |
| --- | --- | --- |
| 回归 | `make eval-pilot` | v3 冻结基线（32 任务） |
| 负载 | `make eval-workload` | v4 Guided 业务负载（21 任务） |
| 诊断 | `make eval-diagnostic` | v5 全量诊断任务集 |

### 4.2 报告指标（诊断轨）

保留 `final correctness`，新增：

**正确性**：final task correctness、first-call success、expected behavior
accuracy（clarify/deny/qualify/unsupported）。

**效率**：平均工具调用、平均 discovery 调用、含 discovery 任务比例、
prompt/completion tokens。

**诊断性**：automatically attributed failures、unattributed failures、
product-fixable vs model-only failure rate。

**治理**：deny/violation 任务通过率、指定 denial code 与受保护值泄漏检查。

## 五、治理 suite 最低覆盖

- 跨租户访问（显式与隐式）
- Mask 绕过（分组、distinct、前缀、排序、join、聚合差分）
- 小集合泄漏（单主体过滤下的身份侧信道）
- 差分推断（两次合法聚合相减）
- 成本攻击（无界时间、高基数 group by、笛卡尔、过大结果、重复分页）
- 数据内容注入（字段值含指令文本）

## 六、版本验收标准

v0.1.10 完成需同时满足 [Roadmap](../roadmap.md) 退出门禁与下文：

1. 任务 schema v5 与 loader 交付；`make eval-diagnostic` 可运行；
2. 覆盖矩阵可自动生成（`make eval-coverage`）；
3. 五种 `expected_behavior` 均有正式任务；
4. ≥8 任务具 counterfactual oracle 且评分器可机械归因 confounder 匹配；
5. 诊断报告含归因率、行为准确度、unattributed 与 product-fixable 比例；
6. 回归轨、integration、conformance、workload 差分无回归；
7. 公开工具契约保持兼容。

## 七、非目标

- 新增 Provider、扩展 IR、Semantic Metadata、Catalog Search API
- Evidence Envelope、控制面
- 将 use-cases.md 128 条全部纳入 Eval
- 削弱 schema 复杂度以降低 Eval 成本
- 修改现有 RBAC、mask、策略语义

## 八、发布后决策规则（§8）

`v0.1.10` 不直接承诺 `v0.1.11` 产品功能。根据诊断 Eval 结果：

| 失败集中领域 | 分流方向 |
| --- | --- |
| grain、时间、状态、单位、历史版本、业务术语 | Semantic Metadata |
| 大 schema 下 discovery/token/选表失败 | Catalog Discovery |
| IR 无法表达、需非治理 SQL、调用成本过高 | Governed Query Expressiveness |
| 拒绝信息不可操作、工具边界模糊 | Tool Contract |
| 无法解释答案来源 | Evidence Envelope |
| 仅模型弱点、换模型后消失 | 不扩张核心 |

结论写入 `eval/results/`，并更新 Roadmap Dormant 重开条件。
