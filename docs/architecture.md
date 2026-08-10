# KnowFlow architecture

KnowFlow is a modular monolith with two independent Go processes. The API owns synchronous business operations and RAG orchestration; the Worker owns asynchronous ingestion. Both share versioned domain and platform packages without pretending to be separate microservices.

```mermaid
flowchart LR
    Browser["Vue 3 Web"] -->|"HTTP / JSON / SSE"| API["Go API"]
    API -->|"business data, tsvector, pgvector"| PG[("PostgreSQL + pgvector")]
    API -->|"rate limits, cache, job IDs"| Redis[("Redis")]
    API -->|"generated object keys"| MinIO[("MinIO")]
    API -->|"OpenAI-compatible calls"| Models["Chat / Embedding / Rerank"]
    Redis -->|"document.index"| Worker["Go Index Worker"]
    Worker --> MinIO
    Worker --> Models
    Worker --> PG
    Eval["Go Evaluator"] --> PG
    Eval --> Reports["JSON + Markdown reports"]
```

## Ingestion lifecycle

```mermaid
sequenceDiagram
    participant U as User / Web
    participant A as API
    participant O as MinIO
    participant R as Redis
    participant W as Worker
    participant P as PostgreSQL
    U->>A: multipart upload
    A->>A: validate extension, MIME, size, SHA-256
    A->>O: store under generated object key
    A->>P: document + idempotent job
    A->>R: enqueue job ID
    A-->>U: 201 queued
    W->>R: claim job ID
    W->>O: download source
    W->>W: parse, clean, chunk, batch embed
    W->>P: atomic chunks + ready + succeeded
    U->>A: poll document status
    A-->>U: progress / ready / failure
```

## Grounded answer path

```mermaid
flowchart TD
    Q["Persist user + pending assistant messages"] --> Rewrite["Rewrite with latest 6 messages"]
    Rewrite --> Dense["Dense Top K"]
    Rewrite --> Sparse["Sparse Top K"]
    Dense --> RRF["RRF fusion + de-duplication"]
    Sparse --> RRF
    RRF --> Filter["Minimum score + optional rerank fallback"]
    Filter --> Evidence["Final Top K, 8k-token evidence budget"]
    Evidence --> Stream["ChatModel SSE stream"]
    Stream --> Validate["Validate citation numbers"]
    Validate --> Save["Persist answer, citations, trace, usage"]
```

## Trust boundaries

- Every resource query is scoped by the authenticated owner and knowledge-base ID.
- Source filenames never become object-store paths; object keys are generated server-side.
- API keys exist only in process environment and are represented by a redacting configuration type.
- Provider and database details are logged server-side but never returned in client error envelopes.
- Compose publishes development ports only on `127.0.0.1`.
