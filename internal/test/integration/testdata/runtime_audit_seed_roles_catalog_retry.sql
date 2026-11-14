CREATE SEQUENCE public.tack_test_catalog_retry_sequence START 1 CACHE 1;

CREATE FUNCTION public.tack_test_catalog_retry_failure()
RETURNS TRIGGER
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NEW.event #>> '{actor,id}' IS DISTINCT FROM TG_ARGV[0] THEN
        RETURN NEW;
    END IF;
    IF NEW.event #>> '{context,phase}' IS DISTINCT FROM 'result' THEN
        RETURN NEW;
    END IF;
    IF NEW.event #>> '{extra,role}' IS NULL THEN
        RETURN NEW;
    END IF;
    IF nextval('public.tack_test_catalog_retry_sequence'::regclass) <= 2 THEN
        RAISE EXCEPTION 'forced catalog snapshot rejection' USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER tack_test_catalog_retry
BEFORE INSERT ON public.ops_outbox
FOR EACH ROW EXECUTE FUNCTION public.tack_test_catalog_retry_failure('%s');
