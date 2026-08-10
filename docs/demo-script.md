# KnowFlow three-minute demo script

This script stays inside the requirements-defined release scope and uses the offline Fake models, so it needs no external credentials.

## 0:00–0:25 — launch and orient

1. Run `docker compose up -d --build` and open `http://localhost:5173`.
2. Point out the independent API and Worker plus PostgreSQL/pgvector, Redis, MinIO, and the Vue application in `docker compose ps`.
3. Show `/api/v1/health/ready` returning all three dependency checks as `ok`.

## 0:25–1:15 — ingest real source data

1. Register with a new email and password.
2. Create `产品与架构` with the default Dense + Sparse + RRF configuration and Reranker enabled.
3. Upload `demo/knowflow-demo.md`.
4. Keep the document screen visible while status and progress move through queued, parsing, chunking, embedding, and ready.
5. Open the chunk preview and show the original heading, paragraph position, token estimate, and text.

## 1:15–2:10 — grounded streaming answer

1. Open Knowledge Q&A and create a conversation.
2. Ask `KnowFlow 支持哪些文档格式？`.
3. Show the SSE answer arriving incrementally.
4. Click citation `[1]` and verify the filename, original excerpt, paragraph location, real document/chunk IDs, and retrieval score.
5. Refresh or reopen the conversation to show that the answer and citation snapshot were persisted.

## 2:10–2:45 — evaluation and governance

1. Run `docker compose run --rm --build eval`.
2. Open `eval/results/m5-comparison.md` and show 60 questions across four retrieval strategies plus Recall@K, MRR, citation hit rate, latency, Token, and cost.
3. For an admin account, open System Governance to show API-backed summary and model usage; alternatively show `/metrics`.

## 2:45–3:00 — reproducible acceptance

Run `docker compose run --rm --build smoke`. End on `SMOKE PASSED`, which independently proves registration, knowledge-base creation, upload, asynchronous indexing, cited SSE answer, and persisted citations.
