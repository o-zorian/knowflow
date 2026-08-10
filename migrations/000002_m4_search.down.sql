DROP INDEX document_chunks_search_vector_idx;
ALTER TABLE document_chunks DROP COLUMN search_vector;
ALTER TABLE document_chunks ADD COLUMN search_vector TSVECTOR
    GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED;
CREATE INDEX document_chunks_search_vector_idx ON document_chunks USING GIN (search_vector);
DROP FUNCTION knowflow_search_terms(TEXT);
