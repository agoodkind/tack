# Address Index Design Decision

Status: draft. Author role: decision subagent. Read-only against the codebase. The recommendation in section 6 is the load-bearing output. Everything else is the supporting analysis behind it.

The document is meant to be readable end to end without re-reading the source. The citations point at the specific files and line ranges that drove each conclusion, so a reviewer can verify any single claim without re-deriving the rest of the analysis.

---

## 1. Question

Does Tack's `address_index` FDB key family stay global, become fully org-scoped, or take a hybrid shape where the org slug is global and everything below the org is scoped to its parent? The 2026-05-09 incident showed that the current global single-slot design is incompatible with multi-tenancy, and the recovery path inside that incident was only safe because there is currently one production tenant.

The decision needs to be made before any second tenant is introduced. Once a second tenant exists, the choice is forced by whichever cross-tenant collision lands first, and the team would be making the call under outage pressure rather than ahead of time.

The decision has to cover six concrete things in one pass:

1. The chosen FDB key shape for `address_index`.
2. The migration path off the current global rows.
3. Whether to add a reverse index keyed by node ID.
4. The bootstrap path for resolving the first tenant reference when no orgID is in hand.
5. The test plan that proves the chosen shape would not have produced the 2026-05-09 outage.
6. The open questions that the operator has to answer before the migration is scheduled.

---

## 2. Current state with citations

The implemented key family lives in `internal/adapters/foundationdb/keys.go`. The relevant constant is `keyAddressIndex = "address_index"` declared at `keys.go:46`, and the only constructor is `addressIndexKey(nodeType string, addressKind node.AddressKind, address string)` at `keys.go:140-142`. The packed tuple is `(address_index, nodeType, addressKind, address)` and the value is the raw 16-byte UUID. There is no `orgID` component, no `scopeID` component, and no reverse-index constructor anywhere in the file.

The other keys in `keys.go` show the contrast. `nodeInstanceKey` at `keys.go:110-112` packs orgID first. `nodeViewKey` at `keys.go:114-117` packs orgID first. `nodeByPropertyKey` at `keys.go:124-127` packs orgID first. `nodeResolveKey` at `keys.go:119-122` is intentionally global because it has to be resolvable without an orgID, by design. The `address_index` family is the only product-data key family that is global without an explicit bootstrap justification, and that asymmetry is the structural source of the multi-tenant problem.

The store-level accessors live in `internal/adapters/foundationdb/node_address.go`. `GetAddress` at `node_address.go:17-61` reads the global key and returns either a UUID or `uuid.Nil`. `WriteAddress` at `node_address.go:64-106` reads the existing slot, refuses to overwrite a different owner with `domain.ErrAlreadyExists`, and otherwise writes the new owner. `DeleteAddress` at `node_address.go:144-173` clears the slot.

The conflict gate is `ensureAddressOwner` at `node_address.go:108-141`. The conflict log line `node.address.owner.conflict` is the only signal that two callers tried to claim the same address, and that signal does not distinguish between same-tenant rename collisions and cross-tenant collisions because the key has no orgID component to compare.

The service layer over the store is in `internal/service/node_address.go`. `writeReferenceAddress` at lines 27-39 derives the address value from a NodeType's `Reference.DirectAddressProperty()` and writes one slot per `(typeKey, primary, address)`. `reconcileReferenceAddress` at lines 41-65 deletes the old slot and writes the new one when the property changes. The service never passes orgID into these calls because the store API does not accept one.

The reference contract is in `internal/domain/node/reference.go`. `AddressKindPrimary` is defined at line 12. `IsDirectAddress` at lines 18-20 and `DirectAddressProperty` at lines 23-28 declare which property gates address-index materialization. The NodeType struct at `internal/domain/node/types.go:122-153` carries the `Reference ReferenceConfig` field that selects the strategy.

Three node types currently declare `FeatureHasAddress`: `org`, `workspace`, and `project`, declared in `internal/service/seed.go` at lines 254-264 and used in the type spec table at `seed.go:271-283`. Issues use `ReferenceScopedSequence`, states and labels use `ReferenceScopedProperty`, and comments and activities use `ReferenceUUIDOnly`. None of those strategies write into `address_index`. The address-index decision affects only the three address-bearing types, and that small surface area is what makes the migration tractable.

The MCP bootstrap path is in `internal/adapters/mcp/tools/resolve.go`. `Workspace` at lines 95-112 is the entry-point lookup and is the only call site that resolves an address with no parent context. It uses `r.entryPointTypeKey`, which is whichever NodeType carries `FeatureIsEntryPoint`, and today that is `workspace` only. Scope resolution under a parent goes through `resolveScopeReference` at `internal/adapters/mcp/tools/resolve_scope_reference.go:13-30`, which routes direct-property strategies to `resolveDirectNodeUnderParent` at lines 84-102. That path uses `nodes.ListByProperty` against the `node_by_property` secondary index, which is org-scoped at `keys.go:124-127`.

So the `address_index` is consulted only at the entry-point step. Once a workspace is in hand, every deeper scope resolution is already org-scoped through `ListByProperty`. That observation is load-bearing for sections 4, 5, and 10: the bootstrap problem the address index has to solve is "how do I find a top-level node when I do not yet know which org it belongs to," and the answer for everything below the top level is already in place through other infrastructure.

Seed is in `cmd/server/seed.go`. The `ensureNode` helper at lines 153-235 calls `s.Nodes.GetAddress(ctx, typeKey, node.AddressKindPrimary, slug)` at line 155 to decide whether to create or reuse, then calls `s.Nodes.WriteAddress` at line 229 after a successful create.

The 2026-05-09 incident landed exactly in this path. The seed query against `address_index` returned `uuid.Nil` for `(workspace, primary, "main")` because the legacy data lived under `slug_index` and the seed code did not consult that family. Seed then created a parallel org and workspace, derived deterministic UUIDs from the slug, wrote a fresh `node_resolve` record, and called `WriteAddress`. The `WriteAddress` call did not conflict because the slot was empty, and the new IDs claimed `goodkind-io` and `main` globally. The legacy IDs were orphaned at the address layer.

The incident retro at `incident_2026-05-09_seed_parallel_org/retro_log.md` section 1 documents the user-visible failure, and section 1B documents the architectural drift between the implemented global key and the documented org-scoped key.

The backfill that produced the current production rows lives in `internal/ops/backfill_addresses_plan.go` and `internal/ops/backfill_addresses_preview.go`. The candidate computation at `backfill_addresses_plan.go:85-126` keeps an `OrgID` field on each candidate target (line 95 and lines 142-143) by reading it from the owner node's `node_resolve` row. The actual write at `backfill_addresses_preview.go:96-102` calls `WriteAddress` with no orgID because the store API does not accept one. The `OrgID` on the candidate target is only used in the JSON report. The retro log section 9 records that this backfill wrote 9 rows on 2026-05-09 during the forward-fix and that production currently holds those 9 global rows.

Two corollaries follow from the current state. First, the `address_index` is small. There are 9 known rows after the forward-fix, plus whatever seed has written since then on the single tenant in production. Migration cost in row terms is tiny. Second, there is no reverse index. Any "what addresses point at this node" question has to be answered today by a full prefix scan of `address_index`, which is small enough to scan but only because the system has one tenant.

The OrgID derivation rule is also part of the current state and matters for sections 4, 5, and 7. `node.OrgID(slug)` at `internal/domain/node/types.go:288-290` is implemented as `uuid.NewSHA1(orgNamespace, []byte(address))`. Two tenants with the same slug produce the same orgID. That property is intentional for cross-wipe determinism per the comment in `cmd/server/seed.go:163-178`, but it interacts with the address-index design in a way that has to be addressed explicitly in any scoped option.

---

## 3. Option A: Stay global

Option A keeps the key as `(address_index, nodeType, addressKind, address) -> nodeID`. No migration. No reverse index unless we add one separately. The store API stays the same. The service API stays the same. The seed path stays the same.

### Strengths

The strengths of this option are real but narrow.

The resolver code is the simplest of the three options because the entry-point lookup at `tools/resolve.go:96` does not have to derive an orgID before the address read. The MCP handler does not have to walk the user's orgs to disambiguate a workspace slug.

The bootstrap problem does not exist, because there is no scope component in the key. An org slug is resolved the same way any other address is resolved, with no special path.

The index footprint is the smallest of the three options because every address takes exactly one row regardless of how many tenants exist.

The current backfill rows do not need to move. The 9 rows that landed on 2026-05-09 stay where they are. The team's recent migration experience does not have to repeat.

### Weaknesses

Multi-tenancy is broken at the address layer in a way that is structural, not policy. If two tenants both seed a workspace named `main`, the second seed's `WriteAddress` call rejects with `domain.ErrAlreadyExists` (per `node_address.go:108-141`), and seed currently treats that error as a fatal error in `cmd/server/seed.go:230-232`. That is the mechanical outcome.

The semantic outcome is worse than the mechanical one. Any feature that lets a tenant rename a workspace, project, or org slug to a value already owned by another tenant fails with the same generic "address already owned" error. Tenants would observe each other through the conflict messages. The retro log at section 1B captures this point at lines 210 to 218.

A second cost is that today's incident class becomes harder to make impossible. The 2026-05-09 outage was not a cross-tenant collision in the usual sense. It was a same-tenant ghost: the legacy data was in `slug_index` and the new code only consulted `address_index`. The address index design did not cause the outage by itself. It enabled the outage by giving the new seed a single empty global slot to claim.

Under any scoped design, the slot the seed could have claimed would have been keyed on the new org's UUID, which is itself derived from the slug. The legacy org's UUID is also derived from the same slug through `node.OrgID(slug)` at `internal/domain/node/types.go:288-290`. The orgIDs would be identical, which means the seed-claimed slot and the legacy slot would actually collide on the same key. That is the bootstrap problem in a different shape, and sections 4 and 5 below address it.

A third cost is operational. The retro log section 1B at lines 274-277 notes that diagnostics for "which addresses point at this node" require a full scan today. The product goal in `AGENTS.md` is to be a horizontally scalable multi-tenant platform, and a full scan at scale is not viable. Adding a reverse index later is possible but it has the same migration shape as moving to a scoped key, so the timing argument for "do it later" does not actually save work.

### Verdict on Option A

Option A is defensible only on the assumption that Tack will never have a second tenant. That assumption directly contradicts the architecture statement in `AGENTS.md` ("Multiple orgs, each with multiple workspaces, teams, and users", "Multi-tenant from the start", "Org is the tenancy root").

It is also operationally fragile: there is no enforcement anywhere that a second tenant cannot be admitted. The first time anyone runs seed against a fresh slug, the system silently moves into broken territory. Picking Option A would require either accepting the contradiction with the stated architecture or adding an explicit "single tenant" guard at the auth and seed layers that refuses additional orgs entirely. Neither is consistent with the product goals.

---

## 4. Option B: Fully org-scoped

Option B re-keys the index as `(address_index, orgID, nodeType, addressKind, address) -> nodeID`. Every address is scoped to its owning org. The store API gains an orgID parameter. The service-layer callers have to pass an orgID, which they already have because they are inside a NodeType context that knows its OrgID.

The MCP entry-point lookup at `tools/resolve.go:96` no longer works as written, because it has no orgID at the moment of the call.

### Strengths

The strengths are exactly the multi-tenant story. Two tenants can both have a workspace named `main` because the keys are different. Cross-tenant collisions become structurally impossible.

The architectural principle that orgID is the tenancy root is preserved at the address layer the way it already is at every other product-data layer. The mismatch between the address index and the rest of the key space disappears.

The backfill that landed the 9 rows on 2026-05-09 happens to know the correct orgID per row, because it reads it from `node_resolve` per `backfill_addresses_plan.go:142-143`. The migration data is on hand without an extra discovery step.

### Weaknesses

The bootstrap problem is the cost. The org slug itself has to be resolvable without an orgID, because that resolution is what produces the orgID.

The most direct way to handle that is a separate global key family, which would look something like `(org_address_index, addressKind, address) -> orgID`. The `org` NodeType would write into this family on create and only this family. Every other NodeType would write into the scoped `address_index` family. That makes Option B effectively isomorphic to Option C, just framed differently.

If the entry point stays as `workspace`, the resolver has to walk one more step: take the workspace reference, find every org the user belongs to (`members.ListOrgIDsForUser`, already used in `WorkspacesForUser` at `tools/resolve.go:141-161`), and probe `(orgID, workspace, primary, ref)` under each. That step is cheap because the auth gate already enumerates the user's orgs.

A second cost is that this option introduces two key families where there was one. The retro log section 1B argues at lines 222-235 that the org-scoped design is the documented intent, and the small additional complexity is in line with the rest of the system, where every product key already carries an orgID prefix. The data-model purity argument at the bottom of `AGENTS.md` ("everything is a node") still holds: orgs are nodes, but the address index is metadata, and metadata has always required scope hints (`node_type_def` is org-scoped per `keys.go:150-152`, `property_def` is org-scoped per `keys.go:154-157`). One more org-scoped family fits the existing pattern.

A third cost is migration. The 9 production rows have to be rewritten to include the orgID, which is a stop-the-world write of 9 transactions. The migration is small in row count but requires a deploy that ships the new key shape and a backfill operation that reads the old global rows, derives the orgID from `node_resolve` per row, writes the new scoped row, and clears the old global row in the same FDB transaction. Section 7 below covers this in detail. The risk is the same shape as the wave 1 slug-to-address migration that triggered the 2026-05-09 incident, so the runbook from that wave is the relevant template for what to avoid this time.

### The OrgID determinism interaction

The bootstrap design point that needs explicit handling is the OrgID determinism. `node.OrgID(slug)` at `types.go:288-290` derives the org UUID from the slug alone using a SHA-1 namespace. Two tenants with the same desired slug produce the same UUID. That is a separate invariant violation from the address index, but it interacts directly with this option, because if the org address index is global and keyed on slug, two tenants with the same slug also produce the same orgID.

The org-address slot would conflict on the slug, but the orgID would be a deterministic match for both, which means whoever wrote first wins and the second tenant's identity is forever entangled with the first. Either the OrgID derivation has to incorporate a random salt or a tenant-allocator step, or the `org_address_index` has to enforce that the slug is globally unique. These are separable problems, and the operator has to pick one. Section 12 records this as an open question.

### Verdict on Option B

Option B works. It is structurally close to Option C. The reason to prefer C over B is that C names the bootstrap family explicitly and makes the special case for org bootstrapping visible in the code shape, where B leaves it implicit. Either of these flows into the same migration plan.

---

## 5. Option C: Hybrid

Option C makes the org slug global and everything below the org scoped to its parent. The two key families look like this:

```
org_address_index    (org_address_index, addressKind, address) -> orgID bytes
address_index        (address_index, orgID, nodeType, addressKind, address) -> nodeID bytes
```

The org NodeType writes into `org_address_index` only. Every other NodeType with `FeatureHasAddress` (today: workspace and project) writes into the scoped `address_index`. Reads go to the family appropriate to the NodeType. The store API gains a NodeType parameter that already exists, plus an orgID parameter for the non-org calls.

### Strengths

The strengths are the union of the multi-tenant property of Option B and the no-bootstrap-problem property of Option A.

Two tenants can both have a workspace `main` and a project `TACK` because those addresses are scoped under different orgIDs.

The org slug remains globally unique because there is only one global namespace for orgs, and that is consistent with the actual reality that orgs are the tenancy root and are addressable from outside the system (URLs, MCP entry points, user-typed slugs).

The MCP entry-point lookup needs no extra step if the operator chooses to make `org` the entry point. If `workspace` stays as the entry point, the resolver walks one extra step the same way Option B does, but it now reads `org_address_index` first to convert the org slug into orgID, then reads scoped `address_index` for the workspace. That two-step walk is cheap and well-suited to the cache-per-user pattern the MCP server already uses at `internal/adapters/mcp/server.go:25-50`.

### Weaknesses

The cost is two families, the same as Option B's bootstrap-shaped cost. The naming convention has to make it obvious which family a given lookup goes into; section 8 below covers the reverse index decision under the same constraint.

There is one additional cost specific to Option C: the org-slug-vs-OrgID-determinism question still exists. Two tenants seeded with the same desired org slug under Option C produce the same SHA-1 derived orgID through `node.OrgID(slug)`, so the global `org_address_index` slot collision and the deterministic-UUID collision are the same event.

That is an improvement over Option A's silent overwrite but still leaves the second-tenant operator with a "your slug is taken" error and no recovery path. The fix for that is the same as Option B: change the OrgID derivation to incorporate a fresh entropy source, or make global slug claim an explicit allocator step. Section 12 records the open question.

### Alignment with existing intent

The hybrid shape is also closer to the documentation the retro log section 1B at lines 237-242 suggests. The CLAUDE.md design predates the implementation drift; the hybrid shape captures the original intent (orgID prefix on most things, special bootstrap path for the org itself) without the unimplemented `node_address` family name and without the unimplemented reverse index.

The hybrid shape also matches the way the rest of the resolver is written. Scope resolution under a parent already uses an org-scoped read through `ListByProperty`. Making the address index org-scoped for everything-below-org puts the address layer on the same footing.

---

## 6. Recommendation

The recommendation is Option C, the hybrid shape, with the org slug in a dedicated global `org_address_index` family and all other addresses scoped under their owning orgID in a re-keyed `address_index` family. The recommendation comes with a tightly-coupled fix for the deterministic-UUID derivation in `node.OrgID(slug)` so that the global org-slug claim and the org-UUID derivation cannot diverge, plus an explicit reverse index decision in section 8.

### Why Option C over A

Option A cannot satisfy the multi-tenant property the system is supposed to have. The retro log section 1B already established this and the analysis in section 3 above traces it to the structural single-slot key shape. Choosing A means choosing single-tenancy.

### Why Option C over B

Options B and C are structurally close. The difference is whether the bootstrap family is named explicitly (C) or treated as a special case inside the same family (B). Naming the bootstrap family explicitly makes the code easier to reason about, makes the migration easier to validate, and makes future audits of the address layer more direct. The retro log section 1B at line 281 already lists the hybrid as the defensible target.

### Why now

The address index is currently small. The forward-fix of 2026-05-09 wrote 9 rows. Even after a year of single-tenant production, the address index is going to be on the order of hundreds of rows at most, because the only NodeTypes that have `FeatureHasAddress` are `org`, `workspace`, and `project`.

A migration of that size is rehearseable in QA (per the QA-environment requirement in retro log section 1C) and applicable to production in a single transaction window of a few seconds. Once a second tenant lands, the migration is no longer that small, because every additional address-bearing node multiplies the row count. The window where this migration is cheap is now.

### Scope of the recommendation

The recommendation does not extend `FeatureHasAddress` to additional NodeTypes today. Issues use `ReferenceScopedSequence`, states and labels use `ReferenceScopedProperty`, and comments and activities use `ReferenceUUIDOnly`, all per `internal/service/seed.go:265-282`. Those strategies do not write into `address_index` at all. The address-index decision affects only the three address-bearing types.

The recommendation also does not change the semantics of `ReferenceScopedSequence` or `ReferenceScopedProperty`. Those strategies use `ListByProperty`, which is already org-scoped, and continue to work without modification.

---

## 7. Migration plan from current global to the recommended shape

The migration has six distinct steps. Each step is independently revertible up to the point where the new shape is read-write live. The plan assumes the QA environment from retro log section 1C is operational; if it is not, the migration cannot be safely scheduled because the rehearsal step is the load-bearing safety net.

### Step 1: New key constructors

Add `orgAddressIndexKey(addressKind, address)` and rewrite `addressIndexKey` to accept an `orgID` parameter. Both constructors live in `internal/adapters/foundationdb/keys.go`. The current `addressIndexKey` at lines 140 to 142 stays in place under a renamed name like `legacyAddressIndexKey` so the migration code can read the old rows.

The new shapes are:

- `(org_address_index, addressKind, address)` for orgs.
- `(address_index, orgID, nodeType, addressKind, address)` for everything else.

### Step 2: Store API

Add an `orgID uuid.UUID` parameter to `GetAddress`, `WriteAddress`, and `DeleteAddress` in `internal/adapters/foundationdb/node_address.go`. The org NodeType branch routes to the org family with no orgID; all other NodeTypes route to the scoped family with the orgID.

The branching logic lives in the store, not in the callers, so the callers always pass orgID and the store decides which key to write. The decision can be data-driven: the store receives the NodeType definition and reads `Features.Has(node.FeatureHasAddress)` plus a NodeType-key check for `org`. A cleaner alternative is a new feature `FeatureIsOrgScopeRoot` that the org type carries; the operator can choose either approach.

### Step 3: Service-layer callers

Update `internal/service/node_address.go` lines 27 to 65 to pass the orgID it has on the NodeType into the store calls.

Update `cmd/server/seed.go:155` and `:229` to pass orgID. The seed path constructs the org first; for org creation itself, orgID equals the new node's own ID, which is `node.OrgID(slug)` (and see step 6 for the salt fix).

Update `internal/test/integration/setup.go:151` similarly.

Update `internal/adapters/mcp/tools/resolve.go:96` so the entry-point workspace lookup walks orgs first; the simplest implementation is to enumerate the user's orgs through `members.ListOrgIDsForUser`, probe each one for the workspace reference, and return the first match. The cache in `internal/adapters/mcp/server.go` already keys per user, so the per-request cost is one extra org-list read on a cache miss.

### Step 4: Backfill operation

Add a new ops command `backfill.addresses.scope` to `internal/ops/`. The command reads every existing row in the legacy global `address_index` family using the same `scanPrefix` shape as `queryLegacyAddressRows` in `internal/adapters/foundationdb/inspect_query.go:152-176`. For each row, it resolves the owner node through `node_resolve` (already a global key, so this works with no orgID context), determines the owner's NodeType, and writes the new row in the appropriate family inside the same FDB transaction that clears the old row.

Conflict handling: if the new row already exists with a different owner, the migration row is logged as a conflict and skipped, and the old global row stays put. With 9 known rows in production today, no conflict is expected. The conflict path is still required because QA tests will exercise it.

### Step 5: Deploy and rehearse

Build the new binary with the dual-read fallback enabled: `GetAddress` reads the new family first and falls back to the legacy global family if the new row is absent. This is the wave-1 mistake from 2026-05-09 in reverse. In that wave, the new code only read the new family and missed the legacy data. Here, the new code reads both during the transition window.

Deploy to QA first, run the backfill, run the integration tests, and seed a synthetic second tenant in QA to confirm that the cross-tenant collision case is now structurally prevented. Only after QA is green does production get the deploy.

### Step 6: OrgID derivation fix

This is where Option C either works or does not. Today `node.OrgID(slug)` at `types.go:288-290` is `uuid.NewSHA1(orgNamespace, []byte(address))`. Two tenants with the same slug produce the same UUID, which collapses Option C's tenant isolation back to the same single-slot story Option A has, just with extra steps.

The fix is to change the seed-time OrgID derivation to use UUIDv7 (k-sortable, time-and-randomness-derived, already the default for non-root nodes per `cmd/server/seed.go:177`) and store the slug claim separately in `org_address_index` as the canonical claim.

The deterministic-UUID property of the current derivation is used in two places per `cmd/server/seed.go:163-178`: stable IDs across wipes for NodeType and PropertyDef. Both of those usages downstream from OrgID can be made stable by other means, including a per-org seed config that pins the orgID, or a tenant-allocator service that records the orgID once and replays it. The loss of cross-wipe determinism is acceptable in exchange for tenant isolation.

Section 12 records the operator question on whether the determinism is load-bearing for any other workflow.

### Rollback path

The rollback path is the dual-read fallback from step 5. If anything goes wrong after the production deploy, the operator reverts the binary to the previous version. The previous version reads only the legacy global family. The legacy rows are still in place because the backfill writes the new family in the same transaction that clears each legacy row, so a partial backfill leaves legacy rows for un-backfilled entries and new rows for backfilled entries; the previous binary does not see the new rows and falls back to the legacy rows correctly.

Once the backfill is complete, the legacy rows are gone, and a rollback would require running an inverse backfill. The decision of when to remove the dual-read fallback from the new binary is the point of no return; the recommendation is to remove it only after a full backup-and-restore cycle has been validated against the new shape, which depends on the backup-system rebuild from retro log section 1A.

---

## 8. Reverse index decision

The decision is to add a reverse index in this migration, named `node_address_by_node`, keyed as `(node_address_by_node, nodeID, addressKind, address) -> nil`, and written in the same transaction as every `address_index` (or `org_address_index`) write. The value can be empty because the existence of the row is the signal.

The reverse index is global (keyed by nodeID alone, no orgID prefix) for the same reason `node_resolve` is global at `keys.go:119-122`: any caller holding a nodeID needs to be able to ask "what addresses point at this node" without knowing the org first.

### Use cases the reverse index serves

The use cases are real and recurring.

First, audit and inspection: "given this nodeID, what is its canonical reference?" is asked every time the operator inspects a node during incident response, and today that question requires a full prefix scan.

Second, rename safety: when a workspace's slug changes through `reconcileReferenceAddress` at `internal/service/node_address.go:41-65`, the service code needs to know the old slug to clear the right slot, and today it relies on the caller passing the old props correctly. With a reverse index, the service can verify that the address-side cleanup matches the property-side change.

Third, deletion safety: when a node is deleted, every address pointing at it has to be cleared. Today this depends on the caller knowing which addresses exist. With a reverse index, the deletion path can enumerate them.

### Cost

The cost is one extra FDB key write per address change, which is two writes per WriteAddress and one extra clear per DeleteAddress. The transaction count does not change because the existing accessors already wrap the address write in a transaction.

The index size doubles in absolute terms (forward and reverse), which on the current 9-row footprint is 18 rows. At scale, the size is still bounded by the number of addresses, which is bounded by the number of address-bearing nodes, which is small relative to total nodes.

### Why not defer

The alternative is to defer the reverse index. The retro log section 1B at lines 288-289 lists "implement the missing `node_address_by_node` reverse index, or remove it from CLAUDE.md if the target design no longer includes it."

Deferring works only if the audit-and-rename use cases can be answered another way. They cannot today: the address index has no organizational structure that supports a reverse lookup other than full scan, and full scan does not scale.

Adding the reverse index later has the same migration shape as the forward-shape change, so the cost of adding it now versus later is roughly the same write-side cost and roughly the same deploy cost. Deferring has the extra cost of a second migration window. The recommendation is to do both shape changes in one migration.

---

## 9. Code paths affected

The files and approximate line ranges that change are:

- `internal/adapters/foundationdb/keys.go:46` and `:140-142`. Add the `org_address_index` constant, rewrite `addressIndexKey` to take orgID, add `orgAddressIndexKey`, add `nodeAddressByNodeKey`. Rename the legacy `addressIndexKey` to `legacyAddressIndexKey` for the migration window.

- `internal/adapters/foundationdb/node_address.go:17-173`. Add orgID parameter to all three accessors. Branch on NodeType to route to org family vs scoped family. Add reverse-index writes in the same transactions as `WriteAddress` and `DeleteAddress`. The dual-read fallback for `GetAddress` reads the new family first and the legacy family second during the migration window.

- `internal/domain/node/repository.go:112-117`. Update the interface signatures to accept orgID. The interface change forces every implementation to the new contract, which prevents partial migrations.

- `internal/service/node_address.go:27-65`. Pass the orgID from the NodeType (already a field on NodeType per `types.go:127`) into the store calls.

- `cmd/server/seed.go:155` and `:229`. Pass orgID. For the org create, orgID is the new node's own ID, which sets up the OrgID-derivation question from section 7 step 6.

- `internal/test/integration/setup.go:151`. Pass orgID.

- `internal/adapters/mcp/tools/resolve.go:95-112`. Walk the user's orgs to resolve the entry-point reference. The members lookup at `tools/resolve.go:141-161` already does the right enumeration; refactor to share the logic.

- `internal/ops/backfill_addresses_plan.go:85-126` and `internal/ops/backfill_addresses_preview.go:25-118`. The existing backfill operates on legacy `slug_index` rows. The new migration operates on legacy `address_index` rows. The shape of the operation is similar enough that it can be a sibling file rather than a replacement, named `backfill_addresses_scope.go` or similar.

- `internal/adapters/foundationdb/inspect_query.go:152-176`. Add a new query for legacy global `address_index` rows that mirrors `queryLegacyAddressRows` but reads the post-2026-05-09 global family. The migration backfill reads through this query.

- `AGENTS.md` (the file currently named `CLAUDE.md` in the working tree). Update the "FDB key space" section to reflect the chosen shape and remove the documented-but-unimplemented `node_address` family. Update the "deprecated names in older artifacts" section to record the global-to-scoped transition.

The migration introduces one new feature flag environment variable, `TACK_ADDRESS_INDEX_DUAL_READ`, defaulting to true during the transition window and removed in the post-migration deploy. The flag is the rollback handle.

---

## 10. Bootstrap path walkthrough

The walkthrough covers the case the operator actually hits in production: a fresh MCP request from a known user, asking for a workspace by slug, with no orgID anywhere in the request. The user is authenticated through the bearer token (`Authorization: Bearer ...`) per `AGENTS.md` "Auth" section. The auth middleware resolves the token to a userID and stamps it on the request context. From there, the MCP handler at `internal/adapters/mcp/server.go:84-100` takes over.

### Step 1: User-to-orgs lookup

The MCP handler already calls `members.ListOrgIDsForUser` for the workspace-listing path at `tools/resolve.go:141-161`. The bootstrap path runs the same lookup. The result is a slice of orgIDs that this user is a member of. In production today there is one org per user; in the multi-tenant target, there could be several.

### Step 2: Org-slug resolution if the request specified an org

If the MCP request includes an org reference (the post-migration MCP can add this as an optional input), the handler reads `org_address_index` with the address kind and the slug and gets back the canonical orgID. If the request does not include an org reference, the handler uses the slice from step 1.

### Step 3: Workspace lookup

For each candidate orgID (one if step 2 ran, the user's full slice if it did not), the handler reads `address_index` with `(orgID, "workspace", "primary", slug)`. The first match wins.

If multiple matches return for a single user and slug across different orgs, the handler returns an ambiguous-reference error and asks the caller to specify an org. That case is rare in practice but matters for correctness.

### Step 4: Auth gate

The orgID from step 3 is checked against `org_members` in YugabyteDB through the existing auth pattern at `AGENTS.md` "Per-entity auth". The check is structurally redundant with step 1 because the orgID came out of the user's own membership list, but it remains in place as defense in depth.

### Properties of the walkthrough

The walkthrough has two important properties.

First, it works without a special second-class lookup path for the org slug. The org-slug lookup is a normal read from a normal global key family that exists for exactly this purpose.

Second, it works without violating the multi-tenant property. Two tenants can both have a workspace `main`, and the user only sees the one in their own org because step 3 reads the scoped family under their own orgID.

### Subtlety: not-found vs wrong-tenant

The existing `Workspace` resolver at `tools/resolve.go:95-112` returns `domain.ErrNotFound` when the address is missing. In the multi-tenant world, the same call could find the address in a different org that the user is not a member of, and it has to return `ErrNotFound` rather than the wrong workspace.

Step 3 already enforces this because it scopes by the user's orgIDs from step 1, so a workspace owned by an org the user does not belong to is invisible to the resolver. The conflict-message hygiene from Option B section 4 carries forward: the user never sees other tenants' addresses, even in error messages.

---

## 11. Test plan

The test plan has to convince the operator that the new design eliminates the 2026-05-09 failure mode. The failure mode is "seed creates a parallel org and workspace, claims the address slot, orphans the legacy data, and the system silently routes new traffic to the empty parallel records."

The plan has five layers, each closer to production reality than the last.

### Layer 1: store-level unit tests

Add tests in `internal/adapters/foundationdb/node_address_test.go` that verify:

- A write to the org family does not create a row in the scoped family.
- A write to the scoped family does not create a row in the org family.
- A read for the same slug under two different orgIDs returns two different nodeIDs.
- A write that conflicts on the scoped family rejects with `domain.ErrAlreadyExists`.
- The reverse index gains a row on every forward write and loses it on every delete.
- The dual-read fallback reads new before legacy.

### Layer 2: integration tests against a real FDB cluster

Use the existing tests in `internal/test/integration/`. Seed two synthetic tenants whose orgs both want a workspace named `main`. Both seeds succeed. Both tenants observe their own workspace through the entry-point lookup. Neither tenant can resolve the other's nodes by reference. Delete one tenant's workspace and confirm that the other tenant's lookup is unaffected.

### Layer 3: regression test for the 2026-05-09 incident

Construct an FDB state that mimics the pre-incident production state: a populated legacy `slug_index` family for an existing org and workspace, an empty new `address_index` family, and a `node_resolve` row pointing at the legacy IDs.

Run the seed code against the same slug. Assert that seed sees the legacy entry through the dual-read fallback or through the explicit `slug_index` consultation that should be added per retro log section 1B's required follow-ups. Assert that no parallel org or workspace is created. The seed should reuse the legacy IDs.

### Layer 4: scoped-collision regression test

Seed two synthetic tenants in QA with slugs that are different at the org level but identical at the workspace level (`acme-org` with workspace `main`, `umbra-org` with workspace `main`).

Confirm that both seeds succeed without conflict. Probe each tenant's workspace through MCP and confirm the right workspace returns for each user. Confirm the conflict log line `node.address.owner.conflict` from `node_address.go:134-140` does not fire for either seed.

### Layer 5: operational rehearsal

Run the migration in QA against a production-shape data copy. The QA environment from retro log section 1C is required for this; if it is not in place, the migration cannot be exercised at this layer.

Capture before-and-after key counts in the legacy and new families. Confirm that every legacy global row is either rewritten or accounted for as a conflict. Confirm that the reverse index has exactly the same row count as the forward index. Capture the timing of the migration so the production maintenance window can be sized correctly. Run the integration tests post-migration to confirm the system is in the expected state.

### Exit condition

The exit condition for the migration is all five test layers green in QA, plus a verified backup of the production FDB cluster taken with the new backup mechanism (per retro log section 1A) before the production migration runs.

---

## 12. Open questions for the operator

The list below is the set of decisions the operator owns. The migration cannot be scheduled until each one has an answer.

### Q1: OrgID determinism

Is `node.OrgID(slug)` at `internal/domain/node/types.go:288-290` allowed to lose its determinism property in exchange for tenant isolation? The current code derives orgID from slug alone, which means two tenants with the same desired slug produce the same orgID, which collapses Option C back to single-tenant behavior.

The fix is to make orgID independent of slug (UUIDv7 at seed time, recorded once in `org_address_index` and never regenerated), but the seed code at `cmd/server/seed.go:163-178` relies on the determinism to keep NodeType and PropertyDef IDs stable across wipes.

The operator has to decide whether the cross-wipe determinism is load-bearing for any downstream workflow (MCP config caches, downstream integrations, search index identity). If it is, the migration has to add a tenant-allocator step that records and replays the orgID, which is more work than just dropping the determinism.

### Q2: Org slug uniqueness

Should the org slug remain a globally unique address, or should multiple tenants be allowed to register the same human-readable org slug?

Option C as recommended assumes globally unique org slugs because the org slot is the bootstrap entry point, which means two tenants cannot both pick `acme`. That is the convention in every comparable product (Linear, Jira Cloud, GitHub orgs all enforce a global unique slug).

It is also a constraint that has to be communicated to anyone seeding a new tenant, because the existing seed code does not have a "your slug is taken" branch that produces a clean operator-facing message. The alternative is to introduce a second layer of indirection where the human-typed org reference is a tenant-generated slug under a tenant-allocator namespace, which is more flexible but is also a substantial product change.

The recommendation defaults to globally unique org slugs unless the operator says otherwise.

### Q3: Entry point identity

Should the MCP entry point remain `workspace` (the current `FeatureIsEntryPoint` declaration in `internal/service/seed.go:255`) or move to `org`?

The bootstrap walkthrough in section 10 works either way, but the walk is simpler if `org` is the entry point because step 2 becomes the entry-point lookup directly.

The product question is whether MCP callers want to address by `workspace_reference` alone (today's behavior) or by `org_reference + workspace_reference` (the post-migration multi-tenant behavior). The recommendation defers this to the operator because the answer depends on the MCP UX direction.

### Q4: Migration window shape

Does the migration run as a scheduled maintenance window, or as a hot transition with the dual-read fallback live for a defined period?

The dual-read fallback in section 7 step 5 enables the hot path. The maintenance-window path is simpler to reason about and matches the team's experience with the wave 1 migration. The hot path is closer to production-grade and matches the architectural goal of horizontal scaling.

The operator's preference depends on whether the QA environment can rehearse a hot transition convincingly.

### Q5: Sequencing relative to other workstreams

What is the timing of this migration relative to the audit-table fix in retro log section 1D and the backup-system rebuild in retro log section 1A?

Both of those are also required follow-ups from the same incident. The address-index migration should happen after the backup system is verified end to end, because the migration is the kind of change that requires a verified pre-migration backup. The audit-table fix is independent and can happen on either side.

The operator has to sequence these three workstreams.

### Q6: Synthetic second tenant identity

Who owns the QA second-tenant seeding, and what synthetic identity convention does it use?

Retro log section 1C at line 410-412 lists this as an open follow-up. The address-index migration cannot be safely tested in QA without a deterministic synthetic second tenant identity that is obviously not a real customer. The operator has to pick the synthetic identity convention before the migration is scheduled.

### Q7: Reverse index exposure

Should the reverse index from section 8 expose its addresses through a new MCP tool, or stay an internal-only diagnostic?

The recommendation defers an MCP tool until at least one downstream caller asks for it, but the internal use cases (audit, rename safety, deletion safety) are enough to justify writing the index in this migration regardless of the MCP-tool decision.
