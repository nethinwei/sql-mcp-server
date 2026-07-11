# v0.1.5 — Contract + Product Proof

状态：**Committed**

返回[主路线图](../roadmap.md)。

## 目标

固定数据面的机器契约和真实 HTTP 路径，同时把现有安全、发布和 quickstart 能力
组合成一个可在五分钟内复现的产品证明。

## 范围

### Operate

- 定义稳定、机器可读的拒绝契约，包含 `code`、`reason`、`retryable`、
  `constraints`、`hints` 和 `decision ID`；
- 让 `decision ID` 关联 MCP 响应、审计事件与 trace；
- 为真实 streamable HTTP `/mcp` 建立 e2e，覆盖认证、subject/tenant、
  allow/deny、mask、row policy、成本拒绝和事务；
- 定义 MCP tool schema、机器可读错误和配置导出格式的兼容规则；
- 提供确定性 YAML `export`，固定默认值、字段顺序和 secret 表示；
- 完成 revision/snapshot 数据模型、兼容性和失败语义设计评审，但不实现持久化。

### Adopt

- 将现有 PostgreSQL Compose quickstart 收敛为单一五分钟演示路径；
- 自动或文档化验证六个场景：正常查询、字段拒绝、跨 tenant 拒绝、mask 可返回但
  不可过滤、执行前成本拒绝、Agent 根据结构化错误收窄请求后成功；
- 为 MCP Inspector、Claude Desktop、Cursor 和 VS Code 提供经过核对的入口；
- 发布一页架构与安全边界说明，并将签名、SBOM、Registry 和 Provider 兼容矩阵
  汇总为可验证的证据索引。

### Prove

- 在对抗测试中建立 critical/high threat ID 到回归测试的可追溯映射；
- 公开当前 fuzz smoke 的真实运行范围，不将其表述为长期持续 fuzz；
- 所有对外安全声明链接到测试、兼容矩阵或明确的剩余风险。

## 退出标准

- 拒绝响应可稳定解析全部约定字段；修复建议只能收紧或等价改写请求；
- HTTP e2e 与 PostgreSQL MCP e2e 的关键授权路径一致；
- tool contract 的兼容与破坏性变化具有机器可检查的版本规则；
- 同一有效配置重复 export 得到确定性、可审阅的结果；
- snapshot 的拒绝语义及在途请求一致性形成评审通过的设计结论；
- 新用户按文档可在五分钟演示路径中完成六个场景；
- 每个 critical/high threat 均能追溯到现有测试或明确标注的证据缺口。

## 非目标

- 持久化 revision、draft/publish/rollback、token revocation 或管理 API；
- 完整 Agent Eval、可执行语义层或动态 catalog search；
- 新 Provider、管理 UI、完整可观测性平台或 durable audit。
