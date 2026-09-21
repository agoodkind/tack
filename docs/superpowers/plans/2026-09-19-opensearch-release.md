# OpenSearch QA and Production Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy and verify the single-node QA and production search service, then remove deployed Meilisearch resources.

**Architecture:** Each environment creates an empty OpenSearch index, rebuilds only from FoundationDB, and enables public search after the alias is ready. QA establishes behavior and host capacity. Production repeats the same proof before application cutover.

**Tech Stack:** Existing configs deployment entry points, OpenSearch operator commands, public MCP checks, host telemetry.

**Spec:** [Evidence and environment](../specs/2026-09-19-search-acceptance.md#evidence-and-environment).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Every apply, application cutover, and old-volume deletion requires its own authorization. Never disable a branch rule or rewrite shared history. Do not read or convert Meilisearch data.

## Review Focus

Verify the applied revision, running image, model identity, TLS, topology, backlog recovery, public behavior, capacity, and exact old-resource deletion.

---

### Task 14: Verify QA and production after authorization

**Files:**

- Produce: saved OpenTofu plans and applies in the configs operations record.
- Produce: QA and production search verification reports.
- Modify after successful cutover: deployed configs inventory, secrets, services, and volumes that exist only for Meilisearch.

**Interfaces:**

- Consumes: reviewed Tack and configs commits, completed Task 13 evidence, `ops search provision`, `ops search verify`, `ops backfill once-search-projections`, and `datagen.VerifySearch`.
- Produces: separate provisioning, deployment, live acceptance, and cleanup evidence.

- [ ] **Step 1: Present concrete QA changes for authorization.**

Show the exact reviewed commits, saved OpenTofu plan, one guest addition, one proxy listener, rendered endpoint, secrets referenced by name, and commands that will apply them. Wait for authorization before applying.

- [ ] **Step 2: Provision QA without changing the application path.**

Apply the approved suburban plan. Start one empty OpenSearch guest. Deploy the QA hypervisor proxy.

Run: `./configsctl deploy deploy-proxmox --limit suburban`

Verify listener address, client certificate, backend certificate, authenticated readiness, source restrictions, and one backend.

- [ ] **Step 3: Complete the projection manifest and empty rebuild.**

Generate the complete QA manifest. Run the expiring backfill in dry-run mode. Review and apply that exact manifest. Rerun it and require zero changes. Require zero missing declarations. Provision the model and an empty index, then rebuild only from FoundationDB.

- [ ] **Step 4: Deploy the QA application cutover.**

Run: `./configsctl deploy deploy-tack --limit tack_qa_all`

Record the deployed Tack revision and active image digest. Run `ops search verify` and guarded QA datagen. Verify public semantic relevance, final-page text, edit, deletion, access filtering, corrupt-index authorization, and complete continuation.

- [ ] **Step 5: Prove QA outage recovery.**

Stop the search guest during source writes. Require FDB commits and an explicit search-unavailable error. Restart the guest. Require authenticated readiness, model deployment, durable backlog recovery, and public search.

- [ ] **Step 6: Run the declared QA capacity workload.**

Record corpus size, page distribution, query mix, concurrency, mutation rate, rebuild activity, and pass thresholds before the run. Measure latency, errors, throughput, oldest work age, memory, and disk. Require capacity for serving, replacement, and retiring indexes. Keep at least 6.26 GiB suburban host memory available throughout.

- [ ] **Step 7: Prove Tack and FoundationDB scale independently.**

Run two Tack processes against the same FoundationDB and OpenSearch. Alternate one session between them. Increase request and worker load. Add FoundationDB capacity independently and require higher throughput without stored-format changes. Do not claim OpenSearch failover or horizontal scale from single-node QA.

- [ ] **Step 8: Verify QA contains no deployed Meilisearch path.**

Inspect processes, containers, environment, secrets, inventory, volumes, and application requests. Require no active Meilisearch dependency. Present the exact old volume deletion separately. Delete it only after authorization.

- [ ] **Step 9: Present concrete production changes for authorization.**

Show the exact production guest, proxy, application commits, empty rebuild procedure, expected outage behavior, capacity measurement, and old-resource cleanup. Wait for authorization before applying.

- [ ] **Step 10: Provision and verify production before cutover.**

Apply the approved vault plan. Deploy the proxy.

Run: `./configsctl deploy deploy-proxmox --limit vault`

Keep the application on its old configuration. Require one primary, zero replicas, reserved routing shards, verified TLS, model placement, and direct search traffic through the stable endpoint. Stop and restart the search node while source writes commit. Require backlog recovery.

- [ ] **Step 11: Complete the production manifest and rebuild.**

Repeat dry run, reviewed manifest apply, idempotent rerun, and zero-missing readiness. Create an empty index and rebuild only from FoundationDB. Do not inspect Meilisearch documents, settings, results, or volume.

- [ ] **Step 12: Deploy and verify the production application cutover.**

Run: `./configsctl deploy deploy-tack --limit tack_prod_all`

Record the deployed revision and image. Verify public authenticated search, access filters, final authorization, ranking, continuation, pending-work recovery, memory, disk, latency, and errors. Record provisioning, deployment, and live acceptance as separate facts.

- [ ] **Step 13: Remove deployed Meilisearch resources after authorization.**

Verify the production application no longer references the service. Present the exact secret, service, inventory, and volume deletion. Apply only the authorized deletion. Verify no live service, endpoint, credential, volume, dependency, or fallback remains.

- [ ] **Step 14: Preserve the later production scale-out procedure.**

Before adding members, review the measured bottleneck and concrete guest plan. Join members through the existing cluster without `cluster.initial_cluster_manager_nodes`. Add healthy backends to the stable proxy. Deploy the model on eligible ML nodes. Set replicas after enough data nodes exist. Use native split only for a permitted primary-count increase. Claim failover only after stopping each of at least three members separately while search and indexing remain available.
