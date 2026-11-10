SELECT t, has_table_privilege('audit_operator', t, 'SELECT,INSERT,UPDATE,DELETE')
  FROM unnest(ARRAY['audit.events', 'audit.event_parts', 'audit.chain_heads', 'audit.pii']) AS t
