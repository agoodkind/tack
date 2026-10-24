WITH audit_tables AS (
    SELECT c.oid, c.relname
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE c.relkind = 'r'
       AND (n.nspname = 'audit'
            OR (n.nspname = 'public' AND c.relname IN ('ops_outbox', 'ops_outbox_parts', 'ops_read_state')))
), event_tables AS (
    SELECT i.inhrelid AS oid FROM pg_inherits i WHERE i.inhparent = 'audit.events'::regclass
)
SELECT coalesce((SELECT sum(pg_indexes_size(oid)) FROM audit_tables), 0)::bigint,
       coalesce((SELECT sum(pg_total_relation_size(oid)) FROM audit_tables), 0)::bigint,
       coalesce((SELECT sum(pg_total_relation_size(oid)) FROM event_tables), 0)::bigint,
       (SELECT count(*) FROM audit.events),
       coalesce((SELECT sum(pg_total_relation_size(oid)) FROM audit_tables WHERE relname = 'event_parts'), 0)::bigint,
       (SELECT count(*) FROM audit.event_parts)
