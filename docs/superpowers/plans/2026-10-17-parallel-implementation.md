# Parallel node integrity implementation plan

Implement the [node integrity requirements](../specs/2026-10-16-node-integrity.md) through parallel tasks with explicit source ownership. Preserve the [repair mappings](../specs/2026-10-16-node-backfill.md), data repair before strict validation, and QA proof before production.

The inspected baseline is `264759cc67b86ef1f4d3ef35dcb818d1a422efd0`. Recheck current main and open changes before dispatch. The new contracts in the detailed plans are proposed implementation additions, not existing APIs.

## Reuse active implementation

1. Extend [PR 371](https://github.com/agoodkind/tack/pull/371). It already changes generic hierarchy interpretation, parent removal, deletion, search access, and real parent-edge tests. Integrate its useful work before correcting remaining transaction and index-completeness gaps.
2. Extend [PR 370](https://github.com/agoodkind/tack/pull/370). It adds useful error identities and default-child backfill checks. Replace its whole-node projection rejection expectations with valid-content indexing and property warnings.
3. Preserve [PR 369](https://github.com/agoodkind/tack/pull/369) and existing provider work under their current ownership. Follow the [provider contract](../specs/2026-10-10-tack-provider.md) for generic resource reuse and application adoption.
4. Use isolated worktrees. Preserve other changes and integrate current work deliberately. Do not reset an active implementation branch to this planning baseline.

## Dispatch independent outcomes

Assign one owner per active outcome. Each worker receives an exact module or file boundary, its neighboring interfaces, a working directory, verification, and a short report contract.

| Owner | Required outcome | Dependency |
| --- | --- | --- |
| Mutation | Implement checked writes, canonical containment, source expectations, deletion completeness, and shared transaction integration. Extend the existing parent work. | Publish the transaction interfaces and integrate Schema and Audit. |
| Schema | Implement generic applicability, value validation, defaults, and fixed-definition correction. Supply pure validation operations. | Agree on the schema snapshot with Mutation. |
| Audit | Implement actual before/after deltas, attempt-local event construction, stable event identity, and real delivery proof. | Consume transaction source/result snapshots. |
| Projection | Implement readable defaults, structured property diagnostics, current warning state, and verification output. | Consume the Schema interpreter. |
| Migration | Implement versioned digests, renderer activation, full-content rescans, obsolete cursors, and delayed-writer rejection. | Consume Schema dependencies and Projection results. |
| Recovery | Implement operational failure classification, retained work, attempt reporting, and automatic recovery. | Agree on typed outcomes with Projection; otherwise proceed independently. |
| Repair | Implement frozen manifests, durable checkpoints, report preservation, verified export, and the expiring command. | Develop preview and checkpoints now; integrate Mutation, Schema, and Audit before execution. |
| Relevance | Add the production regression, collect traces, and correct the demonstrated retrieval failure. | Investigate independently; coordinate any rendering correction with Projection. |
| Integration | Integrate shared wiring and fixtures, inspect diffs, run full acceptance, and serialize shared QA/deployment operations. | Prepare fixtures and evidence now; verify the combined result later. |
| Deployment | Continue existing application contracts, provider implementation, adoption, and shared guest prerequisites. | Coordinate shared guests and locks; continue independent work during node repairs. |

The [transaction and repair details](2026-10-17-node-transaction-details.md) define Mutation, Schema, Audit, and Repair source ownership and implementation. The [search details](2026-10-17-search-indexing-details.md) define Projection, Migration, Recovery, and Relevance.

Integration owns shared wiring in [the runtime graph](../../../internal/runtime/graph.go), [store construction](../../../internal/adapters/foundationdb/stores.go), [command registration](../../../internal/ops/cli.go), and [ops dependencies](../../../internal/ops/ops.go). It applies new shared search ports and key registrations serially. Workers use separate files for warning, checkpoint, and digest key concerns.

## Establish shared interfaces without stopping independent work

1. Mutation publishes concrete source-expectation, schema-snapshot, parent, audit-builder, and checkpoint interfaces. Projection publishes its content/diagnostic result. Migration publishes version and rescan inputs. Record exact signatures with the implementation change.
2. Other owners can inspect source, author public regressions, and implement their bounded modules concurrently. Resolve these engineering interfaces within the implementation task; do not reopen settled product choices.
3. Keep edits to the same file sequential. One owner applies the coupled patch or combines dependent changes into one compiling PR. Do not publish unused production APIs, placeholders, unchecked alternative writers, or temporary test doubles.
4. Review each report and inspect its diff before integration. Verify required behavior through the production boundary. A report alone does not establish acceptance.
5. Require each worker to preserve other changes and adapt to the current tree. Reuse an existing worker's context when its task still fits the outcome.

## Integrate in dependency order

1. Integrate checked mutation interfaces with working existing callers, source checks, generic parents, and attempt-local audit construction. Reuse the open parent and property-check changes.
2. Integrate shared validation, fixed metadata, and the repair command. Repairs invoke full-result validation; ordinary writes retain existing checks until verified cleanup.
3. Integrate valid-content rendering, warning persistence, and operational retries. Correct conflicting projection tests and readiness checks together.
4. Integrate versioned projection activation and durable rescans. Renderer or metadata changes must schedule affected content even when node values remain unchanged.
5. Integrate the measured relevance correction and reconcile shared fixtures. Preserve the fixed corpus, authorization, continuation, and complete acceptance contract.
6. Execute reviewed repairs on QA, verify the broad scan and safe retries, then activate strict ordinary-write validation. Prove the scan-to-activation boundary against concurrent writes.
7. Follow the [search and deployment sequence](2026-10-16-search-correction.md) for full acceptance and authorized production work. Review the concrete production manifest separately from the source change.

## Verify each outcome through real boundaries

Use the current [Makefile](../../../Makefile). The runner starts real dependencies through [internal/testenv](../../../internal/testenv). Older testing prose about a host-side `TACK_INTEGRATION` gate does not match this baseline.

Run `make build` on the exact unedited implementation base and after a coherent coding task. Run `git diff --check` before committing. Do not change lint baselines or unrelated CI configuration to make this work pass.

For a worker's proposed public node/repair test family, use a focused runner selection:

```sh
make test-integration TEST_STORE_ARGS="-tags integration -count=1 -timeout 30m -v -run '^TestNodeIntegrity' ./internal/test/integration/..."
```

For a worker's proposed projection test family, use a focused search selection:

```sh
make test-search-group GROUP=G5 SEARCH_G5_ARGS="-count=1 -timeout 45m -run '^TestSearchProjection'"
```

Implement the named scenarios and confirm that they execute. A command that selects zero tests does not prove the outcome. Use the relevant named family for other tasks; neither example supplies complete acceptance.

Integration runs `make test-integration` and the full search groups without selection overrides on the combined revision. Run the applicable repeated groups after changes to protected behavior. Preserve all existing acceptance sections and their evidence requirements.

## Reserve shared operations separately

Allow parallel coding and isolated lightweight tests. Reserve the model-backed engine, large corpus, full suites, QA guests, deployment locks, and workload measurements before heavy operations. A blocked reservation must not stop unrelated implementation.

Only the deployment owner changes a shared environment during an agreed window. Coordinate directly with the guest/provider owner. Do not run another worker's environment cleanup or delete its dependencies.

Continue the existing provider and infrastructure work during node corrections. Production adoption still requires import, drift, interrupted-apply, backup, lock, and single-writer evidence. Search activation and one passing query do not establish full acceptance or capacity.

## Require a short evidence report

Each outcome reports its branch/commit, changed modules, observable behavior, exact tests and results, unverified criteria, and the next dependent interface. Distinguish source completion, merge, configuration apply, installed image, repaired data, and runtime acceptance.

Store worker assignment and coordination in the task tracker and private execution state. Repository documents state technical contracts and verification; they must not publish session identifiers or conversation history.
