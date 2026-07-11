# Roadmap

当前 `v0.1.0` 范围已完成。已实现能力、默认值和不完整边界分别以
[v0.1.0 发布说明](releases/v0.1.0.md)、[配置参考](configuration.md) 和
[安全模型](security.md) 为准；本文件只记录未来方向。

## v0.2.0

- 让 provider 的原生 statement timeout/scan-row cap/resource manager 从
  capability 描述变为可验证的连接级装配。
- 支持重载后安全刷新 MCP 工具发现集合，保持在途请求和 session 一致性。
- 补充 OAuth/OIDC 身份映射、明确 CORS 策略，以及可替换的持久 session/event
  store。
- 为 budget 的扫描行限制增加 provider/数据库侧可验证的硬上限，并定义分布式/
  多实例配额语义。
- 提供稳定的 template fingerprint 发现与审核工作流，避免手工计算或配置精确
  SQL。
- 补齐可直接部署的 metrics exporter/endpoint 和审计轮转/外部 sink。
- 评估将 YAML presence 解码移出 core config，消除 depguard 中精确记录的
  `yaml.v3` 例外。
- 增加 MCP 官方 conformance、强化并测试现有配置 schema 契约，以及跨 provider
  行为一致性测试；不是首次建立配置契约。

## P2+

- 通用 join/union 和跨数据源查询规划；在此之前关系展开保持同源、单 join
  pair。
- 物化视图、bucketing/broadcast 等明确有使用场景后的查询策略。
- Docker/Helm/operator 与生产部署基线。
- 配置多文件 merge/include 和 secret provider 插件。
- OpenAPI/GraphQL 等基于同一实体与授权模型的额外协议面。
- 文档站、交互式 playground 和可复现 benchmark。
- 基于性能数据评估 PGO、`weak.Pointer`、`unique.Make`；当前不预设采用。
