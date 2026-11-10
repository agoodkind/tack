-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.audit_part_restore_check_keys(p_raw_texts TEXT[])
RETURNS VOID
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    has_duplicate BOOLEAN;
BEGIN
    WITH RECURSIVE walk(value) AS (
        SELECT t::JSON FROM unnest(p_raw_texts) AS t
        UNION ALL
        SELECT child.value FROM walk
        CROSS JOIN LATERAL (
            SELECT e.value FROM json_each(CASE WHEN json_typeof(walk.value) = 'object' THEN walk.value ELSE '{}'::JSON END) AS e
            UNION ALL
            SELECT a.value FROM json_array_elements(CASE WHEN json_typeof(walk.value) = 'array' THEN walk.value ELSE '[]'::JSON END) AS a
        ) AS child
    )
    SELECT EXISTS (
        SELECT 1 FROM walk
         WHERE json_typeof(walk.value) = 'object'
           AND (SELECT count(*) FROM json_object_keys(walk.value)) <> (SELECT count(DISTINCT k) FROM json_object_keys(walk.value) AS k))
      INTO has_duplicate;
    IF has_duplicate THEN
        RAISE EXCEPTION 'restore refused: a change message repeats a JSON object key' USING ERRCODE = 'TK620';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'GRANT audit_writer TO ' || quote_ident(current_user);
    GRANT CREATE ON SCHEMA public TO audit_writer;
    ALTER FUNCTION public.audit_part_restore_check_keys(TEXT[]) OWNER TO audit_writer;
    REVOKE ALL ON FUNCTION public.audit_part_restore_check_keys(TEXT[]) FROM PUBLIC;
    REVOKE CREATE ON SCHEMA public FROM audit_writer;
    EXECUTE 'REVOKE audit_writer FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS public.audit_part_restore_check_keys(TEXT[]);
-- +goose StatementEnd
