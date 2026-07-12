# 诊断轨框架交付说明（v5）

`v0.1.10` Diagnostic Evaluation 基础设施已落地（任务 schema v5、诊断轨
runner、覆盖矩阵、48 项正式单轮任务）。**正式三轮模型评测**需配置
`EVAL_API_KEY` / `EVAL_MODEL` 后运行 `make eval-diagnostic`（与 v4 相同
依赖 Docker + 在线模型）。

## 已交付

- 设计文档：[docs/design/diagnostic-evaluation.md](../../docs/design/diagnostic-evaluation.md)
- 任务集：21 guided + 27 正式扩展（P1 草案不进入默认轨）
- 命令：`make eval-diagnostic`、`make eval-coverage`
- 覆盖矩阵：`fixtures/v4/coverage/matrix.md`（48 tasks）
- P1 草案：`fixtures/v4/scenarios/`（3 调查场景）、`catalog/medium/`、
  `fixtures/v4/tasks-v5/cross-role-draft.yaml`

## 正式运行清单（待执行）

```sh
export EVAL_API_KEY=...
export EVAL_MODEL=...
make eval-diagnostic > eval/results/<date>-<model>-diagnostic-v5-run1.json
# 重复 3 次；按 eval/results/diagnostic-v5-template.md 撰写结论
```

## 预期与 v4 的差异

- Natural/Ambiguous 任务不泄露查询路径，预期区分度高于 v4 Guided 21/21；
- clarify/deny/qualify/unsupported 任务测量行为而不仅是最终数字；
- counterfactual oracle（≥8 任务）在答错时可机械归因 grain/time 等；
- 需使用更弱模型或 dogfooding 负载方能为 Dormant 方向产出新 go/no-go。

## 框架自检（无需模型）

- `go test ./eval/runner/...` — loader、oracle、grading 单元测试
- `make eval-coverage` — 覆盖矩阵生成
- `make eval-pilot` / workload integration — 回归与差分无改动
