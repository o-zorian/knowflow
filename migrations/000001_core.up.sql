CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email VARCHAR(320) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(16) NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    status VARCHAR(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_unique ON users (lower(email));

CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash VARCHAR(255) NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);
CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_active_expiry_idx ON refresh_tokens (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE knowledge_bases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL CHECK (length(btrim(name)) > 0),
    description TEXT NOT NULL DEFAULT '',
    embedding_model VARCHAR(255) NOT NULL,
    embedding_dimension INTEGER NOT NULL DEFAULT 1024 CHECK (embedding_dimension = 1024),
    retrieval_config JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(retrieval_config) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX knowledge_bases_owner_name_active_unique
    ON knowledge_bases (owner_id, lower(name)) WHERE deleted_at IS NULL;
CREATE INDEX knowledge_bases_owner_id_idx ON knowledge_bases (owner_id) WHERE deleted_at IS NULL;

CREATE TABLE documents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    knowledge_base_id UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    filename VARCHAR(1024) NOT NULL,
    mime_type VARCHAR(255) NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256 CHAR(64) NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    object_key VARCHAR(1024) NOT NULL UNIQUE,
    status VARCHAR(16) NOT NULL CHECK (status IN ('uploaded', 'queued', 'parsing', 'chunking', 'embedding', 'ready', 'failed', 'deleting')),
    chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
    index_version INTEGER NOT NULL DEFAULT 1 CHECK (index_version > 0),
    error_code VARCHAR(128),
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX documents_kb_sha256_active_unique
    ON documents (knowledge_base_id, sha256) WHERE deleted_at IS NULL;
CREATE INDEX documents_knowledge_base_id_idx ON documents (knowledge_base_id) WHERE deleted_at IS NULL;
CREATE INDEX documents_status_idx ON documents (status) WHERE deleted_at IS NULL;

CREATE TABLE document_chunks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    knowledge_base_id UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    index_version INTEGER NOT NULL CHECK (index_version > 0),
    chunk_index INTEGER NOT NULL CHECK (chunk_index >= 0),
    content TEXT NOT NULL CHECK (length(content) > 0),
    token_count INTEGER NOT NULL DEFAULT 0 CHECK (token_count >= 0),
    page_start INTEGER CHECK (page_start IS NULL OR page_start > 0),
    page_end INTEGER CHECK (page_end IS NULL OR page_end > 0),
    heading_path TEXT,
    content_hash CHAR(64) NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    search_vector TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED,
    embedding VECTOR(1024) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (page_start IS NULL OR page_end IS NULL OR page_end >= page_start)
);
CREATE INDEX document_chunks_knowledge_base_id_idx ON document_chunks (knowledge_base_id);
CREATE UNIQUE INDEX document_chunks_version_position_unique
    ON document_chunks (document_id, index_version, chunk_index);
CREATE INDEX document_chunks_search_vector_idx ON document_chunks USING GIN (search_vector);
CREATE INDEX document_chunks_embedding_hnsw_idx
    ON document_chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE ingestion_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    index_version INTEGER NOT NULL CHECK (index_version > 0),
    status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    stage VARCHAR(32) NOT NULL DEFAULT 'queued',
    progress INTEGER NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    idempotency_key VARCHAR(255) NOT NULL UNIQUE,
    error_code VARCHAR(128),
    error_message TEXT,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (finished_at IS NULL OR started_at IS NULL OR finished_at >= started_at)
);
CREATE INDEX ingestion_jobs_document_id_idx ON ingestion_jobs (document_id);
CREATE INDEX ingestion_jobs_status_created_idx ON ingestion_jobs (status, created_at);

CREATE TABLE conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    knowledge_base_id UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    title VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX conversations_user_id_updated_idx ON conversations (user_id, updated_at DESC);
CREATE INDEX conversations_knowledge_base_id_idx ON conversations (knowledge_base_id);

CREATE TABLE messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role VARCHAR(16) NOT NULL CHECK (role IN ('user', 'assistant')),
    content TEXT NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN ('pending', 'streaming', 'completed', 'failed')),
    citations JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(citations) = 'array'),
    retrieval_trace JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(retrieval_trace) = 'object'),
    model VARCHAR(255),
    prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    completion_tokens INTEGER NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    estimated_cost_usd NUMERIC(18, 8) NOT NULL DEFAULT 0 CHECK (estimated_cost_usd >= 0),
    latency_ms INTEGER NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    error_code VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX messages_conversation_id_created_idx ON messages (conversation_id, created_at);

CREATE TABLE model_usage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    knowledge_base_id UUID REFERENCES knowledge_bases(id) ON DELETE SET NULL,
    request_id UUID,
    trace_id UUID NOT NULL,
    request_type VARCHAR(16) NOT NULL CHECK (request_type IN ('chat', 'embedding', 'rerank')),
    model VARCHAR(255) NOT NULL,
    prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    completion_tokens INTEGER NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    text_count INTEGER NOT NULL DEFAULT 0 CHECK (text_count >= 0),
    estimated_cost_usd NUMERIC(18, 8) NOT NULL DEFAULT 0 CHECK (estimated_cost_usd >= 0),
    latency_ms INTEGER NOT NULL CHECK (latency_ms >= 0),
    status VARCHAR(16) NOT NULL CHECK (status IN ('succeeded', 'failed')),
    error_code VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX model_usage_created_at_idx ON model_usage (created_at DESC);
CREATE INDEX model_usage_user_id_created_idx ON model_usage (user_id, created_at DESC);
CREATE INDEX model_usage_knowledge_base_id_created_idx ON model_usage (knowledge_base_id, created_at DESC);
CREATE INDEX model_usage_model_created_idx ON model_usage (model, created_at DESC);

CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER knowledge_bases_set_updated_at BEFORE UPDATE ON knowledge_bases
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER documents_set_updated_at BEFORE UPDATE ON documents
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER conversations_set_updated_at BEFORE UPDATE ON conversations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
