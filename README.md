# KnowFlow

KnowFlow is a Go-based enterprise knowledge-base RAG platform. The repository currently implements M0 through M4: the engineering foundation, owner-scoped authentication/knowledge-base/document APIs, asynchronous document indexing into PostgreSQL/pgvector, citation-grounded streaming RAG conversations, and configurable hybrid retrieval.

## Architecture through M4

The API and Worker are separate processes in one Go module. Shared infrastructure lives under `internal/`; HTTP concerns are isolated under `internal/transport/http`; versioned SQL is the only source of database schema changes. Registration, token rotation, owner-scoped knowledge-base CRUD, document upload/status/retry/chunk-preview APIs, Redis queueing, parsing, recursive chunking, replaceable model adapters, transactional vector persistence, hybrid retrieval, persisted conversations, streaming chat, and server-validated citations are implemented.

The API applies pending migrations while holding a PostgreSQL advisory lock before accepting traffic. The API stores validated source files under generated MinIO keys and enqueues only job identifiers. The Worker downloads the source, parses and cleans it, chunks it using the knowledge-base configuration, embeds chunks in configurable batches, then writes every chunk and changes the document to `ready` in one database transaction.

For a question, the API persists the user and streaming assistant messages first. It uses the most recent six completed messages to rewrite a follow-up into a standalone query, falling back to the original question on any rewrite error. It then runs the enabled dense pgvector and sparse PostgreSQL full-text searches concurrently, filters every query by owner, knowledge base, ready document, and current index version, fuses hybrid candidates with RRF, optionally reranks the configured candidate count, and falls back to RRF ordering if reranking fails. The final configured candidates are bounded to an 8,000-token context, numbered as evidence, streamed over SSE, validated, and saved with the retrieval trace and real chunk citation snapshots.

## Retrieval configurations

Retrieval is configured per knowledge base. `dense_top_k` and `sparse_top_k` default to 20; setting either one to `0` disables that source, while disabling both is rejected. `rrf_k` defaults to 60, `rerank_top_k` to 10, and `final_top_k` to 5. `minimum_score` is applied to the active strategy's score before reranking.

The four supported configurations are:

| Configuration | `dense_top_k` | `sparse_top_k` | `rerank_enabled` |
|---|---:|---:|---:|
| Dense only | `> 0` | `0` | `false` |
| Sparse only | `0` | `> 0` | `false` |
| Dense + Sparse + RRF | `> 0` | `> 0` | `false` |
| Dense + Sparse + RRF + Reranker | `> 0` | `> 0` | `true` |

The `retrieval.completed` trace records the original and rewritten queries, rewrite fallback, source counts, strategy, dense/sparse/RRF/rerank scores, and rerank fallback. No provider error text is exposed in the trace.

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

Create a conversation and stream a grounded answer with the returned access token and knowledge-base ID:

```sh
curl -X POST http://localhost:8080/api/v1/conversations \
  -H 'Authorization: Bearer <access-token>' \
  -H 'Content-Type: application/json' \
  -d '{"knowledge_base_id":"<knowledge-base-id>","title":"Demo"}'

curl -N -X POST http://localhost:8080/api/v1/conversations/<conversation-id>/messages \
  -H 'Authorization: Bearer <access-token>' \
  -H 'Content-Type: application/json' \
  -d '{"content":"What does this document say?"}'
```

The stream uses JSON payloads for `message.started`, `retrieval.completed`, `message.delta`, `citation`, `usage`, `message.completed`, and `error`. A `citation` contains the persisted document ID, filename, chunk ID, original excerpt, page or paragraph location, and the final retrieval score. `GET /api/v1/conversations/{id}` returns the saved messages and citation snapshots.

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
KNOWFLOW_TEST_DATABASE_URL='postgres://knowflow:knowflow-dev-password@localhost:5432/knowflow?sslmode=disable' go test ./internal/ingestion ./internal/retrieval ./internal/transport/http -count=1
```

On Windows without a POSIX `make`, run the underlying Go and Docker commands directly.

## Health behavior

- `GET /api/v1/health/live` only confirms that the HTTP process is alive.
- `GET /api/v1/health/ready` concurrently checks PostgreSQL, Redis, and authenticated access to the configured MinIO bucket. It returns HTTP 503 with the unified error envelope if any required dependency is unavailable.
- A valid UUID supplied in `X-Request-ID` is propagated; otherwise the API generates a UUID v4. Every JSON response and request log includes it.

Errors exposed to clients are deliberately generic. Dependency details are present only in structured server logs. Both API and Worker handle interrupt/termination signals and shut down within configured timeouts.

## Configuration

`.env.example` is the configuration reference. In development and test, leaving all three `LLM_*` values empty selects the offline `FakeChatModel` and `FakeQueryRewriter`; leaving all three `EMBEDDING_*` values empty selects the deterministic lexical `FakeEmbedder`; leaving all three `RERANK_*` values empty selects `FakeReranker`. Supplying any provider requires its Base URL, API key, and model name as one complete group. OpenAI-compatible `/chat/completions` streaming and `/embeddings` endpoints are supported. The rerank adapter posts the common `{model, query, documents, top_n}` contract to `/rerank` and accepts `{results:[{index,relevance_score}]}`. Production API startup requires complete LLM and embedding provider groups. Reranking remains optional; if a production knowledge base enables it without a configured provider, the request safely falls back to RRF. API keys are read only from the environment and are redacted from configuration formatting.

`EMBEDDING_BATCH_SIZE`, `WORKER_POLL_TIMEOUT`, and `INGESTION_JOB_TIMEOUT` control the Worker. API startup requires HTTP, PostgreSQL, Redis, MinIO, and JWT values; Worker startup requires PostgreSQL, Redis, and MinIO; the migration command requires PostgreSQL. In `production`, placeholder MinIO credentials, a short/placeholder JWT secret, wildcard CORS, and missing model providers are rejected.

If an SSE client disconnects, its request context cancels query rewriting, active dense/sparse retrieval, reranking, or the answer model stream. The API then uses a short independent database context to mark the assistant message `failed` with `CLIENT_DISCONNECTED`; it does not continue generation in the background.

## Database schema

`migrations/000001_core.up.sql` creates pgcrypto and pgvector plus the requirements-defined core tables: users, refresh tokens, knowledge bases, documents, document chunks, ingestion jobs, conversations, messages, and per-call model usage. It includes foreign keys, state checks, non-negative value checks, partial uniqueness, and an HNSW cosine vector index. Migration 000002 replaces the initial basic `search_vector` expression with M4 search-term generation and rebuilds its GIN index.

Sparse retrieval deliberately uses a small, immutable tokenizer suitable for the first release: ASCII letters/digits are retained as lower-case words, and common CJK Unified Ideographs in `U+4E00..U+9FA5` produce unigram and adjacent bigram terms before PostgreSQL `simple` stemming and GIN indexing. It has no linguistic Chinese segmentation, stemming, synonyms, phrase scoring, or support for every CJK extension block; long `plainto_tsquery` inputs also use AND semantics and can reduce sparse recall. A production deployment with broader language needs should replace this function with a dedicated tokenizer and rebuild the generated column/index.

## Known limitations

- PDF indexing extracts an existing text layer; scanned/image-only PDFs and OCR are outside the requirements and fail with `EMPTY_DOCUMENT` when no text is extractable. Password-protected or malformed PDFs fail with `DOCUMENT_PARSE_FAILED`.
- DOCX parsing targets the main `word/document.xml` body. Headers, footers, comments, drawings, and embedded media are not indexed.
- The development fake embedder is deterministic and lexical. It exercises the complete pgvector path but is not a substitute for a production semantic embedding model; configure the OpenAI-compatible embedding adapter for semantic retrieval.
- Model retries, fallback providers, pricing, and the per-call governance views remain M5 work. M3 persists prompt/completion token counts reported by the chat provider on each assistant message but does not estimate cost.
