# KnowFlow

KnowFlow is a Go-based enterprise knowledge-base RAG platform. This repository currently contains the M0 engineering foundation: independently runnable API and Worker processes, validated environment configuration, JSON logging and request tracing, versioned PostgreSQL/pgvector migrations, dependency-aware health checks, and a local Docker Compose stack.

## Architecture at M0

The API and Worker are separate processes in one Go module. Shared infrastructure lives under `internal/`; HTTP concerns are isolated under `internal/transport/http`; versioned SQL is the only source of database schema changes. No M1–M6 business endpoints or empty model/retrieval interfaces are created in this milestone.

The API applies pending migrations while holding a PostgreSQL advisory lock before accepting traffic. Re-running the API or the migration command is safe because applied versions are recorded in `schema_migrations`. The Worker validates PostgreSQL, Redis, and the configured MinIO bucket on startup, then continues monitoring those dependencies until graceful shutdown; queue consumption begins in M2.

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

On Windows without a POSIX `make`, run the underlying Go and Docker commands directly.

## Health behavior

- `GET /api/v1/health/live` only confirms that the HTTP process is alive.
- `GET /api/v1/health/ready` concurrently checks PostgreSQL, Redis, and authenticated access to the configured MinIO bucket. It returns HTTP 503 with the unified error envelope if any required dependency is unavailable.
- A valid UUID supplied in `X-Request-ID` is propagated; otherwise the API generates a UUID v4. Every JSON response and request log includes it.

Errors exposed to clients are deliberately generic. Dependency details are present only in structured server logs. Both API and Worker handle interrupt/termination signals and shut down within configured timeouts.

## Configuration

`.env.example` is the complete M0 reference. Model provider variables may remain empty until their owning milestones. API startup requires HTTP, PostgreSQL, Redis, MinIO, and JWT values; Worker startup requires PostgreSQL, Redis, and MinIO; the migration command requires PostgreSQL. In `production`, placeholder MinIO credentials, a short/placeholder JWT secret, and wildcard CORS are rejected.

## Database schema

`migrations/000001_core.up.sql` creates pgcrypto and pgvector plus the requirements-defined core tables: users, refresh tokens, knowledge bases, documents, document chunks, ingestion jobs, conversations, messages, and per-call model usage. It includes foreign keys, state checks, non-negative value checks, partial uniqueness for soft-deleted records, GIN full-text indexing, and an HNSW cosine vector index.

The `simple` PostgreSQL text-search configuration only tokenizes on basic word boundaries and is not a production-grade Chinese tokenizer. Hybrid/full-text retrieval is scheduled for M4, where that limitation must be addressed or explicitly retained as a documented tradeoff.

## Current milestone boundary

M0 intentionally does not include authentication, knowledge-base/document APIs, ingestion execution, model adapters, retrieval, chat/SSE, metrics, evaluation, frontend, or OpenAPI paths. These are implemented in their corresponding M1–M6 milestones rather than as placeholders.
