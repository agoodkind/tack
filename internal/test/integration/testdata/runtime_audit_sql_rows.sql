SELECT row_value
  FROM (
        SELECT 'users ' || to_jsonb(stored)::text AS row_value
          FROM public.users AS stored
         WHERE stored.id = ANY($1::uuid[])
        UNION ALL
        SELECT 'api_tokens ' || (to_jsonb(stored) - 'token_hash')::text
          FROM public.api_tokens AS stored
         WHERE stored.user_id = ANY($1::uuid[])
        UNION ALL
        SELECT 'org_members ' || to_jsonb(stored)::text
          FROM public.org_members AS stored
         WHERE stored.user_id = ANY($1::uuid[])
       ) AS rows_of_users
 ORDER BY row_value
