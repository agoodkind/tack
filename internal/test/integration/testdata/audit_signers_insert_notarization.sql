INSERT INTO audit.notarizations (org_id, notarized_at, merkle_root, shard_heads, signature, signing_key, signing_host)
VALUES ($1, $2, $3, '[]'::jsonb, $4, $5, 'read-suppression-test')
