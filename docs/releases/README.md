# 发布说明索引

当前 GA 为 `v0.1.10`（[发布说明](v0.1.10.md)、
[GitHub Release](https://github.com/nethinwei/sql-mcp-server/releases/tag/v0.1.10)）。

本目录中的发布说明描述各版本发布时点的能力、迁移和证据状态。当前运行时行为以
[配置参考](../configuration.md)、[安全模型](../security.md)和
[Provider 兼容矩阵](../provider-compatibility.md)为准；全版本摘要见
[CHANGELOG](../../CHANGELOG.md)。

## 版本

- [`v0.1.10`](v0.1.10.md)（2026-07-12）：Diagnostic Evaluation and
  Workload Hardening——48 个 v5 正式任务、五种期望行为、覆盖矩阵、
  counterfactual oracle、治理 profile 与升级诊断报告；三轮正式运行
  41/48、43/48、43/48；
- [`v0.1.9`](v0.1.9.md)（2026-07-12）：Real Business Workload Model——
  四模块业务 reference model 与确定性生成器（`fixtures/v4/`）、Eval
  双轨化（v3 冻结回归轨 + v4 真实负载轨，三轮 21/21）、十类失败归因、
  跨 Provider workload 一致性差分与文档一致性 CI 检查；
- [`v0.1.8`](v0.1.8.md)（2026-07-12）：IR Semantics and Provider
  Conformance——读路径 IR 语义规范、reference interpreter、三库
  differential conformance suite（85 用例全绿），及无谓词聚合 hint 契约
  收紧；
- [`v0.1.7`](v0.1.7.md)（2026-07-12）：Agent Eval 校准——任务集 v3（8 个
  定向任务与 20 个 decoy 实体）、first-call success 定义修正与 discovery
  计量、`forbid_decoys` 收紧评分、评分器确定性测试、成本硬上限，及三轮
  正式运行的书面归因与 go/no-go；
- [`v0.1.6`](v0.1.6.md)（2026-07-12）：健康分离、最小可观测（metrics/结构化
  日志/OTLP）、审计事件 schema 定版、协议 smoke 进主 CI、overhead benchmark
  与 Agent Eval pilot（含 no-go 结论）；
- [`v0.1.5`](v0.1.5.md)（2026-07-12）：机器可读拒绝契约与 decision ID、真实
  HTTP e2e、确定性 export、六场景产品证明与证据索引；
- [`v0.1.4`](v0.1.4.md)（2026-07-12）：威胁模型、对抗 corpus、fuzz 与三库
  证据边界；
- [`v0.1.3`](v0.1.3.md)（2026-07-11）：跨平台发布、签名、SBOM、GHCR、
  quickstart 与 Registry；
- [`v0.1.2`](v0.1.2.md)（2026-07-11）：安全与资源边界强化，包含 breaking
  迁移；
- [`v0.1.1`](v0.1.1.md)（2026-07-11）：Provider 分层、嵌入式 schema 与程序化
  配置迁移；
- [`v0.1.0`](v0.1.0.md)（2026-07-11）：首个功能与测试基线的历史快照。

## 维护约定

- CHANGELOG 只维护版本摘要和 breaking 提示；
- 发布说明维护该版本的详细能力、迁移步骤和时点限制；
- 历史事实不改写为当前行为；发生后续变化时增加清晰的版本链接；
- 新版本发布后同步更新本索引中的当前 GA。
