SELECT (SELECT count(*) FROM public.users WHERE email = $1::text)
       + (SELECT count(*)
            FROM public.api_tokens
           WHERE user_id IN (SELECT id FROM public.users WHERE email = $1::text))
       + (SELECT count(*)
            FROM public.org_members
           WHERE user_id IN (SELECT id FROM public.users WHERE email = $1::text)) AS stored_rows,
       (SELECT count(*)
          FROM public.ops_outbox
         WHERE event->'actor'->>'id' = $2::text
           AND event->'context'->>'phase' = 'change') AS change_events
