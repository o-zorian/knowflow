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

## Current boundary and risks

- M3 and later functionality is intentionally absent: dense retrieval, chat/SSE/citations, hybrid retrieval/reranking, evaluation/governance, and frontend work remain future milestones.
- M2 PDF support requires a text layer and does not perform OCR. DOCX indexing excludes headers, footers, comments, and embedded media.
- M2 uses the fake embedder by design; it validates batching and pgvector storage but does not provide semantic vectors.
- The Redis list consumer provides duplicate-safe processing but does not yet implement a separate in-flight/acknowledgement list for automatic crash recovery. A process crash after dequeue can leave a job `running`; operator recovery is a remaining reliability hardening item outside the M2 acceptance path.
