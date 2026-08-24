CREATE FUNCTION knowflow_search_primary_terms(input TEXT) RETURNS TEXT
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$
    SELECT COALESCE(string_agg(term, ' ' ORDER BY position), '')
    FROM regexp_split_to_table(knowflow_search_terms(input), ' +') WITH ORDINALITY AS tokens(term, position)
    WHERE char_length(term) > 1 OR term ~ '^[a-z0-9]+$';
$$;

CREATE FUNCTION knowflow_search_fallback_terms(input TEXT) RETURNS TEXT
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$
    SELECT COALESCE(string_agg(term, ' ' ORDER BY position), '')
    FROM regexp_split_to_table(knowflow_search_terms(input), ' +') WITH ORDINALITY AS tokens(term, position)
    WHERE char_length(term) = 1 AND term ~ '^[一-龥]$';
$$;

CREATE FUNCTION knowflow_sparse_query(input TEXT) RETURNS TSQUERY
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$
    SELECT CASE
        WHEN btrim(knowflow_search_terms(input)) = '' THEN ''::tsquery
        ELSE to_tsquery('simple', regexp_replace(knowflow_search_terms(input), ' +', ' | ', 'g'))
    END;
$$;

DROP INDEX document_chunks_search_vector_idx;
ALTER TABLE document_chunks DROP COLUMN search_vector;
ALTER TABLE document_chunks ADD COLUMN search_vector TSVECTOR
    GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', knowflow_search_primary_terms(content)), 'A') ||
        setweight(to_tsvector('simple', knowflow_search_fallback_terms(content)), 'D')
    ) STORED;
CREATE INDEX document_chunks_search_vector_idx ON document_chunks USING GIN (search_vector);
