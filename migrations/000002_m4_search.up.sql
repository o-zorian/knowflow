CREATE FUNCTION knowflow_search_terms(input TEXT) RETURNS TEXT
LANGUAGE plpgsql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$
DECLARE
    result TEXT := '';
    current_word TEXT := '';
    previous_han TEXT := '';
    current_char TEXT;
    position INTEGER;
BEGIN
    FOR position IN 1..char_length(input) LOOP
        current_char := lower(substr(input, position, 1));
        IF current_char ~ '^[a-z0-9]$' THEN
            current_word := current_word || current_char;
            previous_han := '';
        ELSE
            IF current_word <> '' THEN
                result := result || ' ' || current_word;
                current_word := '';
            END IF;
            IF current_char ~ '^[一-龥]$' THEN
                result := result || ' ' || current_char;
                IF previous_han <> '' THEN
                    result := result || ' ' || previous_han || current_char;
                END IF;
                previous_han := current_char;
            ELSE
                previous_han := '';
            END IF;
        END IF;
    END LOOP;
    IF current_word <> '' THEN
        result := result || ' ' || current_word;
    END IF;
    RETURN btrim(result);
END;
$$;

DROP INDEX document_chunks_search_vector_idx;
ALTER TABLE document_chunks DROP COLUMN search_vector;
ALTER TABLE document_chunks ADD COLUMN search_vector TSVECTOR
    GENERATED ALWAYS AS (to_tsvector('simple', knowflow_search_terms(content))) STORED;
CREATE INDEX document_chunks_search_vector_idx ON document_chunks USING GIN (search_vector);
