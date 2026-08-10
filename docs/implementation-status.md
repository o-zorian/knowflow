# KnowFlow implementation status

Last updated: 2026-08-10

## Accepted milestones

### M0 — engineering foundation

Versioned PostgreSQL/pgvector migrations, Redis, MinIO, configuration validation, JSON logging/request IDs, dependency-aware health checks, independent API/Worker processes, Compose, and graceful shutdown are complete.

### M1 — users, knowledge bases, and documents

Registration/login/refresh/logout, password/JWT handling, owner isolation, knowledge-base CRUD, validated MinIO upload with SHA-256 duplicate detection, document/job status, retry, chunk-preview gating, and deletion queueing are complete. The original M1 changes remain part of the current working tree.

### M2 — asynchronous indexing

Status: **accepted**.

Completed scope:

- Redis queue consumption in the independent Worker with per-job timeout and structured `job_id` logging.
- TXT, Markdown, PDF, and DOCX parsing; whitespace/control-character cleaning; explicit empty-document and parse failures.
- Heading, paragraph, table, and PDF page metadata preservation.
- Recursive character chunking using per-knowledge-base `chunk_size` and `chunk_overlap`, with content hashes and token estimates.
- Replaceable `Embedder` interface plus deterministic offline 1024-dimensional `FakeEmbedder`; configurable batch size.
- Stage and progress transitions across parsing, chunking, embedding, and completion.
- PostgreSQL/pgvector persistence with all chunks, document `ready`, chunk count, and job success committed atomically.
- Manual failed-job retry with attempts counted on Worker claim.
- Idempotency through pending-only transactional claims, unique job keys, unique chunk positions, and duplicate-message no-ops.
- Unit coverage for four parsers, cleanup, empty documents, chunk boundaries/overlap/metadata, retry classification, fake embeddings, processor stages, and duplicate claims.
- PostgreSQL integration coverage for ready transition, duplicate message replay, failure/retry, and no duplicate chunks.
- Docker `scratch` image now contains a mode-1777 `/tmp`, fixing the M1 upload/M2 PDF parsing blocker.

## M2 verification

Executed from the repository root on 2026-08-10:

- Baseline before M2: `go test ./...`, `go vet ./...`, and `go build ./...` passed after pointing Go caches at ignored workspace paths.
- `go test ./...`: passed; integration suites skip unless `KNOWFLOW_TEST_DATABASE_URL` is set.
- PostgreSQL integration: `go test ./internal/ingestion ./internal/transport/http -run 'TestM2|TestM1HTTPIntegration' -count=1 -v` passed against Compose PostgreSQL/pgvector.
- Compose build/start: passed; PostgreSQL, Redis, MinIO, and API became healthy and the Worker started.
- Real API → MinIO → Redis → Worker → pgvector test: a Markdown document moved from `queued` to `ready`, job reached `succeeded/completed/100`, and 9 chunks were persisted.
- The same Redis job payload was replayed. Before and after values were both `attempts=1`, `chunk_count=9`, and actual chunk rows `=9`.

## M2 boundary and risks at acceptance

- M2 PDF support requires a text layer and does not perform OCR. DOCX indexing excludes headers, footers, comments, and embedded media.
- M2 originally used the fake embedder by design. M3 retains a deterministic lexical fake for offline tests and adds the OpenAI-compatible production adapter.
- The Redis list consumer provides duplicate-safe processing but does not yet implement a separate in-flight/acknowledgement list for automatic crash recovery. A process crash after dequeue can leave a job `running`; operator recovery is a remaining reliability hardening item outside the M2 acceptance path.

### M3 — RAG question answering

Status: **accepted**.

Completed scope:

- Owner- and knowledge-base-scoped pgvector cosine retrieval over ready documents and their current index versions, using the configured dense Top K.
- Replaceable `ChatModel` streaming interface, deterministic offline `FakeChatModel`, and OpenAI-compatible chat streaming adapter.
- Matching fake or OpenAI-compatible query/document embedding selection in API and Worker.
- Conversation create/list/detail/delete APIs and persisted multi-turn user/assistant messages.
- JSON SSE events for start, retrieval completion, answer deltas, citations, usage, completion, and errors.
- Stable evidence numbering, streaming validation/removal of nonexistent numeric citation markers, and citation snapshots mapped to real document/chunk rows with location and similarity.
- Completed answer/usage/retrieval persistence, readable failed-message state on model failure, and documented client-disconnect cancellation behavior.
- Unit tests for citation filtering, fake streaming, and OpenAI-compatible adapters plus a PostgreSQL integration test covering HTTP upload, indexing, dense retrieval, SSE, real citations, and failure persistence.

M3 verification on 2026-08-10:

- `go test ./... -count=1`: passed with PostgreSQL integration enabled.
- `go vet ./...`: passed.
- `go build ./...`: passed.
- `go test ./internal/ingestion ./internal/transport/http -run 'TestM1HTTPIntegration|TestM2|TestM3' -count=1 -v`: passed against the running PostgreSQL/pgvector service. The M3 test uploaded a TXT document, processed it into a real chunk/vector row, retrieved it by dense similarity, observed all successful SSE event types, verified the emitted document/chunk IDs, and reloaded the completed answer/citation from the database. The same test verified owner isolation and persisted `failed` state on a fake model error.
- That historical Docker limitation is obsolete. The M5 verification re-ran Compose successfully with Docker CLI and Docker Engine available.

Current boundary: the M3 behavior is retained and now governed by the M5 usage, retry, pricing, metrics, and evaluation layers.

### M4 — retrieval enhancement

Status: **accepted**.

Completed scope:

- PostgreSQL `tsvector` sparse retrieval with a GIN-indexed first-release tokenizer for ASCII terms and common Han unigram/bigram terms.
- Per-knowledge-base Dense-only, Sparse-only, Dense + Sparse + RRF, and Dense + Sparse + RRF + Reranker configurations; a zero source Top K disables that source.
- Concurrent dense/sparse source execution, RRF score fusion, stable chunk de-duplication, configurable score filtering and final Top K.
- Replaceable `Reranker` interface, deterministic offline fake, HTTP `/rerank` adapter, response validation, and transparent RRF fallback on any reranker error.
- Conversation-aware standalone-query rewriting from the latest six completed messages, with original-query fallback.
- Retrieval traces containing rewrite, strategy, source-count, component-score, and fallback information; citations now persist the final strategy score.
- Unit coverage for RRF/de-duplication, all four configurations, reranker adapter/fallback, query rewriting/fallback, and retrieval configuration validation.
- PostgreSQL integration coverage that executes all four configurations through real pgvector and `tsvector` queries and verifies reranker fallback without a paid model.

M4 verification on 2026-08-10:

- M3 baseline before M4: `go test ./... -count=1`, `go vet ./...`, and `go build ./...` passed; opt-in database tests were skipped because the variable was not initially set.
- PostgreSQL M4 integration: `go test ./internal/retrieval -run TestM4PostgresFourConfigurationsAndRerankFallback -count=1 -v` passed all four named subtests plus the fallback assertion.
- M1–M4 database regression: `go test ./internal/ingestion ./internal/retrieval ./internal/transport/http -run 'TestM1HTTPIntegration|TestM2|TestM3|TestM4' -count=1 -v` passed against local PostgreSQL/pgvector.

M4 boundary and risks:

- The first-release sparse tokenizer and `plainto_tsquery` tradeoffs are documented in README; it is not production-grade Chinese linguistic segmentation.
- The rerank HTTP contract follows the commonly deployed `/rerank` shape because OpenAI Chat/Embedding compatibility does not define a universal rerank wire protocol.
- These previously deferred items are implemented in M5 below.

### M5 — evaluation and governance

Status: **implemented and verified**.

Completed scope:

- A validated 60-question JSONL dataset and isolated deterministic PostgreSQL seed corpus.
- A one-command evaluator for Dense only, Sparse only, Dense + Sparse + RRF, and Dense + Sparse + RRF + Reranker.
- JSON and Markdown output with configuration, Recall@1/5/10, MRR, citation hit rate, average/P95 retrieval and end-to-end latency, average Token/configured cost, per-question cases, failures, and improvement conclusions.
- Per-call chat, embedding, and rerank usage persistence with model, scope, tokens/texts, configurable cost, latency, status, request ID, trace ID, and error code; assistant messages also persist cost.
- Bounded exponential retry for model-provider network, 429, and 5xx failures.
- Redis IP, authenticated-user, and failed-login rate limits.
- Prometheus HTTP, ingestion, queue, model, embedding, and retrieval metrics.
- Admin summary, failed-job, model-usage, user-list/status APIs and an API-backed `/admin` page; disabling a user revokes active refresh tokens.

M5 verification uses the live Docker Engine. `docker compose run --rm --build eval` evaluated 60 questions across all four strategies and generated `eval/results/m5-comparison.json` and `.md`. Full unit, integration, vet, build, Compose, API, metrics, and admin endpoint results are recorded in the final M5 handoff.

### M6 — release preparation

Status: **implemented and release-verified**.

Completed scope:

- A responsive Vue 3 product UI backed only by real APIs: registration/login, knowledge-base creation and retrieval configuration, multi-format upload, live indexing progress, retry, chunk preview, persisted conversations, JSON SSE rendering, clickable original citations, and administrator governance metrics.
- A small independent Go Web process that serves committed Vite release assets and runtime API configuration, avoiding a Node runtime in the production stack while CI verifies that assets match source.
- A fixed demonstration document and cross-platform `smoke` command covering unique registration, knowledge-base creation, upload, asynchronous Worker indexing, every required successful SSE event, a real citation, and persisted-answer reload.
- Release README with background, screenshots, architecture, retrieval flow, fresh-environment startup, configuration, evaluation results, operation, testing, and limitations.
- Detailed Mermaid architecture/sequence diagrams, OpenAPI 3.1 contract, four real product screenshots, and a three-minute demonstration/recording script.
- GitHub Actions jobs for Go format/vet/offline test/build, Vue lint/test/build, serialized pgvector integration, Compose validation, and all production/tool image builds.

M6 verification on 2026-08-10:

- `go test ./cmd/... ./internal/... ./migrations -count=1`, `go vet ...`, and `go build ...`: passed.
- `npm ci`, `npm run lint`, `npm test`, and `npm run build`: passed with zero audit vulnerabilities; the SSE parser and Vue release build were exercised.
- OpenAPI 3.1 validation with Redocly: passed without structural errors.
- A separate `knowflow-m6` Compose project used new PostgreSQL, Redis, and MinIO volumes plus isolated host ports. PostgreSQL/pgvector, Redis, MinIO, API, Worker, and Web became healthy without touching the existing development stack.
- Migrations ran twice and both executions reported current. The serialized ingestion/retrieval/HTTP integration suites passed against the fresh pgvector database, including four parser formats, ownership isolation, ready/failure/retry/idempotency, four retrieval strategies/rerank fallback, SSE persistence/citations, model failure, admin usage, and disable/revoke behavior.
- `docker compose run --rm --build smoke` ended with `SMOKE PASSED`; the uploaded Markdown reached `ready`, streamed every successful event with a real source citation, and reloaded the saved answer.
- `docker compose run --rm --build eval` evaluated 60 questions across all four configurations and regenerated JSON/Markdown reports.
- Headless Chrome loaded the deployed Web runtime configuration, fetched actual API data under CORS, and captured the committed release screenshots.

Remaining release boundaries are documented in README. No external credential is required for development, automated tests, the smoke demonstration, or the deterministic evaluation.
