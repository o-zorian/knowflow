DROP INDEX document_chunks_search_vector_idx;
ALTER TABLE document_chunks DROP COLUMN search_vector;
ALTER TABLE document_chunks ADD COLUMN search_vector TSVECTOR
    GENERATED ALWAYS AS (to_tsvector('simple', knowflow_search_terms(content))) STORED;
CREATE INDEX document_chunks_search_vector_idx ON document_chunks USING GIN (search_vector);

DROP FUNCTION knowflow_sparse_query(TEXT);
DROP FUNCTION knowflow_search_fallback_terms(TEXT);
DROP FUNCTION knowflow_search_primary_terms(TEXT);
