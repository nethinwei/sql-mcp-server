# SQL MCP Server

> **The governed SQL gateway for untrusted AI agents.**

面向不可信 AI Agent 的受控 SQL 数据访问网关。通过显式 Entity、关系代数 IR 和
参数化 codegen 访问 PostgreSQL、MySQL 与 OceanBase，**不接受任意 SQL，不提供
DDL**。

## 核心差异

- **确定性执行**：Agent 只能组合受控工具和白名单 IR；
- **字段与行治理**：RBAC、字段 ACL、row policy 和 mask 统一强制；
- **成本可控**：EXPLAIN 预筛、结果上限、超时和 session/tenant 预算；
- **默认拒绝**：安全能力无法证明时 fail closed；
- **可审计**：配置热重载、异步审计和 OpenTelemetry hook。

运行时安全行为以[安全模型](docs/security.md)为准；Provider 验证边界与证据层级
以[兼容矩阵](docs/provider-compatibility.md)为准；威胁、控制与剩余风险见
[威胁模型](docs/threat-model.md)。所有对外声明的证据汇总见
[架构、安全边界与证据索引](docs/evidence.md)。**不以本页摘要为准。**

## 五分钟体验

只需 Docker 与 Docker Compose：

```sh
docker compose -f examples/quickstart/compose.yaml up -d --wait
curl -fsS http://127.0.0.1:8080/healthz
```

完整的 MCP Inspector 调用、tenant 隔离、mask 和拒绝场景见
[五分钟快速体验](docs/quickstart.md)。

## 安装与接入

源码构建要求 Go 1.25.12+ 和一个[已验证数据库版本](docs/supported-versions.md)：

```sh
git clone https://github.com/nethinwei/sql-mcp-server.git
cd sql-mcp-server
make build
```

- Cursor、Claude Desktop 和 VS Code 的 stdio 模板：
  [`examples/clients/`](examples/clients/)（核对结论见
  [客户端接入核对](docs/clients.md)）；
- 完整配置模板：[`examples/config.example.yaml`](examples/config.example.yaml)；
- CLI、启动、热重载和升级：[运行与运维](docs/operations.md)；
- 魔搭分发展示：[ModelScope 上架与使用](docs/modelscope.md)。

## 按角色阅读

### 首次体验与集成

- [五分钟快速体验](docs/quickstart.md)：唯一完整 Demo 与 Inspector 示例；
- [配置参考](docs/configuration.md)：公开 YAML 配置的唯一事实源；
- [Provider 兼容性](docs/provider-compatibility.md)与
  [支持版本](docs/supported-versions.md)：当前能力和验证边界。

### 部署与安全

- [运行与运维](docs/operations.md)：CLI、生命周期、监控和升级；
- [安全模型](docs/security.md)：运行时安全行为的唯一事实源；
- [威胁模型](docs/threat-model.md)：威胁、控制、测试证据与剩余风险；
- [SECURITY.md](SECURITY.md)：漏洞披露流程。

### 开发与维护

- [架构](docs/architecture.md)与[不变量](docs/invariants.md)；
- [测试与 CI](docs/testing.md)：测试命令、CI、fuzz 和发布前门禁；
- [贡献指南](CONTRIBUTING.md)。

### 版本与规划

- 当前 GA：`v0.1.5`（[发布说明](docs/releases/v0.1.5.md)）；
- 全版本摘要：[CHANGELOG](CHANGELOG.md)；历史能力与迁移：
  [发布说明索引](docs/releases/README.md)；
- 未发布产品规划：[Roadmap](docs/roadmap.md)；
- 数据库候选：[Provider Roadmap](docs/provider-roadmap.md)。

## 许可

[MIT](LICENSE)
