# Search Cluster Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prepare three search guests in QA and three in production, then verify the approved release.

**Architecture:** Configs provisions guests, networking, credentials, and certificates. Tack defines the containers and search setup. QA must pass the complete acceptance suite before production deployment.

**Tech Stack:** OpenTofu, Proxmox LXC, Ansible, Docker Compose, OpenSearch 3.8.0, and official OpenSearch Go client v4.7.3.

**Spec:** [Deployment and capacity](../specs/2026-09-19-search-design.md#deployment-and-capacity).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Each environment has `tack-search1`, `tack-search2`, and `tack-search3`, each with at least 8 GiB memory, 2 CPU cores, 40 GiB storage, and a 2 GiB JVM heap. QA runs on suburban; production runs on vault. This task prepares changes and evidence; deployment requires separate authorization.

Final rendered and live environments contain no Meilisearch service, endpoint,
secret, volume, dependency, or fallback. FoundationDB supplies every initial
OpenSearch rebuild. No deployment operation reads or converts the Meilisearch index.

Task 11 prepares role-specific OpenSearch services and guest configuration. It does
not deploy them or change the active application search path. Tasks 7 and 8 own the
only application cutover. Task 12 provisions an empty OpenSearch index, deploys that
cutover, rebuilds from FoundationDB, verifies public search, and removes the unused
Meilisearch deployment. No step writes to both engines.

## Review Focus

Test normal coordinator distribution, loss of each guest, model availability after
restart, verified TLS, IPv6-only connectivity, and concurrent rebuilding within the
guest memory and disk limits.

---

## Task 11: Define containers and prepare the six guests

Tack changes:

```text
docker-compose.yml                                OpenSearch service definition
internal/ops/search_provision.go                   audited model/index provisioning
internal/ops/search_verify.go                      deployed search verification
internal/test/integration/search_cluster_test.go   real three-node local cluster
```

Configs changes, relative to its repository root:

```text
ansible/inventory/group_vars/all/service_mapping.yml guest identities and groups
ansible/inventory/group_vars/tack_search.yml          search guest settings
ansible/inventory/group_vars/tack_prod_all.yml        production endpoints
ansible/inventory/group_vars/tack_qa_all.yml          QA endpoints
ansible/playbooks/deploy-tack.yml                    role-specific service startup
opentofu/vault/tack_search.tf                        production LXCs
opentofu/suburban/tack_search_qa.tf                   QA LXCs
tack/tack.env.j2                                    application search environment
tack/docker-compose.override.yml.j2                 guest-specific container settings
spec/ansible/tack_search_spec.rb                     real template rendering
```

Consume the native task's verified configuration and measured artifact checksums.
Produce the six inventory entries, three HTTPS application endpoints per
environment, and separate provisioning credentials. `ops search provision` and
`ops search verify` register through clispec and its existing audit policy. Add one `searchOpsGroup` under `opsGroup` in `internal/ops/cli_search.go`. Define two `clispec.Operation[noInput]` values and register them through `RegisterCommands`. Do not add legacy operation registration. Implement `runSearchProvision(ctx context.Context, env *Env) error` and `runSearchVerify(ctx context.Context, env *Env) error` in the new ops files.

- [ ] Add a configs render test using the existing `AnsibleRender.render` runner. Render the real override for a search guest and an application guest. Parse the resulting YAML and assert that the search guest starts OpenSearch with persistent storage, while the application receives all three HTTPS endpoints and no provisioning credential.
- [ ] Add the role-specific OpenSearch service and application endpoints. Keep this
  preparation inactive. Tasks 7 and 8 delete the Meilisearch application dependency,
  endpoint, and Tack service during the single cutover. Task 12 removes the remaining
  deployed secret, volume, inventory, and template values after public verification.
- [ ] Run `bundle exec rspec spec/ansible/tack_search_spec.rb`. Expect failure before the inventory and templates define search guests.
- [ ] Allocate six distinct guest IDs, addresses, pinned MACs, and Docker IPv6 subnets in service_mapping. Check the entire mapping and live guest inventory before reserving them. The existing `tack_data1/2/3` entries are ledger guests and must remain separate. Use production keys `tack_search1/2/3` and QA keys with `_suburban`; use QA VMIDs equal to their production counterpart plus 100 where the verified inventory permits it.
- [ ] Add the LXC resources using mapping-derived identities. Match the existing production and QA bridge, gateway, DNS, Debian template, unprivileged nesting, discard, and prevent_destroy settings. Set memory to at least 8192 MiB, cores to 2, and disk size to 40 GiB. Do not provision a fourth permanent search guest.
- [ ] Render the following container settings from environment-specific inventory. Supply the three actual node names to discovery and initial cluster bootstrap. Use the bootstrap setting only when forming a new cluster, not when restarting or joining an existing one.

```yaml
services:
  opensearch:
    image: opensearchproject/opensearch:3.8.0
    environment:
      OPENSEARCH_JAVA_OPTS: -Xms2g -Xmx2g
      node.roles: cluster_manager,data,ingest,ml
    volumes:
      - opensearch-data:/usr/share/opensearch/data
    ulimits:
      nofile:
        soft: 65536
        hard: 65536
```

This fragment is the resource configuration. Add real inventory-derived discovery,
REST and transport certificate mounts, trust settings, and persistent volume
ownership in the existing templates. Reject disabled certificate verification.
Apply required host kernel settings through configs, including the OpenSearch
memory-map prerequisite; validate them inside the LXC before container startup.

- [ ] Restrict application credentials to the typed index, bulk, query, point-in-time, document, health, and local inference operations used by the official client. Provisioning credentials create models, semantic mappings, and aliases. Test monitoring and discovery permissions required by client routing and metrics. Verify denied administrative calls with the application identity. Use secret references and Ansible no_log for secret-bearing tasks.
- [ ] Ensure replicas cannot share a guest with their primary. Each guest already has the `ml` role. Deploy the pinned model without `node_ids` so ML Commons selects every eligible ML node. Require the deployment task to reach `DEPLOYED` on all three nodes. Keep native automatic redeployment enabled and verify the worker-node list after each restart. Store the chosen primary-shard count with each physical index. Tack must not select a guest for each node.
- [ ] Add a three-node local integration test using real containers and the production official client. Record selected connections through its test-only `ConnectionObserver`. During a fixed query run, require every healthy node to accept work and require that one node does not remain the sole coordinator. Do not require exact round-robin counts. Stop each node in turn through the Docker SDK. The client must recover and search must succeed; a newly written small node must become searchable within 10 seconds. Restart each node and repeat. Deny model-download network access after provisioning and require ordinary inference to keep working.
- [ ] Run the render tests, `tofu validate` in both OpenTofu directories, the local cluster test, and repository checks. Review a saved OpenTofu plan for exactly the intended six additions and no unrelated replacement or deletion. Commit Tack with subject `Provision and verify the OpenSearch container cluster`; commit configs with subject `Add QA and production Tack search guests`.
- [ ] Do not preserve or migrate the old Meilisearch volume. Its deletion is a
  separate destructive deployment action. Request authorization after the empty
  OpenSearch index has rebuilt from FoundationDB and public search checks pass.
  Do not define a Meilisearch rollback path.

## Task 12: Verify QA and production after authorization

Consume the reviewed commits, successful native coverage report, and saved
provisioning plan. Produce deployment evidence with revisions, image digests,
model/tokenizer checksums, TLS identities, topology, and acceptance measurements.

- [ ] Present the concrete guest additions and deployment commits for authorization before applying them. Never disable a branch rule or rewrite shared history to publish these changes.
- [ ] Apply the approved QA provisioning plan to the three search guests. Create an
  empty OpenSearch index. Do not read or convert Meilisearch data.
- [ ] Deploy the reviewed application cutover through the existing entry point:

```sh
./configsctl deploy deploy-tack --limit tack_qa_all
```

- [ ] Generate and review the complete QA search-projection manifest. Run the
  expiring backfill in dry-run mode, execute that exact manifest, rerun it, and
  require zero changes and zero missing declarations before rebuilding.
- [ ] Keep public search unavailable until the FoundationDB rebuild activates the
  verified alias. Run audited reindexing, verification, and guarded QA datagen.
  Verify three independent LXCs, actual resources, IPv6-only REST and transport
  connectivity, valid TLS, model identity, and all primary and replica placements.
- [ ] Verify the QA application process, containers, volumes, environment, secrets,
  and inventory contain no Meilisearch resource. After search passes, present the
  exact old volume deletion for separate authorization.
- [ ] Stop each QA search guest separately and repeat public search and node-write checks. Require inference and all primaries to remain available. These guests share a hypervisor; this test does not claim hypervisor fault tolerance.
- [ ] Before the capacity run, record corpus size, page distribution, query mix,
  concurrency, mutation rate, rebuild activity, and pass thresholds for latency,
  errors, throughput, oldest work age, memory, and disk. Run the exact workload with
  all guests and with each guest stopped. Require disk for serving, replacement,
  and retiring indexes. Do not infer capacity from the model bundle size.
- [ ] Add one temporary QA search guest and verify native shard redistribution.
  Rebuild the same corpus with a higher primary-shard count and repeat the fixed
  workload. Require improved throughput without a Tack routing change. Remove the
  guest only after its shards relocate. Keep three guests in the release topology.
- [ ] Require all first-release acceptance checks, including multi-page behavior under today's FDB limit. Tests beyond that limit remain mandatory for the later storage change, not a reason to defer current multi-page coverage.
- [ ] After QA passes and production deployment is authorized, use the existing production entry point:

```sh
./configsctl deploy deploy-tack --limit tack_prod_all
```

- [ ] Repeat the manifest dry run, reviewed backfill, idempotent rerun, and
  requirement for zero missing declarations against production before its first
  OpenSearch rebuild.
- [ ] Verify deployed revisions, image/model identity, TLS, topology, and authorized smoke fixtures. Record provisioning, deployment, and live verification separately. Do not report the implementation tickets complete merely because source tests passed.
- [ ] Verify the production application process, containers, volumes, environment,
  secrets, and inventory contain no Meilisearch resource. Delete an old volume only
  after its exact destructive action has separate authorization.
