DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tack_test_read_finalize_denied') THEN
        CREATE ROLE tack_test_read_finalize_denied NOLOGIN;
    END IF;
END
$$;
GRANT EXECUTE ON FUNCTION public.ops_read_begin(uuid, uuid, smallint, jsonb) TO tack_test_read_finalize_denied;
GRANT EXECUTE ON FUNCTION public.ops_read_renew(uuid, uuid, bigint) TO tack_test_read_finalize_denied;
