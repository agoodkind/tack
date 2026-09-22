# Search Projection Metadata Rollout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every property definition an explicit search decision before the first OpenSearch rebuild.

**Architecture:** Stored metadata remains the runtime authority. New seeds and QA data write complete declarations. An audited expiring command applies one reviewed manifest to existing definitions without inference or overwrite. This initial rollout runs before the index pipeline exists and schedules no search work.

**Tech Stack:** Go, FoundationDB, existing metadata repositories, `clispec`, audit outbox.

**Spec:** [Searchable content](../specs/2026-09-19-search-design.md#searchable-content).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Every property definition explicitly includes or excludes search. Do not derive the decision from identifier, property type, options, applicability, or the FDB `Indexed` flag.

## Review Focus

Test incomplete manifests, duplicate identities, cross-organization entries, concurrent explicit updates, partial failure, exact rerun, and zero-missing readiness.

---

### Task 1: Roll out explicit search projections

**Files:**

- Modify: `internal/domain/node/types.go`
- Create: `internal/domain/node/search_projection.go`
- Modify: `internal/service/seed.go`
- Modify: `internal/datagen/property_defs.go`
- Create: `internal/ops/cli_search_projection_backfill.go`
- Create: `internal/ops/search_projection_backfill.go`
- Modify: `internal/ops/search_provision.go`
- Modify: `internal/ops/search_verify.go`
- Modify: `internal/audit/verbs.go`
- Create: `internal/service/seed_search_test.go`
- Create: `internal/datagen/property_defs_search_test.go`
- Test: `internal/test/integration/search_projection_backfill_test.go`

**Interfaces:**

- This plan uses the existing property definition and audited operation machinery.
- This plan implements complete stored declarations and `RequireSearchProjections(context.Context) error` for provisioning and rebuild.

```go
type TextRule struct {
    Mode string `json:"mode"`
    Fields []TextField `json:"fields,omitempty"`
    Items *TextRule `json:"items,omitempty"`
    Labels map[string]string `json:"labels,omitempty"`
}
type TextField struct { Key string `json:"key"`; Rule TextRule `json:"rule"` }
type SearchProjection struct { Include bool `json:"include"`; Order int `json:"order"`; Rule TextRule `json:"rule"` }
type ProjectionManifestEntry struct {
    OrgID uuid.UUID `json:"org_id"`
    PropertyDefID uuid.UUID `json:"property_def_id"`
    Search node.SearchProjection `json:"search"`
}
type ProjectionBackfillResult struct { Scanned, Changed, Unchanged, Missing int }
```

- [ ] **Step 1: Add the failing completeness and rerun test.**

```go
func TestSearchProjectionBackfill(t *testing.T) {
    fixture := newProjectionBackfillFixture(t)
    manifest := fixture.ManifestForEveryMissingDefinition()
    first, err := fixture.Run(t.Context(), manifest, false)
    if err != nil { t.Fatal(err) }
    if first.Changed == 0 || first.Missing != 0 { t.Fatalf("first result: %#v", first) }
    second, err := fixture.Run(t.Context(), manifest, false)
    if err != nil { t.Fatal(err) }
    if second.Changed != 0 || second.Missing != 0 { t.Fatalf("rerun result: %#v", second) }
}
```

- [ ] **Step 2: Record the deferred failure contract.**

The final validation plan runs `^TestSearchProjectionBackfill$` against the completed branch. The test must fail when the manifest command, retry rules, or permanent declaration validation is absent. Do not start FoundationDB during this coding task.

- [ ] **Step 3: Add declaration types and explicit values to new metadata.**

Add `Search *SearchProjection` to `PropertyDef`. Validate known modes, item rules, unique object fields, label maps, and maximum recursion depth. Add included and excluded projections to every built-in seed and QA definition. Seed tests require one declaration per definition. QA checks search included text and reject excluded text. These values provide new-organization convenience only. Runtime code reads stored metadata.

- [ ] **Step 4: Decode and validate the manifest incrementally.**

Read a sorted JSON array through `json.Decoder`. Reject malformed entries, duplicate property-definition IDs, unknown IDs, organization mismatches, invalid rules, and omitted definitions that were nil when the manifest was prepared. Do not load all organizations or definitions into memory.

```json
[{"org_id":"018f...","property_def_id":"0190...","search":{"include":true,"order":10,"rule":{"mode":"scalar"}}}]
```

- [ ] **Step 5: Register the exact expiring audited command.**

```go
clispec.Lifetime{Ticket: "TACK-542", RemoveBy: time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC)}
```

Register `ops backfill once-search-projections` through the existing execute gate, result sink, audit path, and build expiration check. Do not add another confirmation or expiration system.

- [ ] **Step 6: Implement a nonmutating dry run.**

Read definitions in bounded ID order. Require one manifest entry for each nil declaration. Report planned identities and bounded totals. Write no projection, epoch, scan event, or audit record.

- [ ] **Step 7: Apply retry-safe bounded batches.**

Set a projection only when stored `Search` is nil. Accept an already equal value. Return a conflict for an already different value. Do not create a projection epoch or search rescan event. The index pipeline does not exist during this initial rollout, and the first FoundationDB rebuild reads the completed declarations. Record applied identities through the audit outbox, including completed batches before a later error.

- [ ] **Step 8: Add the permanent readiness gate.**

`RequireSearchProjections` scans definitions in bounded order, reports bounded missing identities, and refuses success until every definition has a declaration. Wire it into the registered provision and verify operations in this task. The later rebuild operation also calls it before target creation.

- [ ] **Step 9: Add failure coverage.**

Test incomplete, duplicate, unknown, cross-organization, malformed, and conflicting entries. Inject a partial failure and rerun the exact manifest. Test included and excluded seed and QA definitions. Require dry run to leave all key families unchanged. Require execution to leave projection-epoch and search-work key families unchanged.

- [ ] **Step 10: Run the serial coding checks.**

Run: `make build`

Expected: PASS. The final validation plan runs the backfill and readiness checks.

- [ ] **Step 11: Create the next Graphite slice.**

```sh
git add internal/domain/node/types.go internal/domain/node/search_projection.go internal/service/seed.go internal/service/seed_search_test.go internal/datagen/property_defs.go internal/datagen/property_defs_search_test.go internal/ops/cli_search_projection_backfill.go internal/ops/search_projection_backfill.go internal/ops/search_provision.go internal/ops/search_verify.go internal/audit/verbs.go internal/test/integration/search_projection_backfill_test.go
```

Run Graphite MCP `create` from stack position 1 with this exact message:

```text
Backfill explicit search projection metadata

Co-authored-by: Codex <noreply@openai.com>
```

This branch is stack position 2.

After QA and production both record zero missing declarations, delete the command, its integration test, and its audit verb before the removal date. Keep permanent validation, seeds, QA data, and the readiness gate.
