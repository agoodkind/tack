-- Refuse CREATE, ALTER, and DROP on the audit and partman schemas by any role
-- except tack_migrator (TACK-556). Only a superuser creates an event trigger,
-- and this is the last migration that the engine superuser applies.
--
-- The migration drops both triggers before it replaces the function. On a
-- retry after a partial run, an existing trigger would refuse the
-- replacement.

-- +goose Up
DROP EVENT TRIGGER IF EXISTS tack_audit_schema_guard;
DROP EVENT TRIGGER IF EXISTS tack_audit_schema_guard_drop;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit.refuse_schema_change()
RETURNS event_trigger
LANGUAGE plpgsql
AS $$
DECLARE
    changed TEXT;
BEGIN
    IF current_user = 'tack_migrator' THEN
        RETURN;
    END IF;
    IF tg_event = 'sql_drop' THEN
        SELECT string_agg(object_identity, ', ') INTO changed
          FROM pg_event_trigger_dropped_objects()
         WHERE schema_name IN ('audit', 'partman');
    ELSE
        SELECT string_agg(object_identity, ', ') INTO changed
          FROM pg_event_trigger_ddl_commands()
         WHERE schema_name IN ('audit', 'partman');
    END IF;
    IF changed IS NOT NULL THEN
        RAISE EXCEPTION 'schema change refused on %: only a migration changes the audit and partman schemas', changed
            USING ERRCODE = 'insufficient_privilege',
                  DETAIL = 'command ' || tg_tag || ', session role ' || session_user
                           || ', current role ' || current_user || ', statement: ' || current_query(),
                  HINT = 'Write a migration or a reviewed ops command.';
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER FUNCTION audit.refuse_schema_change() OWNER TO tack_migrator;

CREATE EVENT TRIGGER tack_audit_schema_guard ON ddl_command_end
    EXECUTE PROCEDURE audit.refuse_schema_change();
CREATE EVENT TRIGGER tack_audit_schema_guard_drop ON sql_drop
    EXECUTE PROCEDURE audit.refuse_schema_change();

-- +goose Down
DROP EVENT TRIGGER IF EXISTS tack_audit_schema_guard;
DROP EVENT TRIGGER IF EXISTS tack_audit_schema_guard_drop;
DROP FUNCTION IF EXISTS audit.refuse_schema_change();
