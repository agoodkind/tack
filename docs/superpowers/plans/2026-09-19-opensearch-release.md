# Search Removal and OpenSearch Release Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Release Meilisearch removal first, then provision and activate OpenSearch only after the replacement passes real validation.

**Architecture:** Phase A removes the unused search stack and leaves `tack_search` explicitly unavailable while every other operation continues. Phase B later creates an empty OpenSearch index, rebuilds only from FoundationDB, validates QA, repeats the procedure in production, then enables ranked search. The phases share no search data or compatibility path.

**Tech Stack:** Existing configs deployment entry points, OpenSearch operator commands, public MCP checks, and host telemetry.

**Spec:** [Evidence and environment](../specs/2026-09-19-search-acceptance.md#evidence-and-environment).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Obtain separate authorization for each merge, infrastructure apply, and deployment. Never disable a branch rule or rewrite shared history. Do not read or convert Meilisearch data. Do not delete its old data volume unless a later request authorizes that exact deletion.

## Review Focus

Phase A proves the outage contract and complete active-service removal. Phase B proves the applied revision, running image, model identity, TLS, topology, FoundationDB-only rebuild, backlog recovery, public behavior, and capacity.

---

### Phase A: Release Meilisearch removal

- [ ] **Present the removal release for authorization.** Show the reviewed Tack and Configs commits, exact deploy targets, service removals, application changes, expected public response, and proof that the old data volume is absent from the deletion plan.
- [ ] **Merge through the protected pull-request path.** Record the merged commits. Do not use a ruleset bypass, ruleset suspension, force push, or branch-history rewrite.
- [ ] **Apply the approved configuration removal.** Remove active Meilisearch service, proxy, credential injection, monitoring, and inventory entries from QA and production. Preserve the existing data volume or disk.
- [ ] **Deploy the approved Tack removal release.** Record each environment's deployed revision and image digest.
- [ ] **Verify the public outage contract.** Call `tack_search` with an ordinary query, an exact reference, filters, invalid input, and omitted input. Require exactly `Search is temporarily unavailable.` every time.
- [ ] **Verify the rest of Tack.** Create, edit, read, and delete nodes through MCP. Exercise every representative non-search tool. Require FoundationDB and SQL writes to succeed.
- [ ] **Verify active Meilisearch removal.** Inspect running containers, processes, application environment, proxy routes, credentials, startup logs, and outbound requests. Require no active service or application dependency. Record the preserved old volume separately without reading it.

Search remains unavailable after Phase A. Update `origin/main` to these merged commits before creating the Tack Graphite stack. OpenSearch implementation and configuration start only from this released state.

### Phase B: Provision and activate OpenSearch

- [ ] **Require completed implementation evidence.** Require the final validation plan's signed Tack Graphite stack, independent Configs pull request, every real-dependency result, exact image digests, corrected failures, and clean worktrees.
- [ ] **Merge the validated implementation.** Obtain separate authorization. Merge the Tack stack bottom to top through Graphite. Merge the independent Configs pull request through its protected pull-request path. Record every merged commit. Do not bypass branch rules or rewrite shared history.
- [ ] **Present concrete QA changes for authorization.** Show the saved OpenTofu plan, one suburban guest, one stable proxy endpoint, rendered application values, secrets referenced by name, and exact apply and deployment commands.
- [ ] **Provision one empty QA node.** Apply only the approved suburban plan. Start the pinned OpenSearch 3.8.0 container and deploy the QA proxy. Verify listener address, client certificate, backend certificate, authenticated health, caller restrictions, one backend, 8 GiB memory, two CPU cores, 40 GiB fast storage, and a 2 GiB JVM heap.
- [ ] **Complete QA projection metadata.** Generate the complete manifest. Run the expiring backfill in dry-run mode. Review and apply that exact manifest. Rerun it and require zero changes and zero missing declarations.
- [ ] **Create and rebuild the QA index.** Run `ops search provision` against an empty physical index. Run the full rebuild only from FoundationDB. Include source mutations committed during the outage and rebuild. Verify the pinned model, mapping, one primary, reserved routing shards, zero replicas, alias target, and green health.
- [ ] **Activate the QA public handler.** Review and commit the single rendered change from `OPENSEARCH_PUBLIC_ENABLED=false` to `true`. Deploy the exact validated Tack revision and that Configs commit. Record the deployed revision and image digest. Run `ops search verify` and guarded QA datagen.
- [ ] **Validate QA behavior.** Require semantic relevance, final-page text, edits, deletion, pre-ranking access filtering, final FoundationDB authorization, replay, and complete continuation. Stop OpenSearch during source writes. Require writes to commit and search to return an unavailable error. Restart it and require backlog recovery and correct search results.
- [ ] **Validate QA capacity.** Record corpus size, page distribution, query mix, concurrency, mutation rate, rebuild activity, and pass thresholds before the run. Measure latency, errors, throughput, oldest work age, memory, and disk. Require capacity for serving, replacement, and retiring indexes. Keep at least 6.26 GiB available on `suburban` throughout the complete workload.
- [ ] **Validate independent Tack scaling.** Run two Tack processes against the same FoundationDB and OpenSearch. Alternate one session between them. Increase request and worker load and require higher throughput without stored-format changes. Do not claim OpenSearch failover from one node.
- [ ] **Present concrete production changes for authorization.** Show the saved vault plan, stable endpoint, exact validated commits, metadata manifest, empty rebuild procedure, activation change, outage behavior, QA measurements, and rollback state.
- [ ] **Provision one empty production node.** Apply only the approved vault plan. Start the pinned container and proxy. Verify TLS, caller restrictions, one normal cluster member, one primary, reserved routing shards, zero replicas, model placement, and stable-endpoint traffic. Do not restore or inspect Meilisearch.
- [ ] **Complete production metadata and rebuild.** Repeat manifest dry run, reviewed apply, idempotent rerun, and zero-missing readiness. Create an empty index and rebuild only from FoundationDB. Keep public search unavailable while validating the alias, model, mapping, work backlog, current access values, memory, disk, and errors.
- [ ] **Activate and verify production.** Review and commit the production rendered change from `OPENSEARCH_PUBLIC_ENABLED=false` to `true`. Deploy the exact validated Tack revision and that Configs commit. Record the revision and image digest. Verify authenticated search, access filters, final authorization, ranking, continuation, pending-work recovery, latency, errors, and source writes during a single-node OpenSearch outage.
- [ ] **Record each release fact separately.** Record infrastructure provisioning, application deployment, public activation, live acceptance, and capacity as separate claims with evidence.

### Later production scale-out

Add production members only after measurements justify them. Join each member to the existing cluster without setting `cluster.initial_cluster_manager_nodes` again. Add healthy members to the stable proxy. Deploy the model on eligible ML nodes. Set one replica only after enough data nodes exist. Use native split only for a permitted primary-count increase. Claim failover only after stopping each of at least three members separately while search and indexing remain available.

The preserved Meilisearch volume is outside this release. A future deletion requires an exact inventory, a concrete deletion plan, and separate authorization.
