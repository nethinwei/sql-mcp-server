# Roadmap

当前 `v0.1.2` 范围已完成。已实现能力、默认值和已知边界分别以
[v0.1.2 发布说明](releases/v0.1.2.md)、[配置参考](configuration.md) 和
[安全模型](security.md) 为准。

**本文件是未发布工作的唯一规划来源。** `docs/releases/` 只记录**已发布**
版本的变更与迁移说明；版本真正发布前不在该目录预建 release 文档，避免与
roadmap 重复维护。

## 方向

从静态 YAML 网关升级为**可运营的数据访问控制面**：控制面（策略、数据源、
token）与数据面（MCP 查询执行）分离；授权模型统一，拒绝可解释，策略可审核、
可发布、可审计。

---

## v0.2.0 — 控制面 MVP

目标：在不依赖 UI 的前提下，提供可持久化、可版本化的控制面 API，并落地
token / capability profile 模型。

### 控制面与存储

- 引入控制面 API（独立端口或与数据面分离的 admin listener），管理：
  - 数据源（增删改、连通性校验、自省同步）；
  - 实体与字段暴露；
  - 成本/预算/限流策略；
  - MCP 调用 token 与能力配置。
- 策略与配置持久化存储（替代「仅 YAML 文件」作为唯一真相源）；支持
  draft → review → publish 发布流程。
- 控制面操作审计：谁、何时、改了什么、何时生效；支持回滚到上一发布版本。
- 明确**单一授权模型**：token / capability profile 映射到现有
  RBAC（role、fieldACL、rowPolicies）与 cost/budget 限制，不长期维护两套
  并行语义。

### Token 与能力

- 为 MCP 调用方签发 machine credential（token）；每个 token 可配置：
  - 有效工具/动作（read、write、aggregate、procedure 等）；
  - 可访问的数据源与实体（库表）；
  - 成本、预算、限流等执行限制。
- 支持「预设能力组（profile）+ 单独能力覆盖」组合，便于批量赋权与例外配置。
- token 变更后，数据面在可接受延迟内生效（与配置发布模型联动）。

### 策略运营

- template fingerprint **发现与审核工作流**（算法已在 v0.1.2 稳定）：列出待审
  模板、审批/拒绝、写入 allow/reject 策略。
- **拒绝可解释性**：成本闸门、RBAC、预算拒绝时返回结构化原因（缺哪条策略、
  触发了哪条规则）。
- **策略模拟 / dry-run**：管理员预览某 token 对某实体的有效权限与预估限制，
  不执行真实查询。

### 数据面衔接

- 数据面从控制面拉取或订阅已发布配置快照；保留 YAML 作为 bootstrap / GitOps
  兼容入口，但不是动态运营的唯一路径。
- 热重载时安全刷新 MCP 工具发现集合（`tools/list`），保持在途请求与 session
  一致性（drain-before-publish 已在 v0.1.2 落地）。

---

## v0.2.1 — 可观测与可部署

目标：达到可被企业 SRE 接纳的运维基线。

- 默认可用的 metrics endpoint、结构化日志与 trace 贯通（基于现有 OTel hook）。
- `/healthz` 之外增加 readiness（依赖数据源、控制面存储、配置快照就绪）。
- 审计轮转与外部 sink（S3、syslog、OTLP 等）；查询审计与**控制面变更审计**
  分通道或可筛选。
- 标准部署产物：Docker 镜像、Helm chart、参考架构与安全加固清单（网络、
  mTLS、secret 注入）。
- 运维 runbook：启动失败、reload 拒绝、成本拒绝、队列丢弃等场景的排障指引。
- MCP 官方 conformance 套件；强化配置 schema 契约测试（实例校验，而非仅 JSON
  合法性）；跨 provider 行为一致性测试基线。

---

## v0.2.2 — 管理后台 UI

目标：在 v0.2.0 控制面 API 稳定后，提供可视化运营界面（UI 是 API 的壳，
不绕过控制面直接改运行时状态）。

- Web 管理后台，调用 v0.2.0 控制面 API。
- 管理员登录：OAuth/OIDC 或本地密码（**Admin IdP**，与 MCP 调用方身份分离）。
- 数据源、实体、策略、token/profile 的 CRUD 与发布流程可视化。
- fingerprint 审核队列、策略模拟、拒绝原因查询的运营视图。

---

## v0.3.0 — 多实例与接入扩展

目标：水平扩展与更丰富的客户端接入，不削弱 v0.2.x 的安全承诺。

### 多实例与配额

- 多 MCP 实例间的配置分发与版本一致性（发布快照推送或拉取）。
- 分布式 / 多实例 budget 与 token 级配额语义（共享 store 或明确「单实例
  有界」的部署约束并文档化）。
- 可替换的持久 session / event store（streamable HTTP 多实例必需）。
- 将 provider 原生 scan-row cap / resource manager 从 capability 描述变为
  可验证的连接级装配（`statement timeout` 已在 v0.1.2 落地）。
- budget 扫描行限制与 provider/数据库侧可验证硬上限的对齐（在能证明的
  范围内收紧，不将返回行数冒充扫描行数）。

### MCP 客户端接入

- MCP 接入侧 OAuth/OIDC 身份映射（**Client Auth**，machine token 与用户
  subject 委托分离）；明确与 Admin IdP 的信任域边界。
- 明确 CORS 策略（仅在有浏览器侧 MCP client 场景时启用）。

---

## P2+

有明确客户需求或使用场景后再立项；避免分散 MCP 安全网关主线。

- 配置多文件 merge/include 与 secret provider 插件（Vault 等）。
- 文档站、交互式 playground 与可复现 benchmark（对外证明性能与成本边界）。
- 物化视图、bucketing/broadcast 等查询策略（需单独成本与安全评审）。
- 通用 join/union 与跨数据源查询（高风险，与当前「受控子集」模型强冲突；
  在此之前关系展开保持同源、单 join pair）。
- OpenAPI/GraphQL 等额外协议面（仅在与 MCP 并行的集成场景被验证后考虑）。
