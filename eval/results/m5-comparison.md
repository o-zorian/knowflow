# KnowFlow M5 Retrieval Evaluation

Generated: `2026-08-10T10:38:23Z`

Dataset: `/app/eval/datasets/knowflow-m5.jsonl`

Questions: **60**

Configured illustrative pricing (USD / 1M tokens): chat input 0.1500, chat output 0.6000, embedding 0.0200, rerank input 0.0500.

## Experiment configurations

| Configuration | Dense K | Sparse K | RRF K | Rerank | Rerank K | Final K |
|---|---:|---:|---:|---:|---:|---:|
| Dense only | 20 | 0 | 60 | false | 10 | 10 |
| Sparse only | 0 | 20 | 60 | false | 10 | 10 |
| Dense + Sparse + RRF | 20 | 20 | 60 | false | 10 | 10 |
| Dense + Sparse + RRF + Reranker | 20 | 20 | 60 | true | 10 | 10 |

## Metrics

| Configuration | Recall@1 | Recall@5 | Recall@10 | MRR | Citation hit | Retrieval avg / P95 ms | E2E avg / P95 ms | Avg tokens | Avg cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Dense only | 0.9833 | 1.0000 | 1.0000 | 0.9889 | 0.9833 | 0.975 / 1.333 | 0.993 / 1.348 | 38.75 | 0.00001509 |
| Sparse only | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 2.141 / 3.240 | 2.161 / 3.256 | 35.17 | 0.00001507 |
| Dense + Sparse + RRF | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 2.147 / 2.763 | 2.166 / 2.808 | 38.83 | 0.00001514 |
| Dense + Sparse + RRF + Reranker | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 1.976 / 2.509 | 1.997 / 2.529 | 230.65 | 0.00002473 |

## Failure cases

### Dense only

| ID | First relevant rank | Citation hit | Error |
|---|---:|---:|---|
| q-055 | 3 | false |  |

### Sparse only

No Recall@10 or citation failures.

### Dense + Sparse + RRF

No Recall@10 or citation failures.

### Dense + Sparse + RRF + Reranker

No Recall@10 or citation failures.

## Conclusions and improvements

- Highest Recall@10/MRR combination: Sparse only (1.0000 / 1.0000).
- Dense and sparse Recall@10 are tied; inspect MRR and failure cases before choosing a default.
- Reranking did not reduce MRR relative to RRF on this run.
- Review the JSON per-question cases for rank regressions; expand hard negatives and multi-hop questions before using the numbers as a production quality claim.
- Latency and cost use the local fake models and configured price table; repeat with explicitly enabled production providers for provider-specific capacity planning.
