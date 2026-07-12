# Eval suites

渐进迁移的 suite 索引（`v0.1.10`）：

| Suite | 路径 | 命令 |
| --- | --- | --- |
| regression | `eval/regression/` | `make eval-pilot` |
| workload (v4 guided) | `fixtures/v4/tasks/` | `make eval-workload` |
| diagnostic (v5) | `fixtures/v4/tasks-v5/` | `make eval-diagnostic` |
| governance | `fixtures/v4/tasks-v5/additions.yaml`（gov.* 前缀） | `make eval-diagnostic` |
| investigation (P1) | `fixtures/v4/scenarios/` | 多轮 runner 待接入 |
| discovery (P1) | `fixtures/v4/catalog/medium/` | 草案 |
| cross-role (P1) | `fixtures/v4/tasks-v5/cross-role-draft.yaml` | 草案，不进入默认轨 |

覆盖矩阵：`make eval-coverage` → `fixtures/v4/coverage/matrix.json`。
