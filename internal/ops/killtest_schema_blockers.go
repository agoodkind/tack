package ops

// A table owns its indexes, row type, array type of the row type,
// and sequences that its columns own.
// The query lists every other relation, function, and type in killtest.
const killtestSchemaOtherObjectsQuery = `
	WITH target AS (SELECT oid FROM pg_namespace WHERE nspname = $1)
	SELECT 'relation ' || c.relname || ' of kind ' || c.relkind::text
	  FROM pg_class c
	 WHERE c.relnamespace = (SELECT oid FROM target)
	   AND c.relkind NOT IN ('r', 'p')
	   AND NOT EXISTS (SELECT 1 FROM pg_index x WHERE x.indexrelid = c.oid)
	   AND NOT (c.relkind = 'S' AND EXISTS (
	           SELECT 1
	             FROM pg_depend d
	             JOIN pg_class t ON t.oid = d.refobjid
	            WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid
	              AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
	              AND t.relnamespace = c.relnamespace AND t.relkind IN ('r', 'p')))
	UNION ALL
	SELECT 'function ' || p.proname
	  FROM pg_proc p
	 WHERE p.pronamespace = (SELECT oid FROM target)
	UNION ALL
	SELECT 'type ' || t.typname
	  FROM pg_type t
	 WHERE t.typnamespace = (SELECT oid FROM target)
	   AND NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.oid = t.typrelid AND c.relkind <> 'c')
	   AND NOT EXISTS (
	           SELECT 1
	             FROM pg_type element
	             JOIN pg_class c ON c.oid = element.typrelid
	            WHERE element.oid = t.typelem AND c.relkind <> 'c')
	 ORDER BY 1`

// pg_depend records rules, column defaults, triggers, and policies in
// separate catalogs. The query resolves each to its table's schema.
// pg_depend does not record references inside function bodies.
const killtestSchemaDependentsQuery = `
	WITH target AS (SELECT oid FROM pg_namespace WHERE nspname = $1),
	member AS (
	    SELECT 'pg_namespace'::regclass::oid AS classid, oid AS objid FROM target
	    UNION ALL
	    SELECT 'pg_class'::regclass::oid, c.oid FROM pg_class c WHERE c.relnamespace = (SELECT oid FROM target)
	    UNION ALL
	    SELECT 'pg_proc'::regclass::oid, p.oid FROM pg_proc p WHERE p.pronamespace = (SELECT oid FROM target)
	    UNION ALL
	    SELECT 'pg_type'::regclass::oid, t.oid FROM pg_type t WHERE t.typnamespace = (SELECT oid FROM target)
	),
	dependent AS (
	    SELECT d.classid, d.objid, d.refclassid, d.refobjid,
	           COALESCE(
	               (SELECT c.relnamespace FROM pg_class c
	                 WHERE d.classid = 'pg_class'::regclass AND c.oid = d.objid),
	               (SELECT p.pronamespace FROM pg_proc p
	                 WHERE d.classid = 'pg_proc'::regclass AND p.oid = d.objid),
	               (SELECT t.typnamespace FROM pg_type t
	                 WHERE d.classid = 'pg_type'::regclass AND t.oid = d.objid),
	               (SELECT k.connamespace FROM pg_constraint k
	                 WHERE d.classid = 'pg_constraint'::regclass AND k.oid = d.objid),
	               (SELECT c.relnamespace FROM pg_rewrite w JOIN pg_class c ON c.oid = w.ev_class
	                 WHERE d.classid = 'pg_rewrite'::regclass AND w.oid = d.objid),
	               (SELECT c.relnamespace FROM pg_attrdef a JOIN pg_class c ON c.oid = a.adrelid
	                 WHERE d.classid = 'pg_attrdef'::regclass AND a.oid = d.objid),
	               (SELECT c.relnamespace FROM pg_trigger g JOIN pg_class c ON c.oid = g.tgrelid
	                 WHERE d.classid = 'pg_trigger'::regclass AND g.oid = d.objid),
	               (SELECT c.relnamespace FROM pg_policy y JOIN pg_class c ON c.oid = y.polrelid
	                 WHERE d.classid = 'pg_policy'::regclass AND y.oid = d.objid)
	           ) AS namespace
	      FROM pg_depend d
	      JOIN member m ON m.classid = d.refclassid AND m.objid = d.refobjid
	)
	SELECT DISTINCT pg_describe_object(dependent.classid, dependent.objid, 0)
	       || ' depends on ' || pg_describe_object(dependent.refclassid, dependent.refobjid, 0)
	  FROM dependent
	  JOIN pg_namespace n ON n.oid = dependent.namespace
	 WHERE n.nspname IN ('audit', 'partman', 'public')
	 ORDER BY 1`
