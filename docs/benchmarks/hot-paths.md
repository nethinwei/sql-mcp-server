# Hot-Path Benchmarks

用 Go benchmark 量化网关自身代码在各热路径上的开销，回归时对比基线。数据库
响应由 fake 立即返回，因此数字只包含本项目与依赖库的开销；真实往返的端到端
增量见 [data-plane-overhead.md](data-plane-overhead.md)。

## 复现

```sh
make bench               # 无需 Docker
make bench-integration   # 真实 PostgreSQL/MySQL（需要 Docker）
```

对比两次结果可用 `benchstat`（`go test -count 10` 采样后比较）。

## 覆盖的路径

| Benchmark | 包 | 测什么 |
|---|---|---|
| `BenchmarkReadToolPointLookup` / `HundredMaskedRows` | `core/tool` | 工具本身：输入解码、授权、IR、codegen、掩码 |
| `BenchmarkToolPathPointRead` / `Parallel` | `x/bootstrap` | 装配后的完整路径：预算、engine、授权、cost gate、codegen、掩码、审计 |
| `BenchmarkMCPPointRead` | `x/mcpserver` | in-memory MCP 会话的一次 `tools/call`（JSON-RPC + 上述路径） |
| `BenchmarkAssemble300Entities` | `x/bootstrap` | 300 个实体的一次启动/热重载装配 |
| `BenchmarkRuntimeAcquire` | `x/bootstrap` | 每个请求获取快照租约 |
| `BenchmarkFairEventStoreEvictSmallEvents` | `x/mcpserver` | 事件存储满（1 MiB、64 字节事件）时每次追加淘汰一个事件：分散在 1000 个会话，或集中在一个会话的 100 个流 |
| `BenchmarkReconcile` | `core/introspect` | 1000 个实体对 5000 张表对账（有无名称索引） |
| `BenchmarkBuildSchemaImport` | `x/admin/graph` | 2000 张表的导入页组装 |
| `Benchmark{PG,MySQL}Discover600Tables` | `x/providers/*`（integration） | 600 张表的元数据扫描 |
| `Benchmark{PG,MySQL}TablePrivileges600Tables` | `x/providers/*`（integration） | 600 张表的连接权限探测 |

## 基线（Apple M5，10 核）

前七项为 2026-10-20 用 Go 1.26.9（仓库固定的工具链）重测，其余为 2026-10-09（Go 1.26）。

| Benchmark | 耗时/op | 内存/op | 分配次数/op |
|---|---|---|---|
| ReadToolPointLookup | 4.7 µs | 5.9 KB | 109 |
| ReadToolHundredMaskedRows | 47 µs | 61 KB | 1078 |
| ToolPathPointRead | 26 µs | 21.7 KB | 284 |
| ToolPathPointReadParallel | 7.0 µs | 11.3 KB | 155 |
| MCPPointRead | 71 µs | 259 KB | 421 |
| Assemble300Entities | 1.9 ms | 4.84 MB | 19626 |
| RuntimeAcquire | 13 ns | 16 B | 1 |
| Reconcile（无索引 / 有索引） | 35 ms / 0.71 ms | 45 MB / 4.0 MB | 5914 / 4912 |
| BuildSchemaImport（2000 表） | 2.1 ms | 5.0 MB | 44104 |
| PGDiscover600Tables | 39 ms | — | — |
| MySQLDiscover600Tables | 14.5 ms | — | — |
| PGTablePrivileges600Tables | 1.1 ms | — | — |
| MySQLTablePrivileges600Tables | 0.58 ms | — | — |

`FairEventStoreEvictSmallEvents`（2026-10-19，同一机器，Go 1.26.9，`-benchtime=20000x -count=5`
取中位数）：v0.1.20 前每次淘汰遍历全部会话并重新累加最大会话所有流的字节数，分散 / 集中两种
形状分别为 7.6 µs / 7.0 µs；改为按字节数维护的两级大顶堆后为 0.31 µs / 0.21 µs，淘汰代价
O(log n)，与会话、流和事件的数量无关（余下开销主要是基准自身生成键的分配）。
同日把流条目与每个事件的固定开销计入上限（空事件和长会话的流条目不再无界）后，基准按 SDK 的
方式先 `Open` 再追加，Go 1.27.2 下为 0.32 µs / 0.16 µs。

读取路径比 2026-10-09 多出的分配主要来自结果计量：每行先 `json.Marshal` 一次计字节数
（`core/tool/results.go`，约 6 次分配/行），传输层再编码一次。2026-10-20 把脱敏规则表改为每条
语句算一次、扫描缓冲改为每次查询复用一份后，`ReadToolHundredMaskedRows` 从 1276 降到 1078 次分配；
计量与输出共用一次编码要改动传输层，相对数据库往返收益有限，暂不做。

批量扫描前（逐表查询）同一 600 张表在本机零网络延迟下：PostgreSQL 0.98 s、
MySQL 1.32 s，且随表数与网络往返线性增长。

## 结论与已知热点

- **e2e 慢不是代码慢**：单个 e2e 测试中启动 PostgreSQL 容器占 95% 以上
  （1.3–1.9 s），装配约 30 ms，单次 MCP 调用 0.3–4 ms。e2e 套件改为共享一个
  容器、每个测试一个新数据库后，`make test-e2e` 从约 12 s 降到约 4 s。
- **engine 的 goroutine 交接**：`ToolPathPointRead`（26 µs）比工具本身
  （4.7 µs）多出的部分，CPU profile 显示约 70% 在 goroutine 唤醒
  （`singleflight` 的 leader 在独立 goroutine 中执行，以便调用方取消时
  不影响其他等待者）。并发下摊薄到 7.0 µs/op；相对毫秒级的真实数据库往返
  可以忽略，暂不改变该取消语义。
- **MCP SDK 的 JSON 解码分配**：`MCPPointRead` 每次约 259 KB 中约 91% 来自
  MCP SDK 内部 `segmentio/encoding` 解码器的缓冲分配（客户端与服务端各一
  半），本项目代码约占 4%。属于上游依赖，记录待跟进。
