# Apply missing search projection declarations

The one-time `ops backfill once-search-projections` command fills missing declarations before the first FoundationDB rebuild. It does not update the projection digest or schedule a refresh.

## Prepare the manifest

1. Validate the selected QA environment and audited operator identity. Inventory current property definitions and preserve intentional exclusions. Metadata readiness accepts supported defaults. An absent optional declaration alone does not make valid content unreadable.
2. Create one reviewed JSON array in organization-ID and definition-ID order. Include each definition that lacks a declaration. Each entry supplies `org_id`, `property_def_id`, and a validated `search` rule. The command reads incrementally and rejects duplicate IDs, unknown definitions, cross-organization identities, malformed rules, omitted missing definitions, and conflicting existing declarations.
3. Review the complete manifest before execution. Preserve source identities and explicit exclusions. Do not infer a decision from property names or `Indexed`.

Use complete stored UUIDs in place of the abbreviated IDs in this example. The entry selects scalar text at order 10.

```json
[{"org_id":"018f...","property_def_id":"0190...","search":{"include":true,"order":10,"rule":{"mode":"scalar"}}}]
```

## Validate and apply

1. Run the command without `--execute` using its required `--manifest` input. Require a dry run with bounded planned identities and totals. Require the dry run to leave declarations, projection state, and search work unchanged. The shared operator command must still record the preview.
2. Review the dry-run result and execute that exact manifest through the shared execute and audit path. The command writes each declaration and its audit evidence atomically. Preserve evidence of completed entries if a later entry fails.
3. Repeat the exact manifest after interruption or successful execution. Equal existing declarations count as unchanged. Require a conflict when a concurrent write stored a different declaration. Require an unchanged successful second execution and complete manifest coverage.
4. Run metadata readiness and search verification before the first rebuild. Check invalid declarations separately from definitions that lack a supported default. Validate QA before repeating the reviewed procedure in production under its authorization.

## Remove the one-time command

After QA and production both record complete declaration coverage, remove the command, its one-time integration test, and its audit verb before its registered December 31, 2026 removal date. Preserve permanent validation, defaults, seeds, QA coverage, and readiness. A source change or a configured environment does not prove either environment completed this backfill.
