CREATE FUNCTION knowflow_sparse_coverage(document_vector TSVECTOR, input TEXT) RETURNS REAL
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$
    WITH terms AS (
        SELECT DISTINCT term
        FROM regexp_split_to_table(knowflow_search_primary_terms(input), ' +') AS tokens(term)
        WHERE term <> ''
    )
    SELECT COALESCE(
        count(*) FILTER (WHERE term = ANY(tsvector_to_array(document_vector)))::REAL /
            NULLIF(count(*), 0),
        0
    )
    FROM terms;
$$;
