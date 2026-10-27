REVOKE ALL ON public.users, public.api_tokens, public.org_members FROM {{.Login}};
REVOKE USAGE ON SCHEMA public FROM {{.Login}};
DROP ROLE IF EXISTS {{.Login}};
