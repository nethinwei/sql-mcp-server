# 发布说明索引

当前 GA 为 `v0.1.4`（[发布说明](v0.1.4.md)、
[GitHub Release](https://github.com/nethinwei/sql-mcp-server/releases/tag/v0.1.4)）。

本目录中的发布说明描述各版本发布时点的能力、迁移和证据状态。当前运行时行为以
[配置参考](../configuration.md)、[安全模型](../security.md)和
[Provider 兼容矩阵](../provider-compatibility.md)为准；全版本摘要见
[CHANGELOG](../../CHANGELOG.md)。

## 版本

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
