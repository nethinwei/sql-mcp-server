# Changelog

本项目的重要变更记录于此。格式参考
[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本遵循
[Semantic Versioning](https://semver.org/lang/zh-CN/)。

## Unreleased

## 0.1.1 - 2026-07-11

### Changed

- 将 PostgreSQL、MySQL、OceanBase 方言实现从核心 `dialect` 包移至
  `x/providers/*`，核心仅保留 `Dialect` 接口与 `Capabilities` 声明。
- 配置 JSON Schema 与 MCP 工具 input schema 改为 `embed` 静态 JSON 文件，不再
  硬编码在 Go 源码中。
- 重组文档，使配置、安全边界、运行和测试分别拥有单一真相源。
- 扩充用于 YAML 编辑辅助的配置 JSON Schema，并统一配置字段的 lowerCamelCase
  名称；Schema 不作为标准 `encoding/json` 输入契约。
- Go 源码中的 `config.CostConfig.Enabled` 从 `bool` 改为 `*bool`，以区分“省略”
  与显式 `false`，从而保持默认开启的安全三态。程序化构造配置时请将
  `Enabled: true/false` 迁移为 `Enabled: config.Bool(true/false)`，读取有效值
  请使用 `EnabledOrDefault()`。
- 多数据源配置中的精确 SQL baseline 必须写成 `datasource:SQL`；裸 SQL 仅在
  单数据源配置中为兼容旧配置而继续接受。`fp:v2:` fingerprint 已包含数据源，
  不受影响。
- 热重载会原子更新预算限制并保留 session 用量；事务 `ttl`/`maxOpen` 变化因
  无法安全迁移在途事务而拒绝 reload，需重启生效。

## 0.1.0 - 2026-07-11

### Added

- PostgreSQL、MySQL、OceanBase provider。
- stdio/streamable HTTP MCP 和 HTTP token、TLS/mTLS。
- 实体 CRUD、procedure、aggregate 与显式事务工具。
- RBAC、字段 ACL、行级策略、mask、审计和成本控制。
- 多数据源、关系展开、分页、prepared cache、预算与热重载。
- 授权 schema resource、安全 prompts、CLI 和分层测试。

完整能力与限制见 [`docs/releases/v0.1.0.md`](docs/releases/v0.1.0.md)。
