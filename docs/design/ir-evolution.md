# IR Evolution — Governed Expressiveness and Provider Optimization

状态：**设计方向（未获版本承诺）**。

本文是长期架构方向的详细设计，对应
[Evidence-Gated Directions](../roadmap/directions.md) 中的
L12（Governed Query Expressiveness）与 L13（Provider Optimization
Extensibility）。任何能力只有满足对应触发证据并按 roadmap 规则升级后才进入
版本承诺；本文的能力清单是候选池，不是实施清单。

## 目标

将项目逐步演进为一个面向 AI Agent 的、具备统一治理能力和数据库原生优化能力
的跨数据库查询编译框架。该方向包含两个互补部分：

1. **Governed Query Expressiveness**：扩大统一 IR 能够安全表达的查询能力；
2. **Provider Optimization Extensibility**：允许各数据库 Provider 在保持统一
   语义和治理边界的前提下，充分利用数据库原生能力进行优化。

整体处理链路：

```text
Agent Intent
    ↓
Canonical Governed IR
    ↓
Authorization / RLS / Mask / Cost Enforcement
    ↓
Provider Lowering
    ↓
Provider Physical Plan
    ↓
Optimized Execution
```

核心原则：

> 最大化治理约束下的逻辑表达能力，同时最大化不同数据库的原生执行能力，
> 且不弱化共享安全不变量。

---

## 1. Governed Query Expressiveness（L12）

目标：在不引入任意 SQL、不削弱授权、安全、成本和可解释性边界的前提下，持续
扩大 IR 可表达的声明式关系查询集合。

项目不以复刻完整 SQL 语法为目标，而是定义受治理 SQL 子语言 `SQL_G`，并逐步
使 IR 在该范围内达到表达完备性：

```text
∀ q ∈ SQL_G, ∃ e ∈ IR_G:  ⟦q⟧ = ⟦e⟧
```

同时要求所有合法 IR 均可编译为语义等价的参数化 SQL：

```text
∀ e ∈ IR_G, ∀ p:  ⟦Codegen_p(e)⟧ = ⟦e⟧
```

### SQL_G 的约束

`SQL_G` 中的查询必须满足：

- 只能访问已声明且已授权的实体、字段和关系；
- 所有调用者提供的值必须参数化；
- Join 只能沿显式声明和授权的关系构造；
- 行级策略不可被覆盖或弱化；
- hidden、mask 和字段 ACL 在所有查询位置保持一致；
- 查询必须服从成本、预算、超时、返回行数和数据出站限制；
- 不允许通过任意 SQL、任意表达式、任意函数或任意子查询绕过治理边界。

### 候选能力池

计划逐步评估的能力（每项独立立项，不整体推进）：

- 显式关系 Join；
- Semi Join 和 Anti Join；
- Union、Intersect 和 Except；
- Group By、Having 和复合聚合；
- 条件表达式和受控标量函数；
- Exists 和存在性查询；
- 窗口函数；
- CTE 和可复用查询片段；
- 在具备明确语义和真实需求后考虑递归查询。

### 每项能力必须定义

- 输入和输出类型；
- bag semantics；
- NULL 和三值逻辑；
- 排序和结果确定性；
- 聚合空集行为；
- 字段可见性传播；
- RLS、mask 和 ACL 的作用位置；
- 成本估算和强制执行语义；
- 各 Provider 的支持和降级行为。

### 逐项升级门禁

新 IR 能力必须通过以下门禁：

- Agent Eval 或真实部署能够证明存在明确需求；
- 无法通过现有 IR 合理、稳定且低成本地完成；
- 具有独立于 SQL 文本的明确语义；
- 所有支持的 Provider 通过统一语义测试；
- codegen 通过 property-based 或 differential testing；
- 不支持该能力的 Provider 明确 fail closed；
- 不得引入 `RawSQL`、`RawExpression` 等逃生口。

最终目标不是"生成所有 SQL"，而是：

> 对项目目标场景中所有值得支持的查询，IR 均能以结构化、可授权、可估算、
> 可解释且跨数据库一致的方式表达。

与 L1（Executable Semantic Layer）的关系：L1 的 `Metric`/`Dimension` 必须
编译到现有 IR，是本方向表达力的消费者；L12 扩大的是 IR 本身的表达集合。

---

## 2. Provider Optimization Extensibility（L13）

目标：在保持统一逻辑语义和核心治理不变量的前提下，为不同数据库开放足够且
类型化的优化切口，使每个 Provider 能充分利用其原生查询、类型、参数绑定、
执行计划和资源控制能力。

### 职责划分

核心统一定义：

- Canonical IR；
- 查询逻辑语义；
- authorization、RLS、mask、cost enforcement、audit；
- 安全不变量。

Provider 负责：

- capability 声明；
- 语义保持的 IR lowering；
- 数据库原生逻辑优化；
- SQL codegen、参数绑定、类型映射；
- Explain 和成本解析；
- 物理执行计划；
- 事务、取消、流式读取和临时资源管理。

### 三层模型

1. **Canonical IR**：数据库无关的规范逻辑表示，是授权、策略注入和语义验证
   的基础；
2. **Provider IR / Lowered IR**：Provider 根据数据库特性生成的语义等价表示，
   可以包含受控的数据库特有节点；
3. **Physical Execution Plan**：描述最终 SQL、参数绑定、批处理、临时表、
   游标、异步任务或其他实际执行方式。

Provider lowering 必须满足语义保持与治理闭包：

```text
⟦Lower_p(e)⟧ = ⟦e⟧
AuthorizedResources(Lower_p(e)) ⊆ AuthorizedResources(e)
```

### 候选扩展点

- 结构化 capability model；
- logical rewriter；
- physical planner；
- dialect codegen；
- scalar、list、tuple 和 bulk parameter binder；
- type adapter 和 coercion；
- native function registry；
- Explain parser 和 cost estimator；
- execution strategy；
- transaction capability；
- cancellation 和 timeout control；
- temporary resource lifecycle；
- result decoder；
- Provider-specific metrics 和 audit details。

系统级扩展边界清单（IdentityProvider、AuditSink 等）以 L11（Constrained
Extensibility）为准；本方向只覆盖 Provider 内部的优化分层。

### Capability 实现方式分级

capability 不只区分支持与否，还应区分实现方式：

- `native`：数据库原生实现；
- `emulated`：核心层或 Provider 模拟；
- `restricted`：受限子集；
- `unsupported`：不提供。

对于 `emulated` 或 `restricted` 能力，Provider 必须声明语义差异、原子性
限制、性能影响、支持版本、成本可见性与失败模式。该维度与
[Provider Roadmap](../provider-roadmap.md) 能力模型中的保证强度
（`unsupported`/`best_effort`/`enforced`）正交：前者描述"怎么实现"，
后者描述"能保证什么"。

### 禁止事项

Provider 可以决定**如何执行**，但不能决定**是否允许执行**。任何 Provider
扩展均不得：

- 绕过 authorization、RLS、mask、budget 或 audit；
- 接受任意 SQL、标识符或函数名称；
- 在授权完成后引入新的业务实体或字段；
- 删除或弱化系统谓词；
- 将只读 IR 转换为具有副作用的操作；
- 静默改变 NULL、bag、排序、聚合或事务语义；
- 通过数据库特有优化绕过统一 engine。

### 每项优化的验收要求

- Canonical IR 与 lowered IR 的差分测试；
- 跨 Provider 语义一致性测试；
- 权限、安全和成本边界回归测试；
- Provider 版本能力矩阵；
- native、emulated、restricted 和 unsupported 路径测试；
- Explain 解析 corpus；
- 性能 benchmark，证明优化切口具有可测量收益；
- fail-closed 行为验证。

最终目标不是让所有数据库生成相同的 SQL，而是：

> 对同一个受治理逻辑查询，各 Provider 能够在保持统一结果语义和安全边界的
> 前提下，选择最适合自身数据库的表达、绑定、计划和执行方式。

---

## Combined Success Criteria

两个方向共同构成项目长期的 IR 演进目标：

```text
Business Intent → Governed Canonical IR → Provider Lowering → Optimized Execution
```

最终应达到：

- 逻辑能力不受当前最低公分母限制；
- 数据库原生优化能力不被统一抽象抹平；
- 所有查询仍经过统一治理和安全边界；
- 不通过任意 SQL 或万能 Hook 换取灵活性；
- 新能力可以被测试、测量、审计和证明；
- IR 表达能力与 Provider 执行能力可以独立演进。

一句话概括：

> Maximize governed logical expressiveness and provider-native execution
> capability without weakening shared safety invariants.
