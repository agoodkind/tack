# Implement and adopt the Tack OpenTofu provider

Implement TACK-568 through TACK-573 under TACK-567 using the [provider requirements](../specs/2026-10-10-tack-provider.md). Complete each task's observable verification before its dependent task. Production delivery after QA is authorized without another approval pause.

## 1. Establish the implementation evidence for TACK-568

This task has no implementation dependency.

1. Coordinate source ownership and deployment windows with the hardening, search, migration, and MWAN provider owners. Record the exact shared transport and environment-lock interfaces in the extended ledger. Preserve the hardening owner's lock repair and release behavior.
2. Fetch before every comparison, diff, merge-base calculation, or history range. Use remote-tracking references. Use a suitable existing checkout or isolated worktree without switching the main checkout.
3. Run `make build` on the fetched implementation base before editing. Record the result and exact revisions. Resolve a failing base before provider coding.
4. Inspect [cmd/server](../../../cmd/server), [cmd/audit-consumer](../../../cmd/audit-consumer), and the [internal/clispec registry](../../../internal/clispec). Record the actual command-registration symbols and compatibility boundaries.
5. Inspect the existing real container/native integration runner. Record its exact invocation and dependency setup before adding tests. Do not invent a test target.
6. Ask the migration owner to determine the existing Configs workspace placement and database ownership from live reads. Record the exact workspace directory, resource addresses, cluster identities, previous writers, and infrastructure dependencies.
7. Maintain the extended ledger with ticket status, source ownership, dependencies, commands, results, and unresolved evidence. After every compaction, reread the ledger, relevant memories, response and writing rules, documentation, plans, and relevant Clyde context.

Verification must establish a passing unedited build, supported authenticated transport, usable lock integration, and exact adoption targets. Missing database decomposition must delay database implementation without blocking application work.

## 2. Connect the pure model and provider executable for TACK-568

This task depends on task 1.

1. Create [internal/deploymodel/model.go](../../../internal/deploymodel/model.go), [internal/deploymodel/validate.go](../../../internal/deploymodel/validate.go), and [internal/deploymodel/normalize.go](../../../internal/deploymodel/normalize.go). These files are new. Add new `ResourceIdentity`, `OperationRequest`, `OperationResult`, `ServiceSpec`, `ValidateService`, and `NormalizeService` symbols.
2. Define versioned identity encoding, typed request/result envelopes, stable collection normalization, credential references, observed generations, and sanitized errors. Keep FoundationDB bindings outside the pure package dependency graph.
3. Create [cmd/terraform-provider-tack/main.go](../../../cmd/terraform-provider-tack/main.go) and [internal/tfprovider/provider.go](../../../internal/tfprovider/provider.go). These files are new. Add new `main`, `Provider`, and `New` symbols for protocol 6 startup and configuration validation.
4. Connect the executable to the model within this task. Register only resources with working production operations. Do not commit unused constructors or placeholder resources for later connection.
5. Preserve existing server and Tack command entry points. Record further executable extraction as a recommendation without making extraction a dependency.

Verification must exercise provider startup, configuration diagnostics, cancellation, and shutdown through the installed executable. Run `make build` before committing.

## 3. Implement audited service lifecycle operations for TACK-569

This task depends on task 2.

1. Create [internal/ops/cli_provider.go](../../../internal/ops/cli_provider.go), [internal/ops/provider_read.go](../../../internal/ops/provider_read.go), and [internal/ops/provider_apply.go](../../../internal/ops/provider_apply.go). These files are new. Add new `providerReadOp`, `providerApplyOp`, `readProviderResource`, and `applyProviderResource` symbols.
2. Register separate read and mutation commands through the existing registry. Implement typed JSON input and output with resource identity, operation ID, observed generation, operator identity, and audit correlation.
3. Inspect [internal/ops/provision.go](../../../internal/ops/provision.go) and [internal/ops/deploy_verify.go](../../../internal/ops/deploy_verify.go). Reuse applicable production operations through narrow changes. Do not call `provisionRun` from Read or treat deferred `provisionSeed` success as readiness.
4. Create [internal/tfprovider/resource_service.go](../../../internal/tfprovider/resource_service.go). This file is new. Add new `serviceResource` lifecycle methods for Create, Read, Update, Delete, and ImportState. Implement both supported workload kinds.
5. Resolve image digests before mutation. Inspect effective configuration and actual images after apply. Verify manifest and platform image relationships without equating unrelated digest types.
6. Integrate the verified pveguest execution capability using argument arrays and structured JSON. Acquire the existing environment deployment lock only for mutation. Reject stale generations and resume interrupted changes from observed progress.

Verification must use real containers to prove explicit lifecycle behavior, live configuration drift, read-only import, zero plan mutations, and data-preserving service deletion.

## 4. Implement audit roles for TACK-569

This task depends on task 3.

1. Inspect [internal/ops/audit_seed_roles.go](../../../internal/ops/audit_seed_roles.go). Record the existing authenticated role operations before modifying them.
2. Create [internal/tfprovider/resource_audit_role.go](../../../internal/tfprovider/resource_audit_role.go). This file is new. Add new `auditRoleResource` lifecycle methods and connect role requests to the shared operation implementation.
3. Observe login existence and effective grants through authenticated reads. Resolve credentials by reference. Require an explicit credential-version change for rotation.
4. Implement stable role import and dependency checks before revocation or deletion. Preserve audit records and sanitize database diagnostics.

Verification must inspect real database logins and grants after apply, import, drift repair, rotation, and deletion. An authorization failure must return a diagnostic without deleting the role from state.

## 5. Publish and verify the installable provider for TACK-569

This task depends on tasks 3 and 4.

1. Update [Makefile](../../../Makefile) to build the provider executable through the existing build gates. Inspect [Dockerfile](../../../Dockerfile), [Dockerfile.audit-consumer](../../../Dockerfile.audit-consumer), and [.github/workflows/build-push.yml](../../../.github/workflows/build-push.yml) for shared release inputs. Preserve application image behavior.
2. Create [.github/workflows/provider-release.yml](../../../.github/workflows/provider-release.yml). This file is new. Publish versioned installation artifacts, checksums, installation metadata, and complete required native runtime dependencies.
3. Create [internal/test/integration/tack_provider_release_test.go](../../../internal/test/integration/tack_provider_release_test.go). This file is new. Add new `TestTackProviderInstalledRelease` through the existing real container/native runner.
4. Install the published artifact through the migration owner's selected installation mechanism. Run actual `tofu init`, `tofu validate`, `tofu plan`, `tofu import`, and `tofu apply` in the disposable test configuration. Use recorded resource addresses and import identities.

Verification must prove artifact usability on each supported release platform. Require successful provider startup and shutdown with the published dependencies. Do not weaken validation or claim unsupported static builds.

## 6. Adopt QA application and consumer resources for TACK-570

This task depends on task 5.

1. Inspect the Configs [deployment playbook](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-tack.yml), [environment template](https://github.com/agoodkind/configs/blob/main/tack/tack.env.j2), and [compose override template](https://github.com/agoodkind/configs/blob/main/tack/docker-compose.override.yml.j2). Inventory the exact application and consumer fields, configuration renderers, start operations, and restart handlers.
2. Add declarations in the existing workspace selected by the migration owner. Use infrastructure outputs and secret references. Do not invent a workspace path.
3. Import QA application and consumer identities without mutation. Reconcile declarations until `tofu plan` reports no changes.
4. Acquire the shared QA environment lock. Disable the previous writer for each transferred field and container before provider mutation. Modify only verified transferred responsibilities in the inspected Configs files.
5. Apply a deliberate image or configuration change through the shared deployment interface. Run a second apply and a no-change plan. Verify the live image, effective configuration, health, and audit records.

Verification must prove that subsequent Ansible deployment and restart handlers cannot overwrite transferred resources. Application adoption must proceed independently of search activation benchmarks.

## 7. Implement database, search, and backup resources for TACK-571

This task depends on task 6 and the live ownership inventory from task 1.

1. Finalize database resource decomposition with the migration owner. Add exact proposed file paths, new symbols, import identities, field ownership, and lifecycle preconditions to this plan and ledger before coding. Implement typed Tack store operations with infrastructure composition. Never configure-new an imported cluster.
2. Create [internal/tfprovider/resource_search_cluster.go](../../../internal/tfprovider/resource_search_cluster.go), [internal/tfprovider/resource_search_index.go](../../../internal/tfprovider/resource_search_index.go), and [internal/tfprovider/resource_backup_policy.go](../../../internal/tfprovider/resource_backup_policy.go). These files are new. Add new `searchClusterResource`, `searchIndexResource`, and `backupPolicyResource` lifecycle methods.
3. Extend the pure model and shared operations with new `SearchClusterSpec`, `SearchIndexSpec`, and `BackupPolicySpec` symbols. Connect each resource to its production implementation in the task that introduces it.
4. Inspect [internal/ops/cli_search_provision.go](../../../internal/ops/cli_search_provision.go), including `searchProvisionOp`. Separate index provisioning from verified activation where required. Preserve rebuild journals, revisions, aliases, visibility, and point-in-time pagination.
5. Implement backup scheduling and target observation without treating schedule installation as restore proof. Preserve existing backups during policy deletion.
6. Import database, search, and backup resources read-only. Reconcile each declaration to a no-change plan. Transfer each previous writer under the environment lock before mutation.

Verification must prove live membership and settings, observed rebuild progress, failed-activation recovery, effective backup configuration, and preserved database contents. Rehearse destructive preconditions with disposable dependencies.

## 8. Complete public integration coverage and ownership transfer for TACK-572

This task depends on task 7.

1. Create [internal/test/integration/tack_provider_lifecycle_test.go](../../../internal/test/integration/tack_provider_lifecycle_test.go), [internal/test/integration/tack_provider_recovery_test.go](../../../internal/test/integration/tack_provider_recovery_test.go), and [internal/test/integration/tack_provider_lock_test.go](../../../internal/test/integration/tack_provider_lock_test.go). These files are new. Add new `TestTackProviderLifecycle`, `TestTackProviderPartialRecovery`, and `TestTackProviderEnvironmentContention` tests.
2. Enter through installed OpenTofu commands and authenticated public operations. Use real containers, databases, storage, and transport. Do not use mocks, recorded responses, or static implementation-mirroring tests.
3. Prove no-change planning, drift detection, idempotent second apply, schema upgrades, null and unknown handling, sanitized diagnostics, and mutation audit correlation. Compare live generations, content, and audit records before and after planning to prove zero mutations.
4. Interrupt applies before and after committed changes. Resume from live progress. Disconnect transport and deny authorization during refresh. Verify state retention and explicit errors.
5. Contend Ansible and provider mutations within one environment. Require serialization. Run QA, production, and PowerEdge test requests independently and verify progress without a global deployment lock.
6. Complete the remaining Configs ownership removals using the recorded field inventory. Verify ordinary deployment operations cannot recreate or overwrite provider-managed resources.

Run the existing integration runner and `make build`. Require passing observable outcomes before committing.

## 9. Verify search activation independently

This task depends on the search implementation and acceptance prerequisites. It does not block unrelated provider adoption.

1. Have the search owner update the authoritative [search acceptance document](../specs/2026-09-19-search-acceptance.md). Preserve its existing correctness, visibility, pagination, recovery, and capacity contracts.
2. Reuse the mixed-load machinery from `make test-search-host-group GROUP=G2`. Record its existing session/update structure and its unchanged-versus-updated node distinction. Do not present that command as an absolute production-capacity measurement.
3. Measure a search-only baseline and sustained simultaneous public ingestion and retrieval at offered search rates of 1,000, 3,000, and 5,000 per minute. Record the corpus, write rate, query mix, duration, and concurrency before testing. Use existing documented performance thresholds. Report measurements when no numerical threshold exists.
4. Exercise continuous create/edit/delete, bulk imports, lexical and semantic query variation, large and small documents, tenant permissions, pagination and replay across two processes, rebuilds, maintenance, bursts, backlog recovery, and isolated outages.
5. Search for unique revision markers in newly created and edited content during ingestion. Verify removal and permission revocation through searches and continuation replay while indexing work remains queued. Measure a bulk import together with ordinary edits and sustained queries. Continue measuring after the import stops until the backlog drains.
6. Count offered and completed searches separately from ingestion. Record p50, p95, p99, errors, unique revision sentinel freshness, backlog age, CPU, memory, and disk. Record exact revisions, images, configuration, model inputs, and corpus.
7. Fail correctness for unauthorized or deleted results. Activate only after rebuild completion and required verification.

The search owner must execute the mixed-load tests. Existing production rollout must continue independently of provider delivery.

## 10. Adopt production and complete delivery for TACK-573

This task depends on successful QA adoption, applicable resource acceptance, and completed ownership transfer. Search activation must additionally satisfy task 9.

1. Import each production resource read-only and reconcile its configuration to a no-change plan. Verify data, cluster identity, mounted volumes, and live generations.
2. Disable each previous writer under the production environment lock before mutation. Apply through the existing shared deployment interface. Do not create a private applier or release retry loop.
3. Verify live images, configuration, health, database state, search state, backups, and audit records. Require an idempotent second apply and a final no-change plan.
4. Use subagent-driven-development for bounded implementation and specification/code reviews. Coordinate shared files explicitly. Do not change `pr-review-agent`.
5. Require Claude implementers to generate newly written prose through bounded ephemeral `gpt-6.1-sol` CLI executions with the response and writing rules verbatim. Set a hard timeout and require successful completion. Codex may write prose directly. Do not run retrospective prose audits.
6. Create logical signed commits with `git commit -S` and the applicable harness coauthor trailer. Before rebasing, confirm `rebase.gpgSign=true`. After fetching, verify every commit in `origin/main..HEAD` using signature verification and the raw `gpgsig` header before pushing.
7. Use the pr skill for concrete previews. Follow the active GitHub ruleset for review and merge requirements. Record installed-release and live deployment evidence separately from merged-source evidence.

Prerequisites are verification checks rather than additional approval pauses. Production adoption must preserve data and existing operational contracts.
