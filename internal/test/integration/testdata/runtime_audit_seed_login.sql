CREATE ROLE {{.Login}} LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '{{.Secret}}';
GRANT USAGE ON SCHEMA public TO {{.Login}};
GRANT SELECT ON public.goose_db_version TO {{.Login}};
GRANT SELECT, INSERT, UPDATE, DELETE ON public.users, public.api_tokens, public.org_members TO {{.Login}};
