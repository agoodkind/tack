# Verify search projection corrections after activation

Validate the selected QA environment and audited operator identity. Preserve intentional exclusions. Validate QA before repeating the reviewed procedure in production under its authorization.

1. Preview each required definition correction and projection-version change. Keep the source-data repair manifest separate from the projection manifest.
2. Apply corrections through the transactional metadata-update boundary. Commit the definition, projection state, audit evidence, and affected-content work together. Do not use `SetProjectionIfNil` as a general correction API or expose the migration as a public schema editor.
3. Require affected pages to refresh against the same serving index, unaffected pages to remain intact, and obsolete IDs to retire. Access-only corrections must preserve text and embeddings. Verify each outcome:

   - Explicit exclusions remain intact.
   - Interrupted work recovers.
   - Repeated corrections produce the same result.
   - Current metadata passes readiness checks.
   - Indexing covers every page.
   - Reads and search return consistent content.
4. For an interpreter change, verify that old cursors fail and old pending work resumes under the new renderer even when declarations remain unchanged.
