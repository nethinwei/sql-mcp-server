# Data-Plane Overhead Benchmark

测量同一查询经过完整治理路径（MCP 协议 + RBAC + cost gate + engine + 掩码 +
序列化）相对直连数据库的增量延迟，报告 p50/p95/p99。

## 方法

- **fixture**：PostgreSQL 16（testcontainers `postgres:16-alpine`），
  `app_user(id, email, tenant_id)` 共 10000 行，由 `generate_series`
  确定性生成（fixture 是行数的纯函数），插入后执行 `ANALYZE`；
- **查询形状**：主键点查 `id = ?`，key 在 1–1000 间循环，两条路径命中同一
  热集；
- **对照组（directQuery）**：经同一 provider 连接池直接执行
  `SELECT id, email, tenant_id FROM public.app_user WHERE id = $1` 并消费
  全部行；
- **实验组（toolPath）**：in-memory MCP client 调用 `read_records`
  （配置见 `internal/benchoverhead/config.yaml`：治理全开、cache 关闭、
  audit 关闭）；
- **采样**：200 次 warmup 后采样 2000 次迭代（`BENCH_ROWS`、
  `BENCH_ITERATIONS` 环境变量可覆盖），报告每条路径的 p50/p95/p99 与逐
  分位差值（overhead = toolPath − directQuery）。

## 复现

```sh
make bench-overhead        # 需要 Docker
```

输出为 JSON（含 Go 版本、OS/arch、CPU 数、fixture 行数与迭代数）。

## 示例运行

以下为开发环境单次运行样例，**不构成官方基线**；正式基线须在固定专用环境
中多次运行并报告分布（见 [Roadmap Metrics](../roadmap/metrics.md) 公开数字
规则）。

| 环境 | 值 |
| --- | --- |
| 版本 | dev（v0.1.6 开发分支） |
| Go | go1.26.5 |
| OS/Arch | linux/amd64（WSL2） |
| CPU | 24 逻辑核 |
| fixture | 10000 行 / 2000 次迭代 |

| 分位 | directQuery | toolPath | overhead |
| --- | --- | --- | --- |
| p50 | 157 µs | 340 µs | +183 µs |
| p95 | 225 µs | 674 µs | +449 µs |
| p99 | 289 µs | 833 µs | +544 µs |

## 已知限制

- 单次运行、共享开发机（WSL2），存在调度噪声；发布数字前必须固定环境并
  报告多次运行分布；
- in-memory MCP transport 不含网络与 HTTP 序列化开销，测得的是治理层
  下界；生产 HTTP 部署的协议开销另计；
- 仅覆盖主键点查（治理路径的最低成本形状）；范围查询、聚合与掩码密集
  负载的 overhead 需要扩展查询形状后另行测量；
- 该 benchmark 不进入 PR CI（依赖 Docker 且对噪声敏感），按需手动运行。
