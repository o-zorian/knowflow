# KnowFlow

KnowFlow is a deployable enterprise knowledge-base RAG platform built with Go, Vue 3, PostgreSQL/pgvector, Redis, and MinIO. It demonstrates the complete loop from source ingestion through hybrid retrieval, grounded streaming answers, evaluation, and service governance. The default development profile uses deterministic Fake models, so the complete product and test suite run without paid APIs.

## What is included

- Email/password registration, JWT access tokens, rotating hashed refresh tokens, logout, rate limits, and strict owner isolation.
- Knowledge-base CRUD with configurable chunking, Dense, Sparse, RRF, score threshold, final Top K, and optional Reranker.
- Validated PDF, DOCX, Markdown, and TXT upload to MinIO with SHA-256 duplicate detection.
- Independent Go Worker for parse, clean, recursive chunk, batch embed, atomic pgvector persistence, progress, failure, retry, and idempotency.
- Multi-turn chat with query rewriting, concurrent hybrid retrieval, rerank fallback, JSON SSE, server-validated source citations, and persistence.
- Per-call model usage, Token, latency, configurable cost, Prometheus metrics, admin APIs, and a Vue governance screen.
- A deterministic 60-question, four-strategy evaluator that writes JSON and Markdown reports.

## Product tour

The Vue application connects only to real backend APIs; it contains no fabricated dashboard or chat data.

![KnowFlow sign-in and product introduction](docs/screenshots/01-sign-in.png)

![Knowledge-base and retrieval configuration screen](docs/screenshots/02-knowledge-bases.png)

![Asynchronous document indexing and status screen](docs/screenshots/03-documents.png)

![Grounded SSE answer with source citation drawer](docs/screenshots/04-grounded-chat.png)

## Architecture

```mermaid
flowchart LR
    Web["Vue 3 Web"] -->|"JSON + SSE"| API["Go API"]
    API --> PG[("PostgreSQL + pgvector")]
    API --> Redis[("Redis")]
    API --> MinIO[("MinIO")]
    Redis --> Worker["Go Worker"]
    Worker --> MinIO
    Worker --> PG
    API --> Models["OpenAI-compatible models"]
    Worker --> Models
    Eval["Go evaluator"] --> PG
```

The API and Worker are independently runnable processes in one modular Go codebase. Versioned SQL is the only formal schema source. See [the full architecture and sequence diagrams](docs/architecture.md) for ingestion, retrieval, persistence, and trust boundaries.

### Retrieval and generation flow

1. Persist the user message and pending assistant message before model work starts.
2. Rewrite follow-up questions with the latest six completed messages; fall back to the original query on error.
3. Execute enabled Dense pgvector and Sparse `tsvector` searches concurrently, always scoped to the owner, knowledge base, ready document, and current index version.
4. Fuse with RRF, de-duplicate chunks, apply `minimum_score`, and optionally rerank. A reranker failure transparently falls back to RRF order.
5. Bound the final evidence to 8,000 estimated tokens, assign stable evidence numbers, and stream the answer through SSE.
6. Remove nonexistent citation markers, map valid markers to real chunk rows, then persist the answer, citation snapshots, retrieval trace, Token, latency, and cost.

## Quick start — fresh environment

Requirements: Docker with Compose v2. The development stack needs no external model credential. All published service ports bind to `127.0.0.1`.

1. Create the local environment file.

   macOS/Linux:

   ```sh
   cp .env.example .env
   ```

   PowerShell:

   ```powershell
   Copy-Item .env.example .env
   ```

2. Build and start PostgreSQL/pgvector, Redis, MinIO, API, Worker, and Web.

   ```sh
   docker compose up -d --build
   docker compose ps
   ```

3. Verify readiness and open the product.

   ```sh
   curl http://localhost:8080/api/v1/health/ready
   ```

   Expected checks are `postgres: ok`, `redis: ok`, and `minio: ok`. Open [http://localhost:5173](http://localhost:5173), register, create a knowledge base, upload `demo/knowflow-demo.md`, wait for `ready`, and ask `KnowFlow 支持哪些文档格式？`. Click citation `[1]` to inspect the persisted original excerpt and location.

4. Run the reproducible API-to-Worker release acceptance. It creates a unique user and knowledge base, uploads the same demo source, waits for indexing, consumes every required SSE event, validates a real citation, and reloads the saved answer.

   ```sh
   docker compose run --rm --build smoke
   ```

   A successful run ends with `SMOKE PASSED`.

5. Run the 60-question comparison.

   ```sh
   docker compose run --rm --build eval
   ```

   Results are written to [JSON](eval/results/m5-comparison.json) and [Markdown](eval/results/m5-comparison.md). The evaluator seeds only its deterministic `demo-kb` namespace.

6. Stop the stack without deleting data.

   ```sh
   docker compose down
   ```

Compose intentionally has no target that deletes volumes. The complete startup and one cited answer normally take a few minutes and stay comfortably inside the 30-minute release criterion.

## UI workflow

- Register or sign in at `http://localhost:5173`.
- Create a knowledge base; setting either source Top K to zero selects Dense-only or Sparse-only. Setting both to zero is rejected.
- Upload a supported document and watch live progress. Failed jobs expose a sanitized reason and Retry action; ready documents expose chunk preview.
- Create a conversation. The answer streams incrementally, then source buttons open the original excerpt, filename, page/paragraph location, chunk ID, and score.
- An administrator sees System Governance for live summary, failed jobs, model usage, Token, latency, and cost. To promote a local development user explicitly:

  ```sh
  docker compose exec postgres psql -U knowflow -d knowflow -c "UPDATE users SET role='admin' WHERE email='you@example.com';"
  ```

  Sign in again after promotion so the new JWT carries the admin role. Production role assignment is deployment-owned; self-promotion is deliberately not an API feature.

## Evaluation results

The committed report covers 60 questions and these required configurations:

| Strategy | Dense Top K | Sparse Top K | Reranker |
|---|---:|---:|---:|
| Dense only | 20 | 0 | off |
| Sparse only | 0 | 20 | off |
| Dense + Sparse + RRF | 20 | 20 | off |
| Dense + Sparse + RRF + Reranker | 20 | 20 | on |

Each report includes configuration, Recall@1/5/10, MRR, citation hit rate, average/P95 retrieval and end-to-end latency, average Token and configured cost, per-question output, failures, and improvement conclusions. The default prices are illustrative report-only configuration—not provider price claims.

## Configuration

`.env.example` is the complete reference. Important groups are:

| Group | Variables | Behavior |
|---|---|---|
| Core | `DATABASE_URL`, `REDIS_*`, `MINIO_*`, `JWT_SECRET` | Required by the relevant process; startup fails fast on missing or malformed values. |
| Web | `WEB_PORT`, `WEB_API_BASE_URL`, `CORS_ALLOWED_ORIGINS` | The Web process exposes the browser API URL at runtime; allowed origins are enforced by the API. |
| Chat | `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL` | All empty in development selects `FakeChatModel`; partial groups are rejected. |
| Embedding | `EMBEDDING_BASE_URL`, `EMBEDDING_API_KEY`, `EMBEDDING_MODEL`, `EMBEDDING_DIMENSION`, `EMBEDDING_BATCH_SIZE` | All empty selects the deterministic lexical Fake Embedder. Dimension is fixed at 1024 by the migration. |
| Rerank | `RERANK_BASE_URL`, `RERANK_API_KEY`, `RERANK_MODEL` | Optional; all empty selects Fake Reranker in development. |
| Reliability | `MODEL_MAX_RETRIES`, `WORKER_POLL_TIMEOUT`, `INGESTION_JOB_TIMEOUT` | Bounds provider retry and Worker execution. |
| Governance | `RATE_LIMIT_*`, `LOGIN_FAILURE_*`, per-model cost variables | Controls Redis limits and usage accounting. |

Supplying a real provider requires its Base URL, API key, and model name as one complete group. Keys are read only from environment, wrapped by a redacting configuration type, and never returned to the frontend or written to logs. Production startup rejects placeholder MinIO credentials, weak/placeholder JWT secrets, wildcard CORS, and missing LLM/Embedding providers.

## API and process operation

The [OpenAPI 3.1 contract](docs/openapi.yaml) documents every public JSON/SSE endpoint. Prometheus metrics are served separately at [http://localhost:8080/metrics](http://localhost:8080/metrics).

Health behavior:

- `GET /api/v1/health/live` confirms the HTTP process is alive.
- `GET /api/v1/health/ready` checks PostgreSQL, Redis, and authenticated MinIO bucket access concurrently.
- A valid `X-Request-ID` UUID is propagated; otherwise the API creates one. Every JSON response and structured access log contains it.

To prove migration idempotency:

```sh
docker compose run --rm api /usr/local/bin/migrate
docker compose run --rm api /usr/local/bin/migrate
```

To run processes on the host, export `.env.example` values while changing `postgres`, `redis`, and `minio` hostnames to `localhost`, then use `make run-api` and `make run-worker`. The application intentionally does not parse `.env` files. API and Worker both handle interrupt/termination signals and shut down within configured timeouts.

## Development and CI

Go 1.25 and Node 24 are used by CI and container builds.

```sh
cd web && npm ci && cd ..
make verify
```

Without POSIX `make`, run the equivalent commands:

```sh
gofmt -w cmd internal migrations
go vet ./cmd/... ./internal/... ./migrations
go test ./cmd/... ./internal/... ./migrations -count=1
go build ./cmd/... ./internal/... ./migrations
cd web
npm ci
npm run lint
npm test
npm run build
cd ..
docker compose config --quiet
```

PostgreSQL integration tests remain offline with respect to model providers:

```sh
KNOWFLOW_TEST_DATABASE_URL='postgres://knowflow:knowflow-dev-password@localhost:5432/knowflow?sslmode=disable' \
  go test -p 1 ./internal/ingestion ./internal/retrieval ./internal/transport/http -count=1
```

The GitHub Actions workflow runs Go format/vet/test/build, Vue lint/test/build, serialized pgvector integration suites, Compose validation, and all container builds. Automation never calls a paid model.

## Repository map

```text
cmd/api, cmd/worker       independent production processes
cmd/eval, cmd/smoke       evaluation and release acceptance tools
internal/                 domain, adapters, platform, and HTTP transport
migrations/               versioned PostgreSQL/pgvector schema
web/                      Vue 3 + Vite product UI
demo/                     fixed release demonstration source
eval/                     60-question dataset and reports
docs/                     OpenAPI, diagrams, screenshots, demo script
.github/workflows/ci.yml  lint, test, integration, and build CI
```

The [three-minute demo script](docs/demo-script.md) is suitable for a recording or live walkthrough.

## Known limitations

- PDF indexing extracts an existing text layer. Scanned/image-only PDF and OCR are outside the release scope and fail as an empty document; password-protected or malformed files fail with a controlled parse error.
- DOCX parsing targets the main `word/document.xml` body, including headings, body paragraphs, and tables. Headers, footers, comments, drawings, and embedded media are not indexed.
- The first-release sparse tokenizer retains ASCII words and creates common Han unigram/bigram terms before PostgreSQL `simple` stemming. It has no linguistic Chinese segmentation, synonyms, phrase scoring, or every CJK extension block. Long `plainto_tsquery` input also uses AND semantics.
- Fake embeddings are deterministic and lexical. They prove the complete pgvector path and make offline evaluation repeatable, but they are not a claim about production semantic quality.
- The rerank adapter uses the common `/rerank` `{model, query, documents, top_n}` shape because OpenAI compatibility does not define a universal rerank protocol.
- Automatic fallback to a second external model provider is not configured. Streaming cannot safely switch after deltas have been emitted without risking duplicated text.
- If an SSE client disconnects, request context cancels active rewriting/retrieval/rerank/generation and a short independent context marks the assistant message `failed` with `CLIENT_DISCONNECTED`; generation does not continue in the background.
- Redis list processing is duplicate-safe but does not use a separate in-flight acknowledgement list. A hard Worker crash after dequeue can leave a running job for operator recovery.
