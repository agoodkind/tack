CREATE TRIGGER part_restore_stale_inject AFTER INSERT ON public.ops_outbox
FOR EACH ROW EXECUTE FUNCTION public.part_restore_stale_inject()
