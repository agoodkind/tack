-- The Up migration grants the current database user membership in ops_outbox_owner
-- and grants ops_outbox_owner CREATE on the public schema.
--
-- Up sets ops_outbox_owner as the owner of each listed function, revokes all
-- PUBLIC privileges on those functions, and grants audit_operator EXECUTE (TACK-583).
-- Up then revokes CREATE on public from ops_outbox_owner and revokes the current
-- database user's membership in ops_outbox_owner.
--
-- The Down migration grants the current database user membership in ops_outbox_owner,
-- revokes audit_operator EXECUTE on each listed function, and revokes that membership.

-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    signature TEXT;
BEGIN
    EXECUTE 'GRANT ops_outbox_owner TO ' || quote_ident(current_user);
    GRANT CREATE ON SCHEMA public TO ops_outbox_owner;
    FOREACH signature IN ARRAY ARRAY[
        'public.ops_read_begin(uuid, uuid, smallint, jsonb)',
        'public.ops_read_renew(uuid, uuid, bigint)',
        'public.ops_read_template(uuid, uuid, bigint, text)',
        'public.ops_read_finalize(uuid, uuid, bigint, text, text, jsonb, bytea[])'
    ] LOOP
        EXECUTE 'ALTER FUNCTION ' || signature || ' OWNER TO ops_outbox_owner';
        EXECUTE 'REVOKE ALL ON FUNCTION ' || signature || ' FROM PUBLIC';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ' || signature || ' TO audit_operator';
    END LOOP;
    REVOKE CREATE ON SCHEMA public FROM ops_outbox_owner;
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
    signature TEXT;
BEGIN
    EXECUTE 'GRANT ops_outbox_owner TO ' || quote_ident(current_user);
    FOREACH signature IN ARRAY ARRAY[
        'public.ops_read_begin(uuid, uuid, smallint, jsonb)',
        'public.ops_read_renew(uuid, uuid, bigint)',
        'public.ops_read_template(uuid, uuid, bigint, text)',
        'public.ops_read_finalize(uuid, uuid, bigint, text, text, jsonb, bytea[])'
    ] LOOP
        EXECUTE 'REVOKE EXECUTE ON FUNCTION ' || signature || ' FROM audit_operator';
    END LOOP;
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd
