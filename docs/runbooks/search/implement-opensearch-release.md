# Release and validate search

Use this procedure for a reviewed search release. An infrastructure apply, application deployment, public activation, source-data repair, and retained-volume deletion have different effects and require their own authorized scope. Preserve authoritative data and audit history throughout the release.

## Prepare the release evidence

1. Record reviewed Tack and Configs commits, required checks, signed commit verification, image digests, exact targets, current ownership, deployment locks, and migration compatibility. Follow the active protected pull-request contract. Do not bypass rules, rewrite shared history, or treat a merged change as a deployment.
2. Require the local [acceptance test procedure](run-acceptance-tests.md) on the implementation revision. Correct failures at their owning layer and repeat affected tests without weakening accepted limits. Record unrun criteria as unverified.
3. Review the current Configs OpenTofu changes. The [release module](https://github.com/agoodkind/configs/blob/main/opentofu/modules/tack-release/main.tf) updates images in an existing installation and runs audited checks. It does not install a missing stack, apply migrations, or create credentials. Confirm a reviewed procedure for every prerequisite that an image-only release cannot supply.
4. Compare proposed infrastructure values with current inventory before saving a plan. Check each value:

   - Guest IDs.
   - IPv6 addresses.
   - MAC addresses.
   - Docker subnets.
   - Bridges.
   - Storage pools.
   - Host assignments.

   Stop on collisions or replacement of an unrelated data guest. An initial one-guest release must not reserve additional production guests.
5. For a new QA guest, check the [Configs guest declaration](https://github.com/agoodkind/configs/blob/main/opentofu/modules/suburban/tack-qa/main.tf) for each setting:

   - Gateway.
   - DNS.
   - Debian template.
   - Unprivileged nesting.
   - Storage discard.
   - `prevent_destroy`.

6. Review the exact saved plan, targets, secret references, and commands before applying authorized changes.
7. Preserve existing Meilisearch volumes without reading them. Their deletion requires an exact inventory, a concrete deletion plan, and separate authorization. Require FoundationDB-only rebuild inputs and verify the absence of active Meilisearch configuration or dependencies separately from application source.

## Apply an image-only release

1. Review the [QA release declaration](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/tack-qa/release.tf). Set its exact Tack commit, server digest, consumer digest, and accountable email through the reviewed Configs change. The owner module updates app and consumer images. The application-only module waits for the owner and updates the second app guest.
2. Confirm that the installation already contains the selected Compose stack, overrides, environment, secret files, cluster files, compatible schema, and required service units. The image-only module changes the image tag. It does not render an `OPENSEARCH_PUBLIC_ENABLED` change or install those prerequisites. Complete a reviewed Configs procedure for missing prerequisites or changed configuration before applying the image-only release.
3. Review the plan in the selected Configs environment and apply only its reviewed saved plan under the release authorization. Inspect the module's pull, service-recreation, health, and digest-verification results in order. Require owner search verification and audit queue inspection to complete. Preserve the module's service identity, accountable operator, commit session, and deployment commit in audit evidence.
4. Verify actual running digests and public behavior after the units finish. A successful plan or unit-file write does not establish that services run the selected release. After QA acceptance, repeat the reviewed change with the [production release declaration](https://github.com/agoodkind/configs/blob/main/opentofu/vault/tack-prod/release.tf) under production authorization.

## Establish QA prerequisites

1. Coordinate the QA window with the guest and deployment owners. Record current membership, migration state, locks, expected images, and ownership. Preserve unrelated source writes and non-search checks.
2. For a fresh or restored environment, follow [Provision an environment](../deployment/provision-environment.md). Use the existing ordered audited provision operation. Never run destructive FoundationDB initialization against configured data. If product seeding defers until the app starts, repeat provisioning and verify completion.
3. Before starting the search container, verify the Configs-declared `vm.max_map_count` inside the search LXC. Verify actual search resources against the reviewed initial topology, including one combined-role QA guest on `suburban`, pinned engine, IPv6-only access, REST and transport TLS, restricted callers, least-privilege application credentials, authenticated readiness, and one proxy backend. Require a certificate authority, credentials, and reviewed primary, routing, and replica counts. Restrict endpoint access to Tack application guests. Protect the proxy's credentials from other users and from logs. Require the proxy to verify backend certificates and use authenticated cluster-health checks. Verify memory, heap, CPU, fast storage, and the host memory gate from the [cluster and capacity criteria](../../reference/search/cluster-capacity-criteria.md#cluster-and-capacity).
4. Complete any reviewed metadata rollout with [Apply missing search projection declarations](implement-opensearch-metadata.md). Preserve intentional exclusions and use a separate manifest for source-data repair.
5. Prepare the required isolated QA cohorts and preserve actual stored IDs for semantic, continuation, and final-page checks. Keep public search disabled until the empty rebuild and prerequisite checks complete.

## Rebuild and activate QA

1. Run audited `ops search provision` against an empty index, then full FoundationDB rebuilding through `ops search reindex`. Include source mutations committed during any outage and rebuilding. Inspect the recorded replacement until it completes.
2. Run `ops search verify` and inspect model identity and eligible predictors, mapping, primary and routing shards, replicas, physical generation, alias, green health, exclusions, warnings, stuck work, and pending work. Classify unresolved exclusions. Do not delete content to force an empty report.
3. Review and commit the QA `OPENSEARCH_PUBLIC_ENABLED` activation through Configs. Deploy the exact validated revision and configuration. Record running app and audit-consumer digests and the search image separately.
4. Run public relevance, final-page, edit, shorter-value, deletion, pre-ranking permission, final FoundationDB authorization, replay, and complete-continuation checks. Run guarded datagen and inspect permanent audit evidence. A successful health check or verification command does not replace these observations.
5. Stop the QA search guest during source writes. Require committed source mutations, explicit public outage, and pending durable work. Restart the guest. Allow five minutes for engine and model readiness, then require the public query to return the node committed during the outage within ten seconds, without another edit or rebuild. Preserve the first public error and distinguish it from later diagnostics.
6. Measure search-only and mixed workloads under every accepted offered rate. Apply the capacity criteria already linked in the QA prerequisites:

   - Declare the workload inputs.
   - Compare each mixed workload with the search-only baseline at the same offered rate.
   - Apply the stopping conditions.
   - Record missing product thresholds explicitly.

   Measure each workload below and preserve its inputs and measurements.

   Workload evidence:

   | Category | Required evidence |
   | --- | --- |
   | Workloads | Bulk import, permission changes, and rebuilding. |
   | Two-process continuation | A continuation between two processes under load. |
   | Outage | An isolated outage. |
   | Catch-up | Catch-up after the outage. |
   | Results | Latency, errors, committed writes, freshness, and work age. |
   | Resources and stopping | Host and guest memory, CPU, disk, and stop telemetry. |
7. Alternate requests between two actual Tack processes. Require exact continuation, replay, current permission checks, and cleanup without sticky routing. Record the independent two-process throughput comparison and its machine limits. A single OpenSearch guest does not establish node failover.

## Validate production independently

1. Present the concrete production configuration and saved plan with QA evidence, exact commits and images, metadata manifest, rebuild procedure, activation change, expected outage behavior, and compatible rollback state. Preserve the applicable authorization and deployment locks.
2. Verify production guest identity, TLS, caller restrictions, one normal cluster member, proxy readiness, topology, and predictor placement. Set initial cluster manager nodes only when forming the first cluster. Remove that setting after formation and use existing members for later discovery. Do not restore another engine's state.
3. Repeat reviewed metadata readiness and any initial manifest apply, then build an empty index only from FoundationDB. Keep public search unavailable while verifying model, mapping, alias, current access values, pending work, errors, memory, and disk.
4. Activate production through the reviewed Configs change and exact validated Tack release. Verify running image digests, authenticated queries, pre-ranking access filtering, current authorization, relevance, continuation, permanent audit records, and outage recovery during authorized source-write checks.
5. Measure production capacity on `vault` with its own workload and baseline. QA measurements do not establish production capacity. Complete the planned recovery drill with source writes and pending search work. An earlier restart does not establish a full recovery drill.
6. Record infrastructure application, installed images, metadata changes, public activation, acceptance, capacity, and cleanup as separate results. Keep unresolved criteria open until their evidence satisfies the contract.

## Recover a failed release

1. Stop the dependent deployment when a prerequisite correction or acceptance check fails. Preserve authoritative data, the exact failure, partial manifests, pending indexing work, and permanent audit evidence.
2. Restore a previously verified release or configuration only when its rollback supports the migrations already applied. Do not use application rollback to undo repaired values or rewrite audit history.
3. Resume durable indexing from recorded state and repeat every affected acceptance criterion. Retained-volume deletion remains outside this recovery procedure.
