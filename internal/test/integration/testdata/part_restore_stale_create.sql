CREATE OR REPLACE FUNCTION public.part_restore_stale_inject() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, audit, public
AS $$
BEGIN
    IF NEW.event #>> '{context,phase}' = 'intent'
       AND NEW.event #>> '{extra,target_event_id}' = '{{target}}' THEN
        INSERT INTO audit.event_parts (org_id, event_id, part_index, digest, payload)
        VALUES ('{{org}}', '{{target}}', 0, '{{digest}}', decode('{{payload_hex}}', 'hex'))
        ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;
