# Roadmap Metrics

返回[主路线图](../roadmap.md)。

这些指标用于决定优先级、验证产品声明和判断阶段是否升级，不替代具体版本的退出
标准。尚未建立 baseline 的指标是测量目标，不是当前能力声明。

## 技术可信度

- 所有 critical/high threat 均有测试证据或明确剩余风险；
- 所有权限和成本拒绝具有稳定机器码与可关联的 `decision ID`；
- 所有 Provider 保证均标注证据层级和保证强度；
- 公开 benchmark 可复现，并报告 p50/p95/p99 增量延迟；
- schema drift、reload、预算、审计和依赖故障具有可检测语义；
- 安全限制默认 fail closed。

## Agent 与业务效果

- entity/tool selection；
- argument validity；
- first-call success 和 final task success；
- repair rate 和 tool-call count；
- schema、context 和 result token；
- semantic correctness；
- policy violation attempt rate 和 violation success rate。

Agent Eval 分为两级：

- **Pilot**：20–30 个固定任务，用于尽早校正 roadmap，不承担竞品排名；
- **Public suite**：版本化任务集、多客户端与多模型环境、固定评分规则和可复现
  baseline，用于支持公开方案对照。

任何竞品或方案对比必须使用相同任务、fixture、环境和评分方法。非确定性的在线模型
结果应报告多次运行分布，不以单次结果作为每次 CI 的硬门禁。

## 采用与生态

- 五分钟 Demo 完成率；
- Registry 和客户端集成可用性；
- 活跃生产部署；
- 公开 case study；
- 外部 issue 和持续 contributor；
- 外部维护 Provider；
- 稳定 release cadence 和升级成功率。

star 数只作为传播信号，不作为产品质量代理。

## 可信供应链

- signed release、checksum、SBOM 和 provenance 覆盖；
- 持续 fuzzing 时长与 crash 处理；
- 漏洞确认、修复与披露 SLA；
- 外部安全审计状态。

当前短时 fuzz smoke 不得表述为长期持续 fuzz，未完成的外部审计或 provenance
门禁不得表述为当前保证。

## Graduation Targets

长期成熟度目标：

- 至少 10 个可验证生产部署；
- 3 个公开 case study；
- 5 个持续外部 contributor；
- 2 个外部维护 Provider。

这些目标不承诺由某个版本单独达成，也不能替代安全、性能和 Agent Eval 证据。

## 公开数字规则

任何公开数字必须同时附带：

- 数据集或任务集版本；
- fixture、模型、客户端和运行环境；
- 评分规则；
- 重复次数与结果分布；
- 可执行的复现方法；
- 已知限制和不适用范围。
