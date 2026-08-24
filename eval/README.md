# KnowFlow evaluations

## Deterministic M5 regression baseline

`datasets/knowflow-m5.jsonl` contains 60 deterministic, offline questions in the requirements-defined JSONL shape. The evaluator seeds an isolated `demo-kb` corpus in PostgreSQL, executes the real pgvector/full-text/RRF/rerank retrieval path, and uses the fake chat model for repeatable citation, end-to-end latency, Token, and configured-cost measurement.

This suite is a regression baseline only. Its near-perfect score must not be described as real-document quality or real-provider performance.

Generate both reports with one command:

```sh
docker compose run --rm --build eval
```

`make eval` is an equivalent convenience target. Outputs are written to `eval/results/m5-comparison.json` and `eval/results/m5-comparison.md`. Re-running replaces only the deterministic evaluation knowledge base and the two report files; it does not call paid providers.

The Compose command supplies explicitly labelled illustrative per-million-token rates for a non-zero estimated-cost comparison. Override the `EVAL_*_COST_PER_MILLION_USD` variables with the active provider price table; the exact assumptions are embedded in each report.

## `real-world-v1` real-document evaluation

`real-world-v1/` contains ten actual PDF, DOCX, Markdown, and TXT documents plus 60 manually verifiable questions. The corpus includes headings, long passages, tables, similar terms, conflicting versions, cross-section/cross-document facts, distractors, conversational follow-ups, and unanswerable questions.

The evaluator never seeds chunks or vectors. Provisioning registers a dedicated user, creates a knowledge base, and uploads every source through the public HTTP API. It then waits for MinIO → Redis → Worker → real Ark Embedding → pgvector processing, verifies expected evidence through the public chunks API, and performs read-only pipeline audits.

Run provisioning and evaluation separately for an easily resumable execution:

```sh
go run ./cmd/realworldeval --phase provision --timeout 45m
go run ./cmd/realworldeval --phase evaluate --timeout 4h
```

`--phase all` performs both phases. Evaluation fails closed unless `.env` supplies real DeepSeek, real Ark Embedding, and a live VikingDB AK/SK Reranker. Fake models and rerank fallback are forbidden. It executes Dense, Sparse, Dense+Sparse+RRF, and Dense+Sparse+RRF+Reranker over all 60 questions.

Completed strategies resume from the JSON checkpoint. To intentionally rerun selected strategies after a retrieval change, pass their exact comma-separated names, for example:

```sh
go run ./cmd/realworldeval --phase evaluate \
  --force-strategies 'Sparse,Dense+Sparse+RRF,Dense+Sparse+RRF+Reranker' \
  --timeout 4h
```

Independent outputs are `results/real-world-evaluation.json` and `results/real-world-evaluation.md`; they do not replace the M5 reports. In addition to Recall@K, MRR, citation hit rate, P95 latency, tokens, and configured cost, the reports include answer accuracy, evidence support, correct refusal, hallucination, and per-format success/failure cases.
