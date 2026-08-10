# KnowFlow M5 evaluation

`datasets/knowflow-m5.jsonl` contains 60 deterministic, offline questions in the requirements-defined JSONL shape. The evaluator seeds an isolated `demo-kb` corpus in PostgreSQL, executes the real pgvector/full-text/RRF/rerank retrieval path, and uses the fake chat model for repeatable citation, end-to-end latency, Token, and configured-cost measurement.

Generate both reports with one command:

```sh
docker compose run --rm --build eval
```

`make eval` is an equivalent convenience target. Outputs are written to `eval/results/m5-comparison.json` and `eval/results/m5-comparison.md`. Re-running replaces only the deterministic evaluation knowledge base and the two report files; it does not call paid providers.

The Compose command supplies explicitly labelled illustrative per-million-token rates for a non-zero estimated-cost comparison. Override the `EVAL_*_COST_PER_MILLION_USD` variables with the active provider price table; the exact assumptions are embedded in each report.
