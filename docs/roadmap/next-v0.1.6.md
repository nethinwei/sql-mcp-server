# v0.1.6（候选）— Observable + Measurable

状态：**Next**（未获得版本承诺）

返回[主路线图](../roadmap.md)。

本文件把主路线图中的 `Next 1 — Observable + Measurable` 具体化为 v0.1.6
的候选范围。按路线图规则，它在满足进入门禁前不构成发布承诺；进入
`Committed` 时再转为正式承诺文档。

## 进入门禁

- [v0.1.5](committed-v0.1.5.md) 验收完成；
- 关键拒绝路径可通过 `decision ID` 关联 MCP 响应、审计与 trace
  （即 v0.1.5 落地计划中的 WP1/WP2 是直接前置）。

## 目标

让拒绝、成本、故障和 Agent 效果从“可运行”变为“可解释、可测量”：任何
一次拒绝或故障都能通过 telemetry 定位来源，性能与 Agent 效果有可复现的
数字基线。

## 范围

- **健康分离**：将现有 `/healthz` 拆分为 liveness、snapshot readiness 和
  数据库 readiness 三类端点，分别表达进程存活、配置快照可用和数据库
  可达；
- **最小可观测**：最小 metrics 集与结构化日志，均可通过 `decision ID`
  与审计、trace 关联；补齐 `x/otel` 的 TracerProvider 初始化，使既有
  hook 产生真实 span；
- **审计事件 schema 定版**：为审计 JSON Lines 字段补 json tag 并固定命名
  （当前为 Go 字段名，正在成为事实契约，越晚定版破坏面越大）；主路径补记
  entity、action 与拒绝码（Denial `code`），使审计能独立解释每一次拒绝；
  定版后纳入 [tool-contract.md](../tool-contract.md) 兼容规则；
- **协议 smoke 进主 CI**：stdio 与 streamable HTTP 协议 smoke 从
  release 链前移到 PR/主分支 CI；
- **性能基线**：可复现的 p50/p95/p99 data-plane overhead benchmark，
  具备固定环境、fixture 和复现命令；
- **Agent Eval pilot**：20–30 个固定任务，按 [Roadmap Metrics](metrics.md)
  的指标（first-call success、repair rate、token 开销等）运行，产出对
  Next 2（语义元数据）的 go/no-go 结论。

## 退出门禁

- telemetry 能解释每一次拒绝和故障的来源；
- benchmark 与 pilot 具备固定环境、评分规则和可执行的复现命令；
- pilot 形成书面 go/no-go 结论，作为 Next 2 是否升格的证据。

## 非目标（证据未触发，不列入 v0.1.6）

- 语义元数据（Next 2）：需 pilot 证明 grain、时间、枚举、单位或 catalog
  token 是显著失败源后才升格；
- 新 Provider（SQLite、SQL Server、ClickHouse 等）：按
  [Provider Roadmap](../provider-roadmap.md) 的进入条件独立评估，不占用
  本版本；
- 持久化 revision、控制面 API、管理 UI、durable audit：维持 Next 3 /
  Later，不因 v0.1.5 完成设计评审而自动升级；
- 完整可观测性平台、SLO/告警与外部审计 sink：属于 L5，需真实部署证据。
