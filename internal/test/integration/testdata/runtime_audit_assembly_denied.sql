SELECT event_id FROM audit.event_assemblies LIMIT 1;
SELECT event_id FROM audit.event_assembly_parts LIMIT 1;
DELETE FROM audit.event_assemblies WHERE event_id IS NULL;
DELETE FROM audit.event_assembly_parts WHERE event_id IS NULL
