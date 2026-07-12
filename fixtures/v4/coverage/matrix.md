# Eval coverage matrix

## Expected behaviors

- answer: 29
- clarify: 3
- deny: 11
- qualify: 3
- unsupported: 2

## Prompt levels

- ambiguous: 3
- guided: 23
- natural: 22

## Semantic challenges

| Challenge | Positive | Clarify | Deny | Natural | Guided |
| --- | ---: | ---: | ---: | ---: | ---: |
| async_consistency | 7 | 0 | 0 | 4 | 3 |
| grain | 14 | 1 | 0 | 6 | 9 |
| minor_units | 1 | 0 | 0 | 0 | 1 |
| relation_selection | 1 | 0 | 0 | 0 | 1 |
| snapshot_vs_flow | 2 | 1 | 0 | 2 | 1 |
| soft_delete | 1 | 0 | 0 | 0 | 1 |
| status_semantics | 12 | 2 | 0 | 5 | 9 |
| time_semantics | 14 | 2 | 0 | 7 | 9 |
| unit_semantics | 6 | 0 | 1 | 3 | 4 |
| users_vs_customers | 1 | 0 | 0 | 0 | 1 |
| version_semantics | 6 | 0 | 1 | 2 | 5 |

## Governance challenges

| Challenge | Positive | Clarify | Deny | Natural | Guided |
| --- | ---: | ---: | ---: | ---: | ---: |
| field_acl | 0 | 0 | 5 | 3 | 2 |
| inference_risk | 1 | 0 | 3 | 4 | 0 |
| masking | 0 | 0 | 3 | 3 | 0 |
| tenant_isolation | 0 | 0 | 3 | 3 | 0 |
