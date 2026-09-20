# Search projection metadata rollout plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Give every property definition an explicit search decision before the first OpenSearch rebuild.

**Architecture:** Stored metadata remains the only runtime authority. Seeds and QA data write complete declarations for new definitions. An audited, expiring command applies a complete manifest reviewed by an operator to existing definitions without inference or overwrite.

**Tech Stack:** Go, FoundationDB, existing metadata repositories, `clispec`, audit outbox, and QA data generation.

**Spec:** [Searchable content](../specs/2026-09-19-search-design.md#searchable-content).

## Global constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). A property definition must explicitly include or exclude search. Do not derive search behavior from its identifier, property type, options, applicability, or FDB `Indexed` flag. Do not use product seeds as runtime configuration.

## Task: Roll out explicit search projections

Ticket: TACK-542.

Modify:

```text
internal/domain/node/types.go                              declaration types
internal/service/seed.go                                  new-org convenience
internal/datagen/property_defs.go                         generated declarations
internal/ops/cli.go                                       command registration
internal/audit                                             audited mutation verb
```

Create:

```text
internal/ops/cli_search_projection_backfill.go            expiring operation
internal/ops/search_projection_backfill.go                bounded manifest apply
internal/test/integration/search_projection_backfill_test.go real FDB proof
```

Add `Search *SearchProjection` to `PropertyDef`. Metadata writes reject nil after the rollout code is active. `Include:false` records an intentional exclusion. `Include:true` requires a valid order and text rule.

- [ ] Add explicit declarations to every built-in seed definition. Update existing seed tests to prove every definition has one. Treat this as new-org convenience only.
- [ ] Add explicit included and excluded declarations to every QA data definition. Search for included text and prove excluded text is absent through the public QA checks.
- [ ] Add a sorted JSON manifest containing organization ID, property-definition ID, and the complete desired projection. Decode it incrementally. Reject duplicate, unknown, cross-organization, malformed, and conflicting entries.
- [ ] Register `search-projections` on `opsGroup` with this literal lifetime:

```go
clispec.Lifetime{
    Ticket: "TACK-542",
    RemoveBy: time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC),
}
```

The rendered command is `ops backfill once-search-projections`. Reuse the global execute gate, audit choke point, result sink, and build expiration check. Do not add a separate confirmation or runtime expiration system.

- [ ] Make dry run read definitions in bounded order and require one manifest entry for every definition that lacked a declaration when the manifest was prepared. Report each planned identity and bounded totals. Make no metadata, epoch, scan, or audit writes.
- [ ] Apply bounded batches through FoundationDB transactions. Set a projection only when it is nil. Accept an already equal value during retry. Return a conflict for an already different value. Bump the organization projection epoch and coalesce one search rescan event for every changed organization.
- [ ] Make a partial failure safe to rerun with the same manifest. Never load all organizations, definitions, or report entries into memory. Record applied identities through the existing audit outbox, including partial completion before returning an error.
- [ ] Add a readiness check used by search provisioning, verification, and the first rebuild. It reports bounded missing identities and refuses success until every definition has an explicit declaration.
- [ ] Test an incomplete manifest, duplicate entry, unknown definition, concurrent explicit update, partial failure, exact rerun, dry run, included and excluded seeds, generated metadata, and the zero-missing readiness gate with real FoundationDB.
- [ ] Run the focused integration test and `make check`. Commit with subject `Backfill explicit search projection metadata` through the signed procedure.

Run the command in QA and production before each environment's first OpenSearch rebuild. After both environments pass the zero-missing gate and the release evidence is recorded, delete the command, its integration test, and its audit verb before the literal removal date. Keep the permanent declaration validation, seed data, QA data, and readiness check.
