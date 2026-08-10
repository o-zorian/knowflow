# Demo data

`knowflow-demo.md` is the fixed upload used by the automated smoke acceptance flow and the three-minute demo script. The evaluator owns a separate deterministic 60-question corpus in `eval/datasets/knowflow-m5.jsonl`; running it seeds an isolated `demo-kb` evaluation namespace in PostgreSQL.

Run both release demonstrations after the Compose stack is healthy:

```sh
docker compose run --rm --build smoke
docker compose run --rm --build eval
```
