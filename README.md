# KnowFlow

KnowFlow is a Go-based enterprise knowledge-base RAG platform. The repository currently implements M0 through M2: the engineering foundation, owner-scoped authentication/knowledge-base/document APIs, and asynchronous document indexing into PostgreSQL/pgvector.

## Architecture through M2

The API and Worker are separate processes in one Go module. Shared infrastructure lives under `internal/`; HTTP concerns are isolated under `internal/transport/http`; versioned SQL is the only source of database schema changes. Registration, token rotation, owner-scoped knowledge-base CRUD, document upload/status/retry/chunk-preview APIs, Redis queueing, parsing, recursive chunking, fake embedding, and transactional vector persistence are implemented.

The API applies pending migrations while holding a PostgreSQL advisory lock before accepting traffic. The API stores validated source files under generated MinIO keys and enqueues only job identifiers. The Worker downloads the source, parses and cleans it, chunks it using the knowledge-base configuration, embeds chunks in configurable batches, then writes every chunk and changes the document to `ready` in one database transaction.

## Asynchronous indexing behavior

- Redis carries `document.index` messages; API upload never parses or embeds synchronously.
- TXT and Markdown retain paragraph positions; Markdown also retains heading paths.
- PDF extracts text page by page and records `page_start`/`page_end` on chunks.
- DOCX extracts headings, body paragraphs, and table text with paragraph positions.
- Job progress advances through `parsing`, `chunking`, `embedding`, and `completed`; the document mirrors the processing state.
- The M2 deterministic `FakeEmbedder` runs offline and emits 1024-dimensional vectors in configurable batches. It is a test/development adapter, not a semantic production embedding model.
- A job is claimed only while pending. Replayed messages for running or succeeded jobs are no-ops, while the database unique index on `(document_id, index_version, chunk_index)` provides a second idempotency boundary.
- Chunk inserts, final chunk count, job success, and document `ready` are committed atomically. Failed parsing or embedding cannot expose a partially written index.
- Failed jobs can be reset through `POST /api/v1/documents/{id}/retry`; the attempt counter increments when the Worker actually claims an attempt.

## Quick start

Requirements: Docker with Compose v2. The host ports bind to `127.0.0.1` only.

```sh
cp .env.example .env
docker compose up -d --build
curl http://localhost:8080/api/v1/health/live
curl http://localhost:8080/api/v1/health/ready
```

Expected readiness response:

```json
{"data":{"checks":{"minio":"ok","postgres":"ok","redis":"ok"},"status":"ready"},"request_id":"..."}
```

To prove migration idempotency, run the migration command twice. Both executions should exit successfully and report that migrations are current:

```sh
docker compose run --rm api /usr/local/bin/migrate
docker compose run --rm api /usr/local/bin/migrate
```

After registering, creating a knowledge base, and uploading a supported document, poll `GET /api/v1/documents/{id}` until its status is `ready`. `GET /api/v1/documents/{id}/chunks` then returns the persisted chunk preview.

`docker compose down` stops the stack but retains named volumes. This project does not provide a target that deletes volumes.

## Running Go processes on the host

Environment variables are read directly from the process environment; the application intentionally does not parse `.env` files. Export values from `.env.example`, but change service hostnames from `postgres`, `redis`, and `minio` to `localhost` when running outside Compose. Then use `make run-api` or `make run-worker`.

Startup fails with a concise configuration error when required values are missing or malformed. Secrets are represented by a redacting type and are never logged. `EMBEDDING_DIMENSION` is currently required to be `1024`, matching `VECTOR(1024)` and the HNSW index in migration 000001. A dimension change requires a deliberate later migration and coordinated model configuration.

## Development commands

```sh
make fmt
make test
make vet
make build
docker compose config --quiet
```

PostgreSQL integration tests are opt-in and remain offline with respect to model providers:

```sh
KNOWFLOW_TEST_DATABASE_URL='postgres://knowflow:knowflow-dev-password@localhost:5432/knowflow?sslmode=disable' go test ./internal/ingestion ./internal/transport/http -count=1
```

On Windows without a POSIX `make`, run the underlying Go and Docker commands directly.

## Health behavior

- `GET /api/v1/health/live` only confirms that the HTTP process is alive.
- `GET /api/v1/health/ready` concurrently checks PostgreSQL, Redis, and authenticated access to the configured MinIO bucket. It returns HTTP 503 with the unified error envelope if any required dependency is unavailable.
- A valid UUID supplied in `X-Request-ID` is propagated; otherwise the API generates a UUID v4. Every JSON response and request log includes it.

Errors exposed to clients are deliberately generic. Dependency details are present only in structured server logs. Both API and Worker handle interrupt/termination signals and shut down within configured timeouts.

## Configuration

`.env.example` is the configuration reference. Model provider variables may remain empty because M2 uses the offline fake embedder. `EMBEDDING_BATCH_SIZE`, `WORKER_POLL_TIMEOUT`, and `INGESTION_JOB_TIMEOUT` control the M2 Worker. API startup requires HTTP, PostgreSQL, Redis, MinIO, and JWT values; Worker startup requires PostgreSQL, Redis, and MinIO; the migration command requires PostgreSQL. In `production`, placeholder MinIO credentials, a short/placeholder JWT secret, and wildcard CORS are rejected.

## Database schema

`migrations/000001_core.up.sql` creates pgcrypto and pgvector plus the requirements-defined core tables: users, refresh tokens, knowledge bases, documents, document chunks, ingestion jobs, conversations, messages, and per-call model usage. It includes foreign keys, state checks, non-negative value checks, partial uniqueness for soft-deleted records, GIN full-text indexing, and an HNSW cosine vector index.

The `simple` PostgreSQL text-search configuration only tokenizes on basic word boundaries and is not a production-grade Chinese tokenizer. Hybrid/full-text retrieval is scheduled for M4, where that limitation must be addressed or explicitly retained as a documented tradeoff.

## Known M2 limitations

- PDF indexing extracts an existing text layer; scanned/image-only PDFs and OCR are outside the requirements and fail with `EMPTY_DOCUMENT` when no text is extractable. Password-protected or malformed PDFs fail with `DOCUMENT_PARSE_FAILED`.
- DOCX parsing targets the main `word/document.xml` body. Headers, footers, comments, drawings, and embedded media are not indexed.
- The fake vectors are deterministic and exercise pgvector persistence only. Dense semantic retrieval and a real OpenAI-compatible embedding adapter belong to later milestones.
- M3 retrieval, conversations, ChatModel streaming, SSE, and citations are intentionally not implemented.
