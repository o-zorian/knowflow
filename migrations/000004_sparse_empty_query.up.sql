CREATE OR REPLACE FUNCTION knowflow_sparse_query(input TEXT) RETURNS TSQUERY
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$
    SELECT CASE
        WHEN btrim(knowflow_search_terms(input)) = '' THEN NULL::tsquery
        ELSE to_tsquery('simple', regexp_replace(knowflow_search_terms(input), ' +', ' | ', 'g'))
    END;
$$;
